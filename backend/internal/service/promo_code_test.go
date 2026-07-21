package service

import "testing"

func TestNormalizePromoReward(t *testing.T) {
	tests := []struct {
		name         string
		rewardType   string
		validityDays int
		wantType     string
		wantDays     int
		wantErr      bool
	}{
		{name: "legacy empty type defaults to balance", rewardType: "", validityDays: 99, wantType: PromoCodeRewardTypeBalance, wantDays: 0},
		{name: "balance clears validity", rewardType: PromoCodeRewardTypeBalance, validityDays: 30, wantType: PromoCodeRewardTypeBalance, wantDays: 0},
		{name: "limited credit keeps validity", rewardType: PromoCodeRewardTypeLimitedCredit, validityDays: 30, wantType: PromoCodeRewardTypeLimitedCredit, wantDays: 30},
		{name: "limited credit requires positive validity", rewardType: PromoCodeRewardTypeLimitedCredit, validityDays: 0, wantErr: true},
		{name: "limited credit has maximum validity", rewardType: PromoCodeRewardTypeLimitedCredit, validityDays: 36501, wantErr: true},
		{name: "unknown type is rejected", rewardType: "unknown", validityDays: 0, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotType, gotDays, err := NormalizePromoReward(tt.rewardType, tt.validityDays)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizePromoReward() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if gotType != tt.wantType || gotDays != tt.wantDays {
				t.Fatalf("NormalizePromoReward() = (%q, %d), want (%q, %d)", gotType, gotDays, tt.wantType, tt.wantDays)
			}
		})
	}
}

func TestValidatePromoReward(t *testing.T) {
	tests := []struct {
		name         string
		rewardType   string
		amount       float64
		validityDays int
		wantErr      bool
	}{
		{name: "permanent balance may be zero", rewardType: PromoCodeRewardTypeBalance, amount: 0},
		{name: "limited credit requires positive amount", rewardType: PromoCodeRewardTypeLimitedCredit, amount: 0, validityDays: 7, wantErr: true},
		{name: "limited credit accepts positive amount", rewardType: PromoCodeRewardTypeLimitedCredit, amount: 1.5, validityDays: 7},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePromoReward(tt.rewardType, tt.amount, tt.validityDays)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidatePromoReward() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
