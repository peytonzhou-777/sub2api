package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type schedulerAffinityAccountRepo struct {
	schedulerTestOpenAIAccountRepo
	OpenAIUserAffinityRuntimeStore
	placement           *OpenAIUserPlacement
	stats               map[int64]OpenAIUserAffinityCandidate
	authorizedAt        *time.Time
	authorizedByAccount map[int64]*time.Time
	recordedIncidents   []OpenAIUserAffinityIncidentIdentity
	resetExclusions     []int64
}

func (r *schedulerAffinityAccountRepo) GetOpenAIUserAffinityCandidateStats(_ context.Context, _ int64, accountIDs []int64) (map[int64]OpenAIUserAffinityCandidate, error) {
	result := make(map[int64]OpenAIUserAffinityCandidate, len(accountIDs))
	for _, accountID := range accountIDs {
		result[accountID] = r.stats[accountID]
	}
	return result, nil
}

func (r *schedulerAffinityAccountRepo) PredictOpenAIUserAffinityDemand(context.Context, int64, float64) (OpenAIUserAffinityDemand, error) {
	return OpenAIUserAffinityDemand{Demand5H: 0.05, Demand7D: 0.05, Version: "test"}, nil
}

func (r *schedulerAffinityAccountRepo) ListOpenAIUserAffinityResetExcludedAccountIDs(context.Context, int64, string) ([]int64, error) {
	return append([]int64(nil), r.resetExclusions...), nil
}

func (r *schedulerAffinityAccountRepo) GetOpenAIUserPlacement(_ context.Context, userID int64, scopeKey string) (*OpenAIUserPlacement, error) {
	if r.placement != nil && r.placement.UserID == userID && r.placement.ScopeKey == scopeKey {
		copy := *r.placement
		return &copy, nil
	}
	return nil, nil
}

func (r *schedulerAffinityAccountRepo) UpsertOpenAIUserPlacement(context.Context, OpenAIUserPlacement) error {
	return nil
}

func (r *schedulerAffinityAccountRepo) AssignOpenAIUserAffinityPlacement(_ context.Context, placement OpenAIUserPlacement, _ OpenAIUserAffinityConfig) (bool, error) {
	copy := placement
	r.placement = &copy
	return true, nil
}

func (r *schedulerAffinityAccountRepo) RecordOpenAIUserPlacementEvent(context.Context, OpenAIUserPlacementEvent) error {
	return nil
}

func (r *schedulerAffinityAccountRepo) RecordOpenAIUserAffinityCapacityFailure(_ context.Context, incident OpenAIUserAffinityIncidentIdentity, _, _ string, _ OpenAIUserAffinityConfig) (*time.Time, error) {
	r.recordedIncidents = append(r.recordedIncidents, incident)
	if r.authorizedByAccount != nil {
		return r.authorizedByAccount[incident.AccountID], nil
	}
	return r.authorizedAt, nil
}

func (r *schedulerAffinityAccountRepo) GetOpenAIUserAffinityMigrationAuthorizedAt(_ context.Context, incident OpenAIUserAffinityIncidentIdentity) (*time.Time, error) {
	if r.authorizedByAccount != nil {
		return r.authorizedByAccount[incident.AccountID], nil
	}
	return r.authorizedAt, nil
}

func (r *schedulerAffinityAccountRepo) BeginOpenAIUserAffinityReentry(context.Context, OpenAIUserAffinityReentryBegin) (*OpenAIUserAffinityReentryAdmission, error) {
	return &OpenAIUserAffinityReentryAdmission{Required: false}, nil
}

type schedulerAffinityCache struct {
	*schedulerTestGatewayCache
	OpenAIUserAffinityReentryQueue
}

type schedulerConversationAffinityRepo struct {
	*schedulerAffinityAccountRepo
	binding                    *OpenAIUserConversationBinding
	bindingsByHash             map[string]*OpenAIUserConversationBinding
	bindingLookups             []string
	slots                      []OpenAIUserResidentSlot
	reservations               []OpenAIUserConversationReservation
	failovers                  []OpenAIUserConversationFailoverReservation
	replacements               []OpenAIUserResidentSlotReplacementReservation
	activeRoute                *OpenAIUserActiveRoute
	occupancies                map[int64]OpenAIAccountSoftOccupancy
	converges                  int
	evictedSlots               []OpenAIUserResidentSlotVersion
	aliasType                  string
	aliasHash                  string
	aliasBindings              map[string]*OpenAIUserConversationBinding
	reservationErrors          map[int64]error
	slotsAfterReservationError map[int64][]OpenAIUserResidentSlot
	bindingValid               *bool
	commitTransitions          []OpenAIUserConversationTransition
	rollbackTransitions        []OpenAIUserConversationTransition
}

type missingAccountConversationAffinityRepo struct {
	*schedulerConversationAffinityRepo
}

func (r *missingAccountConversationAffinityRepo) GetByID(context.Context, int64) (*Account, error) {
	return nil, ErrAccountNotFound
}

func withOpenAICodexThreadAffinityTestState(ctx context.Context, selfHash string, parentHashes ...string) (context.Context, *openAICodexThreadAffinityState) {
	state := &openAICodexThreadAffinityState{
		selfAliasHash:     selfHash,
		parentAliasHashes: append([]string(nil), parentHashes...),
		internalSubagent:  len(parentHashes) > 0,
	}
	return context.WithValue(ctx, openAICodexThreadAffinityContextKey{}, state), state
}

func openAICodexThreadAliasTestKey(groupID *int64, hash string) string {
	return openAICodexThreadAliasScopeKey(groupID) + "\x00" + openAICodexThreadAliasType + "\x00" + hash
}

func (r *schedulerConversationAffinityRepo) ConvergeOpenAIUserResidentSlots(context.Context, int64, string, OpenAIUserAffinityConfig, time.Time) error {
	r.converges++
	return nil
}

func (r *schedulerConversationAffinityRepo) EvictOpenAIUserResidentSlot(_ context.Context, _ int64, _ string, slotID, accountID, generation int64, _ string, _ time.Time) (bool, error) {
	for i := range r.slots {
		if r.slots[i].ID != slotID || r.slots[i].AccountID != accountID || r.slots[i].Generation != generation {
			continue
		}
		r.slots[i].Status = OpenAIUserResidentSlotStatusDraining
		r.slots[i].ProvisionalToken = ""
		r.evictedSlots = append(r.evictedSlots, OpenAIUserResidentSlotVersion{ID: slotID, AccountID: accountID, Generation: generation})
		return true, nil
	}
	return false, nil
}

func (r *schedulerConversationAffinityRepo) ListOpenAIUserResidentSlots(context.Context, int64, string) ([]OpenAIUserResidentSlot, error) {
	return append([]OpenAIUserResidentSlot(nil), r.slots...), nil
}

func (r *schedulerConversationAffinityRepo) GetOpenAIUserActiveRoute(context.Context, int64, string) (*OpenAIUserActiveRoute, error) {
	if r.activeRoute == nil {
		return nil, nil
	}
	copy := *r.activeRoute
	return &copy, nil
}

func (r *schedulerConversationAffinityRepo) ListOpenAIAccountSoftOccupancies(_ context.Context, accountIDs []int64) (map[int64]OpenAIAccountSoftOccupancy, error) {
	result := make(map[int64]OpenAIAccountSoftOccupancy, len(accountIDs))
	for _, accountID := range accountIDs {
		result[accountID] = r.occupancies[accountID]
	}
	return result, nil
}

func (r *schedulerConversationAffinityRepo) GetOpenAIUserConversationBinding(_ context.Context, _, _ int64, _, conversationHash string) (*OpenAIUserConversationBinding, error) {
	r.bindingLookups = append(r.bindingLookups, conversationHash)
	binding := r.binding
	if r.bindingsByHash != nil {
		binding = r.bindingsByHash[conversationHash]
	}
	if binding == nil {
		return nil, nil
	}
	copy := *binding
	return &copy, nil
}

func (r *schedulerConversationAffinityRepo) GetOpenAIUserConversationBindingByAlias(_ context.Context, _, _ int64, scopeKey, aliasType, aliasHash string) (*OpenAIUserConversationBinding, error) {
	r.aliasType = aliasType
	r.aliasHash = aliasHash
	if binding := r.aliasBindings[scopeKey+"\x00"+aliasType+"\x00"+aliasHash]; binding != nil {
		copy := *binding
		return &copy, nil
	}
	return nil, nil
}

func (r *schedulerConversationAffinityRepo) ValidateOpenAIUserConversationBinding(_ context.Context, _ OpenAIUserConversationBinding) (bool, error) {
	if r.bindingValid != nil {
		return *r.bindingValid, nil
	}
	return true, nil
}

func (r *schedulerConversationAffinityRepo) ReserveOpenAIUserConversationBinding(_ context.Context, reservation OpenAIUserConversationReservation) (*OpenAIUserConversationBinding, bool, error) {
	r.reservations = append(r.reservations, reservation)
	if reserveErr := r.reservationErrors[reservation.AccountID]; reserveErr != nil {
		delete(r.reservationErrors, reservation.AccountID)
		if slots, ok := r.slotsAfterReservationError[reservation.AccountID]; ok {
			r.slots = append([]OpenAIUserResidentSlot(nil), slots...)
		}
		return nil, false, reserveErr
	}
	if existing := r.bindingsByHash[reservation.ConversationHash]; existing != nil {
		copy := *existing
		r.binding = &copy
		if r.aliasBindings == nil {
			r.aliasBindings = make(map[string]*OpenAIUserConversationBinding)
		}
		for _, alias := range reservation.Aliases {
			aliasCopy := copy
			r.aliasBindings[alias.ScopeKey+"\x00"+alias.Type+"\x00"+alias.Hash] = &aliasCopy
		}
		return &copy, false, nil
	}
	residentSlotID := int64(100 + len(r.reservations))
	slotGeneration := reservation.PlacementGeneration
	if reservation.PreferredResidentSlotID > 0 && reservation.PreferredSlotGeneration > 0 {
		residentSlotID = reservation.PreferredResidentSlotID
		slotGeneration = reservation.PreferredSlotGeneration
	}
	if reservation.PreferredResidentSlotID == 0 {
		for _, slot := range r.slots {
			if slot.AccountID == reservation.AccountID {
				residentSlotID = slot.ID
				slotGeneration = slot.Generation
				break
			}
		}
	}
	r.binding = &OpenAIUserConversationBinding{
		ID: int64(200 + len(r.reservations)), UserID: reservation.UserID, APIKeyID: reservation.APIKeyID,
		ScopeKey: reservation.ScopeKey, ConversationHash: reservation.ConversationHash,
		ResidentSlotID: residentSlotID, AccountID: reservation.AccountID, SlotGeneration: slotGeneration,
		BindingEpoch: OpenAIConversationBindingEpoch,
		Status:       "provisional", ContextRebuildable: reservation.ContextRebuildable,
		ExpiresAt: time.Now().UTC().Add(2 * time.Minute), ProvisionalToken: reservation.ProvisionalToken,
		ManageActiveRoute: reservation.ManageActiveRoute,
	}
	if reservation.ManageActiveRoute && (r.activeRoute == nil || r.activeRoute.AccountID != reservation.AccountID ||
		r.activeRoute.ResidentSlotID != residentSlotID || r.activeRoute.SlotGeneration != slotGeneration) {
		r.binding.ActiveRoutePending = true
	}
	if r.aliasBindings == nil {
		r.aliasBindings = make(map[string]*OpenAIUserConversationBinding)
	}
	for _, alias := range reservation.Aliases {
		copy := *r.binding
		r.aliasBindings[alias.ScopeKey+"\x00"+alias.Type+"\x00"+alias.Hash] = &copy
	}
	return r.binding, true, nil
}

func (r *schedulerConversationAffinityRepo) CommitOpenAIUserConversationBinding(_ context.Context, transition OpenAIUserConversationTransition) (bool, error) {
	r.commitTransitions = append(r.commitTransitions, transition)
	if r.binding == nil || r.binding.ID != transition.BindingID {
		return false, nil
	}
	r.binding.AccountID = transition.AccountID
	r.binding.ResidentSlotID = transition.ResidentSlotID
	r.binding.SlotGeneration = transition.SlotGeneration
	r.binding.Status = "active"
	r.binding.FirstOutputCommitted = true
	r.binding.ProvisionalToken = ""
	if r.aliasBindings == nil {
		r.aliasBindings = make(map[string]*OpenAIUserConversationBinding)
	}
	for _, alias := range transition.Aliases {
		copy := *r.binding
		r.aliasBindings[alias.ScopeKey+"\x00"+alias.Type+"\x00"+alias.Hash] = &copy
	}
	if transition.ResponseAliasHash != "" {
		copy := *r.binding
		r.aliasBindings[transition.ScopeKey+"\x00response_id\x00"+transition.ResponseAliasHash] = &copy
	}
	return true, nil
}

func (r *schedulerConversationAffinityRepo) BindOpenAIUserConversationExecutionTarget(_ context.Context, transition OpenAIUserConversationTransition, target OpenAIExecutionTarget) error {
	if r.binding == nil || r.binding.ID != transition.BindingID {
		return ErrOpenAIUserAffinityReservationConflict
	}
	r.binding.AccountPersonaID = target.AccountPersonaID
	r.binding.PersonaSessionEpoch = target.SessionEpoch
	r.binding.CredentialChainID = target.CredentialChainID
	r.binding.RootClientSessionHash = transition.RootClientSessionHash
	r.binding.UserGroupLeaseID = 0
	r.binding.ProfileID = target.ProfileID
	r.binding.ProfileVersion = target.ProfileVersion
	return nil
}

func (r *schedulerConversationAffinityRepo) RollbackOpenAIUserConversationBinding(_ context.Context, transition OpenAIUserConversationTransition) (bool, error) {
	r.rollbackTransitions = append(r.rollbackTransitions, transition)
	if r.binding == nil || r.binding.ID != transition.BindingID ||
		(r.binding.Status != "provisional" && !transition.Failover) {
		return false, nil
	}
	if !transition.Failover {
		for key, binding := range r.aliasBindings {
			if binding != nil && binding.ID == transition.BindingID {
				delete(r.aliasBindings, key)
			}
		}
		r.binding = nil
	}
	return true, nil
}

func (r *schedulerConversationAffinityRepo) ReserveOpenAIUserConversationFailover(_ context.Context, reservation OpenAIUserConversationFailoverReservation) (*OpenAIUserConversationTransition, bool, error) {
	r.failovers = append(r.failovers, reservation)
	return &OpenAIUserConversationTransition{
		BindingID: reservation.BindingID, UserID: reservation.UserID, APIKeyID: reservation.APIKeyID, ScopeKey: reservation.ScopeKey,
		ConversationHash: reservation.ConversationHash, ResidentSlotID: reservation.TargetResidentSlotID,
		AccountID: reservation.TargetAccountID, SlotGeneration: reservation.TargetSlotGeneration,
		ProvisionalToken: reservation.ProvisionalToken, Failover: true,
		SourceAccountID: reservation.SourceAccountID, SourceSlotID: reservation.SourceResidentSlotID,
		SourceGeneration: reservation.SourceSlotGeneration, DetachSource: reservation.DetachSource,
		Aliases: reservation.Aliases, Config: reservation.Config,
	}, true, nil
}

func (r *schedulerConversationAffinityRepo) ReserveOpenAIUserResidentSlotReplacement(_ context.Context, reservation OpenAIUserResidentSlotReplacementReservation) (*OpenAIUserConversationTransition, bool, error) {
	r.replacements = append(r.replacements, reservation)
	return &OpenAIUserConversationTransition{
		BindingID: reservation.BindingID, UserID: reservation.UserID, APIKeyID: reservation.APIKeyID, ScopeKey: reservation.ScopeKey,
		ConversationHash: reservation.ConversationHash, ResidentSlotID: 901,
		AccountID: reservation.TargetAccountID, SlotGeneration: 9,
		ProvisionalToken: reservation.ProvisionalToken, Failover: true, Replacement: true,
		SourceAccountID: reservation.SourceAccountID, SourceSlotID: reservation.SourceResidentSlotID,
		SourceGeneration: reservation.SourceSlotGeneration, ReplacementSlotID: reservation.VictimSlotID,
		Aliases: reservation.Aliases, Config: reservation.Config,
	}, true, nil
}

func newMultiSlotAffinitySchedulerTestService(t *testing.T, slots []OpenAIUserResidentSlot, accounts []Account, stats map[int64]OpenAIUserAffinityCandidate, slotCount int) (*OpenAIGatewayService, *schedulerConversationAffinityRepo, context.Context) {
	t.Helper()
	configValue := DefaultOpenAIUserAffinityConfig()
	configValue.Enabled = true
	configValue.Mode = OpenAIUserAffinityModeEnforce
	configValue.ResidentAccountSlotCount = slotCount
	raw, err := json.Marshal(configValue)
	require.NoError(t, err)
	baseRepo := &schedulerAffinityAccountRepo{
		schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
		stats:                          stats,
	}
	repo := &schedulerConversationAffinityRepo{schedulerAffinityAccountRepo: baseRepo, slots: slots}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := &OpenAIGatewayService{
		accountRepo: repo, cfg: cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		settingService:     NewSettingService(&openAIUserAffinitySuccessSettingRepo{value: string(raw)}, cfg),
	}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	ctx = context.WithValue(ctx, ctxkey.APIKeyID, int64(77))
	ctx = context.WithValue(ctx, ctxkey.RequestID, "multi-slot-selection")
	return svc, repo, ctx
}

// bindOpenAIUserAffinityTestExecutionTarget 为 OAuth 续链测试补齐当前代的不可变 Persona 执行目标。
func bindOpenAIUserAffinityTestExecutionTarget(svc *OpenAIGatewayService, binding *OpenAIUserConversationBinding) {
	personaID := binding.AccountID*10 + 1
	chainID := "affinity-test-chain"
	installationID := "affinity-test-installation"
	startedAt := time.Now().UTC().Add(-time.Minute)
	binding.BindingEpoch = OpenAIConversationBindingEpoch
	binding.AccountPersonaID = personaID
	binding.PersonaSessionEpoch = 1
	binding.CredentialChainID = chainID
	binding.ProfileID = SessionPersonaCodexCLIStrict
	binding.ProfileVersion = CodexOutboundProfileCLI0149
	svc.accountPersonaRepo = &openAIExecutionTargetRestorePersonaRepo{
		persona: OpenAIAccountPersona{
			ID: personaID, AccountID: binding.AccountID, ProfileID: SessionPersonaCodexCLIStrict,
			ProfileVersion: CodexOutboundProfileCLI0149, State: OpenAIAccountPersonaStateActive,
			Enabled: true, PersonaGeneration: 1, CurrentCredentialChainID: chainID, CurrentSessionEpoch: 1,
			DeviceSeed: []byte("0123456789abcdef0123456789abcdef"), InstallationID: installationID,
		},
		session: OpenAIAccountPersonaSession{
			AccountPersonaID: personaID, SessionEpoch: 1, UpstreamSessionID: "affinity-test-session",
			State: OpenAIPersonaSessionCurrent, PersonaGeneration: 1, CredentialChainID: chainID,
			ProfileID: SessionPersonaCodexCLIStrict, ProfileVersion: CodexOutboundProfileCLI0149,
			InstallationID: installationID, StartedAt: startedAt,
		},
	}
}

func openAIUserAffinityTestQuotaExtra(now time.Time, used7D float64) map[string]any {
	return map[string]any{
		"codex_usage_updated_at":  now.Format(time.RFC3339Nano),
		"codex_7d_window_minutes": 10080,
		"codex_7d_used_percent":   used7D,
		"codex_7d_reset_at":       now.Add(6 * 24 * time.Hour).Format(time.RFC3339Nano),
	}
}

func TestOpenAIGatewayService_MultiSlotKeepsSoftOwnerOnActiveRoute(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36211, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 36212, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	slots := []OpenAIUserResidentSlot{
		{ID: 11, UserID: 42, ScopeKey: openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE), AccountID: 36211, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 2, ScoreUpdatedAt: now, AdmittedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour), ActiveRouteUserCount: 2, SoftOwnerUserID: 42},
		{ID: 12, UserID: 42, ScopeKey: openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE), AccountID: 36212, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 1, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	activeUntil := now.Add(time.Hour)
	repo.activeRoute = &OpenAIUserActiveRoute{UserID: 42, ScopeKey: slots[0].ScopeKey, ResidentSlotID: 11, AccountID: 36211, SlotGeneration: 1, ActiveUntil: &activeUntil}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "idle-resident"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36211), selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, int64(36211), repo.reservations[0].AccountID)
}

func TestOpenAIGatewayService_MultiSlotPrefersOccupiedPersonaAccountOverActiveRoute(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accounts := []Account{
		{ID: 36241, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 36242, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	slots := []OpenAIUserResidentSlot{
		{ID: 41, UserID: 42, ScopeKey: scopeKey, AccountID: 36241, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 4, ScoreUpdatedAt: now, AdmittedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour), SoftOwnerUserID: 42},
		{ID: 42, UserID: 42, ScopeKey: scopeKey, AccountID: 36242, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 1, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	activeUntil := now.Add(time.Hour)
	repo.activeRoute = &OpenAIUserActiveRoute{UserID: 42, ScopeKey: scopeKey, ResidentSlotID: 41, AccountID: 36241, SlotGeneration: 1, ActiveUntil: &activeUntil}
	ctx = contextWithOpenAIUserOccupiedPersonaAccounts(ctx, map[int64]struct{}{36242: {}})
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "occupied-persona-account"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, int64(36242), selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, int64(36242), repo.reservations[0].AccountID)
}

func TestOpenAIGatewayService_MultiSlotNonOwnerRetreatsOnNewConversation(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accounts := []Account{
		{ID: 36213, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 36214, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	slots := []OpenAIUserResidentSlot{
		{ID: 13, UserID: 42, ScopeKey: scopeKey, AccountID: 36213, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 4, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour), ActiveRouteUserCount: 2, SoftOwnerUserID: 99},
		{ID: 14, UserID: 42, ScopeKey: scopeKey, AccountID: 36214, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 1, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	activeUntil := now.Add(time.Hour)
	repo.activeRoute = &OpenAIUserActiveRoute{UserID: 42, ScopeKey: scopeKey, ResidentSlotID: 13, AccountID: 36213, SlotGeneration: 1, ActiveUntil: &activeUntil}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "non-owner-retreat"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36214), selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.True(t, repo.reservations[0].ManageActiveRoute)
}

func TestOpenAIGatewayService_MultiSlotNonOwnerKeepsSharingWithoutAlternative(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accounts := []Account{
		{ID: 36215, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 36216, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusError, Schedulable: false, Concurrency: 1},
	}
	slots := []OpenAIUserResidentSlot{{
		ID: 15, UserID: 42, ScopeKey: scopeKey, AccountID: 36215, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, UsageScore: 2, ScoreUpdatedAt: now,
		AdmittedAt: now, ExpiresAt: now.Add(time.Hour), ActiveRouteUserCount: 2, SoftOwnerUserID: 99,
	}, {
		ID: 16, UserID: 42, ScopeKey: scopeKey, AccountID: 36216, Generation: 2,
		Status: OpenAIUserResidentSlotStatusActive, UsageScore: 1, ScoreUpdatedAt: now,
		AdmittedAt: now, ExpiresAt: now.Add(time.Hour), ActiveRouteUserCount: 3, SoftOwnerUserID: 98,
	}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	activeUntil := now.Add(time.Hour)
	repo.activeRoute = &OpenAIUserActiveRoute{UserID: 42, ScopeKey: scopeKey, ResidentSlotID: 15, AccountID: 36215, SlotGeneration: 1, ActiveUntil: &activeUntil}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "non-owner-shared-fallback"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36215), selection.Account.ID)
}

func TestOpenAIGatewayService_EvictsHardUnavailableResidentAndFillsSlot(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accounts := []Account{
		{ID: 36218, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusError, Schedulable: false, Concurrency: 1},
		{ID: 36219, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
	}
	slots := []OpenAIUserResidentSlot{{
		ID: 18, UserID: 42, ScopeKey: scopeKey, AccountID: 36218, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour),
	}}
	stats := map[int64]OpenAIUserAffinityCandidate{36219: {AccountID: 36219}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, stats, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "hard-resident-eviction"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, int64(36219), selection.Account.ID)
	require.Len(t, repo.evictedSlots, 1)
	require.Equal(t, int64(18), repo.evictedSlots[0].ID)
	require.Equal(t, int64(36219), repo.reservations[0].AccountID)
	require.Empty(t, repo.recordedIncidents, "硬不可用不应进入临时容量失败窗口")
}

func TestOpenAIGatewayService_MultiSlotPendingRouteFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	account := Account{ID: 36217, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	slot := OpenAIUserResidentSlot{ID: 17, UserID: 42, ScopeKey: scopeKey, AccountID: 36217, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour)}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, []OpenAIUserResidentSlot{slot}, []Account{account}, nil, 2)
	pendingExpiresAt := now.Add(time.Minute)
	repo.activeRoute = &OpenAIUserActiveRoute{UserID: 42, ScopeKey: scopeKey, PendingAccountID: 36217, PendingResidentSlotID: 17, PendingSlotGeneration: 1, PendingExpiresAt: &pendingExpiresAt}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "pending-route"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, found)
	require.Nil(t, selection)
	require.Empty(t, repo.reservations)
}

func TestOpenAIGatewayService_MultiSlotFillsBestFitWhenAllResidentsBusy(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36221, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 50)},
		{ID: 36222, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
		{ID: 36223, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 30)},
	}
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	slots := []OpenAIUserResidentSlot{{
		ID: 21, UserID: 42, ScopeKey: scopeKey, AccountID: 36221, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, UsageScore: 1, ScoreUpdatedAt: now,
		AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour), ActiveRouteUserCount: 2, SoftOwnerUserID: 99,
	}}
	stats := map[int64]OpenAIUserAffinityCandidate{
		36222: {AccountID: 36222}, 36223: {AccountID: 36223},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, stats, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "fill-best-fit"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36222), selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, 2, repo.reservations[0].MaxResidentSlots)
}

func TestOpenAIGatewayService_MultiSlotFreshResidentIgnoresLegacySoftOwnership(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36241, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 5)},
		{ID: 36242, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 60)},
	}
	stats := map[int64]OpenAIUserAffinityCandidate{36241: {AccountID: 36241}, 36242: {AccountID: 36242}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, accounts, stats, 2)
	repo.occupancies = map[int64]OpenAIAccountSoftOccupancy{
		36241: {AccountID: 36241, ActiveUserCount: 1, OwnerUserID: 99},
	}
	selection, found, err := svc.selectOpenAIUserAffinityNewResident(
		ctx, nil, "", nil, false, "", "", OpenAIUpstreamTransportHTTPSSE,
		openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
		DefaultOpenAIUserAffinityConfig(), now, true,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36241), selection.Account.ID)
	require.Equal(t, int64(36241), *repo.placement.AccountID)
}

func TestOpenAIGatewayService_MultiSlotFreshResidentIgnoresLegacyOccupancyCounts(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36243, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 5)},
		{ID: 36244, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 40)},
	}
	stats := map[int64]OpenAIUserAffinityCandidate{36243: {AccountID: 36243}, 36244: {AccountID: 36244}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, accounts, stats, 2)
	repo.occupancies = map[int64]OpenAIAccountSoftOccupancy{
		36243: {AccountID: 36243, ActiveUserCount: 3, OwnerUserID: 91},
		36244: {AccountID: 36244, ActiveUserCount: 1, OwnerUserID: 92},
	}
	selection, found, err := svc.selectOpenAIUserAffinityNewResident(
		ctx, nil, "", nil, false, "", "", OpenAIUpstreamTransportHTTPSSE,
		openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
		DefaultOpenAIUserAffinityConfig(), now, true,
	)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36243), selection.Account.ID)
	require.Equal(t, int64(36243), *repo.placement.AccountID)
}

func TestOpenAIGatewayService_MultiSlotUnknownQuotaFallsBackForSnapshotHealing(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36224, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 36225, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	slots := []OpenAIUserResidentSlot{{
		ID: 24, UserID: 42, ScopeKey: scopeKey, AccountID: 36224, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(24 * time.Hour), ActiveRouteUserCount: 2, SoftOwnerUserID: 99,
	}}
	stats := map[int64]OpenAIUserAffinityCandidate{36225: {AccountID: 36225}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, stats, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "unknown-quota-slot-fill"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, selection)
	require.Empty(t, repo.reservations)
}

func TestOpenAIGatewayService_SingleSlotStillConvergesReducedConfiguration(t *testing.T) {
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, nil, nil, 1)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "single-slot-convergence"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, selection)
	require.Equal(t, 1, repo.converges, "2/3 槽降到 1 槽后仍须执行收敛")
}

func TestOpenAIGatewayService_MultiSlotUsesLeastSharedResidentWhenFull(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36231, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 36232, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	slots := []OpenAIUserResidentSlot{
		{ID: 31, UserID: 42, ScopeKey: scopeKey, AccountID: 36231, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 5, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour), ActiveRouteUserCount: 3, SoftOwnerUserID: 91},
		{ID: 32, UserID: 42, ScopeKey: scopeKey, AccountID: 36232, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 2, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour), ActiveRouteUserCount: 1, SoftOwnerUserID: 92},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "full-hottest"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36232), selection.Account.ID)
	require.Len(t, repo.reservations, 1)
}

func TestOpenAIGatewayService_DrainingConflictReloadsAndReusesActiveResident(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	activeAccount := Account{ID: 36281, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	drainingAccount := Account{ID: 36282, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 5)}
	activeSlot := OpenAIUserResidentSlot{
		ID: 81, UserID: 42, ScopeKey: scopeKey, AccountID: activeAccount.ID, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour), ActiveRouteUserCount: 2, SoftOwnerUserID: 99,
	}
	drainingSlot := OpenAIUserResidentSlot{
		ID: 82, UserID: 42, ScopeKey: scopeKey, AccountID: drainingAccount.ID, Generation: 2,
		Status: OpenAIUserResidentSlotStatusDraining, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t,
		[]OpenAIUserResidentSlot{activeSlot}, []Account{activeAccount, drainingAccount},
		map[int64]OpenAIUserAffinityCandidate{drainingAccount.ID: {AccountID: drainingAccount.ID}}, 2)
	repo.reservationErrors = map[int64]error{drainingAccount.ID: ErrOpenAIUserAffinityDrainingSlotConflict}
	repo.slotsAfterReservationError = map[int64][]OpenAIUserResidentSlot{
		drainingAccount.ID: {activeSlot, drainingSlot},
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "draining-conflict-reload"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, activeAccount.ID, selection.Account.ID)
	require.Len(t, repo.reservations, 2)
	require.Equal(t, drainingAccount.ID, repo.reservations[0].AccountID)
	require.Equal(t, activeAccount.ID, repo.reservations[1].AccountID)
}

func TestOpenAIGatewayService_AccountUnavailableReloadsAndReusesActiveResident(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	activeAccount := Account{ID: 36287, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	unavailableAccount := Account{ID: 36288, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 5)}
	activeSlot := OpenAIUserResidentSlot{
		ID: 87, UserID: 42, ScopeKey: scopeKey, AccountID: activeAccount.ID, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour), ActiveRouteUserCount: 2, SoftOwnerUserID: 99,
	}
	drainingSlot := OpenAIUserResidentSlot{
		ID: 88, UserID: 42, ScopeKey: scopeKey, AccountID: unavailableAccount.ID, Generation: 2,
		Status: OpenAIUserResidentSlotStatusDraining, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t,
		[]OpenAIUserResidentSlot{activeSlot}, []Account{activeAccount, unavailableAccount},
		map[int64]OpenAIUserAffinityCandidate{unavailableAccount.ID: {AccountID: unavailableAccount.ID}}, 2)
	repo.reservationErrors = map[int64]error{unavailableAccount.ID: ErrOpenAIUserAffinityAccountUnavailable}
	repo.slotsAfterReservationError = map[int64][]OpenAIUserResidentSlot{
		unavailableAccount.ID: {activeSlot, drainingSlot},
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "account-unavailable-reload"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, activeAccount.ID, selection.Account.ID)
	require.Len(t, repo.reservations, 2)
	require.Equal(t, unavailableAccount.ID, repo.reservations[0].AccountID)
	require.Equal(t, activeAccount.ID, repo.reservations[1].AccountID)
}

func TestOpenAIGatewayService_MissingAliasAndBindingRebuildsFromActiveRoute(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	account := Account{ID: 36283, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	slot := OpenAIUserResidentSlot{
		ID: 83, UserID: 42, ScopeKey: scopeKey, AccountID: account.ID, Generation: 4,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour), SoftOwnerUserID: 42,
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, []OpenAIUserResidentSlot{slot}, []Account{account}, nil, 2)
	activeUntil := now.Add(time.Hour)
	repo.activeRoute = &OpenAIUserActiveRoute{
		UserID: 42, ScopeKey: scopeKey, ResidentSlotID: slot.ID, AccountID: account.ID,
		SlotGeneration: slot.Generation, ActiveUntil: &activeUntil,
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "missing-response", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "missing-binding-active-route"
	req.PreviousResponseCanMove = true

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, selection)

	selection, found, err = svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, slot.ID, repo.binding.ResidentSlotID)
}

func TestOpenAIGatewayService_FullProvisionalResidentSlotIsReused(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	account := Account{ID: 36284, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	otherAccount := Account{ID: 36286, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1}
	slot := OpenAIUserResidentSlot{
		ID: 84, UserID: 42, ScopeKey: scopeKey, AccountID: account.ID, Generation: 7,
		Status: OpenAIUserResidentSlotStatusProvisional, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	otherSlot := OpenAIUserResidentSlot{
		ID: 86, UserID: 42, ScopeKey: scopeKey, AccountID: otherAccount.ID, Generation: 8,
		Status: OpenAIUserResidentSlotStatusReplacementPending, ScoreUpdatedAt: now.Add(-time.Minute), AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t,
		[]OpenAIUserResidentSlot{slot, otherSlot}, []Account{account, otherAccount}, nil, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "full-provisional-slot"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, account.ID, selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, slot.ID, repo.binding.ResidentSlotID)
}

func TestOpenAIGatewayService_AllResidentCandidatesUnavailableReturnsDomainError(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	drainingSlot := OpenAIUserResidentSlot{
		ID: 85, UserID: 42, ScopeKey: scopeKey, AccountID: 36285, Generation: 1,
		Status: OpenAIUserResidentSlotStatusDraining, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	svc, _, ctx := newMultiSlotAffinitySchedulerTestService(t, []OpenAIUserResidentSlot{drainingSlot}, nil, nil, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "no-reusable-resident"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.ErrorIs(t, err, ErrOpenAIUserAffinityNoCandidateSlot)
	require.True(t, found)
	require.Nil(t, selection)
}

func TestOpenAIGatewayService_ConversationBindingPrecedesResidentPlacement(t *testing.T) {
	residentAccountID := int64(36201)
	boundAccountID := int64(36202)
	now := time.Now().UTC()
	baseRepo := &schedulerAffinityAccountRepo{
		schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{
			{ID: residentAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
			{ID: boundAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		}},
		placement: &OpenAIUserPlacement{
			UserID: 42, ScopeKey: openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportAny),
			AccountID: &residentAccountID, Generation: 4, Status: "active", AssignedAt: now, ExpiresAt: now.Add(time.Hour),
		},
	}
	repo := &schedulerConversationAffinityRepo{
		schedulerAffinityAccountRepo: baseRepo,
		binding: &OpenAIUserConversationBinding{
			ID: 8, UserID: 42, APIKeyID: 77,
			ScopeKey:         openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportAny),
			ConversationHash: strings.Repeat("a", 64), ResidentSlotID: 3,
			AccountID: boundAccountID, SlotGeneration: 2, Status: "active",
			FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
		},
	}
	configValue := DefaultOpenAIUserAffinityConfig()
	configValue.Enabled = true
	raw, err := json.Marshal(configValue)
	require.NoError(t, err)
	svc := &OpenAIGatewayService{
		accountRepo: repo, cfg: &config.Config{RunMode: config.RunModeSimple},
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		settingService:     NewSettingService(&openAIUserAffinitySuccessSettingRepo{value: string(raw)}, nil),
	}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	ctx = context.WithValue(ctx, ctxkey.APIKeyID, int64(77))
	ctx = context.WithValue(ctx, ctxkey.RequestID, "conversation-binding-priority")

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, nil, "", "session-1", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, boundAccountID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerConversationBinding, decision.Layer)
}

func TestOpenAIGatewayService_CodexDerivedThreadLocksParentAndReservesOwnBinding(t *testing.T) {
	now := time.Now().UTC()
	accountID := int64(36231)
	accounts := []Account{{
		ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
	}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, accounts, nil, 2)
	parentHash := strings.Repeat("a", 64)
	childHash := strings.Repeat("b", 64)
	parent := &OpenAIUserConversationBinding{
		ID: 2301, UserID: 42, APIKeyID: 77,
		ScopeKey:         openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
		ConversationHash: strings.Repeat("c", 64), ResidentSlotID: 31,
		AccountID: accountID, SlotGeneration: 3, Status: "active",
		ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
	}
	bindOpenAIUserAffinityTestExecutionTarget(svc, parent)
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		openAICodexThreadAliasTestKey(nil, parentHash): parent,
	}
	ctx, topology := withOpenAICodexThreadAffinityTestState(ctx, childHash, parentHash)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "shared-session"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, accountID, selection.Account.ID)
	require.True(t, topology.allows(accountID))
	require.Len(t, repo.reservations, 1)
	require.Equal(t, childHash, repo.reservations[0].ConversationHash)
	require.Equal(t, parent.ScopeKey, repo.reservations[0].ScopeKey)
	require.Equal(t, parent.ResidentSlotID, repo.reservations[0].PreferredResidentSlotID)
	require.Equal(t, parent.SlotGeneration, repo.reservations[0].PreferredSlotGeneration)
	require.Equal(t, []OpenAIUserConversationAlias{
		openAICodexThreadReservationAlias(nil, childHash),
	}, repo.reservations[0].Aliases)
	attempt, ok := svc.openAIUserAffinityAttempt(ctx, accountID)
	require.True(t, ok)
	require.NotNil(t, attempt.conversation)
	require.NotEqual(t, parent.ID, attempt.conversation.BindingID, "子线程必须提交自己的 provisional binding")
	require.NotNil(t, selection.ExecutionTarget)
	require.True(t, selection.ExecutionTarget.Valid())

	reservationRepo := &openAIBoundPersonaReservationRepo{personaLeaseID: 3301}
	reservationState := &openAIPersonaUserReservationState{
		repo: reservationRepo, token: "child-client-reservation",
	}
	clientHash := strings.Repeat("d", 64)
	require.NoError(t, svc.attachBoundOpenAIPersonaReservation(ctx, selection, reservationState, clientHash))
	require.Equal(t, parent.AccountPersonaID, repo.binding.AccountPersonaID)
	require.Equal(t, parent.PersonaSessionEpoch, repo.binding.PersonaSessionEpoch)
	require.Equal(t, parent.CredentialChainID, repo.binding.CredentialChainID)
	require.Equal(t, clientHash, repo.binding.RootClientSessionHash)
	require.Zero(t, repo.binding.UserGroupLeaseID)
	require.Equal(t, parent.ProfileID, repo.binding.ProfileID)
	require.Equal(t, parent.ProfileVersion, repo.binding.ProfileVersion)

	const childResponseID = "resp_child_persona_target"
	svc.stageOpenAIUserAffinityResponseAlias(ctx, accountID, childResponseID)
	require.True(t, svc.commitOpenAIUserAffinityConversation(ctx, accountID))

	continuationCtx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	continuationCtx = context.WithValue(continuationCtx, ctxkey.APIKeyID, int64(77))
	continuationCtx = context.WithValue(continuationCtx, ctxkey.RequestID, "child-persona-continuation")
	continuationReq := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, childResponseID, "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	continued, continuedFound, continueErr := svc.selectOpenAIUserAffinityConversation(continuationCtx, continuationReq)
	require.NoError(t, continueErr)
	require.True(t, continuedFound)
	require.NotNil(t, continued)
	require.NotNil(t, continued.ExecutionTarget)
	require.Equal(t, selection.ExecutionTarget.AccountPersonaID, continued.ExecutionTarget.AccountPersonaID)
	require.Equal(t, selection.ExecutionTarget.SessionEpoch, continued.ExecutionTarget.SessionEpoch)
	require.Equal(t, selection.ExecutionTarget.CredentialChainID, continued.ExecutionTarget.CredentialChainID)
}

func TestOpenAIGatewayService_ContinuationRestoresBoundPersonaBeforeNewRootCapacityFilter(t *testing.T) {
	now := time.Now().UTC()
	groupID := int64(9)
	accountID := int64(36239)
	account := Account{
		ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, []Account{account}, nil, 2)
	responseID := "resp_bound_persona_capacity"
	scopeKey := openAIUserAffinityScopeKey(&groupID, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	binding := &OpenAIUserConversationBinding{
		ID: 2391, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("2", 64), ResidentSlotID: 39,
		AccountID: accountID, SlotGeneration: 1, Status: "active",
		ContextRebuildable: false, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
	}
	bindOpenAIUserAffinityTestExecutionTarget(svc, binding)
	binding.RootClientSessionHash = strings.Repeat("3", 64)
	binding.UserGroupLeaseID = 3901
	responseHash := openAIUserAffinityScopedStateHash(42, 77, scopeKey, "response_id", responseID)
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		scopeKey + "\x00response_id\x00" + responseHash: binding,
	}
	reservationRepo := &openAIBoundPersonaReservationRepo{personaLeaseID: 3902}
	// 空候选模拟该账号的 Persona 已被其他用户占满。旧实现会在恢复 binding
	// 之前执行账号级新根预筛选，并错误返回 account unavailable。
	svc.personaUserReservationRepo = reservationRepo

	selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
		ctx, &groupID, responseID, "bound-client-session", "", nil,
		OpenAIUpstreamTransportHTTPSSE, "", false, false, true,
	)

	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerConversationBinding, decision.Layer)
	require.Equal(t, accountID, selection.Account.ID)
	require.NotNil(t, selection.ExecutionTarget)
	require.Equal(t, binding.AccountPersonaID, selection.ExecutionTarget.AccountPersonaID)
	require.Equal(t, binding.PersonaSessionEpoch, selection.ExecutionTarget.SessionEpoch)
	require.Zero(t, reservationRepo.listCalls, "continuation 不应经过新根账号级 Persona 容量预筛选")
}

func TestOpenAIGatewayService_BoundPersonaCapacityReturnsPersonaBusyWithoutMigration(t *testing.T) {
	now := time.Now().UTC()
	groupID := int64(10)
	accountID := int64(36240)
	account := Account{
		ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, []Account{account}, nil, 2)
	responseID := "resp_bound_persona_full"
	scopeKey := openAIUserAffinityScopeKey(&groupID, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	binding := &OpenAIUserConversationBinding{
		ID: 2401, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("4", 64), ResidentSlotID: 40,
		AccountID: accountID, SlotGeneration: 1, Status: "active",
		ContextRebuildable: false, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
	}
	bindOpenAIUserAffinityTestExecutionTarget(svc, binding)
	binding.RootClientSessionHash = strings.Repeat("5", 64)
	binding.UserGroupLeaseID = 4001
	responseHash := openAIUserAffinityScopedStateHash(42, 77, scopeKey, "response_id", responseID)
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		scopeKey + "\x00response_id\x00" + responseHash: binding,
	}
	reservationRepo := &openAIBoundPersonaReservationRepo{
		reserveErr: ErrOpenAIPersonaUserCapacity,
	}
	svc.personaUserReservationRepo = reservationRepo

	selection, _, err := svc.SelectAccountWithSchedulerForCapability(
		ctx, &groupID, responseID, "expired-bound-client-session", "", nil,
		OpenAIUpstreamTransportHTTPSSE, "", false, false, true,
	)

	require.ErrorIs(t, err, ErrOpenAIPersonaUserCapacity)
	require.NotErrorIs(t, err, ErrOpenAIPreviousResponseAccountUnavailable)
	require.Nil(t, selection)
	require.Zero(t, reservationRepo.listCalls)
}

func TestOpenAIGatewayService_CodexParentProvisionalFailsRetryably(t *testing.T) {
	now := time.Now().UTC()
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, nil, nil, 2)
	parentHash := strings.Repeat("d", 64)
	childHash := strings.Repeat("e", 64)
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		openAICodexThreadAliasTestKey(nil, parentHash): {
			ID: 2302, UserID: 42, APIKeyID: 77, ScopeKey: openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
			ConversationHash: strings.Repeat("f", 64), ResidentSlotID: 32, AccountID: 36232,
			SlotGeneration: 1, Status: "provisional", ExpiresAt: now.Add(time.Minute),
		},
	}
	ctx, topology := withOpenAICodexThreadAffinityTestState(ctx, childHash, parentHash)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "shared-session"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrOpenAICodexParentThreadPending)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, found)
	require.Nil(t, selection)
	require.False(t, topology.allows(36232))
	require.Empty(t, repo.reservations)
}

func TestOpenAIGatewayService_CodexSelfBindingWinsAndDetachesMigratedLineage(t *testing.T) {
	now := time.Now().UTC()
	parentAccountID := int64(36233)
	selfAccountID := int64(36234)
	accounts := []Account{
		{ID: parentAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: selfAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, accounts, nil, 2)
	parentHash := strings.Repeat("1", 64)
	selfHash := strings.Repeat("2", 64)
	newBinding := func(id, accountID int64, hash string) *OpenAIUserConversationBinding {
		return &OpenAIUserConversationBinding{
			ID: id, UserID: 42, APIKeyID: 77,
			ScopeKey:         openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
			ConversationHash: hash, ResidentSlotID: id + 100, AccountID: accountID,
			SlotGeneration: 1, Status: "active", ContextRebuildable: true,
			FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
		}
	}
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		openAICodexThreadAliasTestKey(nil, parentHash): newBinding(2303, parentAccountID, strings.Repeat("3", 64)),
		openAICodexThreadAliasTestKey(nil, selfHash):   newBinding(2304, selfAccountID, selfHash),
	}
	ctx, topology := withOpenAICodexThreadAffinityTestState(ctx, selfHash, parentHash)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "shared-session"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, selfAccountID, selection.Account.ID)
	require.False(t, topology.allows(selfAccountID), "迁移后的自身绑定不得重新继承旧父系")
	require.Empty(t, repo.reservations)
}

func TestOpenAIGatewayService_CodexParentUnavailableDoesNotFallBack(t *testing.T) {
	now := time.Now().UTC()
	parentAccountID := int64(36235)
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, []Account{{
		ID: parentAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusDisabled, Schedulable: false, Concurrency: 1,
	}}, nil, 2)
	parentHash := strings.Repeat("4", 64)
	childHash := strings.Repeat("5", 64)
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		openAICodexThreadAliasTestKey(nil, parentHash): {
			ID: 2305, UserID: 42, APIKeyID: 77,
			ScopeKey:         openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
			ConversationHash: strings.Repeat("6", 64), ResidentSlotID: 35,
			AccountID: parentAccountID, SlotGeneration: 1, Status: "active",
			ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
		},
	}
	ctx, _ = withOpenAICodexThreadAffinityTestState(ctx, childHash, parentHash)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "shared-session"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrOpenAICodexParentThreadUnavailable)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, found)
	require.Nil(t, selection)
	require.Empty(t, repo.failovers)
	require.Empty(t, repo.replacements)
}

func TestOpenAIGatewayService_CodexThreadBackfillsLegacySessionBinding(t *testing.T) {
	now := time.Now().UTC()
	accountID := int64(36237)
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, []Account{{
		ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
	}}, nil, 2)
	selfHash := strings.Repeat("8", 64)
	sessionHash := "legacy-session"
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	legacyHash := openAIUserAffinityScopedStateHash(42, 77, scopeKey, "session_hash", sessionHash)
	legacyBinding := &OpenAIUserConversationBinding{
		ID: 2307, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey,
		ConversationHash: legacyHash, ResidentSlotID: 37, AccountID: accountID,
		SlotGeneration: 2, Status: "active", ContextRebuildable: true,
		FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
	}
	repo.bindingsByHash = map[string]*OpenAIUserConversationBinding{legacyHash: legacyBinding}
	ctx, topology := withOpenAICodexThreadAffinityTestState(ctx, selfHash)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = sessionHash

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, accountID, selection.Account.ID)
	require.False(t, topology.allows(accountID), "根线程补齐别名不得自我授权父系")
	require.Equal(t, []string{selfHash, legacyHash}, repo.bindingLookups)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, legacyHash, repo.reservations[0].ConversationHash)
	require.Equal(t, []OpenAIUserConversationAlias{openAICodexThreadReservationAlias(nil, selfHash)}, repo.reservations[0].Aliases)
	require.Equal(t, legacyBinding.ID, repo.binding.ID)
}

func TestOpenAIGatewayService_ConversationFailoverRequiresThresholdAndMovesOnlyBinding(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	sourceExtra := openAIUserAffinityTestQuotaExtra(now, 20)
	sourceExtra["codex_5h_window_minutes"] = 300
	sourceExtra["codex_5h_used_percent"] = 100.0
	sourceExtra["codex_5h_reset_at"] = now.Add(time.Hour).Format(time.RFC3339Nano)
	accounts := []Account{
		{ID: 36241, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: sourceExtra},
		{ID: 36242, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	slots := []OpenAIUserResidentSlot{
		{ID: 41, UserID: 42, ScopeKey: scopeKey, AccountID: 36241, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 4, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{ID: 42, UserID: 42, ScopeKey: scopeKey, AccountID: 36242, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 2, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	repo.binding = &OpenAIUserConversationBinding{
		ID: 241, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("8", 64), ResidentSlotID: 41,
		AccountID: 36241, SlotGeneration: 1, Status: "active", ContextRebuildable: true,
		FirstOutputCommitted: true, ExpiresAt: now.Add(24 * time.Hour),
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "conversation-threshold"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, found)
	require.Nil(t, selection)
	require.Empty(t, repo.failovers, "低于客户端重试阈值不得切号")
	require.Len(t, repo.recordedIncidents, 1)
	require.Equal(t, strings.Repeat("8", 64), repo.recordedIncidents[0].ConversationHash)
	require.Equal(t, int64(41), repo.recordedIncidents[0].ResidentSlotID)

	authorizedAt := now.Add(-time.Hour)
	repo.authorizedByAccount = map[int64]*time.Time{36241: &authorizedAt}
	secondCtx := context.WithValue(ctx, ctxkey.RequestID, "multi-slot-failover-second-retry")
	selection, found, err = svc.selectOpenAIUserAffinityConversation(secondCtx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, int64(36242), selection.Account.ID)
	require.Len(t, repo.failovers, 1)
	require.Equal(t, int64(36241), repo.binding.AccountID, "首输出前测试仓储中的原绑定不得被覆盖")
	attempt, ok := svc.openAIUserAffinityAttempt(secondCtx, 36242)
	require.True(t, ok)
	require.NotNil(t, attempt.conversation)
	require.True(t, attempt.conversation.Failover)
}

func TestOpenAIGatewayService_ProvisionalHardUnavailableReplaysOnReplacementAccount(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accounts := []Account{
		{ID: 36248, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusError, Schedulable: false, Concurrency: 1},
		{ID: 36249, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	slots := []OpenAIUserResidentSlot{
		{ID: 48, UserID: 42, ScopeKey: scopeKey, AccountID: 36248, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour)},
		{ID: 49, UserID: 42, ScopeKey: scopeKey, AccountID: 36249, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	repo.binding = &OpenAIUserConversationBinding{
		ID: 248, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("b", 64), ResidentSlotID: 48, AccountID: 36248, SlotGeneration: 1,
		Status: "provisional", ContextRebuildable: true, FirstOutputCommitted: false,
		ProvisionalToken: "provisional-hard-error", ExpiresAt: now.Add(time.Hour),
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "provisional-hard-error"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, int64(36249), selection.Account.ID)
	require.Len(t, repo.evictedSlots, 1)
	require.Equal(t, int64(48), repo.evictedSlots[0].ID)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, int64(36249), repo.reservations[0].AccountID)
}

func TestOpenAIGatewayService_UnderfilledHardUnavailableBindingStartsReplacement(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accounts := []Account{
		{ID: 36253, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusDisabled, Schedulable: false, Concurrency: 1},
		{ID: 36254, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
	}
	slots := []OpenAIUserResidentSlot{{
		ID: 53, UserID: 42, ScopeKey: scopeKey, AccountID: 36253, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour),
	}}
	stats := map[int64]OpenAIUserAffinityCandidate{36254: {AccountID: 36254}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, stats, 2)
	repo.binding = &OpenAIUserConversationBinding{
		ID: 253, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey, ConversationHash: strings.Repeat("c", 64),
		ResidentSlotID: 53, AccountID: 36253, SlotGeneration: 1, Status: "active",
		ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
	}
	bindOpenAIUserAffinityTestExecutionTarget(svc, repo.binding)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "underfilled-hard-unavailable"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrOpenAIPreviousResponseAccountUnavailable)
	require.True(t, found)
	require.Nil(t, selection)
	require.Empty(t, repo.replacements)
	require.Equal(t, uint64(1), svc.SnapshotOpenAIUserAffinityMetrics().StaleBindingAccountUnavailable)
}

func TestOpenAIGatewayService_StaleBindingSlotGenerationRebuildsProvisionally(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, OpenAIEndpointCapabilityResponses, "", OpenAIUpstreamTransportHTTPSSE)
	accountID := int64(36259)
	account := Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)}
	slot := OpenAIUserResidentSlot{
		ID: 59, UserID: 42, ScopeKey: scopeKey, AccountID: accountID, Generation: 2,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, []OpenAIUserResidentSlot{slot}, []Account{account},
		map[int64]OpenAIUserAffinityCandidate{accountID: {AccountID: accountID}}, 2)
	invalid := false
	repo.bindingValid = &invalid
	repo.binding = &OpenAIUserConversationBinding{
		ID: 259, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey, ConversationHash: strings.Repeat("9", 64),
		ResidentSlotID: slot.ID, AccountID: accountID, SlotGeneration: 1, Status: "active",
		ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses, "", false, nil)
	req.SessionHash = "stale-slot-generation"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, selection)

	selection, found, err = svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, accountID, selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.NotNil(t, repo.binding)
	require.Equal(t, "provisional", repo.binding.Status)
	require.Equal(t, slot.Generation, repo.binding.SlotGeneration)
	require.False(t, repo.binding.FirstOutputCommitted)
}

func TestOpenAIGatewayService_ConcurrentRebuildFollowerRecordsConflict(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, OpenAIEndpointCapabilityResponses, "", OpenAIUpstreamTransportHTTPSSE)
	accountID := int64(36260)
	account := Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)}
	slot := OpenAIUserResidentSlot{
		ID: 60, UserID: 42, ScopeKey: scopeKey, AccountID: accountID, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, []OpenAIUserResidentSlot{slot}, []Account{account}, nil, 2)
	conversationHash := openAIUserAffinityScopedStateHash(42, 77, scopeKey, "session_hash", "concurrent-rebuild")
	repo.binding = &OpenAIUserConversationBinding{
		ID: 260, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey, ConversationHash: conversationHash,
		ResidentSlotID: slot.ID, AccountID: accountID, SlotGeneration: slot.Generation, Status: "provisional",
		ContextRebuildable: true, ExpiresAt: now.Add(time.Minute), ProvisionalToken: "leader",
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses, "", false, nil)
	req.SessionHash = "concurrent-rebuild"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, found)
	require.Nil(t, selection)
	require.Equal(t, uint64(1), svc.SnapshotOpenAIUserAffinityMetrics().ConcurrentRebuildConflict)
}

func TestOpenAIGatewayService_CrossScopeStaleLineageRebuildsResponsesBinding(t *testing.T) {
	now := time.Now().UTC()
	responsesScope := openAIUserAffinityScopeKey(nil, false, OpenAIEndpointCapabilityResponses, "", OpenAIUpstreamTransportHTTPSSE)
	chatScope := openAIUserAffinityScopeKey(nil, false, OpenAIEndpointCapabilityChatCompletions, "", OpenAIUpstreamTransportHTTPSSE)
	staleAccountID, healthyAccountID := int64(36255), int64(36256)
	accounts := []Account{
		{ID: staleAccountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusError, Schedulable: false, Concurrency: 1},
		{ID: healthyAccountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
	}
	slots := []OpenAIUserResidentSlot{{
		ID: 55, UserID: 42, ScopeKey: responsesScope, AccountID: staleAccountID, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour),
	}}
	stats := map[int64]OpenAIUserAffinityCandidate{healthyAccountID: {AccountID: healthyAccountID}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, stats, 2)
	selfHash := strings.Repeat("d", 64)
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		openAICodexThreadAliasTestKey(nil, selfHash): {
			ID: 255, UserID: 42, APIKeyID: 77, ScopeKey: chatScope, ConversationHash: strings.Repeat("e", 64),
			ResidentSlotID: 550, AccountID: staleAccountID, SlotGeneration: 7, Status: "active",
			ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
		},
	}
	ctx, _ = withOpenAICodexThreadAffinityTestState(ctx, selfHash)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses, "", false, nil)
	req.SessionHash = "cross-scope-stale-lineage"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, selection)

	selection, found, err = svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, healthyAccountID, selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, responsesScope, repo.reservations[0].ScopeKey)
	require.Equal(t, []OpenAIUserConversationAlias{openAICodexThreadReservationAlias(nil, selfHash)}, repo.reservations[0].Aliases)
	require.True(t, svc.commitOpenAIUserAffinityConversation(ctx, healthyAccountID))
	lineageBinding := repo.aliasBindings[openAICodexThreadAliasTestKey(nil, selfHash)]
	require.NotNil(t, lineageBinding)
	require.Equal(t, responsesScope, lineageBinding.ScopeKey)
	require.Equal(t, healthyAccountID, lineageBinding.AccountID)
	metrics := svc.SnapshotOpenAIUserAffinityMetrics()
	require.Equal(t, uint64(1), metrics.StaleLineageScopeMismatch)
	require.Equal(t, uint64(1), metrics.StaleLineageAccountUnavailable)
	require.Equal(t, uint64(1), metrics.ProvisionalCommitSuccess)
}

func TestOpenAIGatewayService_CrossScopeLineageRebuildRollsBackBeforeFirstOutput(t *testing.T) {
	now := time.Now().UTC()
	responsesScope := openAIUserAffinityScopeKey(nil, false, OpenAIEndpointCapabilityResponses, "", OpenAIUpstreamTransportHTTPSSE)
	accountID := int64(36258)
	account := Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)}
	slot := OpenAIUserResidentSlot{ID: 58, UserID: 42, ScopeKey: responsesScope, AccountID: accountID, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour)}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, []OpenAIUserResidentSlot{slot}, []Account{account},
		map[int64]OpenAIUserAffinityCandidate{accountID: {AccountID: accountID}}, 2)
	selfHash := strings.Repeat("1", 64)
	repo.aliasBindings = map[string]*OpenAIUserConversationBinding{
		openAICodexThreadAliasTestKey(nil, selfHash): {
			ID: 258, UserID: 42, APIKeyID: 77,
			ScopeKey:         openAIUserAffinityScopeKey(nil, false, OpenAIEndpointCapabilityChatCompletions, "", OpenAIUpstreamTransportHTTPSSE),
			ConversationHash: strings.Repeat("2", 64), ResidentSlotID: 580, AccountID: accountID,
			SlotGeneration: 3, Status: "active", ContextRebuildable: true,
			FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
		},
	}
	ctx, _ = withOpenAICodexThreadAffinityTestState(ctx, selfHash)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses, "", false, nil)
	req.SessionHash = "cross-scope-rollback"
	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, selection)

	selection, found, err = svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, accountID, selection.Account.ID)
	attempt, ok := svc.openAIUserAffinityAttempt(ctx, accountID)
	require.True(t, ok)
	require.NotNil(t, attempt.conversation)
	require.Equal(t, responsesScope, attempt.conversation.ScopeKey)

	svc.rollbackOpenAIUserAffinityConversation(ctx, attempt)
	require.Nil(t, repo.binding)
	require.Nil(t, repo.aliasBindings[openAICodexThreadAliasTestKey(nil, selfHash)])
	require.Len(t, repo.rollbackTransitions, 1)
	require.Equal(t, uint64(1), svc.SnapshotOpenAIUserAffinityMetrics().ProvisionalRollback)
}

func TestOpenAIGatewayService_HardUnavailableBindingWithoutCandidateDoesNotPolluteState(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	account := Account{ID: 36257, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusDisabled, Schedulable: false, Concurrency: 1}
	slot := OpenAIUserResidentSlot{ID: 57, UserID: 42, ScopeKey: scopeKey, AccountID: account.ID, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour)}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, []OpenAIUserResidentSlot{slot}, []Account{account}, nil, 2)
	repo.binding = &OpenAIUserConversationBinding{
		ID: 257, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey, ConversationHash: strings.Repeat("f", 64),
		ResidentSlotID: slot.ID, AccountID: account.ID, SlotGeneration: slot.Generation, Status: "active",
		ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
	}
	bindOpenAIUserAffinityTestExecutionTarget(svc, repo.binding)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "no-healthy-candidate"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrOpenAIPreviousResponseAccountUnavailable)
	require.True(t, found)
	require.Nil(t, selection)
	require.Empty(t, repo.replacements)
	require.Empty(t, repo.failovers)
	require.Empty(t, repo.reservations)
	require.Equal(t, account.ID, repo.binding.AccountID)
	require.Equal(t, "active", repo.binding.Status)
}

func TestOpenAIGatewayService_MissingBoundAccountFailsClosedWithoutPanic(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accountID := int64(36258)
	slot := OpenAIUserResidentSlot{ID: 58, UserID: 42, ScopeKey: scopeKey, AccountID: accountID, Generation: 1,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(time.Hour)}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, []OpenAIUserResidentSlot{slot}, nil, nil, 2)
	svc.accountRepo = &missingAccountConversationAffinityRepo{schedulerConversationAffinityRepo: repo}
	repo.binding = &OpenAIUserConversationBinding{
		ID: 258, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey, ConversationHash: strings.Repeat("0", 64),
		ResidentSlotID: slot.ID, AccountID: accountID, SlotGeneration: slot.Generation, Status: "active",
		ContextRebuildable: true, FirstOutputCommitted: true, ExpiresAt: now.Add(time.Hour),
	}
	bindOpenAIUserAffinityTestExecutionTarget(svc, repo.binding)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "missing-bound-account"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrOpenAIPreviousResponseAccountUnavailable)
	require.True(t, found)
	require.Nil(t, selection)
	require.Empty(t, repo.replacements)
	require.Empty(t, repo.failovers)
	require.Empty(t, repo.reservations)
}

func TestOpenAIGatewayService_NonRebuildableConversationFailsClosed(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accounts := []Account{
		{ID: 36251, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusDisabled, Schedulable: false, Concurrency: 1},
		{ID: 36252, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	slots := []OpenAIUserResidentSlot{
		{ID: 51, UserID: 42, ScopeKey: scopeKey, AccountID: 36251, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{ID: 52, UserID: 42, ScopeKey: scopeKey, AccountID: 36252, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	repo.binding = &OpenAIUserConversationBinding{
		ID: 251, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("9", 64), ResidentSlotID: 51,
		AccountID: 36251, SlotGeneration: 1, Status: "active", ContextRebuildable: false,
		FirstOutputCommitted: true, ExpiresAt: now.Add(24 * time.Hour),
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "resp_strict", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "strict-conversation"
	req.PreviousResponseCanMove = false

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrOpenAIPreviousResponseAccountUnavailable)
	require.True(t, found)
	require.Nil(t, selection)
	require.Empty(t, repo.failovers)
}

func TestOpenAIGatewayService_UnknownStrictResponseAliasFailsClosed(t *testing.T) {
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, nil, nil, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "resp_unknown_owner", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.PreviousResponseCanMove = false

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.ErrorIs(t, err, ErrOpenAIConversationResetRequired)
	require.True(t, found)
	require.Nil(t, selection)
	require.Equal(t, "response_id", repo.aliasType)
	require.Equal(t, openAIUserAffinityScopedStateHash(42, 77,
		openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
		"response_id", "resp_unknown_owner"), repo.aliasHash)
}

func TestDefaultOpenAIAccountScheduler_DoesNotUseGroupResponseCacheWhenAffinityIsEnabled(t *testing.T) {
	svc, _, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, nil, nil, 2)
	svc.openaiWSStateStore = NewOpenAIWSStateStore(nil)
	require.NoError(t, svc.openaiWSStateStore.BindResponseAccount(ctx, 0, "resp_cross_user", 999, time.Hour))
	scheduler := newDefaultOpenAIAccountScheduler(svc, nil)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "resp_cross_user", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.PreviousResponseCanMove = false

	selection, decision, err := scheduler.Select(ctx, req)
	require.ErrorIs(t, err, ErrOpenAIConversationResetRequired)
	require.Nil(t, selection)
	require.False(t, decision.StickyPreviousHit)
}

func TestOpenAIGatewayService_ReplacesUnavailableSourceSlotAfterAllSlotsUnavailable(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	accounts := []Account{
		{ID: 36261, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusDisabled, Schedulable: false, Concurrency: 1},
		{ID: 36262, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusDisabled, Schedulable: false, Concurrency: 1},
		{ID: 36263, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
	}
	slots := []OpenAIUserResidentSlot{
		{ID: 61, UserID: 42, ScopeKey: scopeKey, AccountID: 36261, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 5, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
		{ID: 62, UserID: 42, ScopeKey: scopeKey, AccountID: 36262, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 1, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	}
	stats := map[int64]OpenAIUserAffinityCandidate{36263: {AccountID: 36263}}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, stats, 2)
	repo.binding = &OpenAIUserConversationBinding{
		ID: 261, UserID: 42, APIKeyID: 77, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("a", 64), ResidentSlotID: 61,
		AccountID: 36261, SlotGeneration: 1, Status: "active", ContextRebuildable: true,
		FirstOutputCommitted: true, ExpiresAt: now.Add(24 * time.Hour),
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "replace-coldest"

	selection, found, err := svc.selectOpenAIUserAffinityConversation(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, int64(36263), selection.Account.ID)
	require.Len(t, repo.replacements, 1)
	require.Equal(t, int64(61), repo.replacements[0].VictimSlotID)
	require.Len(t, repo.replacements[0].CheckedSlots, 2)
	attempt, ok := svc.openAIUserAffinityAttempt(ctx, 36263)
	require.True(t, ok)
	require.True(t, attempt.conversation.Replacement)
	require.Equal(t, uint64(1), svc.SnapshotOpenAIUserAffinityMetrics().ResidentSlotFullReplace)
}

func TestOpenAIGatewayService_ResetScopeExcludesAllOldSlotsAndSkipsAffinityPriority(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36271, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
		{ID: 36272, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 55)},
	}
	stats := map[int64]OpenAIUserAffinityCandidate{
		36271: {AccountID: 36271},
		36272: {AccountID: 36272, UserAlreadyResident: true},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, accounts, stats, 2)
	repo.resetExclusions = []int64{36269, 36270}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "manual-reset-new-conversation"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, int64(36271), selection.Account.ID, "重置后应直接按 BestFit，而不是优先跨 Scope 已居住账号")
	require.Len(t, repo.reservations, 1)
}

func TestOpenAIGatewayService_UserAffinityDoesNotDependOnAdvancedScheduler(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(11001)
	scopeKey := openAIUserAffinityScopeKey(&groupID, false, "", "", OpenAIUpstreamTransportAny)
	accountID := int64(36102)
	now := time.Now().UTC()
	repo := &schedulerAffinityAccountRepo{
		schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{
			{ID: 36101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0},
			{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10},
		}},
		placement: &OpenAIUserPlacement{UserID: 42, ScopeKey: scopeKey, AccountID: &accountID, Generation: 3, Status: "active", AssignedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	affinityConfig := DefaultOpenAIUserAffinityConfig()
	affinityConfig.Enabled = true
	affinityConfig.Mode = OpenAIUserAffinityModeEnforce
	raw, err := json.Marshal(affinityConfig)
	require.NoError(t, err)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cache := &schedulerAffinityCache{schedulerTestGatewayCache: &schedulerTestGatewayCache{}}
	svc := &OpenAIGatewayService{
		accountRepo: repo, cache: cache, cfg: cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		settingService:     NewSettingService(&openAIUserAffinitySuccessSettingRepo{value: string(raw)}, cfg),
	}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	ctx = context.WithValue(ctx, ctxkey.RequestID, "affinity-legacy-scheduler")

	selection, decision, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, accountID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerUserAffinity, decision.Layer)
	require.False(t, svc.isOpenAIAdvancedSchedulerEnabled(ctx))
}

func TestOpenAIGatewayService_StalePlacementWithoutLiveSlotFallsBackToNewResident(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	residentAccountID := int64(36281)
	targetAccountID := int64(36282)
	accounts := []Account{
		{ID: targetAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
			Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
	}
	stats := map[int64]OpenAIUserAffinityCandidate{
		targetAccountID: {AccountID: targetAccountID, Available5HRatio: 0.9, Available7DRatio: 0.9, Quota5HKnown: true, Quota7DKnown: true},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, accounts, stats, 2)
	repo.placement = &OpenAIUserPlacement{
		UserID: 42, ScopeKey: scopeKey, AccountID: &residentAccountID, Generation: 4,
		Status: "active", AssignedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "gpt-5.1",
		OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "stale-placement-session"

	decision := OpenAIAccountScheduleDecision{}
	selection, found, err := svc.selectLegacyOpenAIUserAffinityPreflight(ctx, req, &decision)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, targetAccountID, selection.Account.ID)
	require.Equal(t, openAIAccountScheduleLayerUserAffinity, decision.Layer)
	require.Len(t, repo.reservations, 1)
}

func TestOpenAIGatewayService_SinglePlacementHardUnavailableReassigns(t *testing.T) {
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	residentAccountID := int64(36285)
	targetAccountID := int64(36286)
	accounts := []Account{
		{ID: residentAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusError, Schedulable: false, Concurrency: 1},
		{ID: targetAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1,
			Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
	}
	slots := []OpenAIUserResidentSlot{{
		ID: 85, UserID: 42, ScopeKey: scopeKey, AccountID: residentAccountID, Generation: 5,
		Status: OpenAIUserResidentSlotStatusActive, ScoreUpdatedAt: now, AdmittedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}}
	stats := map[int64]OpenAIUserAffinityCandidate{
		targetAccountID: {AccountID: targetAccountID, Available5HRatio: 0.9, Available7DRatio: 0.9, Quota5HKnown: true, Quota7DKnown: true},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, stats, 1)
	repo.placement = &OpenAIUserPlacement{
		UserID: 42, ScopeKey: scopeKey, AccountID: &residentAccountID, Generation: 5,
		Status: "active", AssignedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "gpt-5.1",
		OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "single-placement-hard-error"

	selection, found, err := svc.selectOpenAIUserAffinityPlacementForRequest(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, targetAccountID, selection.Account.ID)
	require.Len(t, repo.evictedSlots, 1)
	require.Equal(t, int64(85), repo.evictedSlots[0].ID)
	require.NotNil(t, repo.placement)
	require.Equal(t, targetAccountID, *repo.placement.AccountID)
}

func TestOpenAIGatewayService_UserAffinityShadowRunsWithLegacyScheduler(t *testing.T) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	defer resetOpenAIAdvancedSchedulerSettingCacheForTest()

	groupID := int64(11002)
	scopeKey := openAIUserAffinityScopeKey(&groupID, false, "", "", OpenAIUpstreamTransportAny)
	residentAccountID := int64(36112)
	now := time.Now().UTC()
	repo := &schedulerAffinityAccountRepo{
		schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{
			{ID: 36111, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 0},
			{ID: residentAccountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10},
		}},
		placement: &OpenAIUserPlacement{UserID: 42, ScopeKey: scopeKey, AccountID: &residentAccountID, Generation: 3, Status: "active", AssignedAt: now, ExpiresAt: now.Add(time.Hour)},
	}
	affinityConfig := DefaultOpenAIUserAffinityConfig()
	affinityConfig.Enabled = true
	affinityConfig.Mode = OpenAIUserAffinityModeShadow
	raw, err := json.Marshal(affinityConfig)
	require.NoError(t, err)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := &OpenAIGatewayService{
		accountRepo: repo, cache: &schedulerAffinityCache{schedulerTestGatewayCache: &schedulerTestGatewayCache{}}, cfg: cfg,
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		settingService:     NewSettingService(&openAIUserAffinitySuccessSettingRepo{value: string(raw)}, cfg),
	}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	ctx = context.WithValue(ctx, ctxkey.RequestID, "affinity-shadow-legacy-scheduler")

	selection, _, err := svc.SelectAccountWithScheduler(ctx, &groupID, "", "", "", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, uint64(1), svc.SnapshotOpenAIUserAffinityMetrics().ShadowEvaluations)
	require.False(t, svc.isOpenAIAdvancedSchedulerEnabled(ctx))
}

func TestOpenAIGatewayService_UserAffinityEnforceRejectsMissingIdentity(t *testing.T) {
	affinityConfig := DefaultOpenAIUserAffinityConfig()
	affinityConfig.Enabled = true
	affinityConfig.Mode = OpenAIUserAffinityModeEnforce
	raw, err := json.Marshal(affinityConfig)
	require.NoError(t, err)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{}, cfg: cfg,
		settingService: NewSettingService(&openAIUserAffinitySuccessSettingRepo{value: string(raw)}, cfg),
	}

	selection, found, err := svc.selectOpenAIUserAffinityPlacement(
		context.Background(), nil, "", nil, false, "", "", OpenAIUpstreamTransportAny,
	)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, found)
	require.Nil(t, selection)
}

func TestOpenAIGatewayService_UserAffinityUnknownQuotaFallsBackForSnapshotHealing(t *testing.T) {
	accountID := int64(36121)
	repo := &schedulerAffinityAccountRepo{
		schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{
			{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		}},
		stats: map[int64]OpenAIUserAffinityCandidate{accountID: {AccountID: accountID}},
	}
	svc := &OpenAIGatewayService{accountRepo: repo, cfg: &config.Config{RunMode: config.RunModeSimple}}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	ctx = context.WithValue(ctx, ctxkey.RequestID, "affinity-unknown-quota")

	selection, handled, err := svc.selectOpenAIUserAffinityNewResident(
		ctx, nil, "gpt-5.1", nil, false, "", "", OpenAIUpstreamTransportHTTPSSE,
		openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE),
		DefaultOpenAIUserAffinityConfig(), time.Now().UTC(), true,
	)
	require.NoError(t, err)
	require.False(t, handled)
	require.Nil(t, selection)
}

func TestResolveOpenAIUserAffinityNewResidentPolicyDisablesAffinityReuseAfterExcludedReset(t *testing.T) {
	sourceAccountID := int64(36141)
	excludeSource := true
	originalExcluded := map[int64]struct{}{36140: {}}
	placement := &OpenAIUserPlacement{
		Status:                    "reset",
		ResetExcludeSourceAccount: &excludeSource,
		ResetSourceAccountID:      &sourceAccountID,
	}

	excluded, preferExistingAffinity := resolveOpenAIUserAffinityNewResidentPolicy(placement, originalExcluded)

	require.False(t, preferExistingAffinity)
	require.Contains(t, excluded, int64(36140))
	require.Contains(t, excluded, sourceAccountID)
	require.NotContains(t, originalExcluded, sourceAccountID, "不得修改调用方传入的排除集合")
}

func TestOpenAIUserAffinityPreviousResponseAttemptRefreshesMatchingPlacement(t *testing.T) {
	groupID := int64(11003)
	accountID := int64(36131)
	scopeKey := openAIUserAffinityScopeKey(&groupID, false, OpenAIEndpointCapabilityResponses, "", OpenAIUpstreamTransportHTTPSSE)
	now := time.Now().UTC()
	repo := &schedulerAffinityAccountRepo{
		placement: &OpenAIUserPlacement{
			UserID: 42, ScopeKey: scopeKey, AccountID: &accountID, Generation: 5,
			Status: "active", AssignedAt: now, ExpiresAt: now.Add(time.Hour),
		},
	}
	configValue := DefaultOpenAIUserAffinityConfig()
	configValue.Enabled = true
	configValue.Mode = OpenAIUserAffinityModeEnforce
	raw, err := json.Marshal(configValue)
	require.NoError(t, err)
	svc := &OpenAIGatewayService{
		accountRepo:    repo,
		settingService: NewSettingService(&openAIUserAffinitySuccessSettingRepo{value: string(raw)}, &config.Config{}),
	}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	ctx = context.WithValue(ctx, ctxkey.RequestID, "previous-response-affinity-touch")
	req := newOpenAIUserAffinityScheduleRequest(
		&groupID, PlatformOpenAI, "resp_1", "gpt-5.1", OpenAIUpstreamTransportHTTPSSE,
		OpenAIEndpointCapabilityResponses, "", false, nil,
	)

	svc.rememberOpenAIUserAffinityPreviousResponseAttempt(ctx, req, accountID)
	attempt, found := svc.openAIUserAffinityAttempt(ctx, accountID)
	require.True(t, found)
	require.Equal(t, int64(5), attempt.generation)
}
