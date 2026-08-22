package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type resetOpenAIUserAffinityRequest struct {
	ScopeKey      string `json:"scope_key" binding:"required"`
	ExcludeSource bool   `json:"exclude_source_account"`
}

func (h *AccountHandler) openAIUserAffinityAdminService(c *gin.Context) (service.OpenAIUserAffinityAdminService, bool) {
	adminService, ok := h.adminService.(service.OpenAIUserAffinityAdminService)
	if !ok {
		response.Error(c, http.StatusNotImplemented, "OpenAI user affinity admin service unavailable")
	}
	return adminService, ok
}

// ListOpenAIUserAffinityResidents 查看账号当前有效、替换中及排空中的常驻槽位。
func (h *AccountHandler) ListOpenAIUserAffinityResidents(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "50"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	adminService, ok := h.openAIUserAffinityAdminService(c)
	if !ok {
		return
	}
	items, total, err := adminService.ListOpenAIUserAffinityResidents(c.Request.Context(), accountID, pageSize, (page-1)*pageSize)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, response.PaginatedData{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: int((total + int64(pageSize) - 1) / int64(pageSize))})
}

// GetOpenAIUserAffinityUserDetail 反查用户当前居住账号与搬迁记录。
func (h *AccountHandler) GetOpenAIUserAffinityUserDetail(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "invalid user id")
		return
	}
	adminService, ok := h.openAIUserAffinityAdminService(c)
	if !ok {
		return
	}
	detail, err := adminService.GetOpenAIUserAffinityUserDetail(c.Request.Context(), userID, 100)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, detail)
}

// ResetOpenAIUserAffinityPlacement 手动清除用户归属，下次请求按新居民重新 Best Fit。
func (h *AccountHandler) ResetOpenAIUserAffinityPlacement(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "invalid user id")
		return
	}
	var req resetOpenAIUserAffinityRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.ScopeKey) == "" {
		response.BadRequest(c, "scope_key is required")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Error(c, http.StatusUnauthorized, "admin identity unavailable")
		return
	}
	adminService, ok := h.openAIUserAffinityAdminService(c)
	if !ok {
		return
	}
	if response.ErrorFrom(c, adminService.ResetOpenAIUserAffinityPlacement(c.Request.Context(), userID, subject.UserID, req.ScopeKey, req.ExcludeSource)) {
		return
	}
	response.Success(c, gin.H{"user_id": userID, "reset": true})
}

// GetOpenAIUserAffinityAccountPolicy 读取账号级触达上限、冷却和搬迁阈值覆盖。
func (h *AccountHandler) GetOpenAIUserAffinityAccountPolicy(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return
	}
	adminService, ok := h.openAIUserAffinityAdminService(c)
	if !ok {
		return
	}
	policy, err := adminService.GetOpenAIUserAffinityAccountPolicy(c.Request.Context(), accountID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, policy)
}

// UpdateOpenAIUserAffinityAccountPolicy 更新账号级策略覆盖；JSON null 表示恢复继承。
func (h *AccountHandler) UpdateOpenAIUserAffinityAccountPolicy(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return
	}
	var policy service.OpenAIUserAffinityAccountPolicy
	if err := c.ShouldBindJSON(&policy); err != nil {
		response.BadRequest(c, "invalid affinity policy")
		return
	}
	policy.AccountID = accountID
	adminService, ok := h.openAIUserAffinityAdminService(c)
	if !ok {
		return
	}
	if response.ErrorFrom(c, adminService.UpdateOpenAIUserAffinityAccountPolicy(c.Request.Context(), policy)) {
		return
	}
	updated, err := adminService.GetOpenAIUserAffinityAccountPolicy(c.Request.Context(), accountID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, updated)
}
