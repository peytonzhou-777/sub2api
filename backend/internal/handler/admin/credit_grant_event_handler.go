package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type creditGrantEventRequest struct {
	Name              string    `json:"name" binding:"required"`
	CreditType        string    `json:"credit_type" binding:"required,oneof=permanent limited"`
	Amount            float64   `json:"amount" binding:"required,gt=0"`
	ValidityDays      *int      `json:"validity_days"`
	ExpectedUpdatedAt time.Time `json:"expected_updated_at"`
}

func creditGrantEventID(c *gin.Context, param string) (int64, bool) {
	id, err := strconv.ParseInt(c.Param(param), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid credit grant event ID")
		return 0, false
	}
	return id, true
}

func creditGrantEventInput(req creditGrantEventRequest) service.CreditGrantEventInput {
	return service.CreditGrantEventInput{
		Name:         req.Name,
		CreditType:   req.CreditType,
		Amount:       req.Amount,
		ValidityDays: req.ValidityDays,
	}
}

// ListCreditGrantEvents 分页返回未删除的赠额事件。
func (h *UserHandler) ListCreditGrantEvents(c *gin.Context) {
	page, size := response.ParsePagination(c)
	items, total, err := h.creditService().ListCreditGrantEvents(c.Request.Context(), page, size, c.Query("search"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Paginated(c, items, total, page, size)
}

// CreateCreditGrantEvent 创建一项立即生效的赠额事件。
func (h *UserHandler) CreateCreditGrantEvent(c *gin.Context) {
	var req creditGrantEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	item, err := h.creditService().CreateCreditGrantEvent(c.Request.Context(), creditGrantEventInput(req))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, item)
}

// UpdateCreditGrantEvent 完整更新事件的后续发放配置。
func (h *UserHandler) UpdateCreditGrantEvent(c *gin.Context) {
	id, ok := creditGrantEventID(c, "event_id")
	if !ok {
		return
	}
	var req creditGrantEventRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.ExpectedUpdatedAt.IsZero() {
		response.BadRequest(c, "expected_updated_at is required")
		return
	}
	item, err := h.creditService().UpdateCreditGrantEvent(c.Request.Context(), id, creditGrantEventInput(req), req.ExpectedUpdatedAt)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// DeleteCreditGrantEvent 软删除事件。
func (h *UserHandler) DeleteCreditGrantEvent(c *gin.Context) {
	id, ok := creditGrantEventID(c, "event_id")
	if !ok {
		return
	}
	expectedUpdatedAt, err := time.Parse(time.RFC3339Nano, c.Query("expected_updated_at"))
	if err != nil {
		response.BadRequest(c, "expected_updated_at is required and must be RFC3339")
		return
	}
	if err = h.creditService().DeleteCreditGrantEvent(c.Request.Context(), id, expectedUpdatedAt); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "credit grant event deleted"})
}

// TriggerCreditGrantEvent 为单个用户直接触发一项赠额事件。
func (h *UserHandler) TriggerCreditGrantEvent(c *gin.Context) {
	userID, ok := creditUserID(c)
	if !ok {
		return
	}
	eventID, ok := creditGrantEventID(c, "event_id")
	if !ok {
		return
	}
	item, err := h.creditService().TriggerCreditGrantEvent(c.Request.Context(), userID, eventID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}
