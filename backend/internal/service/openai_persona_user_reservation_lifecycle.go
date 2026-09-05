package service

import (
	"context"
	"errors"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// OpenAIPersonaClientSessionHash 仅生成对话绑定的诊断摘要，不参与 Persona 容量身份。
func OpenAIPersonaClientSessionHash(ctx context.Context, sessionHash string) (string, error) {
	if ctx == nil {
		return "", errors.New("OpenAI Persona client Session identity is unavailable")
	}
	sessionHash = strings.TrimSpace(sessionHash)
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	if userID <= 0 || apiKeyID <= 0 || sessionHash == "" {
		return "", errors.New("OpenAI Persona client Session identity is unavailable")
	}
	return openAIUserAffinityScopedStateHash(
		userID, apiKeyID, "openai:persona-active-session:v1", "session_hash", sessionHash,
	), nil
}

func commitOpenAIPersonaUserReservation(ctx context.Context, _ int64) {
	if dynamic := dynamicOpenAIPersonaUserReservationFromContext(ctx); dynamic != nil {
		dynamic.commit()
	}
}

func rollbackOpenAIPersonaUserReservation(ctx context.Context, _ int64) {
	if dynamic := dynamicOpenAIPersonaUserReservationFromContext(ctx); dynamic != nil {
		dynamic.rollback()
	}
}
