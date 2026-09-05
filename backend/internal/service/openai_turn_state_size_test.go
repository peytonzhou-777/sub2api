package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUpstreamTurnStateSizeBytes(t *testing.T) {
	require.Nil(t, upstreamTurnStateSizeBytes(nil))
	for _, raw := range []string{"", "blob-A", strings.Repeat("a", 4097), "\xc3\xa9", " raw "} {
		headers := http.Header{}
		headers.Set(openAICodexTurnStateHeader, raw)
		size := upstreamTurnStateSizeBytes(headers)
		if raw == "" {
			require.Nil(t, size)
		} else {
			require.Equal(t, len(raw), *size)
		}
	}
}

func TestUpstreamTurnStateSizeBytes_BeforeClientWrapping(t *testing.T) {
	svc := &OpenAIGatewayService{turnStateCipher: newOpenAITurnStateCipher(&config.Config{JWT: config.JWTConfig{Secret: "test-secret"}})}
	c, recorder := newTurnStateTestContext(t, 7, "size-session")
	headers := http.Header{}
	headers.Set(openAICodexTurnStateHeader, "raw-state")
	svc.relayOpenAICodexTurnState(c, &Account{ID: 42}, headers)
	require.True(t, strings.HasPrefix(recorder.Header().Get(openAICodexTurnStateHeader), "ts1."))
	require.Equal(t, 9, *upstreamTurnStateSizeBytes(headers))
	require.Greater(t, len(recorder.Header().Get(openAICodexTurnStateHeader)), 9)
}

func TestOpenAIGatewayServiceRecordUsage_UpstreamTurnStateSize(t *testing.T) {
	size := 4321
	for _, tt := range []struct {
		name    string
		size    *int
		headers http.Header
		want    *int
	}{
		{name: "http", size: &size, want: &size},
		{name: "websocket_handshake", headers: http.Header{"X-Codex-Turn-State": {strings.Repeat("s", size)}}, want: &size},
		{name: "absent"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{
					RequestID: "resp_state_size", Model: "gpt-5.4",
					Usage:                      OpenAIUsage{InputTokens: 20, OutputTokens: 10},
					UpstreamTurnStateSizeBytes: tt.size, ResponseHeaders: tt.headers,
					OpenAIWSMode: tt.name == "websocket_handshake",
				},
				APIKey: &APIKey{ID: 10}, User: &User{ID: 20}, Account: &Account{ID: 30},
			})
			require.NoError(t, err)
			require.NotNil(t, repo.lastLog)
			require.Equal(t, tt.want, repo.lastLog.UpstreamTurnStateSizeBytes)
		})
	}
}

func TestOpenAIGatewayForward_UpstreamTurnStateSize(t *testing.T) {
	for _, tt := range []struct {
		name   string
		path   string
		stream bool
	}{
		{name: "responses_stream", path: "/v1/responses", stream: true},
		{name: "responses_json", path: "/v1/responses"},
		{name: "compact_json", path: "/v1/responses/compact"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := newOpenAICompactFallbackTestContext(t, tt.path)
			body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","stream":%t,"instructions":"size-test","input":[]}`, tt.stream))
			c.Request.Header.Set("Content-Type", "application/json")
			payload := `{"id":"resp_size","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`
			headers := http.Header{"Content-Type": {"application/json"}}
			headers.Set(openAICodexTurnStateHeader, "original-state")
			if tt.stream {
				headers.Set("Content-Type", "text/event-stream")
				payload = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + payload + "}\n\n"
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(strings.NewReader(payload)),
			}}
			cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-secret"}}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, turnStateCipher: newOpenAITurnStateCipher(cfg)}
			configureOpenAICodexGatewayTest(svc)
			account := &Account{
				ID: 1, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
				Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"},
				Status:      StatusActive, Schedulable: true,
			}
			ctx := bindOpenAICodexGatewayTestExecution(t, svc, c, account)
			result, err := svc.Forward(ctx, c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, result.UpstreamTurnStateSizeBytes)
			require.Equal(t, len("original-state"), *result.UpstreamTurnStateSizeBytes)
			require.True(t, strings.HasPrefix(c.Writer.Header().Get(openAICodexTurnStateHeader), "ts1."))
		})
	}
}
