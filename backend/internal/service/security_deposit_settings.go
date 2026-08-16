package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	SecurityDepositPenaltyModeOff     = "off"
	SecurityDepositPenaltyModeShadow  = "shadow"
	SecurityDepositPenaltyModeEnforce = "enforce"

	defaultSecurityDepositPolicyVersion = "2026-08-16-v1"
	defaultSecurityDepositFreezeHours   = 24
	maxSecurityDepositFreezeHours       = 24 * 365

	defaultSecurityDepositAgreementContentZH = `# 网络安全保证金约定

- 禁止破限、破甲、越狱、提示注入绕过，以及以角色扮演等方式掩饰绕过网络安全策略。
- 保证金与调用额度完全分离，不会被正常 API 请求消费。
- 用户实付保证金在冻结期内不可退款。冻结期结束后，仅当平台保证金自助退款、对应支付实例的退款能力和“允许用户退款”均开启时，用户才可自助退款；否则需由管理员处理。管理员发放保证金永久冻结且不可退款。冻结不影响分组准入或违规扣除。
- 可信上游官方网络安全策略命中时，平台将按本次个人门槛扣除保证金并安全锁定触发密钥；首次触发按 1 倍门槛，第二次按 2 倍，第三次按 3 倍，之后依次递增，并受管理员配置的倍率上限约束。
- 本地内容审计不扣保证金，但可独立触发账户封禁。
- 退款或扣除后余额不足时，相关密钥将被自动禁用；补足后仍需显式启用。
- 支付退款、申诉和隐私处理以平台公布的现行规则为准。`

	defaultSecurityDepositAgreementContentEN = `# Network Security Deposit Terms

- Attempts to bypass limits, safeguards, jailbreak protections, prompt-injection controls, or to disguise such attempts through role-play are prohibited.
- The security deposit is separate from usage credit and is never consumed by normal API requests.
- User-paid deposits cannot be refunded during the freeze period. After that period, self-service refunds are available only when platform self-refund and the corresponding payment-provider instance's refund and user-refund capabilities are all enabled; otherwise an administrator must process the refund. Administrator-issued deposits are permanently frozen and non-refundable. Frozen funds still count for access and policy forfeiture.
- A trusted official upstream network-security-policy response causes forfeiture up to the applicable personal threshold and security-locks the triggering key. The first violation uses 1x the base threshold, the second uses 2x, the third uses 3x, and each later violation increases it by another 1x, subject to the administrator-configured multiplier cap.
- Local content moderation does not forfeit the deposit, but may independently disable the account.
- A refund or deduction that leaves the deposit below a group threshold automatically disables affected keys. Keys must be explicitly enabled again after funds are restored.
- Payment refunds, appeals, and privacy handling follow the platform's current published rules.`
)

// SecurityDepositPolicyConfig 是保证金协议和运行时安全默认值的权威配置。
type SecurityDepositPolicyConfig struct {
	Version            string `json:"version"`
	ContentHash        string `json:"content_hash"`
	ContentZH          string `json:"content_zh"`
	ContentEN          string `json:"content_en"`
	FreezeHours        int    `json:"freeze_hours"`
	MaxRiskMultiplier  int64  `json:"max_risk_multiplier"`
	EnforcementEnabled bool   `json:"enforcement_enabled"`
	SelfRefundEnabled  bool   `json:"self_refund_enabled"`
	PenaltyMode        string `json:"penalty_mode"`
}

// GetSecurityDepositPolicyConfig 读取保证金配置；缺失或非法值回退到保守默认值。
func (s *SettingService) GetSecurityDepositPolicyConfig(ctx context.Context) SecurityDepositPolicyConfig {
	config, _ := s.GetSecurityDepositPolicyConfigStrict(ctx)
	return config
}

// GetSecurityDepositPolicyConfigStrict 用于安全热路径；配置存储不可用时返回错误，禁止静默关闭执法。
func (s *SettingService) GetSecurityDepositPolicyConfigStrict(ctx context.Context) (SecurityDepositPolicyConfig, error) {
	values := map[string]string{}
	if s != nil && s.settingRepo != nil {
		keys := []string{
			SettingKeySecurityDepositPolicyVersion,
			SettingKeySecurityDepositAgreementContentZH,
			SettingKeySecurityDepositAgreementContentEN,
			SettingKeySecurityDepositFreezeHours,
			SettingKeySecurityDepositMaxRiskMultiplier,
			SettingKeySecurityDepositEnforcementEnabled,
			SettingKeySecurityDepositSelfRefundEnabled,
			SettingKeySecurityDepositPenaltyMode,
		}
		loaded, err := s.settingRepo.GetMultiple(ctx, keys)
		if err != nil {
			return buildSecurityDepositPolicyConfig(values), fmt.Errorf("load security deposit policy config: %w", err)
		}
		values = loaded
	}
	return buildSecurityDepositPolicyConfig(values), nil
}

func buildSecurityDepositPolicyConfig(values map[string]string) SecurityDepositPolicyConfig {
	version := firstNonEmpty(strings.TrimSpace(values[SettingKeySecurityDepositPolicyVersion]), defaultSecurityDepositPolicyVersion)
	contentZH := firstNonEmpty(strings.TrimSpace(values[SettingKeySecurityDepositAgreementContentZH]), defaultSecurityDepositAgreementContentZH)
	contentEN := firstNonEmpty(strings.TrimSpace(values[SettingKeySecurityDepositAgreementContentEN]), defaultSecurityDepositAgreementContentEN)
	freezeHours := parseSecurityDepositBoundedInt(values[SettingKeySecurityDepositFreezeHours], defaultSecurityDepositFreezeHours, 0, maxSecurityDepositFreezeHours)
	maxMultiplier := int64(parseSecurityDepositBoundedInt(values[SettingKeySecurityDepositMaxRiskMultiplier], int(defaultSecurityDepositMaxRiskMultiplier), 1, 100))
	return SecurityDepositPolicyConfig{
		Version: version, ContentHash: securityDepositAgreementHash(contentZH, contentEN),
		ContentZH: contentZH, ContentEN: contentEN, FreezeHours: freezeHours,
		MaxRiskMultiplier:  maxMultiplier,
		EnforcementEnabled: strings.EqualFold(strings.TrimSpace(values[SettingKeySecurityDepositEnforcementEnabled]), "true"),
		SelfRefundEnabled:  strings.EqualFold(strings.TrimSpace(values[SettingKeySecurityDepositSelfRefundEnabled]), "true"),
		PenaltyMode:        normalizeSecurityDepositPenaltyMode(values[SettingKeySecurityDepositPenaltyMode]),
	}
}

func normalizeSecurityDepositPenaltyMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SecurityDepositPenaltyModeShadow:
		return SecurityDepositPenaltyModeShadow
	case SecurityDepositPenaltyModeEnforce:
		return SecurityDepositPenaltyModeEnforce
	default:
		return SecurityDepositPenaltyModeOff
	}
}

func securityDepositAgreementHash(contentZH, contentEN string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(contentZH) + "\n---\n" + strings.TrimSpace(contentEN)))
	return hex.EncodeToString(sum[:])
}

func parseSecurityDepositBoundedInt(raw string, fallback, minValue, maxValue int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minValue || value > maxValue {
		return fallback
	}
	return value
}
