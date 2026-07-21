package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ListRechargeBonusCampaigns 返回全部充值活动。
// GET /api/v1/admin/payment/recharge-bonus-campaigns
func (h *PaymentHandler) ListRechargeBonusCampaigns(c *gin.Context) {
	items, err := h.paymentService.ListRechargeBonusCampaigns(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// CreateRechargeBonusCampaign 创建充值活动。
// POST /api/v1/admin/payment/recharge-bonus-campaigns
func (h *PaymentHandler) CreateRechargeBonusCampaign(c *gin.Context) {
	var input service.RechargeBonusCampaignInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	created, err := h.paymentService.CreateRechargeBonusCampaign(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, created)
}

// UpdateRechargeBonusCampaign 更新预约活动或提前结束进行中活动。
// PUT /api/v1/admin/payment/recharge-bonus-campaigns/:id
func (h *PaymentHandler) UpdateRechargeBonusCampaign(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var input service.RechargeBonusCampaignInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	updated, err := h.paymentService.UpdateRechargeBonusCampaign(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, updated)
}

// DeleteRechargeBonusCampaign 删除尚未开始的充值活动。
// DELETE /api/v1/admin/payment/recharge-bonus-campaigns/:id
func (h *PaymentHandler) DeleteRechargeBonusCampaign(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.paymentService.DeleteRechargeBonusCampaign(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}
