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
