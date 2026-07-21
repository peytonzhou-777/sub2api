//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

type paymentRechargeBonusLimitedCreditRepo struct {
	fail   bool
	grants []*LimitedCreditGrant
}

func (r *paymentRechargeBonusLimitedCreditRepo) CreateGrant(_ context.Context, grant *LimitedCreditGrant) (*LimitedCreditGrant, error) {
	if r.fail {
		return nil, errors.New("limited credit grant failed")
	}
	copyGrant := *grant
	copyGrant.ID = int64(len(r.grants) + 1)
	r.grants = append(r.grants, &copyGrant)
	return &copyGrant, nil
}

func (r *paymentRechargeBonusLimitedCreditRepo) CreateGrantsIndependent(context.Context, []*LimitedCreditGrant) ([]LimitedCreditGrant, error) {
	panic("unexpected CreateGrantsIndependent call")
}

func (r *paymentRechargeBonusLimitedCreditRepo) ListActiveByUser(context.Context, int64) ([]LimitedCreditGrant, error) {
	panic("unexpected ListActiveByUser call")
}

func (r *paymentRechargeBonusLimitedCreditRepo) GetAvailableAmount(context.Context, int64) (float64, error) {
	panic("unexpected GetAvailableAmount call")
}

func setupPaymentRechargeBonusFulfillment(t *testing.T, grantRepo *paymentRechargeBonusLimitedCreditRepo) (*PaymentService, *dbent.Client, *dbent.PaymentOrder, *RechargeBonusCampaign) {
	t.Helper()
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	bonusService := NewRechargeBonusService(client, NewLimitedCreditService(grantRepo), nil, nil)
	now := time.Now().UTC()
	campaign, err := bonusService.CreateCampaign(ctx, RechargeBonusCampaignInput{
		Name:               "履约赠送",
		Description:        "到账后赠送",
		StartAt:            now.Add(-time.Hour),
		EndAt:              now.Add(time.Hour),
		ParticipationLimit: 1,
		Tiers: []RechargeBonusTier{
			{MinAmount: 10, MaxAmount: 1000, MinRate: 10, MaxRate: 10},
		},
	})
	require.NoError(t, err)
	staleAt := time.Now().Add(-paymentFulfillmentLeaseDuration - time.Minute)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusRecharging, staleAt)
	order, err = client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType(payment.OrderTypeBalance).
		ClearPlanID().
		ClearSubscriptionGroupID().
		ClearSubscriptionDays().
		SetRechargeBonusCampaignID(campaign.ID).
		SetRechargeBonusCampaignName(campaign.Name).
		SetRechargeBonusRate(10).
		SetRechargeBonusAmount(10).
		SetRechargeBonusStatus(RechargeBonusStatusEligible).
		SetUpdatedAt(staleAt).
		Save(ctx)
	require.NoError(t, err)
	redeemRepo := &redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{
		order.RechargeCode: {
			ID:     501,
			Code:   order.RechargeCode,
			Type:   RedeemTypeBalance,
			Value:  order.Amount,
			Status: StatusUsed,
		},
	}}
	paymentService := &PaymentService{
		entClient:            client,
		redeemService:        &RedeemService{redeemRepo: redeemRepo},
		rechargeBonusService: bonusService,
	}
	return paymentService, client, order, campaign
}

func TestExecuteBalanceFulfillmentGrantsRechargeBonusBeforeCompleting(t *testing.T) {
	grantRepo := &paymentRechargeBonusLimitedCreditRepo{}
	svc, client, order, _ := setupPaymentRechargeBonusFulfillment(t, grantRepo)
	ctx := context.Background()

	require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, order.ID))
	require.Len(t, grantRepo.grants, 1)
	require.Equal(t, LimitedCreditSourceRechargeBonus, grantRepo.grants[0].SourceType)
	require.NotNil(t, grantRepo.grants[0].SourceID)
	require.Equal(t, order.ID, *grantRepo.grants[0].SourceID)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, RechargeBonusStatusGranted, string(reloaded.RechargeBonusStatus))
	grantedAudits, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.ActionEQ("RECHARGE_BONUS_GRANTED")).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, grantedAudits)
}

func TestExecuteBalanceFulfillmentRetriesBonusFailureWithoutRepeatingPermanentCredit(t *testing.T) {
	grantRepo := &paymentRechargeBonusLimitedCreditRepo{fail: true}
	svc, client, order, campaign := setupPaymentRechargeBonusFulfillment(t, grantRepo)
	ctx := context.Background()

	err := svc.ExecuteBalanceFulfillment(ctx, order.ID)
	require.Error(t, err)
	reloaded, getErr := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, OrderStatusFailed, reloaded.Status)
	count, countErr := client.RechargeBonusParticipation.Query().Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, count)

	failedAudits, auditErr := client.PaymentAuditLog.Query().
		Where(paymentauditlog.ActionEQ("RECHARGE_BONUS_FAILED")).
		Count(ctx)
	require.NoError(t, auditErr)
	require.Equal(t, 1, failedAudits)
	grantRepo.fail = false
	require.NoError(t, svc.RetryFulfillment(ctx, order.ID))
	reloaded, getErr = client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, getErr)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	require.Equal(t, RechargeBonusStatusGranted, string(reloaded.RechargeBonusStatus))
	require.Len(t, grantRepo.grants, 1)
	participation, partErr := client.RechargeBonusParticipation.Query().Only(ctx)
	require.NoError(t, partErr)
	require.Equal(t, campaign.ID, participation.CampaignID)
	require.Equal(t, 1, participation.CompletedCount)
	grantedAudits, auditErr := client.PaymentAuditLog.Query().
		Where(paymentauditlog.ActionEQ("RECHARGE_BONUS_GRANTED")).
		Count(ctx)
	require.NoError(t, auditErr)
	require.Equal(t, 1, grantedAudits)
}
