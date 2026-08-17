//go:build unit

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexFingerprintAdminServiceStub struct {
	service.AdminService
	status        *service.CodexFingerprintAdminStatus
	rotateCalls   int
	disableCalls  int
	lastAccountID int64
}

func (s *codexFingerprintAdminServiceStub) GetCodexFingerprintStatus(_ context.Context, accountID int64) (*service.CodexFingerprintAdminStatus, error) {
	s.lastAccountID = accountID
	return s.status, nil
}

func (s *codexFingerprintAdminServiceStub) RotateCodexFingerprint(_ context.Context, accountID int64) (*service.CodexFingerprintAdminStatus, error) {
	s.rotateCalls++
	s.lastAccountID = accountID
	return s.status, nil
}

func (s *codexFingerprintAdminServiceStub) DisableCodexFingerprint(_ context.Context, accountID int64) (*service.CodexFingerprintAdminStatus, error) {
	s.disableCalls++
	s.lastAccountID = accountID
	return s.status, nil
}

func TestAccountCodexFingerprintAdminActions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &codexFingerprintAdminServiceStub{status: &service.CodexFingerprintAdminStatus{AccountID: 27, Mode: "session", SessionScopeCount: 2}}
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/accounts/:id/codex-fingerprint", handler.GetCodexFingerprintStatus)
	router.POST("/accounts/:id/codex-fingerprint/rotate", handler.RotateCodexFingerprint)
	router.POST("/accounts/:id/codex-fingerprint/disable", handler.DisableCodexFingerprint)

	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/accounts/27/codex-fingerprint"},
		{http.MethodPost, "/accounts/27/codex-fingerprint/rotate"},
		{http.MethodPost, "/accounts/27/codex-fingerprint/disable"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Body.String(), `"session_scope_count":2`)
	}
	require.Equal(t, int64(27), svc.lastAccountID)
	require.Equal(t, 1, svc.rotateCalls)
	require.Equal(t, 1, svc.disableCalls)
}
