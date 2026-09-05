package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"net/http/httptest"
	"testing"
)

func TestOpenAIContinuationRejectionRetainsPersonaAudit(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/responses", nil)
	failure := &service.OpenAIContinuationSelectionError{Cause: service.ErrOpenAIPersonaUserCapacity, BindingID: 3, AccountID: 61, AccountPersonaID: 55, Profile: "codex_cli_strict", SessionEpoch: 1, Source: "persona_reservation"}
	recordOpenAIContinuationSelectionFailure(c, zap.NewNop(), failure)
	id, profile := getOpsPersonaAudit(c)
	require.NotNil(t, id)
	require.Equal(t, int64(55), *id)
	require.Equal(t, "codex_cli_strict", profile)
	account, ok := c.Get(opsAccountIDKey)
	require.True(t, ok)
	require.Equal(t, int64(61), account)
	require.ErrorIs(t, failure, service.ErrOpenAIPersonaUserCapacity)
}
