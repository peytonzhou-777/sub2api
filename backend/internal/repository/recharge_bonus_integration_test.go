//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func createIntegrationRechargeBonusCampaign(t *testing.T, name string, startAt, endAt time.Time) *dbent.RechargeBonusCampaign {
	t.Helper()
	campaign, err := testEntClient(t).RechargeBonusCampaign.Create().
		SetName(name).
		SetDescription("").
		SetStartAt(startAt).
		SetEndAt(endAt).
		SetParticipationLimit(1).
		SetTiers([]domain.RechargeBonusTier{{MinAmount: 0, MaxAmount: 100, MinRate: 5, MaxRate: 5}}).
		Save(context.Background())
	require.NoError(t, err)
	return campaign
}

func TestRechargeBonusCampaignExclusionConstraintRejectsConcurrentOverlap(t *testing.T) {
	client := testEntClient(t)
	startAt := time.Now().UTC().Add(365 * 24 * time.Hour)
	endAt := startAt.Add(time.Hour)
	start := make(chan struct{})
	results := make(chan *dbent.RechargeBonusCampaign, 2)
	errors := make(chan error, 2)
	var waitGroup sync.WaitGroup

	for index := 0; index < 2; index++ {
		waitGroup.Add(1)
		go func(candidate int) {
			defer waitGroup.Done()
			<-start
			campaign, err := client.RechargeBonusCampaign.Create().
				SetName(fmt.Sprintf("并发活动-%d-%d", time.Now().UnixNano(), candidate)).
				SetDescription("").
				SetStartAt(startAt).
				SetEndAt(endAt).
				SetParticipationLimit(0).
				SetTiers([]domain.RechargeBonusTier{{MinAmount: 0, MaxAmount: 100, MinRate: 5, MaxRate: 5}}).
				Save(context.Background())
			results <- campaign
			errors <- err
		}(index)
	}
	close(start)
	waitGroup.Wait()
	close(results)
	close(errors)

	successes := make([]*dbent.RechargeBonusCampaign, 0, 1)
	failures := 0
	for campaign := range results {
		if campaign != nil {
			successes = append(successes, campaign)
		}
	}
	for err := range errors {
		if err != nil {
			failures++
		}
	}
	require.Len(t, successes, 1)
	require.Equal(t, 1, failures)
	require.NoError(t, client.RechargeBonusCampaign.DeleteOne(successes[0]).Exec(context.Background()))
}

func TestRechargeBonusParticipationConditionalUpdateClaimsFinalSlotOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	startAt := time.Now().UTC().Add(400 * 24 * time.Hour)
	campaign := createIntegrationRechargeBonusCampaign(t, "并发名额活动", startAt, startAt.Add(time.Hour))
	t.Cleanup(func() { _ = client.RechargeBonusCampaign.DeleteOneID(campaign.ID).Exec(ctx) })
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("recharge-bonus-%d@example.com", time.Now().UnixNano())).
		SetPasswordHash("hash").
		SetUsername("recharge-bonus-integration").
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.User.DeleteOneID(user.ID).Exec(ctx) })
	participation, err := client.RechargeBonusParticipation.Create().
		SetCampaignID(campaign.ID).
		SetUserID(user.ID).
		SetCompletedCount(0).
		Save(ctx)
	require.NoError(t, err)

	start := make(chan struct{})
	claimedRows := make(chan int64, 2)
	errors := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for index := 0; index < 2; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			result, execErr := integrationDB.ExecContext(ctx, `
				UPDATE recharge_bonus_participations
				SET completed_count = completed_count + 1, updated_at = NOW()
				WHERE id = $1 AND completed_count < 1
			`, participation.ID)
			if execErr != nil {
				errors <- execErr
				claimedRows <- 0
				return
			}
			count, rowsErr := result.RowsAffected()
			errors <- rowsErr
			claimedRows <- count
		}()
	}
	close(start)
	waitGroup.Wait()
	close(claimedRows)
	close(errors)

	var totalClaimed int64
	for err := range errors {
		require.NoError(t, err)
	}
	for count := range claimedRows {
		totalClaimed += count
	}
	require.Equal(t, int64(1), totalClaimed)
	reloaded, err := client.RechargeBonusParticipation.Get(ctx, participation.ID)
	require.NoError(t, err)
	require.Equal(t, 1, reloaded.CompletedCount)
}

func TestRechargeBonusOrderConditionalLockClaimsSameOrderOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("recharge-bonus-order-%d@example.com", time.Now().UnixNano())).
		SetPasswordHash("hash").
		SetUsername("recharge-bonus-order-integration").
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.User.DeleteOneID(user.ID).Exec(ctx) })

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(10).
		SetPayAmount(10).
		SetRechargeCode(fmt.Sprintf("RB-LOCK-%d", time.Now().UnixNano())).
		SetPaymentType("integration").
		SetPaymentTradeNo("").
		SetExpiresAt(time.Now().UTC().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("integration").
		SetRechargeBonusStatus("eligible").
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.PaymentOrder.DeleteOneID(order.ID).Exec(ctx) })

	start := make(chan struct{})
	claimedRows := make(chan int64, 2)
	errors := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for index := 0; index < 2; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			tx, txErr := integrationDB.BeginTx(ctx, nil)
			if txErr != nil {
				errors <- txErr
				claimedRows <- 0
				return
			}
			defer func() { _ = tx.Rollback() }()
			<-start
			result, execErr := tx.ExecContext(ctx, `
				UPDATE payment_orders
				SET recharge_bonus_status = recharge_bonus_status, updated_at = updated_at
				WHERE id = $1 AND recharge_bonus_status = 'eligible'
			`, order.ID)
			if execErr != nil {
				errors <- execErr
				claimedRows <- 0
				return
			}
			claimed, rowsErr := result.RowsAffected()
			if rowsErr == nil && claimed == 1 {
				_, rowsErr = tx.ExecContext(ctx, `
					UPDATE payment_orders SET recharge_bonus_status = 'granted' WHERE id = $1
				`, order.ID)
			}
			if rowsErr == nil {
				rowsErr = tx.Commit()
			}
			errors <- rowsErr
			claimedRows <- claimed
		}()
	}
	close(start)
	waitGroup.Wait()
	close(claimedRows)
	close(errors)

	var totalClaimed int64
	for err := range errors {
		require.NoError(t, err)
	}
	for count := range claimedRows {
		totalClaimed += count
	}
	require.Equal(t, int64(1), totalClaimed)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, "granted", string(reloaded.RechargeBonusStatus))
}
