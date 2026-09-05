package handler

import (
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// recordOpenAIContinuationSelectionFailure 在尚未转发时也记录被拒绝的原绑定目标。
func recordOpenAIContinuationSelectionFailure(c *gin.Context, log *zap.Logger, err error) {
	var failure *service.OpenAIContinuationSelectionError
	if !errors.As(err, &failure) {
		return
	}
	setOpsSelectedAccount(c, failure.AccountID, service.PlatformOpenAI)
	c.Set("openai_continuation_failure", failure)
	log.Warn("openai.continuation_selection_rejected", zap.Int64("binding_id", failure.BindingID),
		zap.Int64("account_id", failure.AccountID), zap.Int64("account_persona_id", failure.AccountPersonaID),
		zap.Int64("persona_session_epoch", failure.SessionEpoch), zap.String("binding_source", failure.Source), zap.Error(failure.Cause))
}
