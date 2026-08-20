package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSReplayCheckpointMaterializesAcrossConnectionCleanup(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	replayStore, ok := store.(openAIWSReplayCheckpointStateStore)
	require.True(t, ok)

	account := &Account{ID: 101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	target := newOpenAIWSConnectionTarget(
		account,
		OpenAIUpstreamTransportResponsesWebsocketV2,
		"wss://api.openai.com/v1/responses",
		http.Header{"User-Agent": []string{"unit-test/1.0"}},
	)
	groupID := int64(7)
	ttl := time.Minute

	replayStore.BindReplayCheckpoint(groupID, "resp_root", openAIWSReplayCheckpoint{
		SourceConnID:     "conn-old",
		Target:           target,
		RequestInput:     []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"hello"}`)},
		RequestInputSeen: true,
		ResponseOutput:   []json.RawMessage{json.RawMessage(`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}`)},
		Replayable:       true,
	}, ttl)
	replayStore.BindReplayCheckpoint(groupID, "resp_child", openAIWSReplayCheckpoint{
		SourceConnID:       "conn-old",
		Target:             target,
		PreviousResponseID: "resp_root",
		RequestInput:       []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"continue"}`)},
		RequestInputSeen:   true,
		ResponseOutput:     []json.RawMessage{json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"shell","arguments":"{}"}`)},
		Replayable:         true,
	}, ttl)

	store.BindResponseConn("resp_root", "conn-old", ttl)
	store.BindResponseConn("resp_child", "conn-old", ttl)
	require.Equal(t, 2, deleteOpenAIWSConnBindings(store, "conn-old"))

	checkpoint, found := replayStore.GetReplayCheckpoint(groupID, "resp_child", target)
	require.True(t, found)
	require.True(t, checkpoint.Replayable)
	require.True(t, checkpoint.FullInputExists)
	require.Len(t, checkpoint.FullInput, 4)
	require.Equal(t, "user", gjson.GetBytes(checkpoint.FullInput[0], "role").String())
	require.Equal(t, "assistant", gjson.GetBytes(checkpoint.FullInput[1], "role").String())
	require.Equal(t, "continue", gjson.GetBytes(checkpoint.FullInput[2], "content").String())
	require.Equal(t, "call_1", gjson.GetBytes(checkpoint.FullInput[3], "call_id").String())
	require.Equal(t, "conn-old", checkpoint.SourceConnID)

	mismatchedTarget := target
	mismatchedTarget.wsURL = "wss://example.invalid/v1/responses"
	mismatch, found := replayStore.GetReplayCheckpoint(groupID, "resp_child", mismatchedTarget)
	require.True(t, found, "已知 checkpoint 目标不匹配时必须保留已知来源信号")
	require.False(t, mismatch.Replayable)
	require.Equal(t, "target_mismatch", mismatch.UnavailableReason)
}

func TestOpenAIWSReplayCheckpointMissingParentStaysKnownButBlocked(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	replayStore := store.(openAIWSReplayCheckpointStateStore)
	target := newOpenAIWSConnectionTarget(
		&Account{ID: 102, Platform: PlatformOpenAI, Type: AccountTypeAPIKey},
		OpenAIUpstreamTransportResponsesWebsocketV2,
		"wss://api.openai.com/v1/responses",
		http.Header{"User-Agent": []string{"unit-test/1.0"}},
	)
	replayStore.BindReplayCheckpoint(8, "resp_child", openAIWSReplayCheckpoint{
		SourceConnID:       "conn-old",
		Target:             target,
		PreviousResponseID: "resp_missing",
		RequestInput:       []json.RawMessage{json.RawMessage(`{"type":"message","role":"user","content":"continue"}`)},
		RequestInputSeen:   true,
		Replayable:         true,
	}, time.Minute)

	checkpoint, found := replayStore.GetReplayCheckpoint(8, "resp_child", target)
	require.True(t, found)
	require.False(t, checkpoint.Replayable)
	require.Equal(t, "missing_parent_checkpoint", checkpoint.UnavailableReason)
	require.Equal(t, "conn-old", checkpoint.SourceConnID)
}

func TestOpenAIWSStateStore_BindGetDeleteResponseAccount(t *testing.T) {
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(7)

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_abc", 101, time.Minute))

	accountID, err := store.GetResponseAccount(ctx, groupID, "resp_abc")
	require.NoError(t, err)
	require.Equal(t, int64(101), accountID)

	require.NoError(t, store.DeleteResponseAccount(ctx, groupID, "resp_abc"))
	accountID, err = store.GetResponseAccount(ctx, groupID, "resp_abc")
	require.NoError(t, err)
	require.Zero(t, accountID)
}

func TestOpenAIWSStateStoreConnectionTargetIsolation(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	proxyAID := int64(1)
	proxyBID := int64(2)
	accountA := &Account{ID: 101, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                   map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintVersion: "v2", CodexFingerprintEpoch: 3,
		ProxyID: &proxyAID, Proxy: &Proxy{ID: proxyAID, Protocol: "http", Host: "proxy-a.example", Port: 8080}}
	accountB := &Account{ID: 102, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                   map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintVersion: "v2", CodexFingerprintEpoch: 3}
	accountNextProxy := *accountA
	accountNextProxy.ProxyID = &proxyBID
	accountNextProxy.Proxy = &Proxy{ID: proxyBID, Protocol: "http", Host: "proxy-b.example", Port: 8080}
	headersA := http.Header{
		"X-Codex-Installation-Id": []string{"device-a"},
		"Session-Id":              []string{"session-3"},
		"User-Agent":              []string{"codex-cli/1.2.3"},
		"Originator":              []string{"codex_cli_rs"},
		"OpenAI-Beta":             []string{"responses_websockets=2026-02-06"},
	}
	headersNextEpoch := headersA.Clone()
	headersNextEpoch.Set("session-id", "session-4")
	headersNextUserAgent := headersA.Clone()
	headersNextUserAgent.Set("user-agent", "codex-cli/1.2.4")
	headersNextBeta := headersA.Clone()
	headersNextBeta.Set("openai-beta", "responses_websockets=2027-01-01")
	targetA := newOpenAIWSConnectionTarget(accountA, OpenAIUpstreamTransportResponsesWebsocketV2, "wss://api.openai.com/v1/responses", headersA)
	targetB := newOpenAIWSConnectionTarget(accountB, OpenAIUpstreamTransportResponsesWebsocketV2, "wss://api.openai.com/v1/responses", headersA)
	nextEpoch := newOpenAIWSConnectionTarget(accountA, OpenAIUpstreamTransportResponsesWebsocketV2, "wss://api.openai.com/v1/responses", headersNextEpoch)
	nextUserAgent := newOpenAIWSConnectionTarget(accountA, OpenAIUpstreamTransportResponsesWebsocketV2, "wss://api.openai.com/v1/responses", headersNextUserAgent)
	nextBeta := newOpenAIWSConnectionTarget(accountA, OpenAIUpstreamTransportResponsesWebsocketV2, "wss://api.openai.com/v1/responses", headersNextBeta)
	nextProxy := newOpenAIWSConnectionTarget(&accountNextProxy, OpenAIUpstreamTransportResponsesWebsocketV2, "wss://api.openai.com/v1/responses", headersA)

	bindOpenAIWSResponseConn(store, "resp_target", targetA, "conn_a", time.Minute)
	bindOpenAIWSSessionConn(store, 9, "session_target", targetA, "conn_a", time.Minute)

	connID, ok := getOpenAIWSResponseConn(store, "resp_target", targetA)
	require.True(t, ok)
	require.Equal(t, "conn_a", connID)
	_, ok = getOpenAIWSResponseConn(store, "resp_target", targetB)
	require.False(t, ok)
	_, ok = getOpenAIWSResponseConn(store, "resp_target", nextEpoch)
	require.False(t, ok)
	_, ok = getOpenAIWSResponseConn(store, "resp_target", nextUserAgent)
	require.False(t, ok)
	_, ok = getOpenAIWSResponseConn(store, "resp_target", nextBeta)
	require.False(t, ok)
	_, ok = getOpenAIWSResponseConn(store, "resp_target", nextProxy)
	require.False(t, ok)
	_, ok = getOpenAIWSSessionConn(store, 9, "session_target", targetB)
	require.False(t, ok)
}

func TestOpenAIWSStateStore_ResponseConnTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindResponseConn("resp_conn", "conn_1", 30*time.Millisecond)

	connID, ok := store.GetResponseConn("resp_conn")
	require.True(t, ok)
	require.Equal(t, "conn_1", connID)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetResponseConn("resp_conn")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_TurnStateAccountTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	ctx := context.Background()
	require.NoError(t, store.BindTurnStateAccount(ctx, 9, 101, "session_hash_1", "turn_state_1", 501, 30*time.Millisecond))
	require.NoError(t, store.BindTurnStateAccount(ctx, 9, 101, "session_hash_1", "turn_state_2", 502, 30*time.Millisecond))

	accountID, err := store.GetTurnStateAccount(ctx, 9, 101, "session_hash_1", "turn_state_1")
	require.NoError(t, err)
	require.Equal(t, int64(501), accountID)
	accountID, err = store.GetTurnStateAccount(ctx, 9, 101, "session_hash_1", "turn_state_2")
	require.NoError(t, err)
	require.Equal(t, int64(502), accountID, "同一 session 的并发状态不能互相覆盖")

	accountID, err = store.GetTurnStateAccount(ctx, 10, 101, "session_hash_1", "turn_state_1")
	require.NoError(t, err)
	require.Zero(t, accountID, "group 必须隔离")
	accountID, err = store.GetTurnStateAccount(ctx, 9, 102, "session_hash_1", "turn_state_1")
	require.NoError(t, err)
	require.Zero(t, accountID, "API Key 必须隔离")
	accountID, err = store.GetTurnStateAccount(ctx, 9, 101, "session_hash_2", "turn_state_1")
	require.NoError(t, err)
	require.Zero(t, accountID, "session 必须隔离")

	time.Sleep(60 * time.Millisecond)
	accountID, err = store.GetTurnStateAccount(ctx, 9, 101, "session_hash_1", "turn_state_1")
	require.NoError(t, err)
	require.Zero(t, accountID)
}

func TestOpenAIWSStateStore_TurnStateProvenanceSharedWithoutRawState(t *testing.T) {
	cache := &stubGatewayCache{}
	writer := NewOpenAIWSStateStore(cache)
	ctx := context.Background()

	require.NoError(t, writer.BindTurnStateAccount(ctx, 7, 31, "session_shared", "sensitive_state_blob", 901, time.Minute))
	reader := NewOpenAIWSStateStore(cache)
	accountID, err := reader.GetTurnStateAccount(ctx, 7, 31, "session_shared", "sensitive_state_blob")
	require.NoError(t, err)
	require.Equal(t, int64(901), accountID)

	for key := range cache.sessionBindings {
		require.NotContains(t, key, "sensitive_state_blob")
		require.NotContains(t, key, "session_shared")
	}
}

func TestOpenAIWSStateStore_SessionConnTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindSessionConn(9, "session_hash_conn_1", "conn_1", 30*time.Millisecond)

	connID, ok := store.GetSessionConn(9, "session_hash_conn_1")
	require.True(t, ok)
	require.Equal(t, "conn_1", connID)

	// group 隔离
	_, ok = store.GetSessionConn(10, "session_hash_conn_1")
	require.False(t, ok)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetSessionConn(9, "session_hash_conn_1")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_DeleteConnBindings(t *testing.T) {
	raw := NewOpenAIWSStateStore(nil)
	store, ok := raw.(*defaultOpenAIWSStateStore)
	require.True(t, ok)

	store.BindResponseConn("resp_old_1", "conn_old", time.Minute)
	store.BindResponseConn("resp_old_2", "conn_old", time.Minute)
	store.BindResponseConn("resp_keep", "conn_keep", time.Minute)
	store.BindSessionConn(9, "session_old", "conn_old", time.Minute)
	store.BindSessionConn(9, "session_keep", "conn_keep", time.Minute)

	require.Equal(t, 3, store.DeleteConnBindings("conn_old"))
	_, ok = store.GetResponseConn("resp_old_1")
	require.False(t, ok)
	_, ok = store.GetResponseConn("resp_old_2")
	require.False(t, ok)
	_, ok = store.GetSessionConn(9, "session_old")
	require.False(t, ok)

	connID, ok := store.GetResponseConn("resp_keep")
	require.True(t, ok)
	require.Equal(t, "conn_keep", connID)
	connID, ok = store.GetSessionConn(9, "session_keep")
	require.True(t, ok)
	require.Equal(t, "conn_keep", connID)
}

func TestOpenAIWSStateStore_GetResponseAccount_NoStaleAfterCacheMiss(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(17)
	responseID := "resp_cache_stale"
	cacheKey := openAIWSResponseAccountCacheKey(responseID)

	cache.sessionBindings[cacheKey] = 501
	accountID, err := store.GetResponseAccount(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Equal(t, int64(501), accountID)

	delete(cache.sessionBindings, cacheKey)
	accountID, err = store.GetResponseAccount(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Zero(t, accountID, "上游缓存失效后不应继续命中本地陈旧映射")
}

func TestOpenAIWSStateStore_MaybeCleanupRemovesExpiredIncrementally(t *testing.T) {
	raw := NewOpenAIWSStateStore(nil)
	store, ok := raw.(*defaultOpenAIWSStateStore)
	require.True(t, ok)

	expiredAt := time.Now().Add(-time.Minute)
	total := 2048
	store.responseToConnMu.Lock()
	for i := 0; i < total; i++ {
		store.responseToConn[fmt.Sprintf("resp_%d", i)] = openAIWSConnBinding{
			connID:    "conn_incremental",
			expiresAt: expiredAt,
		}
	}
	store.responseToConnMu.Unlock()

	store.lastCleanupUnixNano.Store(time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano())
	store.maybeCleanup()

	store.responseToConnMu.RLock()
	remainingAfterFirst := len(store.responseToConn)
	store.responseToConnMu.RUnlock()
	require.Less(t, remainingAfterFirst, total, "单轮 cleanup 应至少有进展")
	require.Greater(t, remainingAfterFirst, 0, "增量清理不要求单轮清空全部键")

	for i := 0; i < 8; i++ {
		store.lastCleanupUnixNano.Store(time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano())
		store.maybeCleanup()
	}

	store.responseToConnMu.RLock()
	remaining := len(store.responseToConn)
	store.responseToConnMu.RUnlock()
	require.Zero(t, remaining, "多轮 cleanup 后应逐步清空全部过期键")
}

func TestEnsureBindingCapacity_EvictsOneWhenMapIsFull(t *testing.T) {
	bindings := map[string]int{
		"a": 1,
		"b": 2,
	}

	ensureBindingCapacity(bindings, "c", 2)
	bindings["c"] = 3

	require.Len(t, bindings, 2)
	require.Equal(t, 3, bindings["c"])
}

func TestEnsureBindingCapacity_DoesNotEvictWhenUpdatingExistingKey(t *testing.T) {
	bindings := map[string]int{
		"a": 1,
		"b": 2,
	}

	ensureBindingCapacity(bindings, "a", 2)
	bindings["a"] = 9

	require.Len(t, bindings, 2)
	require.Equal(t, 9, bindings["a"])
}

type openAIWSStateStoreTimeoutProbeCache struct {
	setHasDeadline    bool
	getHasDeadline    bool
	deleteHasDeadline bool
	setDeadlineDelta  time.Duration
	getDeadlineDelta  time.Duration
	delDeadlineDelta  time.Duration
}

func (c *openAIWSStateStoreTimeoutProbeCache) GetSessionAccountID(ctx context.Context, _ int64, _ string) (int64, error) {
	if deadline, ok := ctx.Deadline(); ok {
		c.getHasDeadline = true
		c.getDeadlineDelta = time.Until(deadline)
	}
	return 123, nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) SetSessionAccountID(ctx context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.setHasDeadline = true
		c.setDeadlineDelta = time.Until(deadline)
	}
	return errors.New("set failed")
}

func (c *openAIWSStateStoreTimeoutProbeCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) DeleteSessionAccountID(ctx context.Context, _ int64, _ string) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.deleteHasDeadline = true
		c.delDeadlineDelta = time.Until(deadline)
	}
	return nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) SetGrokVideoPendingBilling(_ context.Context, _ string, _ []byte, _ time.Duration) error {
	return nil
}
func (c *openAIWSStateStoreTimeoutProbeCache) GetGrokVideoPendingBilling(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}
func (c *openAIWSStateStoreTimeoutProbeCache) ClaimGrokVideoBilled(_ context.Context, _ string, _ time.Duration) (bool, error) {
	return true, nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) ReleaseGrokVideoBilled(_ context.Context, _ string) error {
	return nil
}

func TestOpenAIWSStateStore_RedisOpsUseShortTimeout(t *testing.T) {
	probe := &openAIWSStateStoreTimeoutProbeCache{}
	store := NewOpenAIWSStateStore(probe)
	ctx := context.Background()
	groupID := int64(5)

	err := store.BindResponseAccount(ctx, groupID, "resp_timeout_probe", 11, time.Minute)
	require.Error(t, err)

	accountID, getErr := store.GetResponseAccount(ctx, groupID, "resp_timeout_probe")
	require.NoError(t, getErr)
	require.Equal(t, int64(11), accountID, "本地缓存命中应优先返回已绑定账号")

	require.NoError(t, store.DeleteResponseAccount(ctx, groupID, "resp_timeout_probe"))

	require.True(t, probe.setHasDeadline, "SetSessionAccountID 应携带独立超时上下文")
	require.True(t, probe.deleteHasDeadline, "DeleteSessionAccountID 应携带独立超时上下文")
	require.False(t, probe.getHasDeadline, "GetSessionAccountID 本用例应由本地缓存命中，不触发 Redis 读取")
	require.Greater(t, probe.setDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe.setDeadlineDelta, 3*time.Second)
	require.Greater(t, probe.delDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe.delDeadlineDelta, 3*time.Second)

	probe2 := &openAIWSStateStoreTimeoutProbeCache{}
	store2 := NewOpenAIWSStateStore(probe2)
	accountID2, err2 := store2.GetResponseAccount(ctx, groupID, "resp_cache_only")
	require.NoError(t, err2)
	require.Equal(t, int64(123), accountID2)
	require.True(t, probe2.getHasDeadline, "GetSessionAccountID 在缓存未命中时应携带独立超时上下文")
	require.Greater(t, probe2.getDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe2.getDeadlineDelta, 3*time.Second)
}

func TestWithOpenAIWSStateStoreRedisTimeout_WithParentContext(t *testing.T) {
	ctx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
	defer cancel()
	require.NotNil(t, ctx)
	_, ok := ctx.Deadline()
	require.True(t, ok, "应附加短超时")
}
