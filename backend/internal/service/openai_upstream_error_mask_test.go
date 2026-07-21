package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccount_IsOpenAIUpstreamErrorMaskEnabled(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{name: "空账号默认关闭", account: nil, want: false},
		{name: "OpenAI默认关闭", account: &Account{Platform: PlatformOpenAI}, want: false},
		{name: "OpenAI显式开启", account: &Account{Platform: PlatformOpenAI, Extra: map[string]any{"openai_mask_upstream_errors": true}}, want: true},
		{name: "非OpenAI不生效", account: &Account{Platform: PlatformAnthropic, Extra: map[string]any{"openai_mask_upstream_errors": true}}, want: false},
		{name: "非布尔值不生效", account: &Account{Platform: PlatformOpenAI, Extra: map[string]any{"openai_mask_upstream_errors": "true"}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.account.IsOpenAIUpstreamErrorMaskEnabled())
		})
	}
}

func TestMapOpenAIMaskedUpstreamError(t *testing.T) {
	tests := []struct {
		status      int
		wantStatus  int
		wantType    string
		wantMessage string
	}{
		{http.StatusBadRequest, http.StatusBadRequest, "invalid_request_error", "Request rejected by upstream service"},
		{http.StatusUnauthorized, http.StatusBadGateway, "upstream_error", "Upstream service authentication or quota error"},
		{http.StatusTooManyRequests, http.StatusTooManyRequests, "rate_limit_error", "Service is busy, please retry later"},
		{529, http.StatusServiceUnavailable, "upstream_error", "Service is temporarily overloaded, please retry later"},
		{http.StatusInternalServerError, http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable"},
		{http.StatusTeapot, http.StatusBadGateway, "upstream_error", "Upstream request failed"},
	}

	for _, tt := range tests {
		got := MapOpenAIMaskedUpstreamError(tt.status)
		require.Equal(t, tt.wantStatus, got.Status)
		require.Equal(t, tt.wantType, got.ErrType)
		require.Equal(t, tt.wantMessage, got.Message)
	}
}

func TestMaskOpenAIWSEventForClient(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		path    string
		message string
	}{
		{name: "response failed", payload: `{"type":"response.failed","response":{"error":{"code":"third_party","message":"relay.example.com rejected"}}}`, path: "response.error.message", message: "Upstream service temporarily unavailable"},
		{name: "error event", payload: `{"type":"error","error":{"type":"invalid_request_error","message":"relay.example.com rejected"}}`, path: "error.message", message: "Request rejected by upstream service"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskOpenAIWSEventForClient([]byte(tt.payload))
			require.Equal(t, tt.message, gjson.GetBytes(got, tt.path).String())
			require.NotContains(t, string(got), "relay.example.com")
		})
	}
}

func TestIsOpenAIResponseFailedJSON(t *testing.T) {
	require.True(t, isOpenAIResponseFailedJSON([]byte(`{"status":"failed"}`)))
	require.True(t, isOpenAIResponseFailedJSON([]byte(`{"response":{"status":"failed"}}`)))
	require.True(t, isOpenAIResponseFailedJSON([]byte(`{"type":"response.failed"}`)))
	require.False(t, isOpenAIResponseFailedJSON([]byte(`{"status":"completed"}`)))
}
