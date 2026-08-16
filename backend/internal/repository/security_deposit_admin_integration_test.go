//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSecurityDepositAdminMutationsKeepBucketsSeparatedAndReconcileKeys(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	operator, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-operator-%d@test.com", suffix)).
		SetPasswordHash("test-password-hash").
		SetStatus(service.StatusActive).
		SetRole(service.RoleAdmin).
		Save(ctx)
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-admin-user-%d@test.com", suffix)).
		SetPasswordHash("test-password-hash").
		SetStatus(service.StatusActive).
		SetRole(service.RoleUser).
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName(fmt.Sprintf("security-deposit-admin-group-%d", suffix)).
		SetStatus(service.StatusActive).
		SetSecurityDepositBaseRequiredCents(13000).
		Save(ctx)
	require.NoError(t, err)
	activeKey, err := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey(fmt.Sprintf("sk-security-deposit-admin-active-%d", suffix)).
		SetName("active-key").
		SetGroupID(group.ID).
		SetStatus(service.StatusAPIKeyActive).
		Save(ctx)
	require.NoError(t, err)
	lockedKey, err := client.APIKey.Create().
		SetUserID(user.ID).
		SetKey(fmt.Sprintf("sk-security-deposit-admin-locked-%d", suffix)).
		SetName("locked-key").
		SetGroupID(group.ID).
		SetStatus(service.StatusAPIKeySecurityLocked).
		Save(ctx)
	require.NoError(t, err)
	paymentOrder, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode(fmt.Sprintf("SD-ADMIN-%d", suffix)).
		SetOutTradeNo(fmt.Sprintf("sd-admin-%d", suffix)).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(fmt.Sprintf("trade-sd-admin-%d", suffix)).
		SetOrderType(payment.OrderTypeSecurityDeposit).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	cleanupSecurityDepositIntegrationData(t, []int64{operator.ID, user.ID}, []int64{group.ID})

	_, err = integrationDB.ExecContext(ctx, `
INSERT INTO security_deposit_accounts (user_id, bucket_type, balance_cents, refund_reserved_cents)
VALUES ($1, 'paid', 10000, 0)`, user.ID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, payment_order_id, original_cents, remaining_cents,
    currency, locked_until, refund_policy, status
) VALUES ($1, 'paid', 'payment', $2, 10000, 10000, 'CNY', NOW() - INTERVAL '1 hour', 'timed_original_channel', 'active')`, user.ID, paymentOrder.ID)
	require.NoError(t, err)

	repo := &securityDepositRepository{db: integrationDB}
	grant, err := repo.CreditAdminGrant(ctx, service.AdminSecurityDepositCreditInput{
		UserID: user.ID, OperatorID: operator.ID, AmountCents: 5000,
		ActionType: service.SecurityDepositAdminActionAdd, IdempotencyKey: "grant-1",
	})
	require.NoError(t, err)
	require.Equal(t, int64(5000), grant.AdminGrantBalanceAfterCents)
	require.NotNil(t, grant.LotID)
	compensation, err := repo.CreditAdminGrant(ctx, service.AdminSecurityDepositCreditInput{
		UserID: user.ID, OperatorID: operator.ID, AmountCents: 2000,
		ActionType: service.SecurityDepositAdminActionCompensation, IdempotencyKey: "compensation-1",
	})
	require.NoError(t, err)
	require.Equal(t, int64(7000), compensation.AdminGrantBalanceAfterCents)

	_, err = repo.DeductAdminGrant(ctx, service.AdminSecurityDepositDeductInput{
		UserID: user.ID, OperatorID: operator.ID, AmountCents: 8000, IdempotencyKey: "deduct-too-much",
	}, true)
	require.Error(t, err)
	require.Equal(t, "SECURITY_DEPOSIT_ADMIN_GRANT_INSUFFICIENT", infraerrors.Reason(err))
	assertSecurityDepositBucketBalances(t, ctx, user.ID, 10000, 7000)

	deduction, err := repo.DeductAdminGrant(ctx, service.AdminSecurityDepositDeductInput{
		UserID: user.ID, OperatorID: operator.ID, AmountCents: 2000, IdempotencyKey: "deduct-1",
	}, true)
	require.NoError(t, err)
	require.Equal(t, int64(5000), deduction.AdminGrantBalanceAfterCents)
	require.Empty(t, deduction.DisabledKeyIDs)
	assertSecurityDepositBucketBalances(t, ctx, user.ID, 10000, 5000)
	replayedDeduction, err := repo.DeductAdminGrant(ctx, service.AdminSecurityDepositDeductInput{
		UserID: user.ID, OperatorID: operator.ID, AmountCents: 2000, IdempotencyKey: "deduct-1",
	}, true)
	require.NoError(t, err)
	require.True(t, replayedDeduction.AlreadyProcessed)
	assertSecurityDepositBucketBalances(t, ctx, user.ID, 10000, 5000)

	revoke, err := repo.RevokeAdminGrantLot(ctx, service.AdminSecurityDepositRevokeInput{
		UserID: user.ID, OperatorID: operator.ID, LotID: *grant.LotID, IdempotencyKey: "revoke-1",
	}, true)
	require.NoError(t, err)
	require.Equal(t, int64(3000), revoke.AmountCents)
	require.Equal(t, []int64{activeKey.ID}, revoke.DisabledKeyIDs)
	assertSecurityDepositBucketBalances(t, ctx, user.ID, 10000, 2000)

	var activeStatus, disabledReason string
	var disabledEventID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT status, disabled_reason, disabled_financial_event_id
FROM api_keys WHERE id = $1`, activeKey.ID).Scan(&activeStatus, &disabledReason, &disabledEventID))
	require.Equal(t, service.StatusAPIKeyDisabled, activeStatus)
	require.Equal(t, service.DisabledReasonSecurityDepositInsufficient, disabledReason)
	require.Equal(t, revoke.ActionID, disabledEventID)

	unlock, err := repo.UnlockSecurityLockedAPIKey(ctx, service.AdminSecurityDepositUnlockInput{
		UserID: user.ID, OperatorID: operator.ID, APIKeyID: lockedKey.ID, IdempotencyKey: "unlock-1",
	})
	require.NoError(t, err)
	require.Equal(t, service.StatusAPIKeyDisabled, unlock.Status)
	var unlockedStatus string
	var lockedAt, lockViolationID, lockReason any
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT status, security_locked_at, security_lock_violation_id, security_lock_reason
FROM api_keys WHERE id = $1`, lockedKey.ID).Scan(&unlockedStatus, &lockedAt, &lockViolationID, &lockReason))
	require.Equal(t, service.StatusAPIKeyDisabled, unlockedStatus)
	require.Nil(t, lockedAt)
	require.Nil(t, lockViolationID)
	require.Nil(t, lockReason)
}

func assertSecurityDepositBucketBalances(t *testing.T, ctx context.Context, userID, paid, adminGrant int64) {
	t.Helper()
	var paidBalance, adminGrantBalance int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT balance_cents FROM security_deposit_accounts
WHERE user_id = $1 AND bucket_type = 'paid'`, userID).Scan(&paidBalance))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT balance_cents FROM security_deposit_accounts
WHERE user_id = $1 AND bucket_type = 'admin_grant'`, userID).Scan(&adminGrantBalance))
	require.Equal(t, paid, paidBalance)
	require.Equal(t, adminGrant, adminGrantBalance)
}
