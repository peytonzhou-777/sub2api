package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SecurityDepositCyberPenaltyInput 只接受可信上游标记和认证中间件生成的准入快照。
type SecurityDepositCyberPenaltyInput struct {
	EventKey           string
	RequestID          string
	UpstreamResponseID string
	TurnIndex          *int64
	PolicyCode         string
	Grant              SecurityDepositAccessGrant
	APIKeyID           int64
	APIKeyName         string
	GroupName          string
}

// SecurityDepositCyberPenaltyResult 返回幂等处罚结果和事务内受影响密钥。
type SecurityDepositCyberPenaltyResult struct {
	ViolationID          int64
	State                string
	RiskMultiplierBefore int64
	RiskMultiplierAfter  int64
	ForfeitedCents       int64
	ShortfallCents       int64
	SecurityLocked       bool
	DisabledKeyIDs       []int64
	AlreadyProcessed     bool
}

// SecurityDepositPenaltyRepository 以单事务完成处罚、倍率和密钥状态变更。
type SecurityDepositPenaltyRepository interface {
	ApplyCyberPolicyPenalty(ctx context.Context, input SecurityDepositCyberPenaltyInput, maxRiskMultiplier int64, shadow bool) (*SecurityDepositCyberPenaltyResult, error)
}

// ApplyCyberPolicyPenalty 同步处理一次可信官方网安处罚；同一 event_key 重放不重复扣款或加倍率。
func (s *SecurityDepositService) ApplyCyberPolicyPenalty(ctx context.Context, input SecurityDepositCyberPenaltyInput) (*SecurityDepositCyberPenaltyResult, error) {
	if s == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_PENALTY_UNAVAILABLE", "security deposit penalty service is unavailable")
	}
	if !strings.EqualFold(strings.TrimSpace(input.PolicyCode), "cyber_policy") {
		return nil, nil
	}
	input.PolicyCode = "cyber_policy"
	if input.Grant.UserID <= 0 || input.Grant.GroupID <= 0 || input.APIKeyID <= 0 || input.Grant.BaseRequiredCents <= 0 {
		return nil, nil
	}
	if input.Grant.RequiredCents < 0 || input.Grant.RiskMultiplier < 1 {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_PENALTY", "security deposit access grant is invalid")
	}
	input.EventKey = strings.TrimSpace(input.EventKey)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.EventKey == "" || input.RequestID == "" {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_PENALTY", "security deposit penalty event identity is required")
	}

	policy, err := s.securityDepositPolicyStrict(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit policy is temporarily unavailable").WithCause(err)
	}
	mode := normalizeSecurityDepositPenaltyMode(policy.PenaltyMode)
	if mode == SecurityDepositPenaltyModeOff {
		return nil, nil
	}
	shadow := mode == SecurityDepositPenaltyModeShadow
	if !shadow && (!policy.EnforcementEnabled || !input.Grant.Enforced) {
		return nil, nil
	}
	repo, ok := s.repo.(SecurityDepositPenaltyRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_PENALTY_UNAVAILABLE", "security deposit penalty repository is unavailable")
	}
	result, err := repo.ApplyCyberPolicyPenalty(ctx, input, policy.MaxRiskMultiplier, shadow)
	if err != nil {
		return nil, fmt.Errorf("apply security deposit cyber policy penalty: %w", err)
	}
	if result != nil && !shadow {
		result.DisabledKeyIDs, err = s.reconcileKeysAfterBalanceChange(ctx, input.Grant.UserID, "cyber_policy_penalty", result.ViolationID, result.DisabledKeyIDs)
		if err != nil {
			return nil, fmt.Errorf("reconcile security deposit keys after cyber policy penalty: %w", err)
		}
		if result.ForfeitedCents > 0 {
			if err = s.reconcileBonusAfterBalanceDecrease(ctx, input.Grant.UserID, "cyber_policy_penalty", result.ViolationID); err != nil {
				return nil, fmt.Errorf("reconcile security deposit bonus after cyber policy penalty: %w", err)
			}
		}
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, input.Grant.UserID)
		}
	}
	return result, nil
}

// BuildSecurityDepositCyberPenaltyEventKey 生成不包含 API Key 明文的稳定处罚幂等键。
func BuildSecurityDepositCyberPenaltyEventKey(requestID string, apiKeyID int64, upstreamResponseID string, turnIndex *int64) string {
	turn := int64(-1)
	if turnIndex != nil {
		turn = *turnIndex
	}
	requestID = strings.TrimSpace(requestID)
	upstreamResponseID = strings.TrimSpace(upstreamResponseID)
	// 有上游响应 ID 时以官方事实为主键，避免同一事件因重试请求 ID 不同而重复处罚。
	// 没有响应 ID 时才退回服务端请求 ID；该值不能使用客户端可控的 request ID。
	identity := "request|" + requestID
	if upstreamResponseID != "" {
		identity = "response|" + upstreamResponseID
	}
	canonical := fmt.Sprintf("v1|%s|%d|%d", identity, apiKeyID, turn)
	sum := sha256.Sum256([]byte(canonical))
	return "security_deposit:cyber:" + hex.EncodeToString(sum[:])
}
