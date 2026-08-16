//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSecurityDepositPenaltyIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-penalty-%d@test.com", suffix)).
		SetPasswordHash("test-password-hash").
		SetStatus(service.StatusActive).
		SetRole(service.RoleUser).
		Save(ctx)
	require.NoError(t, err)
	triggerGroup, err := client.Group.Create().
		SetName(fmt.Sprintf("security-deposit-trigger-%d", suffix)).
		SetStatus(service.StatusActive).
		SetSecurityDepositBaseRequiredCents(10000).
		Save(ctx)
	require.NoError(t, err)
	otherGroup, err := client.Group.Create().
		SetName(fmt.Sprintf("security-deposit-other-%d", suffix)).
		SetStatus(service.StatusActive).
		SetSecurityDepositBaseRequiredCents(8000).
		Save(ctx)
	require.NoError(t, err)
	triggerKey, err := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey(fmt.Sprintf("sk-security-deposit-trigger-%d", suffix)).
		SetName("trigger-key").
		SetGroupID(triggerGroup.ID).
		SetStatus(service.StatusAPIKeyActive).
		Save(ctx)
	require.NoError(t, err)
	otherKey, err := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey(fmt.Sprintf("sk-security-deposit-other-%d", suffix)).
		SetName("other-key").
		SetGroupID(otherGroup.ID).
		SetStatus(service.StatusAPIKeyActive).
		Save(ctx)
	require.NoError(t, err)
	paymentOrder, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(150).
		SetPayAmount(150).
		SetFeeRate(0).
		SetRechargeCode(fmt.Sprintf("SD-PENALTY-%d", suffix)).
		SetOutTradeNo(fmt.Sprintf("sd-penalty-%d", suffix)).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(fmt.Sprintf("trade-sd-penalty-%d", suffix)).
		SetOrderType(payment.OrderTypeSecurityDeposit).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	cleanupSecurityDepositIntegrationData(t, []int64{user.ID}, []int64{triggerGroup.ID, otherGroup.ID})

	_, err = integrationDB.ExecContext(ctx, `
INSERT INTO security_deposit_risk_profiles (user_id, cyber_strike_count, risk_multiplier, version)
VALUES ($1, 1, 2, 1)`, user.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
INSERT INTO security_deposit_accounts (user_id, bucket_type, balance_cents, refund_reserved_cents)
VALUES ($1, 'paid', 15000, 0), ($1, 'admin_grant', 10000, 0)`, user.ID)
	require.NoError(t, err)
	var paidLotID, adminLotID int64
	err = integrationDB.QueryRowContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, payment_order_id, original_cents, remaining_cents,
    currency, locked_until, refund_policy, status, created_at
) VALUES ($1, 'paid', 'payment', $2, 15000, 15000, 'CNY', NOW() + INTERVAL '24 hours', 'timed_original_channel', 'active', NOW() - INTERVAL '2 hours')
RETURNING id`, user.ID, paymentOrder.ID).Scan(&paidLotID)
	require.NoError(t, err)
	err = integrationDB.QueryRowContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, original_cents, remaining_cents,
    currency, refund_policy, status, created_at
) VALUES ($1, 'admin_grant', 'admin', 10000, 10000, 'CNY', 'never', 'active', NOW() - INTERVAL '1 hour')
RETURNING id`, user.ID).Scan(&adminLotID)
	require.NoError(t, err)

	repo := &securityDepositRepository{db: integrationDB}
	input := service.SecurityDepositCyberPenaltyInput{
		EventKey: "penalty-event-" + fmt.Sprint(suffix), RequestID: "request-1", PolicyCode: "cyber_policy",
		Grant: service.SecurityDepositAccessGrant{
			UserID: user.ID, GroupID: triggerGroup.ID, BaseRequiredCents: 10000,
			RiskMultiplier: 2, RequiredCents: 20000, EffectiveBalanceCents: 25000, Enforced: true,
		},
		APIKeyID: triggerKey.ID, APIKeyName: triggerKey.Name, GroupName: triggerGroup.Name,
	}
	// 准入后管理员提高门槛；处罚必须按事务发生时的最新门槛计算，而不是旧准入快照。
	_, err = client.Group.UpdateOneID(triggerGroup.ID).SetSecurityDepositBaseRequiredCents(20000).Save(ctx)
	require.NoError(t, err)
	// 模拟退款/扣除与官方网安回调竞态：触发密钥先被普通资金事件禁用，可信处罚仍必须升级为安全锁。
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET status = $1 WHERE id = $2`, service.StatusAPIKeyDisabled, triggerKey.ID)
	require.NoError(t, err)

	result, err := repo.ApplyCyberPolicyPenalty(ctx, input, 8, false)
	require.NoError(t, err)
	require.Equal(t, "processed", result.State)
	require.Equal(t, int64(25000), result.ForfeitedCents)
	require.Equal(t, int64(15000), result.ShortfallCents)
	require.Equal(t, int64(3), result.RiskMultiplierAfter)
	require.True(t, result.SecurityLocked)
	require.Equal(t, []int64{otherKey.ID}, result.DisabledKeyIDs)

	assertSecurityDepositPenaltyState(t, ctx, user.ID, paidLotID, adminLotID, triggerKey.ID, otherKey.ID, result.ViolationID)
	replayed, err := repo.ApplyCyberPolicyPenalty(ctx, input, 8, false)
	require.NoError(t, err)
	require.True(t, replayed.AlreadyProcessed)
	require.Equal(t, result.ViolationID, replayed.ViolationID)
	assertSecurityDepositPenaltyState(t, ctx, user.ID, paidLotID, adminLotID, triggerKey.ID, otherKey.ID, result.ViolationID)

	shadowInput := input
	shadowInput.EventKey += "-shadow"
	shadowInput.RequestID = "request-shadow"
	shadowResult, err := repo.ApplyCyberPolicyPenalty(ctx, shadowInput, 8, true)
	require.NoError(t, err)
	require.Equal(t, "shadow", shadowResult.State)
	require.Zero(t, shadowResult.ForfeitedCents)
	require.False(t, shadowResult.SecurityLocked)
	assertSecurityDepositPenaltyState(t, ctx, user.ID, paidLotID, adminLotID, triggerKey.ID, otherKey.ID, result.ViolationID)
	var shadowRiskEvents int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_deposit_risk_events WHERE violation_id = $1`, shadowResult.ViolationID).Scan(&shadowRiskEvents))
	require.Zero(t, shadowRiskEvents)
}

func assertSecurityDepositPenaltyState(t *testing.T, ctx context.Context, userID, paidLotID, adminLotID, triggerKeyID, otherKeyID, violationID int64) {
	t.Helper()
	var paidBalance, adminBalance int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance_cents FROM security_deposit_accounts WHERE user_id = $1 AND bucket_type = 'paid'`, userID).Scan(&paidBalance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT balance_cents FROM security_deposit_accounts WHERE user_id = $1 AND bucket_type = 'admin_grant'`, userID).Scan(&adminBalance))
	require.Equal(t, int64(0), paidBalance)
	require.Zero(t, adminBalance)

	var paidRemaining, adminRemaining int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT remaining_cents FROM security_deposit_lots WHERE id = $1`, paidLotID).Scan(&paidRemaining))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT remaining_cents FROM security_deposit_lots WHERE id = $1`, adminLotID).Scan(&adminRemaining))
	require.Equal(t, int64(0), paidRemaining)
	require.Zero(t, adminRemaining)

	var strikeCount, multiplier int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT cyber_strike_count, risk_multiplier FROM security_deposit_risk_profiles WHERE user_id = $1`, userID).Scan(&strikeCount, &multiplier))
	require.Equal(t, int64(2), strikeCount)
	require.Equal(t, int64(3), multiplier)

	var triggerStatus, otherStatus string
	var lockViolationID, disabledEventID *int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status, security_lock_violation_id FROM api_keys WHERE id = $1`, triggerKeyID).Scan(&triggerStatus, &lockViolationID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status, disabled_financial_event_id FROM api_keys WHERE id = $1`, otherKeyID).Scan(&otherStatus, &disabledEventID))
	require.Equal(t, service.StatusAPIKeySecurityLocked, triggerStatus)
	require.NotNil(t, lockViolationID)
	require.Equal(t, violationID, *lockViolationID)
	require.Equal(t, service.StatusAPIKeyDisabled, otherStatus)
	require.NotNil(t, disabledEventID)
	require.Equal(t, violationID, *disabledEventID)

	var ledgerCount, riskEventCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_deposit_ledger WHERE violation_id = $1`, violationID).Scan(&ledgerCount))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_deposit_risk_events WHERE violation_id = $1`, violationID).Scan(&riskEventCount))
	require.Equal(t, 2, ledgerCount)
	require.Equal(t, 1, riskEventCount)

	var baseRequiredSnapshot, riskMultiplierBefore, requiredSnapshot, forfeited, shortfall int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT base_required_snapshot_cents, risk_multiplier_before, required_snapshot_cents, forfeited_cents, shortfall_cents
FROM security_deposit_violations
WHERE id = $1`, violationID).Scan(&baseRequiredSnapshot, &riskMultiplierBefore, &requiredSnapshot, &forfeited, &shortfall))
	require.Equal(t, int64(20000), baseRequiredSnapshot)
	require.Equal(t, int64(2), riskMultiplierBefore)
	require.Equal(t, int64(40000), requiredSnapshot)
	require.Equal(t, int64(25000), forfeited)
	require.Equal(t, int64(15000), shortfall)
}
