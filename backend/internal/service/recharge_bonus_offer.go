package service

import (
	"context"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbcampaign "github.com/Wei-Shaw/sub2api/ent/rechargebonuscampaign"
	dbparticipation "github.com/Wei-Shaw/sub2api/ent/rechargebonusparticipation"
)

// RechargeBonusCampaignOffer 表示充值页当前可展示的活动及用户参与进度。
type RechargeBonusCampaignOffer struct {
	RechargeBonusCampaign
	CompletedCount int  `json:"completed_count"`
	RemainingCount *int `json:"remaining_count"`
	ValidityDays   int  `json:"validity_days"`
}

// RechargeBonusOrderSnapshot 表示订单创建时固化的赠送承诺。
type RechargeBonusOrderSnapshot struct {
	CampaignID   int64      `json:"campaign_id"`
	CampaignName string     `json:"campaign_name"`
	Rate         float64    `json:"rate"`
	Amount       float64    `json:"amount"`
	Status       string     `json:"status"`
	ValidityDays int        `json:"validity_days"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// GetActiveCampaignOffer 返回指定时刻生效的活动，即使用户次数已经用完也继续展示。
func (s *RechargeBonusService) GetActiveCampaignOffer(ctx context.Context, userID int64, at time.Time) (*RechargeBonusCampaignOffer, error) {
	row, err := s.findCampaignAt(ctx, at)
	if err != nil || row == nil {
		return nil, err
	}
	completedCount, err := s.getCompletedParticipationCount(ctx, row.ID, userID)
	if err != nil {
		return nil, err
	}

	campaign := rechargeBonusCampaignFromEntity(row, at.UTC())
	offer := &RechargeBonusCampaignOffer{
		RechargeBonusCampaign: *campaign,
		CompletedCount:        completedCount,
		ValidityDays:          RechargeBonusValidityDays,
	}
	if row.ParticipationLimit > 0 {
		remaining := max(row.ParticipationLimit-completedCount, 0)
		offer.RemainingCount = &remaining
	}
	return offer, nil
}

// QuoteOrder 按订单创建时刻和永久到账额度生成赠送快照。
func (s *RechargeBonusService) QuoteOrder(ctx context.Context, userID int64, creditedAmount float64, createdAt time.Time) (*RechargeBonusOrderSnapshot, error) {
	row, err := s.findCampaignAt(ctx, createdAt)
	if err != nil || row == nil {
		return nil, err
	}
	completedCount, err := s.getCompletedParticipationCount(ctx, row.ID, userID)
	if err != nil {
		return nil, err
	}
	if row.ParticipationLimit > 0 && completedCount >= row.ParticipationLimit {
		return nil, nil
	}
	quote, err := calculateRechargeBonusChecked(creditedAmount, row.Tiers)
	if err != nil {
		return nil, fmt.Errorf("calculate recharge bonus quote: %w", err)
	}
	if !quote.Matched || quote.Amount <= 0 {
		return nil, nil
	}
	return &RechargeBonusOrderSnapshot{
		CampaignID:   row.ID,
		CampaignName: row.Name,
		Rate:         quote.Rate,
		Amount:       quote.Amount,
		Status:       RechargeBonusStatusEligible,
		ValidityDays: RechargeBonusValidityDays,
	}, nil
}

func (s *RechargeBonusService) findCampaignAt(ctx context.Context, at time.Time) (*dbent.RechargeBonusCampaign, error) {
	client := rechargeBonusClientFromContext(ctx, s.client)
	row, err := client.RechargeBonusCampaign.Query().
		Where(
			dbcampaign.StartAtLTE(at.UTC()),
			dbcampaign.EndAtGT(at.UTC()),
		).
		First(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find active recharge bonus campaign: %w", err)
	}
	return row, nil
}

func (s *RechargeBonusService) getCompletedParticipationCount(ctx context.Context, campaignID, userID int64) (int, error) {
	client := rechargeBonusClientFromContext(ctx, s.client)
	row, err := client.RechargeBonusParticipation.Query().
		Where(
			dbparticipation.CampaignIDEQ(campaignID),
			dbparticipation.UserIDEQ(userID),
		).
		Only(ctx)
	if dbent.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get recharge bonus participation: %w", err)
	}
	return row.CompletedCount, nil
}

func rechargeBonusClientFromContext(ctx context.Context, fallback *dbent.Client) *dbent.Client {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return tx.Client()
	}
	return fallback
}
