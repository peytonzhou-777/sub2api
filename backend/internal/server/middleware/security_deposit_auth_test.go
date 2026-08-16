//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
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

func TestAPIKeyAuthRejectsInsufficientSecurityDepositBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := securityDepositAuthTestAPIKey()
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return apiKey, nil }}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	gate := &middlewareSecurityDepositGateStub{err: infraerrors.Forbidden(
		"SECURITY_DEPOSIT_REQUIRED", "security deposit is insufficient for this group",
	)}
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

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "SECURITY_DEPOSIT_REQUIRED")
	require.False(t, handlerCalled)
	require.Equal(t, 1, gate.calls)
}

func TestGoogleAPIKeyAuthRejectsInsufficientSecurityDepositBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := securityDepositAuthTestAPIKey()
	repo := fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return apiKey, nil }}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	gate := &middlewareSecurityDepositGateStub{err: infraerrors.Forbidden(
		"SECURITY_DEPOSIT_REQUIRED", "security deposit is insufficient for this group",
	)}
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

	require.Equal(t, http.StatusForbidden, w.Code)
	require.Contains(t, w.Body.String(), "security deposit is insufficient")
	require.False(t, handlerCalled)
	require.Equal(t, 1, gate.calls)
}

func TestAPIKeyAuthAttachesSecurityDepositAccessGrant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	apiKey := securityDepositAuthTestAPIKey()
	repo := &stubApiKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) { return apiKey, nil }}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	want := &service.SecurityDepositAccessGrant{
		UserID: 7, GroupID: 9, BaseRequiredCents: 10000, RiskMultiplier: 2,
		RequiredCents: 20000, EffectiveBalanceCents: 20000, Enforced: true,
	}
	svc.SetSecurityDepositGate(&middlewareSecurityDepositGateStub{grant: want})

	router := gin.New()
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)))
	router.POST("/v1/messages", func(c *gin.Context) {
		got, _ := c.Request.Context().Value(ctxkey.SecurityDepositAccessGrant).(*service.SecurityDepositAccessGrant)
		require.Equal(t, want, got)
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", apiKey.Key)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}
