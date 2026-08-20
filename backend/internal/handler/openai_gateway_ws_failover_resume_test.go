package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSNextAttemptMessageUsesCurrentTurnPayload(t *testing.T) {
	firstMessage := []byte(`{"type":"response.create","input":"first"}`)
	currentTurn := []byte(`{"type":"response.create","input":"turn-281"}`)

	next, ok := openAIWSNextAttemptMessage(firstMessage, currentTurn, true)

	require.True(t, ok)
	require.Equal(t, currentTurn, next)
	next[0] = 'x'
	require.Equal(t, byte('{'), currentTurn[0], "retry payload must be cloned")
}

func TestOpenAIWSNextAttemptMessageRejectsMissingCurrentTurnPayload(t *testing.T) {
	next, ok := openAIWSNextAttemptMessage([]byte(`{"type":"response.create"}`), nil, true)

	require.False(t, ok)
	require.Nil(t, next)
}

func TestOpenAIWSNextAttemptMessageKeepsInitialMessageForFirstTurnFailover(t *testing.T) {
	firstMessage := []byte(`{"type":"response.create","input":"first"}`)

	next, ok := openAIWSNextAttemptMessage(firstMessage, nil, false)

	require.True(t, ok)
	require.Equal(t, firstMessage, next)
}

func TestShouldRetryOpenAIWS429OnSameAccountConsumesConfiguredBudget(t *testing.T) {
	account := &service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":             true,
			"pool_mode_retry_count": 3,
		},
	}
	failoverErr := &service.UpstreamFailoverError{
		StatusCode:           http.StatusTooManyRequests,
		OpenAIRateLimitClass: service.OpenAIRateLimitClassUsageQuota,
	}

	for completed := 0; completed < 3; completed++ {
		require.True(t, shouldRetryOpenAIWS429OnSameAccount(account, failoverErr, true, completed))
	}
	require.False(t, shouldRetryOpenAIWS429OnSameAccount(account, failoverErr, true, 3))
}

func TestShouldRetryOpenAIWS429OnSameAccountHonorsZeroAndSafetyGuards(t *testing.T) {
	zeroRetryAccount := &service.Account{
		ID:       43,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"pool_mode":             true,
			"pool_mode_retry_count": 0,
		},
	}
	retryAccount := &service.Account{ID: 44, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	failoverErr := &service.UpstreamFailoverError{StatusCode: http.StatusTooManyRequests}

	require.False(t, shouldRetryOpenAIWS429OnSameAccount(zeroRetryAccount, failoverErr, true, 0))
	require.False(t, shouldRetryOpenAIWS429OnSameAccount(retryAccount, failoverErr, false, 0))

	localFailure := *failoverErr
	localFailure.LocalRequestFailure = true
	require.False(t, shouldRetryOpenAIWS429OnSameAccount(retryAccount, &localFailure, true, 0))

	stopFailure := *failoverErr
	stopFailure.NextAccountAction = service.NextAccountStop
	require.False(t, shouldRetryOpenAIWS429OnSameAccount(retryAccount, &stopFailure, true, 0))

	non429 := *failoverErr
	non429.StatusCode = http.StatusServiceUnavailable
	require.False(t, shouldRetryOpenAIWS429OnSameAccount(retryAccount, &non429, true, 0))
}
