package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

// registerOpenAIUserAffinityAccountRoutes 注册居民、归属审计和账号策略接口。
func registerOpenAIUserAffinityAccountRoutes(accounts *gin.RouterGroup, h *handler.Handlers) {
	accounts.GET("/user-affinity/:user_id", h.Admin.Account.GetOpenAIUserAffinityUserDetail)
	accounts.POST("/user-affinity/:user_id/reset", h.Admin.Account.ResetOpenAIUserAffinityPlacement)
	accounts.GET("/:id/affinity-residents", h.Admin.Account.ListOpenAIUserAffinityResidents)
	accounts.GET("/:id/affinity-policy", h.Admin.Account.GetOpenAIUserAffinityAccountPolicy)
	accounts.PUT("/:id/affinity-policy", h.Admin.Account.UpdateOpenAIUserAffinityAccountPolicy)
}

// registerOpenAIUserAffinitySettingRoutes 注册完整对象和版本检查配置接口。
func registerOpenAIUserAffinitySettingRoutes(settings *gin.RouterGroup, h *handler.Handlers) {
	settings.GET("/openai-user-affinity-scheduling", h.Admin.Setting.GetOpenAIUserAffinityScheduling)
	settings.PUT("/openai-user-affinity-scheduling", h.Admin.Setting.UpdateOpenAIUserAffinityScheduling)
}
