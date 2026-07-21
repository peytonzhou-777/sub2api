package service

import (
	"context"
	"fmt"
	"math"
	"time"
)

const (
	LimitedCreditSourceRedeemCode         = "redeem_code"
	LimitedCreditSourceDefaultUserSetting = "default_user_setting"
	LimitedCreditSourceRechargeBonus      = "recharge_bonus"
	LimitedCreditStatusActive             = "active"
	LimitedCreditStatusDepleted           = "depleted"
	LimitedCreditStatusExpired            = "expired"
)

// LimitedCreditGrant 表示用户持有的一份限时额度。
// 每份额度独立过期，扣费时按 expires_at 从近到远消耗。
type LimitedCreditGrant struct {
	ID            int64     `json:"id"`
	UserID        int64     `json:"user_id"`
	SourceType    string    `json:"source_type"`
	SourceID      *int64    `json:"source_id,omitempty"`
	SourceReason  string    `json:"source_reason,omitempty"`
	InitialAmount float64   `json:"initial_amount"`
	UsedAmount    float64   `json:"used_amount"`
	FrozenAmount  float64   `json:"frozen_amount"`
	ExpiresAt     time.Time `json:"expires_at"`
	Status        string    `json:"status"`
	Notes         string    `json:"notes,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ValidityDays  *int      `json:"validity_days,omitempty"`
}

// RemainingAmount 返回未使用完的总剩余额度，包含已冻结部分。
func (g LimitedCreditGrant) RemainingAmount() float64 {
	remaining := g.InitialAmount - g.UsedAmount
	if remaining < 0 {
		return 0
	}
	return remaining
}

// AvailableAmount 返回当前可立即扣费的额度，不包含冻结部分。
func (g LimitedCreditGrant) AvailableAmount() float64 {
	available := g.InitialAmount - g.UsedAmount - g.FrozenAmount
	if math.Abs(available) < 0.00000001 {
		return 0
	}
	if available < 0 {
		return 0
	}
	return available
}

// LimitedCreditSummary 汇总用户当前有效限时额度。
type LimitedCreditSummary struct {
	ActiveCount     int                  `json:"active_count"`
	AvailableAmount float64              `json:"available_amount"`
	FrozenAmount    float64              `json:"frozen_amount"`
	RemainingAmount float64              `json:"remaining_amount"`
	Grants          []LimitedCreditGrant `json:"grants,omitempty"`
}

// LimitedCreditRepository 定义限时额度持久化能力。
type LimitedCreditRepository interface {
	CreateGrant(ctx context.Context, grant *LimitedCreditGrant) (*LimitedCreditGrant, error)
	CreateGrantsIndependent(ctx context.Context, grants []*LimitedCreditGrant) ([]LimitedCreditGrant, error)
	ListActiveByUser(ctx context.Context, userID int64) ([]LimitedCreditGrant, error)
	GetAvailableAmount(ctx context.Context, userID int64) (float64, error)
}

// DefaultLimitedCreditGranter 定义新用户默认限时额度发放能力。
type DefaultLimitedCreditGranter interface {
	GrantFromDefaultSettings(ctx context.Context, userID int64, items []DefaultLimitedCreditSetting) ([]LimitedCreditGrant, error)
}

// LimitedCreditService 管理限时额度发放和查询。
type LimitedCreditService struct {
	repo LimitedCreditRepository
}

// NewLimitedCreditService 创建限时额度服务。
func NewLimitedCreditService(repo LimitedCreditRepository) *LimitedCreditService {
	return &LimitedCreditService{repo: repo}
}

// GrantFromRedeemCode 通过兑换码给用户发放一份限时额度。
func (s *LimitedCreditService) GrantFromRedeemCode(ctx context.Context, userID int64, code *RedeemCode) (*LimitedCreditGrant, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("limited credit service is not configured")
	}
	if code == nil {
		return nil, fmt.Errorf("redeem code is required")
	}
	if code.Value <= 0 {
		return nil, fmt.Errorf("limited credit value must be greater than zero")
	}
	validityDays := code.ValidityDays
	if validityDays <= 0 {
		validityDays = 30
	}
	sourceID := code.ID
	grant := &LimitedCreditGrant{
		UserID:        userID,
		SourceType:    LimitedCreditSourceRedeemCode,
		SourceID:      &sourceID,
		InitialAmount: code.Value,
		ExpiresAt:     time.Now().UTC().AddDate(0, 0, validityDays),
		Status:        LimitedCreditStatusActive,
		Notes:         fmt.Sprintf("通过兑换码 %s 兑换", code.Code),
	}
	return s.repo.CreateGrant(ctx, grant)
}

// GrantFromDefaultSettings 按用户默认设置一次性发放多份独立限时额度。
func (s *LimitedCreditService) GrantFromDefaultSettings(ctx context.Context, userID int64, items []DefaultLimitedCreditSetting) ([]LimitedCreditGrant, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("limited credit service is not configured")
	}
	if userID <= 0 {
		return nil, fmt.Errorf("user id must be greater than zero")
	}
	if err := validateDefaultLimitedCredits(items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []LimitedCreditGrant{}, nil
	}

	grantedAt := time.Now().UTC()
	grants := make([]*LimitedCreditGrant, 0, len(items))
	for _, item := range items {
		grants = append(grants, &LimitedCreditGrant{
			UserID:        userID,
			SourceType:    LimitedCreditSourceDefaultUserSetting,
			InitialAmount: item.Amount,
			ExpiresAt:     grantedAt.AddDate(0, 0, item.ValidityDays),
			Status:        LimitedCreditStatusActive,
			Notes:         "由用户默认设置自动发放",
		})
	}

	return s.repo.CreateGrantsIndependent(ctx, grants)
}

// GrantFromRechargeBonus 为一笔充值订单发放固定 30 天有效的限时额度。
func (s *LimitedCreditService) GrantFromRechargeBonus(
	ctx context.Context,
	userID int64,
	orderID int64,
	amount float64,
	grantedAt time.Time,
) (*LimitedCreditGrant, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("limited credit service is not configured")
	}
	if userID <= 0 || orderID <= 0 {
		return nil, fmt.Errorf("user id and order id must be greater than zero")
	}
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return nil, fmt.Errorf("recharge bonus amount must be a finite positive number")
	}
	sourceID := orderID
	return s.repo.CreateGrant(ctx, &LimitedCreditGrant{
		UserID:        userID,
		SourceType:    LimitedCreditSourceRechargeBonus,
		SourceID:      &sourceID,
		InitialAmount: amount,
		ExpiresAt:     grantedAt.UTC().AddDate(0, 0, RechargeBonusValidityDays),
		Status:        LimitedCreditStatusActive,
		Notes:         fmt.Sprintf("充值订单 %d 活动赠送", orderID),
	})
}

// ListActive 返回用户尚未过期且仍有可用或冻结额度的批次。
func (s *LimitedCreditService) ListActive(ctx context.Context, userID int64) ([]LimitedCreditGrant, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	return s.repo.ListActiveByUser(ctx, userID)
}

// GetSummary 汇总用户当前所有 active 限时额度。
func (s *LimitedCreditService) GetSummary(ctx context.Context, userID int64) (*LimitedCreditSummary, error) {
	grants, err := s.ListActive(ctx, userID)
	if err != nil {
		return nil, err
	}
	summary := &LimitedCreditSummary{Grants: grants}
	for _, grant := range grants {
		summary.ActiveCount++
		summary.AvailableAmount += grant.AvailableAmount()
		summary.FrozenAmount += grant.FrozenAmount
		summary.RemainingAmount += grant.RemainingAmount()
	}
	return summary, nil
}

// GetAvailableAmount 返回可立即扣费的限时额度总额。
func (s *LimitedCreditService) GetAvailableAmount(ctx context.Context, userID int64) (float64, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	return s.repo.GetAvailableAmount(ctx, userID)
}
