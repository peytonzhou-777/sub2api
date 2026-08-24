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
	binding        *OpenAIUserConversationBinding
	bindingsByHash map[string]*OpenAIUserConversationBinding
	bindingLookups []string
	slots          []OpenAIUserResidentSlot
	reservations   []OpenAIUserConversationReservation
	failovers      []OpenAIUserConversationFailoverReservation
	replacements   []OpenAIUserResidentSlotReplacementReservation
	converges      int
	aliasType      string
	aliasHash      string
	aliasBindings  map[string]*OpenAIUserConversationBinding
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

func (r *schedulerConversationAffinityRepo) ListOpenAIUserResidentSlots(context.Context, int64, string) ([]OpenAIUserResidentSlot, error) {
	return append([]OpenAIUserResidentSlot(nil), r.slots...), nil
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

func (r *schedulerConversationAffinityRepo) ReserveOpenAIUserConversationBinding(_ context.Context, reservation OpenAIUserConversationReservation) (*OpenAIUserConversationBinding, bool, error) {
	r.reservations = append(r.reservations, reservation)
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
		Status: "provisional", ContextRebuildable: reservation.ContextRebuildable,
		ExpiresAt: time.Now().UTC().Add(2 * time.Minute), ProvisionalToken: reservation.ProvisionalToken,
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

func (r *schedulerConversationAffinityRepo) CommitOpenAIUserConversationBinding(context.Context, OpenAIUserConversationTransition) (bool, error) {
	return false, nil
}

func (r *schedulerConversationAffinityRepo) RollbackOpenAIUserConversationBinding(context.Context, OpenAIUserConversationTransition) (bool, error) {
	return false, nil
}

func (r *schedulerConversationAffinityRepo) ReserveOpenAIUserConversationFailover(_ context.Context, reservation OpenAIUserConversationFailoverReservation) (*OpenAIUserConversationTransition, bool, error) {
	r.failovers = append(r.failovers, reservation)
	return &OpenAIUserConversationTransition{
		BindingID: reservation.BindingID, UserID: reservation.UserID, ScopeKey: reservation.ScopeKey,
		ConversationHash: reservation.ConversationHash, ResidentSlotID: reservation.TargetResidentSlotID,
		AccountID: reservation.TargetAccountID, SlotGeneration: reservation.TargetSlotGeneration,
		ProvisionalToken: reservation.ProvisionalToken, Failover: true,
		SourceAccountID: reservation.SourceAccountID, SourceSlotID: reservation.SourceResidentSlotID,
		SourceGeneration: reservation.SourceSlotGeneration, Config: reservation.Config,
	}, true, nil
}

func (r *schedulerConversationAffinityRepo) ReserveOpenAIUserResidentSlotReplacement(_ context.Context, reservation OpenAIUserResidentSlotReplacementReservation) (*OpenAIUserConversationTransition, bool, error) {
	r.replacements = append(r.replacements, reservation)
	return &OpenAIUserConversationTransition{
		BindingID: reservation.BindingID, UserID: reservation.UserID, ScopeKey: reservation.ScopeKey,
		ConversationHash: reservation.ConversationHash, ResidentSlotID: 901,
		AccountID: reservation.TargetAccountID, SlotGeneration: 9,
		ProvisionalToken: reservation.ProvisionalToken, Failover: true, Replacement: true,
		SourceAccountID: reservation.SourceAccountID, SourceSlotID: reservation.SourceResidentSlotID,
		SourceGeneration: reservation.SourceSlotGeneration, ReplacementSlotID: reservation.VictimSlotID,
		Config: reservation.Config,
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

func openAIUserAffinityTestQuotaExtra(now time.Time, used7D float64) map[string]any {
	return map[string]any{
		"codex_usage_updated_at":  now.Format(time.RFC3339Nano),
		"codex_7d_window_minutes": 10080,
		"codex_7d_used_percent":   used7D,
		"codex_7d_reset_at":       now.Add(6 * 24 * time.Hour).Format(time.RFC3339Nano),
	}
}

func TestOpenAIGatewayService_MultiSlotUsesIdleResidentFirst(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36211, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 36212, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	slots := []OpenAIUserResidentSlot{
		{ID: 11, UserID: 42, ScopeKey: openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE), AccountID: 36211, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 2, ScoreUpdatedAt: now, AdmittedAt: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour), ActiveConversationCount: 1},
		{ID: 12, UserID: 42, ScopeKey: openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE), AccountID: 36212, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 1, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour)},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "idle-resident"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36212), selection.Account.ID)
	require.Len(t, repo.reservations, 1)
	require.Equal(t, int64(36212), repo.reservations[0].AccountID)
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
		AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour), ActiveConversationCount: 1,
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
		ExpiresAt: now.Add(24 * time.Hour), ActiveConversationCount: 1,
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

func TestOpenAIGatewayService_MultiSlotReturnsHottestWhenFullAndBusy(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36231, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
		{ID: 36232, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1},
	}
	scopeKey := openAIUserAffinityScopeKey(nil, false, "", "", OpenAIUpstreamTransportHTTPSSE)
	slots := []OpenAIUserResidentSlot{
		{ID: 31, UserID: 42, ScopeKey: scopeKey, AccountID: 36231, Generation: 1, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 5, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour), ActiveConversationCount: 1},
		{ID: 32, UserID: 42, ScopeKey: scopeKey, AccountID: 36232, Generation: 2, Status: OpenAIUserResidentSlotStatusActive, UsageScore: 2, ScoreUpdatedAt: now, AdmittedAt: now, ExpiresAt: now.Add(24 * time.Hour), ActiveConversationCount: 1},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, slots, accounts, nil, 2)
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "full-hottest"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(36231), selection.Account.ID)
	require.Len(t, repo.reservations, 1)
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
		ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
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
	require.ErrorIs(t, err, ErrOpenAIPreviousResponseAccountUnavailable)
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
	require.ErrorIs(t, err, ErrOpenAIPreviousResponseAccountUnavailable)
	require.Nil(t, selection)
	require.False(t, decision.StickyPreviousHit)
}

func TestOpenAIGatewayService_ReplacesColdestSlotOnlyAfterAllSlotsUnavailable(t *testing.T) {
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
	require.Equal(t, int64(62), repo.replacements[0].VictimSlotID)
	require.Len(t, repo.replacements[0].CheckedSlots, 2)
	attempt, ok := svc.openAIUserAffinityAttempt(ctx, 36263)
	require.True(t, ok)
	require.True(t, attempt.conversation.Replacement)
}

func TestOpenAIGatewayService_ResetScopeExcludesAllOldSlotsAndSkipsAffinityPriority(t *testing.T) {
	now := time.Now().UTC()
	accounts := []Account{
		{ID: 36271, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 10)},
		{ID: 36272, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Extra: openAIUserAffinityTestQuotaExtra(now, 55)},
	}
	stats := map[int64]OpenAIUserAffinityCandidate{
		36271: {AccountID: 36271},
		36272: {AccountID: 36272, UserAlreadyActive: true, UserAlreadyResident: true},
	}
	svc, repo, ctx := newMultiSlotAffinitySchedulerTestService(t, nil, accounts, stats, 2)
	repo.resetExclusions = []int64{36269, 36270}
	req := newOpenAIUserAffinityScheduleRequest(nil, PlatformOpenAI, "", "", OpenAIUpstreamTransportHTTPSSE, "", "", false, nil)
	req.SessionHash = "manual-reset-new-conversation"

	selection, found, err := svc.selectOpenAIUserAffinityResidentSlots(ctx, req)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, selection)
	require.Equal(t, int64(36271), selection.Account.ID, "重置后应直接按 BestFit，而不是优先已触达账号")
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
