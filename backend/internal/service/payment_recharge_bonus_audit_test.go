//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/stretchr/testify/require"
)

func TestPaymentService_RechargeBonusCampaignWritesAuditLogs(t *testing.T) {
	bonusService, client := newRechargeBonusTestService(t)
	paymentService := &PaymentService{
		entClient:            client,
		rechargeBonusService: bonusService,
	}
	ctx := context.Background()
	startAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	input := validRechargeBonusCampaignInput(startAt, startAt.Add(24*time.Hour))

	created, err := paymentService.CreateRechargeBonusCampaign(ctx, input)
	require.NoError(t, err)

	input.Description = "更新后的活动描述"
	_, err = paymentService.UpdateRechargeBonusCampaign(ctx, created.ID, input)
	require.NoError(t, err)
	require.NoError(t, paymentService.DeleteRechargeBonusCampaign(ctx, created.ID))

	logs, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(fmt.Sprintf("campaign:%d", created.ID))).
		Order(paymentauditlog.ByCreatedAt()).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, logs, 3)
	require.Equal(t, "RECHARGE_BONUS_CAMPAIGN_CREATED", logs[0].Action)
	require.Equal(t, "RECHARGE_BONUS_CAMPAIGN_UPDATED", logs[1].Action)
	require.Equal(t, "RECHARGE_BONUS_CAMPAIGN_DELETED", logs[2].Action)
	for _, item := range logs {
		require.Equal(t, "admin", item.Operator)
	}
}

func TestPaymentService_RechargeBonusCampaignRollsBackWhenAuditFails(t *testing.T) {
	bonusService, client := newRechargeBonusTestService(t)
	client.PaymentAuditLog.Use(func(next dbent.Mutator) dbent.Mutator {
		return dbent.MutateFunc(func(context.Context, dbent.Mutation) (dbent.Value, error) {
			return nil, errors.New("audit write failed")
		})
	})
	paymentService := &PaymentService{
		entClient:            client,
		rechargeBonusService: bonusService,
	}
	ctx := context.Background()
	startAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)

	_, err := paymentService.CreateRechargeBonusCampaign(
		ctx,
		validRechargeBonusCampaignInput(startAt, startAt.Add(24*time.Hour)),
	)
	require.ErrorContains(t, err, "audit write failed")
	count, countErr := client.RechargeBonusCampaign.Query().Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, count)
}
