package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	codexFingerprintEpochPolicyCacheTTL = 60 * time.Second
	codexFingerprintEpochPolicyErrorTTL = 5 * time.Second
	codexFingerprintEpochPolicyDBWait   = 5 * time.Second
	codexFingerprintEpochPolicyCacheKey = "codex_fingerprint_epoch_policy"
)

var codexFingerprintEpochPolicySettingKeys = []string{
	SettingKeyCodexFingerprintMinSessionAgeHours,
	SettingKeyCodexFingerprintMaxSessionAgeHours,
	SettingKeyCodexFingerprintRotationJitterHours,
	SettingKeyCodexFingerprintIdleGateMinutes,
	SettingKeyCodexFingerprintOldEpochGraceHours,
}

// CodexFingerprintEpochPolicy 描述所有 Session scope 共用的 epoch 生命周期策略。
// 单次请求必须使用同一份快照，避免门槛在配置刷新期间跨版本混用。
type CodexFingerprintEpochPolicy struct {
	MinSessionAgeHours  int
	MaxSessionAgeHours  int
	RotationJitterHours int
	IdleGateMinutes     int
	OldEpochGraceHours  int
}

type cachedCodexFingerprintEpochPolicy struct {
	policy    CodexFingerprintEpochPolicy
	expiresAt int64
}

func defaultCodexFingerprintEpochPolicy() CodexFingerprintEpochPolicy {
	return CodexFingerprintEpochPolicy{
		MinSessionAgeHours:  config.CodexFingerprintMinSessionAgeHoursDefault,
		MaxSessionAgeHours:  config.CodexFingerprintMaxSessionAgeHoursDefault,
		RotationJitterHours: config.CodexFingerprintRotationJitterHoursDefault,
		IdleGateMinutes:     config.CodexFingerprintIdleGateMinutesDefault,
		OldEpochGraceHours:  config.CodexFingerprintOldEpochGraceHoursDefault,
	}
}

// codexFingerprintEpochPolicyFallback 优先使用通过启动校验的静态配置，测试或误配时安全回退程序默认值。
func (s *SettingService) codexFingerprintEpochPolicyFallback() CodexFingerprintEpochPolicy {
	fallback := defaultCodexFingerprintEpochPolicy()
	if s == nil || s.cfg == nil {
		return fallback
	}
	candidate := CodexFingerprintEpochPolicy{
		MinSessionAgeHours:  s.cfg.Gateway.CodexFingerprintMinSessionAgeHours,
		MaxSessionAgeHours:  s.cfg.Gateway.CodexFingerprintMaxSessionAgeHours,
		RotationJitterHours: s.cfg.Gateway.CodexFingerprintRotationJitterHours,
		IdleGateMinutes:     s.cfg.Gateway.CodexFingerprintIdleGateMinutes,
		OldEpochGraceHours:  s.cfg.Gateway.CodexFingerprintOldEpochGraceHours,
	}
	if ValidateCodexFingerprintEpochPolicy(candidate) != nil {
		return fallback
	}
	return candidate
}

// ValidateCodexFingerprintEpochPolicy 校验管理员设置与静态配置共用的完整策略边界。
func ValidateCodexFingerprintEpochPolicy(policy CodexFingerprintEpochPolicy) error {
	if policy.MinSessionAgeHours < 1 || policy.MinSessionAgeHours > config.CodexFingerprintSessionAgeHoursMax {
		return fmt.Errorf("codex fingerprint min session age hours must be between 1 and %d", config.CodexFingerprintSessionAgeHoursMax)
	}
	if policy.MaxSessionAgeHours < policy.MinSessionAgeHours || policy.MaxSessionAgeHours > config.CodexFingerprintSessionAgeHoursMax {
		return fmt.Errorf("codex fingerprint max session age hours must be between min session age and %d", config.CodexFingerprintSessionAgeHoursMax)
	}
	if policy.RotationJitterHours < 0 || policy.RotationJitterHours > config.CodexFingerprintRotationJitterMax {
		return fmt.Errorf("codex fingerprint rotation jitter hours must be between 0 and %d", config.CodexFingerprintRotationJitterMax)
	}
	if policy.IdleGateMinutes < 1 || policy.IdleGateMinutes > config.CodexFingerprintIdleGateMinutesMax {
		return fmt.Errorf("codex fingerprint idle gate minutes must be between 1 and %d", config.CodexFingerprintIdleGateMinutesMax)
	}
	if policy.OldEpochGraceHours < 1 || policy.OldEpochGraceHours > config.CodexFingerprintOldEpochGraceMax {
		return fmt.Errorf("codex fingerprint old epoch grace hours must be between 1 and %d", config.CodexFingerprintOldEpochGraceMax)
	}
	return nil
}

func mergeCodexFingerprintEpochPolicy(values map[string]string, fallback CodexFingerprintEpochPolicy) (CodexFingerprintEpochPolicy, error) {
	policy := fallback
	fields := []struct {
		key    string
		target *int
	}{
		{SettingKeyCodexFingerprintMinSessionAgeHours, &policy.MinSessionAgeHours},
		{SettingKeyCodexFingerprintMaxSessionAgeHours, &policy.MaxSessionAgeHours},
		{SettingKeyCodexFingerprintRotationJitterHours, &policy.RotationJitterHours},
		{SettingKeyCodexFingerprintIdleGateMinutes, &policy.IdleGateMinutes},
		{SettingKeyCodexFingerprintOldEpochGraceHours, &policy.OldEpochGraceHours},
	}
	for _, field := range fields {
		raw := strings.TrimSpace(values[field.key])
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return CodexFingerprintEpochPolicy{}, fmt.Errorf("%s must be an integer", field.key)
		}
		*field.target = value
	}
	if err := ValidateCodexFingerprintEpochPolicy(policy); err != nil {
		return CodexFingerprintEpochPolicy{}, err
	}
	return policy, nil
}

func codexFingerprintEpochPolicyFromSystemSettings(settings *SystemSettings) CodexFingerprintEpochPolicy {
	if settings == nil {
		return CodexFingerprintEpochPolicy{}
	}
	return CodexFingerprintEpochPolicy{
		MinSessionAgeHours:  settings.CodexFingerprintMinSessionAgeHours,
		MaxSessionAgeHours:  settings.CodexFingerprintMaxSessionAgeHours,
		RotationJitterHours: settings.CodexFingerprintRotationJitterHours,
		IdleGateMinutes:     settings.CodexFingerprintIdleGateMinutes,
		OldEpochGraceHours:  settings.CodexFingerprintOldEpochGraceHours,
	}
}

// ensureCodexFingerprintEpochPolicyDefaults 兼容仍按旧 SystemSettings 结构构造零值的内部调用方。
// 管理 API 的部分更新走 Omitting 入口并先与当前值合并，显式非法值仍会被严格拒绝。
func (s *SettingService) ensureCodexFingerprintEpochPolicyDefaults(settings *SystemSettings) {
	policy := codexFingerprintEpochPolicyFromSystemSettings(settings)
	if policy != (CodexFingerprintEpochPolicy{}) {
		return
	}
	applyCodexFingerprintEpochPolicy(settings, s.codexFingerprintEpochPolicyFallback())
}

func applyCodexFingerprintEpochPolicy(settings *SystemSettings, policy CodexFingerprintEpochPolicy) {
	if settings == nil {
		return
	}
	settings.CodexFingerprintMinSessionAgeHours = policy.MinSessionAgeHours
	settings.CodexFingerprintMaxSessionAgeHours = policy.MaxSessionAgeHours
	settings.CodexFingerprintRotationJitterHours = policy.RotationJitterHours
	settings.CodexFingerprintIdleGateMinutes = policy.IdleGateMinutes
	settings.CodexFingerprintOldEpochGraceHours = policy.OldEpochGraceHours
}

// GetCodexFingerprintEpochPolicy 返回请求热路径使用的完整策略快照。
// 数据库读取失败时保留最后一次有效值；冷启动失败则退回静态配置。
func (s *SettingService) GetCodexFingerprintEpochPolicy(ctx context.Context) CodexFingerprintEpochPolicy {
	fallback := s.codexFingerprintEpochPolicyFallback()
	if s == nil || s.settingRepo == nil {
		return fallback
	}
	now := time.Now()
	if cached, _ := s.codexFingerprintEpochCache.Load().(*cachedCodexFingerprintEpochPolicy); cached != nil && now.UnixNano() < cached.expiresAt {
		return cached.policy
	}
	value, _, _ := s.codexFingerprintEpochSF.Do(codexFingerprintEpochPolicyCacheKey, func() (any, error) {
		now := time.Now()
		cached, _ := s.codexFingerprintEpochCache.Load().(*cachedCodexFingerprintEpochPolicy)
		if cached != nil && now.UnixNano() < cached.expiresAt {
			return cached.policy, nil
		}
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexFingerprintEpochPolicyDBWait)
		defer cancel()
		values, err := s.settingRepo.GetMultiple(dbCtx, codexFingerprintEpochPolicySettingKeys)
		if err == nil {
			policy, parseErr := mergeCodexFingerprintEpochPolicy(values, fallback)
			if parseErr == nil {
				s.codexFingerprintEpochCache.Store(&cachedCodexFingerprintEpochPolicy{
					policy:    policy,
					expiresAt: now.Add(codexFingerprintEpochPolicyCacheTTL).UnixNano(),
				})
				return policy, nil
			}
			err = parseErr
		}
		slog.Warn("failed to load codex fingerprint epoch policy", "error", err)
		if cached != nil && ValidateCodexFingerprintEpochPolicy(cached.policy) == nil {
			s.codexFingerprintEpochCache.Store(&cachedCodexFingerprintEpochPolicy{
				policy:    cached.policy,
				expiresAt: now.Add(codexFingerprintEpochPolicyErrorTTL).UnixNano(),
			})
			return cached.policy, nil
		}
		s.codexFingerprintEpochCache.Store(&cachedCodexFingerprintEpochPolicy{
			policy:    fallback,
			expiresAt: now.Add(codexFingerprintEpochPolicyErrorTTL).UnixNano(),
		})
		return fallback, nil
	})
	if policy, ok := value.(CodexFingerprintEpochPolicy); ok {
		return policy
	}
	return fallback
}

func (s *SettingService) refreshCodexFingerprintEpochPolicyCache(settings *SystemSettings) {
	if s == nil {
		return
	}
	policy := codexFingerprintEpochPolicyFromSystemSettings(settings)
	if ValidateCodexFingerprintEpochPolicy(policy) != nil {
		policy = s.codexFingerprintEpochPolicyFallback()
	}
	s.codexFingerprintEpochSF.Forget(codexFingerprintEpochPolicyCacheKey)
	s.codexFingerprintEpochCache.Store(&cachedCodexFingerprintEpochPolicy{
		policy:    policy,
		expiresAt: time.Now().Add(codexFingerprintEpochPolicyCacheTTL).UnixNano(),
	})
}
