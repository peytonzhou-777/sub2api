package service

import (
	"strings"
	"time"
)

// SchedulerAccountSnapshotSchemaVersion 是调度账号快照的结构版本。
// 修改调度决策字段时必须递增版本，并切换缓存命名空间，避免旧快照被新代码解释。
const SchedulerAccountSnapshotSchemaVersion = 4

// SchedulerPrivacyStatus 是调度使用的隐私合规状态，而不是账号持久化状态。
type SchedulerPrivacyStatus string

const (
	SchedulerPrivacyCompliant     SchedulerPrivacyStatus = "compliant"
	SchedulerPrivacyNoncompliant  SchedulerPrivacyStatus = "noncompliant"
	SchedulerPrivacyUnknown       SchedulerPrivacyStatus = "unknown"
	SchedulerPrivacyNotApplicable SchedulerPrivacyStatus = "not_applicable"
)

// SchedulerAccountCredentialsSnapshot 只声明选号阶段允许读取的凭据字段。
// 访问令牌、刷新令牌和 API Key 等密钥不得进入调度元数据缓存。
type SchedulerAccountCredentialsSnapshot struct {
	ModelMapping        any `json:"model_mapping,omitempty"`
	CompactModelMapping any `json:"compact_model_mapping,omitempty"`
	ProjectID           any `json:"project_id,omitempty"`
	OAuthType           any `json:"oauth_type,omitempty"`
	PlanType            any `json:"plan_type,omitempty"`
	SubscriptionTier    any `json:"subscription_tier,omitempty"`
	OpenAICapabilities  any `json:"openai_capabilities,omitempty"`
	AuthMode            any `json:"auth_mode,omitempty"`
	OpenAIAuthMode      any `json:"openai_auth_mode,omitempty"`
}

// SchedulerAccountExtraSnapshot 显式声明选号阶段使用的 Extra 字段。
// 新增调度条件时必须在此投影中声明并升级快照 schema。
type SchedulerAccountExtraSnapshot struct {
	QuotaLimit              any `json:"quota_limit,omitempty"`
	QuotaUsed               any `json:"quota_used,omitempty"`
	QuotaDailyLimit         any `json:"quota_daily_limit,omitempty"`
	QuotaDailyUsed          any `json:"quota_daily_used,omitempty"`
	QuotaDailyStart         any `json:"quota_daily_start,omitempty"`
	QuotaDailyResetMode     any `json:"quota_daily_reset_mode,omitempty"`
	QuotaDailyResetHour     any `json:"quota_daily_reset_hour,omitempty"`
	QuotaWeeklyLimit        any `json:"quota_weekly_limit,omitempty"`
	QuotaWeeklyUsed         any `json:"quota_weekly_used,omitempty"`
	QuotaWeeklyStart        any `json:"quota_weekly_start,omitempty"`
	QuotaWeeklyResetMode    any `json:"quota_weekly_reset_mode,omitempty"`
	QuotaWeeklyResetDay     any `json:"quota_weekly_reset_day,omitempty"`
	QuotaWeeklyResetHour    any `json:"quota_weekly_reset_hour,omitempty"`
	QuotaResetTimezone      any `json:"quota_reset_timezone,omitempty"`
	MixedScheduling         any `json:"mixed_scheduling,omitempty"`
	AllowOverages           any `json:"allow_overages,omitempty"`
	WindowCostLimit         any `json:"window_cost_limit,omitempty"`
	WindowCostStickyReserve any `json:"window_cost_sticky_reserve,omitempty"`
	MaxSessions             any `json:"max_sessions,omitempty"`
	SessionIdleTimeout      any `json:"session_idle_timeout_minutes,omitempty"`
	BaseRPM                 any `json:"base_rpm,omitempty"`
	RPMStrategy             any `json:"rpm_strategy,omitempty"`
	RPMStickyBuffer         any `json:"rpm_sticky_buffer,omitempty"`

	OpenAIOAuthResponsesWSEnabled  any `json:"openai_oauth_responses_websockets_v2_enabled,omitempty"`
	OpenAIOAuthResponsesWSMode     any `json:"openai_oauth_responses_websockets_v2_mode,omitempty"`
	OpenAIAPIKeyResponsesWSEnabled any `json:"openai_apikey_responses_websockets_v2_enabled,omitempty"`
	OpenAIAPIKeyResponsesWSMode    any `json:"openai_apikey_responses_websockets_v2_mode,omitempty"`
	ResponsesWSEnabled             any `json:"responses_websockets_v2_enabled,omitempty"`
	OpenAIWSEnabled                any `json:"openai_ws_enabled,omitempty"`
	OpenAIWSForceHTTP              any `json:"openai_ws_force_http,omitempty"`
	OpenAIResponsesMode            any `json:"openai_responses_mode,omitempty"`
	OpenAIResponsesSupported       any `json:"openai_responses_supported,omitempty"`
	OpenAIPassthrough              any `json:"openai_passthrough,omitempty"`
	OpenAIOAuthPassthrough         any `json:"openai_oauth_passthrough,omitempty"`
	OpenAICompactMode              any `json:"openai_compact_mode,omitempty"`
	OpenAICompactSupported         any `json:"openai_compact_supported,omitempty"`

	Codex5HUsedPercent              any `json:"codex_5h_used_percent,omitempty"`
	Codex7DUsedPercent              any `json:"codex_7d_used_percent,omitempty"`
	CodexPrimaryUsedPercent         any `json:"codex_primary_used_percent,omitempty"`
	CodexSecondaryUsedPercent       any `json:"codex_secondary_used_percent,omitempty"`
	Codex5HResetAt                  any `json:"codex_5h_reset_at,omitempty"`
	Codex7DResetAt                  any `json:"codex_7d_reset_at,omitempty"`
	Codex5HResetAfterSeconds        any `json:"codex_5h_reset_after_seconds,omitempty"`
	Codex7DResetAfterSeconds        any `json:"codex_7d_reset_after_seconds,omitempty"`
	CodexPrimaryResetAfterSeconds   any `json:"codex_primary_reset_after_seconds,omitempty"`
	CodexSecondaryResetAfterSeconds any `json:"codex_secondary_reset_after_seconds,omitempty"`
	CodexUsageUpdatedAt             any `json:"codex_usage_updated_at,omitempty"`
	AutoPause5HThreshold            any `json:"auto_pause_5h_threshold,omitempty"`
	AutoPause7DThreshold            any `json:"auto_pause_7d_threshold,omitempty"`
	AutoPause5HDisabled             any `json:"auto_pause_5h_disabled,omitempty"`
	AutoPause7DDisabled             any `json:"auto_pause_7d_disabled,omitempty"`

	ModelRateLimits      any `json:"model_rate_limits,omitempty"`
	UpstreamBillingProbe any `json:"upstream_billing_probe,omitempty"`
	GrokMediaEligible    any `json:"grok_media_eligible,omitempty"`
	GrokBillingSnapshot  any `json:"grok_billing_snapshot,omitempty"`
	GrokUsageSnapshot    any `json:"grok_usage_snapshot,omitempty"`
	SubscriptionTier     any `json:"subscription_tier,omitempty"`
	PlanType             any `json:"plan_type,omitempty"`
	PrivacyMode          any `json:"privacy_mode,omitempty"`
}

func newSchedulerAccountCredentialsSnapshot(credentials map[string]any) *SchedulerAccountCredentialsSnapshot {
	if len(credentials) == 0 {
		return nil
	}
	snapshot := &SchedulerAccountCredentialsSnapshot{
		ModelMapping:        credentials["model_mapping"],
		CompactModelMapping: credentials["compact_model_mapping"],
		ProjectID:           credentials["project_id"],
		OAuthType:           credentials["oauth_type"],
		PlanType:            credentials["plan_type"],
		SubscriptionTier:    credentials["subscription_tier"],
		OpenAICapabilities:  credentials[openAIEndpointCapabilitiesCredentialKey],
		AuthMode:            credentials[openAIAuthModeCredentialKey],
		OpenAIAuthMode:      credentials[openAIAuthModeLegacyCredentialKey],
	}
	if len(snapshot.toMap()) == 0 {
		return nil
	}
	return snapshot
}

func (s *SchedulerAccountCredentialsSnapshot) toMap() map[string]any {
	if s == nil {
		return nil
	}
	result := make(map[string]any)
	putSchedulerSnapshotValue(result, "model_mapping", s.ModelMapping)
	putSchedulerSnapshotValue(result, "compact_model_mapping", s.CompactModelMapping)
	putSchedulerSnapshotValue(result, "project_id", s.ProjectID)
	putSchedulerSnapshotValue(result, "oauth_type", s.OAuthType)
	putSchedulerSnapshotValue(result, "plan_type", s.PlanType)
	putSchedulerSnapshotValue(result, "subscription_tier", s.SubscriptionTier)
	putSchedulerSnapshotValue(result, openAIEndpointCapabilitiesCredentialKey, s.OpenAICapabilities)
	putSchedulerSnapshotValue(result, openAIAuthModeCredentialKey, s.AuthMode)
	putSchedulerSnapshotValue(result, openAIAuthModeLegacyCredentialKey, s.OpenAIAuthMode)
	if len(result) == 0 {
		return nil
	}
	return result
}

func newSchedulerAccountExtraSnapshot(extra map[string]any) *SchedulerAccountExtraSnapshot {
	if len(extra) == 0 {
		return nil
	}
	snapshot := &SchedulerAccountExtraSnapshot{
		QuotaLimit:                      extra["quota_limit"],
		QuotaUsed:                       extra["quota_used"],
		QuotaDailyLimit:                 extra["quota_daily_limit"],
		QuotaDailyUsed:                  extra["quota_daily_used"],
		QuotaDailyStart:                 extra["quota_daily_start"],
		QuotaDailyResetMode:             extra["quota_daily_reset_mode"],
		QuotaDailyResetHour:             extra["quota_daily_reset_hour"],
		QuotaWeeklyLimit:                extra["quota_weekly_limit"],
		QuotaWeeklyUsed:                 extra["quota_weekly_used"],
		QuotaWeeklyStart:                extra["quota_weekly_start"],
		QuotaWeeklyResetMode:            extra["quota_weekly_reset_mode"],
		QuotaWeeklyResetDay:             extra["quota_weekly_reset_day"],
		QuotaWeeklyResetHour:            extra["quota_weekly_reset_hour"],
		QuotaResetTimezone:              extra["quota_reset_timezone"],
		MixedScheduling:                 extra["mixed_scheduling"],
		AllowOverages:                   extra["allow_overages"],
		WindowCostLimit:                 extra["window_cost_limit"],
		WindowCostStickyReserve:         extra["window_cost_sticky_reserve"],
		MaxSessions:                     extra["max_sessions"],
		SessionIdleTimeout:              extra["session_idle_timeout_minutes"],
		BaseRPM:                         extra["base_rpm"],
		RPMStrategy:                     extra["rpm_strategy"],
		RPMStickyBuffer:                 extra["rpm_sticky_buffer"],
		OpenAIOAuthResponsesWSEnabled:   extra["openai_oauth_responses_websockets_v2_enabled"],
		OpenAIOAuthResponsesWSMode:      extra["openai_oauth_responses_websockets_v2_mode"],
		OpenAIAPIKeyResponsesWSEnabled:  extra["openai_apikey_responses_websockets_v2_enabled"],
		OpenAIAPIKeyResponsesWSMode:     extra["openai_apikey_responses_websockets_v2_mode"],
		ResponsesWSEnabled:              extra["responses_websockets_v2_enabled"],
		OpenAIWSEnabled:                 extra["openai_ws_enabled"],
		OpenAIWSForceHTTP:               extra["openai_ws_force_http"],
		OpenAIResponsesMode:             extra["openai_responses_mode"],
		OpenAIResponsesSupported:        extra["openai_responses_supported"],
		OpenAIPassthrough:               extra["openai_passthrough"],
		OpenAIOAuthPassthrough:          extra["openai_oauth_passthrough"],
		OpenAICompactMode:               extra["openai_compact_mode"],
		OpenAICompactSupported:          extra["openai_compact_supported"],
		Codex5HUsedPercent:              extra["codex_5h_used_percent"],
		Codex7DUsedPercent:              extra["codex_7d_used_percent"],
		CodexPrimaryUsedPercent:         extra["codex_primary_used_percent"],
		CodexSecondaryUsedPercent:       extra["codex_secondary_used_percent"],
		Codex5HResetAt:                  extra["codex_5h_reset_at"],
		Codex7DResetAt:                  extra["codex_7d_reset_at"],
		Codex5HResetAfterSeconds:        extra["codex_5h_reset_after_seconds"],
		Codex7DResetAfterSeconds:        extra["codex_7d_reset_after_seconds"],
		CodexPrimaryResetAfterSeconds:   extra["codex_primary_reset_after_seconds"],
		CodexSecondaryResetAfterSeconds: extra["codex_secondary_reset_after_seconds"],
		CodexUsageUpdatedAt:             extra["codex_usage_updated_at"],
		AutoPause5HThreshold:            extra["auto_pause_5h_threshold"],
		AutoPause7DThreshold:            extra["auto_pause_7d_threshold"],
		AutoPause5HDisabled:             extra["auto_pause_5h_disabled"],
		AutoPause7DDisabled:             extra["auto_pause_7d_disabled"],
		ModelRateLimits:                 extra["model_rate_limits"],
		UpstreamBillingProbe:            extra[UpstreamBillingProbeExtraKey],
		GrokMediaEligible:               extra[GrokMediaEligibleExtraKey],
		GrokBillingSnapshot:             extra["grok_billing_snapshot"],
		GrokUsageSnapshot:               extra["grok_usage_snapshot"],
		SubscriptionTier:                extra["subscription_tier"],
		PlanType:                        extra["plan_type"],
		PrivacyMode:                     extra["privacy_mode"],
	}
	if len(snapshot.toMap()) == 0 {
		return nil
	}
	return snapshot
}

func (s *SchedulerAccountExtraSnapshot) toMap() map[string]any {
	if s == nil {
		return nil
	}
	values := map[string]any{
		"quota_limit": s.QuotaLimit, "quota_used": s.QuotaUsed,
		"quota_daily_limit": s.QuotaDailyLimit, "quota_daily_used": s.QuotaDailyUsed,
		"quota_daily_start": s.QuotaDailyStart, "quota_daily_reset_mode": s.QuotaDailyResetMode,
		"quota_daily_reset_hour": s.QuotaDailyResetHour, "quota_weekly_limit": s.QuotaWeeklyLimit,
		"quota_weekly_used": s.QuotaWeeklyUsed, "quota_weekly_start": s.QuotaWeeklyStart,
		"quota_weekly_reset_mode": s.QuotaWeeklyResetMode, "quota_weekly_reset_day": s.QuotaWeeklyResetDay,
		"quota_weekly_reset_hour": s.QuotaWeeklyResetHour, "quota_reset_timezone": s.QuotaResetTimezone,
		"mixed_scheduling": s.MixedScheduling, "allow_overages": s.AllowOverages,
		"window_cost_limit": s.WindowCostLimit, "window_cost_sticky_reserve": s.WindowCostStickyReserve,
		"max_sessions": s.MaxSessions, "session_idle_timeout_minutes": s.SessionIdleTimeout,
		"base_rpm": s.BaseRPM, "rpm_strategy": s.RPMStrategy, "rpm_sticky_buffer": s.RPMStickyBuffer,
		"openai_oauth_responses_websockets_v2_enabled":  s.OpenAIOAuthResponsesWSEnabled,
		"openai_oauth_responses_websockets_v2_mode":     s.OpenAIOAuthResponsesWSMode,
		"openai_apikey_responses_websockets_v2_enabled": s.OpenAIAPIKeyResponsesWSEnabled,
		"openai_apikey_responses_websockets_v2_mode":    s.OpenAIAPIKeyResponsesWSMode,
		"responses_websockets_v2_enabled":               s.ResponsesWSEnabled, "openai_ws_enabled": s.OpenAIWSEnabled,
		"openai_ws_force_http": s.OpenAIWSForceHTTP, "openai_responses_mode": s.OpenAIResponsesMode,
		"openai_responses_supported": s.OpenAIResponsesSupported, "openai_passthrough": s.OpenAIPassthrough,
		"openai_oauth_passthrough": s.OpenAIOAuthPassthrough, "openai_compact_mode": s.OpenAICompactMode,
		"openai_compact_supported": s.OpenAICompactSupported,
		"codex_5h_used_percent":    s.Codex5HUsedPercent, "codex_7d_used_percent": s.Codex7DUsedPercent,
		"codex_primary_used_percent": s.CodexPrimaryUsedPercent, "codex_secondary_used_percent": s.CodexSecondaryUsedPercent,
		"codex_5h_reset_at": s.Codex5HResetAt, "codex_7d_reset_at": s.Codex7DResetAt,
		"codex_5h_reset_after_seconds": s.Codex5HResetAfterSeconds, "codex_7d_reset_after_seconds": s.Codex7DResetAfterSeconds,
		"codex_primary_reset_after_seconds":   s.CodexPrimaryResetAfterSeconds,
		"codex_secondary_reset_after_seconds": s.CodexSecondaryResetAfterSeconds,
		"codex_usage_updated_at":              s.CodexUsageUpdatedAt,
		"auto_pause_5h_threshold":             s.AutoPause5HThreshold, "auto_pause_7d_threshold": s.AutoPause7DThreshold,
		"auto_pause_5h_disabled": s.AutoPause5HDisabled, "auto_pause_7d_disabled": s.AutoPause7DDisabled,
		"model_rate_limits": s.ModelRateLimits, UpstreamBillingProbeExtraKey: s.UpstreamBillingProbe,
		GrokMediaEligibleExtraKey: s.GrokMediaEligible, "grok_billing_snapshot": s.GrokBillingSnapshot,
		"grok_usage_snapshot": s.GrokUsageSnapshot, "subscription_tier": s.SubscriptionTier,
		"plan_type": s.PlanType, "privacy_mode": s.PrivacyMode,
	}
	for key, value := range values {
		if value == nil {
			delete(values, key)
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func putSchedulerSnapshotValue(target map[string]any, key string, value any) {
	if value != nil {
		target[key] = value
	}
}

// SchedulerAccountSnapshot 明确承载调度决策所需的账号投影。
// 隐私状态独立于 Extra；新增调度条件时必须显式扩展此结构并升级 schema。
type SchedulerAccountSnapshot struct {
	SchemaVersion int `json:"schema_version"`

	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Type        string `json:"type"`
	Concurrency int    `json:"concurrency"`
	LoadFactor  *int   `json:"load_factor,omitempty"`
	Priority    int    `json:"priority"`

	RateMultiplier     *float64 `json:"rate_multiplier,omitempty"`
	Status             string   `json:"status"`
	Schedulable        bool     `json:"schedulable"`
	AutoPauseOnExpired bool     `json:"auto_pause_on_expired"`

	LastUsedAt              *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt               *time.Time `json:"expires_at,omitempty"`
	ProxyID                 *int64     `json:"proxy_id,omitempty"`
	RateLimitedAt           *time.Time `json:"rate_limited_at,omitempty"`
	RateLimitResetAt        *time.Time `json:"rate_limit_reset_at,omitempty"`
	OverloadUntil           *time.Time `json:"overload_until,omitempty"`
	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until,omitempty"`
	TempUnschedulableReason string     `json:"temp_unschedulable_reason,omitempty"`
	SessionWindowStart      *time.Time `json:"session_window_start,omitempty"`
	SessionWindowEnd        *time.Time `json:"session_window_end,omitempty"`
	SessionWindowStatus     string     `json:"session_window_status,omitempty"`

	ParentAccountID *int64                               `json:"parent_account_id,omitempty"`
	QuotaDimension  string                               `json:"quota_dimension,omitempty"`
	AccountGroups   []AccountGroup                       `json:"account_groups,omitempty"`
	GroupIDs        []int64                              `json:"group_ids,omitempty"`
	Credentials     *SchedulerAccountCredentialsSnapshot `json:"credentials,omitempty"`
	Extra           *SchedulerAccountExtraSnapshot       `json:"extra,omitempty"`

	PrivacyStatus SchedulerPrivacyStatus `json:"privacy_status"`
	NeedsRefresh  bool                   `json:"-"`
}

// NewSchedulerAccountSnapshot 从数据库账号构造强类型调度投影。
func NewSchedulerAccountSnapshot(account Account) SchedulerAccountSnapshot {
	status := ResolveSchedulerPrivacyStatus(account)
	return SchedulerAccountSnapshot{
		SchemaVersion:           SchedulerAccountSnapshotSchemaVersion,
		ID:                      account.ID,
		Name:                    account.Name,
		Platform:                account.Platform,
		Type:                    account.Type,
		Concurrency:             account.Concurrency,
		LoadFactor:              account.LoadFactor,
		Priority:                account.Priority,
		RateMultiplier:          account.RateMultiplier,
		Status:                  account.Status,
		Schedulable:             account.Schedulable,
		AutoPauseOnExpired:      account.AutoPauseOnExpired,
		LastUsedAt:              account.LastUsedAt,
		ExpiresAt:               account.ExpiresAt,
		ProxyID:                 account.ProxyID,
		RateLimitedAt:           account.RateLimitedAt,
		RateLimitResetAt:        account.RateLimitResetAt,
		OverloadUntil:           account.OverloadUntil,
		TempUnschedulableUntil:  account.TempUnschedulableUntil,
		TempUnschedulableReason: account.TempUnschedulableReason,
		SessionWindowStart:      account.SessionWindowStart,
		SessionWindowEnd:        account.SessionWindowEnd,
		SessionWindowStatus:     account.SessionWindowStatus,
		ParentAccountID:         account.ParentAccountID,
		QuotaDimension:          account.QuotaDimension,
		AccountGroups:           account.AccountGroups,
		GroupIDs:                account.GroupIDs,
		Credentials:             newSchedulerAccountCredentialsSnapshot(account.Credentials),
		Extra:                   newSchedulerAccountExtraSnapshot(account.Extra),
		PrivacyStatus:           status,
	}
}

// ToAccount 将调度投影转换为兼容现有调度能力检查的账号，并保留四态隐私结果。
func (s SchedulerAccountSnapshot) ToAccount() Account {
	account := Account{
		ID:                      s.ID,
		Name:                    s.Name,
		Platform:                s.Platform,
		Type:                    s.Type,
		Concurrency:             s.Concurrency,
		LoadFactor:              s.LoadFactor,
		Priority:                s.Priority,
		RateMultiplier:          s.RateMultiplier,
		Status:                  s.Status,
		Schedulable:             s.Schedulable,
		AutoPauseOnExpired:      s.AutoPauseOnExpired,
		LastUsedAt:              s.LastUsedAt,
		ExpiresAt:               s.ExpiresAt,
		ProxyID:                 s.ProxyID,
		RateLimitedAt:           s.RateLimitedAt,
		RateLimitResetAt:        s.RateLimitResetAt,
		OverloadUntil:           s.OverloadUntil,
		TempUnschedulableUntil:  s.TempUnschedulableUntil,
		TempUnschedulableReason: s.TempUnschedulableReason,
		SessionWindowStart:      s.SessionWindowStart,
		SessionWindowEnd:        s.SessionWindowEnd,
		SessionWindowStatus:     s.SessionWindowStatus,
		ParentAccountID:         s.ParentAccountID,
		QuotaDimension:          s.QuotaDimension,
		AccountGroups:           s.AccountGroups,
		GroupIDs:                s.GroupIDs,
		Credentials:             s.Credentials.toMap(),
		Extra:                   s.Extra.toMap(),
	}
	account.SchedulerPrivacyStatus = normalizeSchedulerPrivacyStatus(s.PrivacyStatus)
	account.SchedulerSnapshotNeedsRefresh = s.NeedsRefresh
	account.IsSchedulerSnapshot = true
	return account
}

// ResolveSchedulerPrivacyStatus 根据原始 Extra 计算隐私四态。
// OpenAI 缺失或空值是 unknown；只有明确失败值才是 noncompliant。
func ResolveSchedulerPrivacyStatus(account Account) SchedulerPrivacyStatus {
	if account.Platform != PlatformOpenAI && account.Platform != PlatformAntigravity {
		return SchedulerPrivacyNotApplicable
	}
	if account.Extra == nil {
		return SchedulerPrivacyUnknown
	}
	raw, exists := account.Extra["privacy_mode"]
	if !exists || raw == nil {
		return SchedulerPrivacyUnknown
	}
	mode, ok := raw.(string)
	if !ok || strings.TrimSpace(mode) == "" {
		return SchedulerPrivacyUnknown
	}
	mode = strings.TrimSpace(mode)
	switch account.Platform {
	case PlatformOpenAI:
		if mode == PrivacyModeTrainingOff {
			return SchedulerPrivacyCompliant
		}
		if mode == PrivacyModeFailed || mode == PrivacyModeCFBlocked {
			return SchedulerPrivacyNoncompliant
		}
		return SchedulerPrivacyUnknown
	case PlatformAntigravity:
		if mode == AntigravityPrivacySet {
			return SchedulerPrivacyCompliant
		}
		return SchedulerPrivacyNoncompliant
	default:
		return SchedulerPrivacyNotApplicable
	}
}

func normalizeSchedulerPrivacyStatus(status SchedulerPrivacyStatus) SchedulerPrivacyStatus {
	switch status {
	case SchedulerPrivacyCompliant, SchedulerPrivacyNoncompliant, SchedulerPrivacyUnknown, SchedulerPrivacyNotApplicable:
		return status
	default:
		return SchedulerPrivacyUnknown
	}
}

// SchedulerPrivacyAllowsSelection 只允许明确合规或不适用的账号进入 require_privacy_set 筛选。
func SchedulerPrivacyAllowsSelection(account Account, requirePrivacySet bool) bool {
	if !requirePrivacySet {
		return true
	}
	status := normalizeSchedulerPrivacyStatus(account.SchedulerPrivacyStatus)
	if status == SchedulerPrivacyUnknown {
		status = ResolveSchedulerPrivacyStatus(account)
	}
	return status == SchedulerPrivacyCompliant || status == SchedulerPrivacyNotApplicable
}

func annotateSchedulerAccount(account Account) Account {
	snapshot := NewSchedulerAccountSnapshot(account)
	return snapshot.ToAccount()
}

func annotateSchedulerAccounts(accounts []Account) []Account {
	for i := range accounts {
		accounts[i] = annotateSchedulerAccount(accounts[i])
	}
	return accounts
}

func schedulerAccountsNeedRefresh(accounts []Account) bool {
	for i := range accounts {
		if accounts[i].SchedulerSnapshotNeedsRefresh {
			return true
		}
	}
	return false
}
