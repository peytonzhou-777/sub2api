//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSecurityDepositManualRefundReservationAndEvidenceCompletion(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	operator, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-refund-operator-%d@test.com", suffix)).
		SetPasswordHash("hash").SetStatus(service.StatusActive).SetRole(service.RoleAdmin).Save(ctx)
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-refund-user-%d@test.com", suffix)).
		SetPasswordHash("hash").SetStatus(service.StatusActive).SetRole(service.RoleUser).Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().
		SetName(fmt.Sprintf("security-deposit-refund-group-%d", suffix)).
		SetStatus(service.StatusActive).SetSecurityDepositBaseRequiredCents(10000).Save(ctx)
	require.NoError(t, err)
	apiKey, err := client.APIKey.Create().
		SetUserID(user.ID).SetKey(fmt.Sprintf("sk-security-deposit-refund-%d", suffix)).
		SetName("refund-key").SetGroupID(group.ID).SetStatus(service.StatusAPIKeyActive).Save(ctx)
	require.NoError(t, err)
	order := createSecurityDepositRefundOrder(t, ctx, client, user.ID, user.Email, suffix, 100)
	cleanupSecurityDepositIntegrationData(t, []int64{operator.ID, user.ID}, []int64{group.ID})

	_, err = integrationDB.ExecContext(ctx, `
INSERT INTO security_deposit_accounts (user_id, bucket_type, balance_cents, refund_reserved_cents)
VALUES ($1, 'paid', 10000, 0)`, user.ID)
	require.NoError(t, err)
	var lotID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, payment_order_id, original_cents, remaining_cents,
    currency, locked_until, refund_policy, status
) VALUES ($1, 'paid', 'payment', $2, 10000, 10000, 'CNY', NOW() + INTERVAL '24 hours', 'timed_original_channel', 'active')
RETURNING id`, user.ID, order.ID).Scan(&lotID))

	repo := &securityDepositRepository{db: integrationDB}
	impacts, err := repo.PreviewSecurityDepositRefundImpact(ctx, user.ID, 10000, true)
	require.NoError(t, err)
	require.Len(t, impacts, 1)
	require.Equal(t, apiKey.ID, impacts[0].APIKeyID)
	_, err = repo.ReserveSecurityDepositRefund(ctx, service.SecurityDepositRefundReserveInput{
		RefundID: "sdref-user-frozen-test", UserID: user.ID, LotID: lotID, PaymentOrderID: order.ID,
		PrincipalCents: 10000, GatewayAmount: "100.00", GatewayCurrency: "CNY",
		Mode: service.SecurityDepositRefundModeAutomatic, OperatorID: user.ID,
		QuoteHash: "user-frozen-quote", IdempotencyKey: "user-frozen-key", RequireUnlocked: true,
	}, true)
	require.Error(t, err)
	require.Equal(t, "SECURITY_DEPOSIT_REFUND_FROZEN", infraerrors.Reason(err))
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 10000, 0)

	reserve, err := repo.ReserveSecurityDepositRefund(ctx, service.SecurityDepositRefundReserveInput{
		RefundID: "sdref-manual-test", UserID: user.ID, LotID: lotID, PaymentOrderID: order.ID,
		PrincipalCents: 10000, GatewayAmount: "100.00", GatewayCurrency: "CNY",
		Mode: service.SecurityDepositRefundModeManual, OperatorID: operator.ID,
		QuoteHash: "manual-quote", IdempotencyKey: "manual-reserve-key",
	}, true)
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStateManualReview, reserve.State)
	require.Equal(t, []int64{apiKey.ID}, reserve.DisabledKeyIDs)
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 10000, 10000)

	replayed, err := repo.ReserveSecurityDepositRefund(ctx, service.SecurityDepositRefundReserveInput{
		RefundID: "sdref-manual-test", UserID: user.ID, LotID: lotID, PaymentOrderID: order.ID,
		PrincipalCents: 10000, GatewayAmount: "100.00", GatewayCurrency: "CNY",
		Mode: service.SecurityDepositRefundModeManual, OperatorID: operator.ID,
		QuoteHash: "manual-quote", IdempotencyKey: "manual-reserve-key",
	}, true)
	require.NoError(t, err)
	require.True(t, replayed.AlreadyProcessed)

	_, err = repo.CompleteManualSecurityDepositRefund(ctx, service.AdminSecurityDepositManualCompleteInput{
		UserID: user.ID, RefundID: reserve.RefundID, OperatorID: operator.ID,
		ExternalRefundID: "external-refund-wrong", ExternalAmountCents: 9900,
		ExternalRefundedAt: time.Now(), ExternalEvidence: map[string]any{"voucher": "proof-1"},
		IdempotencyKey: "manual-complete-wrong",
	})
	require.Error(t, err)
	require.Equal(t, "SECURITY_DEPOSIT_EXTERNAL_REFUND_AMOUNT_MISMATCH", infraerrors.Reason(err))
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 10000, 10000)

	completed, err := repo.CompleteManualSecurityDepositRefund(ctx, service.AdminSecurityDepositManualCompleteInput{
		UserID: user.ID, RefundID: reserve.RefundID, OperatorID: operator.ID,
		ExternalRefundID: "external-refund-ok", ExternalAmountCents: 10000,
		ExternalRefundedAt: time.Now(), ExternalEvidence: map[string]any{"voucher": "proof-2"},
		IdempotencyKey: "manual-complete-ok",
	})
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStateSucceeded, completed.State)
	require.NotNil(t, completed.ExternalRefundID)
	require.Equal(t, "external-refund-ok", *completed.ExternalRefundID)
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 0, 0)

	var lotRemaining, lotReserved, lotRefunded int64
	var lotStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT remaining_cents, refund_reserved_cents, refunded_cents, status
FROM security_deposit_lots WHERE id = $1`, lotID).Scan(&lotRemaining, &lotReserved, &lotRefunded, &lotStatus))
	require.Zero(t, lotRemaining)
	require.Zero(t, lotReserved)
	require.Equal(t, int64(10000), lotRefunded)
	require.Equal(t, "refunded", lotStatus)
	reloadedKey, err := client.APIKey.Get(ctx, apiKey.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusAPIKeyDisabled, reloadedKey.Status)
}

func TestSecurityDepositAutomaticRefundFailureReleasesReservation(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	operator, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-auto-operator-%d@test.com", suffix)).
		SetPasswordHash("hash").SetStatus(service.StatusActive).SetRole(service.RoleAdmin).Save(ctx)
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-auto-user-%d@test.com", suffix)).
		SetPasswordHash("hash").SetStatus(service.StatusActive).SetRole(service.RoleUser).Save(ctx)
	require.NoError(t, err)
	order := createSecurityDepositRefundOrder(t, ctx, client, user.ID, user.Email, suffix, 50)
	cleanupSecurityDepositIntegrationData(t, []int64{operator.ID, user.ID}, nil)
	_, err = integrationDB.ExecContext(ctx, `
INSERT INTO security_deposit_accounts (user_id, bucket_type, balance_cents, refund_reserved_cents)
VALUES ($1, 'paid', 5000, 0)`, user.ID)
	require.NoError(t, err)
	var lotID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, payment_order_id, original_cents, remaining_cents,
    currency, locked_until, refund_policy, status
) VALUES ($1, 'paid', 'payment', $2, 5000, 5000, 'CNY', NOW(), 'timed_original_channel', 'active')
RETURNING id`, user.ID, order.ID).Scan(&lotID))

	repo := &securityDepositRepository{db: integrationDB}
	reserved, err := repo.ReserveSecurityDepositRefund(ctx, service.SecurityDepositRefundReserveInput{
		RefundID: "sdref-auto-failure", UserID: user.ID, LotID: lotID, PaymentOrderID: order.ID,
		PrincipalCents: 5000, GatewayAmount: "50.00", GatewayCurrency: "CNY",
		Mode: service.SecurityDepositRefundModeAutomatic, OperatorID: operator.ID,
		QuoteHash: "auto-quote", IdempotencyKey: "auto-reserve-key",
	}, true)
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStateReserved, reserved.State)
	_, claimed, err := repo.ClaimAutomaticSecurityDepositRefund(ctx, reserved.RefundID)
	require.NoError(t, err)
	require.True(t, claimed)

	failed, err := repo.FinalizeAutomaticSecurityDepositRefund(ctx, reserved.RefundID, service.SecurityDepositRefundStateFailedReleased, "", map[string]any{"error": "declined"})
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStateFailedReleased, failed.State)
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 5000, 0)
	var remaining, refunded int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT remaining_cents, refunded_cents FROM security_deposit_lots WHERE id = $1`, lotID).Scan(&remaining, &refunded))
	require.Equal(t, int64(5000), remaining)
	require.Zero(t, refunded)

	pendingOrder := createSecurityDepositRefundOrder(t, ctx, client, user.ID, user.Email, suffix+1, 50)
	_, err = integrationDB.ExecContext(ctx, `
UPDATE security_deposit_accounts
SET balance_cents = balance_cents + 5000
WHERE user_id = $1 AND bucket_type = 'paid'`, user.ID)
	require.NoError(t, err)
	var pendingLotID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, payment_order_id, original_cents, remaining_cents,
    currency, locked_until, refund_policy, status
) VALUES ($1, 'paid', 'payment', $2, 5000, 5000, 'CNY', NOW(), 'timed_original_channel', 'active')
RETURNING id`, user.ID, pendingOrder.ID).Scan(&pendingLotID))
	pending, err := repo.ReserveSecurityDepositRefund(ctx, service.SecurityDepositRefundReserveInput{
		RefundID: "sdref-auto-pending", UserID: user.ID, LotID: pendingLotID, PaymentOrderID: pendingOrder.ID,
		PrincipalCents: 5000, GatewayAmount: "50.00", GatewayCurrency: "CNY",
		Mode: service.SecurityDepositRefundModeAutomatic, OperatorID: operator.ID,
		QuoteHash: "pending-quote", IdempotencyKey: "pending-reserve-key",
	}, true)
	require.NoError(t, err)
	_, claimed, err = repo.ClaimAutomaticSecurityDepositRefund(ctx, pending.RefundID)
	require.NoError(t, err)
	require.True(t, claimed)
	pending, err = repo.FinalizeAutomaticSecurityDepositRefund(ctx, pending.RefundID, service.SecurityDepositRefundStatePending, "provider-pending", map[string]any{"status": "pending"})
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStatePending, pending.State)
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 10000, 5000)

	// pending 查询恢复转成功时不得重复累计支付订单退款额。
	claimedRecord, previousState, claimed, err := repo.ClaimAutomaticSecurityDepositRefundQuery(ctx, pending.RefundID, user.ID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, service.SecurityDepositRefundStatePending, previousState)
	require.Equal(t, service.SecurityDepositRefundStateSubmitting, claimedRecord.State)
	completed, err := repo.FinalizeAutomaticSecurityDepositRefund(ctx, pending.RefundID, service.SecurityDepositRefundStateSucceeded, "provider-pending", map[string]any{"status": "success"})
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStateSucceeded, completed.State)
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 5000, 0)
	var paymentRefundAmount float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT refund_amount FROM payment_orders WHERE id = $1`, pendingOrder.ID).Scan(&paymentRefundAmount))
	require.Equal(t, 50.0, paymentRefundAmount)

	unknownOrder := createSecurityDepositRefundOrder(t, ctx, client, user.ID, user.Email, suffix+2, 25)
	_, err = integrationDB.ExecContext(ctx, `
UPDATE security_deposit_accounts SET balance_cents = balance_cents + 2500
WHERE user_id = $1 AND bucket_type = 'paid'`, user.ID)
	require.NoError(t, err)
	var unknownLotID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, payment_order_id, original_cents, remaining_cents,
    currency, locked_until, refund_policy, status
) VALUES ($1, 'paid', 'payment', $2, 2500, 2500, 'CNY', NOW(), 'timed_original_channel', 'active')
RETURNING id`, user.ID, unknownOrder.ID).Scan(&unknownLotID))
	unknown, err := repo.ReserveSecurityDepositRefund(ctx, service.SecurityDepositRefundReserveInput{
		RefundID: "sdref-auto-unknown", UserID: user.ID, LotID: unknownLotID, PaymentOrderID: unknownOrder.ID,
		PrincipalCents: 2500, GatewayAmount: "25.00", GatewayCurrency: "CNY",
		Mode: service.SecurityDepositRefundModeAutomatic, OperatorID: user.ID,
		QuoteHash: "unknown-quote", IdempotencyKey: "unknown-reserve-key", RequireUnlocked: true,
	}, true)
	require.NoError(t, err)
	_, claimed, err = repo.ClaimAutomaticSecurityDepositRefund(ctx, unknown.RefundID)
	require.NoError(t, err)
	require.True(t, claimed)
	unknown, err = repo.FinalizeAutomaticSecurityDepositRefund(ctx, unknown.RefundID, service.SecurityDepositRefundStateManualReview, "", map[string]any{"error": "timeout"})
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStateManualReview, unknown.State)

	reviewed, err := repo.FailAutomaticSecurityDepositRefundReview(ctx, service.AdminSecurityDepositAutomaticReviewFailureInput{
		UserID: user.ID, RefundID: unknown.RefundID, OperatorID: operator.ID,
		Evidence: map[string]any{"reference": "provider-console-no-refund"}, IdempotencyKey: "review-failed-1",
	})
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStateFailedReleased, reviewed.State)
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 7500, 0)
	var reviewedOrderStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT status, refund_amount FROM payment_orders WHERE id = $1`, unknownOrder.ID).Scan(&reviewedOrderStatus, &paymentRefundAmount))
	require.Equal(t, service.OrderStatusCompleted, reviewedOrderStatus)
	require.Zero(t, paymentRefundAmount)
}

func TestSecurityDepositAutomaticManualReviewEvidenceCompletion(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()
	operator, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-auto-review-operator-%d@test.com", suffix)).
		SetPasswordHash("hash").SetStatus(service.StatusActive).SetRole(service.RoleAdmin).Save(ctx)
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("security-deposit-auto-review-user-%d@test.com", suffix)).
		SetPasswordHash("hash").SetStatus(service.StatusActive).SetRole(service.RoleUser).Save(ctx)
	require.NoError(t, err)
	order := createSecurityDepositRefundOrder(t, ctx, client, user.ID, user.Email, suffix, 25)
	cleanupSecurityDepositIntegrationData(t, []int64{operator.ID, user.ID}, nil)
	_, err = integrationDB.ExecContext(ctx, `
INSERT INTO security_deposit_accounts (user_id, bucket_type, balance_cents, refund_reserved_cents)
VALUES ($1, 'paid', 2500, 0)`, user.ID)
	require.NoError(t, err)
	var lotID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, payment_order_id, original_cents, remaining_cents,
    currency, locked_until, refund_policy, status
) VALUES ($1, 'paid', 'payment', $2, 2500, 2500, 'CNY', NOW(), 'timed_original_channel', 'active')
RETURNING id`, user.ID, order.ID).Scan(&lotID))

	repo := &securityDepositRepository{db: integrationDB}
	reserved, err := repo.ReserveSecurityDepositRefund(ctx, service.SecurityDepositRefundReserveInput{
		RefundID: "sdref-auto-review-success", UserID: user.ID, LotID: lotID, PaymentOrderID: order.ID,
		PrincipalCents: 2500, GatewayAmount: "25.00", GatewayCurrency: "CNY",
		Mode: service.SecurityDepositRefundModeAutomatic, OperatorID: user.ID,
		QuoteHash: "auto-review-quote", IdempotencyKey: "auto-review-reserve-key", RequireUnlocked: true,
	}, true)
	require.NoError(t, err)
	_, claimed, err := repo.ClaimAutomaticSecurityDepositRefund(ctx, reserved.RefundID)
	require.NoError(t, err)
	require.True(t, claimed)
	review, err := repo.FinalizeAutomaticSecurityDepositRefund(ctx, reserved.RefundID, service.SecurityDepositRefundStateManualReview, "", map[string]any{"error": "timeout"})
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundStateManualReview, review.State)
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 2500, 2500)

	refundedAt := time.Now().UTC()
	completed, err := repo.CompleteManualSecurityDepositRefund(ctx, service.AdminSecurityDepositManualCompleteInput{
		UserID: user.ID, RefundID: review.RefundID, OperatorID: operator.ID,
		ExternalRefundID: "external-auto-review-ok", ExternalAmountCents: 2500,
		ExternalRefundedAt: refundedAt, ExternalEvidence: map[string]any{"voucher": "provider-console-proof"},
		IdempotencyKey: "auto-review-complete-key",
	})
	require.NoError(t, err)
	require.Equal(t, service.SecurityDepositRefundModeAutomatic, completed.Mode)
	require.Equal(t, service.SecurityDepositRefundStateSucceeded, completed.State)
	require.NotNil(t, completed.ExternalRefundID)
	require.Equal(t, "external-auto-review-ok", *completed.ExternalRefundID)
	require.NotNil(t, completed.ExternalRefundedAt)
	require.NotNil(t, completed.ExternalEvidence)
	assertSecurityDepositRefundBalance(t, ctx, user.ID, 0, 0)

	var lotRemaining, lotReserved, lotRefunded int64
	var lotStatus string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT remaining_cents, refund_reserved_cents, refunded_cents, status
FROM security_deposit_lots WHERE id = $1`, lotID).Scan(&lotRemaining, &lotReserved, &lotRefunded, &lotStatus))
	require.Zero(t, lotRemaining)
	require.Zero(t, lotReserved)
	require.Equal(t, int64(2500), lotRefunded)
	require.Equal(t, "refunded", lotStatus)

	var orderStatus string
	var paymentRefundAmount float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT status, refund_amount FROM payment_orders WHERE id = $1`, order.ID).Scan(&orderStatus, &paymentRefundAmount))
	require.Equal(t, service.OrderStatusRefunded, orderStatus)
	require.Equal(t, 25.0, paymentRefundAmount)
}

func createSecurityDepositRefundOrder(t *testing.T, ctx context.Context, client *dbent.Client, userID int64, email string, suffix int64, amount float64) *dbent.PaymentOrder {
	t.Helper()
	order, err := client.PaymentOrder.Create().
		SetUserID(userID).
		SetUserEmail(email).
		SetUserName("security-deposit-refund-user").
		SetAmount(amount).
		SetPayAmount(amount).
		SetFeeRate(0).
		SetRechargeCode(fmt.Sprintf("SD-REFUND-%d", suffix)).
		SetOutTradeNo(fmt.Sprintf("sd-refund-%d", suffix)).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(fmt.Sprintf("trade-sd-refund-%d", suffix)).
		SetOrderType(payment.OrderTypeSecurityDeposit).
		SetStatus(service.OrderStatusCompleted).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)
	return order
}

func assertSecurityDepositRefundBalance(t *testing.T, ctx context.Context, userID, balance, reserved int64) {
	t.Helper()
	var actualBalance, actualReserved int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT balance_cents, refund_reserved_cents
FROM security_deposit_accounts
WHERE user_id = $1 AND bucket_type = 'paid'`, userID).Scan(&actualBalance, &actualReserved))
	require.Equal(t, balance, actualBalance)
	require.Equal(t, reserved, actualReserved)
}
