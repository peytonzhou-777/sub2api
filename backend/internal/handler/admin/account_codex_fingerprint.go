package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) codexFingerprintAdminService(c *gin.Context) (service.CodexFingerprintAdminService, bool) {
	adminService, ok := h.adminService.(service.CodexFingerprintAdminService)
	if !ok {
		response.Error(c, http.StatusNotImplemented, "Codex fingerprint admin service unavailable")
	}
	return adminService, ok
}

func parseCodexFingerprintAccountID(c *gin.Context) (int64, bool) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "invalid account id")
		return 0, false
	}
	return accountID, true
}

// GetCodexFingerprintStatus 返回账号级指纹灰度状态和安全统计。
func (h *AccountHandler) GetCodexFingerprintStatus(c *gin.Context) {
	accountID, ok := parseCodexFingerprintAccountID(c)
	if !ok {
		return
	}
	adminService, ok := h.codexFingerprintAdminService(c)
	if !ok {
		return
	}
	status, err := adminService.GetCodexFingerprintStatus(c.Request.Context(), accountID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, status)
}

// RotateCodexFingerprint 手动推进 Session epoch。
func (h *AccountHandler) RotateCodexFingerprint(c *gin.Context) {
	accountID, ok := parseCodexFingerprintAccountID(c)
	if !ok {
		return
	}
	adminService, ok := h.codexFingerprintAdminService(c)
	if !ok {
		return
	}
	status, err := adminService.RotateCodexFingerprint(c.Request.Context(), accountID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, status)
}

// DisableCodexFingerprint 一键关闭账号指纹收敛。
func (h *AccountHandler) DisableCodexFingerprint(c *gin.Context) {
	accountID, ok := parseCodexFingerprintAccountID(c)
	if !ok {
		return
	}
	adminService, ok := h.codexFingerprintAdminService(c)
	if !ok {
		return
	}
	status, err := adminService.DisableCodexFingerprint(c.Request.Context(), accountID)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, status)
}
