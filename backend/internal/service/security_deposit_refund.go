package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SecurityDepositRefundModeAutomatic = "automatic_original_channel"
	SecurityDepositRefundModeManual    = "manual_external"

	SecurityDepositRefundStateReserved       = "reserved"
	SecurityDepositRefundStateSubmitting     = "submitting"
	SecurityDepositRefundStatePending        = "pending"
	SecurityDepositRefundStateManualReview   = "manual_review"
	SecurityDepositRefundStateSucceeded      = "succeeded"
	SecurityDepositRefundStateFailedReleased = "failed_released"
	SecurityDepositRefundStateCanceled       = "canceled"
)

// SecurityDepositRefundTarget 是退款预检所需的实付批次快照。
type SecurityDepositRefundTarget struct {
	UserID         int64
	LotID          int64
	PaymentOrderID int64
	PrincipalCents int64
	Currency       string
	LockedUntil    *time.Time
}

// SecurityDepositRefundImpact 是退款预览中可能被自动禁用的密钥摘要。
type SecurityDepositRefundImpact struct {
	APIKeyID          int64  `json:"api_key_id"`
	APIKeyName        string `json:"api_key_name"`
	GroupID           int64  `json:"group_id"`
	GroupName         string `json:"group_name"`
	RequiredCents     int64  `json:"required_cents"`
	BalanceAfterCents int64  `json:"balance_after_cents"`
}

// SecurityDepositRefundPreview 返回确认退款前的权威金额和密钥影响范围。
type SecurityDepositRefundPreview struct {
	LotID           int64                         `json:"lot_id"`
	PrincipalCents  int64                         `json:"principal_cents"`
	GatewayAmount   string                        `json:"gateway_amount"`
	GatewayCurrency string                        `json:"gateway_currency"`
	AffectedAPIKeys []SecurityDepositRefundImpact `json:"affected_api_keys"`
}

// SecurityDepositRefundRecord 返回一笔保证金退款的权威状态。
type SecurityDepositRefundRecord struct {
	ID                       int64          `json:"id"`
	RefundID                 string         `json:"refund_id"`
	UserID                   int64          `json:"user_id"`
	LotID                    int64          `json:"lot_id"`
	PaymentOrderID           int64          `json:"payment_order_id"`
	PrincipalCents           int64          `json:"principal_cents"`
	GatewayAmount            string         `json:"gateway_amount"`
	GatewayCurrency          string         `json:"gateway_currency"`
	Mode                     string         `json:"mode"`
	State                    string         `json:"state"`
	RequestedBy              *int64         `json:"requested_by,omitempty"`
	Reason                   *string        `json:"reason,omitempty"`
	ProviderRequestID        *string        `json:"provider_request_id,omitempty"`
	ProviderResponseSnapshot map[string]any `json:"provider_response_snapshot,omitempty"`
	ExternalRefundID         *string        `json:"external_refund_id,omitempty"`
	ExternalRefundedAt       *time.Time     `json:"external_refunded_at,omitempty"`
	ExternalEvidence         map[string]any `json:"external_evidence,omitempty"`
	DisabledKeyIDs           []int64        `json:"disabled_key_ids"`
	AlreadyProcessed         bool           `json:"already_processed"`
	CreatedAt                time.Time      `json:"created_at"`
	SubmittedAt              *time.Time     `json:"submitted_at,omitempty"`
	CompletedAt              *time.Time     `json:"completed_at,omitempty"`
}

// SecurityDepositRefundReserveInput 创建自动或人工退款预留。
type SecurityDepositRefundReserveInput struct {
	RefundID          string
	UserID            int64
	LotID             int64
	PaymentOrderID    int64
	PrincipalCents    int64
	GatewayAmount     string
	GatewayCurrency   string
	Mode              string
	OperatorID        int64
	Reason            *string
	QuoteHash         string
	IdempotencyKey    string
	ProviderRequestID *string
	RequireUnlocked   bool
}

type UserSecurityDepositAutomaticRefundInput struct {
	UserID         int64
	LotID          int64
	IdempotencyKey string
}

type AdminSecurityDepositAutomaticRefundInput struct {
	UserID         int64
	LotID          int64
	OperatorID     int64
	Reason         *string
	IdempotencyKey string
}

type AdminSecurityDepositManualReserveInput struct {
	UserID         int64
	LotID          int64
	OperatorID     int64
	Reason         *string
	IdempotencyKey string
}

type AdminSecurityDepositManualCompleteInput struct {
	UserID              int64
	RefundID            string
	OperatorID          int64
	ExternalRefundID    string
	ExternalAmountCents int64
	ExternalRefundedAt  time.Time
	ExternalEvidence    map[string]any
	Reason              *string
	IdempotencyKey      string
}

type AdminSecurityDepositRefundCancelInput struct {
	UserID         int64
	RefundID       string
	OperatorID     int64
	Reason         *string
	IdempotencyKey string
}

type AdminSecurityDepositAutomaticReviewFailureInput struct {
	UserID         int64
	RefundID       string
	OperatorID     int64
	Evidence       map[string]any
	Reason         *string
	IdempotencyKey string
}

// SecurityDepositRefundRepository 维护预留、核销与释放的事务边界。
type SecurityDepositRefundRepository interface {
	GetSecurityDepositRefund(ctx context.Context, refundID string) (*SecurityDepositRefundRecord, error)
	GetSecurityDepositRefundTarget(ctx context.Context, userID, lotID int64) (*SecurityDepositRefundTarget, error)
	PreviewSecurityDepositRefundImpact(ctx context.Context, userID, principalCents int64, enforcementEnabled bool) ([]SecurityDepositRefundImpact, error)
	ReserveSecurityDepositRefund(ctx context.Context, input SecurityDepositRefundReserveInput, enforcementEnabled bool) (*SecurityDepositRefundRecord, error)
	ClaimAutomaticSecurityDepositRefund(ctx context.Context, refundID string) (*SecurityDepositRefundRecord, bool, error)
	ClaimAutomaticSecurityDepositRefundQuery(ctx context.Context, refundID string, userID int64) (*SecurityDepositRefundRecord, string, bool, error)
	FinalizeAutomaticSecurityDepositRefund(ctx context.Context, refundID, state, providerRefundID string, snapshot map[string]any) (*SecurityDepositRefundRecord, error)
	CompleteManualSecurityDepositRefund(ctx context.Context, input AdminSecurityDepositManualCompleteInput) (*SecurityDepositRefundRecord, error)
	CancelSecurityDepositRefund(ctx context.Context, input AdminSecurityDepositRefundCancelInput) (*SecurityDepositRefundRecord, error)
	FailAutomaticSecurityDepositRefundReview(ctx context.Context, input AdminSecurityDepositAutomaticReviewFailureInput) (*SecurityDepositRefundRecord, error)
}

// SecurityDepositGatewayRefundPlan 是支付模块与保证金状态机之间的最小协议。
type SecurityDepositGatewayRefundPlan struct {
	PaymentOrderID  int64
	UserID          int64
	PrincipalCents  int64
	GatewayAmount   string
	GatewayCurrency string
	RequestID       string
	Reason          string
	internal        any
}

type securityDepositRefundGateway interface {
	PrepareSecurityDepositGatewayRefund(ctx context.Context, paymentOrderID, userID, principalCents int64, requestID, reason string) (*SecurityDepositGatewayRefundPlan, error)
	PrepareUserSecurityDepositGatewayRefund(ctx context.Context, paymentOrderID, userID, principalCents int64, requestID, reason string) (*SecurityDepositGatewayRefundPlan, error)
	ExecuteSecurityDepositGatewayRefund(ctx context.Context, plan *SecurityDepositGatewayRefundPlan) (*payment.RefundResponse, error)
	QuerySecurityDepositGatewayRefund(ctx context.Context, paymentOrderID int64, gatewayAmount, providerRefundID string) (*payment.RefundResponse, error)
}

type securityDepositRefundCapability struct {
	RefundEnabled   bool
	AllowUserRefund bool
}

type securityDepositRefundCapabilityReader interface {
	GetSecurityDepositRefundCapability(ctx context.Context, paymentOrderID int64) (securityDepositRefundCapability, error)
}

// PreviewUserRefundPaidLot 校验全部自助退款开关并返回退款影响范围，不修改资金状态。
func (s *SecurityDepositService) PreviewUserRefundPaidLot(ctx context.Context, userID, lotID int64) (*SecurityDepositRefundPreview, error) {
	if userID <= 0 || lotID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND", "user_id and lot_id are required")
	}
	policy, err := s.securityDepositPolicyStrict(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit policy is temporarily unavailable").WithCause(err)
	}
	if !policy.SelfRefundEnabled {
		return nil, infraerrors.Forbidden("SECURITY_DEPOSIT_SELF_REFUND_DISABLED", "self-service security deposit refunds are disabled")
	}
	repo, err := s.refundRepository()
	if err != nil {
		return nil, err
	}
	target, err := repo.GetSecurityDepositRefundTarget(ctx, userID, lotID)
	if err != nil {
		return nil, err
	}
	if target.LockedUntil != nil && s.now().UTC().Before(target.LockedUntil.UTC()) {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_FROZEN", "security deposit lot is still within its refund freeze period")
	}
	gateway, ok := s.paymentCreator.(securityDepositRefundGateway)
	if !ok || gateway == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_GATEWAY_UNAVAILABLE", "security deposit refund gateway is unavailable")
	}
	plan, err := gateway.PrepareUserSecurityDepositGatewayRefund(ctx, target.PaymentOrderID, userID, target.PrincipalCents,
		SecurityDepositAdminActionKey("self_refund_preview", userID, fmt.Sprintf("lot-%d", lotID)), securityDepositRefundReason(nil, lotID))
	if err != nil {
		return nil, err
	}
	impacts, err := repo.PreviewSecurityDepositRefundImpact(ctx, userID, target.PrincipalCents, policy.EnforcementEnabled)
	if err != nil {
		return nil, err
	}
	return &SecurityDepositRefundPreview{
		LotID: lotID, PrincipalCents: target.PrincipalCents, GatewayAmount: plan.GatewayAmount,
		GatewayCurrency: plan.GatewayCurrency, AffectedAPIKeys: impacts,
	}, nil
}

// UserAutomaticRefundPaidLot 对已解冻实付批次执行一次用户自助原路退款。
func (s *SecurityDepositService) UserAutomaticRefundPaidLot(ctx context.Context, input UserSecurityDepositAutomaticRefundInput) (*SecurityDepositRefundRecord, error) {
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.UserID <= 0 || input.LotID <= 0 || input.IdempotencyKey == "" {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND", "user_id, lot_id and idempotency key are required")
	}
	if len([]rune(input.IdempotencyKey)) > 191 {
		return nil, infraerrors.BadRequest("IDEMPOTENCY_KEY_TOO_LONG", "idempotency key must not exceed 191 characters")
	}
	repo, err := s.refundRepository()
	if err != nil {
		return nil, err
	}
	refundID := securityDepositRefundID("user_"+SecurityDepositRefundModeAutomatic, input.UserID, input.IdempotencyKey)
	if existing, err := repo.GetSecurityDepositRefund(ctx, refundID); err != nil {
		return nil, err
	} else if existing != nil {
		existing.AlreadyProcessed = true
		return s.finishSecurityDepositRefundChange(ctx, existing, "user_refund")
	}
	policy, err := s.securityDepositPolicyStrict(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit policy is temporarily unavailable").WithCause(err)
	}
	if !policy.SelfRefundEnabled {
		return nil, infraerrors.Forbidden("SECURITY_DEPOSIT_SELF_REFUND_DISABLED", "self-service security deposit refunds are disabled")
	}
	target, err := repo.GetSecurityDepositRefundTarget(ctx, input.UserID, input.LotID)
	if err != nil {
		return nil, err
	}
	if target.LockedUntil != nil && s.now().UTC().Before(target.LockedUntil.UTC()) {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_FROZEN", "security deposit lot is still within its refund freeze period")
	}
	gateway, ok := s.paymentCreator.(securityDepositRefundGateway)
	if !ok || gateway == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_GATEWAY_UNAVAILABLE", "security deposit refund gateway is unavailable")
	}
	requestID := SecurityDepositAdminActionKey("self_refund", input.UserID, input.IdempotencyKey)
	plan, err := gateway.PrepareUserSecurityDepositGatewayRefund(ctx, target.PaymentOrderID, input.UserID, target.PrincipalCents, requestID, securityDepositRefundReason(nil, input.LotID))
	if err != nil {
		return nil, err
	}
	var result *SecurityDepositRefundRecord
	err = s.withSecurityDepositFinancialFence(ctx, input.UserID, requestID, func() error {
		latestPolicy, policyErr := s.securityDepositPolicyStrict(ctx)
		if policyErr != nil {
			return infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit policy is temporarily unavailable").WithCause(policyErr)
		}
		if !latestPolicy.SelfRefundEnabled {
			return infraerrors.Forbidden("SECURITY_DEPOSIT_SELF_REFUND_DISABLED", "self-service security deposit refunds are disabled")
		}
		plan, err = gateway.PrepareUserSecurityDepositGatewayRefund(ctx, target.PaymentOrderID, input.UserID, target.PrincipalCents, requestID, securityDepositRefundReason(nil, input.LotID))
		if err != nil {
			return err
		}
		providerRequestID := plan.RequestID
		result, err = repo.ReserveSecurityDepositRefund(ctx, SecurityDepositRefundReserveInput{
			RefundID: refundID, UserID: input.UserID, LotID: input.LotID,
			PaymentOrderID: target.PaymentOrderID, PrincipalCents: target.PrincipalCents,
			GatewayAmount: plan.GatewayAmount, GatewayCurrency: plan.GatewayCurrency,
			Mode: SecurityDepositRefundModeAutomatic, OperatorID: input.UserID,
			QuoteHash: securityDepositRefundQuoteHash(target, plan.GatewayAmount), IdempotencyKey: requestID,
			ProviderRequestID: &providerRequestID, RequireUnlocked: true,
		}, latestPolicy.EnforcementEnabled)
		if err != nil || result.AlreadyProcessed {
			return err
		}
		var claimed bool
		result, claimed, err = repo.ClaimAutomaticSecurityDepositRefund(ctx, refundID)
		if err != nil || !claimed {
			return err
		}
		response, gatewayErr := gateway.ExecuteSecurityDepositGatewayRefund(ctx, plan)
		return s.finishAdminAutomaticSecurityDepositRefund(ctx, repo, refundID, response, gatewayErr, &result)
	})
	if err != nil {
		return nil, err
	}
	return s.finishSecurityDepositRefundChange(ctx, result, "user_refund")
}

// QueryAutomaticSecurityDepositRefund 查询 pending/unknown 网关结果并恢复本地状态机。
func (s *SecurityDepositService) QueryAutomaticSecurityDepositRefund(ctx context.Context, userID int64, refundID string) (*SecurityDepositRefundRecord, error) {
	refundID = strings.TrimSpace(refundID)
	if userID <= 0 || refundID == "" {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND_QUERY", "user_id and refund_id are required")
	}
	repo, err := s.refundRepository()
	if err != nil {
		return nil, err
	}
	record, previousState, claimed, err := repo.ClaimAutomaticSecurityDepositRefundQuery(ctx, refundID, userID)
	if err != nil {
		return record, err
	}
	if !claimed {
		return s.finishSecurityDepositRefundChange(ctx, record, "refund_query")
	}
	gateway, ok := s.paymentCreator.(securityDepositRefundGateway)
	if !ok || gateway == nil {
		_, _ = repo.FinalizeAutomaticSecurityDepositRefund(ctx, refundID, SecurityDepositRefundStateManualReview, "", map[string]any{"error": "refund query gateway unavailable"})
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_GATEWAY_UNAVAILABLE", "security deposit refund gateway is unavailable")
	}
	providerRefundID := securityDepositProviderRefundID(record)
	response, queryErr := gateway.QuerySecurityDepositGatewayRefund(ctx, record.PaymentOrderID, record.GatewayAmount, providerRefundID)
	if queryErr != nil {
		restoreState := previousState
		if restoreState != SecurityDepositRefundStatePending {
			restoreState = SecurityDepositRefundStateManualReview
		}
		updated, finalizeErr := repo.FinalizeAutomaticSecurityDepositRefund(ctx, refundID, restoreState, providerRefundID, map[string]any{"error": queryErr.Error(), "query": true})
		if finalizeErr != nil {
			return nil, finalizeErr
		}
		return updated, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_RESULT_UNKNOWN", "refund result remains unknown and requires another query or manual review").WithCause(queryErr)
	}
	var result *SecurityDepositRefundRecord
	if err := s.finishAdminAutomaticSecurityDepositRefund(ctx, repo, refundID, response, nil, &result); err != nil {
		return nil, err
	}
	return s.finishSecurityDepositRefundChange(ctx, result, "refund_query")
}

// AdminAutomaticRefundPaidLot 对用户实付批次执行一次保证金专用原路退款。
func (s *SecurityDepositService) AdminAutomaticRefundPaidLot(ctx context.Context, input AdminSecurityDepositAutomaticRefundInput) (*SecurityDepositRefundRecord, error) {
	if err := validateSecurityDepositRefundAdminBase(input.UserID, input.LotID, input.OperatorID, &input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	repo, err := s.refundRepository()
	if err != nil {
		return nil, err
	}
	refundID := securityDepositRefundID(SecurityDepositRefundModeAutomatic, input.UserID, input.IdempotencyKey)
	if existing, err := repo.GetSecurityDepositRefund(ctx, refundID); err != nil {
		return nil, err
	} else if existing != nil {
		existing.AlreadyProcessed = true
		return s.finishSecurityDepositRefundChange(ctx, existing, "admin_automatic_refund")
	}
	target, err := repo.GetSecurityDepositRefundTarget(ctx, input.UserID, input.LotID)
	if err != nil {
		return nil, err
	}
	gateway, ok := s.paymentCreator.(securityDepositRefundGateway)
	if !ok || gateway == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_GATEWAY_UNAVAILABLE", "security deposit refund gateway is unavailable")
	}
	reason := securityDepositRefundReason(input.Reason, input.LotID)
	requestID := SecurityDepositAdminActionKey("refund", input.UserID, input.IdempotencyKey)
	plan, err := gateway.PrepareSecurityDepositGatewayRefund(ctx, target.PaymentOrderID, input.UserID, target.PrincipalCents, requestID, reason)
	if err != nil {
		return nil, err
	}
	policy, err := s.securityDepositPolicyStrict(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit policy is temporarily unavailable").WithCause(err)
	}

	var result *SecurityDepositRefundRecord
	err = s.withSecurityDepositFinancialFence(ctx, input.UserID, requestID, func() error {
		providerRequestID := plan.RequestID
		result, err = repo.ReserveSecurityDepositRefund(ctx, SecurityDepositRefundReserveInput{
			RefundID: refundID, UserID: input.UserID, LotID: input.LotID,
			PaymentOrderID: target.PaymentOrderID, PrincipalCents: target.PrincipalCents,
			GatewayAmount: plan.GatewayAmount, GatewayCurrency: plan.GatewayCurrency,
			Mode: SecurityDepositRefundModeAutomatic, OperatorID: input.OperatorID,
			Reason: input.Reason, QuoteHash: securityDepositRefundQuoteHash(target, plan.GatewayAmount),
			IdempotencyKey: requestID, ProviderRequestID: &providerRequestID,
		}, policy.EnforcementEnabled)
		if err != nil || result.AlreadyProcessed {
			return err
		}
		var claimed bool
		result, claimed, err = repo.ClaimAutomaticSecurityDepositRefund(ctx, refundID)
		if err != nil || !claimed {
			return err
		}
		response, gatewayErr := gateway.ExecuteSecurityDepositGatewayRefund(ctx, plan)
		return s.finishAdminAutomaticSecurityDepositRefund(ctx, repo, refundID, response, gatewayErr, &result)
	})
	if err != nil {
		return nil, err
	}
	return s.finishSecurityDepositRefundChange(ctx, result, "admin_automatic_refund")
}

// AdminReserveManualRefundPaidLot 在外部退款前先排空请求并预留实付保证金。
func (s *SecurityDepositService) AdminReserveManualRefundPaidLot(ctx context.Context, input AdminSecurityDepositManualReserveInput) (*SecurityDepositRefundRecord, error) {
	if err := validateSecurityDepositRefundAdminBase(input.UserID, input.LotID, input.OperatorID, &input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	repo, err := s.refundRepository()
	if err != nil {
		return nil, err
	}
	refundID := securityDepositRefundID(SecurityDepositRefundModeManual, input.UserID, input.IdempotencyKey)
	if existing, err := repo.GetSecurityDepositRefund(ctx, refundID); err != nil {
		return nil, err
	} else if existing != nil {
		existing.AlreadyProcessed = true
		return s.finishSecurityDepositRefundChange(ctx, existing, "admin_manual_refund_reserve")
	}
	target, err := repo.GetSecurityDepositRefundTarget(ctx, input.UserID, input.LotID)
	if err != nil {
		return nil, err
	}
	policy, err := s.securityDepositPolicyStrict(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "security deposit policy is temporarily unavailable").WithCause(err)
	}
	requestID := SecurityDepositAdminActionKey("manual_refund", input.UserID, input.IdempotencyKey)
	var result *SecurityDepositRefundRecord
	err = s.withSecurityDepositFinancialFence(ctx, input.UserID, requestID, func() error {
		result, err = repo.ReserveSecurityDepositRefund(ctx, SecurityDepositRefundReserveInput{
			RefundID: refundID, UserID: input.UserID, LotID: input.LotID,
			PaymentOrderID: target.PaymentOrderID, PrincipalCents: target.PrincipalCents,
			GatewayAmount: formatSecurityDepositCents(target.PrincipalCents), GatewayCurrency: target.Currency,
			Mode: SecurityDepositRefundModeManual, OperatorID: input.OperatorID, Reason: input.Reason,
			QuoteHash:      securityDepositRefundQuoteHash(target, formatSecurityDepositCents(target.PrincipalCents)),
			IdempotencyKey: requestID,
		}, policy.EnforcementEnabled)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.finishSecurityDepositRefundChange(ctx, result, "admin_manual_refund_reserve")
}

// AdminCompleteManualRefund 使用必填外部退款事实核销人工预留。
func (s *SecurityDepositService) AdminCompleteManualRefund(ctx context.Context, input AdminSecurityDepositManualCompleteInput) (*SecurityDepositRefundRecord, error) {
	if input.UserID <= 0 || input.OperatorID <= 0 || strings.TrimSpace(input.RefundID) == "" {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_MANUAL_REFUND", "user_id, operator_id and refund_id are required")
	}
	if err := normalizeSecurityDepositAdminInput(&input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	input.RefundID = strings.TrimSpace(input.RefundID)
	input.ExternalRefundID = strings.TrimSpace(input.ExternalRefundID)
	if input.ExternalRefundID == "" || input.ExternalAmountCents <= 0 || input.ExternalRefundedAt.IsZero() || len(input.ExternalEvidence) == 0 {
		return nil, infraerrors.BadRequest("SECURITY_DEPOSIT_EXTERNAL_REFUND_FACTS_REQUIRED", "external refund id, amount, time and evidence are required")
	}
	if len([]rune(input.ExternalRefundID)) > 191 {
		return nil, infraerrors.BadRequest("SECURITY_DEPOSIT_EXTERNAL_REFUND_ID_TOO_LONG", "external refund id must not exceed 191 characters")
	}
	repo, err := s.refundRepository()
	if err != nil {
		return nil, err
	}
	operationID := SecurityDepositAdminActionKey("manual_refund_complete", input.UserID, input.IdempotencyKey)
	var result *SecurityDepositRefundRecord
	err = s.withSecurityDepositFinancialFence(ctx, input.UserID, operationID, func() error {
		var completeErr error
		result, completeErr = repo.CompleteManualSecurityDepositRefund(ctx, input)
		return completeErr
	})
	if err != nil {
		return nil, err
	}
	return s.finishSecurityDepositRefundChange(ctx, result, "admin_manual_refund_complete")
}

// AdminCancelSecurityDepositRefund 仅释放尚未确认成功的人工预留或明确失败预留。
func (s *SecurityDepositService) AdminCancelSecurityDepositRefund(ctx context.Context, input AdminSecurityDepositRefundCancelInput) (*SecurityDepositRefundRecord, error) {
	if input.UserID <= 0 || input.OperatorID <= 0 || strings.TrimSpace(input.RefundID) == "" {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND_CANCEL", "user_id, operator_id and refund_id are required")
	}
	if err := normalizeSecurityDepositAdminInput(&input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	input.RefundID = strings.TrimSpace(input.RefundID)
	repo, err := s.refundRepository()
	if err != nil {
		return nil, err
	}
	result, err := repo.CancelSecurityDepositRefund(ctx, input)
	if err != nil {
		return nil, err
	}
	return s.finishSecurityDepositRefundChange(ctx, result, "admin_refund_cancel")
}

// AdminFailAutomaticRefundReview 以必填核验凭证确认网关未退款并释放预留。
func (s *SecurityDepositService) AdminFailAutomaticRefundReview(ctx context.Context, input AdminSecurityDepositAutomaticReviewFailureInput) (*SecurityDepositRefundRecord, error) {
	if input.UserID <= 0 || input.OperatorID <= 0 || strings.TrimSpace(input.RefundID) == "" || len(input.Evidence) == 0 {
		return nil, infraerrors.BadRequest("SECURITY_DEPOSIT_REFUND_REVIEW_EVIDENCE_REQUIRED", "user_id, operator_id, refund_id and review evidence are required")
	}
	if err := normalizeSecurityDepositAdminInput(&input.IdempotencyKey, &input.Reason); err != nil {
		return nil, err
	}
	input.RefundID = strings.TrimSpace(input.RefundID)
	repo, err := s.refundRepository()
	if err != nil {
		return nil, err
	}
	result, err := repo.FailAutomaticSecurityDepositRefundReview(ctx, input)
	if err != nil {
		return nil, err
	}
	return s.finishSecurityDepositRefundChange(ctx, result, "admin_refund_review_failed")
}

func (s *SecurityDepositService) finishSecurityDepositRefundChange(ctx context.Context, result *SecurityDepositRefundRecord, eventType string) (*SecurityDepositRefundRecord, error) {
	if result == nil {
		return nil, nil
	}
	disabled, err := s.reconcileKeysAfterBalanceChange(ctx, result.UserID, eventType, result.ID, result.DisabledKeyIDs)
	if err != nil {
		return nil, fmt.Errorf("reconcile security deposit keys after refund change: %w", err)
	}
	result.DisabledKeyIDs = disabled
	s.invalidateSecurityDepositUser(ctx, result.UserID)
	return result, nil
}

func (s *SecurityDepositService) finishAdminAutomaticSecurityDepositRefund(ctx context.Context, repo SecurityDepositRefundRepository, refundID string, response *payment.RefundResponse, gatewayErr error, result **SecurityDepositRefundRecord) error {
	state := SecurityDepositRefundStateSucceeded
	providerRefundID := ""
	snapshot := map[string]any{}
	if response != nil {
		providerRefundID = strings.TrimSpace(response.RefundID)
		snapshot["refund_id"] = providerRefundID
		snapshot["status"] = strings.TrimSpace(response.Status)
	}
	if gatewayErr != nil {
		snapshot["error"] = gatewayErr.Error()
		if payment.IsRefundOutcomeUnknown(gatewayErr) {
			state = SecurityDepositRefundStateManualReview
		} else {
			state = SecurityDepositRefundStateFailedReleased
		}
	} else if response == nil {
		state = SecurityDepositRefundStateFailedReleased
		snapshot["error"] = "payment refund response missing"
	} else {
		switch strings.TrimSpace(response.Status) {
		case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded:
			state = SecurityDepositRefundStateSucceeded
		case payment.ProviderStatusPending:
			state = SecurityDepositRefundStatePending
		default:
			state = SecurityDepositRefundStateFailedReleased
			snapshot["error"] = "unsupported payment refund status"
		}
	}
	updated, err := repo.FinalizeAutomaticSecurityDepositRefund(ctx, refundID, state, providerRefundID, snapshot)
	if err != nil {
		return err
	}
	*result = updated
	return nil
}

func (s *SecurityDepositService) refundRepository() (SecurityDepositRefundRepository, error) {
	if s == nil || s.repo == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_UNAVAILABLE", "security deposit refund is unavailable")
	}
	repo, ok := s.repo.(SecurityDepositRefundRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_UNAVAILABLE", "security deposit refund is unavailable")
	}
	return repo, nil
}

func validateSecurityDepositRefundAdminBase(userID, lotID, operatorID int64, idempotencyKey *string, reason **string) error {
	if userID <= 0 || lotID <= 0 || operatorID <= 0 {
		return infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND", "user_id, lot_id and operator_id are required")
	}
	return normalizeSecurityDepositAdminInput(idempotencyKey, reason)
}

func securityDepositRefundID(mode string, userID int64, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", mode, userID, idempotencyKey)))
	return "sdref_" + hex.EncodeToString(digest[:16])
}

func securityDepositRefundQuoteHash(target *SecurityDepositRefundTarget, gatewayAmount string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%d\x00%d\x00%s", target.UserID, target.LotID, target.PaymentOrderID, target.PrincipalCents, gatewayAmount)))
	return hex.EncodeToString(digest[:])
}

func securityDepositRefundReason(reason *string, lotID int64) string {
	if reason != nil && strings.TrimSpace(*reason) != "" {
		return strings.TrimSpace(*reason)
	}
	return fmt.Sprintf("security deposit refund lot:%d", lotID)
}

func securityDepositProviderRefundID(record *SecurityDepositRefundRecord) string {
	if record == nil || record.ProviderResponseSnapshot == nil {
		return ""
	}
	value, _ := record.ProviderResponseSnapshot["refund_id"].(string)
	return strings.TrimSpace(value)
}

func formatSecurityDepositCents(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}
