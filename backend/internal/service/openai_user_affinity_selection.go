package service

import (
	"fmt"
	"math"
	"sort"
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

// SelectOpenAIUserAffinityCandidate 按额度主窗口 Best Fit 选择新居民账号。
// 先比较配置的主窗口剩余额度，再用另一窗口收敛；接近时以唯一触达用户数打破平局。
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
	sort.SliceStable(valid, func(i, j int) bool {
		residentI := valid[i].UserAlreadyActive || valid[i].UserAlreadyResident
		residentJ := valid[j].UserAlreadyActive || valid[j].UserAlreadyResident
		if residentI != residentJ {
			return residentI
		}
		pi, pj := primary(valid[i]), primary(valid[j])
		if !nearlyEqualAffinityRatio(pi, pj, cfg.BestFitCloseToleranceRatio) {
			return pi < pj
		}
		si, sj := secondary(valid[i]), secondary(valid[j])
		if !nearlyEqualAffinityRatio(si, sj, cfg.BestFitCloseToleranceRatio) {
			return si < sj
		}
		if valid[i].ActiveContactUsers != valid[j].ActiveContactUsers {
			return valid[i].ActiveContactUsers < valid[j].ActiveContactUsers
		}
		return valid[i].AccountID < valid[j].AccountID
	})
	return &valid[0], true
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
