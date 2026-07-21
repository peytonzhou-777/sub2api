//go:build unit

package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	dbparticipation "github.com/Wei-Shaw/sub2api/ent/rechargebonusparticipation"
	dbgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	dbledger "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditledger"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newRechargeBonusFulfillmentTestService(t *testing.T) (*service.RechargeBonusService, *dbent.Client) {
	t.Helper()
	dsn := fmt.Sprintf("file:recharge_bonus_fulfillment_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	limitedCreditService := service.NewLimitedCreditService(repository.NewLimitedCreditRepository(client, db))
	return service.NewRechargeBonusService(client, limitedCreditService, nil, nil), client
}

func createRechargeBonusFulfillmentCampaign(t *testing.T, ctx context.Context, svc *service.RechargeBonusService, limit int) *service.RechargeBonusCampaign {
	t.Helper()
	now := time.Now().UTC()
	campaign, err := svc.CreateCampaign(ctx, service.RechargeBonusCampaignInput{
		Name:               "到账赠送",
		Description:        "充值后赠送限时额度",
		StartAt:            now.Add(-time.Hour),
		EndAt:              now.Add(time.Hour),
		ParticipationLimit: limit,
		Tiers: []service.RechargeBonusTier{
			{MinAmount: 10, MaxAmount: 1000, MinRate: 10, MaxRate: 10},
		},
	})
	require.NoError(t, err)
	return campaign
}

func createRechargeBonusFulfillmentOrder(t *testing.T, ctx context.Context, client *dbent.Client, campaign *service.RechargeBonusCampaign, email string, existingUserID ...int64) *dbent.PaymentOrder {
	t.Helper()
	var user *dbent.User
	var err error
	if len(existingUserID) > 0 {
		user, err = client.User.Get(ctx, existingUserID[0])
	} else {
		user, err = client.User.Create().
			SetEmail(email).
			SetPasswordHash("hash").
			SetUsername(email).
			Save(ctx)
	}
	require.NoError(t, err)
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(100).
		SetPayAmount(100).
		SetFeeRate(0).
		SetRechargeCode("BONUS-" + email).
		SetOutTradeNo("OUT-" + email).
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("TRADE-" + email).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusRecharging).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("example.com").
		SetRechargeBonusCampaignID(campaign.ID).
		SetRechargeBonusCampaignName(campaign.Name).
		SetRechargeBonusRate(10).
		SetRechargeBonusAmount(10).
		SetRechargeBonusStatus(service.RechargeBonusStatusEligible).
		Save(ctx)
	require.NoError(t, err)
	return order
}

func TestRechargeBonusFulfillment_GrantsOnceWithLedgerAndThirtyDayExpiry(t *testing.T) {
	svc, client := newRechargeBonusFulfillmentTestService(t)
	ctx := context.Background()
	campaign := createRechargeBonusFulfillmentCampaign(t, ctx, svc, 2)
	order := createRechargeBonusFulfillmentOrder(t, ctx, client, campaign, "grant-once@example.com")
	startedAt := time.Now().UTC()

	result, err := svc.FulfillOrderBonus(ctx, order)
	require.NoError(t, err)
	require.Equal(t, service.RechargeBonusStatusGranted, result.Status)
	require.NotNil(t, result.ExpiresAt)
	require.WithinDuration(t, startedAt.AddDate(0, 0, service.RechargeBonusValidityDays), *result.ExpiresAt, 2*time.Second)

	grants, err := client.UserLimitedCreditGrant.Query().
		Where(
			dbgrant.SourceTypeEQ(service.LimitedCreditSourceRechargeBonus),
			dbgrant.SourceIDEQ(order.ID),
		).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, order.UserID, grants[0].UserID)
	require.InDelta(t, 10, grants[0].InitialAmount, 0.000000001)
	ledgerCount, err := client.UserLimitedCreditLedger.Query().
		Where(dbledger.GrantIDEQ(grants[0].ID), dbledger.EventTypeEQ("grant")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, ledgerCount)

	_, err = svc.FulfillOrderBonus(ctx, order)
	require.NoError(t, err)
	grantCount, err := client.UserLimitedCreditGrant.Query().
		Where(dbgrant.SourceTypeEQ(service.LimitedCreditSourceRechargeBonus), dbgrant.SourceIDEQ(order.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, grantCount)
	participation, err := client.RechargeBonusParticipation.Query().
		Where(dbparticipation.CampaignIDEQ(campaign.ID), dbparticipation.UserIDEQ(order.UserID)).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, participation.CompletedCount)
}

func TestRechargeBonusFulfillment_FirstCompletedOrderWinsFinalSlot(t *testing.T) {
	svc, client := newRechargeBonusFulfillmentTestService(t)
	ctx := context.Background()
	campaign := createRechargeBonusFulfillmentCampaign(t, ctx, svc, 1)
	first := createRechargeBonusFulfillmentOrder(t, ctx, client, campaign, "first-slot@example.com")
	second := createRechargeBonusFulfillmentOrder(t, ctx, client, campaign, "second-slot@example.com", first.UserID)

	firstResult, err := svc.FulfillOrderBonus(ctx, first)
	require.NoError(t, err)
	require.Equal(t, service.RechargeBonusStatusGranted, firstResult.Status)
	secondResult, err := svc.FulfillOrderBonus(ctx, second)
	require.NoError(t, err)
	require.Equal(t, service.RechargeBonusStatusLimitReached, secondResult.Status)

	secondGrantCount, err := client.UserLimitedCreditGrant.Query().
		Where(dbgrant.SourceTypeEQ(service.LimitedCreditSourceRechargeBonus), dbgrant.SourceIDEQ(second.ID)).
		Count(ctx)
	require.NoError(t, err)
	require.Zero(t, secondGrantCount)
}

func TestRechargeBonusFulfillment_UnlimitedCampaignGrantsEveryEligibleOrder(t *testing.T) {
	svc, client := newRechargeBonusFulfillmentTestService(t)
	ctx := context.Background()
	campaign := createRechargeBonusFulfillmentCampaign(t, ctx, svc, 0)
	first := createRechargeBonusFulfillmentOrder(t, ctx, client, campaign, "unlimited-first@example.com")
	second := createRechargeBonusFulfillmentOrder(t, ctx, client, campaign, "unlimited-second@example.com", first.UserID)

	firstResult, err := svc.FulfillOrderBonus(ctx, first)
	require.NoError(t, err)
	require.Equal(t, service.RechargeBonusStatusGranted, firstResult.Status)
	secondResult, err := svc.FulfillOrderBonus(ctx, second)
	require.NoError(t, err)
	require.Equal(t, service.RechargeBonusStatusGranted, secondResult.Status)

	grantCount, err := client.UserLimitedCreditGrant.Query().
		Where(dbgrant.SourceTypeEQ(service.LimitedCreditSourceRechargeBonus)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, grantCount)
	participation, err := client.RechargeBonusParticipation.Query().
		Where(dbparticipation.CampaignIDEQ(campaign.ID), dbparticipation.UserIDEQ(first.UserID)).
		Only(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, participation.CompletedCount)
}
