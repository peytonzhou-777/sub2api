package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/ent/securitydepositaccount"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSecurityDepositPaymentFulfillmentCreditsPaidBucketExactlyOnce(t *testing.T) {
	ctx := context.Background()
	client := newSecurityDepositFulfillmentTestClient(t)
	user, err := client.User.Create().
		SetEmail("security-deposit@example.com").
		SetPasswordHash("hash").
		SetUsername("security-deposit-user").
		SetBalance(19.5).
		Save(ctx)
	require.NoError(t, err)
	group, err := client.Group.Create().SetName("保证金分组").SetPlatform("openai").Save(ctx)
	require.NoError(t, err)
	paidAt := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(102).
		SetFeeRate(2).
		SetRechargeCode("SD-ORDER").
		SetOutTradeNo("security_deposit_fulfillment_1").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-security-deposit-1").
		SetOrderType(payment.OrderTypeSecurityDeposit).
		SetStatus(OrderStatusPaid).
		SetPaidAt(paidAt).
		SetProviderSnapshot(map[string]any{"security_deposit": SecurityDepositOrderSnapshot{
			SchemaVersion: 1, GroupID: group.ID, GroupName: group.Name, AgreementID: 1,
			PolicyVersion: "v1", ContentHash: "hash", BaseRequiredCents: 10000,
			RiskMultiplier: 1, RequiredCents: 10000, PrincipalCents: 10000,
			FreezeHours: 24, Currency: "CNY", ProviderRefundEnabled: true,
		}}).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	service := &PaymentService{entClient: client}
	require.NoError(t, service.ExecuteSecurityDepositFulfillment(ctx, order.ID))
	require.NoError(t, service.ExecuteSecurityDepositFulfillment(ctx, order.ID))

	account, err := client.SecurityDepositAccount.Query().Where(
		securitydepositaccount.UserIDEQ(user.ID),
		securitydepositaccount.BucketTypeEQ(securitydepositaccount.BucketTypePaid),
	).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(10000), account.BalanceCents)
	require.Equal(t, 1, mustSecurityDepositLotCount(t, ctx, client))
	require.Equal(t, 1, mustSecurityDepositLedgerCount(t, ctx, client))
	lot, err := client.SecurityDepositLot.Query().Only(ctx)
	require.NoError(t, err)
	require.NotNil(t, lot.LockedUntil)
	require.Equal(t, paidAt.Add(24*time.Hour), *lot.LockedUntil)
	updatedUser, err := client.User.Get(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, 19.5, updatedUser.Balance)
	updatedOrder, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, updatedOrder.Status)
}

func mustSecurityDepositLotCount(t *testing.T, ctx context.Context, client *dbent.Client) int {
	t.Helper()
	count, err := client.SecurityDepositLot.Query().Count(ctx)
	require.NoError(t, err)
	return count
}

func mustSecurityDepositLedgerCount(t *testing.T, ctx context.Context, client *dbent.Client) int {
	t.Helper()
	count, err := client.SecurityDepositLedger.Query().Count(ctx)
	require.NoError(t, err)
	return count
}

func newSecurityDepositFulfillmentTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	dsn := fmt.Sprintf("file:security_deposit_fulfillment_%d?mode=memory&cache=shared&_fk=1", time.Now().UnixNano())
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	driver := entsql.OpenDB(dialect.SQLite, database)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(driver)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}
