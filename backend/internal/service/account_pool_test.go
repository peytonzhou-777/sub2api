package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type accountPoolSettingRepo struct{ values map[string]string }

func (r *accountPoolSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}
func (r *accountPoolSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}
func (r *accountPoolSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *accountPoolSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		result[key] = r.values[key]
	}
	return result, nil
}
func (r *accountPoolSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}
func (r *accountPoolSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}
func (r *accountPoolSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

type accountPoolNotReadyCache struct{}

func (accountPoolNotReadyCache) AcquireAccountPoolBuildLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (accountPoolNotReadyCache) RenewAccountPoolBuildLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (accountPoolNotReadyCache) ReleaseAccountPoolBuildLock(context.Context, string) error {
	return nil
}
func (accountPoolNotReadyCache) ReadAccountPoolPreviousCapacities(context.Context, string, []int64) (map[int64]PublicAccountPoolCapacity, error) {
	return nil, ErrAccountPoolSnapshotNotReady
}
func (accountPoolNotReadyCache) WriteAccountPoolGeneration(context.Context, string, string, []PublicAccountPoolAccount, time.Duration) error {
	return nil
}
func (accountPoolNotReadyCache) ReadAccountPoolPage(context.Context, string, int, int, *int64) ([]PublicAccountPoolAccount, int64, error) {
	return nil, 0, ErrAccountPoolSnapshotNotReady
}

type accountPoolCountingSource struct{ pageCalls int }

func (*accountPoolCountingSource) ListAccountPoolBuildBatch(context.Context, int64, int) ([]AccountPoolSourceRecord, bool, error) {
	return nil, false, nil
}

type accountPoolBuildSource struct {
	records []AccountPoolSourceRecord
	err     error
	delay   time.Duration
}

func (s *accountPoolBuildSource) ListAccountPoolBuildBatch(ctx context.Context, _ int64, _ int) ([]AccountPoolSourceRecord, bool, error) {
	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	return s.records, false, s.err
}
func (*accountPoolBuildSource) ListAccountPoolPage(context.Context, int, int, *int64) ([]AccountPoolSourceRecord, int64, error) {
	return nil, 0, nil
}

type accountPoolConcurrencyReader struct {
	counts map[int64]int
	err    error
}

type accountPoolPersonalUsageReaderStub struct {
	stats *AccountPoolPersonalUsageStats
	calls atomic.Int64
	delay time.Duration
}

func (r *accountPoolPersonalUsageReaderStub) GetUserAccountPersonalUsage(ctx context.Context, _ int64, _ int64, _, _, _ time.Time) (*AccountPoolPersonalUsageStats, error) {
	r.calls.Add(1)
	if r.delay > 0 {
		timer := time.NewTimer(r.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.stats, nil
}

type accountPoolPersonalUsageCacheStub struct {
	item  PublicAccountPoolAccount
	value *AccountPoolPersonalUsage
}

func (c *accountPoolPersonalUsageCacheStub) AcquireAccountPoolBuildLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *accountPoolPersonalUsageCacheStub) RenewAccountPoolBuildLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (c *accountPoolPersonalUsageCacheStub) ReleaseAccountPoolBuildLock(context.Context, string) error {
	return nil
}
func (c *accountPoolPersonalUsageCacheStub) ReadAccountPoolPreviousCapacities(context.Context, string, []int64) (map[int64]PublicAccountPoolCapacity, error) {
	return nil, ErrAccountPoolSnapshotNotReady
}
func (c *accountPoolPersonalUsageCacheStub) WriteAccountPoolGeneration(context.Context, string, string, []PublicAccountPoolAccount, time.Duration) error {
	return nil
}
func (c *accountPoolPersonalUsageCacheStub) ReadAccountPoolPage(_ context.Context, _ string, _ int, _ int, accountID *int64) ([]PublicAccountPoolAccount, int64, error) {
	if accountID == nil || *accountID != c.item.ID {
		return []PublicAccountPoolAccount{}, 0, nil
	}
	return []PublicAccountPoolAccount{c.item}, 1, nil
}
func (c *accountPoolPersonalUsageCacheStub) GetAccountPoolPersonalUsage(context.Context, string) (*AccountPoolPersonalUsage, bool, error) {
	if c.value == nil {
		return nil, false, nil
	}
	return c.value, true, nil
}
func (c *accountPoolPersonalUsageCacheStub) SetAccountPoolPersonalUsage(_ context.Context, _ string, value *AccountPoolPersonalUsage, _ time.Duration) error {
	c.value = value
	return nil
}

func (r accountPoolConcurrencyReader) GetAccountConcurrencyBatch(context.Context, []int64) (map[int64]int, error) {
	return r.counts, r.err
}

type accountPoolBuildCache struct {
	acquired    bool
	renewed     bool
	previous    map[int64]PublicAccountPoolCapacity
	previousErr error
	written     []PublicAccountPoolAccount
	writeCalls  int
	renewCalls  atomic.Int64
}

func (c *accountPoolBuildCache) AcquireAccountPoolBuildLock(context.Context, string, time.Duration) (bool, error) {
	return c.acquired, nil
}
func (c *accountPoolBuildCache) RenewAccountPoolBuildLock(context.Context, string, time.Duration) (bool, error) {
	c.renewCalls.Add(1)
	return c.renewed, nil
}
func (*accountPoolBuildCache) ReleaseAccountPoolBuildLock(context.Context, string) error { return nil }
func (c *accountPoolBuildCache) ReadAccountPoolPreviousCapacities(context.Context, string, []int64) (map[int64]PublicAccountPoolCapacity, error) {
	return c.previous, c.previousErr
}
func (c *accountPoolBuildCache) WriteAccountPoolGeneration(_ context.Context, _, _ string, items []PublicAccountPoolAccount, _ time.Duration) error {
	c.writeCalls++
	c.written = append([]PublicAccountPoolAccount(nil), items...)
	return nil
}
func (*accountPoolBuildCache) ReadAccountPoolPage(context.Context, string, int, int, *int64) ([]PublicAccountPoolAccount, int64, error) {
	return nil, 0, ErrAccountPoolSnapshotNotReady
}
func (s *accountPoolCountingSource) ListAccountPoolPage(context.Context, int, int, *int64) ([]AccountPoolSourceRecord, int64, error) {
	s.pageCalls++
	return nil, 0, nil
}

func TestAccountPoolNotReadyDoesNotFallbackToDatabase(t *testing.T) {
	source := &accountPoolCountingSource{}
	svc := NewAccountPoolService(source, accountPoolNotReadyCache{}, nil, AccountPoolOptions{})
	_, err := svc.List(context.Background(), "new-epoch", 1, 20, nil)
	if !errors.Is(err, ErrAccountPoolSnapshotNotReady) {
		t.Fatalf("期望 not ready，实际: %v", err)
	}
	if source.pageCalls != 0 {
		t.Fatalf("预热期间不应数据库降级，调用次数: %d", source.pageCalls)
	}
}

func TestPrepareAccountPoolEnabledEpochOnlyRotatesOnEnableTransition(t *testing.T) {
	repo := &accountPoolSettingRepo{values: map[string]string{
		SettingKeyAccountPoolEnabled:      "false",
		SettingKeyAccountPoolEnabledEpoch: "old-epoch",
	}}
	svc := NewSettingService(repo, nil)
	updates := map[string]string{SettingKeyAccountPoolEnabled: "true"}
	if err := svc.prepareAccountPoolEnabledEpoch(context.Background(), updates); err != nil {
		t.Fatalf("准备启用 epoch: %v", err)
	}
	if updates[SettingKeyAccountPoolEnabledEpoch] == "" || updates[SettingKeyAccountPoolEnabledEpoch] == "old-epoch" {
		t.Fatalf("关到开必须生成新 epoch，得到 %q", updates[SettingKeyAccountPoolEnabledEpoch])
	}

	repo.values[SettingKeyAccountPoolEnabled] = "true"
	repo.values[SettingKeyAccountPoolEnabledEpoch] = "current-epoch"
	updates = map[string]string{SettingKeyAccountPoolEnabled: "true"}
	if err := svc.prepareAccountPoolEnabledEpoch(context.Background(), updates); err != nil {
		t.Fatalf("保持开启状态: %v", err)
	}
	if _, rotated := updates[SettingKeyAccountPoolEnabledEpoch]; rotated {
		t.Fatal("保持开启时不应轮换 epoch")
	}
}

func TestAccountPoolResetCountFreshness(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	svc := NewAccountPoolService(nil, nil, nil, AccountPoolOptions{UsageFresh: 15 * time.Minute, UsageRetention: time.Hour})
	svc.now = func() time.Time { return now }

	zero := 0
	freshAt := now.Add(-time.Minute)
	item := svc.mapPublicAccount(AccountPoolSourceRecord{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ResetCount: &zero, ResetCountObservedAt: &freshAt}, nil, now)
	if item.ResetCount == nil || *item.ResetCount != 0 || item.ResetCountState != string(AccountPoolFreshnessFresh) {
		t.Fatalf("新鲜零值应保留，得到 count=%v state=%s", item.ResetCount, item.ResetCountState)
	}

	positive := 3
	item = svc.mapPublicAccount(AccountPoolSourceRecord{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ResetCount: &positive, ResetCountObservedAt: &freshAt}, nil, now)
	if item.ResetCount == nil || *item.ResetCount != 3 {
		t.Fatalf("新鲜正整数应展示，得到 %v", item.ResetCount)
	}

	staleAt := now.Add(-30 * time.Minute)
	item = svc.mapPublicAccount(AccountPoolSourceRecord{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ResetCount: &positive, ResetCountObservedAt: &staleAt}, nil, now)
	if item.ResetCount != nil || item.ResetCountState != string(AccountPoolFreshnessStale) {
		t.Fatalf("陈旧次数不应展示，得到 count=%v state=%s", item.ResetCount, item.ResetCountState)
	}

	negative := -1
	item = svc.mapPublicAccount(AccountPoolSourceRecord{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ResetCount: &negative, ResetCountObservedAt: &freshAt}, nil, now)
	if item.ResetCount != nil || item.ResetCountState != string(AccountPoolFreshnessUnavailable) {
		t.Fatalf("负数观测应不可用，得到 count=%v state=%s", item.ResetCount, item.ResetCountState)
	}
}

func TestAccountPoolPresentationProjection(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	svc := NewAccountPoolService(nil, nil, nil, AccountPoolOptions{})
	item := svc.mapPublicAccount(AccountPoolSourceRecord{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"auth_mode":               "personal_access_token",
			"plan_type":               "plus",
			"subscription_expires_at": now.Add(24 * time.Hour).Format(time.RFC3339),
		},
		Extra: map[string]any{
			"privacy_mode":              "training_off",
			"openai_compact_mode":       "force_on",
			"openai_compact_supported":  true,
			"openai_compact_checked_at": now.Format(time.RFC3339),
		},
	}, nil, now)
	if item.AuthMode != "personal_access_token" || item.PlanType != "plus" || item.PrivacyMode != "training_off" {
		t.Fatalf("平台徽章字段未按白名单投影: %+v", item)
	}
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("序列化号池账号: %v", err)
	}
	for _, field := range []string{"subscription_expires_at", "openai_compact_mode", "openai_compact_supported", "openai_compact_checked_at"} {
		if strings.Contains(string(payload), field) {
			t.Fatalf("号池响应不应包含字段 %s: %s", field, payload)
		}
	}

	antigravity := svc.mapPublicAccount(AccountPoolSourceRecord{
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			"load_code_assist": map[string]any{"paidTier": map[string]any{"id": "g1-pro-tier"}},
		},
	}, nil, now)
	if antigravity.AntigravityTier != "g1-pro-tier" {
		t.Fatalf("Antigravity 订阅档位未投影: %q", antigravity.AntigravityTier)
	}
	shadow := svc.mapPublicAccount(AccountPoolSourceRecord{
		Platform:          PlatformOpenAI,
		Type:              AccountTypeOAuth,
		ParentAccountID:   func() *int64 { value := int64(10); return &value }(),
		ParentCredentials: map[string]any{"plan_type": "pro"},
		ParentExtra:       map[string]any{"privacy_mode": "training_off"},
	}, nil, now)
	if shadow.PlanType != "pro" || shadow.PrivacyMode != "training_off" {
		t.Fatalf("影子账号应回退母账号展示字段: %+v", shadow)
	}
}

func TestAccountPoolReconcilePreservesPreviousConcurrencyOnReadFailure(t *testing.T) {
	observedAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	current := 3
	cache := &accountPoolBuildCache{
		acquired: true,
		renewed:  true,
		previous: map[int64]PublicAccountPoolCapacity{
			7: {CurrentConcurrency: &current, MaxConcurrency: 5, ObservedAt: &observedAt, State: AccountPoolFreshnessFresh},
		},
	}
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{{ID: 7, Concurrency: 8}}}
	svc := NewAccountPoolService(source, cache, accountPoolConcurrencyReader{err: errors.New("redis unavailable")}, AccountPoolOptions{})

	if err := svc.Reconcile(context.Background(), "generation-b", "epoch-a"); err != nil {
		t.Fatalf("并发读取失败时应复用上一代容量: %v", err)
	}
	if cache.writeCalls != 1 || len(cache.written) != 1 {
		t.Fatalf("应发布一个完整代次，writeCalls=%d items=%d", cache.writeCalls, len(cache.written))
	}
	capacity := cache.written[0].Capacity
	if capacity.CurrentConcurrency == nil || *capacity.CurrentConcurrency != 3 || capacity.ObservedAt != &observedAt || capacity.MaxConcurrency != 8 {
		t.Fatalf("应保留旧观测并使用最新最大容量，得到 %+v", capacity)
	}
}

func TestAccountPoolReconcileAllowsUnavailableConcurrencyWithoutPreviousGeneration(t *testing.T) {
	cache := &accountPoolBuildCache{acquired: true, renewed: true, previousErr: ErrAccountPoolSnapshotNotReady}
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{{ID: 8, Concurrency: 4}}}
	svc := NewAccountPoolService(source, cache, accountPoolConcurrencyReader{err: errors.New("redis unavailable")}, AccountPoolOptions{})

	if err := svc.Reconcile(context.Background(), "generation-a", "epoch-a"); err != nil {
		t.Fatalf("首次预热应允许不可用并发: %v", err)
	}
	if len(cache.written) != 1 || cache.written[0].Capacity.State != AccountPoolFreshnessUnavailable {
		t.Fatalf("首次预热并发应为 unavailable，得到 %+v", cache.written)
	}
}

func TestAccountPoolReconcileDoesNotPublishWhenPreviousObservationReadFails(t *testing.T) {
	cache := &accountPoolBuildCache{acquired: true, renewed: true, previousErr: ErrAccountPoolSnapshotUnavailable}
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{{ID: 9, Concurrency: 4}}}
	svc := NewAccountPoolService(source, cache, accountPoolConcurrencyReader{err: errors.New("redis unavailable")}, AccountPoolOptions{})

	err := svc.Reconcile(context.Background(), "generation-b", "epoch-a")
	if err == nil || cache.writeCalls != 0 {
		t.Fatalf("上一代观测也不可读时不得切换，err=%v writeCalls=%d", err, cache.writeCalls)
	}
}

func TestAccountPoolReconcileReportsBuildLockContention(t *testing.T) {
	cache := &accountPoolBuildCache{acquired: false, renewed: true}
	svc := NewAccountPoolService(&accountPoolBuildSource{}, cache, accountPoolConcurrencyReader{}, AccountPoolOptions{})

	err := svc.Reconcile(context.Background(), "generation-a", "epoch-a")
	if !errors.Is(err, ErrAccountPoolBuildLockNotAcquired) || cache.writeCalls != 0 {
		t.Fatalf("锁竞争应明确跳过且不发布，err=%v writeCalls=%d", err, cache.writeCalls)
	}
}

func TestAccountPoolReconcileRenewsBuildLockDuringLongBuild(t *testing.T) {
	cache := &accountPoolBuildCache{acquired: true, renewed: true}
	source := &accountPoolBuildSource{delay: 40 * time.Millisecond}
	svc := NewAccountPoolService(source, cache, accountPoolConcurrencyReader{}, AccountPoolOptions{BuildLockTTL: 15 * time.Millisecond})

	if err := svc.Reconcile(context.Background(), "generation-a", "epoch-a"); err != nil {
		t.Fatalf("长构建应通过续租完成: %v", err)
	}
	if calls := cache.renewCalls.Load(); calls < 2 {
		t.Fatalf("应在构建期间续租并在发布前复核，调用次数: %d", calls)
	}
}

func TestAccountPoolPersonalUsageUsesLocalWindowsAndPrivateCache(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	fiveHourReset := now.Add(90 * time.Minute)
	sevenDayReset := now.Add(24 * time.Hour)
	cache := &accountPoolPersonalUsageCacheStub{item: PublicAccountPoolAccount{
		ID:       7,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		UsageWindows: []PublicAccountPoolUsageWindow{
			{Code: "5h", ResetsAt: &fiveHourReset},
			{Code: "7d", ResetsAt: &sevenDayReset},
		},
	}}
	reader := &accountPoolPersonalUsageReaderStub{stats: &AccountPoolPersonalUsageStats{
		FiveHour: AccountPoolUsageMetrics{Requests: 0, Tokens: 12, ActualCost: 0},
		SevenDay: AccountPoolUsageMetrics{Requests: 4, Tokens: 300, ActualCost: 1.25},
	}}
	svc := NewAccountPoolService(nil, cache, nil, AccountPoolOptions{})
	svc.now = func() time.Time { return now }
	svc.SetPersonalUsageReader(reader)

	value, err := svc.GetPersonalUsage(context.Background(), "epoch-a", 42, 7)
	if err != nil {
		t.Fatalf("查询个人用量: %v", err)
	}
	if reader.calls.Load() != 1 || len(value.Windows) != 2 {
		t.Fatalf("首次查询应读取一次并返回两个窗口，calls=%d windows=%d", reader.calls.Load(), len(value.Windows))
	}
	if value.Windows[0].Requests != 0 || value.Windows[0].StartAt != fiveHourReset.Add(-5*time.Hour) {
		t.Fatalf("5h 零值或窗口起点错误: %+v", value.Windows[0])
	}
	if value.Windows[1].ActualCost != 1.25 || value.Windows[1].StartAt != sevenDayReset.Add(-7*24*time.Hour) {
		t.Fatalf("7d 聚合或窗口起点错误: %+v", value.Windows[1])
	}
	if _, err := svc.GetPersonalUsage(context.Background(), "epoch-a", 42, 7); err != nil {
		t.Fatalf("缓存命中查询: %v", err)
	}
	if reader.calls.Load() != 1 {
		t.Fatalf("短缓存命中不应重复查询，calls=%d", reader.calls.Load())
	}
}

func TestAccountPoolPersonalUsageRejectsUnsupportedAccount(t *testing.T) {
	cache := &accountPoolPersonalUsageCacheStub{item: PublicAccountPoolAccount{ID: 8, Platform: PlatformGemini, Type: AccountTypeOAuth}}
	reader := &accountPoolPersonalUsageReaderStub{stats: &AccountPoolPersonalUsageStats{}}
	svc := NewAccountPoolService(nil, cache, nil, AccountPoolOptions{})
	svc.SetPersonalUsageReader(reader)
	if _, err := svc.GetPersonalUsage(context.Background(), "epoch-a", 42, 8); !errors.Is(err, ErrAccountPoolPersonalUsageUnsupported) {
		t.Fatalf("非 OpenAI/Anthropic 账号应拒绝个人用量，err=%v", err)
	}
	if reader.calls.Load() != 0 {
		t.Fatal("不支持的账号不应触达 usage_logs")
	}
}

func TestAccountPoolPersonalUsageMergesConcurrentQueries(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	cache := &accountPoolPersonalUsageCacheStub{item: PublicAccountPoolAccount{ID: 9, Platform: PlatformAnthropic, Type: AccountTypeSetupToken}}
	reader := &accountPoolPersonalUsageReaderStub{
		stats: &AccountPoolPersonalUsageStats{FiveHour: AccountPoolUsageMetrics{Requests: 1}},
		delay: 30 * time.Millisecond,
	}
	svc := NewAccountPoolService(nil, cache, nil, AccountPoolOptions{})
	svc.now = func() time.Time { return now }
	svc.SetPersonalUsageReader(reader)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.GetPersonalUsage(context.Background(), "epoch-a", 42, 9)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("并发个人用量查询失败: %v", err)
		}
	}
	if reader.calls.Load() != 1 {
		t.Fatalf("并发请求应合并为一次 usage_logs 查询，calls=%d", reader.calls.Load())
	}
}
