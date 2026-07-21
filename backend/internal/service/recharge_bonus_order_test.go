//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestPaymentService_CreateBalanceOrderPersistsRechargeBonusSnapshot(t *testing.T) {
	bonusService, client := newRechargeBonusTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	campaign, err := bonusService.CreateCampaign(ctx, validRechargeBonusCampaignInput(now.Add(-time.Hour), now.Add(time.Hour)))
	require.NoError(t, err)
	userEntity, err := client.User.Create().
		SetEmail("order-snapshot@example.com").
		SetPasswordHash("hash").
		SetUsername("order-snapshot").
		Save(ctx)
	require.NoError(t, err)

	paymentService := NewPaymentService(client, payment.NewRegistry(), nil, nil, nil, nil, nil, nil, nil)
	paymentService.SetRechargeBonusService(bonusService)
	order, err := paymentService.createOrderInTx(
		ctx,
		CreateOrderRequest{
			UserID:      userEntity.ID,
			PaymentType: payment.TypeAlipay,
			OrderType:   payment.OrderTypeBalance,
			ClientIP:    "127.0.0.1",
			SrcHost:     "example.com",
		},
		&User{ID: userEntity.ID, Email: userEntity.Email, Username: userEntity.Username, Status: "active"},
		nil,
		&PaymentConfig{OrderTimeoutMin: 15, MaxPendingOrders: 3},
		300,
		300,
		0,
		300,
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, order.RechargeBonusCampaignID)
	require.Equal(t, campaign.ID, *order.RechargeBonusCampaignID)
	require.NotNil(t, order.RechargeBonusCampaignName)
	require.Equal(t, campaign.Name, *order.RechargeBonusCampaignName)
	require.InDelta(t, 7.5, order.RechargeBonusRate, 0.000000001)
	require.InDelta(t, 22.5, order.RechargeBonusAmount, 0.000000001)
	require.Equal(t, RechargeBonusStatusEligible, string(order.RechargeBonusStatus))
	require.False(t, order.CreatedAt.IsZero())
}

func TestPaymentService_CreateSubscriptionOrderDoesNotAttachRechargeBonus(t *testing.T) {
	bonusService, client := newRechargeBonusTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	_, err := bonusService.CreateCampaign(ctx, validRechargeBonusCampaignInput(now.Add(-time.Hour), now.Add(time.Hour)))
	require.NoError(t, err)
	userEntity, err := client.User.Create().
		SetEmail("order-subscription@example.com").
		SetPasswordHash("hash").
		SetUsername("order-subscription").
		Save(ctx)
	require.NoError(t, err)

	paymentService := NewPaymentService(client, payment.NewRegistry(), nil, nil, nil, nil, nil, nil, nil)
	paymentService.SetRechargeBonusService(bonusService)
	order, err := paymentService.createOrderInTx(
		ctx,
		CreateOrderRequest{
			UserID:      userEntity.ID,
			PaymentType: payment.TypeAlipay,
			OrderType:   payment.OrderTypeSubscription,
			ClientIP:    "127.0.0.1",
			SrcHost:     "example.com",
		},
		&User{ID: userEntity.ID, Email: userEntity.Email, Username: userEntity.Username, Status: "active"},
		nil,
		&PaymentConfig{OrderTimeoutMin: 15, MaxPendingOrders: 3},
		300,
		300,
		0,
		300,
		nil,
	)
	require.NoError(t, err)
	require.Nil(t, order.RechargeBonusCampaignID)
	require.Equal(t, RechargeBonusStatusNone, string(order.RechargeBonusStatus))
}
