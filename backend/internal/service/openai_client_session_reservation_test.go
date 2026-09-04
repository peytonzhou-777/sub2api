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
	OpenAIClientSessionReservationRepository
	personaLeaseID int64
	reserveErr     error
	rollbackTokens []string
}

func (r *openAIBoundPersonaReservationRepo) ReservePersonaSession(context.Context, OpenAIPersonaSessionReserveInput) (*OpenAIClientSessionLeaseReservation, error) {
	if r.reserveErr != nil {
		return nil, r.reserveErr
	}
	return &OpenAIClientSessionLeaseReservation{LeaseID: r.personaLeaseID}, nil
}

func (r *openAIBoundPersonaReservationRepo) RollbackClientSessionReservation(_ context.Context, token string, _ time.Time) error {
	r.rollbackTokens = append(r.rollbackTokens, token)
	return nil
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
	*openAIClientSessionReservationState,
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
	state := &openAIClientSessionReservationState{
		repo: reservationRepo, token: "client-reservation", userLeaseID: 801,
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
	require.Equal(t, int64(801), store.targets[0].UserGroupLeaseID)
	require.Equal(t, int64(901), store.targets[0].PersonaLeaseID)
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
