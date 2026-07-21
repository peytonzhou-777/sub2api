//go:build unit

package handler

import (
	"encoding/json"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCheckoutInfoResponse_RechargeBonusActivityContract(t *testing.T) {
	remaining := 1
	startAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	response := checkoutInfoResponse{
		RechargeBonusActivity: &service.RechargeBonusCampaignOffer{
			RechargeBonusCampaign: service.RechargeBonusCampaign{
				ID:                 9,
				Name:               "暑期活动",
				Description:        "第一行\n第二行",
				StartAt:            startAt,
				EndAt:              startAt.Add(24 * time.Hour),
				ParticipationLimit: 2,
				Tiers: []service.RechargeBonusTier{
					{MinAmount: 10, MaxAmount: 100, MinRate: 5, MaxRate: 10},
				},
				Status: service.RechargeBonusCampaignStatusActive,
			},
			CompletedCount: 1,
			RemainingCount: &remaining,
			ValidityDays:   service.RechargeBonusValidityDays,
		},
	}

	payload, err := json.Marshal(response)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	activity, ok := decoded["recharge_bonus_activity"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "暑期活动", activity["name"])
	require.Equal(t, "第一行\n第二行", activity["description"])
	require.Equal(t, float64(30), activity["validity_days"])
	require.Equal(t, float64(1), activity["remaining_count"])
	require.Equal(t, "2026-07-01T00:00:00Z", activity["start_at"])
}

func TestPaymentOrderResponse_RechargeBonusSnapshotContract(t *testing.T) {
	campaignID := int64(9)
	campaignName := "暑期活动"
	expiresAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	order := &dbent.PaymentOrder{
		ID:                        88,
		RechargeBonusCampaignID:   &campaignID,
		RechargeBonusCampaignName: &campaignName,
		RechargeBonusRate:         7.5,
		RechargeBonusAmount:       22.5,
		RechargeBonusStatus:       service.RechargeBonusStatusGranted,
		RechargeBonusExpiresAt:    &expiresAt,
	}

	result := sanitizePaymentOrderForResponse(order)
	require.NotNil(t, result)
	require.NotNil(t, result.RechargeBonus)
	require.Equal(t, campaignID, result.RechargeBonus.CampaignID)
	require.Equal(t, campaignName, result.RechargeBonus.CampaignName)
	require.Equal(t, service.RechargeBonusStatusGranted, result.RechargeBonus.Status)
	require.Equal(t, expiresAt, *result.RechargeBonus.ExpiresAt)
}

func TestCreateOrderResponse_RechargeBonusSnapshotContract(t *testing.T) {
	response := service.CreateOrderResponse{
		OrderID: 88,
		RechargeBonus: &service.RechargeBonusOrderSnapshot{
			CampaignID:   9,
			CampaignName: "暑期活动",
			Rate:         7.5,
			Amount:       22.5,
			Status:       service.RechargeBonusStatusEligible,
			ValidityDays: service.RechargeBonusValidityDays,
		},
	}

	payload, err := json.Marshal(response)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(payload, &decoded))
	snapshot, ok := decoded["recharge_bonus"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(9), snapshot["campaign_id"])
	require.Equal(t, "暑期活动", snapshot["campaign_name"])
	require.Equal(t, service.RechargeBonusStatusEligible, snapshot["status"])
}
