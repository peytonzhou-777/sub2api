//go:build unit

package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestBuildAccountRefundQuoteAppliesCampaignAndOriginalPriceRules(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund@example.com").SetPasswordHash("hash").SetUsername("refund-user").SetBalance(150).SetTotalRecharged(150).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("refund-provider").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)

	activityOrder := createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0.2, paymentorder.RechargeBonusStatusGranted)
	createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 50, 50, 0, paymentorder.RechargeBonusStatusNone)
	_, err = client.UserLimitedCreditGrant.Create().SetUserID(userRow.ID).SetSourceType(LimitedCreditSourceRechargeBonus).SetSourceID(activityOrder).SetInitialAmount(20).SetUsedAmount(0).SetFrozenAmount(0).SetExpiresAt(time.Now().Add(24 * time.Hour)).SetStatus(LimitedCreditStatusActive).Save(ctx)
	require.NoError(t, err)
	_, err = client.UserLimitedCreditGrant.Create().SetUserID(userRow.ID).SetSourceType(LimitedCreditSourceResetRebate).SetInitialAmount(7).SetExpiresAt(time.Now().Add(24 * time.Hour)).SetStatus(LimitedCreditStatusActive).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 150, TotalRecharged: 150, Status: StatusActive}}}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.True(t, quote.Eligible)
	require.Equal(t, "reconciled", quote.TotalConfidence)
	require.InDelta(t, 170, quote.EligibleCreditTotal, 1e-8)
	require.InDelta(t, 150, quote.RefundCreditTotal, 1e-8)
	require.InDelta(t, 7, quote.OtherLimitedToClear, 1e-8)
	require.Len(t, quote.Orders, 2)
	require.InDelta(t, 100, quote.Orders[0].GatewayRefund, 1e-8)
	require.InDelta(t, 50, quote.Orders[1].GatewayRefund, 1e-8)
}

func TestBuildAccountRefundQuoteInfersConsumedPermanentBalance(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund-mismatch@example.com").SetPasswordHash("hash").SetUsername("refund-mismatch").SetBalance(99).SetTotalRecharged(100).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("refund-provider-mismatch").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0, paymentorder.RechargeBonusStatusNone)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 99, TotalRecharged: 100, Status: StatusActive}}}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.True(t, quote.Eligible)
	require.Equal(t, "reconciled", quote.TotalConfidence)
	require.Equal(t, "inferred", quote.AllocationConfidence)
	require.InDelta(t, 99, quote.RefundCreditTotal, 1e-8)
	require.InDelta(t, 99, quote.GatewayTotals["CNY"], 1e-8)
	require.InDelta(t, quote.RefundCreditTotal, quote.Orders[0].RefundCredit, 1e-8)
}

func TestBuildAccountRefundQuoteBlocksUnreconciledRechargeTotal(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund-unreconciled@example.com").SetPasswordHash("hash").SetUsername("refund-unreconciled").SetBalance(99).SetTotalRecharged(125).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("refund-provider-unreconciled").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0, paymentorder.RechargeBonusStatusNone)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 99, TotalRecharged: 125, Status: StatusActive}}}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.False(t, quote.Eligible)
	require.Equal(t, "manual_review", quote.TotalConfidence)
	require.Contains(t, quote.BlockReason, "cumulative recharge total")
	require.True(t, quote.DonationEligible)
	require.InDelta(t, 99, quote.DonationAmount, 1e-8)
}

func TestBuildAccountRefundQuoteUsesAccountPoolBonusRatio(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund-pool@example.com").SetPasswordHash("hash").SetUsername("refund-pool").SetBalance(50).SetTotalRecharged(100).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("refund-provider-pool").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	orderID := createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0.2, paymentorder.RechargeBonusStatusGranted)
	_, err = client.UserLimitedCreditGrant.Create().SetUserID(userRow.ID).SetSourceType(LimitedCreditSourceRechargeBonus).SetSourceID(orderID).SetInitialAmount(20).SetUsedAmount(10).SetFrozenAmount(0).SetExpiresAt(time.Now().Add(24 * time.Hour)).SetStatus(LimitedCreditStatusActive).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 50, TotalRecharged: 100, Status: StatusActive}}}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.True(t, quote.Eligible)
	require.Equal(t, "inferred", quote.AllocationConfidence)
	require.InDelta(t, 60, quote.EligibleCreditTotal, 1e-8)
	require.InDelta(t, 50, quote.RefundCreditTotal, 1e-8)
	require.InDelta(t, 50, quote.GatewayTotals["CNY"], 1e-8)
}

func TestBuildAccountRefundQuoteUsesAccountPoolWhenOnlyBonusWasConsumed(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund-mixed-pool@example.com").SetPasswordHash("hash").SetUsername("refund-mixed-pool").SetBalance(150).SetTotalRecharged(150).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("refund-provider-mixed-pool").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	activityOrder := createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0.2, paymentorder.RechargeBonusStatusGranted)
	createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 50, 50, 0, paymentorder.RechargeBonusStatusNone)
	_, err = client.UserLimitedCreditGrant.Create().SetUserID(userRow.ID).SetSourceType(LimitedCreditSourceRechargeBonus).SetSourceID(activityOrder).SetInitialAmount(20).SetUsedAmount(10).SetFrozenAmount(0).SetExpiresAt(time.Now().Add(24 * time.Hour)).SetStatus(LimitedCreditStatusActive).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 150, TotalRecharged: 150, Status: StatusActive}}}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.True(t, quote.Eligible)
	require.Equal(t, "inferred", quote.AllocationConfidence)
	require.InDelta(t, 141.17647059, quote.RefundCreditTotal, 1e-8)
	require.InDelta(t, 141.18, quote.GatewayTotals["CNY"], 1e-8)
}

func TestBuildAccountRefundQuoteBlocksMissingOriginalTradeNumber(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund-no-trade@example.com").SetPasswordHash("hash").SetUsername("refund-no-trade").SetBalance(100).SetTotalRecharged(100).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("refund-provider-no-trade").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	orderID := createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0, paymentorder.RechargeBonusStatusNone)
	_, err = client.PaymentOrder.UpdateOneID(orderID).SetPaymentTradeNo("").Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 100, TotalRecharged: 100, Status: StatusActive}}}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.False(t, quote.Eligible)
	require.Contains(t, quote.BlockReason, "trade number")
	require.True(t, quote.DonationEligible)
	require.InDelta(t, 100, quote.DonationAmount, 1e-8)
}

func TestBuildAccountRefundQuoteBlocksInFlightRecharge(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund-pending@example.com").SetPasswordHash("hash").SetUsername("refund-pending").SetBalance(100).SetTotalRecharged(100).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("refund-provider-pending").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0, paymentorder.RechargeBonusStatusNone)
	pendingID := createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 20, 20, 0, paymentorder.RechargeBonusStatusNone)
	_, err = client.PaymentOrder.UpdateOneID(pendingID).SetStatus(OrderStatusPending).ClearPaidAt().ClearCompletedAt().Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 100, TotalRecharged: 100, Status: StatusActive}}}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.False(t, quote.Eligible)
	require.Contains(t, quote.BlockReason, "still pending")
}

func TestBuildAccountRefundQuoteIgnoresUnpaidFailedRecharge(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund-failed@example.com").SetPasswordHash("hash").SetUsername("refund-failed").SetBalance(100).SetTotalRecharged(100).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("refund-provider-failed").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0, paymentorder.RechargeBonusStatusNone)
	failedID := createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 20, 20, 0, paymentorder.RechargeBonusStatusNone)
	_, err = client.PaymentOrder.UpdateOneID(failedID).SetStatus(OrderStatusFailed).ClearPaidAt().ClearCompletedAt().Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 100, TotalRecharged: 100, Status: StatusActive}}}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.True(t, quote.Eligible)
}

func TestPaymentNotificationHoldsPaidOrderDuringAccountRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("account-refund-late-payment@example.com").SetPasswordHash("hash").SetUsername("refund-late-payment").SetStatus(StatusRefundLocked).Save(ctx)
	require.NoError(t, err)
	now := time.Now().UTC()
	order, err := client.PaymentOrder.Create().
		SetUserID(userRow.ID).
		SetUserEmail(userRow.Email).
		SetUserName(userRow.Username).
		SetAmount(20).
		SetPayAmount(20).
		SetFeeRate(0).
		SetRechargeCode("REFUND-LATE-PAYMENT").
		SetOutTradeNo("refund_late_payment").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("example.com").
		Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client}
	require.NoError(t, svc.toPaid(ctx, order, "late-trade", 20, payment.TypeAlipay))
	updated, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPaid, updated.Status)
	auditExists, err := client.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(fmt.Sprintf("%d", order.ID)),
		paymentauditlog.ActionEQ("PAYMENT_HELD_BY_ACCOUNT_REFUND"),
	).Exist(ctx)
	require.NoError(t, err)
	require.True(t, auditExists)
}

func TestAccountRefundSessionTokenIsBoundAndExpires(t *testing.T) {
	svc := NewPaymentResumeService([]byte("account-refund-session-test-key"))
	token, err := svc.CreateAccountRefundSessionToken("refund-1", 42)
	require.NoError(t, err)
	claims, err := svc.ParseAccountRefundSessionToken(token)
	require.NoError(t, err)
	require.Equal(t, "refund-1", claims.RefundID)
	require.Equal(t, int64(42), claims.UserID)

	expired, err := svc.createSignedToken(AccountRefundSessionClaims{TokenType: accountRefundSessionTokenType, RefundID: "refund-1", UserID: 42, IssuedAt: time.Now().Add(-time.Hour).Unix(), ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	require.NoError(t, err)
	_, err = svc.ParseAccountRefundSessionToken(expired)
	require.Error(t, err)
	require.Equal(t, "REFUND_SESSION_EXPIRED", infraerrors.Reason(err))
}

func TestRestoreAccountRefundSessionOnlyIssuesDedicatedToken(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	account := &User{ID: 42, Email: "restore-refund@example.com", Status: StatusRefundLocked}
	require.NoError(t, account.SetPassword("correct-password"))
	record := &AccountRefundRecord{RefundID: "refund-restore-1", UserID: account.ID, State: AccountRefundStateReadyToConfirm, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	require.NoError(t, writeAccountRefundAudit(ctx, client, record))
	fence := &accountRefundFenceStub{}
	svc := &PaymentService{
		entClient:            client,
		userRepo:             &mockUserRepo{getByEmailUser: account},
		resumeService:        NewPaymentResumeService([]byte("account-refund-restore-test-key")),
		authCacheInvalidator: fence,
	}

	restored, err := svc.RestoreAccountRefundSession(ctx, account.Email, "correct-password", "")
	require.NoError(t, err)
	require.Equal(t, record.RefundID, restored.RefundID)
	require.NotEmpty(t, restored.SessionToken)
	require.Equal(t, 1, fence.acquireCalls)
	claims, err := svc.ParseAccountRefundSession(restored.SessionToken)
	require.NoError(t, err)
	require.Equal(t, account.ID, claims.UserID)
	require.Equal(t, record.RefundID, claims.RefundID)

	_, err = svc.RestoreAccountRefundSession(ctx, account.Email, "wrong-password", "")
	require.Error(t, err)
	require.Equal(t, "INVALID_CREDENTIALS", infraerrors.Reason(err))
}

func TestAllocateRefundUnitsConservesTotalAndCapacity(t *testing.T) {
	allocated := allocateRefundUnits(decimal.RequireFromString("50.00"), []decimal.Decimal{
		decimal.RequireFromString("60.00"),
		decimal.RequireFromString("40.00"),
	}, 2)
	require.Equal(t, []int64{3000, 2000}, allocated)
	require.Equal(t, int64(5000), allocated[0]+allocated[1])

	rounded := allocateRefundUnits(decimal.RequireFromString("0.05"), []decimal.Decimal{
		decimal.RequireFromString("1.00"),
		decimal.RequireFromString("1.00"),
		decimal.RequireFromString("1.00"),
	}, 2)
	require.Equal(t, int64(5), rounded[0]+rounded[1]+rounded[2])
	require.LessOrEqual(t, rounded[0], int64(100))
	require.LessOrEqual(t, rounded[1], int64(100))
	require.LessOrEqual(t, rounded[2], int64(100))
}

func TestUpdateStatusProtectsActiveRefundLock(t *testing.T) {
	repo := &mockUserRepo{getByIDUser: &User{ID: 7, Status: StatusRefundLocked}}
	svc := NewUserService(repo, nil, nil, nil)
	err := svc.UpdateStatus(context.Background(), 7, StatusActive)
	require.Error(t, err)
	require.Equal(t, "REFUND_LOCKED_STATUS_PROTECTED", infraerrors.Reason(err))
	require.Empty(t, repo.updateFields)
}

func TestDonateLockedAccountRefundClearsCreditsWithoutGatewayRefund(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().
		SetEmail("alice.donor@example.com").
		SetPasswordHash("hash").
		SetUsername("alice").
		SetBalance(100).
		SetTotalRecharged(100).
		SetStatus(StatusRefundLocked).
		Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("donation-provider").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	orderID := createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0, paymentorder.RechargeBonusStatusNone)
	grant, err := client.UserLimitedCreditGrant.Create().SetUserID(userRow.ID).SetSourceType(LimitedCreditSourceResetRebate).SetInitialAmount(15).SetUsedAmount(0).SetFrozenAmount(0).SetExpiresAt(time.Now().Add(24 * time.Hour)).SetStatus(LimitedCreditStatusActive).Save(ctx)
	require.NoError(t, err)

	svc := &PaymentService{entClient: client, authCacheInvalidator: &accountRefundFenceStub{}}
	quote, err := svc.buildAccountRefundQuoteWithClient(ctx, client, &User{ID: userRow.ID, Balance: 100, TotalRecharged: 100, Status: StatusRefundLocked})
	require.NoError(t, err)
	require.True(t, quote.Eligible)
	record := &AccountRefundRecord{
		RefundID: "donation-refund-1", UserID: userRow.ID, State: AccountRefundStateManualReview,
		PreviousUserStatus: StatusActive, Quote: quote, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, writeAccountRefundAudit(ctx, client, record))

	donated, err := svc.DonateLockedAccountRefund(ctx, record.RefundID, userRow.ID, quote.QuoteHash)
	require.NoError(t, err)
	require.Equal(t, AccountRefundStateDonated, donated.State)
	require.NotNil(t, donated.Donation)
	require.Equal(t, "alice", donated.Donation.Username)
	require.Equal(t, "a***@example.com", donated.Donation.MaskedEmail)
	require.InDelta(t, 100, donated.Donation.Amount, 1e-8)

	updatedUser, err := client.User.Get(ctx, userRow.ID)
	require.NoError(t, err)
	require.Zero(t, updatedUser.Balance)
	require.Equal(t, StatusRefundLocked, updatedUser.Status)
	updatedGrant, err := client.UserLimitedCreditGrant.Get(ctx, grant.ID)
	require.NoError(t, err)
	require.Equal(t, LimitedCreditStatusDepleted, updatedGrant.Status)
	require.InDelta(t, 15, updatedGrant.UsedAmount, 1e-8)
	updatedOrder, err := client.PaymentOrder.Get(ctx, orderID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, updatedOrder.Status)

	donations, err := svc.ListAccountRefundDonations(ctx)
	require.NoError(t, err)
	require.Len(t, donations, 1)
	require.Equal(t, *donated.Donation, donations[0])

	_, err = svc.DonateLockedAccountRefund(ctx, record.RefundID, userRow.ID, quote.QuoteHash)
	require.Error(t, err)
	donations, err = svc.ListAccountRefundDonations(ctx)
	require.NoError(t, err)
	require.Len(t, donations, 1)
}

func TestMaskAccountRefundDonationEmail(t *testing.T) {
	require.Equal(t, "a***@example.com", maskAccountRefundDonationEmail("alice@example.com"))
	require.Equal(t, "x***@example.com", maskAccountRefundDonationEmail("x@example.com"))
	require.Equal(t, "***", maskAccountRefundDonationEmail("invalid"))
	require.Equal(t, "匿名用户", accountRefundDonationUsername(" "))
}

func createAccountRefundTestOrder(t *testing.T, ctx context.Context, client *dbent.Client, userID int64, email string, providerID int64, amount, paid, bonusRate float64, bonusStatus paymentorder.RechargeBonusStatus) int64 {
	t.Helper()
	sequence := accountRefundTestOrderSequence.Add(1)
	providerIDText := fmt.Sprintf("%d", providerID)
	now := time.Now().UTC()
	order, err := client.PaymentOrder.Create().
		SetUserID(userID).
		SetUserEmail(email).
		SetUserName("refund-user").
		SetAmount(amount).
		SetPayAmount(paid).
		SetFeeRate(0).
		SetRechargeCode(fmt.Sprintf("REFUND-%d-%d", userID, sequence)).
		SetOutTradeNo(fmt.Sprintf("refund_%d_%d", userID, sequence)).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo(fmt.Sprintf("trade_%d", sequence)).
		SetOrderType(payment.OrderTypeBalance).
		SetProviderInstanceID(providerIDText).
		SetProviderKey(payment.TypeAlipay).
		SetProviderSnapshot(map[string]any{"currency": "CNY"}).
		SetRechargeBonusRate(bonusRate).
		SetRechargeBonusAmount(amount * bonusRate).
		SetRechargeBonusStatus(bonusStatus).
		SetStatus(OrderStatusCompleted).
		SetExpiresAt(now.Add(time.Hour)).
		SetPaidAt(now).
		SetCompletedAt(now).
		SetClientIP("127.0.0.1").
		SetSrcHost("example.com").
		Save(ctx)
	require.NoError(t, err)
	return order.ID
}

var accountRefundTestOrderSequence atomic.Int64

type accountRefundFenceStub struct {
	acquireCalls int
}

func (f *accountRefundFenceStub) InvalidateAuthCacheByKey(context.Context, string)    {}
func (f *accountRefundFenceStub) InvalidateAuthCacheByUserID(context.Context, int64)  {}
func (f *accountRefundFenceStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}
func (f *accountRefundFenceStub) AcquireRefundBillingLock(context.Context, int64, string) error {
	f.acquireCalls++
	return nil
}
func (f *accountRefundFenceStub) ReleaseRefundBillingLock(context.Context, int64, string) error {
	return nil
}
func (f *accountRefundFenceStub) IsRefundBillingLocked(context.Context, int64) (bool, error) {
	return true, nil
}
