package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
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
func (accountPoolNotReadyCache) ReadAccountPoolPage(context.Context, string, int, int, AccountPoolListQuery) ([]PublicAccountPoolAccount, int64, error) {
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
func (*accountPoolBuildSource) ListAccountPoolPage(context.Context, int, int, *int64, string) ([]AccountPoolSourceRecord, int64, error) {
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

type accountPoolUserRelationReaderStub struct {
	relations []AccountPoolUserRelation
	err       error
}

type accountPoolUserAccessReaderStub struct {
	access  *AccountPoolUserAccess
	byGroup map[int64]*AccountPoolUserAccess
	err     error
}

type accountPoolVisibilityUserRepoStub struct {
	UserRepository
	user *User
}

type accountPoolDefaultGroupRepoStub struct {
	APIKeyRepository
	groupID *int64
	err     error
}

func (r accountPoolDefaultGroupRepoStub) GetAccountPoolDefaultGroupID(context.Context, int64) (*int64, error) {
	return r.groupID, r.err
}

func (r accountPoolVisibilityUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	return r.user, nil
}

type accountPoolVisibilityGroupRepoStub struct {
	GroupRepository
	groups     []Group
	accountIDs map[int64][]int64
}

func (r accountPoolVisibilityGroupRepoStub) ListActive(context.Context) ([]Group, error) {
	return r.groups, nil
}

func (r accountPoolVisibilityGroupRepoStub) GetAccountIDsByGroupIDs(_ context.Context, groupIDs []int64) ([]int64, error) {
	seen := make(map[int64]struct{})
	for _, groupID := range groupIDs {
		for _, accountID := range r.accountIDs[groupID] {
			seen[accountID] = struct{}{}
		}
	}
	result := make([]int64, 0, len(seen))
	for accountID := range seen {
		result = append(result, accountID)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

type accountPoolResidentStatsReaderStub struct {
	stats       map[int64]AccountPoolResidentStats
	err         error
	accountIDs  []int64
	activeSince time.Time
}

func (r accountPoolUserRelationReaderStub) ListAccountPoolUserRelations(context.Context, int64) ([]AccountPoolUserRelation, error) {
	return r.relations, r.err
}

func (r accountPoolUserAccessReaderStub) GetAccountPoolUserAccess(_ context.Context, _ int64, groupID *int64) (*AccountPoolUserAccess, error) {
	if r.err != nil {
		return nil, r.err
	}
	if groupID != nil && r.byGroup != nil {
		if access, ok := r.byGroup[*groupID]; ok {
			return access, nil
		}
		return &AccountPoolUserAccess{VisibleGroups: r.access.VisibleGroups, AccountIDs: []int64{}}, nil
	}
	return r.access, nil
}

func (r *accountPoolResidentStatsReaderStub) ListAccountPoolResidentStats(_ context.Context, accountIDs []int64, activeSince time.Time) (map[int64]AccountPoolResidentStats, error) {
	r.accountIDs = append([]int64(nil), accountIDs...)
	r.activeSince = activeSince
	return r.stats, r.err
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
func (c *accountPoolPersonalUsageCacheStub) ReadAccountPoolPage(_ context.Context, _ string, _ int, _ int, query AccountPoolListQuery) ([]PublicAccountPoolAccount, int64, error) {
	if query.AccountID == nil || *query.AccountID != c.item.ID {
		return []PublicAccountPoolAccount{}, 0, nil
	}
	if query.AllowedAccountIDs != nil {
		allowed := false
		for _, accountID := range query.AllowedAccountIDs {
			if accountID == c.item.ID {
				allowed = true
				break
			}
		}
		if !allowed {
			return []PublicAccountPoolAccount{}, 0, nil
		}
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
func (*accountPoolBuildCache) ReadAccountPoolPage(context.Context, string, int, int, AccountPoolListQuery) ([]PublicAccountPoolAccount, int64, error) {
	return nil, 0, ErrAccountPoolSnapshotNotReady
}
func (s *accountPoolCountingSource) ListAccountPoolPage(context.Context, int, int, *int64, string) ([]AccountPoolSourceRecord, int64, error) {
	s.pageCalls++
	return nil, 0, nil
}

func TestAccountPoolNotReadyDoesNotFallbackToDatabase(t *testing.T) {
	source := &accountPoolCountingSource{}
	svc := NewAccountPoolService(source, accountPoolNotReadyCache{}, nil, AccountPoolOptions{})
	_, err := svc.List(context.Background(), "new-epoch", 1, 20, AccountPoolListQuery{SortBy: AccountPoolSortByID, SortOrder: AccountPoolSortDesc})
	if !errors.Is(err, ErrAccountPoolSnapshotNotReady) {
		t.Fatalf("期望 not ready，实际: %v", err)
	}
	if source.pageCalls != 0 {
		t.Fatalf("预热期间不应数据库降级，调用次数: %d", source.pageCalls)
	}
}

func TestAccountPoolListForUserFiltersBeforePaginationAndProjectsRelations(t *testing.T) {
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{
		{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	}}
	svc := NewAccountPoolService(source, nil, nil, AccountPoolOptions{})
	svc.SetUserRelationReader(accountPoolUserRelationReaderStub{relations: []AccountPoolUserRelation{
		{AccountID: 11, IsCurrentResidence: true, IsPrimaryResidence: true},
		{AccountID: 12, IsSevenDayContact: true, IsHistoricalContact: true},
		{AccountID: 13, IsHistoricalContact: true},
	}})
	svc.SetUserAccessReader(accountPoolUserAccessReaderStub{access: &AccountPoolUserAccess{
		VisibleGroups: []AccountPoolGroupOption{{ID: 1, Name: "公开分组"}},
		AccountIDs:    []int64{11, 12, 13},
	}})
	svc.SetResidentStatsReader(&accountPoolResidentStatsReaderStub{})

	page, err := svc.ListForUser(context.Background(), "epoch-a", 42, 1, 1, AccountPoolListQuery{
		Relation: AccountPoolRelationHistoricalContact,
		SortBy:   AccountPoolSortByID, SortOrder: AccountPoolSortDesc,
	})
	if err != nil {
		t.Fatalf("按用户关系列出号池: %v", err)
	}
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != 13 || !page.Items[0].IsHistoricalContact {
		t.Fatalf("关系筛选必须先于分页并投影标记: %+v", page)
	}

	page, err = svc.ListForUser(context.Background(), "epoch-a", 42, 1, 20, AccountPoolListQuery{
		Relation: AccountPoolRelationSevenDayContact,
		SortBy:   AccountPoolSortByID, SortOrder: AccountPoolSortAsc,
	})
	if err != nil {
		t.Fatalf("列出带关系投影的号池: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != 12 || !page.Items[0].IsSevenDayContact || !page.Items[0].IsHistoricalContact {
		t.Fatalf("用户关系投影不完整: %+v", page.Items)
	}

	page, err = svc.ListForUser(context.Background(), "epoch-a", 42, 1, 20, AccountPoolListQuery{
		Relation: AccountPoolRelationPrimaryResidence,
		SortBy:   AccountPoolSortByID, SortOrder: AccountPoolSortAsc,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != 11 || !page.Items[0].IsPrimaryResidence {
		t.Fatalf("首选居住账号筛选不完整: page=%+v err=%v", page, err)
	}
}

func TestAccountPoolListForUserFiltersVisibleGroupsBeforePagination(t *testing.T) {
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 4, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
	}}
	svc := NewAccountPoolService(source, nil, nil, AccountPoolOptions{})
	svc.SetUserRelationReader(accountPoolUserRelationReaderStub{})
	svc.SetUserAccessReader(accountPoolUserAccessReaderStub{
		access: &AccountPoolUserAccess{
			VisibleGroups: []AccountPoolGroupOption{{ID: 10, Name: "公开分组"}, {ID: 20, Name: "专属分组"}},
			AccountIDs:    []int64{1, 3, 4},
			GroupsByAccount: map[int64][]AccountPoolGroupOption{
				1: {{ID: 10, Name: "公开分组"}},
				3: {{ID: 20, Name: "专属分组"}},
				4: {{ID: 10, Name: "公开分组"}, {ID: 20, Name: "专属分组"}},
			},
		},
		byGroup: map[int64]*AccountPoolUserAccess{
			10: {VisibleGroups: []AccountPoolGroupOption{{ID: 10, Name: "公开分组"}, {ID: 20, Name: "专属分组"}}, AccountIDs: []int64{1, 4}},
			20: {VisibleGroups: []AccountPoolGroupOption{{ID: 10, Name: "公开分组"}, {ID: 20, Name: "专属分组"}}, AccountIDs: []int64{3}},
		},
	})
	svc.SetResidentStatsReader(&accountPoolResidentStatsReaderStub{})

	page, err := svc.ListForUser(context.Background(), "epoch-a", 42, 1, 2, AccountPoolListQuery{
		SortBy: AccountPoolSortByID, SortOrder: AccountPoolSortDesc,
	})
	if err != nil {
		t.Fatalf("按用户可见分组列出号池: %v", err)
	}
	if page.Total != 3 || len(page.Items) != 2 || page.Items[0].ID != 4 || page.Items[1].ID != 3 {
		t.Fatalf("可见账号必须在分页前过滤: page=%+v", page)
	}
	if len(page.GroupOptions) != 2 || page.GroupOptions[0].ID != 10 || page.GroupOptions[1].ID != 20 {
		t.Fatalf("应返回用户可见分组选项: %+v", page.GroupOptions)
	}
	if len(page.Items[0].Groups) != 2 || page.Items[0].Groups[0].ID != 10 || page.Items[0].Groups[1].ID != 20 {
		t.Fatalf("账号行应只投影用户可见分组: %+v", page.Items[0].Groups)
	}

	groupID := int64(10)
	page, err = svc.ListForUser(context.Background(), "epoch-a", 42, 1, 20, AccountPoolListQuery{
		GroupID: &groupID, SortBy: AccountPoolSortByID, SortOrder: AccountPoolSortAsc,
	})
	if err != nil || page.Total != 2 || len(page.Items) != 2 || page.Items[0].ID != 1 || page.Items[1].ID != 4 {
		t.Fatalf("指定分组应进一步收窄账号范围: page=%+v err=%v", page, err)
	}

	unknownGroupID := int64(99)
	page, err = svc.ListForUser(context.Background(), "epoch-a", 42, 1, 20, AccountPoolListQuery{
		GroupID: &unknownGroupID, SortBy: AccountPoolSortByID, SortOrder: AccountPoolSortDesc,
	})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("不可见分组不得返回账号: page=%+v err=%v", page, err)
	}
}

func TestAccountPoolGroupVisibilityRules(t *testing.T) {
	allowed := map[int64]struct{}{2: {}}
	tests := []struct {
		name                 string
		groupID              int64
		exclusive            bool
		restrictPublicGroups bool
		wantVisible          bool
	}{
		{name: "普通分组默认可见", groupID: 1, wantVisible: true},
		{name: "专属分组未授权不可见", groupID: 3, exclusive: true},
		{name: "专属分组已授权可见", groupID: 2, exclusive: true, wantVisible: true},
		{name: "受限用户普通分组需授权", groupID: 1, restrictPublicGroups: true},
		{name: "受限用户已授权普通分组可见", groupID: 2, restrictPublicGroups: true, wantVisible: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := accountPoolGroupVisible(Group{ID: test.groupID, IsExclusive: test.exclusive}, allowed, test.restrictPublicGroups)
			if got != test.wantVisible {
				t.Fatalf("可见性错误: got=%v want=%v", got, test.wantVisible)
			}
		})
	}
}

func TestAPIKeyServiceAccountPoolUserAccessUsesVisibleGroups(t *testing.T) {
	groupRepo := accountPoolVisibilityGroupRepoStub{
		groups: []Group{
			{ID: 1, Name: "公开分组"},
			{ID: 2, Name: "专属分组", IsExclusive: true},
			{ID: 3, Name: "其他专属分组", IsExclusive: true},
		},
		accountIDs: map[int64][]int64{
			1: {101, 102},
			2: {102, 103},
			3: {104},
		},
	}
	userRepo := accountPoolVisibilityUserRepoStub{user: &User{ID: 42, AllowedGroups: []int64{2}}}
	defaultGroupID := int64(2)
	svc := NewAPIKeyService(accountPoolDefaultGroupRepoStub{groupID: &defaultGroupID}, userRepo, groupRepo, nil, nil, nil, nil)

	access, err := svc.GetAccountPoolUserAccess(context.Background(), 42, nil)
	if err != nil {
		t.Fatalf("读取用户可见号池范围: %v", err)
	}
	if len(access.VisibleGroups) != 2 || access.VisibleGroups[0].ID != 1 || access.VisibleGroups[1].ID != 2 {
		t.Fatalf("普通分组和已授权专属分组应可见: %+v", access.VisibleGroups)
	}
	if len(access.AccountIDs) != 3 || access.AccountIDs[0] != 101 || access.AccountIDs[1] != 102 || access.AccountIDs[2] != 103 {
		t.Fatalf("多分组账号应按并集去重: %v", access.AccountIDs)
	}
	if access.DefaultGroupID == nil || *access.DefaultGroupID != 2 {
		t.Fatalf("全部密钥同组时应返回默认分组: %+v", access.DefaultGroupID)
	}
	if len(access.GroupsByAccount[102]) != 2 || access.GroupsByAccount[102][0].ID != 1 || access.GroupsByAccount[102][1].ID != 2 {
		t.Fatalf("账号应按可见分组投影归属: %+v", access.GroupsByAccount)
	}
	defaultGroupID = 3
	access, err = svc.GetAccountPoolUserAccess(context.Background(), 42, nil)
	if err != nil || access.DefaultGroupID != nil {
		t.Fatalf("不可见分组不得成为默认筛选: access=%+v err=%v", access, err)
	}
	defaultGroupID = 2

	selectedGroupID := int64(2)
	access, err = svc.GetAccountPoolUserAccess(context.Background(), 42, &selectedGroupID)
	if err != nil || len(access.AccountIDs) != 2 || access.AccountIDs[0] != 102 || access.AccountIDs[1] != 103 {
		t.Fatalf("选择专属分组应只返回该分组账号: access=%+v err=%v", access, err)
	}

	selectedGroupID = 3
	access, err = svc.GetAccountPoolUserAccess(context.Background(), 42, &selectedGroupID)
	if err != nil || len(access.AccountIDs) != 0 {
		t.Fatalf("未授权专属分组不得返回账号: access=%+v err=%v", access, err)
	}

	userRepo.user.RestrictPublicGroups = true
	access, err = svc.GetAccountPoolUserAccess(context.Background(), 42, nil)
	if err != nil || len(access.VisibleGroups) != 1 || access.VisibleGroups[0].ID != 2 {
		t.Fatalf("受限用户只能看到授权分组: access=%+v err=%v", access, err)
	}
}

func TestAccountPoolPersonalUsageRejectsInaccessibleAccount(t *testing.T) {
	cache := &accountPoolPersonalUsageCacheStub{item: PublicAccountPoolAccount{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}
	svc := NewAccountPoolService(nil, cache, nil, AccountPoolOptions{})
	svc.SetUserAccessReader(accountPoolUserAccessReaderStub{access: &AccountPoolUserAccess{AccountIDs: []int64{8}}})
	svc.SetPersonalUsageReader(&accountPoolPersonalUsageReaderStub{stats: &AccountPoolPersonalUsageStats{}})

	if _, err := svc.GetPersonalUsage(context.Background(), "epoch-a", 42, 7); !errors.Is(err, ErrAccountPoolPersonalUsageNotFound) {
		t.Fatalf("不可见账号不得查询个人用量，err=%v", err)
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

func TestAccountPoolUsageWindowKeepsLastObservationAfterRetention(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	svc := NewAccountPoolService(nil, nil, nil, AccountPoolOptions{
		UsageFresh: 15 * time.Minute, UsageRetention: time.Hour,
	})
	observedAt := now.Add(-48 * time.Hour)
	window := svc.mapWindow("5h", "5h", 42.0, nil, &observedAt, now, false)
	if window.State != AccountPoolFreshnessStale || window.UsedPercent == nil || *window.UsedPercent != 42 {
		t.Fatalf("超过保留期后仍应展示最后用量并标记 stale，得到 %+v", window)
	}
}

func TestAccountPoolDatabaseFallbackPreservesStatusFilterAndOrder(t *testing.T) {
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{
		{ID: 1, Status: StatusActive, Schedulable: true},
		{ID: 2, Status: StatusError, Schedulable: true},
		{ID: 3, Status: StatusActive, Schedulable: true},
	}}
	svc := NewAccountPoolService(source, nil, nil, AccountPoolOptions{})
	page, err := svc.listAccountPoolDatabaseFallback(context.Background(), 1, 1, AccountPoolListQuery{
		Status: "active", SortBy: AccountPoolSortByStatus, SortOrder: AccountPoolSortDesc,
	})
	if err != nil || page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != 3 {
		t.Fatalf("数据库降级应保留状态筛选、总数和稳定排序: page=%+v err=%v", page, err)
	}
}

func TestAccountPoolDatabaseFallbackProjectsResidentStats(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{
		{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
		{ID: 12, Platform: PlatformGemini, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
		{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
	}}
	reader := &accountPoolResidentStatsReaderStub{stats: map[int64]AccountPoolResidentStats{
		11: {Active: 3, Total: 8},
	}}
	svc := NewAccountPoolService(source, nil, nil, AccountPoolOptions{})
	svc.now = func() time.Time { return now }
	svc.SetResidentStatsReader(reader)

	page, err := svc.listAccountPoolDatabaseFallback(context.Background(), 1, 20, AccountPoolListQuery{
		Status: "active", SortBy: AccountPoolSortByID, SortOrder: AccountPoolSortAsc,
	})
	if err != nil {
		t.Fatalf("数据库降级居民统计: %v", err)
	}
	if len(page.Items) != 3 || page.Items[0].Residents.Active != 3 || page.Items[0].Residents.Total != 8 || !page.Items[0].Residents.Applicable {
		t.Fatalf("OpenAI 居民统计投影错误: %+v", page.Items)
	}
	if page.Items[1].Residents.Applicable {
		t.Fatalf("非 OpenAI 账号不应适用居民统计: %+v", page.Items[1].Residents)
	}
	if got := page.Items[2].Residents; !got.Applicable || got.Active != 0 || got.Total != 0 {
		t.Fatalf("无居民的 OpenAI 账号应保留零值: %+v", got)
	}
	if len(reader.accountIDs) != 2 || reader.accountIDs[0] != 11 || reader.accountIDs[1] != 13 || !reader.activeSince.Equal(now.Add(-time.Hour)) {
		t.Fatalf("居民统计读取参数错误: ids=%v activeSince=%s", reader.accountIDs, reader.activeSince)
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

func TestAccountPoolReconcilePublishesResidentStats(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	cache := &accountPoolBuildCache{acquired: true, renewed: true}
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{
		{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 8, Platform: PlatformAnthropic, Type: AccountTypeOAuth},
	}}
	reader := &accountPoolResidentStatsReaderStub{stats: map[int64]AccountPoolResidentStats{
		7: {Active: 2, Total: 5},
	}}
	svc := NewAccountPoolService(source, cache, accountPoolConcurrencyReader{}, AccountPoolOptions{})
	svc.now = func() time.Time { return now }
	svc.SetResidentStatsReader(reader)

	if err := svc.Reconcile(context.Background(), "generation-residents", "epoch-a"); err != nil {
		t.Fatalf("发布居民统计快照: %v", err)
	}
	if cache.writeCalls != 1 || len(cache.written) != 2 {
		t.Fatalf("应发布完整居民统计快照: calls=%d items=%+v", cache.writeCalls, cache.written)
	}
	if got := cache.written[0].Residents; !got.Applicable || got.Active != 2 || got.Total != 5 {
		t.Fatalf("OpenAI 居民统计错误: %+v", got)
	}
	if got := cache.written[1].Residents; got.Applicable {
		t.Fatalf("非 OpenAI 账号不应适用居民统计: %+v", got)
	}
	if len(reader.accountIDs) != 1 || reader.accountIDs[0] != 7 || !reader.activeSince.Equal(now.Add(-time.Hour)) {
		t.Fatalf("快照居民统计读取参数错误: ids=%v activeSince=%s", reader.accountIDs, reader.activeSince)
	}
}

func TestAccountPoolReconcileDoesNotPublishWhenResidentStatsFail(t *testing.T) {
	cache := &accountPoolBuildCache{acquired: true, renewed: true}
	source := &accountPoolBuildSource{records: []AccountPoolSourceRecord{{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth}}}
	svc := NewAccountPoolService(source, cache, accountPoolConcurrencyReader{}, AccountPoolOptions{})
	svc.SetResidentStatsReader(&accountPoolResidentStatsReaderStub{err: errors.New("database unavailable")})

	err := svc.Reconcile(context.Background(), "generation-residents", "epoch-a")
	if err == nil || !strings.Contains(err.Error(), "resident stats") || cache.writeCalls != 0 {
		t.Fatalf("居民统计失败时不得发布快照: err=%v calls=%d", err, cache.writeCalls)
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
		FiveHour: AccountPoolUsageMetrics{
			Requests: 0, InputTokens: 1, OutputTokens: 2,
			CacheCreationTokens: 3, CacheReadTokens: 6, Tokens: 12, ActualCost: 0,
		},
		SevenDay: AccountPoolUsageMetrics{
			Requests: 4, InputTokens: 100, OutputTokens: 50,
			CacheCreationTokens: 50, CacheReadTokens: 100, Tokens: 300, ActualCost: 1.25,
		},
	}}
	svc := NewAccountPoolService(nil, cache, nil, AccountPoolOptions{})
	svc.now = func() time.Time { return now }
	svc.SetPersonalUsageReader(reader)
	svc.SetUserAccessReader(accountPoolUserAccessReaderStub{access: &AccountPoolUserAccess{AccountIDs: []int64{7}}})

	value, err := svc.GetPersonalUsage(context.Background(), "epoch-a", 42, 7)
	if err != nil {
		t.Fatalf("查询个人用量: %v", err)
	}
	if reader.calls.Load() != 1 || len(value.Windows) != 2 {
		t.Fatalf("首次查询应读取一次并返回两个窗口，calls=%d windows=%d", reader.calls.Load(), len(value.Windows))
	}
	if value.Windows[0].Requests != 0 || value.Windows[0].InputTokens != 1 || value.Windows[0].OutputTokens != 2 ||
		value.Windows[0].Tokens != 12 || value.Windows[0].CacheRate != 0.6 ||
		value.Windows[0].StartAt != fiveHourReset.Add(-5*time.Hour) {
		t.Fatalf("5h 零值或窗口起点错误: %+v", value.Windows[0])
	}
	if value.Windows[1].ActualCost != 1.25 || value.Windows[1].InputTokens != 100 ||
		value.Windows[1].OutputTokens != 50 || value.Windows[1].CacheRate != 0.4 ||
		value.Windows[1].StartAt != sevenDayReset.Add(-7*24*time.Hour) {
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
	svc.SetUserAccessReader(accountPoolUserAccessReaderStub{access: &AccountPoolUserAccess{AccountIDs: []int64{8}}})
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
	svc.SetUserAccessReader(accountPoolUserAccessReaderStub{access: &AccountPoolUserAccess{AccountIDs: []int64{9}}})

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
