package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"

	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const upstreamProtocolModeOpenAICPAH2 = "openai_cpa_h2"

// openAITransportManager 表示一代 OpenAI CPA Transport 注册表。
// manager 轮换后旧实例只允许已有请求自然排空，不再接受新连接或新作用域。
type openAITransportManager struct {
	id         string
	generation uint64
	signature  string

	draining atomic.Bool
	mu       sync.RWMutex
	clients  map[string]*upstreamClientEntry
}

func (s *httpUpstreamService) openAICPATransportEnabled() bool {
	return s.resolveOpenAIHTTP2Settings().enabled
}

func newOpenAITransportManager(id string, generation uint64, signature string) *openAITransportManager {
	return &openAITransportManager{
		id:         id,
		generation: generation,
		signature:  signature,
		clients:    make(map[string]*upstreamClientEntry),
	}
}

// drain 将 manager 标记为排空并关闭空闲连接。
// 活跃请求仍由原 entry 持有，响应体关闭后自然结束，不会被迁移到新 manager。
func (m *openAITransportManager) drain() {
	if m == nil {
		return
	}
	m.draining.Store(true)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.clients {
		if entry != nil && entry.client != nil {
			entry.client.CloseIdleConnections()
		}
	}
	// 清空注册表后，旧 entry 仍可被正在执行的请求使用，但无法被新请求命中。
	m.clients = make(map[string]*upstreamClientEntry)
}

func (s *httpUpstreamService) openAICPAManagerSignature() string {
	settings := s.resolveOpenAIHTTP2Settings()
	return fmt.Sprintf("%s|h2:%t|fallback:%t|threshold:%d|window:%s|ttl:%s",
		tlsfingerprint.CPAChromeProfileVersion,
		settings.enabled,
		settings.allowProxyFallbackToHTTP1,
		settings.fallbackErrorThreshold,
		settings.fallbackWindow,
		settings.fallbackTTL,
	)
}

func (s *httpUpstreamService) ensureOpenAICPAManager() *openAITransportManager {
	signature := s.openAICPAManagerSignature()
	s.openAICPAManagerMu.Lock()
	defer s.openAICPAManagerMu.Unlock()

	if current := s.openAICPAManager; current != nil &&
		!current.draining.Load() && current.signature == signature {
		return current
	}
	if current := s.openAICPAManager; current != nil {
		current.drain()
	}
	generation := s.openAICPAManagerGeneration.Add(1)
	manager := newOpenAITransportManager(
		fmt.Sprintf("openai-cpa-%d", generation),
		generation,
		signature,
	)
	s.openAICPAManager = manager
	return manager
}

func openAITransportScopeFingerprint(scope service.OpenAITransportScope, proxyKey, poolKey string) string {
	// 只保存不可逆摘要，避免把 CredentialChainID/InstallationID 写入缓存键日志。
	raw := fmt.Sprintf("%d|%s|%s|%d|%d|%d|%d|%s|%s|%s|%s",
		scope.AccountID,
		scope.Persona,
		scope.PersonaVersion,
		scope.SlotID,
		scope.SessionEpoch,
		scope.SlotGeneration,
		scope.SlotSetGeneration,
		scope.CredentialChainID,
		scope.InstallationID,
		proxyKey,
		poolKey,
	)
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *httpUpstreamService) doOpenAICPA(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, scope service.OpenAITransportScope) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("request url is nil")
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		// CPA Chrome TLS only applies to encrypted OpenAI OAuth requests.
		return s.Do(req, proxyURL, accountID, accountConcurrency)
	}
	if err := s.validateRequestHost(req); err != nil {
		return nil, err
	}

	proxyKey, parsedProxy, err := normalizeProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	isolation := s.getIsolationMode()
	settings := s.resolvePoolSettings(isolation, accountConcurrency)
	settings = s.applyProfilePoolSettings(settings, service.HTTPUpstreamProfileOpenAI)
	poolKey := buildPoolKey(settings, upstreamProtocolModeOpenAICPAH2) + "|profile:" + tlsfingerprint.CPAChromeProfileVersion
	scopeFingerprint := openAITransportScopeFingerprint(scope, proxyKey, poolKey)
	manager := s.ensureOpenAICPAManager()
	entry, err := s.acquireOpenAICPAClient(manager, scopeFingerprint, proxyKey, parsedProxy, poolKey, settings, accountID, accountConcurrency, scope)
	if err != nil {
		return nil, err
	}

	client := httpClientForUpstreamRequest(entry.client, req)
	client = httpClientWithGrokAccessDeniedFallback(client)
	resp, err := servertiming.Do(client, req)
	if err != nil {
		atomic.AddInt64(&entry.inFlight, -1)
		atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
		return nil, err
	}
	decompressResponseBody(resp)
	resp.Body = wrapTrackedBody(resp.Body, func() {
		atomic.AddInt64(&entry.inFlight, -1)
		atomic.StoreInt64(&entry.lastUsed, time.Now().UnixNano())
	})
	return resp, nil
}

func (s *httpUpstreamService) acquireOpenAICPAClient(
	manager *openAITransportManager,
	scopeFingerprint, proxyKey string,
	parsedProxy *url.URL,
	poolKey string,
	settings poolSettings,
	accountID int64,
	accountConcurrency int,
	scope service.OpenAITransportScope,
) (*upstreamClientEntry, error) {
	if manager == nil || manager.draining.Load() {
		return nil, errors.New("OpenAI CPA transport manager is draining")
	}
	now := time.Now()
	nowUnix := now.UnixNano()
	cacheKey := "cpa:" + scopeFingerprint

	manager.mu.RLock()
	if entry := manager.clients[cacheKey]; openAICPACanReuseEntry(entry, manager, scopeFingerprint, proxyKey, poolKey) {
		atomic.StoreInt64(&entry.lastUsed, nowUnix)
		atomic.AddInt64(&entry.inFlight, 1)
		manager.mu.RUnlock()
		return entry, nil
	}
	manager.mu.RUnlock()

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.draining.Load() {
		return nil, errors.New("OpenAI CPA transport manager is draining")
	}
	if entry := manager.clients[cacheKey]; openAICPACanReuseEntry(entry, manager, scopeFingerprint, proxyKey, poolKey) {
		atomic.StoreInt64(&entry.lastUsed, nowUnix)
		atomic.AddInt64(&entry.inFlight, 1)
		return entry, nil
	}
	if entry := manager.clients[cacheKey]; entry != nil {
		if entry.client != nil {
			entry.client.CloseIdleConnections()
		}
		delete(manager.clients, cacheKey)
	}

	// CPA manager 有独立的上限，避免完整作用域摘要绕过全局连接池保护。
	if s.maxUpstreamClients() > 0 {
		s.evictCPAIdleLocked(manager, now)
		if len(manager.clients) >= s.maxUpstreamClients() && !evictOldestCPAIdleLocked(manager) {
			return nil, errUpstreamClientLimitReached
		}
	}

	sessionCache := tlsfingerprint.NewCPAChromeSessionCache()
	dialer := tlsfingerprint.NewCPAChromeDialer(parsedProxy, sessionCache, true)
	transport := &http2.Transport{
		DialTLSContext:             dialer.DialTLSContext,
		IdleConnTimeout:            settings.idleConnTimeout,
		ReadIdleTimeout:            openAIHTTP2ReadIdleTimeout,
		PingTimeout:                openAIHTTP2PingTimeout,
		StrictMaxConcurrentStreams: false,
	}
	client := &http.Client{Transport: transport}
	if s.shouldValidateResolvedIP() {
		client.CheckRedirect = s.redirectChecker
	}
	entry := &upstreamClientEntry{
		client:                  client,
		proxyKey:                proxyKey,
		poolKey:                 poolKey,
		protocolMode:            upstreamProtocolModeOpenAICPAH2,
		transportManagerID:      manager.id,
		transportGeneration:     manager.generation,
		scopeFingerprint:        scopeFingerprint,
		transportProfileVersion: tlsfingerprint.CPAChromeProfileVersion,
	}
	atomic.StoreInt64(&entry.lastUsed, nowUnix)
	atomic.StoreInt64(&entry.inFlight, 1)
	manager.clients[cacheKey] = entry
	slog.Debug("openai_cpa_transport_created",
		"account_id", accountID,
		"persona", scope.Persona,
		"slot_id", scope.SlotID,
		"manager_generation", manager.generation,
		"scope_fingerprint", scopeFingerprint[:12],
		"account_concurrency", accountConcurrency,
	)
	return entry, nil
}

func openAICPACanReuseEntry(entry *upstreamClientEntry, manager *openAITransportManager, scopeFingerprint, proxyKey, poolKey string) bool {
	return entry != nil && manager != nil &&
		entry.transportManagerID == manager.id &&
		entry.transportGeneration == manager.generation &&
		entry.scopeFingerprint == scopeFingerprint &&
		entry.transportProfileVersion == tlsfingerprint.CPAChromeProfileVersion &&
		entry.proxyKey == proxyKey &&
		entry.poolKey == poolKey &&
		entry.protocolMode == upstreamProtocolModeOpenAICPAH2
}

func (s *httpUpstreamService) evictCPAIdleLocked(manager *openAITransportManager, now time.Time) {
	ttl := s.clientIdleTTL()
	if ttl <= 0 {
		return
	}
	cutoff := now.Add(-ttl).UnixNano()
	for key, entry := range manager.clients {
		if atomic.LoadInt64(&entry.inFlight) != 0 {
			continue
		}
		if atomic.LoadInt64(&entry.lastUsed) <= cutoff {
			if entry.client != nil {
				entry.client.CloseIdleConnections()
			}
			delete(manager.clients, key)
		}
	}
}

func evictOldestCPAIdleLocked(manager *openAITransportManager) bool {
	var oldestKey string
	var oldestEntry *upstreamClientEntry
	var oldestTime int64
	for key, entry := range manager.clients {
		if atomic.LoadInt64(&entry.inFlight) != 0 {
			continue
		}
		lastUsed := atomic.LoadInt64(&entry.lastUsed)
		if oldestEntry == nil || lastUsed < oldestTime {
			oldestKey = key
			oldestEntry = entry
			oldestTime = lastUsed
		}
	}
	if oldestEntry == nil {
		return false
	}
	if oldestEntry.client != nil {
		oldestEntry.client.CloseIdleConnections()
	}
	delete(manager.clients, oldestKey)
	return true
}
