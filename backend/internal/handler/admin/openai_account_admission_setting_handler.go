package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type openAIAccountAdmissionConfigRequest struct {
	service.OpenAIAccountAdmissionConfig
	ExpectedVersion *int64 `json:"expected_version"`
}

// GetOpenAIAccountAdmission 返回账号准入排队的全局完整配置。
func (h *SettingHandler) GetOpenAIAccountAdmission(c *gin.Context) {
	config, err := h.settingService.GetOpenAIAccountAdmissionConfig(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{
		"config":          config,
		"effective_state": map[bool]string{true: "enabled", false: "disabled"}[config.Enabled],
		"config_version":  config.ConfigVersion,
	})
}

// UpdateOpenAIAccountAdmission 使用 expected_version 原子更新全局完整配置。
func (h *SettingHandler) UpdateOpenAIAccountAdmission(c *gin.Context) {
	var req openAIAccountAdmissionConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid OpenAI account admission configuration")
		return
	}
	if req.ExpectedVersion == nil {
		response.Error(c, http.StatusBadRequest, "expected_version is required")
		return
	}
	updated, err := h.settingService.UpdateOpenAIAccountAdmissionConfig(c.Request.Context(), req.OpenAIAccountAdmissionConfig, *req.ExpectedVersion)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{
		"config":                  updated,
		"effective_state":         map[bool]string{true: "enabled", false: "disabled"}[updated.Enabled],
		"config_version":          updated.ConfigVersion,
		"propagation_deadline_ms": 5000,
	})
}
