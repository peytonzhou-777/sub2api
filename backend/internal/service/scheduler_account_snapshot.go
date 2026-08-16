package service

import (
	"strings"
	"time"
)

// SchedulerAccountSnapshotSchemaVersion 是调度账号快照的结构版本。
// 修改调度决策字段时必须递增版本，并切换缓存命名空间，避免旧快照被新代码解释。
const SchedulerAccountSnapshotSchemaVersion = 2

// SchedulerPrivacyStatus 是调度使用的隐私合规状态，而不是账号持久化状态。
type SchedulerPrivacyStatus string

const (
	SchedulerPrivacyCompliant     SchedulerPrivacyStatus = "compliant"
	SchedulerPrivacyNoncompliant  SchedulerPrivacyStatus = "noncompliant"
	SchedulerPrivacyUnknown       SchedulerPrivacyStatus = "unknown"
	SchedulerPrivacyNotApplicable SchedulerPrivacyStatus = "not_applicable"
)

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
	RateLimitedAt           *time.Time `json:"rate_limited_at,omitempty"`
	RateLimitResetAt        *time.Time `json:"rate_limit_reset_at,omitempty"`
	OverloadUntil           *time.Time `json:"overload_until,omitempty"`
	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until,omitempty"`
	TempUnschedulableReason string     `json:"temp_unschedulable_reason,omitempty"`
	SessionWindowStart      *time.Time `json:"session_window_start,omitempty"`
	SessionWindowEnd        *time.Time `json:"session_window_end,omitempty"`
	SessionWindowStatus     string     `json:"session_window_status,omitempty"`

	ParentAccountID *int64         `json:"parent_account_id,omitempty"`
	QuotaDimension  string         `json:"quota_dimension,omitempty"`
	AccountGroups   []AccountGroup `json:"account_groups,omitempty"`
	GroupIDs        []int64        `json:"group_ids,omitempty"`
	Credentials     map[string]any `json:"credentials,omitempty"`
	Extra           map[string]any `json:"extra,omitempty"`

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
		Credentials:             account.Credentials,
		Extra:                   account.Extra,
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
		Credentials:             s.Credentials,
		Extra:                   s.Extra,
	}
	account.SchedulerPrivacyStatus = normalizeSchedulerPrivacyStatus(s.PrivacyStatus)
	account.SchedulerSnapshotNeedsRefresh = s.NeedsRefresh
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
