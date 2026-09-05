package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type activityTestRepository struct {
	*schedulerConversationAffinityRepo
	OpenAIConversationActivityStore
	expired  bool
	acquired atomic.Int32
	released atomic.Int32
}

func (r *activityTestRepository) HasExpiredOpenAIConversation(context.Context, int64, int64, string, string, []OpenAIUserConversationAlias) (bool, error) {
	return r.expired, nil
}
func (r *activityTestRepository) AcquireOpenAIConversationActivity(context.Context, OpenAIUserConversationTransition, string, time.Time, ...string) (bool, error) {
	r.acquired.Add(1)
	return !r.expired, nil
}
func (r *activityTestRepository) ReleaseOpenAIConversationActivity(context.Context, string) error {
	r.released.Add(1)
	return nil
}

func TestOpenAIConversationExpiryRejectsOldIdentityAndOpaqueState(t *testing.T) {
	for _, mode := range []string{"expired_thread", "opaque_state", "missing_parent", "new_root"} {
		t.Run(mode, func(t *testing.T) {
			svc, base, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, nil, nil, 2)
			repo := &activityTestRepository{schedulerConversationAffinityRepo: base, expired: mode == "expired_thread"}
			svc.accountRepo = repo
			req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
			req.SessionHash = "session"
			ctx, _ = withOpenAICodexThreadAffinityTestState(ctx, strings.Repeat("b", 64))
			if mode == "opaque_state" {
				ctx = ContextWithOpenAIContinuationState(ctx, http.Header{"X-Codex-Turn-State": []string{"ts1.old"}}, nil)
			}
			if mode == "missing_parent" {
				ctx, _ = withOpenAICodexThreadAffinityTestState(ctx, strings.Repeat("b", 64), strings.Repeat("c", 64))
			}
			selection, handled, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
			require.Nil(t, selection)
			if mode == "new_root" {
				require.NoError(t, err)
				require.False(t, handled)
			} else {
				require.ErrorIs(t, err, ErrOpenAIConversationResetRequired)
				require.True(t, handled)
			}
			require.Empty(t, base.reservations)
		})
	}
}

func TestOpenAIConversationExpiredWSTurnAndOwnerCacheMiss(t *testing.T) {
	svc, base, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, nil, nil, 2)
	repo := &activityTestRepository{schedulerConversationAffinityRepo: base, expired: true}
	svc.accountRepo = repo
	require.ErrorIs(t, svc.ResumeOpenAIConversationActivity(ctx, 61), ErrOpenAIConversationResetRequired)
	require.Zero(t, repo.acquired.Load())
	owned, err := svc.ValidateOpenAIHTTPResponseOwner(ctx, 4, "resp_expired", 42, 77)
	require.ErrorIs(t, err, ErrOpenAIConversationResetRequired)
	require.False(t, owned)
	repo.expired = false
	owned, err = svc.ValidateOpenAIHTTPResponseOwner(ctx, 4, "resp_unknown", 42, 77)
	require.NoError(t, err)
	require.False(t, owned)
}

func TestOpenAIConversationActivityReleasedOnEveryTerminalPath(t *testing.T) {
	for _, terminal := range []string{"failure", "success", "cancel"} {
		t.Run(terminal, func(t *testing.T) {
			svc, base, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, nil, nil, 2)
			repo := &activityTestRepository{schedulerConversationAffinityRepo: base}
			svc.accountRepo = repo
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			svc.rememberOpenAIUserAffinityConversationAttempt(ctx, &OpenAIUserConversationBinding{ID: 1, UserID: 42, APIKeyID: 77, AccountID: 61, ScopeKey: "test", SlotGeneration: 1, FirstOutputCommitted: true}, DefaultOpenAIUserAffinityConfig(), "")
			require.NoError(t, svc.BeginOpenAIConversationActivity(ctx, 61))
			require.NoError(t, svc.BeginOpenAIConversationActivity(ctx, 61))
			require.Equal(t, int32(1), repo.acquired.Load())
			switch terminal {
			case "failure":
				svc.RecordOpenAIUserAffinityFailure(ctx, 61)
			case "success":
				svc.RecordOpenAIUserAffinitySuccess(ctx, 61)
			case "cancel":
				cancel()
			}
			require.Eventually(t, func() bool { return repo.released.Load() == 1 }, time.Second, time.Millisecond)
			svc.endOpenAIConversationActivity(ctx)
			require.Equal(t, int32(1), repo.released.Load())
		})
	}
}

func TestOpenAIGatewayService_ThreadOnlyContinuationRestoresBeforeCapacityFilter(t *testing.T) {
	for _, tc := range []struct {
		name, previous         string
		spare, wantUnavailable bool
		wantReserve            int
	}{
		{"thread_only_spare_account", "", true, false, 1},
		{"thread_only_all_full", "", false, false, 1},
		{"previous_response_spare_account", "resp_diagnostic", true, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			scope := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
			accounts := []Account{
				{ID: 61, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1},
				{ID: 60, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1},
			}
			slots := []OpenAIUserResidentSlot{{ID: 1, UserID: 42, ScopeKey: scope, AccountID: 61, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, ExpiresAt: now.Add(time.Hour)}}
			svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
			repo.binding = &OpenAIUserConversationBinding{ID: 1, UserID: 42, APIKeyID: 77, ScopeKey: scope, ConversationHash: strings.Repeat("a", 64), ResidentSlotID: 1, AccountID: 61, SlotGeneration: 1, Status: "active", ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour)}
			bindOpenAIUserAffinityTestExecutionTarget(svc, repo.binding)
			selfHash := strings.Repeat("b", 64)
			ctx, _ = withOpenAICodexThreadAffinityTestState(ctx, selfHash)
			repo.aliasBindings = map[string]*OpenAIUserConversationBinding{openAICodexThreadAliasTestKey(nil, selfHash): repo.binding}
			limit := 1
			candidates := []OpenAIPersonaCapacityCandidate{{Persona: OpenAIAccountPersona{ID: 611, AccountID: 61, ProfileID: SessionPersonaCodexCLIStrict, MaxActiveUsersOverride: &limit}, ActiveUsers: 1}}
			if tc.spare {
				candidates = append(candidates, OpenAIPersonaCapacityCandidate{Persona: OpenAIAccountPersona{ID: 601, AccountID: 60, ProfileID: SessionPersonaCodexCLIStrict, MaxActiveUsersOverride: &limit}})
			}
			capRepo := &openAIBoundPersonaReservationRepo{candidates: candidates, reserveErr: ErrOpenAIPersonaUserCapacity}
			svc.personaUserReservationRepo = capRepo
			selection, _, err := svc.selectAccountWithScheduler(ctx, nil, tc.previous, "diagnostic-existing-session", "", nil, OpenAIUpstreamTransportHTTPSSE, "", "", false, PlatformOpenAI, true, false)
			require.Nil(t, selection)
			if tc.wantUnavailable {
				require.ErrorIs(t, err, ErrOpenAIPreviousResponseAccountUnavailable)
			} else {
				require.ErrorIs(t, err, ErrOpenAIPersonaUserCapacity)
			}
			require.Len(t, capRepo.reserveInputs, tc.wantReserve)
			require.Zero(t, capRepo.listCalls, "续链不能进入新根容量预筛选")
			require.Equal(t, int64(61), repo.binding.AccountID)
			require.Empty(t, repo.failovers)
			t.Logf("observed_error=%v exact_persona_reservation_calls=%d binding_account=%d", err, len(capRepo.reserveInputs), repo.binding.AccountID)
		})
	}
}
