package service

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/shopspring/decimal"
)

const (
	RechargeBonusValidityDays = 30

	RechargeBonusStatusNone         = "none"
	RechargeBonusStatusEligible     = "eligible"
	RechargeBonusStatusGranted      = "granted"
	RechargeBonusStatusLimitReached = "limit_reached"
)

var rechargeBonusDatabaseMax = decimal.RequireFromString("999999999999.99999999")

// RechargeBonusTier 定义一个金额区间内的线性赠送比例。
type RechargeBonusTier = domain.RechargeBonusTier

// RechargeBonusCampaignInput 表示创建或更新活动时的完整配置。
type RechargeBonusCampaignInput struct {
	Name               string              `json:"name"`
	Description        string              `json:"description"`
	StartAt            time.Time           `json:"start_at"`
	EndAt              time.Time           `json:"end_at"`
	ParticipationLimit int                 `json:"participation_limit"`
	Tiers              []RechargeBonusTier `json:"tiers"`
}

// RechargeBonusQuote 表示永久到账额度对应的赠送试算结果。
type RechargeBonusQuote struct {
	Matched bool    `json:"matched"`
	Rate    float64 `json:"rate"`
	Amount  float64 `json:"amount"`
}

func calculateRechargeBonusChecked(creditedAmount float64, tiers []RechargeBonusTier) (RechargeBonusQuote, error) {
	if !isFiniteRechargeBonusNumber(creditedAmount) || creditedAmount < 0 || len(tiers) == 0 {
		return RechargeBonusQuote{}, nil
	}

	sorted := append([]RechargeBonusTier(nil), tiers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].MinAmount < sorted[j].MinAmount
	})

	amount := decimal.NewFromFloat(creditedAmount)
	for i, tier := range sorted {
		minAmount := decimal.NewFromFloat(tier.MinAmount)
		maxAmount := decimal.NewFromFloat(tier.MaxAmount)
		if amount.LessThan(minAmount) {
			continue
		}
		isLast := i == len(sorted)-1
		if amount.GreaterThan(maxAmount) || (!isLast && amount.Equal(maxAmount)) {
			continue
		}

		minRate := decimal.NewFromFloat(tier.MinRate)
		maxRate := decimal.NewFromFloat(tier.MaxRate)
		position := amount.Sub(minAmount).Div(maxAmount.Sub(minAmount))
		rate := minRate.Add(position.Mul(maxRate.Sub(minRate)))
		bonus := amount.Mul(rate).Div(decimal.NewFromInt(100)).Round(8)
		if rate.GreaterThan(rechargeBonusDatabaseMax) || bonus.GreaterThan(rechargeBonusDatabaseMax) {
			return RechargeBonusQuote{}, fmt.Errorf("recharge bonus result exceeds database amount range")
		}

		return RechargeBonusQuote{
			Matched: true,
			Rate:    rate.InexactFloat64(),
			Amount:  bonus.InexactFloat64(),
		}, nil
	}

	return RechargeBonusQuote{}, nil
}

// ValidateRechargeBonusCampaign 校验活动字段、时间范围和阶梯配置。
func ValidateRechargeBonusCampaign(input RechargeBonusCampaignInput) error {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return fmt.Errorf("campaign name is required")
	}
	if utf8.RuneCountInString(name) > 100 {
		return fmt.Errorf("campaign name must not exceed 100 characters")
	}
	if utf8.RuneCountInString(input.Description) > 1000 {
		return fmt.Errorf("campaign description must not exceed 1000 characters")
	}
	if input.StartAt.IsZero() || input.EndAt.IsZero() || !input.EndAt.After(input.StartAt) {
		return fmt.Errorf("campaign end time must be after start time")
	}
	if input.ParticipationLimit < 0 {
		return fmt.Errorf("participation limit must not be negative")
	}
	if len(input.Tiers) == 0 {
		return fmt.Errorf("at least one recharge bonus tier is required")
	}

	sorted := append([]RechargeBonusTier(nil), input.Tiers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].MinAmount < sorted[j].MinAmount
	})
	for i, tier := range sorted {
		if !isFiniteRechargeBonusNumber(tier.MinAmount) || !isFiniteRechargeBonusNumber(tier.MaxAmount) ||
			!isFiniteRechargeBonusNumber(tier.MinRate) || !isFiniteRechargeBonusNumber(tier.MaxRate) {
			return fmt.Errorf("recharge bonus tier values must be finite")
		}
		if tier.MinAmount < 0 || tier.MaxAmount <= tier.MinAmount {
			return fmt.Errorf("recharge bonus tier amount range is invalid")
		}
		if tier.MinRate < 0 || tier.MaxRate < 0 {
			return fmt.Errorf("recharge bonus tier rates must not be negative")
		}
		if i > 0 && tier.MinAmount < sorted[i-1].MaxAmount {
			return fmt.Errorf("recharge bonus tier ranges must not overlap")
		}
	}

	return nil
}

func isFiniteRechargeBonusNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
