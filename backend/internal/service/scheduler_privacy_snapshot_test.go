//go:build unit

package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type privacySnapshotCacheStub struct {
	SchedulerCache
	mu       sync.Mutex
	accounts []*Account
	setCalls int
}

func (c *privacySnapshotCacheStub) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]*Account, 0, len(c.accounts))
	for _, account := range c.accounts {
		cloned := *account
		result = append(result, &cloned)
	}
	return result, len(result) > 0, nil
}

func (c *privacySnapshotCacheStub) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *privacySnapshotCacheStub) SetSnapshot(_ context.Context, _ SchedulerBucket, _ SchedulerBucketWriteToken, accounts []Account) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setCalls++
	c.accounts = make([]*Account, 0, len(accounts))
	for i := range accounts {
		account := annotateSchedulerAccount(accounts[i])
		c.accounts = append(c.accounts, &account)
	}
	return nil
}

type privacySnapshotAccountRepoStub struct {
	AccountRepository
	account Account
	calls   atomic.Int64
}

func (r *privacySnapshotAccountRepoStub) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	r.calls.Add(1)
	return []Account{r.account}, nil
}

func TestResolveSchedulerPrivacyStatus(t *testing.T) {
	tests := []struct {
		name    string
		account Account
		want    SchedulerPrivacyStatus
	}{
		{name: "openai compliant", account: Account{Platform: PlatformOpenAI, Extra: map[string]any{"privacy_mode": PrivacyModeTrainingOff}}, want: SchedulerPrivacyCompliant},
		{name: "openai failed", account: Account{Platform: PlatformOpenAI, Extra: map[string]any{"privacy_mode": PrivacyModeFailed}}, want: SchedulerPrivacyNoncompliant},
		{name: "openai missing", account: Account{Platform: PlatformOpenAI}, want: SchedulerPrivacyUnknown},
		{name: "openai unrecognized", account: Account{Platform: PlatformOpenAI, Extra: map[string]any{"privacy_mode": "future_value"}}, want: SchedulerPrivacyUnknown},
		{name: "other platform", account: Account{Platform: PlatformAnthropic}, want: SchedulerPrivacyNotApplicable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, ResolveSchedulerPrivacyStatus(test.account))
		})
	}
}

func TestSchedulerSnapshotMissingPrivacyFieldRefreshesFromDatabase(t *testing.T) {
	groupID := int64(91)
	databaseAccount := Account{
		ID: 901, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Extra: map[string]any{"privacy_mode": PrivacyModeTrainingOff},
	}
	cache := &privacySnapshotCacheStub{accounts: []*Account{{
		ID: 901, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		SchedulerPrivacyStatus:        SchedulerPrivacyUnknown,
		SchedulerSnapshotNeedsRefresh: true,
	}}}
	repo := &privacySnapshotAccountRepoStub{account: databaseAccount}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	cfg.Gateway.Scheduling.DbFallbackMaxQPS = 100
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, cfg)

	accounts, _, err := svc.ListSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, SchedulerPrivacyCompliant, accounts[0].SchedulerPrivacyStatus)
	require.False(t, accounts[0].SchedulerSnapshotNeedsRefresh)
	require.Equal(t, int64(1), repo.calls.Load())
	require.Equal(t, 1, cache.setCalls)

	accounts, _, err = svc.ListSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Equal(t, SchedulerPrivacyCompliant, accounts[0].SchedulerPrivacyStatus)
	require.Equal(t, int64(1), repo.calls.Load(), "重建后的强类型快照应直接命中")
}

func TestSchedulerSnapshotConcurrentRefreshNeverReturnsUnknown(t *testing.T) {
	groupID := int64(92)
	databaseAccount := Account{
		ID: 902, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		Extra: map[string]any{"privacy_mode": PrivacyModeTrainingOff},
	}
	cache := &privacySnapshotCacheStub{accounts: []*Account{{
		ID: 902, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true,
		SchedulerPrivacyStatus:        SchedulerPrivacyUnknown,
		SchedulerSnapshotNeedsRefresh: true,
	}}}
	repo := &privacySnapshotAccountRepoStub{account: databaseAccount}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	cfg.Gateway.Scheduling.DbFallbackMaxQPS = 100
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, cfg)

	const workers = 12
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			accounts, _, err := svc.ListSchedulableAccounts(context.Background(), &groupID, PlatformOpenAI, false)
			if err == nil && (len(accounts) != 1 || accounts[0].SchedulerPrivacyStatus != SchedulerPrivacyCompliant) {
				err = ErrSchedulerCacheNotReady
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.GreaterOrEqual(t, repo.calls.Load(), int64(1))
}

func TestOpenAIPrivacyFilterIsSideEffectFree(t *testing.T) {
	groupID := int64(93)
	groupRepo := &mockGroupRepoForGateway{groups: map[int64]*Group{
		groupID: {ID: groupID, Name: "privacy-required", RequirePrivacySet: true},
	}}
	base := Account{
		ID: 903, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		GroupIDs: []int64{groupID},
	}

	t.Run("training_off remains eligible", func(t *testing.T) {
		account := base
		account.Extra = map[string]any{"privacy_mode": PrivacyModeTrainingOff}
		account = annotateSchedulerAccount(account)
		cache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{&account}, accountsByID: map[int64]*Account{account.ID: &account}}
		svc := &OpenAIGatewayService{
			accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
			cfg:                &config.Config{RunMode: config.RunModeStandard},
			schedulerSnapshot:  &SchedulerSnapshotService{cache: cache, groupRepo: groupRepo},
			concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
		}
		scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
		selection, _, _, _, err := scheduler.selectByLoadBalance(context.Background(), OpenAIAccountScheduleRequest{
			GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-5.1",
		})
		require.NoError(t, err)
		require.NotNil(t, selection)
		require.Equal(t, account.ID, selection.Account.ID)
	})

	t.Run("real failure is rejected without SetError", func(t *testing.T) {
		account := base
		account.ID++
		account.Extra = map[string]any{"privacy_mode": PrivacyModeFailed}
		account = annotateSchedulerAccount(account)
		cache := &openAISnapshotCacheStub{snapshotAccounts: []*Account{&account}, accountsByID: map[int64]*Account{account.ID: &account}}
		svc := &OpenAIGatewayService{
			// 嵌入接口为空；如果筛选热路径再次调用 SetError，本测试会直接 panic。
			accountRepo:       schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
			cfg:               &config.Config{RunMode: config.RunModeStandard},
			schedulerSnapshot: &SchedulerSnapshotService{cache: cache, groupRepo: groupRepo},
		}
		scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: newOpenAIAccountRuntimeStats()}
		selection, _, _, _, err := scheduler.selectByLoadBalance(context.Background(), OpenAIAccountScheduleRequest{
			GroupID: &groupID, Platform: PlatformOpenAI, RequestedModel: "gpt-5.1",
		})
		require.Error(t, err)
		require.Nil(t, selection)
		require.Contains(t, err.Error(), "privacy_noncompliant=1")
	})
}
