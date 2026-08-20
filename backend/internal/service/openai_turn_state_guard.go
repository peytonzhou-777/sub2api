package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const openAITurnStateSessionHashContextKey = "openai_turn_state_session_hash"

// isolateOpenAITurnStateAttempt 为单次账号尝试克隆请求头，并剥离来源未知或属于其他账号的 turn-state。
func (s *OpenAIGatewayService) isolateOpenAITurnStateAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	requestBody []byte,
) func() {
	if c == nil || c.Request == nil {
		return func() {}
	}
	originalRequest := c.Request
	attemptCtx := ctx
	if attemptCtx == nil {
		attemptCtx = originalRequest.Context()
	}
	// 每次转发都使用本轮 ctx，确保账号级指纹、turn-state 与 429 延后处分
	// 只作用于当前尝试，不泄漏到原始客户端请求或后续账号。
	attemptRequest := originalRequest.Clone(attemptCtx)
	attemptRequest.Header = originalRequest.Header.Clone()
	c.Request = attemptRequest

	sessionHash := s.GenerateSessionHash(c, requestBody)
	c.Set(openAITurnStateSessionHashContextKey, sessionHash)
	turnState := strings.TrimSpace(attemptRequest.Header.Get(openAIWSTurnStateHeader))
	if turnState == "" {
		return func() { c.Request = originalRequest }
	}
	allowed, reason, sourceAccountID := s.openAITurnStateAllowedForAccount(ctx, c, account, sessionHash, turnState)
	if !allowed {
		attemptRequest.Header.Del(openAIWSTurnStateHeader)
		s.recordOpenAITurnStateStripped()
		accountID := int64(0)
		if account != nil {
			accountID = account.ID
		}
		logger.L().Warn("openai.turn_state_stripped",
			zap.String("reason", reason),
			zap.Int64("account_id", accountID),
			zap.Int64("source_account_id", sourceAccountID),
			zap.Int64("api_key_id", getAPIKeyIDFromContext(c)),
			zap.Bool("has_session_hash", sessionHash != ""),
		)
	}
	return func() { c.Request = originalRequest }
}

func openAITurnStateSessionHash(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(openAITurnStateSessionHashContextKey)
	if !ok {
		return ""
	}
	sessionHash, _ := value.(string)
	return strings.TrimSpace(sessionHash)
}

func (s *OpenAIGatewayService) openAITurnStateAllowedForAccount(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	sessionHash string,
	turnState string,
) (bool, string, int64) {
	if account == nil || account.ID <= 0 || account.Platform != PlatformOpenAI {
		return false, "non_openai_account", 0
	}
	store := s.getOpenAIWSStateStore()
	apiKeyID := getAPIKeyIDFromContext(c)
	if store == nil || apiKeyID <= 0 || strings.TrimSpace(sessionHash) == "" {
		return false, "missing_provenance_scope", 0
	}
	sourceAccountID, err := store.GetTurnStateAccount(ctx, getOpenAIGroupIDFromContext(c), apiKeyID, sessionHash, turnState)
	if err != nil {
		return false, "provenance_lookup_failed", 0
	}
	if sourceAccountID <= 0 {
		return false, "unknown_source", 0
	}
	if sourceAccountID != account.ID {
		return false, "cross_account", sourceAccountID
	}
	return true, "matched", sourceAccountID
}

// bindOpenAITurnStateProvenance 在有效输出确认后提交状态来源，不持久化原始 state blob。
func (s *OpenAIGatewayService) bindOpenAITurnStateProvenance(
	ctx context.Context,
	c *gin.Context,
	accountID int64,
	sessionHash string,
	turnState string,
	ttl time.Duration,
) {
	store := s.getOpenAIWSStateStore()
	apiKeyID := getAPIKeyIDFromContext(c)
	if store == nil || accountID <= 0 || apiKeyID <= 0 || strings.TrimSpace(sessionHash) == "" || strings.TrimSpace(turnState) == "" {
		return
	}
	if err := store.BindTurnStateAccount(ctx, getOpenAIGroupIDFromContext(c), apiKeyID, sessionHash, turnState, accountID, ttl); err != nil {
		logger.L().Warn("openai.turn_state_provenance_bind_failed",
			zap.Int64("account_id", accountID),
			zap.Int64("api_key_id", apiKeyID),
			zap.Error(err),
		)
	}
}

func cloneOpenAIAttemptHeaderWithTurnState(base http.Header, turnState string) http.Header {
	headers := cloneHeader(base)
	if headers == nil {
		headers = make(http.Header)
	}
	if state := strings.TrimSpace(turnState); state != "" {
		headers.Set(openAIWSTurnStateHeader, state)
	} else {
		headers.Del(openAIWSTurnStateHeader)
	}
	return headers
}
