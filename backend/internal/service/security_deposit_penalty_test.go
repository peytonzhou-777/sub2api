package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type securityDepositPenaltyCacheStub struct {
	userIDs []int64
}

func (s *securityDepositPenaltyCacheStub) InvalidateAuthCacheByKey(context.Context, string)    {}
func (s *securityDepositPenaltyCacheStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}
func (s *securityDepositPenaltyCacheStub) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}

func newSecurityDepositPenaltyTestService(mode string, enforcement bool, repo *fakeSecurityDepositRepository, cache APIKeyAuthCacheInvalidator) *SecurityDepositService {
	settings := NewSettingService(&securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositPenaltyMode:        mode,
		SettingKeySecurityDepositEnforcementEnabled: boolString(enforcement),
		SettingKeySecurityDepositMaxRiskMultiplier:  "8",
	}}, nil)
	svc := NewSecurityDepositService(repo)
	svc.SetOrderDependencies(nil, nil, settings)
	svc.SetPenaltyDependencies(cache)
	return svc
}

func testSecurityDepositCyberPenaltyInput() SecurityDepositCyberPenaltyInput {
	return SecurityDepositCyberPenaltyInput{
		EventKey: "event-1", RequestID: "request-1", PolicyCode: "cyber_policy",
		Grant: SecurityDepositAccessGrant{
			UserID: 7, GroupID: 9, BaseRequiredCents: 10000, RiskMultiplier: 2,
			RequiredCents: 20000, EffectiveBalanceCents: 30000, Enforced: true,
		},
		APIKeyID: 11, APIKeyName: "test-key", GroupName: "test-group",
	}
}

func TestSecurityDepositCyberPenalty_ShadowOnlyRecordsEvent(t *testing.T) {
	repo := &fakeSecurityDepositRepository{penaltyResult: &SecurityDepositCyberPenaltyResult{ViolationID: 21, State: "shadow"}}
	cache := &securityDepositPenaltyCacheStub{}
	svc := newSecurityDepositPenaltyTestService(SecurityDepositPenaltyModeShadow, false, repo, cache)

	result, err := svc.ApplyCyberPolicyPenalty(context.Background(), testSecurityDepositCyberPenaltyInput())

	require.NoError(t, err)
	require.Equal(t, int64(21), result.ViolationID)
	require.True(t, repo.penaltyShadow)
	require.Equal(t, int64(8), repo.penaltyMax)
	require.Empty(t, cache.userIDs)
}

func TestSecurityDepositCyberPenalty_EnforceInvalidatesUserCache(t *testing.T) {
	repo := &fakeSecurityDepositRepository{penaltyResult: &SecurityDepositCyberPenaltyResult{ViolationID: 22, State: "processed"}}
	cache := &securityDepositPenaltyCacheStub{}
	svc := newSecurityDepositPenaltyTestService(SecurityDepositPenaltyModeEnforce, true, repo, cache)
	reconciler := &securityDepositKeyChangeReconcilerStub{}
	svc.SetKeyEligibilityReconciler(reconciler)

	result, err := svc.ApplyCyberPolicyPenalty(context.Background(), testSecurityDepositCyberPenaltyInput())

	require.NoError(t, err)
	require.Equal(t, int64(22), result.ViolationID)
	require.False(t, repo.penaltyShadow)
	require.Equal(t, "cyber_policy_penalty", reconciler.eventType)
	require.Equal(t, int64(22), reconciler.eventID)
	require.Equal(t, []int64{7}, cache.userIDs)
}

func TestSecurityDepositCyberPenalty_RealModeRequiresEnforcedAccessGrant(t *testing.T) {
	repo := &fakeSecurityDepositRepository{}
	svc := newSecurityDepositPenaltyTestService(SecurityDepositPenaltyModeEnforce, true, repo, nil)
	input := testSecurityDepositCyberPenaltyInput()
	input.Grant.Enforced = false

	result, err := svc.ApplyCyberPolicyPenalty(context.Background(), input)

	require.NoError(t, err)
	require.Nil(t, result)
	require.Empty(t, repo.penaltyInput.EventKey)
}

func TestSecurityDepositCyberPenalty_IgnoresUntrustedPolicyCode(t *testing.T) {
	repo := &fakeSecurityDepositRepository{}
	svc := newSecurityDepositPenaltyTestService(SecurityDepositPenaltyModeEnforce, true, repo, nil)
	input := testSecurityDepositCyberPenaltyInput()
	input.PolicyCode = "content_policy"

	result, err := svc.ApplyCyberPolicyPenalty(context.Background(), input)

	require.NoError(t, err)
	require.Nil(t, result)
	require.Empty(t, repo.penaltyInput.EventKey)
}

func TestBuildSecurityDepositCyberPenaltyEventKey_IsStableAndTurnScoped(t *testing.T) {
	turnOne := int64(1)
	turnTwo := int64(2)
	first := BuildSecurityDepositCyberPenaltyEventKey("request-1", 11, "response-1", &turnOne)

	require.Equal(t, first, BuildSecurityDepositCyberPenaltyEventKey("request-1", 11, "response-1", &turnOne))
	require.NotEqual(t, first, BuildSecurityDepositCyberPenaltyEventKey("request-1", 11, "response-1", &turnTwo))
	require.Equal(t, first, BuildSecurityDepositCyberPenaltyEventKey("client-retry", 11, "response-1", &turnOne), "上游响应 ID 存在时不应受重试请求 ID 影响")
	require.NotEqual(t, BuildSecurityDepositCyberPenaltyEventKey("request-1", 11, "", &turnOne), BuildSecurityDepositCyberPenaltyEventKey("request-2", 11, "", &turnOne), "缺少上游响应 ID 时才按服务端请求 ID 区分事件")
	require.NotContains(t, first, "request-1")
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
