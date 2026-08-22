//go:build unit

package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminAccountRefundBatchMatchesSingleQuoteAndSeparatesExecutionModes(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	automaticUser, err := client.User.Create().SetEmail("admin-refund-auto@example.com").SetPasswordHash("hash").SetUsername("auto").SetBalance(100).SetTotalRecharged(100).Save(ctx)
	require.NoError(t, err)
	manualUser, err := client.User.Create().SetEmail("admin-refund-manual@example.com").SetPasswordHash("hash").SetUsername("manual").SetBalance(50).SetTotalRecharged(50).Save(ctx)
	require.NoError(t, err)
	automaticProvider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("admin-refund-auto").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(false).Save(ctx)
	require.NoError(t, err)
	manualProvider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("admin-refund-manual").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(false).SetAllowUserRefund(false).Save(ctx)
	require.NoError(t, err)
	createAccountRefundTestOrder(t, ctx, client, automaticUser.ID, automaticUser.Email, automaticProvider.ID, 100, 100, 0, paymentorder.RechargeBonusStatusNone)
	createAccountRefundTestOrder(t, ctx, client, manualUser.ID, manualUser.Email, manualProvider.ID, 50, 50, 0, paymentorder.RechargeBonusStatusNone)

	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: automaticUser.ID, Balance: 100, TotalRecharged: 100, Status: StatusActive}}}
	single, err := svc.buildAccountRefundQuote(ctx, automaticUser.ID)
	require.NoError(t, err)
	require.Equal(t, AccountRefundCalculationVerified, single.CalculationStatus)
	require.False(t, single.SelfServiceEligible)
	require.Equal(t, AccountRefundAdminAutomatic, single.AdminExecutionMode)

	detail, err := svc.GetAdminAccountRefundDetail(ctx, automaticUser.ID)
	require.NoError(t, err)
	require.Equal(t, single.QuoteHash, detail.Quote.QuoteHash)
	require.Equal(t, single.GatewayTotals, detail.Item.RefundTotals)
	require.Contains(t, detail.Item.AvailableActions, "start")

	queryCount := 0
	client.Intercept(dbent.InterceptFunc(func(next dbent.Querier) dbent.Querier {
		return dbent.QuerierFunc(func(ctx context.Context, query dbent.Query) (dbent.Value, error) {
			queryCount++
			return next.Query(ctx, query)
		})
	}))
	summary, err := svc.GetAdminAccountRefundSummary(ctx)
	require.NoError(t, err)
	initialQueryCount := queryCount
	require.Positive(t, initialQueryCount)
	require.Equal(t, 2, summary.RefundableUsers)
	require.Equal(t, 1, summary.AutomaticUsers)
	require.Equal(t, 1, summary.ManualReviewUsers)
	require.InDelta(t, 150, summary.RefundableTotals["CNY"], 1e-8)
	require.InDelta(t, 100, summary.AutomaticTotals["CNY"], 1e-8)
	require.InDelta(t, 50, summary.ManualExternalTotals["CNY"], 1e-8)

	items, total, err := svc.ListAdminAccountRefunds(ctx, AdminAccountRefundListParams{Tab: AdminAccountRefundTabRefundable, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, int64(2), total)
	require.Len(t, items, 2)
	manualItems, manualTotal, err := svc.ListAdminAccountRefunds(ctx, AdminAccountRefundListParams{Tab: AdminAccountRefundTabManualReview, Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Equal(t, int64(1), manualTotal)
	require.Equal(t, manualUser.ID, manualItems[0].UserID)

	thirdUser, err := client.User.Create().SetEmail("admin-refund-third@example.com").SetPasswordHash("hash").SetUsername("third").SetBalance(25).SetTotalRecharged(25).Save(ctx)
	require.NoError(t, err)
	createAccountRefundTestOrder(t, ctx, client, thirdUser.ID, thirdUser.Email, automaticProvider.ID, 25, 25, 0, paymentorder.RechargeBonusStatusNone)
	queryCount = 0
	_, err = svc.GetAdminAccountRefundSummary(ctx)
	require.NoError(t, err)
	require.Equal(t, initialQueryCount, queryCount, "批量查询次数不应随用户数增长")
}

func TestAdminStartAccountRefundSupportsDisabledUserIdempotencyAndRestore(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("admin-refund-disabled@example.com").SetPasswordHash("hash").SetUsername("disabled").SetStatus(StatusDisabled).SetBalance(100).SetTotalRecharged(100).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("admin-refund-disabled").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(false).Save(ctx)
	require.NoError(t, err)
	createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 100, 100, 0, paymentorder.RechargeBonusStatusNone)

	fence := &accountRefundFenceStub{}
	svc := &PaymentService{
		entClient:            client,
		userRepo:             &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Email: userRow.Email, Username: userRow.Username, Balance: 100, TotalRecharged: 100, Status: StatusRefundLocked}},
		authCacheInvalidator: fence,
	}
	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.Equal(t, AccountRefundAdminAutomatic, quote.AdminExecutionMode)
	actor := AccountRefundActor{Type: "admin", ID: 7, Label: "admin:7", RequestID: "request-1"}
	started, err := svc.AdminStartAccountRefund(ctx, userRow.ID, AdminAccountRefundStartInput{QuoteHash: quote.QuoteHash}, "start-key-1", actor)
	require.NoError(t, err)
	require.Equal(t, AccountRefundStateReadyToConfirm, started.State)
	require.Equal(t, StatusDisabled, started.PreviousUserStatus)
	require.Positive(t, started.StateRevision)

	repeated, err := svc.AdminStartAccountRefund(ctx, userRow.ID, AdminAccountRefundStartInput{QuoteHash: quote.QuoteHash}, "start-key-1", actor)
	require.NoError(t, err)
	require.Equal(t, started.RefundID, repeated.RefundID)
	require.Equal(t, 1, fence.acquireCalls)

	_, err = svc.AdminCancelAccountRefundWithInput(ctx, userRow.ID, AdminAccountRefundActionInput{ExpectedStateRevision: started.StateRevision}, actor)
	require.NoError(t, err)
	restored, err := client.User.Get(ctx, userRow.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, restored.Status)
	require.Equal(t, 1, fence.releaseCalls)

	latestAudit, err := client.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDHasPrefix(accountRefundAuditPrefix)).Order(paymentauditlog.ByID()).All(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, latestAudit)
	require.Equal(t, "admin:7", latestAudit[len(latestAudit)-1].Operator)
}

func TestAdminAccountRefundActionRejectsStaleStateRevision(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	record := &AccountRefundRecord{RefundID: "stale-admin-refund", UserID: 42, State: AccountRefundStateDraining}
	require.NoError(t, writeAccountRefundAudit(ctx, client, record))
	svc := &PaymentService{entClient: client}

	_, err := svc.AdminAdvanceAccountRefund(ctx, 42, AdminAccountRefundActionInput{ExpectedStateRevision: record.StateRevision + 1}, AccountRefundActor{ID: 7})
	require.Error(t, err)
	require.Equal(t, "REFUND_STATE_CHANGED", infraerrors.Reason(err))
}

func TestAccountRefundQuoteKeepsCurrenciesSeparated(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("admin-refund-currencies@example.com").SetPasswordHash("hash").SetUsername("currencies").SetBalance(100).SetTotalRecharged(100).Save(ctx)
	require.NoError(t, err)
	provider, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeAlipay).SetName("admin-refund-currencies").SetConfig("{}").SetSupportedTypes("alipay").SetEnabled(true).SetRefundEnabled(true).SetAllowUserRefund(true).Save(ctx)
	require.NoError(t, err)
	createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 50, 50, 0, paymentorder.RechargeBonusStatusNone)
	usdOrderID := createAccountRefundTestOrder(t, ctx, client, userRow.ID, userRow.Email, provider.ID, 50, 50, 0, paymentorder.RechargeBonusStatusNone)
	_, err = client.PaymentOrder.UpdateOneID(usdOrderID).SetProviderSnapshot(map[string]any{"currency": "USD"}).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client, userRepo: &mockUserRepo{getByIDUser: &User{ID: userRow.ID, Balance: 100, TotalRecharged: 100, Status: StatusActive}}}

	quote, err := svc.buildAccountRefundQuote(ctx, userRow.ID)
	require.NoError(t, err)
	require.Equal(t, AccountRefundCalculationVerified, quote.CalculationStatus)
	require.True(t, quote.Eligible)
	require.InDelta(t, 50, quote.GatewayTotals["CNY"], 1e-8)
	require.InDelta(t, 50, quote.GatewayTotals["USD"], 1e-8)
}

func TestTerminalRefundFenceFailureLeavesRecoverableAdminAction(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	userRow, err := client.User.Create().SetEmail("admin-refund-restore-fence@example.com").SetPasswordHash("hash").SetUsername("restore-fence").SetStatus(StatusRefundLocked).Save(ctx)
	require.NoError(t, err)
	record := &AccountRefundRecord{RefundID: "restore-fence-refund", UserID: userRow.ID, State: AccountRefundStateSucceeded, PreviousUserStatus: StatusActive}
	require.NoError(t, writeAccountRefundAudit(ctx, client, record))
	fence := &accountRefundFenceStub{releaseErr: context.DeadlineExceeded}
	svc := &PaymentService{entClient: client, authCacheInvalidator: fence}

	_, err = svc.GetAccountRefund(ctx, record.RefundID, userRow.ID)
	require.Error(t, err)
	latest, err := svc.latestAccountRefundForUser(ctx, userRow.ID)
	require.NoError(t, err)
	require.Equal(t, AccountRefundReviewAccessRestoreFailed, latest.ReviewReasonCode)
	actions := availableAdminAccountRefundActions(StatusActive, latest, nil)
	require.Contains(t, actions, "restore-access")

	fence.releaseErr = nil
	restored, err := svc.AdminRestoreAccountRefundAccess(ctx, userRow.ID, AdminAccountRefundActionInput{ExpectedStateRevision: latest.StateRevision}, AccountRefundActor{ID: 7, Label: "admin:7"})
	require.NoError(t, err)
	require.Empty(t, restored.ReviewReasonCode)
	require.Equal(t, 2, fence.releaseCalls)
}
