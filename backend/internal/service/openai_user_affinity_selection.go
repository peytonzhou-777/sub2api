package service

import (
	"fmt"
	"math"
	"time"
)

// OpenAIUserAffinityCandidate 是新居民 Best Fit 所需的最小账号快照。
type OpenAIUserAffinityCandidate struct {
	AccountID                  int64
	Available5HRatio           float64
	Available7DRatio           float64
	Quota5HKnown               bool
	Quota7DKnown               bool
	ActiveContactUsers         int
	UserAlreadyActive          bool
	UserAlreadyResident        bool
	MaxContactUsers            int
	NewResidentCooldownSeconds int
	CooldownUntil              *time.Time
}

// openAIUserAffinityScopeKey 按最终调度分组和不可互换能力生成稳定归属范围。
func openAIUserAffinityScopeKey(groupID *int64, requireCompact bool, endpointCapability OpenAIEndpointCapability, imageCapability OpenAIImagesCapability, transport OpenAIUpstreamTransport) string {
	group := "simple"
	if groupID != nil && *groupID > 0 {
		group = fmt.Sprintf("%d", *groupID)
	}
	lane := "general"
	if imageCapability != "" {
		lane = "images:" + string(imageCapability)
	} else if requireCompact {
		lane = "compact"
	} else if endpointCapability != "" {
		lane = "endpoint:" + string(endpointCapability)
	}
	if transport != "" && transport != OpenAIUpstreamTransportAny && transport != OpenAIUpstreamTransportHTTPSSE {
		lane += ":transport:" + string(transport)
	}
	return "openai:v1:group:" + group + ":lane:" + lane
}

// SelectOpenAIUserAffinityCandidate 按额度主窗口选择剩余容量更充足的新居民账号。
// 主窗口接近时优先选择触达用户较少者，再用辅助窗口和账号 ID 保证结果确定。
func SelectOpenAIUserAffinityCandidate(cfg OpenAIUserAffinityConfig, candidates []OpenAIUserAffinityCandidate, demand5H, demand7D float64, now time.Time) (*OpenAIUserAffinityCandidate, bool) {
	if len(candidates) == 0 {
		return nil, false
	}
	valid := make([]OpenAIUserAffinityCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if !candidate.Quota5HKnown || !candidate.Quota7DKnown {
			continue
		}
		resident := candidate.UserAlreadyActive || candidate.UserAlreadyResident
		if candidate.AccountID <= 0 || candidate.ActiveContactUsers >= effectiveMaxContactUsers(cfg, candidate.MaxContactUsers) && !resident {
			continue
		}
		if candidate.CooldownUntil != nil && now.Before(*candidate.CooldownUntil) && !resident {
			continue
		}
		if candidate.Available5HRatio < demand5H+cfg.QuotaReserveRatio5H || candidate.Available7DRatio < demand7D+cfg.QuotaReserveRatio7D {
			continue
		}
		valid = append(valid, candidate)
	}
	if len(valid) == 0 {
		return nil, false
	}
	primary := func(c OpenAIUserAffinityCandidate) float64 { return c.Available7DRatio - demand7D }
	secondary := func(c OpenAIUserAffinityCandidate) float64 { return c.Available5HRatio - demand5H }
	if cfg.BestFitStrategy == OpenAIUserAffinityBestFit5HThen7D {
		primary, secondary = secondary, primary
	}

	// 跨 scope 已居住账号仍优先，避免同一用户扩散到更多账号。
	residentPool := make([]OpenAIUserAffinityCandidate, 0, len(valid))
	for _, candidate := range valid {
		if candidate.UserAlreadyActive || candidate.UserAlreadyResident {
			residentPool = append(residentPool, candidate)
		}
	}
	if len(residentPool) > 0 {
		valid = residentPool
	}

	// 先以主窗口最大剩余量为基准形成容差集合，避免近似比较破坏排序传递性。
	bestPrimary := primary(valid[0])
	for _, candidate := range valid[1:] {
		if value := primary(candidate); value > bestPrimary {
			bestPrimary = value
		}
	}
	primaryPool := make([]OpenAIUserAffinityCandidate, 0, len(valid))
	for _, candidate := range valid {
		if nearlyEqualAffinityRatio(primary(candidate), bestPrimary, cfg.BestFitCloseToleranceRatio) {
			primaryPool = append(primaryPool, candidate)
		}
	}

	minContactUsers := primaryPool[0].ActiveContactUsers
	for _, candidate := range primaryPool[1:] {
		if candidate.ActiveContactUsers < minContactUsers {
			minContactUsers = candidate.ActiveContactUsers
		}
	}
	contactPool := make([]OpenAIUserAffinityCandidate, 0, len(primaryPool))
	for _, candidate := range primaryPool {
		if candidate.ActiveContactUsers == minContactUsers {
			contactPool = append(contactPool, candidate)
		}
	}

	selected := contactPool[0]
	selectedSecondary := secondary(selected)
	for _, candidate := range contactPool[1:] {
		candidateSecondary := secondary(candidate)
		if candidateSecondary > selectedSecondary || candidateSecondary == selectedSecondary && candidate.AccountID < selected.AccountID {
			selected = candidate
			selectedSecondary = candidateSecondary
		}
	}
	return &selected, true
}

func effectiveMaxContactUsers(cfg OpenAIUserAffinityConfig, override int) int {
	if override > 0 {
		return override
	}
	return cfg.DefaultMaxContactUsers
}

func nearlyEqualAffinityRatio(a, b, tolerance float64) bool {
	if tolerance <= 0 {
		return a == b
	}
	return math.Abs(a-b) <= tolerance*math.Max(1, math.Max(math.Abs(a), math.Abs(b)))
}
