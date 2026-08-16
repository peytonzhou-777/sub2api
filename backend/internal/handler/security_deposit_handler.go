package handler

import (
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// SecurityDepositHandler 处理用户保证金只读查询。
type SecurityDepositHandler struct {
	service *service.SecurityDepositService
}

type createSecurityDepositOrderRequest struct {
	GroupID           int64  `json:"group_id" binding:"required"`
	AgreementVersion  string `json:"agreement_version" binding:"required"`
	AgreementHash     string `json:"agreement_hash" binding:"required"`
	QuoteHash         string `json:"quote_hash" binding:"required"`
	Accepted          bool   `json:"accepted" binding:"required"`
	PaymentType       string `json:"payment_type" binding:"required"`
	OpenID            string `json:"openid"`
	WechatResumeToken string `json:"wechat_resume_token"`
	ReturnURL         string `json:"return_url"`
	PaymentSource     string `json:"payment_source"`
	IsMobile          *bool  `json:"is_mobile,omitempty"`
}

type securityDepositRefundRequest struct {
	LotID int64 `json:"lot_id" binding:"required"`
}

// NewSecurityDepositHandler 创建用户保证金查询处理器。
func NewSecurityDepositHandler(securityDepositService *service.SecurityDepositService) *SecurityDepositHandler {
	return &SecurityDepositHandler{service: securityDepositService}
}

// GetAccount 返回当前用户独立于调用余额的保证金汇总与批次。
func (h *SecurityDepositHandler) GetAccount(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	account, err := h.service.GetAccount(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, account)
}

// GetEligibility 返回指定分组的权威保证金资格和差额。
func (h *SecurityDepositHandler) GetEligibility(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	groupID, err := strconv.ParseInt(strings.TrimSpace(c.Query("group_id")), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "group_id must be a positive integer")
		return
	}
	eligibility, err := h.service.GetEligibility(c.Request.Context(), subject.UserID, groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, eligibility)
}

// GetAgreement 返回当前保证金协议；group_id 可选，用于分组版本覆盖。
func (h *SecurityDepositHandler) GetAgreement(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	var groupID int64
	if raw := strings.TrimSpace(c.Query("group_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			response.BadRequest(c, "group_id must be a positive integer")
			return
		}
		groupID = parsed
	}
	agreement, err := h.service.GetAgreement(c.Request.Context(), subject.UserID, groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, agreement)
}

// CreateOrder 接受协议并创建仅收取准确短缺额的保证金订单。
func (h *SecurityDepositHandler) CreateOrder(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	var req createSecurityDepositOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	mobile := isMobile(c)
	if req.IsMobile != nil {
		mobile = *req.IsMobile
	}
	result, err := h.service.CreateOrder(c.Request.Context(), service.CreateSecurityDepositOrderRequest{
		UserID: subject.UserID, GroupID: req.GroupID, PolicyVersion: req.AgreementVersion,
		ContentHash: req.AgreementHash, QuoteHash: req.QuoteHash, PaymentType: req.PaymentType,
		OpenID: req.OpenID, WechatResumeToken: req.WechatResumeToken, ClientIP: c.ClientIP(),
		UserAgent: c.Request.UserAgent(), IsMobile: mobile, IsWeChatBrowser: isWeChatBrowser(c),
		SrcHost: c.Request.Host, SrcURL: c.Request.Referer(), ReturnURL: req.ReturnURL,
		PaymentSource: req.PaymentSource, Locale: c.GetHeader("Accept-Language"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// PreviewRefund 返回当前实付批次可退金额及可能被禁用的密钥。
func (h *SecurityDepositHandler) PreviewRefund(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	var req securityDepositRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.LotID <= 0 {
		response.BadRequest(c, "lot_id must be a positive integer")
		return
	}
	result, err := h.service.PreviewUserRefundPaidLot(c.Request.Context(), subject.UserID, req.LotID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// CreateRefund 二次验证后执行用户自助原路退款。
func (h *SecurityDepositHandler) CreateRefund(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	var req securityDepositRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.LotID <= 0 {
		response.BadRequest(c, "lot_id must be a positive integer")
		return
	}
	result, err := h.service.UserAutomaticRefundPaidLot(c.Request.Context(), service.UserSecurityDepositAutomaticRefundInput{
		UserID: subject.UserID, LotID: req.LotID, IdempotencyKey: c.GetHeader("Idempotency-Key"),
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// GetRefund 查询当前用户退款，并在 pending/unknown 时尝试从网关恢复。
func (h *SecurityDepositHandler) GetRefund(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}
	refundID := strings.TrimSpace(c.Param("id"))
	if refundID == "" {
		response.BadRequest(c, "Invalid refund ID")
		return
	}
	result, err := h.service.QueryAutomaticSecurityDepositRefund(c.Request.Context(), subject.UserID, refundID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
