package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type schedulerAffinityAccountRepo struct {
	schedulerTestOpenAIAccountRepo
	OpenAIUserAffinityRuntimeStore
	placement *OpenAIUserPlacement
	stats     map[int64]OpenAIUserAffinityCandidate
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

func (r *schedulerAffinityAccountRepo) GetOpenAIUserAffinityMigrationAuthorizedAt(context.Context, int64, int64, int64, string) (*time.Time, error) {
	return nil, nil
}

func (r *schedulerAffinityAccountRepo) BeginOpenAIUserAffinityReentry(context.Context, OpenAIUserAffinityReentryBegin) (*OpenAIUserAffinityReentryAdmission, error) {
	return &OpenAIUserAffinityReentryAdmission{Required: false}, nil
}

type schedulerAffinityCache struct {
	*schedulerTestGatewayCache
	OpenAIUserAffinityReentryQueue
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
		DefaultOpenAIUserAffinityConfig(), time.Now().UTC(),
	)
	require.NoError(t, err)
	require.False(t, handled)
	require.Nil(t, selection)
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
