package handler

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// LimitedCreditHandler 处理用户限时额度查询。
type LimitedCreditHandler struct {
	limitedCreditService *service.LimitedCreditService
}

// NewLimitedCreditHandler 创建限时额度 handler。
func NewLimitedCreditHandler(limitedCreditService *service.LimitedCreditService) *LimitedCreditHandler {
	return &LimitedCreditHandler{limitedCreditService: limitedCreditService}
}

type limitedCreditGrantResponse struct {
	ID              int64     `json:"id"`
	SourceID        *int64    `json:"source_id,omitempty"`
	SourceReason    string    `json:"source_reason,omitempty"`
	InitialAmount   float64   `json:"initial_amount"`
	UsedAmount      float64   `json:"used_amount"`
	FrozenAmount    float64   `json:"frozen_amount"`
	RemainingAmount float64   `json:"remaining_amount"`
	AvailableAmount float64   `json:"available_amount"`
	ExpiresAt       time.Time `json:"expires_at"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

// GetActive 返回当前用户仍可见的限时额度明细。
// GET /api/v1/limited-credits/active
func (h *LimitedCreditHandler) GetActive(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	grants, err := h.limitedCreditService.ListActive(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	out := make([]limitedCreditGrantResponse, 0, len(grants))
	for _, grant := range grants {
		out = append(out, limitedCreditGrantResponse{
			ID:              grant.ID,
			SourceID:        grant.SourceID,
			SourceReason:    grant.SourceReason,
			InitialAmount:   grant.InitialAmount,
			UsedAmount:      grant.UsedAmount,
			FrozenAmount:    grant.FrozenAmount,
			RemainingAmount: grant.RemainingAmount(),
			AvailableAmount: grant.AvailableAmount(),
			ExpiresAt:       grant.ExpiresAt,
			Status:          grant.Status,
			CreatedAt:       grant.CreatedAt,
		})
	}

	response.Success(c, out)
}

// GetSummary 返回当前用户限时额度汇总。
// GET /api/v1/limited-credits/summary
func (h *LimitedCreditHandler) GetSummary(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return
	}

	summary, err := h.limitedCreditService.GetSummary(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, summary)
}
