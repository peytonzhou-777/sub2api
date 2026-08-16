//go:build unit

package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type securityDepositPenaltyServiceStub struct {
	checkUserID  int64
	checkGroupID int64
	grant        *service.SecurityDepositAccessGrant
	input        *service.SecurityDepositCyberPenaltyInput
}

func (s *securityDepositPenaltyServiceStub) GetAccessSnapshot(_ context.Context, userID, groupID int64) (*service.SecurityDepositAccessGrant, error) {
	s.checkUserID = userID
	s.checkGroupID = groupID
	return s.grant, nil
}

func (s *securityDepositPenaltyServiceStub) ApplyCyberPolicyPenalty(_ context.Context, input service.SecurityDepositCyberPenaltyInput) (*service.SecurityDepositCyberPenaltyResult, error) {
	s.input = &input
	return &service.SecurityDepositCyberPenaltyResult{State: "processed"}, nil
}

func TestApplySecurityDepositCyberPenaltyLoadsSnapshotOnlyAfterOfficialMark(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(9)
	grant := &service.SecurityDepositAccessGrant{
		UserID: 7, GroupID: groupID, BaseRequiredCents: 10000, RiskMultiplier: 2,
		RequiredCents: 20000, EffectiveBalanceCents: 20000, Enforced: true,
	}
	depositService := &securityDepositPenaltyServiceStub{grant: grant}
	h := &OpenAIGatewayHandler{securityDepositService: depositService}
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	requestCtx := context.WithValue(context.Background(), ctxkey.RequestID, "request-1")

	h.applySecurityDepositCyberPenalty(c, requestCtx, &service.APIKey{
		ID: 11, UserID: 7, Name: "test-key", GroupID: &groupID,
	}, &service.CyberPolicyMark{Code: "cyber_policy", Body: `{"id":"response-1"}`}, "test-group", nil)

	require.Equal(t, int64(7), depositService.checkUserID)
	require.Equal(t, groupID, depositService.checkGroupID)
	require.NotNil(t, depositService.input)
	require.Equal(t, *grant, depositService.input.Grant)
	require.Equal(t, int64(11), depositService.input.APIKeyID)
}
