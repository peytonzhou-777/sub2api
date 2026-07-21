package admin

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// RecurringCreditHandler 处理管理员循环赠额任务与执行历史。
type RecurringCreditHandler struct {
	service *service.RecurringCreditService
}

// NewRecurringCreditHandler 创建循环赠额处理器。
func NewRecurringCreditHandler(recurringService *service.RecurringCreditService) *RecurringCreditHandler {
	return &RecurringCreditHandler{service: recurringService}
}

type recurringTaskRequest struct {
	service.RecurringCreditTaskInput
	ExpectedVersion int `json:"expected_version"`
}

type recurringActionRequest struct {
	ExpectedVersion int                               `json:"expected_version"`
	Count           *int                              `json:"count"`
	Configuration   *service.RecurringCreditTaskInput `json:"configuration"`
}

func recurringActor(c *gin.Context) (service.RecurringCreditActor, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "Administrator context not found")
		return service.RecurringCreditActor{}, false
	}
	return service.RecurringCreditActor{AdminID: subject.UserID, IP: ip.GetClientIP(c)}, true
}

// Preview 返回创建、编辑、恢复或跳过前的成本与日期预览。
func (h *RecurringCreditHandler) Preview(c *gin.Context) {
	var req recurringTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	skipCount, _ := strconv.Atoi(c.DefaultQuery("skip_count", "0"))
	item, err := h.service.Preview(c.Request.Context(), req.RecurringCreditTaskInput, skipCount)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// Create 创建并启用任务或保存为已停止。
func (h *RecurringCreditHandler) Create(c *gin.Context) {
	var req recurringTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	actor, ok := recurringActor(c)
	if !ok {
		return
	}
	item, err := h.service.CreateTask(c.Request.Context(), req.RecurringCreditTaskInput, actor, c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, item)
}

// List 分页返回任务列表。
func (h *RecurringCreditHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.service.ListTasks(c.Request.Context(), page, pageSize, service.RecurringCreditListFilter{Search: c.Query("search"), Status: c.Query("status"), Mode: c.Query("mode"), ScheduleType: c.Query("schedule_type"), IncludeDeleted: c.Query("include_deleted") == "true"})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// Get 返回单个任务详情。
func (h *RecurringCreditHandler) Get(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	item, err := h.service.GetTask(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// Update 完整更新任务未来配置。
func (h *RecurringCreditHandler) Update(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req recurringTaskRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	actor, ok := recurringActor(c)
	if !ok {
		return
	}
	item, err := h.service.UpdateTask(c.Request.Context(), id, req.RecurringCreditTaskInput, req.ExpectedVersion, actor)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// Action 执行单一、显式的任务生命周期操作。
func (h *RecurringCreditHandler) Action(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseIDParam(c, "id")
		if !ok {
			return
		}
		var req recurringActionRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request: "+err.Error())
			return
		}
		actor, ok := recurringActor(c)
		if !ok {
			return
		}
		item, err := h.service.TaskAction(c.Request.Context(), id, action, req.ExpectedVersion, req.Count, req.Configuration, actor)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		response.Success(c, item)
	}
}

// Delete 逻辑删除任务，已领取批次继续完成。
func (h *RecurringCreditHandler) Delete(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	expected, err := strconv.Atoi(c.Query("expected_version"))
	if err != nil {
		response.BadRequest(c, "expected_version is required")
		return
	}
	actor, ok := recurringActor(c)
	if !ok {
		return
	}
	item, err := h.service.TaskAction(c.Request.Context(), id, "delete", expected, nil, nil, actor)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// ListBatches 返回任务批次历史。
func (h *RecurringCreditHandler) ListBatches(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	var start, end *time.Time
	if raw := strings.TrimSpace(c.Query("start")); raw != "" {
		v, e := time.Parse(time.RFC3339, raw)
		if e != nil {
			response.BadRequest(c, "start must be RFC3339")
			return
		}
		start = &v
	}
	if raw := strings.TrimSpace(c.Query("end")); raw != "" {
		v, e := time.Parse(time.RFC3339, raw)
		if e != nil {
			response.BadRequest(c, "end must be RFC3339")
			return
		}
		end = &v
	}
	items, total, err := h.service.ListBatches(c.Request.Context(), id, page, pageSize, c.Query("status"), start, end)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// ListUsers 返回批次逐用户结果。
func (h *RecurringCreditHandler) ListUsers(c *gin.Context) {
	batchID, ok := parseIDParam(c, "batch_id")
	if !ok {
		return
	}
	page, pageSize := response.ParsePagination(c)
	items, total, err := h.service.ListUserItems(c.Request.Context(), batchID, page, pageSize, c.Query("search"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total, "page": page, "page_size": pageSize})
}

// ExportUsers 导出成功或空批次的完整 CSV。
func (h *RecurringCreditHandler) ExportUsers(c *gin.Context) {
	batchID, ok := parseIDParam(c, "batch_id")
	if !ok {
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=recurring-credit-%d-users.csv", batchID))
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	if err := h.service.ExportUserItemsCSV(c.Request.Context(), batchID, c.Writer); err != nil {
		response.ErrorFrom(c, err)
	}
}
