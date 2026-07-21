package service

import (
	"fmt"
	"math"
	"time"
)

// PromoCode 注册优惠码
type PromoCode struct {
	ID           int64
	Code         string
	BonusAmount  float64
	RewardType   string
	ValidityDays int
	MaxUses      int
	UsedCount    int
	Status       string
	ExpiresAt    *time.Time
	Notes        string
	CreatedAt    time.Time
	UpdatedAt    time.Time

	// 关联
	UsageRecords []PromoCodeUsage
}

// PromoCodeUsage 优惠码使用记录
type PromoCodeUsage struct {
	ID           int64
	PromoCodeID  int64
	UserID       int64
	BonusAmount  float64
	RewardType   string
	ValidityDays int
	UsedAt       time.Time

	// 关联
	PromoCode *PromoCode
	User      *User
}

// CanUse 检查优惠码是否可用
func (p *PromoCode) CanUse() bool {
	if p.Status != PromoCodeStatusActive {
		return false
	}
	if p.ExpiresAt != nil && time.Now().After(*p.ExpiresAt) {
		return false
	}
	if p.MaxUses > 0 && p.UsedCount >= p.MaxUses {
		return false
	}
	return true
}

// IsExpired 检查是否已过期
func (p *PromoCode) IsExpired() bool {
	return p.ExpiresAt != nil && time.Now().After(*p.ExpiresAt)
}

// CreatePromoCodeInput 创建优惠码输入
type CreatePromoCodeInput struct {
	Code         string
	BonusAmount  float64
	RewardType   string
	ValidityDays int
	MaxUses      int
	ExpiresAt    *time.Time
	Notes        string
}

// UpdatePromoCodeInput 更新优惠码输入
type UpdatePromoCodeInput struct {
	Code         *string
	BonusAmount  *float64
	RewardType   *string
	ValidityDays *int
	MaxUses      *int
	Status       *string
	ExpiresAt    *time.Time
	Notes        *string
}

// NormalizePromoReward 将优惠码奖励类型和有效期规范化，兼容旧数据。
func NormalizePromoReward(rewardType string, validityDays int) (string, int, error) {
	if rewardType == "" {
		rewardType = PromoCodeRewardTypeBalance
	}
	switch rewardType {
	case PromoCodeRewardTypeBalance:
		return rewardType, 0, nil
	case PromoCodeRewardTypeLimitedCredit:
		if validityDays < 1 || validityDays > 36500 {
			return "", 0, fmt.Errorf("limited credit validity days must be between 1 and 36500")
		}
		return rewardType, validityDays, nil
	default:
		return "", 0, fmt.Errorf("unsupported promo reward type: %s", rewardType)
	}
}

// ValidatePromoReward 校验优惠码奖励金额、类型和有效期。
func ValidatePromoReward(rewardType string, amount float64, validityDays int) error {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0 {
		return fmt.Errorf("promo bonus amount must be a finite non-negative number")
	}
	normalizedType, _, err := NormalizePromoReward(rewardType, validityDays)
	if err != nil {
		return err
	}
	if normalizedType == PromoCodeRewardTypeLimitedCredit && amount <= 0 {
		return fmt.Errorf("limited credit amount must be greater than zero")
	}
	return nil
}
