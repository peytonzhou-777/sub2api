//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type middlewareSecurityDepositGateStub struct {
	grant *service.SecurityDepositAccessGrant
	err   error
	calls int
}

func (s *middlewareSecurityDepositGateStub) CheckAccess(context.Context, int64, int64) (*service.SecurityDepositAccessGrant, error) {
	s.calls++
	return s.grant, s.err
}

func securityDepositAuthTestAPIKey() *service.APIKey {
	group := &service.Group{ID: 9, Name: "受保护分组", Status: service.StatusActive, Hydrated: true}
	user := &service.User{ID: 7, Role: service.RoleUser, Status: service.StatusActive, Balance: 10, Concurrency: 1}
	return &service.APIKey{
		ID: 11, UserID: user.ID, Key: "sk-security-deposit-test", Status: service.StatusActive,
		GroupID: &group.ID, Group: group, User: user,
	}
}

func TestAPIKeyAuthDoesNotCheckSecurityDepositOnRequestPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := securityDepositAuthTestAPIKey()
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return apiKey, nil }}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	gate := &middlewareSecurityDepositGateStub{err: assertNeverCalledError{}}
	svc.SetSecurityDepositGate(gate)

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)))
	handlerCalled := false
	router.POST("/v1/messages", func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, handlerCalled)
	require.Zero(t, gate.calls)
}

func TestGoogleAPIKeyAuthDoesNotCheckSecurityDepositOnRequestPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := securityDepositAuthTestAPIKey()
	repo := fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return apiKey, nil }}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	gate := &middlewareSecurityDepositGateStub{err: assertNeverCalledError{}}
	svc.SetSecurityDepositGate(gate)

	router := gin.New()
	router.Use(APIKeyAuthGoogle(svc, cfg))
	handlerCalled := false
	router.POST("/v1beta/models/test:generateContent", func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/test:generateContent", nil)
	req.Header.Set("x-goog-api-key", apiKey.Key)
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.True(t, handlerCalled)
	require.Zero(t, gate.calls)
}

type assertNeverCalledError struct{}

func (assertNeverCalledError) Error() string {
	return "security deposit gate must not run on request path"
}
