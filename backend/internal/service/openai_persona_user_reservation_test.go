package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type openAIBoundPersonaReservationRepo struct {
	OpenAIPersonaUserReservationRepository
	personaLeaseID int64
	reserveErr     error
	reserveInputs  []OpenAIPersonaUserReserveInput
	rollbackTokens []string
	commits        []OpenAIPersonaUserReservationCommit
	candidates     []OpenAIPersonaCapacityCandidate
	listCalls      int
}

func (r *openAIBoundPersonaReservationRepo) ReservePersonaUser(_ context.Context, input OpenAIPersonaUserReserveInput) (*OpenAIPersonaUserLeaseReservation, error) {
	r.reserveInputs = append(r.reserveInputs, input)
	if r.reserveErr != nil {
		return nil, r.reserveErr
	}
	return &OpenAIPersonaUserLeaseReservation{LeaseID: r.personaLeaseID}, nil
}

func (r *openAIBoundPersonaReservationRepo) RollbackPersonaUserReservation(_ context.Context, token string, _ time.Time) error {
	r.rollbackTokens = append(r.rollbackTokens, token)
	return nil
}

func (r *openAIBoundPersonaReservationRepo) CommitPersonaUserReservation(_ context.Context, commit OpenAIPersonaUserReservationCommit) (OpenAIExecutionTarget, error) {
	r.commits = append(r.commits, commit)
	return OpenAIExecutionTarget{}, nil
}

func (r *openAIBoundPersonaReservationRepo) ListOpenAIPersonaCapacityCandidates(context.Context, []int64, int64, time.Time) ([]OpenAIPersonaCapacityCandidate, error) {
	r.listCalls++
	return append([]OpenAIPersonaCapacityCandidate(nil), r.candidates...), nil
}

type openAIExecutionTargetBindingStore struct {
	AccountRepository
	OpenAIUserAffinityConversationStore
	bindErr     error
	transitions []OpenAIUserConversationTransition
	targets     []OpenAIExecutionTarget
}

func (r *openAIExecutionTargetBindingStore) BindOpenAIUserConversationExecutionTarget(
	_ context.Context,
	transition OpenAIUserConversationTransition,
	target OpenAIExecutionTarget,
) error {
	r.transitions = append(r.transitions, transition)
	r.targets = append(r.targets, target)
	return r.bindErr
}

func openAIBoundPersonaReservationTestFixture(t *testing.T, provisionalToken string) (
	*OpenAIGatewayService,
	*openAIExecutionTargetBindingStore,
	*openAIBoundPersonaReservationRepo,
	context.Context,
	*AccountSelectionResult,
	*openAIPersonaUserReservationState,
) {
	t.Helper()
	const accountID int64 = 61
	startedAt := time.Now().UTC().Add(-time.Minute)
	persona := OpenAIAccountPersona{
		ID: 55, AccountID: accountID, ProfileID: SessionPersonaCodexCLIStrict,
		ProfileVersion: CodexOutboundProfileCLI0149, State: OpenAIAccountPersonaStateActive,
		Enabled: true, PersonaGeneration: 3, CurrentCredentialChainID: "chain-55",
		CurrentSessionEpoch: 7, DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		InstallationID: "install-55",
	}
	session := OpenAIAccountPersonaSession{
		AccountPersonaID: persona.ID, SessionEpoch: 7, UpstreamSessionID: "session-55-7",
		State: OpenAIPersonaSessionCurrent, PersonaGeneration: 3, CredentialChainID: "chain-55",
		ProfileID: persona.ProfileID, ProfileVersion: persona.ProfileVersion,
		InstallationID: persona.InstallationID, StartedAt: startedAt,
	}
	target, err := OpenAIExecutionTargetFromPersonaSession(persona, session)
	require.NoError(t, err)
	store := &openAIExecutionTargetBindingStore{}
	reservationRepo := &openAIBoundPersonaReservationRepo{personaLeaseID: 901}
	svc := &OpenAIGatewayService{
		accountRepo: store,
		accountPersonaRepo: &openAIExecutionTargetRestorePersonaRepo{
			persona: persona,
			session: session,
		},
		settingService: NewSettingService(nil, nil),
	}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(35))
	ctx = context.WithValue(ctx, ctxkey.APIKeyID, int64(300))
	ctx = context.WithValue(ctx, ctxkey.RequestID, "bound-persona-child")
	svc.rememberOpenAIUserAffinityConversationAttempt(ctx, &OpenAIUserConversationBinding{
		ID: 97097, UserID: 35, APIKeyID: 300, ScopeKey: "openai",
		ConversationHash: strings.Repeat("a", 64), ResidentSlotID: 8, AccountID: accountID,
		SlotGeneration: 2, BindingEpoch: OpenAIConversationBindingEpoch,
		Status: "provisional", ProvisionalToken: provisionalToken,
	}, DefaultOpenAIUserAffinityConfig(), provisionalToken)
	selection := &AccountSelectionResult{
		Account:         &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		ExecutionTarget: &target,
	}
	state := &openAIPersonaUserReservationState{
		repo: reservationRepo, token: "client-reservation",
	}
	return svc, store, reservationRepo, ctx, selection, state
}

func TestAttachBoundOpenAIPersonaReservationPersistsProvisionalChildTarget(t *testing.T) {
	svc, store, reservationRepo, ctx, selection, state := openAIBoundPersonaReservationTestFixture(t, "child-provisional")
	clientHash := strings.Repeat("b", 64)

	err := svc.attachBoundOpenAIPersonaReservation(ctx, selection, state, clientHash)

	require.NoError(t, err)
	require.Empty(t, reservationRepo.rollbackTokens)
	require.Len(t, store.transitions, 1)
	require.Equal(t, int64(97097), store.transitions[0].BindingID)
	require.Equal(t, "child-provisional", store.transitions[0].ProvisionalToken)
	require.Equal(t, clientHash, store.transitions[0].RootClientSessionHash)
	require.Equal(t, int64(55), store.targets[0].AccountPersonaID)
	require.Equal(t, int64(7), store.targets[0].SessionEpoch)
	require.Equal(t, "chain-55", store.targets[0].CredentialChainID)
	require.Equal(t, int64(901), store.targets[0].PersonaUserLeaseID)
	require.Len(t, reservationRepo.reserveInputs, 1)
	require.Equal(t, int64(35), reservationRepo.reserveInputs[0].UserID)
	require.True(t, reservationRepo.reserveInputs[0].ExistingThread)
}

func TestAttachBoundOpenAIPersonaReservationRollsBackWhenChildTargetPersistenceFails(t *testing.T) {
	svc, store, reservationRepo, ctx, selection, state := openAIBoundPersonaReservationTestFixture(t, "child-provisional")
	store.bindErr = errors.New("persist child target")

	err := svc.attachBoundOpenAIPersonaReservation(ctx, selection, state, strings.Repeat("c", 64))

	require.ErrorContains(t, err, "persist child target")
	require.Equal(t, []string{"client-reservation"}, reservationRepo.rollbackTokens)
	require.True(t, state.rolledBack)
	require.Len(t, store.transitions, 1)
}

func TestAttachBoundOpenAIPersonaReservationDoesNotRewriteCommittedContinuation(t *testing.T) {
	svc, store, reservationRepo, ctx, selection, state := openAIBoundPersonaReservationTestFixture(t, "")

	err := svc.attachBoundOpenAIPersonaReservation(ctx, selection, state, strings.Repeat("d", 64))

	require.NoError(t, err)
	require.Empty(t, store.transitions)
	require.Empty(t, reservationRepo.rollbackTokens)
}

func TestAccountPermitHandoffKeepsPersonaUserReservationUntilAccepted(t *testing.T) {
	svc, _, reservationRepo, ctx, selection, state := openAIBoundPersonaReservationTestFixture(t, "")
	state.activeTTL = 40 * time.Minute
	permitReleases := 0
	selection.Acquired = true
	selection.ReleaseFunc = func() { permitReleases++ }

	require.NoError(t, svc.attachBoundOpenAIPersonaReservation(ctx, selection, state, strings.Repeat("e", 64)))
	selection.ReleaseAccountPermit()
	selection.ReleaseAccountPermit()

	require.Equal(t, 1, permitReleases)
	require.Empty(t, reservationRepo.rollbackTokens, "统一准入交接不能回滚 Persona 用户占用")
	require.False(t, state.rolledBack)

	acceptedCtx := ContextWithSelectionProfitGate(ctx, selection)
	svc.RecordOpenAIUserAffinityAccepted(acceptedCtx, selection.Account.ID)
	require.Len(t, reservationRepo.commits, 1)
	require.Equal(t, "client-reservation", reservationRepo.commits[0].ReservationToken)
	require.WithinDuration(t, reservationRepo.commits[0].Now.Add(40*time.Minute), reservationRepo.commits[0].ActiveUntil, time.Second)
	require.True(t, state.committed)

	selection.ReleaseFunc()
	require.Equal(t, 1, permitReleases)
	require.Empty(t, reservationRepo.rollbackTokens, "accepted 后释放执行槽不得缩短用户 lease")
}

func TestAttachOpenAIPersonaReservationPrefersPersonaAlreadyOccupiedByUser(t *testing.T) {
	svc, _, reservationRepo, ctx, selection, state := openAIBoundPersonaReservationTestFixture(t, "")
	selection.ExecutionTarget = nil
	basePersona := OpenAIAccountPersona{
		AccountID: selection.Account.ID, ProfileID: SessionPersonaCodexCLIStrict,
		ProfileVersion: CodexOutboundProfileCLI0149, State: OpenAIAccountPersonaStateActive,
		Enabled: true, PersonaGeneration: 1, CurrentCredentialChainID: "chain",
		CurrentSessionEpoch: 1, DeviceSeed: []byte("0123456789abcdef0123456789abcdef"), InstallationID: "install",
	}
	baseSession := OpenAIAccountPersonaSession{
		SessionEpoch: 1, State: OpenAIPersonaSessionCurrent, PersonaGeneration: 1,
		CredentialChainID: "chain", ProfileID: SessionPersonaCodexCLIStrict,
		ProfileVersion: CodexOutboundProfileCLI0149, InstallationID: "install",
		UpstreamSessionID: "session", StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	firstPersona := basePersona
	firstPersona.ID = 101
	firstPersona.Position = 0
	firstSession := baseSession
	firstSession.AccountPersonaID = firstPersona.ID
	secondPersona := basePersona
	secondPersona.ID = 102
	secondPersona.Position = 1
	secondPersona.ProfileID = SessionPersonaOpenCode
	secondPersona.ProfileVersion = SessionPersonaOpenCodeVersion
	secondSession := baseSession
	secondSession.AccountPersonaID = secondPersona.ID
	secondSession.ProfileID = secondPersona.ProfileID
	secondSession.ProfileVersion = secondPersona.ProfileVersion
	reservationRepo.candidates = []OpenAIPersonaCapacityCandidate{
		{Persona: firstPersona, Session: firstSession, ActiveUsers: 0},
		{Persona: secondPersona, Session: secondSession, ActiveUsers: 1, UserAlreadyActive: true},
	}

	err := svc.attachOpenAIPersonaReservation(ctx, selection, state, "different-client-session")

	require.NoError(t, err)
	require.Equal(t, int64(102), selection.ExecutionTarget.AccountPersonaID)
	require.Len(t, reservationRepo.reserveInputs, 1)
	require.Equal(t, int64(102), reservationRepo.reserveInputs[0].AccountPersonaID)
}

func TestWaitPlanPersonaUserReservationCommitsWithoutSchedulerPermit(t *testing.T) {
	svc, _, reservationRepo, ctx, selection, state := openAIBoundPersonaReservationTestFixture(t, "")
	selection.Acquired = false
	selection.ReleaseFunc = nil
	selection.WaitPlan = &AccountWaitPlan{AccountID: selection.Account.ID, MaxConcurrency: 1, Timeout: time.Second}

	require.NoError(t, svc.attachBoundOpenAIPersonaReservation(ctx, selection, state, strings.Repeat("f", 64)))
	require.Nil(t, selection.AccountPermitReleaseFunc())
	svc.RecordOpenAIUserAffinityAccepted(ContextWithSelectionProfitGate(ctx, selection), selection.Account.ID)

	require.Len(t, reservationRepo.commits, 1)
	require.Empty(t, reservationRepo.rollbackTokens)
}

func TestAbandonedPersonaUserReservationRollsBackExactlyOnce(t *testing.T) {
	svc, _, reservationRepo, ctx, selection, state := openAIBoundPersonaReservationTestFixture(t, "")
	permitReleases := 0
	selection.Acquired = true
	selection.ReleaseFunc = func() { permitReleases++ }
	require.NoError(t, svc.attachBoundOpenAIPersonaReservation(ctx, selection, state, strings.Repeat("1", 64)))

	selection.ReleaseFunc()
	selection.ReleaseFunc()
	selection.RollbackOpenAIPersonaUserReservation()

	require.Equal(t, 1, permitReleases)
	require.Equal(t, []string{"client-reservation"}, reservationRepo.rollbackTokens)
	require.True(t, state.rolledBack)
	require.Empty(t, reservationRepo.commits)
}
