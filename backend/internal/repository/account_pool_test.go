package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestAccountPoolSnapshotRejectsPreviousEnabledEpoch(t *testing.T) {
	server := miniredis.RunT(t)
	cache := &accountPoolSnapshotCache{rdb: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	ctx := context.Background()
	items := []service.PublicAccountPoolAccount{{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}}

	locked, err := cache.AcquireAccountPoolBuildLock(ctx, "generation-a", time.Minute)
	if err != nil || !locked {
		t.Fatalf("获取构建锁: locked=%v err=%v", locked, err)
	}
	if err := cache.WriteAccountPoolGeneration(ctx, "generation-a", "epoch-a", items, time.Minute); err != nil {
		t.Fatalf("写入 generation: %v", err)
	}
	query := service.AccountPoolListQuery{SortBy: service.AccountPoolSortByID, SortOrder: service.AccountPoolSortDesc}
	if _, _, err := cache.ReadAccountPoolPage(ctx, "epoch-a", 1, 20, query); err != nil {
		t.Fatalf("同一启用 epoch 应可读取: %v", err)
	}
	if _, _, err := cache.ReadAccountPoolPage(ctx, "epoch-b", 1, 20, query); !errors.Is(err, service.ErrAccountPoolSnapshotNotReady) {
		t.Fatalf("旧 generation 应被拒绝，实际错误: %v", err)
	}
}

func TestAccountPoolSnapshotSortsAndFiltersByPublicStatus(t *testing.T) {
	server := miniredis.RunT(t)
	cache := &accountPoolSnapshotCache{rdb: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	ctx := context.Background()
	items := []service.PublicAccountPoolAccount{
		{ID: 1, Status: service.PublicAccountPoolStatus{Code: "active"}},
		{ID: 2, Status: service.PublicAccountPoolStatus{Code: "error"}},
		{ID: 3, Status: service.PublicAccountPoolStatus{Code: "active"}},
		{ID: 4, Status: service.PublicAccountPoolStatus{Code: "paused"}},
	}
	locked, err := cache.AcquireAccountPoolBuildLock(ctx, "generation-sort", time.Minute)
	if err != nil || !locked {
		t.Fatalf("获取构建锁: locked=%v err=%v", locked, err)
	}
	if err := cache.WriteAccountPoolGeneration(ctx, "generation-sort", "epoch-sort", items, time.Minute); err != nil {
		t.Fatalf("写入 generation: %v", err)
	}

	got, total, err := cache.ReadAccountPoolPage(ctx, "epoch-sort", 1, 20, service.AccountPoolListQuery{
		SortBy: service.AccountPoolSortByID, SortOrder: service.AccountPoolSortDesc,
	})
	if err != nil || total != 4 || len(got) != 4 || got[0].ID != 4 || got[3].ID != 1 {
		t.Fatalf("默认 ID 倒序不正确: total=%d items=%+v err=%v", total, got, err)
	}

	got, total, err = cache.ReadAccountPoolPage(ctx, "epoch-sort", 1, 20, service.AccountPoolListQuery{
		Status: "active", SortBy: service.AccountPoolSortByStatus, SortOrder: service.AccountPoolSortAsc,
	})
	if err != nil || total != 2 || len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("状态筛选及稳定排序不正确: total=%d items=%+v err=%v", total, got, err)
	}
	got, total, err = cache.ReadAccountPoolPage(ctx, "epoch-sort", 1, 1, service.AccountPoolListQuery{
		Status: "active", SortBy: service.AccountPoolSortByID, SortOrder: service.AccountPoolSortDesc,
	})
	if err != nil || total != 2 || len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("状态筛选分页总数不正确: total=%d items=%+v err=%v", total, got, err)
	}
	accountID := int64(2)
	got, total, err = cache.ReadAccountPoolPage(ctx, "epoch-sort", 1, 20, service.AccountPoolListQuery{
		AccountID: &accountID, Status: "active", SortBy: service.AccountPoolSortByID, SortOrder: service.AccountPoolSortDesc,
	})
	if err != nil || total != 0 || len(got) != 0 {
		t.Fatalf("账号精确搜索不应绕过状态筛选: total=%d items=%+v err=%v", total, got, err)
	}

	got, total, err = cache.ReadAccountPoolPage(ctx, "epoch-sort", 1, 20, service.AccountPoolListQuery{
		SortBy: service.AccountPoolSortByStatus, SortOrder: service.AccountPoolSortDesc,
	})
	if err != nil || total != 4 || len(got) != 4 || got[0].Status.Code != "paused" || got[1].Status.Code != "error" || got[2].ID != 3 {
		t.Fatalf("状态倒序不正确: total=%d items=%+v err=%v", total, got, err)
	}

	got, total, err = cache.ReadAccountPoolPage(ctx, "epoch-sort", 1, 1, service.AccountPoolListQuery{
		Relation: service.AccountPoolRelationSevenDayContact, RelationAccountIDs: []int64{1, 3},
		SortBy: service.AccountPoolSortByID, SortOrder: service.AccountPoolSortDesc,
	})
	if err != nil || total != 2 || len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("用户关系筛选必须在快照分页前完成: total=%d items=%+v err=%v", total, got, err)
	}
}

func TestAccountPoolBuildLockRenewalRequiresOwner(t *testing.T) {
	server := miniredis.RunT(t)
	cache := &accountPoolSnapshotCache{rdb: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	ctx := context.Background()
	locked, err := cache.AcquireAccountPoolBuildLock(ctx, "owner-a", time.Minute)
	if err != nil || !locked {
		t.Fatalf("获取构建锁: locked=%v err=%v", locked, err)
	}
	if renewed, err := cache.RenewAccountPoolBuildLock(ctx, "owner-a", 2*time.Minute); err != nil || !renewed {
		t.Fatalf("锁 owner 应可续租: renewed=%v err=%v", renewed, err)
	}
	if renewed, err := cache.RenewAccountPoolBuildLock(ctx, "owner-b", 2*time.Minute); err != nil || renewed {
		t.Fatalf("非 owner 不得续租: renewed=%v err=%v", renewed, err)
	}
}

func TestAccountPoolGenerationPublishRequiresBuildLockOwner(t *testing.T) {
	server := miniredis.RunT(t)
	cache := &accountPoolSnapshotCache{rdb: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	err := cache.WriteAccountPoolGeneration(context.Background(), "generation-a", "epoch-a", nil, time.Minute)
	if !errors.Is(err, service.ErrAccountPoolBuildLockLost) {
		t.Fatalf("未持锁的 generation 不得发布，实际: %v", err)
	}
}

func TestAccountPoolPersonalUsageCacheRoundTripIsPrivate(t *testing.T) {
	server := miniredis.RunT(t)
	cache := &accountPoolSnapshotCache{rdb: redis.NewClient(&redis.Options{Addr: server.Addr()})}
	ctx := context.Background()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	value := &service.AccountPoolPersonalUsage{
		AccountID:  7,
		ObservedAt: now,
		Windows: []service.AccountPoolPersonalUsageWindow{{
			Code: "5h", Label: "5h", StartAt: now.Add(-5 * time.Hour), EndAt: now,
			Requests: 0, Tokens: 12, ActualCost: 0,
		}},
	}
	if err := cache.SetAccountPoolPersonalUsage(ctx, "epoch:user:account", value, time.Minute); err != nil {
		t.Fatalf("写入个人用量缓存: %v", err)
	}
	got, hit, err := cache.GetAccountPoolPersonalUsage(ctx, "epoch:user:account")
	if err != nil || !hit || got == nil {
		t.Fatalf("读取个人用量缓存: hit=%v err=%v value=%+v", hit, err, got)
	}
	if got.AccountID != value.AccountID || got.Windows[0].Requests != 0 || got.Windows[0].ActualCost != 0 {
		t.Fatalf("缓存值损坏: %+v", got)
	}
	if _, hit, err := cache.GetAccountPoolPersonalUsage(ctx, "other-key"); err != nil || hit {
		t.Fatalf("不同私有 key 不应命中: hit=%v err=%v", hit, err)
	}
}
