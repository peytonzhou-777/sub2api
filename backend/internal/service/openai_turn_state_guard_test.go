package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAITurnStateAttemptStripsCrossAccountWithoutMutatingOriginalHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7)
	svc := &OpenAIGatewayService{cache: &stubGatewayCache{}}
	stateStore := svc.getOpenAIWSStateStore()
	body := []byte(`{"model":"gpt-5.1","prompt_cache_key":"session-a","client_metadata":{"turn_id":"turn-a","x-codex-turn-state":"state-from-account-a"}}`)
	account := &Account{ID: 202, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "oauth-token"}}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 81, GroupID: &groupID})
	c.Request.Header.Set("session_id", "session-a")
	c.Request.Header.Set(openAIWSTurnStateHeader, "state-from-account-a")
	stageOpenAITurnStateTestScope(c, account, "turn-a")
	sessionHash := svc.GenerateSessionHash(c, body)
	originalRequest := c.Request
	sourceScope, ok := openAITurnStateScopeForAttempt(context.Background(), c, account)
	require.True(t, ok)
	sourceScope.AccountID = 101
	require.NoError(t, stateStore.BindTurnStateScope(context.Background(), groupID, 81, sessionHash, "state-from-account-a", sourceScope, time.Minute))

	sanitized, restore, err := svc.isolateOpenAITurnStateAttempt(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotSame(t, originalRequest, c.Request)
	require.Empty(t, c.GetHeader(openAIWSTurnStateHeader))
	require.NotContains(t, string(sanitized), "x-codex-turn-state")
	restore()

	require.Same(t, originalRequest, c.Request)
	require.Equal(t, "state-from-account-a", c.GetHeader(openAIWSTurnStateHeader))
	require.Equal(t, int64(1), svc.SnapshotOpenAIWSRetryMetrics().TurnStateStrippedTotal)
}

func stageOpenAITurnStateTestScope(c *gin.Context, account *Account, turnID string) {
	ids := &codexFingerprintIDs{
		accountID:           account.ID,
		sessionScopeVersion: codexFingerprintScopeV2,
		sessionSlot:         0,
		sessionSlotCount:    2,
		sessionEpoch:        11,
		installationID:      "installation-guard",
		sessionScopeHash:    "scope-hash-guard",
		sessionID:           "session-guard",
		threadID:            "thread-guard",
		turnID:              turnID,
	}
	stageCodexFingerprintIDs(c, ids)
	c.Set(codexFingerprintAdmissionPreparedContextKey, account.ID)
	binding := SessionPersonaSlotBinding{
		AccountID:               account.ID,
		SlotID:                  0,
		SlotCount:               2,
		ScopeVersion:            SessionPersonaScopeVersionV3,
		MappingVersion:          SessionPersonaScopeVersionV3,
		FingerprintScopeVersion: codexFingerprintScopeV2,
		PersonaID:               SessionPersonaCodexCLIStrict,
		PersonaVersion:          ResolveCodexOutboundProfile(account),
		CredentialChainID:       "codex-chain-guard",
		InstallationID:          ids.installationID,
		State:                   SessionPersonaSlotStateActive,
		Enabled:                 true,
		Authorized:              true,
		SessionEpoch:            ids.sessionEpoch,
		SlotGeneration:          3,
		SlotSetGeneration:       5,
	}
	requireAttach := AttachSessionPersonaBindingToGin(c, binding)
	if !requireAttach {
		panic("failed to attach test Persona binding")
	}
}

func TestOpenAITurnStateScopeMismatchReasons(t *testing.T) {
	base := testOpenAITurnStateScope(42, "turn-1")
	tests := []struct {
		name   string
		mutate func(*OpenAITurnStateScope)
		reason string
	}{
		{name: "account", mutate: func(scope *OpenAITurnStateScope) { scope.AccountID++ }, reason: "cross_account"},
		{name: "persona", mutate: func(scope *OpenAITurnStateScope) { scope.Persona = SessionPersonaOpenCode }, reason: "cross_persona"},
		{name: "persona version", mutate: func(scope *OpenAITurnStateScope) { scope.PersonaVersion = "other-version" }, reason: "scope_mismatch"},
		{name: "mapping version", mutate: func(scope *OpenAITurnStateScope) { scope.MappingVersion-- }, reason: "scope_mismatch"},
		{name: "slot", mutate: func(scope *OpenAITurnStateScope) { scope.SlotID++ }, reason: "cross_slot"},
		{name: "turn", mutate: func(scope *OpenAITurnStateScope) { scope.UpstreamTurnID = "turn-2" }, reason: "cross_turn"},
		{name: "epoch", mutate: func(scope *OpenAITurnStateScope) { scope.SessionEpoch++ }, reason: "cross_generation"},
		{name: "slot generation", mutate: func(scope *OpenAITurnStateScope) { scope.SlotGeneration++ }, reason: "cross_generation"},
		{name: "slot set generation", mutate: func(scope *OpenAITurnStateScope) { scope.SlotSetGeneration++ }, reason: "cross_generation"},
		{name: "credential chain", mutate: func(scope *OpenAITurnStateScope) { scope.CredentialChainID = "other-chain" }, reason: "cross_credential"},
		{name: "installation", mutate: func(scope *OpenAITurnStateScope) { scope.InstallationID = "other-installation" }, reason: "scope_mismatch"},
		{name: "session scope", mutate: func(scope *OpenAITurnStateScope) { scope.SessionScopeHash = "other-scope" }, reason: "scope_mismatch"},
		{name: "upstream session", mutate: func(scope *OpenAITurnStateScope) { scope.UpstreamSessionID = "other-session" }, reason: "scope_mismatch"},
		{name: "upstream thread", mutate: func(scope *OpenAITurnStateScope) { scope.UpstreamThreadID = "other-thread" }, reason: "scope_mismatch"},
		{name: "profile", mutate: func(scope *OpenAITurnStateScope) { scope.OutboundProfile = "other-profile" }, reason: "scope_mismatch"},
		{name: "transport", mutate: func(scope *OpenAITurnStateScope) { scope.TransportScopeDigest = "other-transport" }, reason: "scope_mismatch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := base
			tt.mutate(&current)
			require.False(t, base.Equal(current))
			require.Equal(t, tt.reason, openAITurnStateScopeMismatchReason(base, current))
		})
	}
}

func TestOpenAITurnStateScopeV3BindsAccountPersonaAndEpoch(t *testing.T) {
	base := OpenAITurnStateScope{
		Version: 3, AccountID: 42, AccountPersonaID: 81,
		Persona: SessionPersonaCodexCLIStrict, PersonaVersion: "0.149.0",
		PersonaGeneration: 2, SessionEpoch: 5, CredentialChainID: "chain",
		InstallationID: "installation", ProxyRevision: 7,
		SessionScopeHash: "scope", UpstreamSessionID: "session",
		UpstreamThreadID: "thread", UpstreamTurnID: "turn",
		OutboundProfile: "0.149.0", TransportScopeDigest: "transport",
	}
	require.True(t, base.Valid())
	other := base
	other.AccountPersonaID++
	require.False(t, base.Equal(other))
	require.Equal(t, "cross_account_persona", openAITurnStateScopeMismatchReason(base, other))
	other = base
	other.SessionEpoch++
	require.Equal(t, "cross_generation", openAITurnStateScopeMismatchReason(base, other))
}

func TestOpenAITurnStateAttemptAllowsOnlyExactScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9)
	svc := &OpenAIGatewayService{cache: &stubGatewayCache{}}
	account := &Account{ID: 303, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"access_token": "oauth-token"}}
	body := []byte(`{"model":"gpt-5.1","client_metadata":{"turn_id":"turn-exact","x-codex-turn-state":"state-exact"}}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "session-exact")
	c.Request.Header.Set(openAIWSTurnStateHeader, "state-exact")
	c.Set("api_key", &APIKey{ID: 83, GroupID: &groupID})
	stageOpenAITurnStateTestScope(c, account, "turn-exact")
	sessionHash := svc.GenerateSessionHash(c, body)
	scope, ok := openAITurnStateScopeForAttempt(context.Background(), c, account)
	require.True(t, ok)
	require.NoError(t, svc.getOpenAIWSStateStore().BindTurnStateScope(
		context.Background(), groupID, 83, sessionHash, "state-exact", scope, time.Minute,
	))

	sanitized, restore, err := svc.isolateOpenAITurnStateAttempt(context.Background(), c, account, body)
	require.NoError(t, err)
	require.Equal(t, "state-exact", c.GetHeader(openAIWSTurnStateHeader))
	require.Equal(t, "state-exact", gjson.GetBytes(sanitized, "client_metadata.x-codex-turn-state").String())
	restore()
}

func TestOpenAITurnStateLifecycleClearsAcrossTurns(t *testing.T) {
	lifecycle := &openAITurnStateLifecycle{}
	require.Equal(t, "state-1", lifecycle.BeginTurn("turn-1", "state-1"))
	require.Equal(t, "state-1", lifecycle.BeginTurn("turn-1", "state-conflict"), "same-turn state is first-write-wins")
	_, committed := lifecycle.Commit("turn-1", "state-late")
	require.False(t, committed)
	require.Empty(t, lifecycle.BeginTurn("turn-2", ""), "a new turn must start without prior state")
	state, committed := lifecycle.Commit("turn-2", "state-2")
	require.True(t, committed)
	require.Equal(t, "state-2", state)
}

func TestExtractOpenAITurnStateFromMetadataEvent(t *testing.T) {
	require.Equal(t, "state-meta", extractOpenAITurnStateFromMetadataEvent(
		"response.metadata", []byte(`{"type":"response.metadata","headers":{"x-codex-turn-state":"state-meta"}}`),
	))
	require.Equal(t, "state-codex", extractOpenAITurnStateFromMetadataEvent(
		"codex.response.metadata", []byte(`{"type":"codex.response.metadata","response":{"headers":{"x_codex_turn_state":"state-codex"}}}`),
	))
	require.Empty(t, extractOpenAITurnStateFromMetadataEvent(
		"response.completed", []byte(`{"headers":{"x-codex-turn-state":"ignored"}}`),
	))
}

func TestOpenAITurnStateAttemptStripsCrossAccountFromWSHTTPBridge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(8)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bridge_guard\",\"model\":\"gpt-5.1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n" +
				"data: [DONE]\n\n",
		)),
	}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, cache: &stubGatewayCache{}, httpUpstream: upstream}
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"prompt_cache_key":"bridge-session","input":"hello","client_metadata":{"turn_id":"turn-bridge"}}`)
	account := &Account{ID: 202, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"access_token": "oauth-token"}}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 82, GroupID: &groupID})
	c.Request.Header.Set("session_id", "bridge-session")
	c.Request.Header.Set(openAIWSTurnStateHeader, "state-from-account-a")
	stageOpenAITurnStateTestScope(c, account, "turn-bridge")
	sessionHash := svc.GenerateSessionHash(c, payload)
	originalRequest := c.Request
	sourceScope, ok := openAITurnStateScopeForAttempt(context.Background(), c, account)
	require.True(t, ok)
	sourceScope.AccountID = 101
	require.NoError(t, svc.getOpenAIWSStateStore().BindTurnStateScope(
		context.Background(), groupID, 82, sessionHash, "state-from-account-a", sourceScope, time.Minute,
	))

	sanitized, restore, isolateErr := svc.isolateOpenAITurnStateAttempt(
		context.Background(), c, account, payload,
	)
	require.NoError(t, isolateErr)
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c,
		account,
		"oauth-token", sanitized, len(sanitized), "gpt-5.1", "", "", "", "", 1,
		func([]byte) error { return nil },
	)
	restore()

	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Empty(t, upstream.lastReq.Header.Get(openAIWSTurnStateHeader))
	require.Same(t, originalRequest, c.Request)
	require.Equal(t, "state-from-account-a", c.GetHeader(openAIWSTurnStateHeader))
}

func TestOpenAIWSV2HandshakeStateIsNotCommittedBeforeValidOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := http.Header{}
		headers.Set(openAIWSTurnStateHeader, "state-from-failed-handshake")
		conn, err := upgrader.Upgrade(w, r, headers)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var payload map[string]any
		_ = conn.ReadJSON(&payload)
	}))
	defer wsServer.Close()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}
	account := &Account{
		ID:          303,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": wsServer.URL},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 91})
	c.Request.Header.Set("session_id", "session-failed-handshake")
	request := map[string]any{"model": "gpt-5.1", "stream": false, "input": []any{"hello"}}

	_, err := svc.forwardOpenAIWSV2(
		context.Background(), c, account, request, "sk-test",
		"", svc.getOpenAIWSProtocolResolver().Resolve(account), false, false,
		"gpt-5.1", "gpt-5.1", time.Now(), 1, "", nil,
	)
	require.Error(t, err)
	require.Empty(t, recorder.Header().Get(openAIWSTurnStateHeader))

	sessionHash := svc.GenerateSessionHash(c, nil)
	_, lookupErr := svc.getOpenAIWSStateStore().GetTurnStateScope(
		context.Background(), 0, 91, sessionHash, "state-from-failed-handshake",
	)
	require.ErrorIs(t, lookupErr, ErrOpenAITurnStateScopeNotFound)
}
