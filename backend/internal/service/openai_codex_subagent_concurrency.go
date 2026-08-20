package service

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const (
	codexSubagentConcurrencyLimitReason = GatewayFailureReason("codex_subagent_concurrency_limit")
	codexSubagentConcurrencyStoreReason = GatewayFailureReason("codex_subagent_concurrency_unavailable")
)

// acquireCodexSubagentSlot 仅为已收敛且已识别的子代理请求占槽。
func (s *OpenAIGatewayService) acquireCodexSubagentSlot(
	ctx context.Context,
	account *Account,
	ids *codexFingerprintIDs,
) (func(), error) {
	if account == nil || ids == nil || !ids.isSubagent {
		return func() {}, nil
	}
	limit := account.GetCodexSubagentMaxInflightPerSession()
	if limit <= 0 {
		return func() {}, nil
	}
	if s == nil || s.concurrencyService == nil {
		return nil, newCodexSubagentConcurrencyStoreError()
	}
	result, err := s.concurrencyService.AcquireCodexSubagentSlot(
		ctx, account.ID, ids.sessionScopeHash, ids.sessionEpoch, limit,
	)
	if err != nil {
		logger.L().Error("codex subagent concurrency acquire failed",
			zap.Int64("account_id", account.ID),
			zap.String("scope_hash", truncateCodexFingerprintHash(ids.sessionScopeHash)),
			zap.Int64("epoch", ids.sessionEpoch),
			zap.Error(err),
		)
		return nil, newCodexSubagentConcurrencyStoreError()
	}
	if result == nil || !result.Acquired {
		logger.L().Warn("codex subagent concurrency limited",
			zap.Int64("account_id", account.ID),
			zap.String("scope_hash", truncateCodexFingerprintHash(ids.sessionScopeHash)),
			zap.Int64("epoch", ids.sessionEpoch),
			zap.Int("limit", limit),
		)
		return nil, newCodexSubagentConcurrencyLimitError()
	}
	if result.ReleaseFunc == nil {
		return func() {}, nil
	}
	return result.ReleaseFunc, nil
}

func newCodexSubagentConcurrencyLimitError() *UpstreamFailoverError {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":    "rate_limit_error",
		"code":    string(codexSubagentConcurrencyLimitReason),
		"message": "Subagent concurrency limit exceeded, please retry later",
	}})
	return &UpstreamFailoverError{
		StatusCode:          http.StatusTooManyRequests,
		ResponseBody:        body,
		ResponseHeaders:     http.Header{"Retry-After": []string{"1"}},
		Scope:               GatewayFailureScopeRequest,
		Reason:              codexSubagentConcurrencyLimitReason,
		NextAccountAction:   NextAccountStop,
		ClientStatusCode:    http.StatusTooManyRequests,
		ClientMessage:       "Subagent concurrency limit exceeded, please retry later",
		LocalRequestFailure: true,
	}
}

func newCodexSubagentConcurrencyStoreError() *UpstreamFailoverError {
	err := newCodexSubagentConcurrencyLimitError()
	err.StatusCode = http.StatusServiceUnavailable
	err.Reason = codexSubagentConcurrencyStoreReason
	err.ResponseHeaders = nil
	err.ClientStatusCode = http.StatusServiceUnavailable
	err.ClientMessage = "Subagent concurrency control is temporarily unavailable"
	err.ResponseBody, _ = json.Marshal(map[string]any{"error": map[string]any{
		"type": "server_error", "code": "codex_subagent_concurrency_unavailable", "message": err.ClientMessage,
	}})
	return err
}

// IsCodexSubagentConcurrencyFailure 判断错误是否来自本地子代理并发控制。
func (e *UpstreamFailoverError) IsCodexSubagentConcurrencyFailure() bool {
	return e != nil && e.LocalRequestFailure &&
		(e.Reason == codexSubagentConcurrencyLimitReason || e.Reason == codexSubagentConcurrencyStoreReason)
}
