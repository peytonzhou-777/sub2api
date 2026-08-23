package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/shopspring/decimal"
)

const (
	securityDepositBonusScanInterval = time.Minute
	securityDepositBonusBatchLimit   = 200
	securityDepositBonusMaxAttempts  = 3
	securityDepositBonusAdvisoryLock = int64(24020260823)
)

type securityDepositBonusBatch struct {
	ID           int64
	BusinessDate time.Time
	StartedAt    time.Time
	ExpiresAt    time.Time
	DailyAmount  decimal.Decimal
	CapRatio     decimal.Decimal
	Status       string
}

type securityDepositBonusBatchItem struct {
	ID                    int64
	BatchID               int64
	UserID                int64
	EffectiveBalanceCents int64
	CapAmount             decimal.Decimal
}

// SecurityDepositBonusRunner 按北京时间自然日创建资格快照并滚动发放保证金赠额。
type SecurityDepositBonusRunner struct {
	service *SecurityDepositBonusService
	db      *sql.DB
	stopCh  chan struct{}
	doneCh  chan struct{}
	once    sync.Once
}

// NewSecurityDepositBonusRunner 创建保证金赠额日任务执行器。
func NewSecurityDepositBonusRunner(service *SecurityDepositBonusService, db *sql.DB) *SecurityDepositBonusRunner {
	return &SecurityDepositBonusRunner{service: service, db: db, stopCh: make(chan struct{}), doneCh: make(chan struct{})}
}

// Start 启动后立即补处理当日批次，之后每分钟继续扫描。
func (r *SecurityDepositBonusRunner) Start() {
	if r == nil || r.db == nil || r.service == nil {
		return
	}
	r.once.Do(func() {
		go func() {
			defer close(r.doneCh)
			r.scan()
			ticker := time.NewTicker(securityDepositBonusScanInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					r.scan()
				case <-r.stopCh:
					return
				}
			}
		}()
	})
}

// Stop 停止新扫描并等待当前扫描退出。
func (r *SecurityDepositBonusRunner) Stop() {
	if r == nil {
		return
	}
	select {
	case <-r.stopCh:
	default:
		close(r.stopCh)
	}
	select {
	case <-r.doneCh:
	case <-time.After(5 * time.Second):
	}
}

func (r *SecurityDepositBonusRunner) scan() {
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	batch, err := r.ensureTodayBatch(ctx)
	if err != nil {
		logger.LegacyPrintf("service.security_deposit_bonus_runner", "[SecurityDepositBonusRunner] ensure batch failed: %v", err)
		return
	}
	if batch == nil || batch.Status != "running" {
		return
	}
	for i := 0; i < securityDepositBonusBatchLimit; i++ {
		processed, processErr := r.processOne(ctx, batch)
		if processErr != nil {
			logger.LegacyPrintf("service.security_deposit_bonus_runner", "[SecurityDepositBonusRunner] process batch=%d failed: %v", batch.ID, processErr)
			return
		}
		if !processed {
			break
		}
	}
	if err := r.finishBatchIfComplete(ctx, batch.ID); err != nil {
		logger.LegacyPrintf("service.security_deposit_bonus_runner", "[SecurityDepositBonusRunner] finish batch=%d failed: %v", batch.ID, err)
	}
}

func (r *SecurityDepositBonusRunner) ensureTodayBatch(ctx context.Context) (*securityDepositBonusBatch, error) {
	policy, err := r.service.settings.GetSecurityDepositPolicyConfigStrict(ctx)
	if err != nil {
		return nil, err
	}
	location := securityDepositBonusLocation()
	now := r.service.now().In(location)
	businessDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
	expiresAt := businessDate.AddDate(0, 0, securityDepositBonusValidityDays)
	daily := decimal.NewFromFloat(policy.BonusDailyAmount).Round(8)
	ratio := decimal.NewFromFloat(policy.BonusCapRatio).Round(8)
	// 多实例先通过事务 advisory lock 串行建批；读已提交可在等待锁后看到其他实例刚提交的批次。
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, securityDepositBonusAdvisoryLock); err != nil {
		return nil, err
	}
	batch, err := loadSecurityDepositBonusBatchByDate(ctx, tx, businessDate)
	if err != nil {
		return nil, err
	}
	if batch != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return batch, nil
	}
	status := "running"
	if !policy.EnforcementEnabled || !daily.IsPositive() {
		status = "skipped"
	}
	batch = &securityDepositBonusBatch{
		BusinessDate: businessDate, StartedAt: now.UTC(), ExpiresAt: expiresAt.UTC(),
		DailyAmount: daily, CapRatio: ratio, Status: status,
	}
	var finishedAt any
	if status == "skipped" {
		finishedAt = now.UTC()
	}
	err = tx.QueryRowContext(ctx, `
INSERT INTO security_deposit_bonus_batches(
    business_date,scheduled_at,started_at,expires_at,daily_amount,cap_ratio,enforcement_enabled,status,finished_at
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
RETURNING id`, businessDate.Format("2006-01-02"), businessDate.UTC(), now.UTC(), expiresAt.UTC(), daily, ratio,
		policy.EnforcementEnabled, status, finishedAt).Scan(&batch.ID)
	if err != nil {
		return nil, err
	}
	if status == "running" {
		if err := snapshotSecurityDepositBonusEligibility(ctx, tx, batch); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `
UPDATE security_deposit_bonus_batches
SET eligible_user_count=(SELECT COUNT(*) FROM security_deposit_bonus_batch_items WHERE batch_id=$1),updated_at=NOW()
WHERE id=$1`, batch.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return batch, nil
}

func loadSecurityDepositBonusBatchByDate(ctx context.Context, tx *sql.Tx, businessDate time.Time) (*securityDepositBonusBatch, error) {
	batch := &securityDepositBonusBatch{}
	err := tx.QueryRowContext(ctx, `
SELECT id,business_date,started_at,expires_at,daily_amount,cap_ratio,status
FROM security_deposit_bonus_batches
WHERE business_date=$1`, businessDate.Format("2006-01-02")).Scan(
		&batch.ID, &batch.BusinessDate, &batch.StartedAt, &batch.ExpiresAt, &batch.DailyAmount, &batch.CapRatio, &batch.Status,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return batch, err
}

func snapshotSecurityDepositBonusEligibility(ctx context.Context, tx *sql.Tx, batch *securityDepositBonusBatch) error {
	_, err := tx.ExecContext(ctx, `
WITH balances AS (
    SELECT user_id, SUM(balance_cents-refund_reserved_cents)::bigint AS effective_balance_cents
    FROM security_deposit_accounts
    GROUP BY user_id
    HAVING SUM(balance_cents-refund_reserved_cents) > 0
), profiles AS (
    SELECT balances.user_id,balances.effective_balance_cents,
           COALESCE(profile.risk_multiplier,1)::bigint AS risk_multiplier
    FROM balances
    LEFT JOIN security_deposit_risk_profiles profile ON profile.user_id=balances.user_id
)
INSERT INTO security_deposit_bonus_batch_items(
    batch_id,user_id,effective_balance_cents,risk_multiplier,
    qualifying_group_id,qualifying_group_name,required_cents,cap_amount
)
SELECT $1,profile.user_id,profile.effective_balance_cents,profile.risk_multiplier,
       qualified.id,qualified.name,qualified.required_cents,
       ROUND(profile.effective_balance_cents::numeric * $2::numeric / 10000,8)
FROM profiles profile
JOIN LATERAL (
    SELECT groups.id,groups.name,
           (groups.security_deposit_base_required_cents::numeric * profile.risk_multiplier::numeric)::bigint AS required_cents
    FROM groups
    WHERE groups.deleted_at IS NULL
      AND groups.status='active'
      AND groups.security_deposit_base_required_cents > 0
      AND (
          (
              groups.subscription_type='subscription'
              AND EXISTS(
                  SELECT 1 FROM user_subscriptions subscription
                  WHERE subscription.user_id=profile.user_id
                    AND subscription.group_id=groups.id
                    AND subscription.status='active'
                    AND subscription.deleted_at IS NULL
                    AND subscription.expires_at > $3
              )
          )
          OR
          (
              groups.subscription_type<>'subscription'
              AND (
                  NOT groups.is_exclusive
                  OR EXISTS(
                      SELECT 1 FROM user_allowed_groups allowed_group
                      WHERE allowed_group.user_id=profile.user_id AND allowed_group.group_id=groups.id
                  )
              )
          )
      )
      AND groups.security_deposit_base_required_cents::numeric * profile.risk_multiplier::numeric
          <= profile.effective_balance_cents::numeric
    ORDER BY required_cents,groups.id
    LIMIT 1
) qualified ON TRUE`, batch.ID, batch.CapRatio, batch.StartedAt)
	return err
}

func (r *SecurityDepositBonusRunner) processOne(ctx context.Context, batch *securityDepositBonusBatch) (bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `
UPDATE security_deposit_bonus_batch_items
SET result='failed',failure_message='processing_interrupted_after_max_attempts',
    processing_started_at=NULL,processed_at=NOW(),updated_at=NOW()
WHERE batch_id=$1 AND result='processing' AND attempt_count >= $2
  AND processing_started_at < NOW()-INTERVAL '5 minutes'`, batch.ID, securityDepositBonusMaxAttempts); err != nil {
		return false, err
	}
	item := &securityDepositBonusBatchItem{}
	err = tx.QueryRowContext(ctx, `
SELECT id,batch_id,user_id,effective_balance_cents,cap_amount
FROM security_deposit_bonus_batch_items
WHERE batch_id=$1
  AND (result='pending' OR (result='processing' AND processing_started_at < NOW()-INTERVAL '5 minutes'))
  AND attempt_count < $2
ORDER BY id
FOR UPDATE SKIP LOCKED
LIMIT 1`, batch.ID, securityDepositBonusMaxAttempts).Scan(
		&item.ID, &item.BatchID, &item.UserID, &item.EffectiveBalanceCents, &item.CapAmount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	var attemptCount int
	if err = tx.QueryRowContext(ctx, `
UPDATE security_deposit_bonus_batch_items
SET result='processing',attempt_count=attempt_count+1,processing_started_at=NOW(),updated_at=NOW()
WHERE id=$1
RETURNING attempt_count`, item.ID).Scan(&attemptCount); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `SAVEPOINT security_deposit_bonus_item`); err != nil {
		return false, err
	}
	// 单用户执行失败时回滚其资金变更，但持久化重试次数，避免整个日批次永久卡住。
	failItem := func(cause error) (bool, error) {
		if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT security_deposit_bonus_item`); rollbackErr != nil {
			return false, errors.Join(cause, rollbackErr)
		}
		result := "pending"
		var processedAt any
		if attemptCount >= securityDepositBonusMaxAttempts {
			result = "failed"
			processedAt = r.service.now().UTC()
		}
		if _, updateErr := tx.ExecContext(ctx, `
UPDATE security_deposit_bonus_batch_items
SET result=$2,failure_message=$3,processing_started_at=NULL,processed_at=$4,updated_at=NOW()
WHERE id=$1`, item.ID, result, cause.Error(), processedAt); updateErr != nil {
			return false, errors.Join(cause, updateErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return false, errors.Join(cause, commitErr)
		}
		return true, nil
	}
	if err = lockSecurityDepositBonusUser(ctx, tx, item.UserID); err != nil {
		return failItem(err)
	}
	var currentBalance int64
	if err = tx.QueryRowContext(ctx, `
SELECT COALESCE(SUM(balance_cents-refund_reserved_cents),0)::bigint
FROM security_deposit_accounts
WHERE user_id=$1`, item.UserID).Scan(&currentBalance); err != nil {
		return failItem(err)
	}
	if currentBalance < item.EffectiveBalanceCents {
		if _, err = tx.ExecContext(ctx, `
UPDATE security_deposit_bonus_batch_items
SET result='skipped',failure_message='balance_decreased_after_snapshot',processed_at=NOW(),updated_at=NOW()
WHERE id=$1`, item.ID); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	grantID, before, added, after, err := applySecurityDepositBonusGrant(ctx, tx, batch, item)
	if err != nil {
		return failItem(err)
	}
	result := "renewed"
	failureMessage := ""
	if grantID == nil {
		result = "skipped"
		failureMessage = "bonus_cap_zero"
	} else if added.IsPositive() {
		result = "issued"
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE security_deposit_bonus_batch_items
SET result=$2,grant_id=$3,amount_before=$4,granted_amount=$5,amount_after=$6,
    failure_message=$7,processed_at=NOW(),updated_at=NOW()
WHERE id=$1`, item.ID, result, grantID, before, added, after, failureMessage); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	r.service.invalidateUser(ctx, item.UserID)
	return true, nil
}

func applySecurityDepositBonusGrant(ctx context.Context, tx *sql.Tx, batch *securityDepositBonusBatch, item *securityDepositBonusBatchItem) (*int64, decimal.Decimal, decimal.Decimal, decimal.Decimal, error) {
	var grantID int64
	var initial, used, frozen, pending decimal.Decimal
	var expiresAt time.Time
	var status string
	err := tx.QueryRowContext(ctx, `
SELECT id,initial_amount,used_amount,frozen_amount,security_deposit_bonus_pending_revoke_amount,expires_at,status
FROM user_limited_credit_grants
WHERE user_id=$1 AND source_type=$2
FOR UPDATE`, item.UserID, LimitedCreditSourceSecurityDepositBonus).Scan(
		&grantID, &initial, &used, &frozen, &pending, &expiresAt, &status,
	)
	if errors.Is(err, sql.ErrNoRows) {
		added := decimal.Min(batch.DailyAmount, item.CapAmount).Round(8)
		if !added.IsPositive() {
			return nil, decimal.Zero, decimal.Zero, decimal.Zero, nil
		}
		err = tx.QueryRowContext(ctx, `
INSERT INTO user_limited_credit_grants(user_id,source_type,initial_amount,used_amount,frozen_amount,expires_at,status,notes)
VALUES($1,$2,$3,0,0,$4,$5,$6)
RETURNING id`, item.UserID, LimitedCreditSourceSecurityDepositBonus, added, batch.ExpiresAt,
			LimitedCreditStatusActive, "保证金每日赠额").Scan(&grantID)
		if err != nil {
			return nil, decimal.Zero, decimal.Zero, decimal.Zero, err
		}
		if err = insertSecurityDepositBonusLedger(ctx, tx, item.UserID, grantID, batch, added, "security_deposit_bonus_grant"); err != nil {
			return nil, decimal.Zero, decimal.Zero, decimal.Zero, err
		}
		return &grantID, decimal.Zero, added, added, nil
	}
	if err != nil {
		return nil, decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	remaining := decimal.Max(initial.Sub(used), decimal.Zero).Round(8)
	if status != LimitedCreditStatusActive || !expiresAt.After(batch.StartedAt) {
		remaining = frozen
	}
	headroom := item.CapAmount.Sub(remaining)
	added := decimal.Zero
	if headroom.IsPositive() {
		added = decimal.Min(batch.DailyAmount, headroom).Round(8)
	}
	after := remaining.Add(added).Round(8)
	if !after.IsPositive() {
		return nil, decimal.Zero, decimal.Zero, decimal.Zero, nil
	}
	if _, err = tx.ExecContext(ctx, `
UPDATE user_limited_credit_grants
SET initial_amount=$1,used_amount=0,expires_at=$2,status=$3,notes=$4,
    security_deposit_bonus_pending_revoke_amount=LEAST($5,frozen_amount),updated_at=NOW()
WHERE id=$6`, after, batch.ExpiresAt, LimitedCreditStatusActive, "保证金每日赠额", pending, grantID); err != nil {
		return nil, decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	eventType := "security_deposit_bonus_renew"
	if added.IsPositive() {
		eventType = "security_deposit_bonus_grant"
	}
	if err = insertSecurityDepositBonusLedger(ctx, tx, item.UserID, grantID, batch, added, eventType); err != nil {
		return nil, decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	return &grantID, remaining, added, after, nil
}

func insertSecurityDepositBonusLedger(ctx context.Context, tx *sql.Tx, userID, grantID int64, batch *securityDepositBonusBatch, amount decimal.Decimal, eventType string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO user_limited_credit_ledger(user_id,grant_id,event_type,amount,batch_id,notes)
VALUES($1,$2,$3,$4,$5,$6)`, userID, grantID, eventType, amount,
		fmt.Sprintf("security-deposit-bonus:%s", batch.BusinessDate.In(securityDepositBonusLocation()).Format("2006-01-02")),
		"保证金赠额日批次")
	return err
}

func (r *SecurityDepositBonusRunner) finishBatchIfComplete(ctx context.Context, batchID int64) error {
	var pending, failed int
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FILTER (WHERE result IN ('pending','processing')),
       COUNT(*) FILTER (WHERE result='failed' OR (result='processing' AND attempt_count >= $2))
FROM security_deposit_bonus_batch_items
WHERE batch_id=$1`, batchID, securityDepositBonusMaxAttempts).Scan(&pending, &failed); err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	status := "succeeded"
	if failed > 0 {
		status = "failed"
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE security_deposit_bonus_batches
SET status=$2,
    issued_user_count=(SELECT COUNT(*) FROM security_deposit_bonus_batch_items WHERE batch_id=$1 AND result='issued'),
    renewed_user_count=(SELECT COUNT(*) FROM security_deposit_bonus_batch_items WHERE batch_id=$1 AND result='renewed'),
    failed_user_count=$3,
    issued_amount=(SELECT COALESCE(SUM(granted_amount),0) FROM security_deposit_bonus_batch_items WHERE batch_id=$1),
    finished_at=NOW(),updated_at=NOW()
WHERE id=$1 AND status='running'`, batchID, status, failed)
	return err
}
