package domain

// RechargeBonusTier 定义一个金额区间内的线性赠送比例。
type RechargeBonusTier struct {
	MinAmount float64 `json:"min_amount"`
	MaxAmount float64 `json:"max_amount"`
	MinRate   float64 `json:"min_rate"`
	MaxRate   float64 `json:"max_rate"`
}
