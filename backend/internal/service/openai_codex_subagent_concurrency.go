package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	codexSubagentConcurrencyLimitReason = GatewayFailureReason("codex_subagent_concurrency_limit")
	codexSubagentConcurrencyStoreReason = GatewayFailureReason("codex_subagent_concurrency_unavailable")
)

type codexSubagentAdmissionLeaseContextKey struct{}

type codexSubagentAdmissionLease struct {
	accountID int64
	scopeHash string
	epoch     int64
	active    *atomic.Bool
}

// acquireCodexSubagentSlot 仅为已收敛且已识别的子代理请求占槽。
func (s *OpenAIGatewayService) acquireCodexSubagentSlot(
	ctx context.Context,
	account *Account,
	ids *codexFingerprintIDs,
) (func(), error) {
	if account == nil || ids == nil || !ids.isSubagent {
		return func() {}, nil
	}
	if lease, _ := ctx.Value(codexSubagentAdmissionLeaseContextKey{}).(codexSubagentAdmissionLease); lease.accountID == account.ID &&
		lease.scopeHash == ids.sessionScopeHash && lease.epoch == ids.sessionEpoch && lease.active != nil && lease.active.Load() {
		return func() {}, nil
	}
	limit := account.GetCodexSubagentMaxInflightPerSession()
	if limit <= 0 {
		return func() {}, nil
	}
	if s == nil || s.concurrencyService == nil {
		return nil, newCodexSubagentConcurrencyStoreError()
	}
	startedAt := time.Now()
	deadline := startedAt.Add(s.codexSubagentQueueMaxWait(ctx))
	for {
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
		if result != nil && result.Acquired {
			if waited := time.Since(startedAt); waited >= 25*time.Millisecond {
				logger.L().Info("codex subagent concurrency queued",
					zap.Int64("account_id", account.ID),
					zap.String("scope_hash", truncateCodexFingerprintHash(ids.sessionScopeHash)),
					zap.Int64("epoch", ids.sessionEpoch),
					zap.Duration("wait", waited),
				)
			}
			if result.ReleaseFunc == nil {
				return func() {}, nil
			}
			return result.ReleaseFunc, nil
		}
		if !time.Now().Before(deadline) {
			logger.L().Warn("codex subagent concurrency queue timeout",
				zap.Int64("account_id", account.ID),
				zap.String("scope_hash", truncateCodexFingerprintHash(ids.sessionScopeHash)),
				zap.Int64("epoch", ids.sessionEpoch),
				zap.Int("limit", limit),
				zap.Duration("wait", time.Since(startedAt)),
			)
			return nil, newCodexSubagentConcurrencyLimitError()
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, newCodexSubagentConcurrencyLimitError()
		case <-timer.C:
		}
	}
}

// TryAcquireCodexSessionAdmissionSlots 按“子代理 Session 槽位 → 账号总槽位”顺序原子组合准入。
// 子代理槽满时返回未获取，让单 Session 队列继续等待；普通请求只获取账号总槽位。
func (s *OpenAIGatewayService) TryAcquireCodexSessionAdmissionSlots(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	maxAccountConcurrency int,
	tryAccount func(context.Context, int64, int) (func(), bool, error),
) (func(), bool, error) {
	if account == nil || tryAccount == nil {
		return nil, false, errors.New("openai account admission slot dependencies unavailable")
	}
	ids := stagedCodexFingerprintIDsForAccount(c, account)
	limit := account.GetCodexSubagentMaxInflightPerSession()
	if ids == nil || !ids.isSubagent || limit <= 0 {
		return tryAccount(ctx, account.ID, maxAccountConcurrency)
	}
	if s == nil || s.concurrencyService == nil {
		return nil, false, errors.New("codex subagent concurrency cache is unavailable")
	}
	subagentResult, err := s.concurrencyService.AcquireCodexSubagentSlot(
		ctx, account.ID, ids.sessionScopeHash, ids.sessionEpoch, limit,
	)
	if err != nil {
		return nil, false, err
	}
	if subagentResult == nil || !subagentResult.Acquired {
		return nil, false, nil
	}
	releaseSubagent := subagentResult.ReleaseFunc
	if releaseSubagent == nil {
		releaseSubagent = func() {}
	}
	releaseAccount, acquired, err := tryAccount(ctx, account.ID, maxAccountConcurrency)
	if err != nil || !acquired {
		releaseSubagent()
		return nil, acquired, err
	}
	if c != nil && c.Request != nil {
		leaseState := &atomic.Bool{}
		leaseState.Store(true)
		lease := codexSubagentAdmissionLease{
			accountID: account.ID, scopeHash: ids.sessionScopeHash, epoch: ids.sessionEpoch, active: leaseState,
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), codexSubagentAdmissionLeaseContextKey{}, lease))
		return func() {
			leaseState.Store(false)
			if releaseAccount != nil {
				releaseAccount()
			}
			releaseSubagent()
		}, true, nil
	}
	return func() {
		if releaseAccount != nil {
			releaseAccount()
		}
		releaseSubagent()
	}, true, nil
}

func (s *OpenAIGatewayService) codexSubagentQueueMaxWait(ctx context.Context) time.Duration {
	if s != nil && s.codexSubagentQueueMaxWaitForTest > 0 {
		return s.codexSubagentQueueMaxWaitForTest
	}
	cfg := DefaultOpenAIAccountAdmissionConfig()
	if s != nil && s.settingService != nil {
		if current, err := s.settingService.GetOpenAIAccountAdmissionConfig(ctx); err == nil {
			cfg = current
		}
	}
	if cfg.MaxWaitSeconds < 1 {
		return 45 * time.Second
	}
	return time.Duration(cfg.MaxWaitSeconds) * time.Second
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
