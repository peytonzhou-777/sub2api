package admin

import (
	"context"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type creditBalanceRequest struct {
	Operation         string    `json:"operation" binding:"required,oneof=add subtract"`
	Amount            float64   `json:"amount" binding:"required,gt=0"`
	Notes             string    `json:"notes"`
	ExpectedUpdatedAt time.Time `json:"expected_updated_at" binding:"required"`
}

type limitedCreditCreateRequest struct {
	Amount       float64 `json:"amount" binding:"required,gt=0"`
	ValidityDays int     `json:"validity_days" binding:"required,min=1,max=36500"`
	Notes        string  `json:"notes"`
}
type limitedCreditAdjustRequest struct {
	AmountTarget      string    `json:"amount_target"`
	AmountOperation   string    `json:"amount_operation"`
	Amount            float64   `json:"amount"`
	ExpiryOperation   string    `json:"expiry_operation"`
	ValidityDays      int       `json:"validity_days"`
	Notes             string    `json:"notes"`
	ExpectedUpdatedAt time.Time `json:"expected_updated_at" binding:"required"`
}
type limitedCreditRevokeRequest struct {
	ExpectedUpdatedAt time.Time `json:"expected_updated_at" binding:"required"`
	Notes             string    `json:"notes"`
}

func creditUserID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return 0, false
	}
	return id, true
}
func creditGrantID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("grant_id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid grant ID")
		return 0, false
	}
	return id, true
}

// creditService 获取额度管理服务，并在依赖注入不完整时立即失败。
func (h *UserHandler) creditService() service.AdminCreditService {
	creditService, ok := h.adminService.(service.AdminCreditService)
	if !ok {
		panic("admin service does not implement AdminCreditService")
	}
	return creditService
}

// ListCreditUsers 返回额度管理用户列表。
func (h *UserHandler) ListCreditUsers(c *gin.Context) {
	page, size := response.ParsePagination(c)
	items, total, err := h.creditService().ListCreditUsers(c.Request.Context(), page, size, c.Query("search"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, size)
}

// GetCreditUserDetail 返回用户额度详情。
func (h *UserHandler) GetCreditUserDetail(c *gin.Context) {
	id, ok := creditUserID(c)
	if !ok {
		return
	}
	item, err := h.creditService().GetCreditUserDetail(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// AdjustCreditBalance 增减用户永久余额。
func (h *UserHandler) AdjustCreditBalance(c *gin.Context) {
	id, ok := creditUserID(c)
	if !ok {
		return
	}
	var req creditBalanceRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	payload := struct {
		UserID  int64
		Request creditBalanceRequest
	}{id, req}
	executeAdminIdempotentJSON(c, "admin.credits.balance.adjust", payload, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.creditService().AdjustCreditBalance(ctx, id, service.AdminBalanceAdjustmentInput{Operation: req.Operation, Amount: req.Amount, Notes: req.Notes, ExpectedUpdatedAt: req.ExpectedUpdatedAt})
	})
}

// CreateAdminLimitedCredit 发放管理员限时额度。
func (h *UserHandler) CreateAdminLimitedCredit(c *gin.Context) {
	id, ok := creditUserID(c)
	if !ok {
		return
	}
	var req limitedCreditCreateRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	item, err := h.creditService().CreateAdminLimitedCredit(c.Request.Context(), id, service.AdminLimitedCreditCreateInput{Amount: req.Amount, ValidityDays: req.ValidityDays, Notes: req.Notes})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// AdjustAdminLimitedCredit 调整单份限时额度。
func (h *UserHandler) AdjustAdminLimitedCredit(c *gin.Context) {
	uid, ok := creditUserID(c)
	if !ok {
		return
	}
	gid, ok := creditGrantID(c)
	if !ok {
		return
	}
	var req limitedCreditAdjustRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	item, err := h.creditService().AdjustAdminLimitedCredit(c.Request.Context(), uid, gid, service.AdminLimitedCreditAdjustmentInput{AmountTarget: req.AmountTarget, AmountOperation: req.AmountOperation, Amount: req.Amount, ExpiryOperation: req.ExpiryOperation, ValidityDays: req.ValidityDays, Notes: req.Notes, ExpectedUpdatedAt: req.ExpectedUpdatedAt})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// ResetAdminLimitedCredit 重置单份限时额度。
func (h *UserHandler) ResetAdminLimitedCredit(c *gin.Context) {
	uid, ok := creditUserID(c)
	if !ok {
		return
	}
	gid, ok := creditGrantID(c)
	if !ok {
		return
	}
	var req limitedCreditRevokeRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	item, err := h.creditService().ResetAdminLimitedCredit(c.Request.Context(), uid, gid, req.ExpectedUpdatedAt, req.Notes)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// RevokeAdminLimitedCredit 作废单份限时额度。
func (h *UserHandler) RevokeAdminLimitedCredit(c *gin.Context) {
	uid, ok := creditUserID(c)
	if !ok {
		return
	}
	gid, ok := creditGrantID(c)
	if !ok {
		return
	}
	var req limitedCreditRevokeRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "Invalid request")
		return
	}
	item, err := h.creditService().RevokeAdminLimitedCredit(c.Request.Context(), uid, gid, req.ExpectedUpdatedAt, req.Notes)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// ListLimitedCreditLedger 返回单份额度流水。
func (h *UserHandler) ListLimitedCreditLedger(c *gin.Context) {
	uid, ok := creditUserID(c)
	if !ok {
		return
	}
	gid, ok := creditGrantID(c)
	if !ok {
		return
	}
	items, err := h.creditService().ListLimitedCreditLedger(c.Request.Context(), uid, gid)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}
