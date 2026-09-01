package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	OpenAIUserAffinityModeShadow  = "shadow"
	OpenAIUserAffinityModeEnforce = "enforce"

	OpenAIUserAffinityBestFit7DThen5H = "7d_then_5h"
	OpenAIUserAffinityBestFit5HThen7D = "5h_then_7d"

	OpenAIUserAffinityTouchAccepted  = "upstream_accepted"
	OpenAIUserAffinityTouchCompleted = "response_completed"
	openAIUserAffinityConfigCacheTTL = 5 * time.Second

	// SettingKeyOpenAIUserAffinityScheduling 保存 OpenAI 用户粘性调度完整配置。
	SettingKeyOpenAIUserAffinityScheduling = "openai_user_affinity_scheduling"
)

// SettingCompareAndSetRepository 为整体版本化配置提供跨实例乐观锁。
type SettingCompareAndSetRepository interface {
	CompareAndSet(ctx context.Context, key string, expectedValue *string, value string) (bool, error)
}

type openAIUserAffinityConfigState struct {
	mu    sync.Mutex
	cache atomic.Value // *cachedOpenAIUserAffinityConfig
}

type cachedOpenAIUserAffinityConfig struct {
	cfg       OpenAIUserAffinityConfig
	expiresAt time.Time
}

// OpenAIUserAffinityConfig 是 OpenAI 用户粘性调度的完整网关配置。
// 配置作为单个 JSON 对象持久化，避免各字段独立更新造成混合版本。
type OpenAIUserAffinityConfig struct {
	Enabled                           bool    `json:"enabled"`
	Mode                              string  `json:"mode"`
	QuotaReserveRatio5H               float64 `json:"quota_reserve_ratio_5h"`
	QuotaReserveRatio7D               float64 `json:"quota_reserve_ratio_7d"`
	ColdStartDemandQuantile           float64 `json:"cold_start_demand_quantile"`
	BestFitStrategy                   string  `json:"best_fit_strategy"`
	BestFitCloseToleranceRatio        float64 `json:"best_fit_close_tolerance_ratio"`
	DefaultMaxResidentUsers           int     `json:"default_max_resident_users"`
	DefaultNewResidentCooldownSeconds int     `json:"default_new_resident_cooldown_seconds"`
	// ResidentReentryOvercommitEnabled 仅保留旧配置兼容；居民准入改按唯一居民数后不再参与决策。
	ResidentReentryOvercommitEnabled  bool   `json:"resident_reentry_overcommit_enabled"`
	CapacityFailureMigrationThreshold int    `json:"capacity_failure_migration_threshold"`
	CapacityFailureWindowSeconds      int    `json:"capacity_failure_window_seconds"`
	MigrationStabilitySeconds         int    `json:"migration_stability_seconds"`
	FollowerJitterMinMS               int    `json:"follower_jitter_min_ms"`
	FollowerJitterMaxMS               int    `json:"follower_jitter_max_ms"`
	TouchSuccessMode                  string `json:"touch_success_mode"`
	ManualResetExcludeSourceAccount   bool   `json:"manual_reset_exclude_source_account"`
	ResidentAccountSlotCount          int    `json:"resident_account_slot_count"`
	ResidentTTLSeconds                int    `json:"resident_ttl_seconds"`
	ConversationActiveTTLSeconds      int    `json:"conversation_active_ttl_seconds"`
	ConfigVersion                     int64  `json:"config_version"`
	UpdatedAt                         string `json:"updated_at,omitempty"`
}

// DefaultOpenAIUserAffinityConfig 返回安全默认值：策略默认关闭，启用后仍限制新居民装箱。
func DefaultOpenAIUserAffinityConfig() OpenAIUserAffinityConfig {
	return OpenAIUserAffinityConfig{
		Enabled:                           false,
		Mode:                              OpenAIUserAffinityModeEnforce,
		QuotaReserveRatio5H:               0.10,
		QuotaReserveRatio7D:               0.10,
		ColdStartDemandQuantile:           0.75,
		BestFitStrategy:                   OpenAIUserAffinityBestFit7DThen5H,
		BestFitCloseToleranceRatio:        0.01,
		DefaultMaxResidentUsers:           10,
		DefaultNewResidentCooldownSeconds: 300,
		ResidentReentryOvercommitEnabled:  true,
		CapacityFailureMigrationThreshold: 3,
		CapacityFailureWindowSeconds:      60,
		MigrationStabilitySeconds:         60,
		FollowerJitterMinMS:               100,
		FollowerJitterMaxMS:               500,
		TouchSuccessMode:                  OpenAIUserAffinityTouchAccepted,
		ResidentAccountSlotCount:          1,
		ResidentTTLSeconds:                7 * 24 * 60 * 60,
		ConversationActiveTTLSeconds:      60 * 60,
		ConfigVersion:                     0,
	}
}

// ValidateAndNormalizeOpenAIUserAffinityConfig 校验管理员输入并补齐枚举空值。
func ValidateAndNormalizeOpenAIUserAffinityConfig(cfg OpenAIUserAffinityConfig) (OpenAIUserAffinityConfig, error) {
	defaults := DefaultOpenAIUserAffinityConfig()
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	cfg.BestFitStrategy = strings.ToLower(strings.TrimSpace(cfg.BestFitStrategy))
	cfg.TouchSuccessMode = strings.ToLower(strings.TrimSpace(cfg.TouchSuccessMode))
	if cfg.Mode == "" {
		cfg.Mode = defaults.Mode
	}
	if cfg.BestFitStrategy == "" {
		cfg.BestFitStrategy = defaults.BestFitStrategy
	}
	if cfg.TouchSuccessMode == "" {
		cfg.TouchSuccessMode = defaults.TouchSuccessMode
	}
	// 新增字段对旧配置和旧管理端请求补默认值，零值不得成为实际 TTL 或槽位上限。
	if cfg.ResidentAccountSlotCount == 0 {
		cfg.ResidentAccountSlotCount = defaults.ResidentAccountSlotCount
	}
	if cfg.ResidentTTLSeconds == 0 {
		cfg.ResidentTTLSeconds = defaults.ResidentTTLSeconds
	}
	if cfg.ConversationActiveTTLSeconds == 0 {
		cfg.ConversationActiveTTLSeconds = defaults.ConversationActiveTTLSeconds
	}
	if cfg.DefaultMaxResidentUsers == 0 {
		cfg.DefaultMaxResidentUsers = defaults.DefaultMaxResidentUsers
	}
	if cfg.Mode != OpenAIUserAffinityModeShadow && cfg.Mode != OpenAIUserAffinityModeEnforce {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "mode must be shadow or enforce")
	}
	if cfg.BestFitStrategy != OpenAIUserAffinityBestFit7DThen5H && cfg.BestFitStrategy != OpenAIUserAffinityBestFit5HThen7D {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "best_fit_strategy is invalid")
	}
	if cfg.TouchSuccessMode != OpenAIUserAffinityTouchAccepted && cfg.TouchSuccessMode != OpenAIUserAffinityTouchCompleted {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "touch_success_mode is invalid")
	}
	if cfg.QuotaReserveRatio5H < 0 || cfg.QuotaReserveRatio5H > 0.90 || cfg.QuotaReserveRatio7D < 0 || cfg.QuotaReserveRatio7D > 0.90 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "quota reserve ratios must be between 0 and 0.90")
	}
	if cfg.ColdStartDemandQuantile < 0.50 || cfg.ColdStartDemandQuantile > 0.99 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "cold_start_demand_quantile must be between 0.50 and 0.99")
	}
	if cfg.BestFitCloseToleranceRatio < 0 || cfg.BestFitCloseToleranceRatio > 0.20 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "best_fit_close_tolerance_ratio must be between 0 and 0.20")
	}
	if cfg.DefaultMaxResidentUsers < 1 || cfg.DefaultMaxResidentUsers > 10000 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "default_max_resident_users must be between 1 and 10000")
	}
	if cfg.DefaultNewResidentCooldownSeconds < 1 || cfg.DefaultNewResidentCooldownSeconds > 86400 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "default_new_resident_cooldown_seconds must be between 1 and 86400")
	}
	if cfg.CapacityFailureMigrationThreshold < 2 || cfg.CapacityFailureMigrationThreshold > 100 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "capacity_failure_migration_threshold must be between 2 and 100")
	}
	if cfg.CapacityFailureWindowSeconds < 10 || cfg.CapacityFailureWindowSeconds > 3600 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "capacity_failure_window_seconds must be between 10 and 3600")
	}
	if cfg.MigrationStabilitySeconds < 0 || cfg.MigrationStabilitySeconds > 3600 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "migration_stability_seconds must be between 0 and 3600")
	}
	if cfg.FollowerJitterMinMS < 0 || cfg.FollowerJitterMaxMS < cfg.FollowerJitterMinMS || cfg.FollowerJitterMaxMS > 10000 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "follower jitter range is invalid")
	}
	if cfg.ResidentAccountSlotCount < 1 || cfg.ResidentAccountSlotCount > 5 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "resident_account_slot_count must be between 1 and 5")
	}
	if cfg.ResidentTTLSeconds < 24*60*60 || cfg.ResidentTTLSeconds > 30*24*60*60 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "resident_ttl_seconds must be between 86400 and 2592000")
	}
	if cfg.ConversationActiveTTLSeconds < 5*60 || cfg.ConversationActiveTTLSeconds > 24*60*60 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "conversation_active_ttl_seconds must be between 300 and 86400")
	}
	return cfg, nil
}

// RuntimeResidentAccountSlotCount 返回当前阶段真正启用的槽位数。
// P1 仅建立兼容数据模型，运行时保持单槽，待多槽调度原子链路完成后再放开配置值。
func (cfg OpenAIUserAffinityConfig) RuntimeResidentAccountSlotCount() int {
	if cfg.ResidentAccountSlotCount < 1 {
		return 1
	}
	if cfg.ResidentAccountSlotCount > 5 {
		return 5
	}
	return cfg.ResidentAccountSlotCount
}

// ResidentTTL 返回常驻槽位及长期会话绑定的统一滑动 TTL。
func (cfg OpenAIUserAffinityConfig) ResidentTTL() time.Duration {
	return time.Duration(cfg.ResidentTTLSeconds) * time.Second
}

// ConversationActiveTTL 返回新会话对常驻槽位的短期活跃占用 TTL。
func (cfg OpenAIUserAffinityConfig) ConversationActiveTTL() time.Duration {
	return time.Duration(cfg.ConversationActiveTTLSeconds) * time.Second
}

// decodeOpenAIUserAffinityConfig 兼容迁移前的触达容量键，便于滚动升级期间保留自定义上限。
func decodeOpenAIUserAffinityConfig(raw string, cfg *OpenAIUserAffinityConfig) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(raw), cfg); err != nil {
		return err
	}
	if _, hasResidentLimit := fields["default_max_resident_users"]; hasResidentLimit {
		return nil
	}
	if legacy, ok := fields["default_max_contact_users"]; ok {
		return json.Unmarshal(legacy, &cfg.DefaultMaxResidentUsers)
	}
	return nil
}

// GetOpenAIUserAffinityConfig 读取完整配置；未初始化时返回默认值和版本 0。
func (s *SettingService) GetOpenAIUserAffinityConfig(ctx context.Context) (OpenAIUserAffinityConfig, error) {
	cfg := DefaultOpenAIUserAffinityConfig()
	if s == nil || s.settingRepo == nil {
		return cfg, nil
	}
	if cached, ok := s.openAIUserAffinityConfig.cache.Load().(*cachedOpenAIUserAffinityConfig); ok && cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.cfg, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIUserAffinityScheduling)
	if errors.Is(err, ErrSettingNotFound) {
		s.openAIUserAffinityConfig.cache.Store(&cachedOpenAIUserAffinityConfig{cfg: cfg, expiresAt: time.Now().Add(openAIUserAffinityConfigCacheTTL)})
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("get openai user affinity config: %w", err)
	}
	if err := decodeOpenAIUserAffinityConfig(raw, &cfg); err != nil {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "stored openai user affinity config is invalid").WithCause(err)
	}
	cfg, err = ValidateAndNormalizeOpenAIUserAffinityConfig(cfg)
	if err != nil {
		return cfg, err
	}
	s.openAIUserAffinityConfig.cache.Store(&cachedOpenAIUserAffinityConfig{cfg: cfg, expiresAt: time.Now().Add(openAIUserAffinityConfigCacheTTL)})
	return cfg, nil
}

// UpdateOpenAIUserAffinityConfig 使用 expectedVersion 防止管理员覆盖较新的配置。
func (s *SettingService) UpdateOpenAIUserAffinityConfig(ctx context.Context, next OpenAIUserAffinityConfig, expectedVersion int64) (OpenAIUserAffinityConfig, error) {
	next, err := ValidateAndNormalizeOpenAIUserAffinityConfig(next)
	if err != nil {
		return next, err
	}
	if s == nil || s.settingRepo == nil {
		return next, infraerrors.InternalServer("SETTINGS_SERVICE_UNAVAILABLE", "settings service is not available")
	}
	s.openAIUserAffinityConfig.mu.Lock()
	defer s.openAIUserAffinityConfig.mu.Unlock()
	current := DefaultOpenAIUserAffinityConfig()
	var expectedRaw *string
	raw, readErr := s.settingRepo.GetValue(ctx, SettingKeyOpenAIUserAffinityScheduling)
	if readErr == nil {
		expectedRaw = &raw
		if err := decodeOpenAIUserAffinityConfig(raw, &current); err != nil {
			return next, infraerrors.BadRequest("INVALID_OPENAI_USER_AFFINITY_CONFIG", "stored openai user affinity config is invalid").WithCause(err)
		}
		var err error
		current, err = ValidateAndNormalizeOpenAIUserAffinityConfig(current)
		if err != nil {
			return next, err
		}
	} else if !errors.Is(readErr, ErrSettingNotFound) {
		return next, fmt.Errorf("get openai user affinity config for update: %w", readErr)
	}
	if current.ConfigVersion != expectedVersion {
		return current, infraerrors.Conflict("OPENAI_USER_AFFINITY_CONFIG_VERSION_CONFLICT", "openai user affinity config has changed").WithMetadata(map[string]string{
			"expected_version": fmt.Sprintf("%d", expectedVersion),
			"actual_version":   fmt.Sprintf("%d", current.ConfigVersion),
		})
	}
	next.ConfigVersion = current.ConfigVersion + 1
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(next)
	if err != nil {
		return next, fmt.Errorf("marshal openai user affinity config: %w", err)
	}
	if casRepo, ok := s.settingRepo.(SettingCompareAndSetRepository); ok {
		updated, err := casRepo.CompareAndSet(ctx, SettingKeyOpenAIUserAffinityScheduling, expectedRaw, string(encoded))
		if err != nil {
			return next, fmt.Errorf("save openai user affinity config: %w", err)
		}
		if !updated {
			s.openAIUserAffinityConfig.cache.Store(&cachedOpenAIUserAffinityConfig{cfg: current, expiresAt: time.Now()})
			return current, infraerrors.Conflict("OPENAI_USER_AFFINITY_CONFIG_VERSION_CONFLICT", "openai user affinity config has changed")
		}
	} else if err := s.settingRepo.Set(ctx, SettingKeyOpenAIUserAffinityScheduling, string(encoded)); err != nil {
		return next, fmt.Errorf("save openai user affinity config: %w", err)
	}
	s.openAIUserAffinityConfig.cache.Store(&cachedOpenAIUserAffinityConfig{cfg: next, expiresAt: time.Now().Add(openAIUserAffinityConfigCacheTTL)})
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return next, nil
}
