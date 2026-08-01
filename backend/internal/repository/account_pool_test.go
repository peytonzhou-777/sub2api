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
	if _, _, err := cache.ReadAccountPoolPage(ctx, "epoch-a", 1, 20, nil); err != nil {
		t.Fatalf("同一启用 epoch 应可读取: %v", err)
	}
	if _, _, err := cache.ReadAccountPoolPage(ctx, "epoch-b", 1, 20, nil); !errors.Is(err, service.ErrAccountPoolSnapshotNotReady) {
		t.Fatalf("旧 generation 应被拒绝，实际错误: %v", err)
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
