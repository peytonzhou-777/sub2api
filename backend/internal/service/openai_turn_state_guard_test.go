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
)

func TestOpenAITurnStateAttemptStripsCrossAccountWithoutMutatingOriginalHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(7)
	svc := &OpenAIGatewayService{cache: &stubGatewayCache{}}
	stateStore := svc.getOpenAIWSStateStore()
	body := []byte(`{"model":"gpt-5.1","prompt_cache_key":"session-a"}`)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 81, GroupID: &groupID})
	c.Request.Header.Set("session_id", "session-a")
	c.Request.Header.Set(openAIWSTurnStateHeader, "state-from-account-a")
	sessionHash := svc.GenerateSessionHash(c, body)
	originalRequest := c.Request
	require.NoError(t, stateStore.BindTurnStateAccount(context.Background(), groupID, 81, sessionHash, "state-from-account-a", 101, time.Minute))

	restore := svc.isolateOpenAITurnStateAttempt(context.Background(), c, &Account{ID: 202, Platform: PlatformOpenAI}, body)
	require.NotSame(t, originalRequest, c.Request)
	require.Empty(t, c.GetHeader(openAIWSTurnStateHeader))
	restore()

	require.Same(t, originalRequest, c.Request)
	require.Equal(t, "state-from-account-a", c.GetHeader(openAIWSTurnStateHeader))
	require.Equal(t, int64(1), svc.SnapshotOpenAIWSRetryMetrics().TurnStateStrippedTotal)
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
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"prompt_cache_key":"bridge-session","input":"hello"}`)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 82, GroupID: &groupID})
	c.Request.Header.Set(openAIWSTurnStateHeader, "state-from-account-a")
	sessionHash := svc.GenerateSessionHash(c, payload)
	originalRequest := c.Request
	require.NoError(t, svc.getOpenAIWSStateStore().BindTurnStateAccount(
		context.Background(), groupID, 82, sessionHash, "state-from-account-a", 101, time.Minute,
	))

	restore := svc.isolateOpenAITurnStateAttempt(
		context.Background(), c, &Account{ID: 202, Platform: PlatformOpenAI}, payload,
	)
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(
		context.Background(), c,
		&Account{ID: 202, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1},
		"sk-test", payload, len(payload), "gpt-5.1", "", "", "", "", 1,
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
		svc.getOpenAIWSProtocolResolver().Resolve(account), false, false,
		"gpt-5.1", "gpt-5.1", time.Now(), 1, "", nil,
	)
	require.Error(t, err)
	require.Empty(t, recorder.Header().Get(openAIWSTurnStateHeader))

	sessionHash := svc.GenerateSessionHash(c, nil)
	accountID, lookupErr := svc.getOpenAIWSStateStore().GetTurnStateAccount(
		context.Background(), 0, 91, sessionHash, "state-from-failed-handshake",
	)
	require.NoError(t, lookupErr)
	require.Zero(t, accountID)
}
