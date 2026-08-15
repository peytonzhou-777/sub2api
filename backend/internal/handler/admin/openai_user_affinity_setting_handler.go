package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type openAIUserAffinityConfigRequest struct {
	service.OpenAIUserAffinityConfig
	ExpectedVersion *int64 `json:"expected_version"`
}

// GetOpenAIUserAffinityScheduling 返回当前完整配置和实际生效状态。
func (h *SettingHandler) GetOpenAIUserAffinityScheduling(c *gin.Context) {
	config, err := h.settingService.GetOpenAIUserAffinityConfig(c.Request.Context())
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{
		"config":          config,
		"effective_state": openAIUserAffinityEffectiveState(config),
		"config_version":  config.ConfigVersion,
	})
}

// UpdateOpenAIUserAffinityScheduling 使用 expected_version 乐观检查更新完整配置对象。
func (h *SettingHandler) UpdateOpenAIUserAffinityScheduling(c *gin.Context) {
	var req openAIUserAffinityConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid OpenAI user affinity configuration")
		return
	}
	if req.ExpectedVersion == nil {
		response.Error(c, http.StatusBadRequest, "expected_version is required")
		return
	}
	updated, err := h.settingService.UpdateOpenAIUserAffinityConfig(c.Request.Context(), req.OpenAIUserAffinityConfig, *req.ExpectedVersion)
	if response.ErrorFrom(c, err) {
		return
	}
	response.Success(c, gin.H{
		"config":                  updated,
		"effective_state":         openAIUserAffinityEffectiveState(updated),
		"config_version":          updated.ConfigVersion,
		"propagation_deadline_ms": 5000,
	})
}

func openAIUserAffinityEffectiveState(config service.OpenAIUserAffinityConfig) string {
	if !config.Enabled {
		return "disabled"
	}
	return config.Mode
}
