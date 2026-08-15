package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayService_CountTokensAcceptedBeforeResponseBodyCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	responseReader, responseWriter := io.Pipe()
	defer func() { _ = responseReader.Close() }()
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       responseReader,
	}}
	svc, touchRepo := newOpenAIUserAffinitySuccessTestService(t, OpenAIUserAffinityTouchAccepted)
	touchRepo.touchCh = make(chan struct{}, 1)
	svc.cfg = &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{
		Enabled: false, AllowInsecureHTTP: true,
	}}}
	svc.httpUpstream = upstream

	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	account := &Account{
		ID: 303, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
		Status:      StatusActive, Schedulable: true,
	}

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- svc.ForwardCountTokensAsAnthropic(
			openAIUserAffinitySuccessTestContext("req-count-accepted"), c, account, body, "gpt-5.3-codex",
		)
	}()

	select {
	case <-touchRepo.touchCh:
		// accepted 必须先于仍被阻塞的响应体读取完成。
	case <-time.After(time.Second):
		t.Fatal("未在响应体完成前记录 upstream_accepted")
	}
	_, err := responseWriter.Write([]byte(`{"object":"response.input_tokens","input_tokens":42}`))
	require.NoError(t, err)
	require.NoError(t, responseWriter.Close())
	require.NoError(t, <-resultCh)
}
