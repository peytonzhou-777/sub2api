package admin

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// ResetRebateHandler 处理管理员重置返利统计与发放。
type ResetRebateHandler struct{ rebateService *service.ResetRebateService }

// NewResetRebateHandler 创建重置返利处理器。
func NewResetRebateHandler(rebateService *service.ResetRebateService) *ResetRebateHandler {
	return &ResetRebateHandler{rebateService: rebateService}
}

type createResetRebateRequest struct {
	GroupID int64  `json:"group_id" binding:"required"`
	Start   string `json:"start" binding:"required"`
	End     string `json:"end" binding:"required"`
}

// Create 创建异步统计任务并立即返回任务摘要。
func (h *ResetRebateHandler) Create(c *gin.Context) {
	var req createResetRebateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	start, err := time.Parse(time.RFC3339, req.Start)
	if err != nil {
		response.BadRequest(c, "start must be RFC3339 with timezone offset")
		return
	}
	end, err := time.Parse(time.RFC3339, req.End)
	if err != nil {
		response.BadRequest(c, "end must be RFC3339 with timezone offset")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Administrator context not found")
		return
	}
	batch, err := h.rebateService.CreateStats(c.Request.Context(), subject.UserID, req.GroupID, start, end)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, batch)
}

// List 返回按创建时间倒序的返利历史。
func (h *ResetRebateHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	filter := service.ResetRebateListFilter{Status: strings.TrimSpace(c.Query("status"))}
	filter.GroupID, _ = strconv.ParseInt(c.Query("group_id"), 10, 64)
	filter.AdminID, _ = strconv.ParseInt(c.Query("admin_id"), 10, 64)
	if raw := strings.TrimSpace(c.Query("period_start")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.BadRequest(c, "period_start must be RFC3339")
			return
		}
		filter.Start = &value
	}
	if raw := strings.TrimSpace(c.Query("period_end")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.BadRequest(c, "period_end must be RFC3339")
			return
		}
		filter.End = &value
	}
	items, total, err := h.rebateService.ListBatches(c.Request.Context(), page, pageSize, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// LatestExecutedPeriodEnd 返回分组最近成功发放批次的统计截止时间。
func (h *ResetRebateHandler) LatestExecutedPeriodEnd(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Query("group_id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "group_id must be a positive integer")
		return
	}
	periodEnd, err := h.rebateService.LatestExecutedPeriodEnd(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"period_end": periodEnd})
}

// Get 返回单个任务进度或批次详情。
func (h *ResetRebateHandler) Get(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	item, err := h.rebateService.GetBatch(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// ListAccounts 分页返回账号资格与上游统计审计。
func (h *ResetRebateHandler) ListAccounts(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.rebateService.ListAccounts(c.Request.Context(), id, page, pageSize)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// Preview 返回指定整数比例下的逐用户发放详情。
func (h *ResetRebateHandler) Preview(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	ratio, err := strconv.Atoi(c.DefaultQuery("ratio", "1"))
	if err != nil {
		response.BadRequest(c, "ratio must be an integer")
		return
	}
	page, pageSize := response.ParsePagination(c)
	item, err := h.rebateService.Preview(c.Request.Context(), id, ratio, page, pageSize, c.Query("search"), c.Query("reason"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

type executeResetRebateRequest struct {
	Ratio     int    `json:"ratio" binding:"required"`
	Confirmed bool   `json:"confirmed" binding:"required"`
	Reason    string `json:"reason"`
}

// Execute 在管理员明确勾选确认后原子发放全部额度。
func (h *ResetRebateHandler) Execute(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req executeResetRebateRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirmed {
		response.BadRequest(c, "confirmation checkbox is required")
		return
	}
	item, err := h.rebateService.Execute(c.Request.Context(), id, req.Ratio, req.Reason)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// ExportUsers 导出当前快照全部用户明细。
func (h *ResetRebateHandler) ExportUsers(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	ratio, err := strconv.Atoi(c.DefaultQuery("ratio", "1"))
	if err != nil {
		response.BadRequest(c, "ratio must be an integer")
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=reset-rebate-%d-users.csv", id))
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	if err = h.rebateService.ExportUsersCSV(c.Request.Context(), id, ratio, c.Writer); err != nil {
		response.ErrorFrom(c, err)
	}
}

// Delete 清理未执行且不在运行中的统计快照。
func (h *ResetRebateHandler) Delete(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.rebateService.DeleteBatch(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}
