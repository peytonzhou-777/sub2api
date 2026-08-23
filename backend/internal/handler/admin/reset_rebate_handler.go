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

// ResetRebateHandler 处理管理员账号维度重置返利流程。
type ResetRebateHandler struct{ service *service.ResetRebateService }

// NewResetRebateHandler 创建重置返利处理器。
func NewResetRebateHandler(rebateService *service.ResetRebateService) *ResetRebateHandler {
	return &ResetRebateHandler{service: rebateService}
}

type resetRebateWindowRequest struct {
	AccountIDs []int64 `json:"account_ids" binding:"required"`
}

type resetRebateCreateAccountRequest struct {
	AccountID            int64   `json:"account_id" binding:"required"`
	PeriodStart          string  `json:"period_start" binding:"required"`
	RatioMode            string  `json:"ratio_mode" binding:"required"`
	ManualRatio          *string `json:"manual_ratio"`
	DefaultWindowVersion string  `json:"default_window_version"`
	WindowModified       bool    `json:"window_modified"`
}

type resetRebateCreateRequest struct {
	MechanismVersion            int                               `json:"mechanism_version" binding:"required"`
	PeriodEnd                   string                            `json:"period_end" binding:"required"`
	AverageBenefitEnabled       bool                              `json:"average_benefit_enabled"`
	ForceStatRatioEnabled       bool                              `json:"force_stat_ratio_enabled"`
	ForceStatRatio              string                            `json:"force_stat_ratio" binding:"required"`
	AcknowledgedErrorAccountIDs []int64                           `json:"acknowledged_error_account_ids"`
	Accounts                    []resetRebateCreateAccountRequest `json:"accounts" binding:"required"`
}

func resetRebateActor(c *gin.Context) (service.ResetRebateActor, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Administrator context not found")
		return service.ResetRebateActor{}, false
	}
	return service.ResetRebateActor{AdminID: subject.UserID}, true
}

// AccountWindowDefaults 批量返回服务端权威默认窗口。
func (h *ResetRebateHandler) AccountWindowDefaults(c *gin.Context) {
	var req resetRebateWindowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	items, err := h.service.AccountWindowDefaults(c.Request.Context(), req.AccountIDs)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

// Create 冻结账号配置并启动异步本地统计。
func (h *ResetRebateHandler) Create(c *gin.Context) {
	var req resetRebateCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	actor, ok := resetRebateActor(c)
	if !ok {
		return
	}
	periodEnd, err := time.Parse(time.RFC3339, req.PeriodEnd)
	if err != nil {
		response.BadRequest(c, "period_end must be RFC3339 with timezone")
		return
	}
	input := service.ResetRebateCreateInput{
		MechanismVersion: req.MechanismVersion, ForceStatRatioEnabled: req.ForceStatRatioEnabled,
		ForceStatRatio: req.ForceStatRatio, AcknowledgedErrorAccountIDs: req.AcknowledgedErrorAccountIDs,
		PeriodEnd: periodEnd, AverageBenefitEnabled: req.AverageBenefitEnabled,
		Accounts: make([]service.ResetRebateAccountInput, 0, len(req.Accounts)),
	}
	for _, item := range req.Accounts {
		start, err := time.Parse(time.RFC3339, item.PeriodStart)
		if err != nil {
			response.BadRequest(c, "period_start must be RFC3339 with timezone")
			return
		}
		input.Accounts = append(input.Accounts, service.ResetRebateAccountInput{
			AccountID: item.AccountID, PeriodStart: start, RatioMode: item.RatioMode,
			ManualRatio: item.ManualRatio, DefaultWindowVersion: item.DefaultWindowVersion, WindowModified: item.WindowModified,
		})
	}
	item, err := h.service.CreateStats(c.Request.Context(), actor, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, item)
}

// List 返回新旧机制历史列表。
func (h *ResetRebateHandler) List(c *gin.Context) {
	page, pageSize := response.ParsePagination(c)
	filter := service.ResetRebateListFilter{Status: strings.TrimSpace(c.Query("status")), AccountSearch: strings.TrimSpace(c.Query("account"))}
	filter.AdminID, _ = strconv.ParseInt(c.Query("admin_id"), 10, 64)
	filter.ExecutedAdminID, _ = strconv.ParseInt(c.Query("executed_admin_id"), 10, 64)
	if raw := strings.TrimSpace(c.Query("created_start")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.BadRequest(c, "created_start must be RFC3339")
			return
		}
		filter.CreatedStart = &value
	}
	if raw := strings.TrimSpace(c.Query("created_end")); raw != "" {
		value, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.BadRequest(c, "created_end must be RFC3339")
			return
		}
		filter.CreatedEnd = &value
	}
	items, err := h.service.ListBatches(c.Request.Context(), page, pageSize, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// Get 返回单个批次。
func (h *ResetRebateHandler) Get(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	item, err := h.service.GetBatch(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// ListAccounts 返回账号快照。
func (h *ResetRebateHandler) ListAccounts(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, err := h.service.ListAccounts(c.Request.Context(), id, page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// ListUsers 返回用户汇总或失败名单。
func (h *ResetRebateHandler) ListUsers(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	page, size := response.ParsePagination(c)
	items, err := h.service.ListUsers(c.Request.Context(), id, page, size, c.Query("search"), c.Query("result"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// ListContributions 返回用户逐账号贡献。
func (h *ResetRebateHandler) ListContributions(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	userID, ok := parseIDParam(c, "user_id")
	if !ok {
		return
	}
	items, err := h.service.ListContributions(c.Request.Context(), id, userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items})
}

type resetRebatePreviewRequest struct {
	PayoutRatio int    `json:"payout_ratio" binding:"required"`
	Reason      string `json:"reason"`
}

// Preview 保存版本化预览。
func (h *ResetRebateHandler) Preview(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req resetRebatePreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	page, size := response.ParsePagination(c)
	item, err := h.service.Preview(c.Request.Context(), id, req.PayoutRatio, page, size, req.Reason, c.Query("search"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

type resetRebateExecuteRequest struct {
	PreviewVersion int  `json:"preview_version"`
	Confirmed      bool `json:"confirmed" binding:"required"`
}
type resetRebateRetryRequest struct {
	Confirmed bool `json:"confirmed" binding:"required"`
}

// Execute 按当前预览逐用户发放。
func (h *ResetRebateHandler) Execute(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req resetRebateExecuteRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirmed {
		response.BadRequest(c, "confirmation checkbox is required")
		return
	}
	actor, ok := resetRebateActor(c)
	if !ok {
		return
	}
	item, err := h.service.Execute(c.Request.Context(), id, req.PreviewVersion, actor)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// RetryFailures 只重试当前失败用户。
func (h *ResetRebateHandler) RetryFailures(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	var req resetRebateRetryRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirmed {
		response.BadRequest(c, "confirmation checkbox is required")
		return
	}
	actor, ok := resetRebateActor(c)
	if !ok {
		return
	}
	item, err := h.service.RetryFailures(c.Request.Context(), id, actor)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

// Delete 删除允许清理且从未成功发放的快照。
func (h *ResetRebateHandler) Delete(c *gin.Context) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	if err := h.service.DeleteBatch(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

func (h *ResetRebateHandler) export(c *gin.Context, suffix string, exporter func(int64) error) {
	id, ok := parseIDParam(c, "id")
	if !ok {
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=reset-rebate-%d-%s.csv", id, suffix))
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	if err := exporter(id); err != nil {
		response.ErrorFrom(c, err)
	}
}

// ExportUsers 导出用户汇总。
func (h *ResetRebateHandler) ExportUsers(c *gin.Context) {
	h.export(c, "users", func(id int64) error { return h.service.ExportUsersCSV(c.Request.Context(), id, c.Writer) })
}

// ExportAccounts 导出包含自动排除原因的完整账号快照。
func (h *ResetRebateHandler) ExportAccounts(c *gin.Context) {
	h.export(c, "accounts", func(id int64) error { return h.service.ExportAccountsCSV(c.Request.Context(), id, c.Writer) })
}

// ExportContributions 导出用户账号贡献。
func (h *ResetRebateHandler) ExportContributions(c *gin.Context) {
	h.export(c, "user-account-contributions", func(id int64) error { return h.service.ExportContributionsCSV(c.Request.Context(), id, c.Writer) })
}

// ExportFailedUsers 导出当前失败用户。
func (h *ResetRebateHandler) ExportFailedUsers(c *gin.Context) {
	h.export(c, "failed-users", func(id int64) error { return h.service.ExportFailedUsersCSV(c.Request.Context(), id, c.Writer) })
}
