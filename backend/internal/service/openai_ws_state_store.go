package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	openAIWSResponseAccountCachePrefix  = "openai:response:"
	openAIWSTurnStateAccountCachePrefix = "openai:turn-state:"
	openAIHTTPResponseOwnerUserPrefix   = "openai:http-response-owner:user:"
	openAIHTTPResponseOwnerKeyPrefix    = "openai:http-response-owner:key:"
	openAIWSStateStoreCleanupInterval   = time.Minute
	openAIWSStateStoreCleanupMaxPerMap  = 512
	openAIWSStateStoreMaxEntriesPerMap  = 65536
	openAIWSStateStoreRedisTimeout      = 3 * time.Second
)

type openAIWSAccountBinding struct {
	accountID int64
	expiresAt time.Time
}

type openAIWSConnBinding struct {
	connID    string
	target    openAIWSConnectionTarget
	expiresAt time.Time
}

type openAIWSTurnStateBinding struct {
	accountID int64
	expiresAt time.Time
}

type openAIWSSessionTurnStateBinding struct {
	turnState string
	expiresAt time.Time
}

type openAIHTTPResponseOwnerBinding struct {
	userID    int64
	apiKeyID  int64
	expiresAt time.Time
}

type openAIWSSessionConnBinding struct {
	connID    string
	target    openAIWSConnectionTarget
	expiresAt time.Time
}

// openAIWSConnectionTarget 描述连接可复用的完整上游目标。
// 任一字段变化都必须按未命中处理，避免旧账号或旧 epoch 的连接被透明复用。
type openAIWSConnectionTarget struct {
	accountID              int64
	handshakeCompatibility openAIWSHandshakeCompatibilityKey
	transport              OpenAIUpstreamTransport
	wsURL                  string
	proxyURL               string
}

func newOpenAIWSConnectionTarget(account *Account, transport OpenAIUpstreamTransport, wsURL string, headers http.Header, topologyScopes ...string) openAIWSConnectionTarget {
	target := openAIWSConnectionTarget{transport: transport, wsURL: strings.TrimSpace(wsURL)}
	if account == nil {
		return target
	}
	target.accountID = account.ID
	target.handshakeCompatibility = normalizeOpenAIWSHandshakeCompatibility(headers, topologyScopes...)
	if account.ProxyID != nil && account.Proxy != nil {
		target.proxyURL = strings.TrimSpace(account.Proxy.URL())
	}
	return target
}

// normalizeOpenAIWSFingerprintScope 对实际握手设备与 Session 标识取摘要，
// 供连接索引和连接池共同判定兼容性，不暴露原始出站标识。
func normalizeOpenAIWSFingerprintScope(headers http.Header) string {
	if headers == nil {
		return ""
	}
	installationID := strings.TrimSpace(headers.Get("x-codex-installation-id"))
	sessionID := strings.TrimSpace(headers.Get("session-id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(headers.Get("session_id"))
	}
	// installation_id 是指纹收敛已实际生效的门控；普通 API Key 客户端自带的
	// session_id 仍沿用原连接复用语义，不能被误当成账号级握手指纹。
	if installationID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("openai-ws-fingerprint-scope:v1\n" + installationID + "\n" + sessionID))
	return hex.EncodeToString(sum[:16])
}

// normalizeOpenAIWSTopologyScope 防止根线程、子线程和兄弟线程复用同一条有状态连接。
func normalizeOpenAIWSTopologyScope(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return normalizeOpenAIWSTopologyScopeValues(
		strings.TrimSpace(headers.Get("x-codex-installation-id")),
		firstNonEmptyHeader(headers, "thread-id", "thread_id"),
		strings.TrimSpace(headers.Get("x-codex-parent-thread-id")),
		strings.TrimSpace(headers.Get("x-openai-subagent")),
	)
}

func normalizeOpenAIWSTopologyScopeValues(installationID, threadID, parentThreadID, subagent string) string {
	if strings.TrimSpace(installationID) == "" {
		return ""
	}
	threadID = strings.TrimSpace(threadID)
	parentThreadID = strings.TrimSpace(parentThreadID)
	subagent = strings.TrimSpace(subagent)
	if threadID == "" && parentThreadID == "" && subagent == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("openai-ws-topology-scope:v1\n" + threadID + "\n" + parentThreadID + "\n" + subagent))
	return hex.EncodeToString(sum[:16])
}

func (t openAIWSConnectionTarget) valid() bool {
	return t.accountID > 0 && t.transport != "" && t.wsURL != ""
}

func (t openAIWSConnectionTarget) matches(other openAIWSConnectionTarget) bool {
	return t.valid() && other.valid() && t == other
}

// OpenAIWSStateStore 管理 WSv2 的粘连状态。
// - response_id -> account_id 用于续链路由
// - response_id -> conn_id 用于连接内上下文复用
//
// response_id -> account_id 优先走 GatewayCache（Redis），同时维护本地热缓存。
// response_id -> conn_id 仅在本进程内有效。
type OpenAIWSStateStore interface {
	BindResponseAccount(ctx context.Context, groupID int64, responseID string, accountID int64, ttl time.Duration) error
	GetResponseAccount(ctx context.Context, groupID int64, responseID string) (int64, error)
	DeleteResponseAccount(ctx context.Context, groupID int64, responseID string) error
	BindHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64, ttl time.Duration) error
	GetHTTPResponseOwner(ctx context.Context, groupID int64, responseID string) (userID, apiKeyID int64, found bool, err error)

	BindResponseConn(responseID, connID string, ttl time.Duration)
	GetResponseConn(responseID string) (string, bool)
	DeleteResponseConn(responseID string)

	BindTurnStateAccount(ctx context.Context, groupID, apiKeyID int64, sessionHash, turnState string, accountID int64, ttl time.Duration) error
	GetTurnStateAccount(ctx context.Context, groupID, apiKeyID int64, sessionHash, turnState string) (int64, error)
	BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration)
	GetSessionTurnState(groupID int64, sessionHash string) (string, bool)
	DeleteSessionTurnState(groupID int64, sessionHash string)

	BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration)
	GetSessionConn(groupID int64, sessionHash string) (string, bool)
	DeleteSessionConn(groupID int64, sessionHash string)
}

// openAIWSTargetStateStore 为连接索引增加 expected-target 校验。
// 保留基础接口是为了兼容测试桩；生产读取只接受实现本接口的存储。
type openAIWSTargetStateStore interface {
	BindResponseConnForTarget(responseID string, target openAIWSConnectionTarget, connID string, ttl time.Duration)
	GetResponseConnForTarget(responseID string, expected openAIWSConnectionTarget) (string, bool)
	BindSessionConnForTarget(groupID int64, sessionHash string, target openAIWSConnectionTarget, connID string, ttl time.Duration)
	GetSessionConnForTarget(groupID int64, sessionHash string, expected openAIWSConnectionTarget) (string, bool)
}

type openAIWSConnBindingCleanupStore interface {
	DeleteConnBindings(connID string) int
}

type defaultOpenAIWSStateStore struct {
	cache             GatewayCache
	replayCheckpoints *openAIWSReplayCheckpointCache

	responseToAccountMu  sync.RWMutex
	responseToAccount    map[string]openAIWSAccountBinding
	responseOwnerMu      sync.RWMutex
	responseOwners       map[string]openAIHTTPResponseOwnerBinding
	responseToConnMu     sync.RWMutex
	responseToConn       map[string]openAIWSConnBinding
	turnStateToAccountMu sync.RWMutex
	turnStateToAccount   map[string]openAIWSTurnStateBinding
	sessionToTurnStateMu sync.RWMutex
	sessionToTurnState   map[string]openAIWSSessionTurnStateBinding
	sessionToConnMu      sync.RWMutex
	sessionToConn        map[string]openAIWSSessionConnBinding

	lastCleanupUnixNano atomic.Int64
}

// NewOpenAIWSStateStore 创建默认 WS 状态存储。
func NewOpenAIWSStateStore(cache GatewayCache) OpenAIWSStateStore {
	store := &defaultOpenAIWSStateStore{
		cache:              cache,
		replayCheckpoints:  newOpenAIWSReplayCheckpointCache(),
		responseToAccount:  make(map[string]openAIWSAccountBinding, 256),
		responseOwners:     make(map[string]openAIHTTPResponseOwnerBinding, 256),
		responseToConn:     make(map[string]openAIWSConnBinding, 256),
		turnStateToAccount: make(map[string]openAIWSTurnStateBinding, 256),
		sessionToTurnState: make(map[string]openAIWSSessionTurnStateBinding, 256),
		sessionToConn:      make(map[string]openAIWSSessionConnBinding, 256),
	}
	store.lastCleanupUnixNano.Store(time.Now().UnixNano())
	return store
}

// BindHTTPResponseOwner 持久化 HTTP Responses 续链的用户与 API key 归属。
// 归属与账号连接状态分离，防止跨用户/跨 key 复用 response_id。
func (s *defaultOpenAIWSStateStore) BindHTTPResponseOwner(ctx context.Context, groupID int64, responseID string, userID, apiKeyID int64, ttl time.Duration) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || userID <= 0 || apiKeyID <= 0 {
		return nil
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()
	mapKey := openAIWSResponseAccountMapKey(groupID, id)
	s.responseOwnerMu.Lock()
	ensureBindingCapacity(s.responseOwners, mapKey, openAIWSStateStoreMaxEntriesPerMap)
	s.responseOwners[mapKey] = openAIHTTPResponseOwnerBinding{userID: userID, apiKeyID: apiKeyID, expiresAt: time.Now().Add(ttl)}
	s.responseOwnerMu.Unlock()
	if s.cache == nil {
		return nil
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	if err := s.cache.SetSessionAccountID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id), userID, ttl); err != nil {
		return err
	}
	return s.cache.SetSessionAccountID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id), apiKeyID, ttl)
}

// GetHTTPResponseOwner 读取 HTTP Responses 续链归属，缓存不可用时安全降级为未命中。
func (s *defaultOpenAIWSStateStore) GetHTTPResponseOwner(ctx context.Context, groupID int64, responseID string) (int64, int64, bool, error) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return 0, 0, false, nil
	}
	s.maybeCleanup()
	mapKey := openAIWSResponseAccountMapKey(groupID, id)
	now := time.Now()
	s.responseOwnerMu.RLock()
	if binding, ok := s.responseOwners[mapKey]; ok && now.Before(binding.expiresAt) {
		s.responseOwnerMu.RUnlock()
		return binding.userID, binding.apiKeyID, true, nil
	}
	s.responseOwnerMu.RUnlock()
	if s.cache == nil {
		return 0, 0, false, nil
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	userID, err := s.cache.GetSessionAccountID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerUserPrefix, id))
	if err != nil || userID <= 0 {
		return 0, 0, false, err
	}
	apiKeyID, err := s.cache.GetSessionAccountID(cacheCtx, groupID, openAIHTTPResponseOwnerCacheKey(openAIHTTPResponseOwnerKeyPrefix, id))
	if err != nil || apiKeyID <= 0 {
		return 0, 0, false, err
	}
	s.responseOwnerMu.Lock()
	ensureBindingCapacity(s.responseOwners, mapKey, openAIWSStateStoreMaxEntriesPerMap)
	s.responseOwners[mapKey] = openAIHTTPResponseOwnerBinding{userID: userID, apiKeyID: apiKeyID, expiresAt: now.Add(time.Minute)}
	s.responseOwnerMu.Unlock()
	return userID, apiKeyID, true, nil
}

func (s *defaultOpenAIWSStateStore) BindResponseAccount(ctx context.Context, groupID int64, responseID string, accountID int64, ttl time.Duration) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || accountID <= 0 {
		return nil
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	expiresAt := time.Now().Add(ttl)
	mapKey := openAIWSResponseAccountMapKey(groupID, id)
	s.responseToAccountMu.Lock()
	ensureBindingCapacity(s.responseToAccount, mapKey, openAIWSStateStoreMaxEntriesPerMap)
	s.responseToAccount[mapKey] = openAIWSAccountBinding{accountID: accountID, expiresAt: expiresAt}
	s.responseToAccountMu.Unlock()

	if s.cache == nil {
		return nil
	}
	cacheKey := openAIWSResponseAccountCacheKey(id)
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	return s.cache.SetSessionAccountID(cacheCtx, groupID, cacheKey, accountID, ttl)
}

func (s *defaultOpenAIWSStateStore) GetResponseAccount(ctx context.Context, groupID int64, responseID string) (int64, error) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return 0, nil
	}
	s.maybeCleanup()

	now := time.Now()
	mapKey := openAIWSResponseAccountMapKey(groupID, id)
	s.responseToAccountMu.RLock()
	if binding, ok := s.responseToAccount[mapKey]; ok {
		if now.Before(binding.expiresAt) {
			accountID := binding.accountID
			s.responseToAccountMu.RUnlock()
			return accountID, nil
		}
	}
	s.responseToAccountMu.RUnlock()

	if s.cache == nil {
		return 0, nil
	}

	cacheKey := openAIWSResponseAccountCacheKey(id)
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	accountID, err := s.cache.GetSessionAccountID(cacheCtx, groupID, cacheKey)
	if err != nil || accountID <= 0 {
		// 缓存读取失败不阻断主流程，按未命中降级。
		return 0, nil
	}
	return accountID, nil
}

func (s *defaultOpenAIWSStateStore) DeleteResponseAccount(ctx context.Context, groupID int64, responseID string) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return nil
	}
	s.responseToAccountMu.Lock()
	delete(s.responseToAccount, openAIWSResponseAccountMapKey(groupID, id))
	s.responseToAccountMu.Unlock()

	if s.cache == nil {
		return nil
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	return s.cache.DeleteSessionAccountID(cacheCtx, groupID, openAIWSResponseAccountCacheKey(id))
}

func (s *defaultOpenAIWSStateStore) BindResponseConn(responseID, connID string, ttl time.Duration) {
	s.bindResponseConn(responseID, openAIWSConnectionTarget{}, connID, ttl)
}

func (s *defaultOpenAIWSStateStore) BindResponseConnForTarget(responseID string, target openAIWSConnectionTarget, connID string, ttl time.Duration) {
	s.bindResponseConn(responseID, target, connID, ttl)
}

func (s *defaultOpenAIWSStateStore) bindResponseConn(responseID string, target openAIWSConnectionTarget, connID string, ttl time.Duration) {
	id := normalizeOpenAIWSResponseID(responseID)
	conn := strings.TrimSpace(connID)
	if id == "" || conn == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.responseToConnMu.Lock()
	ensureBindingCapacity(s.responseToConn, id, openAIWSStateStoreMaxEntriesPerMap)
	s.responseToConn[id] = openAIWSConnBinding{
		connID:    conn,
		target:    target,
		expiresAt: time.Now().Add(ttl),
	}
	s.responseToConnMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) GetResponseConn(responseID string) (string, bool) {
	return s.getResponseConn(responseID, openAIWSConnectionTarget{}, false)
}

func (s *defaultOpenAIWSStateStore) GetResponseConnForTarget(responseID string, expected openAIWSConnectionTarget) (string, bool) {
	return s.getResponseConn(responseID, expected, true)
}

func (s *defaultOpenAIWSStateStore) getResponseConn(responseID string, expected openAIWSConnectionTarget, validateTarget bool) (string, bool) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return "", false
	}
	s.maybeCleanup()

	now := time.Now()
	s.responseToConnMu.RLock()
	binding, ok := s.responseToConn[id]
	s.responseToConnMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || strings.TrimSpace(binding.connID) == "" ||
		(validateTarget && !binding.target.matches(expected)) {
		return "", false
	}
	return binding.connID, true
}

func (s *defaultOpenAIWSStateStore) DeleteResponseConn(responseID string) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return
	}
	s.responseToConnMu.Lock()
	delete(s.responseToConn, id)
	s.responseToConnMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) BindTurnStateAccount(ctx context.Context, groupID, apiKeyID int64, sessionHash, turnState string, accountID int64, ttl time.Duration) error {
	key := openAIWSTurnStateAccountKey(apiKeyID, sessionHash, turnState)
	if key == "" || accountID <= 0 {
		return nil
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	mapKey := openAIWSTurnStateAccountMapKey(groupID, key)
	s.turnStateToAccountMu.Lock()
	ensureBindingCapacity(s.turnStateToAccount, mapKey, openAIWSStateStoreMaxEntriesPerMap)
	s.turnStateToAccount[mapKey] = openAIWSTurnStateBinding{
		accountID: accountID,
		expiresAt: time.Now().Add(ttl),
	}
	s.turnStateToAccountMu.Unlock()

	if s.cache == nil {
		return nil
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	return s.cache.SetSessionAccountID(cacheCtx, groupID, key, accountID, ttl)
}

func (s *defaultOpenAIWSStateStore) GetTurnStateAccount(ctx context.Context, groupID, apiKeyID int64, sessionHash, turnState string) (int64, error) {
	key := openAIWSTurnStateAccountKey(apiKeyID, sessionHash, turnState)
	if key == "" {
		return 0, nil
	}
	s.maybeCleanup()

	now := time.Now()
	mapKey := openAIWSTurnStateAccountMapKey(groupID, key)
	s.turnStateToAccountMu.RLock()
	binding, ok := s.turnStateToAccount[mapKey]
	s.turnStateToAccountMu.RUnlock()
	if ok && now.Before(binding.expiresAt) && binding.accountID > 0 {
		return binding.accountID, nil
	}
	if s.cache == nil {
		return 0, nil
	}

	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	accountID, err := s.cache.GetSessionAccountID(cacheCtx, groupID, key)
	if err != nil || accountID <= 0 {
		// 溯源缓存不可用时按未知来源处理，安全优先剥离状态。
		return 0, nil
	}
	return accountID, nil
}

// BindSessionTurnState 保存当前会话的最近回合状态，仅在本进程内保留原文。
func (s *defaultOpenAIWSStateStore) BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	turnState = strings.TrimSpace(turnState)
	if key == "" || turnState == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()
	s.sessionToTurnStateMu.Lock()
	ensureBindingCapacity(s.sessionToTurnState, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToTurnState[key] = openAIWSSessionTurnStateBinding{turnState: turnState, expiresAt: time.Now().Add(ttl)}
	s.sessionToTurnStateMu.Unlock()
}

// GetSessionTurnState 读取当前会话的最近回合状态。
func (s *defaultOpenAIWSStateStore) GetSessionTurnState(groupID int64, sessionHash string) (string, bool) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return "", false
	}
	s.maybeCleanup()
	now := time.Now()
	s.sessionToTurnStateMu.RLock()
	binding, ok := s.sessionToTurnState[key]
	s.sessionToTurnStateMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || strings.TrimSpace(binding.turnState) == "" {
		return "", false
	}
	return binding.turnState, true
}

// DeleteSessionTurnState 清理指定会话的回合状态，供会话抢占或失效时使用。
func (s *defaultOpenAIWSStateStore) DeleteSessionTurnState(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	s.sessionToTurnStateMu.Lock()
	delete(s.sessionToTurnState, key)
	s.sessionToTurnStateMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration) {
	s.bindSessionConn(groupID, sessionHash, openAIWSConnectionTarget{}, connID, ttl)
}

func (s *defaultOpenAIWSStateStore) BindSessionConnForTarget(groupID int64, sessionHash string, target openAIWSConnectionTarget, connID string, ttl time.Duration) {
	s.bindSessionConn(groupID, sessionHash, target, connID, ttl)
}

func (s *defaultOpenAIWSStateStore) bindSessionConn(groupID int64, sessionHash string, target openAIWSConnectionTarget, connID string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	conn := strings.TrimSpace(connID)
	if key == "" || conn == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.sessionToConnMu.Lock()
	ensureBindingCapacity(s.sessionToConn, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToConn[key] = openAIWSSessionConnBinding{
		connID:    conn,
		target:    target,
		expiresAt: time.Now().Add(ttl),
	}
	s.sessionToConnMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) GetSessionConn(groupID int64, sessionHash string) (string, bool) {
	return s.getSessionConn(groupID, sessionHash, openAIWSConnectionTarget{}, false)
}

func (s *defaultOpenAIWSStateStore) GetSessionConnForTarget(groupID int64, sessionHash string, expected openAIWSConnectionTarget) (string, bool) {
	return s.getSessionConn(groupID, sessionHash, expected, true)
}

func (s *defaultOpenAIWSStateStore) getSessionConn(groupID int64, sessionHash string, expected openAIWSConnectionTarget, validateTarget bool) (string, bool) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return "", false
	}
	s.maybeCleanup()

	now := time.Now()
	s.sessionToConnMu.RLock()
	binding, ok := s.sessionToConn[key]
	s.sessionToConnMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || strings.TrimSpace(binding.connID) == "" ||
		(validateTarget && !binding.target.matches(expected)) {
		return "", false
	}
	return binding.connID, true
}

func bindOpenAIWSResponseConn(store OpenAIWSStateStore, responseID string, target openAIWSConnectionTarget, connID string, ttl time.Duration) {
	targetStore, ok := store.(openAIWSTargetStateStore)
	if !ok || !target.valid() {
		return
	}
	targetStore.BindResponseConnForTarget(responseID, target, connID, ttl)
}

func getOpenAIWSResponseConn(store OpenAIWSStateStore, responseID string, expected openAIWSConnectionTarget) (string, bool) {
	targetStore, ok := store.(openAIWSTargetStateStore)
	if !ok || !expected.valid() {
		return "", false
	}
	return targetStore.GetResponseConnForTarget(responseID, expected)
}

func bindOpenAIWSSessionConn(store OpenAIWSStateStore, groupID int64, sessionHash string, target openAIWSConnectionTarget, connID string, ttl time.Duration) {
	targetStore, ok := store.(openAIWSTargetStateStore)
	if !ok || !target.valid() {
		return
	}
	targetStore.BindSessionConnForTarget(groupID, sessionHash, target, connID, ttl)
}

func getOpenAIWSSessionConn(store OpenAIWSStateStore, groupID int64, sessionHash string, expected openAIWSConnectionTarget) (string, bool) {
	targetStore, ok := store.(openAIWSTargetStateStore)
	if !ok || !expected.valid() {
		return "", false
	}
	return targetStore.GetSessionConnForTarget(groupID, sessionHash, expected)
}

func (s *defaultOpenAIWSStateStore) DeleteSessionConn(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	s.sessionToConnMu.Lock()
	delete(s.sessionToConn, key)
	s.sessionToConnMu.Unlock()
}

// DeleteConnBindings 清理本实例内所有指向失效连接的 response/session 索引。
func (s *defaultOpenAIWSStateStore) DeleteConnBindings(connID string) int {
	if s == nil {
		return 0
	}
	connID = strings.TrimSpace(connID)
	if connID == "" {
		return 0
	}

	deleted := 0
	s.responseToConnMu.Lock()
	for key, binding := range s.responseToConn {
		if strings.TrimSpace(binding.connID) == connID {
			delete(s.responseToConn, key)
			deleted++
		}
	}
	s.responseToConnMu.Unlock()

	s.sessionToConnMu.Lock()
	for key, binding := range s.sessionToConn {
		if strings.TrimSpace(binding.connID) == connID {
			delete(s.sessionToConn, key)
			deleted++
		}
	}
	s.sessionToConnMu.Unlock()
	return deleted
}

func deleteOpenAIWSConnBindings(store OpenAIWSStateStore, connID string) int {
	cleaner, ok := store.(openAIWSConnBindingCleanupStore)
	if !ok {
		return 0
	}
	return cleaner.DeleteConnBindings(connID)
}

func (s *defaultOpenAIWSStateStore) maybeCleanup() {
	if s == nil {
		return
	}
	now := time.Now()
	last := time.Unix(0, s.lastCleanupUnixNano.Load())
	if now.Sub(last) < openAIWSStateStoreCleanupInterval {
		return
	}
	if !s.lastCleanupUnixNano.CompareAndSwap(last.UnixNano(), now.UnixNano()) {
		return
	}

	// 增量限额清理，避免高规模下一次性全量扫描导致长时间阻塞。
	s.responseToAccountMu.Lock()
	cleanupExpiredAccountBindings(s.responseToAccount, now, openAIWSStateStoreCleanupMaxPerMap)
	s.responseToAccountMu.Unlock()

	s.responseOwnerMu.Lock()
	cleanupExpiredHTTPResponseOwnerBindings(s.responseOwners, now, openAIWSStateStoreCleanupMaxPerMap)
	s.responseOwnerMu.Unlock()

	s.responseToConnMu.Lock()
	cleanupExpiredConnBindings(s.responseToConn, now, openAIWSStateStoreCleanupMaxPerMap)
	s.responseToConnMu.Unlock()

	s.turnStateToAccountMu.Lock()
	cleanupExpiredTurnStateBindings(s.turnStateToAccount, now, openAIWSStateStoreCleanupMaxPerMap)
	s.turnStateToAccountMu.Unlock()

	s.sessionToTurnStateMu.Lock()
	cleanupExpiredSessionTurnStateBindings(s.sessionToTurnState, now, openAIWSStateStoreCleanupMaxPerMap)
	s.sessionToTurnStateMu.Unlock()

	s.sessionToConnMu.Lock()
	cleanupExpiredSessionConnBindings(s.sessionToConn, now, openAIWSStateStoreCleanupMaxPerMap)
	s.sessionToConnMu.Unlock()

	if s.replayCheckpoints != nil {
		s.replayCheckpoints.cleanupExpired(now, openAIWSStateStoreCleanupMaxPerMap)
	}
}

func cleanupExpiredAccountBindings(bindings map[string]openAIWSAccountBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredHTTPResponseOwnerBindings(bindings map[string]openAIHTTPResponseOwnerBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredConnBindings(bindings map[string]openAIWSConnBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredTurnStateBindings(bindings map[string]openAIWSTurnStateBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredSessionTurnStateBindings(bindings map[string]openAIWSSessionTurnStateBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredSessionConnBindings(bindings map[string]openAIWSSessionConnBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func ensureBindingCapacity[T any](bindings map[string]T, incomingKey string, maxEntries int) {
	if len(bindings) < maxEntries || maxEntries <= 0 {
		return
	}
	if _, exists := bindings[incomingKey]; exists {
		return
	}
	// 固定上限保护：淘汰任意一项，优先保证内存有界。
	for key := range bindings {
		delete(bindings, key)
		return
	}
}

func normalizeOpenAIWSResponseID(responseID string) string {
	return strings.TrimSpace(responseID)
}

func openAIWSResponseAccountCacheKey(responseID string) string {
	sum := sha256.Sum256([]byte(responseID))
	return openAIWSResponseAccountCachePrefix + hex.EncodeToString(sum[:])
}

func openAIHTTPResponseOwnerCacheKey(prefix, responseID string) string {
	sum := sha256.Sum256([]byte(responseID))
	return prefix + hex.EncodeToString(sum[:])
}

// openAIWSResponseAccountMapKey 本地热缓存按分组隔离的 key，与 Redis 层保持一致，避免跨组命中。
func openAIWSResponseAccountMapKey(groupID int64, responseID string) string {
	return fmt.Sprintf("%d:%s", groupID, responseID)
}

// openAIWSTurnStateAccountKey 只保存状态哈希溯源，避免原始 state blob 进入共享缓存。
func openAIWSTurnStateAccountKey(apiKeyID int64, sessionHash, turnState string) string {
	session := strings.TrimSpace(sessionHash)
	state := strings.TrimSpace(turnState)
	if apiKeyID <= 0 || session == "" || state == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%s", apiKeyID, session, state)))
	return openAIWSTurnStateAccountCachePrefix + hex.EncodeToString(sum[:])
}

func openAIWSTurnStateAccountMapKey(groupID int64, cacheKey string) string {
	return fmt.Sprintf("%d:%s", groupID, cacheKey)
}

func normalizeOpenAIWSTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return time.Hour
	}
	return ttl
}

func openAIWSSessionTurnStateKey(groupID int64, sessionHash string) string {
	hash := strings.TrimSpace(sessionHash)
	if hash == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s", groupID, hash)
}

func withOpenAIWSStateStoreRedisTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, openAIWSStateStoreRedisTimeout)
}
