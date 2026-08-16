package admin

import (
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SecurityDepositHandler 处理管理员保证金查询与审计写操作。
type SecurityDepositHandler struct {
	service *service.SecurityDepositService
}

// NewSecurityDepositHandler 创建管理员保证金查询处理器。
func NewSecurityDepositHandler(securityDepositService *service.SecurityDepositService) *SecurityDepositHandler {
	return &SecurityDepositHandler{service: securityDepositService}
}

// ListUsers 分页返回所有用户的分桶余额与风险倍率。
func (h *SecurityDepositHandler) ListUsers(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.service.ListAdminUsers(c.Request.Context(), page, pageSize, c.Query("search"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, pageSize)
}

// GetUser 返回单个用户的账户、批次、流水、退款和处罚摘要。
func (h *SecurityDepositHandler) GetUser(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return
	}
	detail, err := h.service.GetAdminUserDetail(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, detail)
}

type adminSecurityDepositAmountRequest struct {
	AmountCents int64   `json:"amount_cents" binding:"required"`
	Reason      *string `json:"reason"`
}

type adminSecurityDepositCreditRequest struct {
	AmountCents int64   `json:"amount_cents" binding:"required"`
	ActionType  string  `json:"action_type" binding:"required"`
	Reason      *string `json:"reason"`
}

type adminSecurityDepositReasonRequest struct {
	Reason *string `json:"reason"`
}

type adminSecurityDepositManualCompleteRequest struct {
	ExternalRefundID    string         `json:"external_refund_id" binding:"required"`
	ExternalAmountCents int64          `json:"external_amount_cents" binding:"required"`
	ExternalRefundedAt  time.Time      `json:"external_refunded_at" binding:"required"`
	ExternalEvidence    map[string]any `json:"external_evidence" binding:"required"`
	Reason              *string        `json:"reason"`
}

type adminSecurityDepositReviewFailureRequest struct {
	Evidence map[string]any `json:"evidence" binding:"required"`
	Reason   *string        `json:"reason"`
}

// Credit 为用户发放永久冻结保证金或误判补偿。
func (h *SecurityDepositHandler) Credit(c *gin.Context) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return
	}
	var req adminSecurityDepositCreditRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminCreditAdminGrant(c.Request.Context(), service.AdminSecurityDepositCreditInput{
		UserID: userID, OperatorID: getAdminIDFromContext(c), AmountCents: req.AmountCents,
		ActionType: strings.TrimSpace(req.ActionType), Reason: req.Reason,
		IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// Deduct 仅从管理员发放保证金桶执行扣除。
func (h *SecurityDepositHandler) Deduct(c *gin.Context) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return
	}
	var req adminSecurityDepositAmountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminDeductAdminGrant(c.Request.Context(), service.AdminSecurityDepositDeductInput{
		UserID: userID, OperatorID: getAdminIDFromContext(c), AmountCents: req.AmountCents,
		Reason: req.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// RevokeLot 撤销指定管理员发放批次尚余的全部保证金。
func (h *SecurityDepositHandler) RevokeLot(c *gin.Context) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return
	}
	lotID, ok := parsePositiveSecurityDepositParam(c, "lot_id", "Invalid lot ID")
	if !ok {
		return
	}
	var req adminSecurityDepositReasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminRevokeAdminGrantLot(c.Request.Context(), service.AdminSecurityDepositRevokeInput{
		UserID: userID, OperatorID: getAdminIDFromContext(c), LotID: lotID,
		Reason: req.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// AutomaticRefundLot 对单个用户实付批次执行保证金专用原路退款。
func (h *SecurityDepositHandler) AutomaticRefundLot(c *gin.Context) {
	userID, lotID, ok := parseSecurityDepositUserAndLot(c)
	if !ok {
		return
	}
	var req adminSecurityDepositReasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminAutomaticRefundPaidLot(c.Request.Context(), service.AdminSecurityDepositAutomaticRefundInput{
		UserID: userID, LotID: lotID, OperatorID: getAdminIDFromContext(c),
		Reason: req.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// ReserveManualRefundLot 为外部人工退款建立受审计的保证金预留。
func (h *SecurityDepositHandler) ReserveManualRefundLot(c *gin.Context) {
	userID, lotID, ok := parseSecurityDepositUserAndLot(c)
	if !ok {
		return
	}
	var req adminSecurityDepositReasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminReserveManualRefundPaidLot(c.Request.Context(), service.AdminSecurityDepositManualReserveInput{
		UserID: userID, LotID: lotID, OperatorID: getAdminIDFromContext(c),
		Reason: req.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// CompleteManualRefund 以外部退款事实确认并核销人工预留。
func (h *SecurityDepositHandler) CompleteManualRefund(c *gin.Context) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return
	}
	refundID := strings.TrimSpace(c.Param("refund_id"))
	if refundID == "" {
		response.BadRequest(c, "Invalid refund ID")
		return
	}
	var req adminSecurityDepositManualCompleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminCompleteManualRefund(c.Request.Context(), service.AdminSecurityDepositManualCompleteInput{
		UserID: userID, RefundID: refundID, OperatorID: getAdminIDFromContext(c),
		ExternalRefundID: req.ExternalRefundID, ExternalAmountCents: req.ExternalAmountCents,
		ExternalRefundedAt: req.ExternalRefundedAt, ExternalEvidence: req.ExternalEvidence,
		Reason: req.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// CancelRefund 释放尚未核销的人工退款预留。
func (h *SecurityDepositHandler) CancelRefund(c *gin.Context) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return
	}
	refundID := strings.TrimSpace(c.Param("refund_id"))
	if refundID == "" {
		response.BadRequest(c, "Invalid refund ID")
		return
	}
	var req adminSecurityDepositReasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminCancelSecurityDepositRefund(c.Request.Context(), service.AdminSecurityDepositRefundCancelInput{
		UserID: userID, RefundID: refundID, OperatorID: getAdminIDFromContext(c),
		Reason: req.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// QueryRefund 查询自动退款的 pending/unknown 网关结果并恢复状态。
func (h *SecurityDepositHandler) QueryRefund(c *gin.Context) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return
	}
	refundID := strings.TrimSpace(c.Param("refund_id"))
	if refundID == "" {
		response.BadRequest(c, "Invalid refund ID")
		return
	}
	result, err := h.service.QueryAutomaticSecurityDepositRefund(c.Request.Context(), userID, refundID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// FailAutomaticRefundReview 以人工核验凭证确认网关未退款并释放预留。
func (h *SecurityDepositHandler) FailAutomaticRefundReview(c *gin.Context) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return
	}
	refundID := strings.TrimSpace(c.Param("refund_id"))
	if refundID == "" {
		response.BadRequest(c, "Invalid refund ID")
		return
	}
	var req adminSecurityDepositReviewFailureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminFailAutomaticRefundReview(c.Request.Context(), service.AdminSecurityDepositAutomaticReviewFailureInput{
		UserID: userID, RefundID: refundID, OperatorID: getAdminIDFromContext(c), Evidence: req.Evidence,
		Reason: req.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// UnlockAPIKey 解除官方网安策略安全锁，密钥保持禁用等待用户显式启用。
func (h *SecurityDepositHandler) UnlockAPIKey(c *gin.Context) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return
	}
	apiKeyID, ok := parsePositiveSecurityDepositParam(c, "key_id", "Invalid API key ID")
	if !ok {
		return
	}
	var req adminSecurityDepositReasonRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.service.AdminUnlockSecurityLockedAPIKey(c.Request.Context(), service.AdminSecurityDepositUnlockInput{
		UserID: userID, OperatorID: getAdminIDFromContext(c), APIKeyID: apiKeyID,
		Reason: req.Reason, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func parsePositiveSecurityDepositParam(c *gin.Context, name, message string) (int64, bool) {
	value, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil || value <= 0 {
		response.BadRequest(c, message)
		return 0, false
	}
	return value, true
}

func parseSecurityDepositUserAndLot(c *gin.Context) (int64, int64, bool) {
	userID, ok := parsePositiveSecurityDepositParam(c, "id", "Invalid user ID")
	if !ok {
		return 0, 0, false
	}
	lotID, ok := parsePositiveSecurityDepositParam(c, "lot_id", "Invalid lot ID")
	if !ok {
		return 0, 0, false
	}
	return userID, lotID, true
}
