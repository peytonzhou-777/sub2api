//go:build unit

package service

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCalculateRechargeBonus_UsesLaterTierAtSharedBoundary(t *testing.T) {
	tiers := []RechargeBonusTier{
		{MinAmount: 10, MaxAmount: 100, MinRate: 2, MaxRate: 5},
		{MinAmount: 100, MaxAmount: 500, MinRate: 7, MaxRate: 11},
	}

	quote := calculateRechargeBonus(100, tiers)

	require.True(t, quote.Matched)
	require.InDelta(t, 7, quote.Rate, 0.000000001)
	require.InDelta(t, 7, quote.Amount, 0.000000001)
}

func TestCalculateRechargeBonus_InterpolatesWholeCreditedAmount(t *testing.T) {
	tiers := []RechargeBonusTier{
		{MinAmount: 100, MaxAmount: 500, MinRate: 5, MaxRate: 10},
		{MinAmount: 500, MaxAmount: 1000, MinRate: 10, MaxRate: 20},
	}

	tests := []struct {
		name       string
		amount     float64
		wantRate   float64
		wantAmount float64
	}{
		{name: "middle of first tier", amount: 300, wantRate: 7.5, wantAmount: 22.5},
		{name: "middle of final tier", amount: 750, wantRate: 15, wantAmount: 112.5},
		{name: "final upper boundary", amount: 1000, wantRate: 20, wantAmount: 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quote := calculateRechargeBonus(tt.amount, tiers)
			require.True(t, quote.Matched)
			require.InDelta(t, tt.wantRate, quote.Rate, 0.000000001)
			require.InDelta(t, tt.wantAmount, quote.Amount, 0.000000001)
		})
	}
}

func TestCalculateRechargeBonus_ReturnsNoBonusForGap(t *testing.T) {
	quote := calculateRechargeBonus(150, []RechargeBonusTier{
		{MinAmount: 10, MaxAmount: 100, MinRate: 5, MaxRate: 5},
		{MinAmount: 200, MaxAmount: 300, MinRate: 10, MaxRate: 10},
	})

	require.False(t, quote.Matched)
	require.Zero(t, quote.Amount)
}

func TestCalculateRechargeBonus_RoundsToEightDecimalPlaces(t *testing.T) {
	quote := calculateRechargeBonus(1, []RechargeBonusTier{

		{MinAmount: 0, MaxAmount: 2, MinRate: 33.333333333, MaxRate: 33.333333333},
	})

	require.True(t, quote.Matched)
	require.Equal(t, 0.33333333, quote.Amount)
}

func TestCalculateRechargeBonusChecked_RejectsDatabaseOverflow(t *testing.T) {
	quote, err := calculateRechargeBonusChecked(1_000_000_000_000, []RechargeBonusTier{
		{MinAmount: 0, MaxAmount: 2_000_000_000_000, MinRate: 100, MaxRate: 100},
	})

	require.Error(t, err)
	require.False(t, quote.Matched)
	require.Contains(t, err.Error(), "database amount range")
}

func TestValidateRechargeBonusCampaign_AllowsAdjacentAndDescendingRates(t *testing.T) {
	err := ValidateRechargeBonusCampaign(RechargeBonusCampaignInput{
		Name:               "暑期充值活动",
		Description:        "首充和复充均可参与\n限时额度有效 30 天",
		StartAt:            time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		EndAt:              time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		ParticipationLimit: 0,
		Tiers: []RechargeBonusTier{
			{MinAmount: 10, MaxAmount: 100, MinRate: 20, MaxRate: 10},
			{MinAmount: 100, MaxAmount: 500, MinRate: 8, MaxRate: 5},
		},
	})

	require.NoError(t, err)
}

func TestValidateRechargeBonusCampaign_RejectsInvalidConfiguration(t *testing.T) {
	base := RechargeBonusCampaignInput{
		Name:               "充值活动",
		StartAt:            time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		EndAt:              time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		ParticipationLimit: 1,
		Tiers: []RechargeBonusTier{
			{MinAmount: 10, MaxAmount: 100, MinRate: 5, MaxRate: 10},
		},
	}

	tests := []struct {
		name   string
		mutate func(*RechargeBonusCampaignInput)
	}{
		{name: "blank name", mutate: func(v *RechargeBonusCampaignInput) { v.Name = "   " }},
		{name: "long name", mutate: func(v *RechargeBonusCampaignInput) { v.Name = strings.Repeat("a", 101) }},
		{name: "long description", mutate: func(v *RechargeBonusCampaignInput) { v.Description = strings.Repeat("a", 1001) }},
		{name: "invalid time range", mutate: func(v *RechargeBonusCampaignInput) { v.EndAt = v.StartAt }},
		{name: "negative participation limit", mutate: func(v *RechargeBonusCampaignInput) { v.ParticipationLimit = -1 }},
		{name: "no tiers", mutate: func(v *RechargeBonusCampaignInput) { v.Tiers = nil }},
		{name: "overlapping tiers", mutate: func(v *RechargeBonusCampaignInput) {
			v.Tiers = append(v.Tiers, RechargeBonusTier{MinAmount: 99, MaxAmount: 200, MinRate: 10, MaxRate: 20})
		}},
		{name: "non finite amount", mutate: func(v *RechargeBonusCampaignInput) { v.Tiers[0].MaxAmount = math.Inf(1) }},
		{name: "negative rate", mutate: func(v *RechargeBonusCampaignInput) { v.Tiers[0].MinRate = -0.01 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := base
			input.Tiers = append([]RechargeBonusTier(nil), base.Tiers...)
			tt.mutate(&input)
			require.Error(t, ValidateRechargeBonusCampaign(input))
		})
	}
}
