package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newOpenAICodexThreadAffinityTestContext(t *testing.T, state *openAICodexThreadAffinityState) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if state != nil {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAICodexThreadAffinityContextKey{}, state))
	}
	return c
}

func TestOpenAICodexThreadAffinityStagesOnlyHMACIndexes(t *testing.T) {
	secret := strings.Repeat("s", 32)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.CodexFingerprintSecret = secret
	c := newOpenAICodexThreadAffinityTestContext(t, nil)
	body := []byte(`{"client_metadata":{"session_id":"raw-session","thread_id":"raw-child","parent_thread_id":"raw-parent","forked_from_thread_id":"raw-fork","x-openai-subagent":"collab_spawn"}}`)

	svc.stageOpenAICodexThreadAffinity(c, body)
	state := stagedOpenAICodexThreadAffinity(c)
	require.NotNil(t, state)
	require.True(t, state.internalSubagent)
	require.Equal(t, openAICodexThreadAliasHash([]byte(secret), "raw-session", "raw-child"), state.selfAliasHash)
	require.ElementsMatch(t, []string{
		openAICodexThreadAliasHash([]byte(secret), "raw-session", "raw-parent"),
		openAICodexThreadAliasHash([]byte(secret), "raw-session", "raw-fork"),
	}, state.parentAliasHashes)
	encoded := strings.Join(append([]string{state.selfAliasHash}, state.parentAliasHashes...), "\n")
	require.NotContains(t, encoded, "raw-session")
	require.NotContains(t, encoded, "raw-parent")
}

func TestOpenAICodexThreadAffinityMarkerOnlyNeverAuthorizesLineage(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.CodexFingerprintSecret = strings.Repeat("k", 32)
	c := newOpenAICodexThreadAffinityTestContext(t, nil)
	c.Request.Header.Set("session-id", "session-1")
	c.Request.Header.Set("thread-id", "thread-1")
	c.Request.Header.Set("x-openai-subagent", "collab_spawn")

	svc.GenerateSessionHash(c, nil)
	state := stagedOpenAICodexThreadAffinity(c)
	require.NotNil(t, state)
	require.NotEmpty(t, state.selfAliasHash)
	require.Empty(t, state.parentAliasHashes)
	require.True(t, state.internalSubagent)
	require.False(t, state.allows(1))
}

func TestOpenAICodexThreadAffinitySkipsNonTurnRequests(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.CodexFingerprintSecret = strings.Repeat("n", 32)
	body := []byte(`{"client_metadata":{"session_id":"session","thread_id":"thread","parent_thread_id":"parent","x-openai-subagent":"collab_spawn"}}`)

	for _, test := range []struct {
		name string
		path string
		body []byte
	}{
		{name: "input_tokens", path: "/v1/responses/input_tokens", body: body},
		{name: "legacy_compact", path: "/v1/responses/compact", body: body},
		{name: "chat_completions", path: "/v1/chat/completions", body: body},
		{name: "prewarm", path: "/v1/responses", body: []byte(`{"generate":false,"client_metadata":{"session_id":"session","thread_id":"thread","parent_thread_id":"parent"}}`)},
		{name: "native_compact", path: "/v1/responses", body: []byte(`{"stream":true,"input":[{"type":"compaction_trigger"}],"client_metadata":{"session_id":"session","thread_id":"thread","parent_thread_id":"parent"}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := newOpenAICodexThreadAffinityTestContext(t, nil)
			c.Request.URL.Path = test.path
			svc.GenerateSessionHash(c, test.body)
			state := stagedOpenAICodexThreadAffinity(c)
			require.NotNil(t, state, "非 turn 仍需保留本地子代理语义用于出站剥离")
			require.Empty(t, state.selfAliasHash)
			require.Empty(t, state.parentAliasHashes)
			require.True(t, state.internalSubagent)
		})
	}
}

func TestOpenAICodexThreadAffinityGenerateSessionFeedsParentScheduling(t *testing.T) {
	accountID := int64(36236)
	svc, repo, requestContext := newMultiSlotAffinitySchedulerTestService(t, nil, []Account{{
		ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
	}}, nil, 2)
	secret := strings.Repeat("p", 32)
	svc.cfg.Gateway.CodexFingerprintSecret = secret
	c := newOpenAICodexThreadAffinityTestContext(t, nil)
	c.Request = c.Request.WithContext(requestContext)
	c.Request.Header.Set("session_id", "session-bridge")
	body := []byte(`{"client_metadata":{"session_id":"session-bridge","thread_id":"child-thread","parent_thread_id":"parent-thread","x-openai-subagent":"collab_spawn"}}`)
	sessionHash := svc.GenerateSessionHash(c, body)
	require.NotEmpty(t, sessionHash)
	state := stagedOpenAICodexThreadAffinity(c)
	require.NotNil(t, state)
	parentHash := openAICodexThreadAliasHash([]byte(secret), "session-bridge", "parent-thread")
	require.Contains(t, state.parentAliasHashes, parentHash)
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		openAICodexThreadAliasTestKey(nil, parentHash): {
			ID: 2306, UserID: 42, APIKeyID: 77,
			ScopeKey:         openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
			ConversationHash: strings.Repeat("7", 64), ResidentSlotID: 36,
			AccountID: accountID, SlotGeneration: 1, Status: "active",
			ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: time.Now().UTC().Add(time.Hour),
		},
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = sessionHash

	selection, found, err := svc.selectOpenAIUserAffinityConversation(c.Request.Context(), req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, accountID, selection.Account.ID)
	require.True(t, state.allows(accountID))
}

func TestOpenAICodexThreadAffinityStripsUnauthorizedHTTPAndBodyLineage(t *testing.T) {
	state := &openAICodexThreadAffinityState{internalSubagent: true}
	c := newOpenAICodexThreadAffinityTestContext(t, state)
	account := &Account{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	headers := http.Header{
		"X-Openai-Subagent":        []string{"collab_spawn"},
		"X-Codex-Parent-Thread-Id": []string{"parent"},
		"X-Codex-Turn-Metadata":    []string{`{"parent_thread_id":"parent","forked_from_thread_id":"fork","parent_turn_id":"turn","subagent_kind":"thread_spawn","request_kind":"subagent_turn","thread_source":"subagent","keep":"ok"}`},
	}
	c.Request.Header = headers.Clone()
	outgoingHeaders := headers.Clone()
	body := map[string]any{"client_metadata": map[string]any{
		"parent_thread_id":      "parent",
		"forked_from_thread_id": "fork",
		"x-openai-subagent":     "collab_spawn",
		"subagent_kind":         "thread_spawn",
		"thread_source":         "subagent",
		"keep":                  "ok",
		"x-codex-turn-metadata": `{"parent_thread_id":"parent","subagent_kind":"thread_spawn","keep":"ok"}`,
	}}

	applyStagedCodexFingerprintHeaders(c, account, outgoingHeaders)
	require.Empty(t, outgoingHeaders.Get("x-openai-subagent"))
	require.Empty(t, outgoingHeaders.Get("x-codex-parent-thread-id"))
	headerMetadata := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(outgoingHeaders.Get("x-codex-turn-metadata")), &headerMetadata))
	require.Equal(t, "ok", headerMetadata["keep"])
	require.NotContains(t, headerMetadata, "parent_thread_id")
	require.NotContains(t, headerMetadata, "subagent_kind")
	require.NotContains(t, headerMetadata, "request_kind")
	require.Equal(t, "collab_spawn", c.Request.Header.Get("x-openai-subagent"), "不得修改原始客户端 Header")
	require.Equal(t, "parent", c.Request.Header.Get("x-codex-parent-thread-id"))

	require.True(t, applyStagedCodexFingerprintClientMetadata(c, account, body))
	metadata := body["client_metadata"].(map[string]any)
	require.Equal(t, "ok", metadata["keep"])
	require.NotContains(t, metadata, "parent_thread_id")
	require.NotContains(t, metadata, "forked_from_thread_id")
	require.NotContains(t, metadata, "x-openai-subagent")
	embedded := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(metadata["x-codex-turn-metadata"].(string)), &embedded))
	require.Equal(t, "ok", embedded["keep"])
	require.NotContains(t, embedded, "parent_thread_id")
}

func TestOpenAICodexThreadAffinityRawProjectionStripsMarkerOnly(t *testing.T) {
	state := &openAICodexThreadAffinityState{internalSubagent: true}
	c := newOpenAICodexThreadAffinityTestContext(t, state)
	account := &Account{ID: 92, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"model":"gpt-5.4","client_metadata":{"session_id":"session","thread_id":"child","parent_thread_id":"parent","forked_from_thread_id":"fork","x-openai-subagent":"collab_spawn","subagent_kind":"thread_spawn","payload":"kept"}}`)

	projected, err := (&OpenAIGatewayService{}).applyCodexFingerprintForAttempt(context.Background(), c, account, body, true, true)
	require.NoError(t, err)
	require.Equal(t, "kept", gjsonString(projected, "client_metadata.payload"))
	require.Empty(t, gjsonString(projected, "client_metadata.parent_thread_id"))
	require.Empty(t, gjsonString(projected, "client_metadata.forked_from_thread_id"))
	require.Empty(t, gjsonString(projected, "client_metadata.x-openai-subagent"))
}

func TestOpenAICodexThreadAffinityKeepsLocalSubagentAfterDetachingLineage(t *testing.T) {
	state := &openAICodexThreadAffinityState{internalSubagent: true}
	c := newOpenAICodexThreadAffinityTestContext(t, state)
	account := &Account{ID: 95, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	original := extractCodexFingerprintOriginalIDs(nil, []byte(`{"client_metadata":{"session_id":"session","thread_id":"child","parent_thread_id":"parent","x-openai-subagent":"collab_spawn","subagent_kind":"thread_spawn","thread_source":"subagent"}}`))

	applyOpenAICodexThreadLineagePolicy(c, account, &original)
	require.True(t, original.isSubagent, "本地子代理并发控制仍需识别该请求")
	require.Empty(t, original.parentThreadID)
	require.Empty(t, original.forkedThreadID)
	require.Empty(t, original.subagentHeader)
	require.Empty(t, original.subagentKind)
	require.Empty(t, original.threadSource)
}

func TestOpenAICodexThreadAffinityPreservesAuthorizedLineage(t *testing.T) {
	state := &openAICodexThreadAffinityState{internalSubagent: true, authorizedAccountID: 93}
	c := newOpenAICodexThreadAffinityTestContext(t, state)
	account := &Account{ID: 93, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	headers := http.Header{
		"X-Openai-Subagent":        []string{"collab_spawn"},
		"X-Codex-Parent-Thread-Id": []string{"parent"},
	}
	body := map[string]any{"client_metadata": map[string]any{
		"parent_thread_id":  "parent",
		"x-openai-subagent": "collab_spawn",
	}}

	applyStagedCodexFingerprintHeaders(c, account, headers)
	require.Equal(t, "collab_spawn", headers.Get("x-openai-subagent"))
	require.Equal(t, "parent", headers.Get("x-codex-parent-thread-id"))
	require.False(t, applyStagedCodexFingerprintClientMetadata(c, account, body))
	require.Equal(t, "parent", body["client_metadata"].(map[string]any)["parent_thread_id"])
}

func TestOpenAICodexThreadAffinityPreservesRootTurnChainWithoutDerivedSignal(t *testing.T) {
	state := &openAICodexThreadAffinityState{}
	c := newOpenAICodexThreadAffinityTestContext(t, state)
	account := &Account{ID: 94, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	body := map[string]any{"client_metadata": map[string]any{
		"parent_turn_id": "previous-turn",
		"root_turn_id":   "root-turn",
	}}

	require.False(t, stripOpenAICodexLineageClientMetadata(c, account, body))
	metadata := body["client_metadata"].(map[string]any)
	require.Equal(t, "previous-turn", metadata["parent_turn_id"])
	require.Equal(t, "root-turn", metadata["root_turn_id"])
}

func gjsonString(body []byte, path string) string {
	return gjson.GetBytes(body, path).String()
}
