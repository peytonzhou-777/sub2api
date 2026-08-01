package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// AccountPoolHandler 提供普通用户只读号池接口。
type AccountPoolHandler struct {
	pool     *service.AccountPoolService
	settings *service.SettingService
}

func NewAccountPoolHandler(pool *service.AccountPoolService, settings *service.SettingService) *AccountPoolHandler {
	return &AccountPoolHandler{pool: pool, settings: settings}
}

// List 返回当前完整公开快照；不会触发账号写入或上游查询。
func (h *AccountPoolHandler) List(c *gin.Context) {
	if h.settings == nil || !h.settings.IsAccountPoolEnabled(c.Request.Context()) {
		response.NotFound(c, "Not found")
		return
	}
	page, pageSize, accountID, ok := parseAccountPoolQuery(c)
	if !ok {
		response.BadRequest(c, "Invalid query parameters")
		return
	}
	result, err := h.pool.List(c.Request.Context(), h.settings.GetAccountPoolEnabledEpoch(c.Request.Context()), page, pageSize, accountID)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, "Account pool temporarily unavailable")
		return
	}

	payload, err := json.Marshal(response.Response{Code: 0, Message: "success", Data: result})
	if err != nil {
		response.InternalError(c, "Failed to encode response")
		return
	}
	digest := sha256.Sum256(payload)
	etag := `"` + hex.EncodeToString(digest[:]) + `"`
	c.Header("Cache-Control", "private, no-cache")
	c.Header("Vary", "Authorization")
	c.Header("ETag", etag)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

// PersonalUsage 返回当前用户在指定号池账号的本地 5h/7d 用量汇总。
func (h *AccountPoolHandler) PersonalUsage(c *gin.Context) {
	if h.settings == nil || !h.settings.IsAccountPoolEnabled(c.Request.Context()) {
		response.NotFound(c, "Not found")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Authentication required")
		return
	}
	accountID, ok := parsePositiveASCIIInt(c.Param("id"), 0)
	if !ok {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	result, err := h.pool.GetPersonalUsage(
		c.Request.Context(),
		h.settings.GetAccountPoolEnabledEpoch(c.Request.Context()),
		subject.UserID,
		accountID,
	)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrAccountPoolPersonalUsageNotFound),
			errors.Is(err, service.ErrAccountPoolPersonalUsageUnsupported):
			response.NotFound(c, "Personal usage is unavailable for this account")
		case errors.Is(err, service.ErrAccountPoolSnapshotNotReady),
			errors.Is(err, service.ErrAccountPoolPersonalUsageUnavailable):
			response.Error(c, http.StatusServiceUnavailable, "Account pool temporarily unavailable")
		default:
			response.Error(c, http.StatusServiceUnavailable, "Personal usage temporarily unavailable")
		}
		return
	}
	response.Success(c, result)
}

func parseAccountPoolQuery(c *gin.Context) (int, int, *int64, bool) {
	query := c.Request.URL.Query()
	allowed := map[string]struct{}{"page": {}, "page_size": {}, "account_id": {}}
	for key, values := range query {
		if _, exists := allowed[key]; !exists || len(values) != 1 {
			return 0, 0, nil, false
		}
	}
	page, pageSize := 1, 20
	if raw, exists := query["page"]; exists {
		value, ok := parsePositiveASCIIInt(raw[0], 0)
		if !ok {
			return 0, 0, nil, false
		}
		page = int(value)
	}
	if raw, exists := query["page_size"]; exists {
		value, ok := parsePositiveASCIIInt(raw[0], 1000)
		if !ok {
			return 0, 0, nil, false
		}
		pageSize = int(value)
	}
	var accountID *int64
	if raw, exists := query["account_id"]; exists {
		value, ok := parsePositiveASCIIInt(raw[0], 0)
		if !ok {
			return 0, 0, nil, false
		}
		accountID = &value
		page = 1
	}
	return page, pageSize, accountID, true
}

func parsePositiveASCIIInt(raw string, max int64) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 || (max > 0 && value > max) {
		return 0, false
	}
	return value, true
}
