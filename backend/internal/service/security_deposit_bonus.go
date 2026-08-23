package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

const (
	securityDepositBonusValidityDays = 7
	securityDepositBonusEpsilon      = 0.00000001
	securityDepositBonusUserLockNS   = int32(2400823)
)

// SecurityDepositBonusEstimate 是用户保证金详情中的下一次赠额预估。
type SecurityDepositBonusEstimate struct {
	Enabled              bool       `json:"enabled"`
	Qualified            bool       `json:"qualified"`
	Reason               string     `json:"reason"`
	DailyAmount          float64    `json:"daily_amount"`
	CapRatio             float64    `json:"cap_ratio"`
	CurrentAmount        float64    `json:"current_amount"`
	CapAmount            float64    `json:"cap_amount"`
	EstimatedGrantAmount float64    `json:"estimated_grant_amount"`
	NextGrantAt          time.Time  `json:"next_grant_at"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	QualifyingGroupID    *int64     `json:"qualifying_group_id,omitempty"`
	QualifyingGroupName  string     `json:"qualifying_group_name,omitempty"`
	RequiredCents        int64      `json:"required_cents"`
}

// SecurityDepositBonusReader 为保证金账户详情提供下一次赠额预估。
type SecurityDepositBonusReader interface {
	GetEstimate(ctx context.Context, userID int64, account *SecurityDepositAccountSummary) (*SecurityDepositBonusEstimate, error)
}

// SecurityDepositBonusReconciler 在保证金实际减少后压缩滚动赠额。
type SecurityDepositBonusReconciler interface {
	ReconcileAfterBalanceDecrease(ctx context.Context, userID int64, eventType string, eventID int64) error
}

// SecurityDepositBonusService 管理保证金赠额预估和资金减少后的撤销。
type SecurityDepositBonusService struct {
	db                   *sql.DB
	settings             *SettingService
	depositRepo          SecurityDepositRepository
	groupAccess          securityDepositGroupAccess
	authCacheInvalidator APIKeyAuthCacheInvalidator
	billingCache         *BillingCacheService
	now                  func() time.Time
}

// NewSecurityDepositBonusService 创建保证金赠额服务。
func NewSecurityDepositBonusService(db *sql.DB, settings *SettingService, depositRepo SecurityDepositRepository, groupAccess securityDepositGroupAccess, auth APIKeyAuthCacheInvalidator, billing *BillingCacheService) *SecurityDepositBonusService {
	return &SecurityDepositBonusService{
		db: db, settings: settings, depositRepo: depositRepo, groupAccess: groupAccess,
		authCacheInvalidator: auth, billingCache: billing, now: time.Now,
	}
}

// GetEstimate 根据当前保证金和用户可绑定分组返回下一次实际预计新增额。
func (s *SecurityDepositBonusService) GetEstimate(ctx context.Context, userID int64, account *SecurityDepositAccountSummary) (*SecurityDepositBonusEstimate, error) {
	if s == nil || s.db == nil || s.settings == nil || s.groupAccess == nil {
		return nil, fmt.Errorf("security deposit bonus service is unavailable")
	}
	policy, err := s.settings.GetSecurityDepositPolicyConfigStrict(ctx)
	if err != nil {
		return nil, err
	}
	if account == nil {
		data, loadErr := s.depositRepo.GetUserData(ctx, userID)
		if loadErr != nil {
			return nil, loadErr
		}
		account = buildSecurityDepositSummary(data, s.now().UTC())
	}
	groups, err := s.groupAccess.GetAvailableGroups(ctx, userID)
	if err != nil {
		return nil, err
	}
	group, required := qualifyingSecurityDepositBonusGroup(groups, account.RiskMultiplier, account.EffectiveBalanceCents)
	current, expiresAt, err := s.currentBonusAmount(ctx, userID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	capAmount := securityDepositBonusCapAmount(account.EffectiveBalanceCents, policy.BonusCapRatio)
	daily := decimal.NewFromFloat(policy.BonusDailyAmount).Round(8)
	estimated := decimal.Zero
	if group != nil && policy.EnforcementEnabled && daily.IsPositive() {
		headroom := capAmount.Sub(current)
		if headroom.IsPositive() {
			estimated = decimal.Min(daily, headroom).Round(8)
		}
	}
	nextGrant := nextSecurityDepositBonusMidnight(s.now())
	estimate := &SecurityDepositBonusEstimate{
		Enabled: policy.EnforcementEnabled && daily.IsPositive(), Qualified: group != nil,
		DailyAmount: daily.InexactFloat64(), CapRatio: policy.BonusCapRatio,
		CurrentAmount: current.InexactFloat64(), CapAmount: capAmount.InexactFloat64(),
		EstimatedGrantAmount: estimated.InexactFloat64(), NextGrantAt: nextGrant, ExpiresAt: expiresAt,
	}
	switch {
	case !policy.EnforcementEnabled:
		estimate.Reason = "enforcement_disabled"
	case !daily.IsPositive():
		estimate.Reason = "daily_amount_disabled"
	case group == nil:
		estimate.Reason = "threshold_not_met"
	case !estimated.IsPositive():
		estimate.Reason = "cap_reached"
	default:
		estimate.Reason = "eligible"
	}
	if group != nil {
		groupID := group.ID
		estimate.QualifyingGroupID = &groupID
		estimate.QualifyingGroupName = group.Name
		estimate.RequiredCents = required
	}
	return estimate, nil
}

// ReconcileAfterBalanceDecrease 在成功退款或实际扣除后撤销超过当前上限的未消耗赠额。
func (s *SecurityDepositBonusService) ReconcileAfterBalanceDecrease(ctx context.Context, userID int64, eventType string, eventID int64) error {
	if s == nil || s.db == nil || s.settings == nil || s.depositRepo == nil || s.groupAccess == nil {
		return fmt.Errorf("security deposit bonus reconciler is unavailable")
	}
	policy, err := s.settings.GetSecurityDepositPolicyConfigStrict(ctx)
	if err != nil {
		return err
	}
	data, err := s.depositRepo.GetUserData(ctx, userID)
	if err != nil {
		return err
	}
	account := buildSecurityDepositSummary(data, s.now().UTC())
	groups, err := s.groupAccess.GetAvailableGroups(ctx, userID)
	if err != nil {
		return err
	}
	group, _ := qualifyingSecurityDepositBonusGroup(groups, account.RiskMultiplier, account.EffectiveBalanceCents)
	target := decimal.Zero
	if group != nil {
		target = securityDepositBonusCapAmount(account.EffectiveBalanceCents, policy.BonusCapRatio)
	}
	if err := s.revokeBonusToTarget(ctx, userID, eventType, eventID, target); err != nil {
		return err
	}
	s.invalidateUser(ctx, userID)
	return nil
}

func (s *SecurityDepositBonusService) currentBonusAmount(ctx context.Context, userID int64, now time.Time) (decimal.Decimal, *time.Time, error) {
	var initial, used, frozen decimal.Decimal
	var expires time.Time
	var status string
	err := s.db.QueryRowContext(ctx, `
SELECT initial_amount, used_amount, frozen_amount, expires_at, status
FROM user_limited_credit_grants
WHERE user_id = $1 AND source_type = $2`, userID, LimitedCreditSourceSecurityDepositBonus).
		Scan(&initial, &used, &frozen, &expires, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, nil, nil
	}
	if err != nil {
		return decimal.Zero, nil, err
	}
	if status != LimitedCreditStatusActive || !expires.After(now) {
		// 已到期的可用赠额失效，但请求结算中的冻结赠额仍占用上限。
		return decimal.Max(frozen, decimal.Zero).Round(8), nil, nil
	}
	remaining := decimal.Max(initial.Sub(used), decimal.Zero).Round(8)
	return remaining, &expires, nil
}

func (s *SecurityDepositBonusService) revokeBonusToTarget(ctx context.Context, userID int64, eventType string, eventID int64, target decimal.Decimal) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = lockSecurityDepositBonusUser(ctx, tx, userID); err != nil {
		return err
	}
	var reconciliationID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO security_deposit_bonus_reconciliations(user_id,event_type,event_id,target_amount)
VALUES($1,$2,$3,$4)
ON CONFLICT(user_id,event_type,event_id) DO NOTHING
RETURNING id`, userID, eventType, eventID, target).Scan(&reconciliationID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	var grantID int64
	var initial, used, frozen decimal.Decimal
	err = tx.QueryRowContext(ctx, `
SELECT id,initial_amount,used_amount,frozen_amount
FROM user_limited_credit_grants
WHERE user_id=$1 AND source_type=$2
FOR UPDATE`, userID, LimitedCreditSourceSecurityDepositBonus).Scan(&grantID, &initial, &used, &frozen)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `UPDATE security_deposit_bonus_reconciliations SET target_amount=$2 WHERE id=$1`, reconciliationID, target)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	remaining := decimal.Max(initial.Sub(used), decimal.Zero)
	revoke := decimal.Max(remaining.Sub(target), decimal.Zero).Round(8)
	available := decimal.Max(remaining.Sub(frozen), decimal.Zero)
	immediate := decimal.Min(revoke, available).Round(8)
	pending := decimal.Max(revoke.Sub(immediate), decimal.Zero).Round(8)
	if immediate.IsPositive() || pending.IsPositive() {
		_, err = tx.ExecContext(ctx, `
UPDATE user_limited_credit_grants
SET used_amount=used_amount+$1,
    security_deposit_bonus_pending_revoke_amount=$2,
	    status=CASE WHEN initial_amount-(used_amount+$1)-frozen_amount <= $3 AND frozen_amount <= $3 THEN $4 ELSE status END,
    updated_at=NOW()
	WHERE id=$5`, immediate, pending, securityDepositBonusEpsilon, LimitedCreditStatusDepleted, grantID)
		if err != nil {
			return err
		}
		if immediate.IsPositive() {
			_, err = tx.ExecContext(ctx, `
INSERT INTO user_limited_credit_ledger(user_id,grant_id,event_type,amount,batch_id,notes)
VALUES($1,$2,'security_deposit_bonus_revoke',$3,$4,$5)`, userID, grantID, immediate,
				fmt.Sprintf("security-deposit:%s:%d", eventType, eventID), "保证金退款或扣除后撤销赠额")
			if err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
UPDATE security_deposit_bonus_reconciliations
SET revoked_amount=$2,pending_revoke_amount=$3
WHERE id=$1`, reconciliationID, immediate, pending)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// lockSecurityDepositBonusUser 串行化同一用户的赠额发放与余额减少后撤销。
func lockSecurityDepositBonusUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashint8($2))`, securityDepositBonusUserLockNS, userID)
	return err
}

func (s *SecurityDepositBonusService) invalidateUser(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCache != nil {
		_ = s.billingCache.InvalidateUserBalance(ctx, userID)
	}
}

func qualifyingSecurityDepositBonusGroup(groups []Group, riskMultiplier, effectiveBalanceCents int64) (*Group, int64) {
	if riskMultiplier < 1 {
		riskMultiplier = 1
	}
	var selected *Group
	var selectedRequired int64
	for i := range groups {
		base := groups[i].SecurityDepositBaseRequiredCents
		if base <= 0 {
			continue
		}
		required, err := multiplySecurityDepositThreshold(base, riskMultiplier)
		if err != nil || required > effectiveBalanceCents {
			continue
		}
		if selected == nil || required < selectedRequired || (required == selectedRequired && groups[i].ID < selected.ID) {
			selected = &groups[i]
			selectedRequired = required
		}
	}
	return selected, selectedRequired
}

func securityDepositBonusCapAmount(effectiveBalanceCents int64, capRatio float64) decimal.Decimal {
	if effectiveBalanceCents <= 0 || capRatio <= 0 {
		return decimal.Zero
	}
	return decimal.NewFromInt(effectiveBalanceCents).
		Mul(decimal.NewFromFloat(capRatio)).
		Div(decimal.NewFromInt(10000)).
		Round(8)
}

func securityDepositBonusLocation() *time.Location {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err == nil {
		return location
	}
	return time.FixedZone("Asia/Shanghai", 8*60*60)
}

func nextSecurityDepositBonusMidnight(now time.Time) time.Time {
	local := now.In(securityDepositBonusLocation())
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, local.Location())
}
