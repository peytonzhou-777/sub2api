package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const DisabledReasonSecurityDepositInsufficient = "security_deposit_insufficient"

// SecurityDepositKeyReference 是资格重算所需的最小密钥投影。
type SecurityDepositKeyReference struct {
	ID      int64
	UserID  int64
	Key     string
	GroupID int64
}

// SecurityDepositKeyEligibilityRepository 提供 active 密钥的条件禁用能力。
type SecurityDepositKeyEligibilityRepository interface {
	ListActiveSecurityDepositKeys(ctx context.Context, userID int64) ([]SecurityDepositKeyReference, error)
	ListActiveSecurityDepositKeysByGroup(ctx context.Context, groupID int64) ([]SecurityDepositKeyReference, error)
	DisableActiveSecurityDepositKeys(ctx context.Context, keyIDs []int64, eventType string, eventID int64, disabledAt time.Time) ([]SecurityDepositKeyReference, error)
}

// SecurityDepositKeyChangeReconciler 在任意保证金变化后只负责禁用不足密钥。
type SecurityDepositKeyChangeReconciler interface {
	DisableInsufficientKeys(ctx context.Context, userID int64, eventType string, eventID int64) ([]SecurityDepositKeyReference, error)
}

// SecurityDepositGroupKeyReconciler 在管理员确认后按分组批量禁用不足密钥。
type SecurityDepositGroupKeyReconciler interface {
	DisableInsufficientKeysByGroup(ctx context.Context, groupID int64, eventType string, eventID int64) ([]SecurityDepositKeyReference, error)
}

// KeyEligibilityReconciler 在资金或倍率变化后统一重算密钥资格。
type KeyEligibilityReconciler struct {
	gate        SecurityDepositAccessGate
	repo        SecurityDepositKeyEligibilityRepository
	invalidator APIKeyAuthCacheInvalidator
	now         func() time.Time
}

func NewKeyEligibilityReconciler(gate SecurityDepositAccessGate, repo SecurityDepositKeyEligibilityRepository, invalidator APIKeyAuthCacheInvalidator) *KeyEligibilityReconciler {
	return &KeyEligibilityReconciler{gate: gate, repo: repo, invalidator: invalidator, now: time.Now}
}

// DisableInsufficientKeys 只禁用调用瞬间仍为 active 且低于最新个人门槛的密钥。
func (r *KeyEligibilityReconciler) DisableInsufficientKeys(ctx context.Context, userID int64, eventType string, eventID int64) ([]SecurityDepositKeyReference, error) {
	if r == nil || r.gate == nil || r.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_RECONCILER_UNAVAILABLE", "security deposit key eligibility reconciler is unavailable")
	}
	if userID <= 0 || eventID <= 0 || strings.TrimSpace(eventType) == "" {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_FINANCIAL_EVENT", "user_id, event_type and event_id are required")
	}
	keys, err := r.repo.ListActiveSecurityDepositKeys(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list active security deposit keys: %w", err)
	}
	if len(keys) == 0 {
		return []SecurityDepositKeyReference{}, nil
	}
	return r.disableInsufficientKeys(ctx, keys, eventType, eventID)
}

// DisableInsufficientKeysByGroup 只检查指定分组当前仍为 active 的密钥，不自动启用任何密钥。
func (r *KeyEligibilityReconciler) DisableInsufficientKeysByGroup(ctx context.Context, groupID int64, eventType string, eventID int64) ([]SecurityDepositKeyReference, error) {
	if r == nil || r.gate == nil || r.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_RECONCILER_UNAVAILABLE", "security deposit key eligibility reconciler is unavailable")
	}
	if groupID <= 0 || eventID <= 0 || strings.TrimSpace(eventType) == "" {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_GROUP_EVENT", "group_id, event_type and event_id are required")
	}
	keys, err := r.repo.ListActiveSecurityDepositKeysByGroup(ctx, groupID)
	if err != nil {
		return nil, fmt.Errorf("list active security deposit keys by group: %w", err)
	}
	if len(keys) == 0 {
		return []SecurityDepositKeyReference{}, nil
	}
	return r.disableInsufficientKeys(ctx, keys, eventType, eventID)
}

func (r *KeyEligibilityReconciler) disableInsufficientKeys(ctx context.Context, keys []SecurityDepositKeyReference, eventType string, eventID int64) ([]SecurityDepositKeyReference, error) {
	type eligibilityScope struct {
		userID  int64
		groupID int64
	}
	insufficientScopes := make(map[eligibilityScope]bool)
	checkedScopes := make(map[eligibilityScope]struct{})
	for _, key := range keys {
		scope := eligibilityScope{userID: key.UserID, groupID: key.GroupID}
		if _, checked := checkedScopes[scope]; checked {
			continue
		}
		checkedScopes[scope] = struct{}{}
		grant, accessErr := r.gate.CheckAccess(ctx, key.UserID, key.GroupID)
		if accessErr != nil {
			if infraerrors.Reason(accessErr) == "SECURITY_DEPOSIT_REQUIRED" {
				insufficientScopes[scope] = true
				continue
			}
			return nil, fmt.Errorf("check security deposit access for user %d group %d: %w", key.UserID, key.GroupID, accessErr)
		}
		if grant == nil {
			return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit access status is unavailable")
		}
	}

	keyIDs := make([]int64, 0, len(keys))
	for _, key := range keys {
		if insufficientScopes[eligibilityScope{userID: key.UserID, groupID: key.GroupID}] {
			keyIDs = append(keyIDs, key.ID)
		}
	}
	if len(keyIDs) == 0 {
		return []SecurityDepositKeyReference{}, nil
	}
	disabled, err := r.repo.DisableActiveSecurityDepositKeys(ctx, keyIDs, strings.TrimSpace(eventType), eventID, r.now().UTC())
	if err != nil {
		return nil, fmt.Errorf("disable insufficient security deposit keys: %w", err)
	}
	for _, key := range disabled {
		if r.invalidator != nil && strings.TrimSpace(key.Key) != "" {
			r.invalidator.InvalidateAuthCacheByKey(ctx, key.Key)
		}
	}
	return disabled, nil
}
