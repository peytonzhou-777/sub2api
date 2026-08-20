package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsRequestObservationSanitizesSensitiveValuesAndHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "secret-session")
	startedAt := time.Now().UTC().Add(-1500 * time.Millisecond)

	BeginOpsRequestObservation(c, startedAt)
	SetOpsOpenAIRequestMetadata(c, []byte(`{"service_tier":"priority","prompt_cache_key":"secret-cache"}`))
	proxyID := int64(42)
	SetOpsOpenAIForwardAttempt(c, &proxyID, 3)
	SetOpsAccountConcurrency(c, 7)
	CaptureOpsUpstreamResponse(c, http.Header{
		"X-Request-Id":               []string{"upstream-request"},
		"Retry-After":                []string{"12"},
		"X-Ratelimit-Limit-Requests": []string{"500"},
		"Authorization":              []string{"Bearer must-not-persist"},
	}, []byte(`{"error":{"type":"overloaded_error","code":"server_overloaded"}}`))
	c.Set(codexFingerprintLogicalTurnSourceContextKey, "secret-source")

	obs := GetOpsRequestObservation(c, time.Now().UTC())
	require.True(t, obs.ExplicitSessionIDPresent)
	require.NotEmpty(t, obs.ExplicitSessionIDHash)
	require.NotEqual(t, "secret-session", obs.ExplicitSessionIDHash)
	require.True(t, obs.PromptCacheKeyPresent)
	require.NotEmpty(t, obs.PromptCacheKeyHash)
	require.NotEqual(t, "secret-cache", obs.PromptCacheKeyHash)
	require.NotEmpty(t, obs.SessionSourceHash)
	require.NotEqual(t, "secret-source", obs.SessionSourceHash)
	require.Equal(t, "priority", obs.ServiceTier)
	require.Equal(t, "proxy:42", obs.EgressIdentifier)
	require.Equal(t, 3, obs.RetryCount)
	require.NotNil(t, obs.AccountConcurrency)
	require.Equal(t, 7, *obs.AccountConcurrency)
	require.Equal(t, "server_overloaded", obs.UpstreamErrorCode)
	require.Equal(t, "overloaded_error", obs.UpstreamErrorType)
	require.Equal(t, "upstream-request", obs.UpstreamRequestID)
	require.Equal(t, "12", obs.RetryAfter)
	require.Equal(t, []string{"500"}, obs.RateLimitHeaders["x-ratelimit-limit-requests"])
	require.NotContains(t, *obs.RateLimitHeadersJSON, "Authorization")
	require.GreaterOrEqual(t, obs.DurationMs, int64(1400))
}

func TestMarkOpsOpenAIForwardFailureUsesLastUpstreamOverload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Status(http.StatusOK)
	c.Writer.WriteHeaderNow()
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		UpstreamStatusCode: 529,
		Message:            "Upstream overloaded",
	})

	MarkOpsOpenAIForwardFailure(c, errors.New("stream failed"))
	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.True(t, streamErr.CountTowardsSLA)
	require.Equal(t, 529, streamErr.IntendedStatus)
	require.Equal(t, "upstream_error", streamErr.ErrType)
	require.Equal(t, "Upstream overloaded", streamErr.Message)
}

func TestMarkOpsOpenAIForwardFailureUpgradesExistingStreamMarker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Status(http.StatusOK)
	c.Writer.WriteHeaderNow()

	MarkOpsStreamError(c, "upstream_error", "temporary stream marker", http.StatusBadGateway)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		UpstreamStatusCode: 529,
		Message:            "Upstream overloaded",
	})
	MarkOpsOpenAIForwardFailure(c, errors.New("terminal forward failure"))

	streamErr, ok := GetOpsStreamError(c)
	require.True(t, ok)
	require.True(t, streamErr.CountTowardsSLA)
	require.Equal(t, 529, streamErr.IntendedStatus)
	require.Equal(t, "Upstream overloaded", streamErr.Message)
}

func TestCaptureOpsUpstreamResponseClearsPreviousErrorClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	CaptureOpsUpstreamResponse(c, http.Header{}, []byte(`{"error":{"code":"overloaded","type":"server_error"}}`))
	CaptureOpsUpstreamResponse(c, http.Header{"X-Request-Id": {"req-final"}}, nil)

	obs := GetOpsRequestObservation(c, time.Now())
	require.Empty(t, obs.UpstreamErrorCode)
	require.Empty(t, obs.UpstreamErrorType)
	require.Equal(t, "req-final", obs.UpstreamRequestID)
}

func TestSetOpsOpenAIForwardAttemptClearsPreviousAttemptResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	CaptureOpsUpstreamResponse(c, http.Header{
		"X-Request-Id":               {"req-previous"},
		"X-Ratelimit-Limit-Requests": {"500"},
	}, []byte(`{"error":{"code":"overloaded","type":"server_error"}}`))

	SetOpsOpenAIForwardAttempt(c, nil, 1)
	obs := GetOpsRequestObservation(c, time.Now())
	require.Empty(t, obs.UpstreamRequestID)
	require.Empty(t, obs.UpstreamErrorCode)
	require.Empty(t, obs.UpstreamErrorType)
	require.Empty(t, obs.RateLimitHeaders)
}
