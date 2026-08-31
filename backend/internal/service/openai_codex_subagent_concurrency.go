package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
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
	binding, policy := s.effectiveOpenAIPersonaAdmissionPolicy(ctx, account)
	if err := s.enforceOpenAISubagentDepth(ctx, account, binding, ids, policy.SubagentDepth); err != nil {
		return nil, err
	}
	scopeHash := codexSubagentAdmissionScopeHash(ctx, account, ids)
	if lease, _ := ctx.Value(codexSubagentAdmissionLeaseContextKey{}).(codexSubagentAdmissionLease); lease.accountID == account.ID &&
		lease.scopeHash == scopeHash && lease.epoch == ids.sessionEpoch && lease.active != nil && lease.active.Load() {
		return func() {}, nil
	}
	limit := policy.MaxSubagents
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
			ctx, account.ID, scopeHash, ids.sessionEpoch, limit,
		)
		if err != nil {
			logger.L().Error("codex subagent concurrency acquire failed",
				zap.Int64("account_id", account.ID),
				zap.String("scope_hash", truncateCodexFingerprintHash(scopeHash)),
				zap.Int64("epoch", ids.sessionEpoch),
				zap.Error(err),
			)
			return nil, newCodexSubagentConcurrencyStoreError()
		}
		if result != nil && result.Acquired {
			if waited := time.Since(startedAt); waited >= 25*time.Millisecond {
				logger.L().Info("codex subagent concurrency queued",
					zap.Int64("account_id", account.ID),
					zap.String("scope_hash", truncateCodexFingerprintHash(scopeHash)),
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
				zap.String("scope_hash", truncateCodexFingerprintHash(scopeHash)),
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
	maxPersonaConcurrency int,
	tryAccount func(context.Context, int64, int) (func(), bool, error),
) (func(), bool, error) {
	if account == nil || tryAccount == nil {
		return nil, false, errors.New("openai account admission slot dependencies unavailable")
	}
	ids := stagedCodexFingerprintIDsForAccount(c, account)
	binding, policy := s.effectiveOpenAIPersonaAdmissionPolicy(ctx, account)
	if maxPersonaConcurrency > 0 {
		policy.MaxConcurrency = maxPersonaConcurrency
	}
	if s == nil || s.concurrencyService == nil {
		return nil, false, errors.New("openai persona concurrency cache is unavailable")
	}
	if err := s.enforceOpenAISubagentDepth(ctx, account, binding, ids, policy.SubagentDepth); err != nil {
		return nil, false, err
	}

	releasePersona := func() {}
	if binding.EffectiveMappingVersion() >= SessionPersonaScopeVersionV3 {
		personaResult, err := s.concurrencyService.AcquireOpenAIPersonaSlot(
			ctx, account.ID, binding.PersonaID, binding.SlotID, policy.MaxConcurrency,
		)
		if err != nil || personaResult == nil || !personaResult.Acquired {
			return nil, false, err
		}
		if personaResult.ReleaseFunc != nil {
			releasePersona = personaResult.ReleaseFunc
		}
	}

	limit := policy.MaxSubagents
	if ids == nil || !ids.isSubagent || limit <= 0 {
		releaseAccount, acquired, acquireErr := tryAccount(ctx, account.ID, maxAccountConcurrency)
		if acquireErr != nil || !acquired {
			releasePersona()
			return nil, acquired, acquireErr
		}
		return func() {
			if releaseAccount != nil {
				releaseAccount()
			}
			releasePersona()
		}, true, nil
	}
	scopeHash := codexSubagentAdmissionScopeHash(ctx, account, ids)
	subagentResult, err := s.concurrencyService.AcquireCodexSubagentSlot(
		ctx, account.ID, scopeHash, ids.sessionEpoch, limit,
	)
	if err != nil {
		releasePersona()
		return nil, false, err
	}
	if subagentResult == nil || !subagentResult.Acquired {
		releasePersona()
		return nil, false, nil
	}
	releaseSubagent := subagentResult.ReleaseFunc
	if releaseSubagent == nil {
		releaseSubagent = func() {}
	}
	releaseAccount, acquired, err := tryAccount(ctx, account.ID, maxAccountConcurrency)
	if err != nil || !acquired {
		releaseSubagent()
		releasePersona()
		return nil, acquired, err
	}
	if c != nil && c.Request != nil {
		leaseState := &atomic.Bool{}
		leaseState.Store(true)
		lease := codexSubagentAdmissionLease{
			accountID: account.ID, scopeHash: scopeHash, epoch: ids.sessionEpoch, active: leaseState,
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), codexSubagentAdmissionLeaseContextKey{}, lease))
		return func() {
			leaseState.Store(false)
			if releaseAccount != nil {
				releaseAccount()
			}
			releaseSubagent()
			releasePersona()
		}, true, nil
	}
	return func() {
		if releaseAccount != nil {
			releaseAccount()
		}
		releaseSubagent()
		releasePersona()
	}, true, nil
}

func (s *OpenAIGatewayService) effectiveOpenAIPersonaAdmissionPolicy(ctx context.Context, account *Account) (SessionPersonaSlotBinding, OpenAIPersonaAdmissionPolicy) {
	binding, ok := SessionPersonaBindingFromContext(ctx)
	if !ok || binding.AccountID != 0 && account != nil && binding.AccountID != account.ID {
		persona, _ := ResolveDefaultSessionPersona(0)
		binding = SessionPersonaSlotBinding{AccountID: accountIDForSubagentScope(account, SessionPersonaSlotBinding{}), SlotID: 0, PersonaID: persona.ID, Persona: persona}
	}
	cfg := DefaultOpenAIAccountAdmissionConfig()
	if s != nil && s.settingService != nil {
		if current, err := s.settingService.GetOpenAIAccountAdmissionConfig(ctx); err == nil {
			cfg = current
		}
	}
	globalWS := 0
	if s != nil && s.cfg != nil {
		globalWS = s.cfg.Gateway.OpenAIWS.MaxConnsPerAccount
	}
	return binding, cfg.EffectiveOpenAIPersonaPolicyForAccount(account, binding.PersonaID, globalWS)
}

// EffectiveOpenAIPersonaAdmissionPolicy 返回 handler 可直接执行的 Persona 策略快照。
func (s *OpenAIGatewayService) EffectiveOpenAIPersonaAdmissionPolicy(ctx context.Context, account *Account, binding SessionPersonaSlotBinding) OpenAIPersonaAdmissionPolicy {
	if binding.Valid() {
		ctx = ContextWithSessionPersonaBinding(ctx, binding)
	}
	_, policy := s.effectiveOpenAIPersonaAdmissionPolicy(ctx, account)
	return policy
}

// enforceOpenAISubagentDepth 通过持久化线程深度约束父子代理层级。
func (s *OpenAIGatewayService) enforceOpenAISubagentDepth(ctx context.Context, account *Account, binding SessionPersonaSlotBinding, ids *codexFingerprintIDs, maxDepth int) error {
	if maxDepth <= 0 || account == nil || ids == nil || strings.TrimSpace(ids.threadID) == "" {
		return nil
	}
	if s == nil || s.concurrencyService == nil || len(ids.sessionScopeHash) != 64 || ids.sessionEpoch <= 0 {
		return newCodexSubagentConcurrencyStoreError()
	}
	threadHash := hashOpenAISubagentThread(ids.threadID)
	depth := 0
	if ids.isSubagent {
		parentID := strings.TrimSpace(ids.parentThreadID)
		if parentID == "" {
			parentID = strings.TrimSpace(ids.forkedThreadID)
		}
		if parentID != "" {
			parentDepth, found, err := s.concurrencyService.GetOpenAISubagentDepth(ctx, account.ID, binding.PersonaID, binding.SlotID, ids.sessionScopeHash, ids.sessionEpoch, hashOpenAISubagentThread(parentID))
			if err != nil {
				return newCodexSubagentConcurrencyStoreError()
			}
			if found {
				depth = parentDepth
			}
		}
		depth++
		if depth > maxDepth {
			return newCodexSubagentDepthLimitError(maxDepth)
		}
	}
	if err := s.concurrencyService.SetOpenAISubagentDepth(ctx, account.ID, binding.PersonaID, binding.SlotID, ids.sessionScopeHash, ids.sessionEpoch, threadHash, depth); err != nil {
		return newCodexSubagentConcurrencyStoreError()
	}
	return nil
}

func hashOpenAISubagentThread(threadID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(threadID)))
	return hex.EncodeToString(digest[:])
}

func isOpenCodePersonaBindingForAccount(ctx context.Context, account *Account) bool {
	if ctx == nil {
		return false
	}
	binding, ok := SessionPersonaBindingFromContext(ctx)
	if !ok || !IsOpenCodePersona(binding) {
		return false
	}
	return binding.AccountID == 0 || account == nil || binding.AccountID == account.ID
}

// codexSubagentAdmissionScopeHash keeps the legacy fingerprint scope as the
// base while adding Persona/slot generations when a binding is available. This
// prevents Codex and OpenCode subagent limits from consuming one another's
// capacity without changing unbound legacy requests.
func codexSubagentAdmissionScopeHash(ctx context.Context, account *Account, ids *codexFingerprintIDs) string {
	if ids == nil {
		return ""
	}
	base := strings.TrimSpace(ids.sessionScopeHash)
	if binding, ok := SessionPersonaBindingFromContext(ctx); ok &&
		(binding.AccountID == 0 || account == nil || binding.AccountID == account.ID) &&
		strings.TrimSpace(string(binding.PersonaID)) != "" {
		seed := fmt.Sprintf("persona-subagent:v1|%d|%s|%d|%d|%d|%d|%s",
			accountIDForSubagentScope(account, binding), binding.PersonaID, binding.SlotID,
			binding.SlotGeneration, binding.SlotSetGeneration, binding.SessionEpoch, base)
		digest := sha256.Sum256([]byte(seed))
		return hex.EncodeToString(digest[:])
	}
	return base
}

func accountIDForSubagentScope(account *Account, binding SessionPersonaSlotBinding) int64 {
	if account != nil {
		return account.ID
	}
	return binding.AccountID
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

func newCodexSubagentDepthLimitError(limit int) *UpstreamFailoverError {
	err := newCodexSubagentConcurrencyLimitError()
	err.ClientMessage = fmt.Sprintf("Subagent depth exceeds the Persona limit of %d", limit)
	err.ResponseBody, _ = json.Marshal(map[string]any{"error": map[string]any{
		"type": "rate_limit_error", "code": "openai_subagent_depth_limit", "message": err.ClientMessage,
	}})
	return err
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
