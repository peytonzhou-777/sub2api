package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	// SettingKeyOpenAIAccountAdmission 保存 OpenAI 账号准入排队的完整全局配置。
	SettingKeyOpenAIAccountAdmission     = "openai_account_admission"
	openAIAccountAdmissionConfigCacheTTL = 5 * time.Second
)

type openAIAccountAdmissionConfigState struct {
	mu    sync.Mutex
	cache atomic.Value // *cachedOpenAIAccountAdmissionConfig
}

type cachedOpenAIAccountAdmissionConfig struct {
	cfg       OpenAIAccountAdmissionConfig
	expiresAt time.Time
}

// OpenAIAccountAdmissionConfig 是账号选定后的全局准入配置，不提供逐账号覆盖。
type OpenAIAccountAdmissionConfig struct {
	Enabled                 bool   `json:"enabled"`
	QueueEnabled            bool   `json:"queue_enabled"`
	MaxWaitSeconds          int    `json:"max_wait_seconds"`
	RequestsPerMinute       int    `json:"requests_per_minute"`
	TokensPerMinute         int64  `json:"tokens_per_minute"`
	DefaultOutputTokens     int64  `json:"default_output_tokens"`
	JitterMinMS             int    `json:"jitter_min_ms"`
	JitterMaxMS             int    `json:"jitter_max_ms"`
	MaxQueueDepthPerAccount int    `json:"max_queue_depth_per_account"`
	InteractiveBurst        int    `json:"interactive_burst"`
	BackgroundAgingSeconds  int    `json:"background_aging_seconds"`
	ConfigVersion           int64  `json:"config_version"`
	UpdatedAt               string `json:"updated_at,omitempty"`
}

// DefaultOpenAIAccountAdmissionConfig 返回默认关闭且可直接启用的保守配置。
func DefaultOpenAIAccountAdmissionConfig() OpenAIAccountAdmissionConfig {
	return OpenAIAccountAdmissionConfig{
		Enabled:                 false,
		QueueEnabled:            false,
		MaxWaitSeconds:          45,
		RequestsPerMinute:       0,
		TokensPerMinute:         0,
		DefaultOutputTokens:     4096,
		JitterMinMS:             100,
		JitterMaxMS:             500,
		MaxQueueDepthPerAccount: 100,
		InteractiveBurst:        4,
		BackgroundAgingSeconds:  5,
	}
}

// ValidateOpenAIAccountAdmissionConfig 校验完整对象，确保所有等待共享有界预算。
func ValidateOpenAIAccountAdmissionConfig(cfg OpenAIAccountAdmissionConfig) (OpenAIAccountAdmissionConfig, error) {
	if cfg.MaxWaitSeconds < 1 || cfg.MaxWaitSeconds > 120 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "max_wait_seconds must be between 1 and 120")
	}
	if cfg.RequestsPerMinute < 0 || cfg.RequestsPerMinute > 100000 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "requests_per_minute must be between 0 and 100000")
	}
	if cfg.TokensPerMinute < 0 || cfg.TokensPerMinute > 100000000 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "tokens_per_minute must be between 0 and 100000000")
	}
	if cfg.DefaultOutputTokens < 1 || cfg.DefaultOutputTokens > 1000000 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "default_output_tokens must be between 1 and 1000000")
	}
	if cfg.JitterMinMS < 0 || cfg.JitterMaxMS < cfg.JitterMinMS || cfg.JitterMaxMS > 5000 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "jitter range is invalid")
	}
	if cfg.MaxQueueDepthPerAccount < 1 || cfg.MaxQueueDepthPerAccount > 10000 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "max_queue_depth_per_account must be between 1 and 10000")
	}
	if cfg.InteractiveBurst < 1 || cfg.InteractiveBurst > 100 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "interactive_burst must be between 1 and 100")
	}
	if cfg.BackgroundAgingSeconds < 1 || cfg.BackgroundAgingSeconds > 120 {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "background_aging_seconds must be between 1 and 120")
	}
	return cfg, nil
}

// GetOpenAIAccountAdmissionConfig 读取带短缓存的完整全局配置。
func (s *SettingService) GetOpenAIAccountAdmissionConfig(ctx context.Context) (OpenAIAccountAdmissionConfig, error) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	if s == nil || s.settingRepo == nil {
		return cfg, nil
	}
	if cached, ok := s.openAIAccountAdmissionConfig.cache.Load().(*cachedOpenAIAccountAdmissionConfig); ok && cached != nil && time.Now().Before(cached.expiresAt) {
		return cached.cfg, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIAccountAdmission)
	if errors.Is(err, ErrSettingNotFound) {
		s.openAIAccountAdmissionConfig.cache.Store(&cachedOpenAIAccountAdmissionConfig{cfg: cfg, expiresAt: time.Now().Add(openAIAccountAdmissionConfigCacheTTL)})
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("get openai account admission config: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "stored openai account admission config is invalid").WithCause(err)
	}
	cfg, err = ValidateOpenAIAccountAdmissionConfig(cfg)
	if err != nil {
		return cfg, err
	}
	s.openAIAccountAdmissionConfig.cache.Store(&cachedOpenAIAccountAdmissionConfig{cfg: cfg, expiresAt: time.Now().Add(openAIAccountAdmissionConfigCacheTTL)})
	return cfg, nil
}

// UpdateOpenAIAccountAdmissionConfig 使用整体 JSON 与版本 CAS 防止混合配置。
func (s *SettingService) UpdateOpenAIAccountAdmissionConfig(ctx context.Context, next OpenAIAccountAdmissionConfig, expectedVersion int64) (OpenAIAccountAdmissionConfig, error) {
	next, err := ValidateOpenAIAccountAdmissionConfig(next)
	if err != nil {
		return next, err
	}
	if s == nil || s.settingRepo == nil {
		return next, infraerrors.InternalServer("SETTINGS_SERVICE_UNAVAILABLE", "settings service is not available")
	}
	s.openAIAccountAdmissionConfig.mu.Lock()
	defer s.openAIAccountAdmissionConfig.mu.Unlock()

	current := DefaultOpenAIAccountAdmissionConfig()
	var expectedRaw *string
	raw, readErr := s.settingRepo.GetValue(ctx, SettingKeyOpenAIAccountAdmission)
	if readErr == nil {
		expectedRaw = &raw
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			return next, infraerrors.BadRequest("INVALID_OPENAI_ACCOUNT_ADMISSION_CONFIG", "stored openai account admission config is invalid").WithCause(err)
		}
		current, err = ValidateOpenAIAccountAdmissionConfig(current)
		if err != nil {
			return next, err
		}
	} else if !errors.Is(readErr, ErrSettingNotFound) {
		return next, fmt.Errorf("get openai account admission config for update: %w", readErr)
	}
	if current.ConfigVersion != expectedVersion {
		return current, infraerrors.Conflict("OPENAI_ACCOUNT_ADMISSION_CONFIG_VERSION_CONFLICT", "openai account admission config has changed").WithMetadata(map[string]string{
			"expected_version": fmt.Sprintf("%d", expectedVersion),
			"actual_version":   fmt.Sprintf("%d", current.ConfigVersion),
		})
	}
	next.ConfigVersion = current.ConfigVersion + 1
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	encoded, err := json.Marshal(next)
	if err != nil {
		return next, fmt.Errorf("marshal openai account admission config: %w", err)
	}
	if casRepo, ok := s.settingRepo.(SettingCompareAndSetRepository); ok {
		updated, err := casRepo.CompareAndSet(ctx, SettingKeyOpenAIAccountAdmission, expectedRaw, string(encoded))
		if err != nil {
			return next, fmt.Errorf("save openai account admission config: %w", err)
		}
		if !updated {
			s.openAIAccountAdmissionConfig.cache.Store(&cachedOpenAIAccountAdmissionConfig{cfg: current, expiresAt: time.Now()})
			return current, infraerrors.Conflict("OPENAI_ACCOUNT_ADMISSION_CONFIG_VERSION_CONFLICT", "openai account admission config has changed")
		}
	} else if err := s.settingRepo.Set(ctx, SettingKeyOpenAIAccountAdmission, string(encoded)); err != nil {
		return next, fmt.Errorf("save openai account admission config: %w", err)
	}
	s.openAIAccountAdmissionConfig.cache.Store(&cachedOpenAIAccountAdmissionConfig{cfg: next, expiresAt: time.Now().Add(openAIAccountAdmissionConfigCacheTTL)})
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return next, nil
}
