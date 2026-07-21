//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRechargeBonusService_ActiveOfferIsVisibleEvenWhenUserReachedLimit(t *testing.T) {
	svc, client := newRechargeBonusTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	campaign, err := svc.CreateCampaign(ctx, validRechargeBonusCampaignInput(now.Add(-time.Hour), now.Add(time.Hour)))
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail("offer-limit@example.com").
		SetPasswordHash("hash").
		SetUsername("offer-limit").
		Save(ctx)
	require.NoError(t, err)
	_, err = client.RechargeBonusParticipation.Create().
		SetCampaignID(campaign.ID).
		SetUserID(user.ID).
		SetCompletedCount(campaign.ParticipationLimit).
		Save(ctx)
	require.NoError(t, err)

	offer, err := svc.GetActiveCampaignOffer(ctx, user.ID, now)
	require.NoError(t, err)
	require.NotNil(t, offer)
	require.Equal(t, campaign.Name, offer.Name)
	require.Equal(t, campaign.Description, offer.Description)
	require.Equal(t, campaign.ParticipationLimit, offer.CompletedCount)
	require.NotNil(t, offer.RemainingCount)
	require.Zero(t, *offer.RemainingCount)
	require.Equal(t, RechargeBonusValidityDays, offer.ValidityDays)

	snapshot, err := svc.QuoteOrder(ctx, user.ID, 300, now)
	require.NoError(t, err)
	require.Nil(t, snapshot)
}

func TestRechargeBonusService_QuoteOrderUsesActiveCampaignAndCreditedAmount(t *testing.T) {
	svc, client := newRechargeBonusTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	campaign, err := svc.CreateCampaign(ctx, validRechargeBonusCampaignInput(now.Add(-time.Hour), now.Add(time.Hour)))
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail("offer-quote@example.com").
		SetPasswordHash("hash").
		SetUsername("offer-quote").
		Save(ctx)
	require.NoError(t, err)

	snapshot, err := svc.QuoteOrder(ctx, user.ID, 300, now)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, campaign.ID, snapshot.CampaignID)
	require.Equal(t, campaign.Name, snapshot.CampaignName)
	require.InDelta(t, 7.5, snapshot.Rate, 0.000000001)
	require.InDelta(t, 22.5, snapshot.Amount, 0.000000001)
	require.Equal(t, RechargeBonusStatusEligible, snapshot.Status)
	require.Equal(t, RechargeBonusValidityDays, snapshot.ValidityDays)
}

func TestRechargeBonusService_QuoteOrderUsesCreatedAtAfterCampaignEnds(t *testing.T) {
	svc, client := newRechargeBonusTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	campaign, err := svc.CreateCampaign(ctx, validRechargeBonusCampaignInput(now.Add(-2*time.Hour), now.Add(-time.Hour)))
	require.NoError(t, err)
	user, err := client.User.Create().
		SetEmail("offer-created-at@example.com").
		SetPasswordHash("hash").
		SetUsername("offer-created-at").
		Save(ctx)
	require.NoError(t, err)

	snapshot, err := svc.QuoteOrder(ctx, user.ID, 100, campaign.StartAt.Add(time.Minute))
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, campaign.ID, snapshot.CampaignID)

	currentOffer, err := svc.GetActiveCampaignOffer(ctx, user.ID, now)
	require.NoError(t, err)
	require.Nil(t, currentOffer)
}
