package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/shopspring/decimal"
)

const (
	recurringCreditScanInterval = time.Minute
	recurringCreditStartGrace   = 10 * time.Minute
	recurringCreditLease        = 2 * time.Minute
	recurringCreditHeartbeat    = 30 * time.Second
	recurringCreditMaxDuration  = 30 * time.Minute
	recurringCreditMaxAttempts  = 3
)

type recurringClaim struct {
	BatchID int64
	Owner   string
}

type recurringCandidate struct {
	userID                  int64
	itemID                  int64
	email, username, status string
	deleted                 bool
	actualCost, netRecharge float64
}

// recurringCreditBatchInsertSQL 创建批次并在跳过/错过时写入完成时间。
// status 与 finished_at 判断使用独立参数，避免 PostgreSQL 重复参数类型推断冲突。
const recurringCreditBatchInsertSQL = `INSERT INTO recurring_credit_batches(task_id,task_name,scheduled_at,expires_at,qualification_start,qualification_end,qualification_cutoff_at,config_version,eligibility_policy,validity_days,schedule_type,day_of_month,day_of_week,local_time,timezone,amount,execution_mode,status,claimed_at,lease_owner,lease_expires_at,heartbeat_at,attempt_count,finished_at)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$19,$22,CASE WHEN $23 IN ('skipped','missed') THEN NOW() ELSE NULL END)
	ON CONFLICT(task_id,scheduled_at) DO NOTHING RETURNING id`

// RecurringCreditRunner 使用数据库任务状态驱动循环赠额执行。
type RecurringCreditRunner struct {
	service *RecurringCreditService
	db      *sql.DB
	owner   string
	stopCh  chan struct{}
	doneCh  chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// NewRecurringCreditRunner 创建循环赠额执行器，Start 由 Wire provider 调用。
func NewRecurringCreditRunner(service *RecurringCreditService, db *sql.DB) *RecurringCreditRunner {
	host, _ := os.Hostname()
	return &RecurringCreditRunner{service: service, db: db, owner: fmt.Sprintf("%s-%d-%d", host, os.Getpid(), time.Now().UnixNano()), stopCh: make(chan struct{}), doneCh: make(chan struct{})}
}

// Start 启动后立即扫描，之后每分钟扫描到期任务和失效租约。
func (r *RecurringCreditRunner) Start() {
	if r == nil || r.db == nil {
		return
	}
	r.once.Do(func() {
		go func() {
			defer close(r.doneCh)
			r.scan()
			ticker := time.NewTicker(recurringCreditScanInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					r.scan()
				case <-r.service.wakeCh:
					r.scan()
				case <-r.stopCh:
					return
				}
			}
		}()
	})
}

// Stop 停止新扫描，并等待当前进程已领取的批次结束。
func (r *RecurringCreditRunner) Stop() {
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
	case <-time.After(2 * time.Second):
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
	}
}

func (r *RecurringCreditRunner) scan() {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	r.failExpiredRunning(ctx)
	r.takeOverStale(ctx)
	for i := 0; i < 500; i++ {
		claim, processed, err := r.claimOneDue(ctx)
		if err != nil {
			logger.LegacyPrintf("service.recurring_credit_runner", "[RecurringCreditRunner] claim error: %v", err)
			return
		}
		if !processed {
			return
		}
		if claim != nil {
			r.startClaim(*claim)
		}
	}
}

func (r *RecurringCreditRunner) startClaim(claim recurringClaim) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.runClaim(claim)
	}()
}

func (r *RecurringCreditRunner) claimOneDue(ctx context.Context) (*recurringClaim, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	var now time.Time
	err = tx.QueryRowContext(ctx, `SELECT id,clock_timestamp() FROM recurring_credit_tasks WHERE status='active' AND next_run_at <= NOW() ORDER BY next_run_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &now)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	task, err := queryTaskTx(ctx, tx, id, false)
	if err != nil {
		return nil, false, err
	}
	if task.NextRunAt == nil {
		return nil, false, nil
	}
	scheduled := task.NextRunAt.UTC()
	input := taskViewInput(task)
	now = now.UTC()
	var expires, windowStart, windowEnd time.Time
	var next any
	status := "running"
	windowStart, windowEnd = recurringCreditActivityWindow(now)
	if task.ScheduleType == RecurringCreditImmediate {
		if task.ValidityDays == nil {
			return nil, false, fmt.Errorf("immediate task %d is missing validity_days", task.ID)
		}
		expires = now.AddDate(0, 0, *task.ValidityDays)
	} else {
		expires, err = nextRecurringOccurrence(input, scheduled)
		if err != nil {
			return nil, false, err
		}
		next = expires
		if task.SkipCount > 0 {
			status = "skipped"
		} else if now.After(scheduled.Add(recurringCreditStartGrace)) {
			status = "missed"
		}
	}
	var batchID int64
	var cutoff any
	var claimed any
	var lease any
	attempts := 0
	owner := ""
	if status == "running" {
		cutoff, claimed, lease, attempts, owner = now, now, now.Add(recurringCreditLease), 1, r.owner
	}
	err = tx.QueryRowContext(ctx, recurringCreditBatchInsertSQL, task.ID, task.Name, scheduled, expires, windowStart, windowEnd, cutoff, task.Version, RecurringCreditEligibilityRollingActivity, task.ValidityDays, task.ScheduleType, task.DayOfMonth, task.DayOfWeek, task.LocalTime, task.Timezone, task.Amount, task.ExecutionMode, status, claimed, owner, lease, attempts, status).Scan(&batchID)
	if err == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET next_run_at=$2,version=version+1,updated_at=NOW() WHERE id=$1`, id, next)
		if err == nil {
			err = tx.Commit()
		}
		return nil, true, err
	}
	if err != nil {
		return nil, false, err
	}
	if status == "running" {
		if _, err = tx.ExecContext(ctx, rollingActivitySnapshotSQL, now, batchID); err != nil {
			return nil, false, err
		}
		var eligibleCount, apiCount, siteCount, bothCount int
		err = tx.QueryRowContext(ctx, `SELECT COUNT(*),
			COUNT(*) FILTER (WHERE api_last_used_at IS NOT NULL),
			COUNT(*) FILTER (WHERE site_last_active_at IS NOT NULL),
			COUNT(*) FILTER (WHERE api_last_used_at IS NOT NULL AND site_last_active_at IS NOT NULL)
			FROM recurring_credit_user_items WHERE batch_id=$1`, batchID).Scan(&eligibleCount, &apiCount, &siteCount, &bothCount)
		if err != nil {
			return nil, false, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_batches SET eligible_user_count=$2,api_active_count=$3,site_active_count=$4,both_active_count=$5,snapshot_completed_at=$6,updated_at=NOW() WHERE id=$1`, batchID, eligibleCount, apiCount, siteCount, bothCount, now)
		if err != nil {
			return nil, false, err
		}
	}
	if status == "skipped" {
		_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET skip_count=skip_count-1,next_run_at=$2,version=version+1,updated_at=NOW() WHERE id=$1`, id, next)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET next_run_at=$2,version=version+1,updated_at=NOW() WHERE id=$1`, id, next)
	}
	if err != nil {
		return nil, false, err
	}
	if err = tx.Commit(); err != nil {
		return nil, false, err
	}
	if status != "running" {
		logger.LegacyPrintf("service.recurring_credit_runner", "[RecurringCreditRunner] task=%d scheduled=%s status=%s", task.ID, scheduled.Format(time.RFC3339), status)
		return nil, true, nil
	}
	logger.LegacyPrintf("service.recurring_credit_runner", "[RecurringCreditRunner] claimed task=%d batch=%d scheduled=%s", task.ID, batchID, scheduled.Format(time.RFC3339))
	return &recurringClaim{BatchID: batchID, Owner: r.owner}, true, nil
}

func (r *RecurringCreditRunner) takeOverStale(ctx context.Context) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM recurring_credit_batches WHERE status='running' AND lease_expires_at < NOW() AND claimed_at >= NOW()-INTERVAL '30 minutes' AND attempt_count < $1 ORDER BY lease_expires_at LIMIT 50`, recurringCreditMaxAttempts)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	_ = rows.Close()
	for _, id := range ids {
		result, execErr := r.db.ExecContext(ctx, `UPDATE recurring_credit_batches SET lease_owner=$2,lease_expires_at=NOW()+INTERVAL '2 minutes',heartbeat_at=NOW(),attempt_count=attempt_count+1,updated_at=NOW() WHERE id=$1 AND status='running' AND lease_expires_at < NOW() AND attempt_count < $3`, id, r.owner, recurringCreditMaxAttempts)
		if execErr == nil {
			if n, _ := result.RowsAffected(); n == 1 {
				logger.LegacyPrintf("service.recurring_credit_runner", "[RecurringCreditRunner] takeover batch=%d", id)
				r.startClaim(recurringClaim{BatchID: id, Owner: r.owner})
			}
		}
	}
}

func (r *RecurringCreditRunner) failExpiredRunning(ctx context.Context) {
	_, _ = r.db.ExecContext(ctx, `WITH failed AS (
		UPDATE recurring_credit_batches SET status='failed',failure_code='EXECUTION_TIMEOUT',failure_message='execution exceeded 30 minutes or automatic attempts were exhausted',finished_at=NOW(),lease_expires_at=NULL,updated_at=NOW() WHERE status='running' AND (claimed_at < NOW()-INTERVAL '30 minutes' OR (attempt_count >= $1 AND lease_expires_at < NOW())) RETURNING task_id,schedule_type
	) UPDATE recurring_credit_tasks SET status='completed',remaining_runs=0,next_run_at=NULL,version=version+1,updated_at=NOW() WHERE id IN (SELECT task_id FROM failed WHERE schedule_type='immediate') AND status<>'deleted'`, recurringCreditMaxAttempts)
}

func (r *RecurringCreditRunner) runClaim(claim recurringClaim) {
	ctx, cancel := context.WithTimeout(context.Background(), recurringCreditMaxDuration)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go r.heartbeat(ctx, claim, heartbeatDone)
	defer close(heartbeatDone)
	for {
		userIDs, err := r.executeBatch(ctx, claim)
		if err == nil {
			logger.LegacyPrintf("service.recurring_credit_runner", "[RecurringCreditRunner] completed batch=%d users=%d", claim.BatchID, len(userIDs))
			go r.invalidateUsers(userIDs)
			return
		}
		if ctx.Err() != nil {
			r.markFailed(claim, "EXECUTION_TIMEOUT", ctx.Err().Error())
			return
		}
		var attempts int
		errCount := r.db.QueryRowContext(ctx, `UPDATE recurring_credit_batches SET attempt_count=attempt_count+1,lease_expires_at=NOW()+INTERVAL '2 minutes',heartbeat_at=NOW(),failure_code='RETRYABLE_ERROR',failure_message=$3,updated_at=NOW() WHERE id=$1 AND status='running' AND lease_owner=$2 AND attempt_count < $4 RETURNING attempt_count`, claim.BatchID, claim.Owner, truncateRecurringError(err), recurringCreditMaxAttempts).Scan(&attempts)
		if errCount != nil {
			r.markFailed(claim, "EXECUTION_FAILED", truncateRecurringError(err))
			return
		}
	}
}

func (r *RecurringCreditRunner) heartbeat(ctx context.Context, claim recurringClaim, done <-chan struct{}) {
	ticker := time.NewTicker(recurringCreditHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_, _ = r.db.ExecContext(ctx, `UPDATE recurring_credit_batches SET heartbeat_at=NOW(),lease_expires_at=NOW()+INTERVAL '2 minutes',updated_at=NOW() WHERE id=$1 AND status='running' AND lease_owner=$2`, claim.BatchID, claim.Owner)
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (r *RecurringCreditRunner) executeBatch(ctx context.Context, claim recurringClaim) ([]int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, claim.BatchID); err != nil {
		return nil, err
	}
	var taskID int64
	var taskName, status, owner, batchMode, batchSchedule, eligibilityPolicy string
	var start, end, cutoff, expires time.Time
	var amount float64
	var snapshotEligibleCount int
	err = tx.QueryRowContext(ctx, `SELECT task_id,task_name,status,lease_owner,qualification_start,qualification_end,qualification_cutoff_at,expires_at,amount,execution_mode,schedule_type,eligibility_policy,eligible_user_count FROM recurring_credit_batches WHERE id=$1`, claim.BatchID).Scan(&taskID, &taskName, &status, &owner, &start, &end, &cutoff, &expires, &amount, &batchMode, &batchSchedule, &eligibilityPolicy, &snapshotEligibleCount)
	if err != nil {
		return nil, err
	}
	if status != "running" || owner != claim.Owner {
		return nil, fmt.Errorf("batch lease is no longer owned")
	}
	var rows *sql.Rows
	if eligibilityPolicy == RecurringCreditEligibilityRollingActivity {
		rows, err = tx.QueryContext(ctx, `SELECT i.id,i.user_id,i.email,i.username,COALESCE(u.status,''),(u.id IS NULL OR u.deleted_at IS NOT NULL),0::double precision,0::double precision
			FROM recurring_credit_user_items i LEFT JOIN users u ON u.id=i.user_id WHERE i.batch_id=$1 AND i.result='pending' ORDER BY i.user_id`, claim.BatchID)
	} else if batchSchedule == RecurringCreditImmediate {
		rows, err = tx.QueryContext(ctx, `SELECT id,COALESCE(email,''),COALESCE(username,''),COALESCE(status,''),FALSE,0::double precision,0::double precision
			FROM users WHERE created_at <= $1 AND (deleted_at IS NULL OR deleted_at > $1) ORDER BY id`, cutoff)
	} else {
		rows, err = tx.QueryContext(ctx, `WITH usage AS (
			SELECT user_id,SUM(actual_cost) actual_cost FROM usage_logs WHERE created_at >= $1 AND created_at < $2 AND created_at <= $3 GROUP BY user_id
		), recharge AS (
			SELECT user_id,SUM(GREATEST(amount-CASE WHEN refund_at IS NOT NULL AND refund_at <= $3 THEN refund_amount ELSE 0 END,0)) net_recharge
			FROM payment_orders WHERE order_type='balance' AND completed_at >= $1 AND completed_at < $2 AND completed_at <= $3 GROUP BY user_id
		), ids AS (SELECT user_id FROM usage WHERE actual_cost>0 UNION SELECT user_id FROM recharge WHERE net_recharge>0)
		SELECT ids.user_id,COALESCE(u.email,''),COALESCE(u.username,''),COALESCE(u.status,''),CASE WHEN u.id IS NULL THEN TRUE ELSE u.deleted_at IS NOT NULL AND u.deleted_at <= $3 END,COALESCE(usage.actual_cost,0),COALESCE(recharge.net_recharge,0)
		FROM ids LEFT JOIN users u ON u.id=ids.user_id LEFT JOIN usage ON usage.user_id=ids.user_id LEFT JOIN recharge ON recharge.user_id=ids.user_id ORDER BY ids.user_id`, start, end, cutoff)
	}
	if err != nil {
		return nil, err
	}
	candidates := make([]recurringCandidate, 0)
	for rows.Next() {
		var c recurringCandidate
		if eligibilityPolicy == RecurringCreditEligibilityRollingActivity {
			err = rows.Scan(&c.itemID, &c.userID, &c.email, &c.username, &c.status, &c.deleted, &c.actualCost, &c.netRecharge)
		} else {
			err = rows.Scan(&c.userID, &c.email, &c.username, &c.status, &c.deleted, &c.actualCost, &c.netRecharge)
		}
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	issued, excluded, usageCount, rechargeCount := 0, 0, 0, 0
	userIDs := make([]int64, 0, len(candidates))
	total := decimal.Zero
	for _, candidate := range candidates {
		reason := ""
		if eligibilityPolicy == RecurringCreditEligibilityLegacy {
			reason = "all_users"
			if batchSchedule != RecurringCreditImmediate {
				reason = "usage"
				if candidate.actualCost > 0 {
					usageCount++
				}
				if candidate.netRecharge > 0 {
					rechargeCount++
					if candidate.actualCost > 0 {
						reason = "usage_and_recharge"
					} else {
						reason = "recharge"
					}
				}
			}
		}
		if eligibilityPolicy == RecurringCreditEligibilityRollingActivity && (candidate.deleted || candidate.status != RecurringCreditStatusActive) {
			excluded++
			exclusionReason := "user_inactive"
			if candidate.deleted {
				exclusionReason = "user_deleted"
			}
			_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_user_items SET user_status=$2,user_deleted=$3,result='excluded',exclusion_reason=$4 WHERE id=$1 AND result='pending'`, candidate.itemID, candidate.status, candidate.deleted, exclusionReason)
			if err != nil {
				return nil, err
			}
			continue
		}
		if candidate.deleted {
			excluded++
			_, err = tx.ExecContext(ctx, `INSERT INTO recurring_credit_user_items(batch_id,user_id,email,username,user_status,user_deleted,actual_cost,net_recharge,qualification_reason,grant_amount,result,exclusion_reason) VALUES($1,$2,$3,$4,$5,TRUE,$6,$7,$8,0,'excluded','user_deleted')`, claim.BatchID, candidate.userID, candidate.email, candidate.username, candidate.status, candidate.actualCost, candidate.netRecharge, reason)
			if err != nil {
				return nil, err
			}
			continue
		}
		var grantID int64
		notes := fmt.Sprintf("赠额任务：%s", taskName)
		if eligibilityPolicy == RecurringCreditEligibilityRollingActivity {
			err = tx.QueryRowContext(ctx, `INSERT INTO user_limited_credit_grants(user_id,source_type,source_id,initial_amount,expires_at,status,notes)
				SELECT id,'recurring_grant',$2,$3,$4,'active',$5 FROM users WHERE id=$1 AND status='active' AND deleted_at IS NULL RETURNING id`, candidate.userID, claim.BatchID, amount, expires, notes).Scan(&grantID)
			if err == sql.ErrNoRows {
				excluded++
				_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_user_items SET result='excluded',exclusion_reason='user_state_changed_after_snapshot' WHERE id=$1 AND result='pending'`, candidate.itemID)
				if err != nil {
					return nil, err
				}
				continue
			}
		} else {
			err = tx.QueryRowContext(ctx, `INSERT INTO user_limited_credit_grants(user_id,source_type,source_id,initial_amount,expires_at,status,notes) VALUES($1,'recurring_grant',$2,$3,$4,'active',$5) RETURNING id`, candidate.userID, claim.BatchID, amount, expires, notes).Scan(&grantID)
		}
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO user_limited_credit_ledger(user_id,grant_id,event_type,amount,batch_id,notes) VALUES($1,$2,'grant',$3,$4,$5)`, candidate.userID, grantID, amount, fmt.Sprintf("recurring-grant-%d", claim.BatchID), notes)
		if err != nil {
			return nil, err
		}
		if eligibilityPolicy == RecurringCreditEligibilityRollingActivity {
			_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_user_items SET user_status=$2,user_deleted=FALSE,grant_amount=$3,grant_id=$4,result='issued',exclusion_reason='' WHERE id=$1 AND result='pending'`, candidate.itemID, candidate.status, amount, grantID)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO recurring_credit_user_items(batch_id,user_id,email,username,user_status,user_deleted,actual_cost,net_recharge,qualification_reason,grant_amount,grant_id,result) VALUES($1,$2,$3,$4,$5,FALSE,$6,$7,$8,$9,$10,'issued')`, claim.BatchID, candidate.userID, candidate.email, candidate.username, candidate.status, candidate.actualCost, candidate.netRecharge, reason, amount, grantID)
		}
		if err != nil {
			return nil, err
		}
		issued++
		userIDs = append(userIDs, candidate.userID)
		total = total.Add(decimal.NewFromFloat(amount))
	}
	batchStatus := "succeeded"
	if issued == 0 {
		batchStatus = "empty"
	}
	eligibleCount := issued
	if eligibilityPolicy == RecurringCreditEligibilityRollingActivity {
		eligibleCount = snapshotEligibleCount
	}
	result, err := tx.ExecContext(ctx, `UPDATE recurring_credit_batches SET status=$3,eligible_user_count=$4,issued_user_count=$5,excluded_user_count=$6,usage_eligible_count=$7,recharge_eligible_count=$8,issued_amount=$9,failure_code='',failure_message='',finished_at=NOW(),lease_expires_at=NULL,updated_at=NOW() WHERE id=$1 AND status='running' AND lease_owner=$2`, claim.BatchID, claim.Owner, batchStatus, eligibleCount, issued, excluded, usageCount, rechargeCount, total.InexactFloat64())
	if err != nil {
		return nil, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("batch lease changed before commit")
	}
	if batchMode == RecurringCreditModeFinite {
		_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET remaining_runs=remaining_runs-1,status=CASE WHEN remaining_runs=1 AND status<>'deleted' THEN 'completed' ELSE status END,next_run_at=CASE WHEN remaining_runs=1 THEN NULL ELSE next_run_at END,version=version+1,updated_at=NOW() WHERE id=$1 AND execution_mode='finite' AND remaining_runs>0`, taskID)
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return userIDs, nil
}

func (r *RecurringCreditRunner) markFailed(claim recurringClaim, code, message string) {
	_, _ = r.db.ExecContext(context.Background(), `WITH failed AS (
		UPDATE recurring_credit_batches SET status='failed',failure_code=$3,failure_message=$4,finished_at=NOW(),lease_expires_at=NULL,updated_at=NOW() WHERE id=$1 AND status='running' AND lease_owner=$2 RETURNING task_id,schedule_type
	) UPDATE recurring_credit_tasks SET status='completed',remaining_runs=0,next_run_at=NULL,version=version+1,updated_at=NOW() WHERE id IN (SELECT task_id FROM failed WHERE schedule_type='immediate') AND status<>'deleted'`, claim.BatchID, claim.Owner, code, message)
	logger.LegacyPrintf("service.recurring_credit_runner", "[RecurringCreditRunner] failed batch=%d code=%s message=%s", claim.BatchID, code, message)
}
func (r *RecurringCreditRunner) invalidateUsers(userIDs []int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, id := range userIDs {
		for attempt := 1; attempt <= 3; attempt++ {
			if r.service.authCacheInvalidator != nil {
				r.service.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, id)
			}
			if r.service.billingCache == nil {
				break
			}
			if err := r.service.billingCache.InvalidateUserBalance(ctx, id); err != nil {
				logger.LegacyPrintf("service.recurring_credit_runner", "[RecurringCreditRunner] cache invalidation failed user=%d attempt=%d: %v", id, attempt, err)
				continue
			}
			break
		}
	}
}
func truncateRecurringError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 1000 {
		return message[:1000]
	}
	return message
}
