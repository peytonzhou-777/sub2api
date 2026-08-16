package service

import (
	"context"
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SecurityDepositAdminActionAdd          = "admin_add"
	SecurityDepositAdminActionCompensation = "compensation"
	SecurityDepositAdminActionDeduct       = "admin_deduct"
	SecurityDepositAdminActionRevoke       = "admin_revoke"
	SecurityDepositAdminActionKeyUnlock    = "key_unlock"
)

// AdminSecurityDepositCreditInput 表示管理员永久冻结发放或补偿。
type AdminSecurityDepositCreditInput struct {
	UserID         int64
	OperatorID     int64
	AmountCents    int64
	ActionType     string
	Reason         *string
	IdempotencyKey string
}

// AdminSecurityDepositDeductInput 表示仅从管理员发放桶执行的扣除。
type AdminSecurityDepositDeductInput struct {
	UserID         int64
	OperatorID     int64
	AmountCents    int64
	Reason         *string
	IdempotencyKey string
}

// AdminSecurityDepositRevokeInput 表示撤销一笔误发批次的全部剩余额。
type AdminSecurityDepositRevokeInput struct {
	UserID         int64
	OperatorID     int64
	LotID          int64
	Reason         *string
	IdempotencyKey string
}

// AdminSecurityDepositUnlockInput 表示管理员复核后解除密钥安全锁。
type AdminSecurityDepositUnlockInput struct {
	UserID         int64
	OperatorID     int64
	APIKeyID       int64
	Reason         *string
	IdempotencyKey string
}

// AdminSecurityDepositMutationResult 返回管理员资金操作后的权威状态。
type AdminSecurityDepositMutationResult struct {
	ActionID                    int64   `json:"action_id"`
	ActionType                  string  `json:"action_type"`
	UserID                      int64   `json:"user_id"`
	LotID                       *int64  `json:"lot_id,omitempty"`
	AmountCents                 int64   `json:"amount_cents"`
	AdminGrantBalanceAfterCents int64   `json:"admin_grant_balance_after_cents"`
	DisabledKeyIDs              []int64 `json:"disabled_key_ids"`
	AlreadyProcessed            bool    `json:"already_processed"`
}

// AdminSecurityDepositUnlockResult 返回安全锁解除后的密钥状态。
type AdminSecurityDepositUnlockResult struct {
	ActionID         int64  `json:"action_id"`
	UserID           int64  `json:"user_id"`
	APIKeyID         int64  `json:"api_key_id"`
	Status           string `json:"status"`
	AlreadyProcessed bool   `json:"already_processed"`
	APIKey           string `json:"-"`
}

// SecurityDepositAdminRepository 提供第六阶段管理员写操作的事务接缝。
type SecurityDepositAdminRepository interface {
	CreditAdminGrant(ctx context.Context, input AdminSecurityDepositCreditInput) (*AdminSecurityDepositMutationResult, error)
	DeductAdminGrant(ctx context.Context, input AdminSecurityDepositDeductInput, enforcementEnabled bool) (*AdminSecurityDepositMutationResult, error)
	RevokeAdminGrantLot(ctx context.Context, input AdminSecurityDepositRevokeInput, enforcementEnabled bool) (*AdminSecurityDepositMutationResult, error)
	UnlockSecurityLockedAPIKey(ctx context.Context, input AdminSecurityDepositUnlockInput) (*AdminSecurityDepositUnlockResult, error)
}

// AdminCreditAdminGrant 发放永久冻结保证金；补偿与普通发放使用不同流水类型。
func (s *SecurityDepositService) AdminCreditAdminGrant(ctx context.Context, input AdminSecurityDepositCreditInput) (*AdminSecurityDepositMutationResult, error) {
	if input.UserID <= 0 || input.OperatorID <= 0 || input.AmountCents <= 0 {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ADMIN_CREDIT", "user_id, operator_id and positive amount_cents are required")
	}
	input.ActionType = strings.TrimSpace(input.ActionType)
	if input.ActionType != SecurityDepositAdminActionAdd && input.ActionType != SecurityDepositAdminActionCompensation {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ADMIN_CREDIT_TYPE", "action_type must be admin_add or compensation")
	}
	if err := normalizeSecurityDepositAdminInput(&input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	repo, err := s.adminRepository()
	if err != nil {
		return nil, err
	}
	result, err := repo.CreditAdminGrant(ctx, input)
	if err != nil {
		return nil, err
	}
	result.DisabledKeyIDs, err = s.reconcileKeysAfterBalanceChange(ctx, input.UserID, result.ActionType, result.ActionID, result.DisabledKeyIDs)
	if err != nil {
		return nil, fmt.Errorf("reconcile security deposit keys after admin credit: %w", err)
	}
	s.invalidateSecurityDepositUser(ctx, input.UserID)
	return result, nil
}

// AdminDeductAdminGrant 仅扣除永久冻结发放桶，余额不足时整体失败。
func (s *SecurityDepositService) AdminDeductAdminGrant(ctx context.Context, input AdminSecurityDepositDeductInput) (*AdminSecurityDepositMutationResult, error) {
	if input.UserID <= 0 || input.OperatorID <= 0 || input.AmountCents <= 0 {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ADMIN_DEDUCTION", "user_id, operator_id and positive amount_cents are required")
	}
	if err := normalizeSecurityDepositAdminInput(&input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	policy, err := s.securityDepositPolicyStrict(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit policy is temporarily unavailable").WithCause(err)
	}
	repo, err := s.adminRepository()
	if err != nil {
		return nil, err
	}
	var result *AdminSecurityDepositMutationResult
	err = s.withSecurityDepositFinancialFence(ctx, input.UserID, SecurityDepositAdminActionKey(SecurityDepositAdminActionDeduct, input.UserID, input.IdempotencyKey), func() error {
		var mutationErr error
		result, mutationErr = repo.DeductAdminGrant(ctx, input, policy.EnforcementEnabled)
		return mutationErr
	})
	if err != nil {
		return nil, err
	}
	result.DisabledKeyIDs, err = s.reconcileKeysAfterBalanceChange(ctx, input.UserID, result.ActionType, result.ActionID, result.DisabledKeyIDs)
	if err != nil {
		return nil, fmt.Errorf("reconcile security deposit keys after admin deduction: %w", err)
	}
	s.invalidateSecurityDepositUser(ctx, input.UserID)
	return result, nil
}

// AdminRevokeAdminGrantLot 仅撤销管理员发放批次尚未被处罚或扣除的剩余额。
func (s *SecurityDepositService) AdminRevokeAdminGrantLot(ctx context.Context, input AdminSecurityDepositRevokeInput) (*AdminSecurityDepositMutationResult, error) {
	if input.UserID <= 0 || input.OperatorID <= 0 || input.LotID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ADMIN_REVOKE", "user_id, operator_id and lot_id are required")
	}
	if err := normalizeSecurityDepositAdminInput(&input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	policy, err := s.securityDepositPolicyStrict(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit policy is temporarily unavailable").WithCause(err)
	}
	repo, err := s.adminRepository()
	if err != nil {
		return nil, err
	}
	var result *AdminSecurityDepositMutationResult
	err = s.withSecurityDepositFinancialFence(ctx, input.UserID, SecurityDepositAdminActionKey(SecurityDepositAdminActionRevoke, input.UserID, input.IdempotencyKey), func() error {
		var mutationErr error
		result, mutationErr = repo.RevokeAdminGrantLot(ctx, input, policy.EnforcementEnabled)
		return mutationErr
	})
	if err != nil {
		return nil, err
	}
	result.DisabledKeyIDs, err = s.reconcileKeysAfterBalanceChange(ctx, input.UserID, result.ActionType, result.ActionID, result.DisabledKeyIDs)
	if err != nil {
		return nil, fmt.Errorf("reconcile security deposit keys after admin revoke: %w", err)
	}
	s.invalidateSecurityDepositUser(ctx, input.UserID)
	return result, nil
}

// AdminUnlockSecurityLockedAPIKey 解除安全锁后保持 disabled，不自动恢复调用资格。
func (s *SecurityDepositService) AdminUnlockSecurityLockedAPIKey(ctx context.Context, input AdminSecurityDepositUnlockInput) (*AdminSecurityDepositUnlockResult, error) {
	if input.UserID <= 0 || input.OperatorID <= 0 || input.APIKeyID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_KEY_UNLOCK", "user_id, operator_id and api_key_id are required")
	}
	if err := normalizeSecurityDepositAdminInput(&input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	repo, err := s.adminRepository()
	if err != nil {
		return nil, err
	}
	result, err := repo.UnlockSecurityLockedAPIKey(ctx, input)
	if err != nil {
		return nil, err
	}
	if s.authCacheInvalidator != nil && strings.TrimSpace(result.APIKey) != "" {
		s.authCacheInvalidator.InvalidateAuthCacheByKey(ctx, result.APIKey)
	}
	s.invalidateSecurityDepositUser(ctx, input.UserID)
	return result, nil
}

func (s *SecurityDepositService) adminRepository() (SecurityDepositAdminRepository, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_ADMIN_UNAVAILABLE", "security deposit administration is unavailable")
	}
	repo, ok := s.repo.(SecurityDepositAdminRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_ADMIN_UNAVAILABLE", "security deposit administration is unavailable")
	}
	return repo, nil
}

func normalizeSecurityDepositAdminInput(idempotencyKey *string, reason **string) error {
	key, err := NormalizeIdempotencyKey(*idempotencyKey)
	if err != nil {
		return err
	}
	if key == "" {
		return ErrIdempotencyKeyRequired
	}
	*idempotencyKey = key
	if reason == nil || *reason == nil {
		return nil
	}
	trimmed := strings.TrimSpace(**reason)
	if trimmed == "" {
		*reason = nil
		return nil
	}
	if len([]rune(trimmed)) > 1000 {
		return infraerrors.BadRequest("SECURITY_DEPOSIT_ADMIN_REASON_TOO_LONG", "reason must not exceed 1000 characters")
	}
	*reason = &trimmed
	return nil
}

func (s *SecurityDepositService) invalidateSecurityDepositUser(ctx context.Context, userID int64) {
	if s != nil && s.authCacheInvalidator != nil && userID > 0 {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
}

// SecurityDepositAdminActionKey 将客户端幂等键收敛为不泄露原文的稳定账本键。
func SecurityDepositAdminActionKey(actionType string, userID int64, idempotencyKey string) string {
	return fmt.Sprintf("security_deposit:%s:user:%d:%s", actionType, userID, HashIdempotencyKey(idempotencyKey))
}
