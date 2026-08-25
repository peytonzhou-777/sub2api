package service

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const codexLifecyclePrefetchMaxEntries = 1024

type codexLifecycleQuotaPrefetcher interface {
	QueryUsage(ctx context.Context, accountID int64) (*OpenAIQuotaUsage, error)
}

type codexLifecyclePrefetchEntry struct {
	lastSeen time.Time
	inFlight bool
}

type codexLifecyclePrefetchState struct {
	mu      sync.Mutex
	entries map[string]codexLifecyclePrefetchEntry
}

type codexLifecyclePrefetchRuntime struct {
	mu         sync.RWMutex
	state      codexLifecyclePrefetchState
	prefetcher codexLifecycleQuotaPrefetcher
	delay      func(string) time.Duration
}

// tryBegin 仅在新生命周期或连续空闲超过门槛时放行一次后台预取。
func (s *codexLifecyclePrefetchState) tryBegin(key string, now time.Time, idleGate time.Duration) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	if idleGate <= 0 {
		idleGate = time.Duration(config.CodexFingerprintIdleGateMinutesDefault) * time.Minute
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]codexLifecyclePrefetchEntry)
	}
	entry, exists := s.entries[key]
	shouldStart := !exists || (!entry.inFlight && now.Sub(entry.lastSeen) >= idleGate)
	entry.lastSeen = now
	if shouldStart {
		entry.inFlight = true
	}
	s.entries[key] = entry
	if !exists && len(s.entries) > codexLifecyclePrefetchMaxEntries {
		s.pruneOldestLocked(key)
	}
	return shouldStart
}

func (s *codexLifecyclePrefetchState) finish(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key]
	if !exists {
		return
	}
	entry.inFlight = false
	s.entries[key] = entry
}

func (s *codexLifecyclePrefetchState) pruneOldestLocked(currentKey string) {
	oldestKey := ""
	var oldest time.Time
	for key, entry := range s.entries {
		if key == currentKey || entry.inFlight {
			continue
		}
		if oldestKey == "" || entry.lastSeen.Before(oldest) {
			oldestKey = key
			oldest = entry.lastSeen
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}

// SetCodexLifecycleQuotaPrefetcher 注入账号级额度预取器，不参与请求成功判定和调度反馈。
func (s *OpenAIGatewayService) SetCodexLifecycleQuotaPrefetcher(prefetcher codexLifecycleQuotaPrefetcher) {
	if s == nil {
		return
	}
	if prefetcher == nil {
		s.codexLifecyclePrefetch.Store(nil)
		return
	}
	runtime := codexLifecyclePrefetchRuntimeFor(s)
	runtime.mu.Lock()
	runtime.prefetcher = prefetcher
	runtime.mu.Unlock()
}

// maybeStartCodexLifecyclePrefetch 按上游账号、Session scope 与 epoch 合并 CLI 启动侧带请求。
func (s *OpenAIGatewayService) maybeStartCodexLifecyclePrefetch(ctx context.Context, c *gin.Context, account *Account, ids *codexFingerprintIDs) {
	if s == nil || account == nil || ids == nil || !account.IsOpenAIOAuth() {
		return
	}
	runtime := s.codexLifecyclePrefetch.Load()
	if runtime == nil {
		return
	}
	runtime.mu.RLock()
	prefetcher := runtime.prefetcher
	delayOverride := runtime.delay
	runtime.mu.RUnlock()
	if prefetcher == nil {
		return
	}
	scopeHash := strings.TrimSpace(ids.sessionScopeHash)
	if scopeHash == "" || ids.sessionEpoch <= 0 || resolveCodexFingerprintStableUserScope(c) == "api-key:0" {
		return
	}

	credentialAccountID := account.ID
	if account.ParentAccountID != nil && *account.ParentAccountID > 0 {
		credentialAccountID = *account.ParentAccountID
	}
	key := fmt.Sprintf("%d:%s:%d:%d", credentialAccountID, scopeHash, ids.sessionEpoch, ids.sessionSlot)
	if ctx == nil {
		ctx = context.Background()
	}
	idleGateMinutes := s.codexFingerprintEpochPolicy(ctx).IdleGateMinutes
	idleGate := time.Duration(idleGateMinutes) * time.Minute
	if !runtime.state.tryBegin(key, time.Now(), idleGate) {
		return
	}

	delay := codexLifecyclePrefetchDelay(key)
	if delayOverride != nil {
		delay = delayOverride(key)
	}
	queryAccountID := account.ID
	epoch := ids.sessionEpoch
	sessionSlot := ids.sessionSlot
	go func() {
		defer runtime.state.finish(key)
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			<-timer.C
		}
		ctx, cancel := context.WithTimeout(context.Background(), openaiQuotaUpstreamTimeout+5*time.Second)
		defer cancel()
		if _, err := prefetcher.QueryUsage(ctx, queryAccountID); err != nil {
			logger.L().Debug("Codex 生命周期额度预取失败",
				zap.Int64("account_id", credentialAccountID),
				zap.String("scope_hash", truncateCodexFingerprintHash(scopeHash)),
				zap.Int64("epoch", epoch),
				zap.Int("session_slot", sessionSlot),
				zap.Error(err),
			)
			return
		}
		logger.L().Debug("Codex 生命周期额度预取完成",
			zap.Int64("account_id", credentialAccountID),
			zap.String("scope_hash", truncateCodexFingerprintHash(scopeHash)),
			zap.Int64("epoch", epoch),
			zap.Int("session_slot", sessionSlot),
		)
	}()
}

func codexLifecyclePrefetchRuntimeFor(s *OpenAIGatewayService) *codexLifecyclePrefetchRuntime {
	if runtime := s.codexLifecyclePrefetch.Load(); runtime != nil {
		return runtime
	}
	runtime := &codexLifecyclePrefetchRuntime{}
	if s.codexLifecyclePrefetch.CompareAndSwap(nil, runtime) {
		return runtime
	}
	if resolved := s.codexLifecyclePrefetch.Load(); resolved != nil {
		return resolved
	}
	return runtime
}

func (s *OpenAIGatewayService) setCodexLifecyclePrefetchDelayForTest(delay func(string) time.Duration) {
	if s == nil {
		return
	}
	runtime := codexLifecyclePrefetchRuntimeFor(s)
	runtime.mu.Lock()
	runtime.delay = delay
	runtime.mu.Unlock()
}

func codexLifecyclePrefetchDelay(key string) time.Duration {
	digest := sha256.Sum256([]byte(strings.TrimSpace(key)))
	milliseconds := 200 + binary.BigEndian.Uint16(digest[:2])%1000
	return time.Duration(milliseconds) * time.Millisecond
}
