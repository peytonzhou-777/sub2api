package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

// OpenAIUserAffinityResident 是管理员按账号查看的当前居民投影。
type OpenAIUserAffinityResident struct {
	UserID         int64      `json:"user_id"`
	UserEmail      string     `json:"user_email"`
	AccountID      int64      `json:"account_id"`
	ScopeKey       string     `json:"scope_key"`
	ResidentSlotID int64      `json:"resident_slot_id"`
	SlotIndex      int        `json:"slot_index"`
	Generation     int64      `json:"generation"`
	Status         string     `json:"status"`
	AssignedAt     time.Time  `json:"assigned_at"`
	LastActiveAt   *time.Time `json:"last_active_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	UsageScore     float64    `json:"usage_score"`
	ActiveRoute    bool       `json:"active_route"`
	SoftOwner      bool       `json:"soft_owner"`
	TouchExpiresAt *time.Time `json:"touch_expires_at"`
}

// OpenAIUserAffinityAdminEvent 是管理员反查用户搬迁历史的只读投影。
type OpenAIUserAffinityAdminEvent struct {
	ID                  int64     `json:"id"`
	ScopeKey            string    `json:"scope_key"`
	PlacementGeneration int64     `json:"placement_generation"`
	SourceAccountID     *int64    `json:"source_account_id"`
	TargetAccountID     *int64    `json:"target_account_id"`
	EventType           string    `json:"event_type"`
	Reason              string    `json:"reason"`
	ActorAdminID        *int64    `json:"actor_admin_id"`
	ResidentSlotID      *int64    `json:"resident_slot_id"`
	CreatedAt           time.Time `json:"created_at"`
}

type OpenAIUserAffinityUserDetail struct {
	Placement     *OpenAIUserPlacement           `json:"placement,omitempty"`
	Placements    []OpenAIUserPlacement          `json:"placements"`
	ResidentSlots []OpenAIUserResidentSlot       `json:"resident_slots"`
	Events        []OpenAIUserAffinityAdminEvent `json:"events"`
}

// OpenAIUserAffinityAccountPolicy 是账号级覆盖；nil 表示继承网关全局配置。
type OpenAIUserAffinityAccountPolicy struct {
	AccountID                         int64      `json:"account_id"`
	MaxResidentUsers                  *int       `json:"max_resident_users"`
	NewResidentCooldownSeconds        *int       `json:"new_resident_cooldown_seconds"`
	CapacityFailureMigrationThreshold *int       `json:"capacity_failure_migration_threshold"`
	CapacityFailureWindowSeconds      *int       `json:"capacity_failure_window_seconds"`
	NewResidentCooldownUntil          *time.Time `json:"new_resident_cooldown_until"`
	AffinityConfigVersion             int64      `json:"affinity_config_version"`
}

// OpenAIUserAffinityAdminStore 是管理员查询和重置归属所需的可选仓储能力。
type OpenAIUserAffinityAdminStore interface {
	ListOpenAIUserAffinityResidents(ctx context.Context, accountID int64, limit, offset int) ([]OpenAIUserAffinityResident, int64, error)
	GetOpenAIUserAffinityUserDetail(ctx context.Context, userID int64, eventLimit int) (*OpenAIUserAffinityUserDetail, error)
	ResetOpenAIUserAffinityPlacement(ctx context.Context, userID, actorAdminID int64, scopeKey string, excludeSource bool) error
	GetOpenAIUserAffinityAccountPolicy(ctx context.Context, accountID int64) (*OpenAIUserAffinityAccountPolicy, error)
	UpdateOpenAIUserAffinityAccountPolicy(ctx context.Context, policy OpenAIUserAffinityAccountPolicy) error
}

// OpenAIUserAffinityAdminService 由管理员 handler 通过可选接口使用，避免扩大既有 AdminService 测试契约。
type OpenAIUserAffinityAdminService interface {
	ListOpenAIUserAffinityResidents(ctx context.Context, accountID int64, limit, offset int) ([]OpenAIUserAffinityResident, int64, error)
	GetOpenAIUserAffinityUserDetail(ctx context.Context, userID int64, eventLimit int) (*OpenAIUserAffinityUserDetail, error)
	ResetOpenAIUserAffinityPlacement(ctx context.Context, userID, actorAdminID int64, scopeKey string, excludeSource bool) error
	GetOpenAIUserAffinityAccountPolicy(ctx context.Context, accountID int64) (*OpenAIUserAffinityAccountPolicy, error)
	UpdateOpenAIUserAffinityAccountPolicy(ctx context.Context, policy OpenAIUserAffinityAccountPolicy) error
}

func (s *adminServiceImpl) ListOpenAIUserAffinityResidents(ctx context.Context, accountID int64, limit, offset int) ([]OpenAIUserAffinityResident, int64, error) {
	store, ok := s.accountRepo.(OpenAIUserAffinityAdminStore)
	if !ok {
		return nil, 0, errors.New("openai user affinity admin storage unavailable")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return store.ListOpenAIUserAffinityResidents(ctx, accountID, limit, offset)
}

func (s *adminServiceImpl) GetOpenAIUserAffinityUserDetail(ctx context.Context, userID int64, eventLimit int) (*OpenAIUserAffinityUserDetail, error) {
	store, ok := s.accountRepo.(OpenAIUserAffinityAdminStore)
	if !ok {
		return nil, errors.New("openai user affinity admin storage unavailable")
	}
	if eventLimit <= 0 || eventLimit > 200 {
		eventLimit = 50
	}
	return store.GetOpenAIUserAffinityUserDetail(ctx, userID, eventLimit)
}

func (s *adminServiceImpl) ResetOpenAIUserAffinityPlacement(ctx context.Context, userID, actorAdminID int64, scopeKey string, excludeSource bool) error {
	store, ok := s.accountRepo.(OpenAIUserAffinityAdminStore)
	if !ok {
		return errors.New("openai user affinity admin storage unavailable")
	}
	scopeKey = strings.TrimSpace(scopeKey)
	// 空 scope 表示管理员对该用户执行全 scope 重置；仓储负责枚举并逐 scope 原子清理。
	if !excludeSource && s.settingService != nil {
		config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
		if err != nil {
			return err
		}
		excludeSource = config.ManualResetExcludeSourceAccount
	}
	return store.ResetOpenAIUserAffinityPlacement(ctx, userID, actorAdminID, scopeKey, excludeSource)
}

func (s *adminServiceImpl) GetOpenAIUserAffinityAccountPolicy(ctx context.Context, accountID int64) (*OpenAIUserAffinityAccountPolicy, error) {
	store, ok := s.accountRepo.(OpenAIUserAffinityAdminStore)
	if !ok {
		return nil, errors.New("openai user affinity admin storage unavailable")
	}
	return store.GetOpenAIUserAffinityAccountPolicy(ctx, accountID)
}

func (s *adminServiceImpl) UpdateOpenAIUserAffinityAccountPolicy(ctx context.Context, policy OpenAIUserAffinityAccountPolicy) error {
	store, ok := s.accountRepo.(OpenAIUserAffinityAdminStore)
	if !ok {
		return errors.New("openai user affinity admin storage unavailable")
	}
	if policy.MaxResidentUsers != nil && (*policy.MaxResidentUsers < 1 || *policy.MaxResidentUsers > 10000) {
		return errors.New("max_resident_users must be between 1 and 10000")
	}
	if policy.NewResidentCooldownSeconds != nil && (*policy.NewResidentCooldownSeconds < 1 || *policy.NewResidentCooldownSeconds > 86400) {
		return errors.New("new_resident_cooldown_seconds must be between 1 and 86400")
	}
	if policy.CapacityFailureMigrationThreshold != nil && (*policy.CapacityFailureMigrationThreshold < 2 || *policy.CapacityFailureMigrationThreshold > 100) {
		return errors.New("capacity_failure_migration_threshold must be between 2 and 100")
	}
	if policy.CapacityFailureWindowSeconds != nil && (*policy.CapacityFailureWindowSeconds < 10 || *policy.CapacityFailureWindowSeconds > 3600) {
		return errors.New("capacity_failure_window_seconds must be between 10 and 3600")
	}
	return store.UpdateOpenAIUserAffinityAccountPolicy(ctx, policy)
}
