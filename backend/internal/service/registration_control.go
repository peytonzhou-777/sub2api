package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	RegistrationEligibilityReasonNotLegacyUser            = "NOT_LEGACY_USER"
	RegistrationEligibilityReasonInsufficientSuccessCalls = "INSUFFICIENT_SUCCESS_CALLS"
	RegistrationEligibilityReasonInsufficientActiveDays   = "INSUFFICIENT_ACTIVE_DAYS"
	RegistrationEligibilityReasonCyberPolicyWarning       = "CYBER_POLICY_WARNING"
	RegistrationEligibilityReasonSoftDeleted              = "SOFT_DELETED"
	RegistrationEligibilityReasonExemptionDisabled        = "LEGACY_EXEMPTION_DISABLED"
)

var (
	ErrRegistrationCapacityReached = infraerrors.Forbidden(
		"REGISTRATION_CAPACITY_REACHED",
		"registration capacity has been reached; use a valid invitation code",
	)
	ErrLegacyRegistrationNotEligible = infraerrors.BadRequest(
		"LEGACY_REGISTRATION_NOT_ELIGIBLE",
		"this email is not eligible for invitation-free registration",
	)
)

// RegistrationControlSettings 是注册容量与老用户免邀开关的原子读取结果。
type RegistrationControlSettings struct {
	UserLimit                        int64
	InvitationCodeEnabled            bool
	LegacyInvitationExemptionEnabled bool
}

// RegistrationCapacityStatus 描述当前用户量相对配置上限的状态。
type RegistrationCapacityStatus struct {
	Current   int64 `json:"current"`
	Limit     int64 `json:"limit"`
	Remaining int64 `json:"remaining"`
	Reached   bool  `json:"reached"`
}

// RegistrationLegacyEligibility 是业务库中的最小老用户资格记录。
type RegistrationLegacyEligibility struct {
	Eligible       bool
	FailureReasons []string
}

// RegistrationEligibilityStats 用于管理员确认名单是否已经完成导入。
type RegistrationEligibilityStats struct {
	HistoricalUsers int64
	EligibleUsers   int64
}

// RegistrationEligibilityCheckResult 是公开检测接口的稳定响应结构。
type RegistrationEligibilityCheckResult struct {
	Eligible    bool     `json:"eligible"`
	ReasonCodes []string `json:"reason_codes"`
}

// RegistrationControlRepository 是生产用户仓储提供的注册专用原子能力。
// 独立接口避免扩展 UserRepository 后破坏大量无关测试替身。
type RegistrationControlRepository interface {
	CountRegistrationUsers(ctx context.Context) (int64, error)
	GetRegistrationLegacyEligibility(ctx context.Context, normalizedEmail string) (*RegistrationLegacyEligibility, error)
	GetRegistrationEligibilityStats(ctx context.Context) (*RegistrationEligibilityStats, error)
	CreateWithRegistrationGuards(ctx context.Context, user *User, domain string, userLimit int64) error
}

// GetRegistrationControlSettings 一次读取注册门禁配置；损坏的上限值按配置错误拒绝放行。
func (s *SettingService) GetRegistrationControlSettings(ctx context.Context) (RegistrationControlSettings, error) {
	if s == nil || s.settingRepo == nil {
		return RegistrationControlSettings{}, ErrServiceUnavailable
	}
	values, err := s.settingRepo.GetMultiple(ctx, []string{
		SettingKeyRegistrationUserLimit,
		SettingKeyInvitationCodeEnabled,
		SettingKeyLegacyInvitationExemptionEnabled,
	})
	if err != nil {
		return RegistrationControlSettings{}, fmt.Errorf("load registration control settings: %w", err)
	}

	limit := int64(0)
	rawLimit := strings.TrimSpace(values[SettingKeyRegistrationUserLimit])
	if rawLimit != "" {
		parsed, parseErr := strconv.ParseInt(rawLimit, 10, 64)
		if parseErr != nil || parsed < 0 {
			return RegistrationControlSettings{}, fmt.Errorf("invalid registration user limit %q", rawLimit)
		}
		limit = parsed
	}
	return RegistrationControlSettings{
		UserLimit:                        limit,
		InvitationCodeEnabled:            values[SettingKeyInvitationCodeEnabled] == "true",
		LegacyInvitationExemptionEnabled: values[SettingKeyLegacyInvitationExemptionEnabled] == "true",
	}, nil
}

// GetRegistrationCapacityStatus 返回实时用户数与剩余容量。
func (s *AuthService) GetRegistrationCapacityStatus(ctx context.Context) (*RegistrationCapacityStatus, error) {
	if s == nil || s.settingService == nil {
		return nil, ErrServiceUnavailable
	}
	settings, err := s.settingService.GetRegistrationControlSettings(ctx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	repo, ok := s.userRepo.(RegistrationControlRepository)
	if !ok {
		if s.entClient != nil {
			return nil, ErrServiceUnavailable
		}
		return registrationCapacityStatus(0, settings.UserLimit), nil
	}
	current, err := repo.CountRegistrationUsers(ctx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	return registrationCapacityStatus(current, settings.UserLimit), nil
}

// GetRegistrationEligibilityStats 返回已导入历史/符合条件邮箱数量。
func (s *AuthService) GetRegistrationEligibilityStats(ctx context.Context) (*RegistrationEligibilityStats, error) {
	repo, ok := s.registrationControlRepository()
	if !ok {
		return &RegistrationEligibilityStats{}, nil
	}
	stats, err := repo.GetRegistrationEligibilityStats(ctx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	return stats, nil
}

// CheckRegistrationLegacyEligibility 先检查容量，再判断邮箱是否可免邀请码。
func (s *AuthService) CheckRegistrationLegacyEligibility(ctx context.Context, email string) (*RegistrationEligibilityCheckResult, error) {
	if s == nil || s.settingService == nil || !s.settingService.IsRegistrationEnabled(ctx) {
		return nil, ErrRegDisabled
	}
	settings, err := s.settingService.GetRegistrationControlSettings(ctx)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if err := s.ensureRegistrationCapacity(ctx, settings.UserLimit); err != nil {
		return &RegistrationEligibilityCheckResult{
			Eligible:    false,
			ReasonCodes: []string{"REGISTRATION_CAPACITY_REACHED"},
		}, nil
	}
	return s.checkRegistrationLegacyEligibilityRecord(ctx, email, settings.LegacyInvitationExemptionEnabled)
}

// authorizeRegistrationWithoutInvitation 校验所有非真实邀请码路径；容量判断必须排在名单判断之前。
func (s *AuthService) authorizeRegistrationWithoutInvitation(ctx context.Context, email string) error {
	settings, err := s.settingService.GetRegistrationControlSettings(ctx)
	if err != nil {
		return ErrServiceUnavailable
	}
	if err := s.ensureRegistrationCapacity(ctx, settings.UserLimit); err != nil {
		return err
	}
	if !settings.InvitationCodeEnabled {
		return nil
	}
	if !settings.LegacyInvitationExemptionEnabled {
		return ErrInvitationCodeRequired
	}
	result, err := s.checkRegistrationLegacyEligibilityRecord(ctx, email, true)
	if err != nil {
		return err
	}
	if result.Eligible {
		return nil
	}
	return ErrLegacyRegistrationNotEligible.WithMetadata(map[string]string{
		"reason_codes": strings.Join(result.ReasonCodes, ","),
	})
}

// authorizeRegistrationPreflight 只验证邀请码有效性，不占用；最终注册仍在事务内原子占用。
func (s *AuthService) authorizeRegistrationPreflight(ctx context.Context, email, invitationCode string) error {
	if s == nil || s.settingService == nil {
		return ErrServiceUnavailable
	}
	if !s.settingService.IsInvitationCodeEnabled(ctx) || strings.TrimSpace(invitationCode) == "" {
		return s.authorizeRegistrationWithoutInvitation(ctx, email)
	}
	if s.redeemRepo == nil {
		return ErrServiceUnavailable
	}
	redeemCode, err := s.redeemRepo.GetByCode(ctx, strings.TrimSpace(invitationCode))
	if err != nil || redeemCode.Type != RedeemTypeInvitation || !redeemCode.CanUse() {
		return ErrInvitationCodeInvalid
	}
	return nil
}

func (s *AuthService) checkRegistrationLegacyEligibilityRecord(ctx context.Context, email string, enabled bool) (*RegistrationEligibilityCheckResult, error) {
	if !enabled {
		return &RegistrationEligibilityCheckResult{
			Eligible:    false,
			ReasonCodes: []string{RegistrationEligibilityReasonExemptionDisabled},
		}, nil
	}
	normalized := NormalizeRegistrationEligibilityEmail(email)
	if normalized == "" {
		return &RegistrationEligibilityCheckResult{
			Eligible:    false,
			ReasonCodes: []string{RegistrationEligibilityReasonNotLegacyUser},
		}, nil
	}
	repo, ok := s.registrationControlRepository()
	if !ok {
		return nil, ErrServiceUnavailable
	}
	record, err := repo.GetRegistrationLegacyEligibility(ctx, normalized)
	if err != nil {
		return nil, ErrServiceUnavailable
	}
	if record == nil {
		return &RegistrationEligibilityCheckResult{
			Eligible:    false,
			ReasonCodes: []string{RegistrationEligibilityReasonNotLegacyUser},
		}, nil
	}
	if record.Eligible {
		return &RegistrationEligibilityCheckResult{Eligible: true, ReasonCodes: []string{}}, nil
	}
	reasons := make([]string, 0, len(record.FailureReasons))
	for _, reason := range record.FailureReasons {
		if mapped := publicRegistrationEligibilityReason(reason); mapped != "" {
			reasons = append(reasons, mapped)
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, RegistrationEligibilityReasonNotLegacyUser)
	}
	return &RegistrationEligibilityCheckResult{Eligible: false, ReasonCodes: reasons}, nil
}

// NormalizeRegistrationEligibilityEmail 与运维名单 normalized_email_basic 的口径保持一致。
func NormalizeRegistrationEligibilityEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *AuthService) ensureRegistrationCapacity(ctx context.Context, userLimit int64) error {
	if userLimit <= 0 {
		return nil
	}
	repo, ok := s.registrationControlRepository()
	if !ok {
		return ErrServiceUnavailable
	}
	current, err := repo.CountRegistrationUsers(ctx)
	if err != nil {
		return ErrServiceUnavailable
	}
	if current >= userLimit {
		return ErrRegistrationCapacityReached
	}
	return nil
}

func (s *AuthService) registrationControlRepository() (RegistrationControlRepository, bool) {
	if s == nil || s.userRepo == nil {
		return nil, false
	}
	repo, ok := s.userRepo.(RegistrationControlRepository)
	if !ok && s.entClient == nil {
		return nil, false
	}
	return repo, ok
}

func registrationCapacityStatus(current, limit int64) *RegistrationCapacityStatus {
	status := &RegistrationCapacityStatus{Current: current, Limit: limit}
	if limit <= 0 {
		return status
	}
	status.Remaining = limit - current
	if status.Remaining <= 0 {
		status.Remaining = 0
		status.Reached = true
	}
	return status
}

func publicRegistrationEligibilityReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "insufficient_success_calls":
		return RegistrationEligibilityReasonInsufficientSuccessCalls
	case "insufficient_active_days":
		return RegistrationEligibilityReasonInsufficientActiveDays
	case "cyber_policy_warning":
		return RegistrationEligibilityReasonCyberPolicyWarning
	case "soft_deleted":
		return RegistrationEligibilityReasonSoftDeleted
	default:
		return ""
	}
}

func parseRegistrationUserLimit(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}
