package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"testing"
	"time"
)

// conversationIdentityTestFixture 模拟同一 OAuth Thread 的持久目标与严格哈希查找。
func conversationIdentityTestFixture(t *testing.T) (*OpenAIGatewayService, *schedulerConversationAffinityRepo, context.Context, *OpenAIUserConversationBinding) {
	t.Helper()
	account := Account{ID: 61, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, []Account{account}, nil, 2)
	svc.cfg.Gateway.CodexFingerprintSecret = strings.Repeat("x", 32)
	self := openAICodexThreadAliasHash([]byte(svc.cfg.Gateway.CodexFingerprintSecret), "identity-thread")
	scope := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	binding := &OpenAIUserConversationBinding{ID: 1, UserID: 42, APIKeyID: 77, ScopeKey: scope, ConversationHash: self, ResidentSlotID: 1, AccountID: 61, SlotGeneration: 1, Status: "active", ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: time.Now().Add(time.Hour)}
	bindOpenAIUserAffinityTestExecutionTarget(svc, binding)
	repo.binding = binding
	repo.bindingsByHash = map[string]*OpenAIUserConversationBinding{self: binding}
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{openAICodexThreadAliasTestKey(nil, self): binding}
	return svc, repo, ctx, binding
}

func TestConversationIdentityRequestKindsKeepOriginalTarget(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"normal", "/v1/responses", "{\"stream\":true}"},
		{"native_compact", "/v1/responses", "{\"stream\":true,\"input\":[{\"type\":\"compaction_trigger\"}]}"},
		{"legacy_compact", "/v1/responses/compact", "{}"},
		{"prewarm", "/v1/responses", "{\"generate\":false}"},
	} {
		for _, state := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{true: "_state", false: "_no_state"}[state], func(t *testing.T) {
				svc, repo, ctx, binding := conversationIdentityTestFixture(t)
				c := newOpenAICodexThreadAffinityTestContext(t, nil)
				c.Request = c.Request.WithContext(ctx)
				c.Request.URL.Path = tc.path
				c.Request.Header.Set("session_id", "identity-session")
				c.Request.Header.Set("thread-id", "identity-thread")
				if state {
					c.Request.Header.Set("x-codex-turn-state", "ts1.test")
				}
				body := []byte(tc.body)
				c.Request = c.Request.WithContext(ContextWithOpenAIContinuationState(c.Request.Context(), c.Request.Header, body, tc.path))
				req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", tc.name == "legacy_compact", nil)
				req.SessionHash = svc.GenerateSessionHash(c, body)
				selection, handled, err := svc.selectOpenAIUserAffinityConversation(c.Request.Context(), req)
				require.NoError(t, err)
				require.True(t, handled)
				require.NotNil(t, selection)
				require.Equal(t, binding.AccountID, selection.Account.ID)
				require.Equal(t, binding.AccountPersonaID, selection.ExecutionTarget.AccountPersonaID)
				require.Equal(t, binding.PersonaSessionEpoch, selection.ExecutionTarget.SessionEpoch)
				require.Empty(t, repo.reservations)
			})
		}
	}
}

func TestConversationIdentityCrossTransportAndParent(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "self", true: "child"}[parent], func(t *testing.T) {
			svc, repo, ctx, binding := conversationIdentityTestFixture(t)
			svc.cfg.Gateway.OpenAIWS = newSchedulerTestOpenAIWSV2Config().Gateway.OpenAIWS
			svc.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
			svc.cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeHTTPBridge
			if parent {
				ctx, _ = withOpenAICodexThreadAffinityTestState(ctx, strings.Repeat("c", 64), binding.ConversationHash)
			} else {
				ctx, _ = withOpenAICodexThreadAffinityTestState(ctx, binding.ConversationHash)
			}
			ctx = ContextWithOpenAIContinuationState(ctx, http.Header{"X-Codex-Turn-State": {"ts1.test"}}, nil)
			req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportResponsesWebsocketV2Ingress, "", "", false, nil)
			req.SessionHash = "identity-session"
			result, handled, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
			require.NoError(t, err)
			require.True(t, handled)
			require.Equal(t, binding.AccountPersonaID, result.ExecutionTarget.AccountPersonaID)
			require.Equal(t, binding.PersonaSessionEpoch, result.ExecutionTarget.SessionEpoch)
			attempt, ok := svc.openAIUserAffinityAttempt(ctx, 61)
			require.True(t, ok)
			require.Equal(t, binding.ScopeKey, attempt.conversation.ScopeKey)
			if parent {
				require.Len(t, repo.reservations, 1)
			} else {
				require.Empty(t, repo.reservations)
			}
		})
	}
}

func TestConversationIdentityPrewarmCannotCreateOrRenew(t *testing.T) {
	svc, repo, ctx, binding := conversationIdentityTestFixture(t)
	ctx = ContextWithOpenAIContinuationState(ctx, nil, []byte("{\"generate\":false}"))
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "unknown"
	_, handled, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, handled)
	require.Empty(t, repo.reservations)
	svc.rememberOpenAIUserAffinityConversationAttempt(ctx, binding, DefaultOpenAIUserAffinityConfig(), "")
	capacity := &openAIBoundPersonaReservationRepo{}
	ctx = context.WithValue(ctx, openAIPersonaUserReservationContextKey{}, &openAIPersonaUserReservationState{repo: capacity, token: "prewarm-held"})
	svc.RecordOpenAIUserAffinityAccepted(ctx, 61)
	svc.RecordOpenAIUserAffinitySuccess(ctx, 61)
	require.Empty(t, repo.commitTransitions)
	require.Empty(t, capacity.commits)
	require.Equal(t, []string{"prewarm-held"}, capacity.rollbackTokens)
}

func TestConversationIdentityWSRestoresAfterCacheEviction(t *testing.T) {
	svc, repo, ctx, binding := conversationIdentityTestFixture(t)
	activity := &activityTestRepository{schedulerConversationAffinityRepo: repo}
	svc.accountRepo = activity
	capacity := &openAIBoundPersonaReservationRepo{personaLeaseID: 901}
	svc.personaUserReservationRepo = capacity
	svc.rememberOpenAIUserAffinityConversationAttempt(ctx, binding, DefaultOpenAIUserAffinityConfig(), "")
	ctx = svc.ContextWithOpenAIConversationReference(ctx, 61)
	attempt, ok := svc.openAIUserAffinityAttempt(ctx, 61)
	require.True(t, ok)
	attempt.createdAt = time.Now().Add(-2 * time.Hour)
	require.NoError(t, svc.BeginOpenAIConversationActivity(ctx, 61))
	svc.endOpenAIConversationActivity(ctx)
	svc.pruneOpenAIUserAffinityRequestStates()
	_, ok = svc.openAIUserAffinityAttempt(ctx, 61)
	require.False(t, ok)
	require.NoError(t, svc.ResumeOpenAIConversationActivity(ctx, 61))
	svc.commitOpenAIConversationPersonaReservation(ctx)
	svc.endOpenAIConversationActivity(ctx)
	require.Len(t, capacity.reserveInputs, 1)
	require.Len(t, capacity.commits, 1)
	require.True(t, capacity.reserveInputs[0].ExistingThread)
	require.Equal(t, binding.AccountPersonaID, capacity.reserveInputs[0].AccountPersonaID)
	require.Equal(t, int32(2), activity.acquired.Load())
	require.Empty(t, capacity.rollbackTokens)
	// 第二次恢复必须重新申请容量；满额不重选号，也不获得会话 hold。
	capacity.reserveErr = ErrOpenAIPersonaUserCapacity
	require.ErrorIs(t, svc.ResumeOpenAIConversationActivity(ctx, 61), ErrOpenAIPersonaUserCapacity)
	require.Equal(t, int32(2), activity.acquired.Load())
	require.Empty(t, repo.failovers)
	require.NotEqual(t, capacity.reserveInputs[0].ReservationToken, capacity.reserveInputs[1].ReservationToken)
}

func TestConversationIdentityWSRejectsChangedEpoch(t *testing.T) {
	svc, repo, ctx, binding := conversationIdentityTestFixture(t)
	svc.accountRepo = &activityTestRepository{schedulerConversationAffinityRepo: repo}
	svc.rememberOpenAIUserAffinityConversationAttempt(ctx, binding, DefaultOpenAIUserAffinityConfig(), "")
	ctx = svc.ContextWithOpenAIConversationReference(ctx, 61)
	binding.PersonaSessionEpoch++
	require.ErrorIs(t, svc.ResumeOpenAIConversationActivity(ctx, 61), ErrOpenAIConversationResetRequired)
}

func TestConversationIdentityResponseOwnerRecoversFromDurableAlias(t *testing.T) {
	svc, repo, ctx, binding := conversationIdentityTestFixture(t)
	scope := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportResponsesWebsocketV2Ingress)
	hash := openAIUserAffinityScopedStateHash(42, 77, scope, "response_id", "resp-durable")
	repo.aliasBindings[scope+"\x00response_id\x00"+hash] = binding
	owned, err := svc.ValidateOpenAIHTTPResponseOwner(ctx, 0, "resp-durable", 42, 77)
	require.NoError(t, err)
	require.True(t, owned)
	owned, err = svc.ValidateOpenAIHTTPResponseOwner(ctx, 0, "resp-durable", 42, 78)
	require.NoError(t, err)
	require.False(t, owned)
}

func TestConversationIdentityScopeBoundaries(t *testing.T) {
	base := "openai:v1:group:4:lane:general"
	for _, lane := range []string{"general", "compact", "endpoint:responses", "general:transport:responses_websockets_v2_ingress"} {
		require.True(t, OpenAIConversationScopesCompatible(base, "openai:v1:group:4:lane:"+lane))
	}
	for _, other := range []string{"openai:v1:group:5:lane:general", "openai:v1:group:4:lane:endpoint:chat_completions", "openai:v1:group:4:lane:images:native", "openai:v1:group:4:lane:general:transport:unknown"} {
		require.False(t, OpenAIConversationScopesCompatible(base, other))
	}
}
