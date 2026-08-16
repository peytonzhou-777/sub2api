package handler

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// applySecurityDepositCyberPenalty 在网关请求收尾时同步提交可信官方网安处罚。
// 该适配层独立于 OpenAI handler 主流程，便于其他兼容入口复用并降低上游冲突面。
func (h *OpenAIGatewayHandler) applySecurityDepositCyberPenalty(
	c *gin.Context,
	requestCtx context.Context,
	apiKey *service.APIKey,
	mark *service.CyberPolicyMark,
	groupName string,
	turnIndex *int64,
) {
	if h == nil || h.securityDepositService == nil || c == nil || apiKey == nil || mark == nil || requestCtx == nil {
		return
	}
	grant, ok := requestCtx.Value(ctxkey.SecurityDepositAccessGrant).(*service.SecurityDepositAccessGrant)
	if !ok || grant == nil {
		return
	}
	penaltyRequestID, _ := requestCtx.Value(ctxkey.RequestID).(string)
	penaltyRequestID = strings.TrimSpace(penaltyRequestID)
	if penaltyRequestID == "" && c.Writer != nil {
		penaltyRequestID = strings.TrimSpace(c.Writer.Header().Get("X-Request-ID"))
	}
	if penaltyRequestID == "" {
		return
	}
	upstreamResponseID := strings.TrimSpace(gjson.Get(mark.Body, "response.id").String())
	if upstreamResponseID == "" {
		upstreamResponseID = strings.TrimSpace(gjson.Get(mark.Body, "id").String())
	}
	penaltyCtx, cancelPenalty := context.WithTimeout(requestCtx, 30*time.Second)
	defer cancelPenalty()
	_, penaltyErr := h.securityDepositService.ApplyCyberPolicyPenalty(penaltyCtx, service.SecurityDepositCyberPenaltyInput{
		EventKey:  service.BuildSecurityDepositCyberPenaltyEventKey(penaltyRequestID, apiKey.ID, upstreamResponseID, turnIndex),
		RequestID: penaltyRequestID, UpstreamResponseID: upstreamResponseID, TurnIndex: turnIndex,
		PolicyCode: mark.Code, Grant: *grant, APIKeyID: apiKey.ID,
		APIKeyName: apiKey.Name, GroupName: groupName,
	})
	if penaltyErr != nil {
		requestLogger(c, "handler.openai_gateway.security_deposit_penalty").Error(
			"security_deposit_penalty_failed",
			zap.Int64("user_id", grant.UserID),
			zap.Int64("api_key_id", apiKey.ID),
			zap.Int64("group_id", grant.GroupID),
			zap.Error(penaltyErr),
		)
	}
}
