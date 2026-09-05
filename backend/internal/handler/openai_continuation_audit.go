package handler

import (
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// recordOpenAIContinuationSelectionFailure 在尚未转发时也记录被拒绝的原绑定目标。
func recordOpenAIContinuationSelectionFailure(c *gin.Context, log *zap.Logger, err error) {
	var failure *service.OpenAIContinuationSelectionError
	if !errors.As(err, &failure) {
		return
	}
	if failure.AccountID > 0 {
		setOpsSelectedAccount(c, failure.AccountID, service.PlatformOpenAI)
	}
	c.Set("openai_continuation_failure", failure)
	log.Warn("openai.continuation_selection_rejected", zap.Int64("binding_id", failure.BindingID),
		zap.Int64("account_id", failure.AccountID), zap.Int64("account_persona_id", failure.AccountPersonaID),
		zap.Int64("persona_session_epoch", failure.SessionEpoch), zap.String("binding_source", failure.Source),
		zap.String("reason", failure.Reason), zap.String("request_kind", failure.RequestKind), zap.String("scope_key", failure.ScopeKey), zap.Error(failure.Cause))
}

// openAIConversationWSResumeError 仅把确证身份失效报告为永久错误，临时故障保持可重试。
func openAIConversationWSResumeError(err error) error {
	if errors.Is(err, service.ErrOpenAIConversationResetRequired) {
		return service.NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "Conversation state expired; start a new conversation", err)
	}
	return service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "Conversation state temporarily unavailable; retry later", err)
}
