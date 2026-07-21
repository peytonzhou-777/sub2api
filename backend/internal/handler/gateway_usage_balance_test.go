//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type totalAvailableBalanceProviderStub struct {
	total     float64
	err       error
	userID    int64
	callCount int
}

func (s *totalAvailableBalanceProviderStub) GetUserTotalAvailableBalance(_ context.Context, userID int64) (float64, error) {
	s.callCount++
	s.userID = userID
	return s.total, s.err
}

func TestGatewayUsageUnrestrictedReturnsTotalAvailableBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &totalAvailableBalanceProviderStub{total: 107.5}
	handler := &GatewayHandler{totalBalanceProvider: provider}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)

	handler.usageUnrestricted(c, c.Request.Context(), &service.APIKey{}, middleware2.AuthSubject{UserID: 42}, nil, nil, nil)

	require.Equal(t, http.StatusOK, recorder.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, 107.5, body["balance"])
	require.Equal(t, 107.5, body["remaining"])
	require.Equal(t, int64(42), provider.userID)
	require.Equal(t, 1, provider.callCount)
}

func TestGatewayUsageUnrestrictedFailsWhenTotalBalanceQueryFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &totalAvailableBalanceProviderStub{err: errors.New("limited credit query failed")}
	handler := &GatewayHandler{totalBalanceProvider: provider}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)

	handler.usageUnrestricted(c, c.Request.Context(), &service.APIKey{}, middleware2.AuthSubject{UserID: 42}, nil, nil, nil)

	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Failed to get user balance")
}

func TestGatewayUsageQuotaLimitedDoesNotQueryAccountBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &totalAvailableBalanceProviderStub{total: 999}
	handler := &GatewayHandler{totalBalanceProvider: provider}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{Quota: 10, QuotaUsed: 3, Status: service.StatusAPIKeyActive})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 42})

	handler.Usage(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 0, provider.callCount)
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "quota_limited", body["mode"])
	require.Equal(t, float64(7), body["remaining"])
}

func TestGatewayUsageSubscriptionDoesNotQueryAccountBalance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	provider := &totalAvailableBalanceProviderStub{total: 999}
	handler := &GatewayHandler{totalBalanceProvider: provider}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	apiKey := &service.APIKey{Group: &service.Group{SubscriptionType: service.SubscriptionTypeSubscription}}

	handler.usageUnrestricted(c, c.Request.Context(), apiKey, middleware2.AuthSubject{UserID: 42}, nil, nil, nil)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, 0, provider.callCount)
	var body map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.Equal(t, "unrestricted", body["mode"])
	require.NotContains(t, body, "balance")
}
