package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newOpenAIUserAffinityReentryCacheTest(t *testing.T) *gatewayCache {
	t.Helper()
	server := miniredis.RunT(t)
	return &gatewayCache{rdb: redis.NewClient(&redis.Options{Addr: server.Addr()})}
}

func TestOpenAIUserAffinityReentryQueueReleasesFollowersInFIFOOrder(t *testing.T) {
	cache := newOpenAIUserAffinityReentryCacheTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	leader := service.OpenAIUserAffinityReentryAdmission{
		AccountID: 11, UserID: 22, Generation: 3, BatchToken: "batch-1",
		LeaderVersion: 1, JitterMinMS: 0, JitterMaxMS: 0, Deadline: now.Add(time.Minute),
	}
	require.NoError(t, cache.InitializeOpenAIUserAffinityReentry(ctx, leader))
	first := leader
	first.WaiterToken = "waiter-1"
	second := leader
	second.WaiterToken = "waiter-2"
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, first))
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, second))

	empty, err := cache.ActivateOpenAIUserAffinityFollowers(ctx, leader, now)
	require.NoError(t, err)
	require.False(t, empty)
	pollSecond, err := cache.PollOpenAIUserAffinityFollower(ctx, second, now)
	require.NoError(t, err)
	require.False(t, pollSecond.Released)
	pollFirst, err := cache.PollOpenAIUserAffinityFollower(ctx, first, now)
	require.NoError(t, err)
	require.True(t, pollFirst.Released)
	empty, err = cache.AcknowledgeOpenAIUserAffinityFollower(ctx, first, now)
	require.NoError(t, err)
	require.False(t, empty)
	pollSecond, err = cache.PollOpenAIUserAffinityFollower(ctx, second, now)
	require.NoError(t, err)
	require.True(t, pollSecond.Released)
	empty, err = cache.AcknowledgeOpenAIUserAffinityFollower(ctx, second, now)
	require.NoError(t, err)
	require.True(t, empty)
}

func TestOpenAIUserAffinityReentryQueueKeepsFIFOAcrossPlacementGenerations(t *testing.T) {
	cache := newOpenAIUserAffinityReentryCacheTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	leader := service.OpenAIUserAffinityReentryAdmission{
		AccountID: 12, UserID: 23, Generation: 3, BatchToken: "batch-cross-scope",
		LeaderVersion: 1, JitterMinMS: 0, JitterMaxMS: 0, Deadline: now.Add(time.Minute),
	}
	require.NoError(t, cache.InitializeOpenAIUserAffinityReentry(ctx, leader))
	first := leader
	first.Generation = 4
	first.WaiterToken = "waiter-generation-4"
	second := leader
	second.Generation = 9
	second.WaiterToken = "waiter-generation-9"
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, first))
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, second))

	_, err := cache.ActivateOpenAIUserAffinityFollowers(ctx, leader, now)
	require.NoError(t, err)
	pollSecond, err := cache.PollOpenAIUserAffinityFollower(ctx, second, now)
	require.NoError(t, err)
	require.False(t, pollSecond.Released)
	pollFirst, err := cache.PollOpenAIUserAffinityFollower(ctx, first, now)
	require.NoError(t, err)
	require.True(t, pollFirst.Released)
}

func TestOpenAIUserAffinityReentryQueueOnlyHeadMayTakeover(t *testing.T) {
	cache := newOpenAIUserAffinityReentryCacheTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	leader := service.OpenAIUserAffinityReentryAdmission{
		AccountID: 31, UserID: 41, Generation: 5, BatchToken: "batch-2",
		LeaderVersion: 7, JitterMinMS: 0, JitterMaxMS: 0, Deadline: now.Add(time.Minute),
	}
	require.NoError(t, cache.InitializeOpenAIUserAffinityReentry(ctx, leader))
	first := leader
	first.WaiterToken = "waiter-1"
	second := leader
	second.WaiterToken = "waiter-2"
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, first))
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, second))
	require.NoError(t, cache.MarkOpenAIUserAffinityLeaderFailed(ctx, leader))

	pollSecond, err := cache.PollOpenAIUserAffinityFollower(ctx, second, now)
	require.NoError(t, err)
	require.False(t, pollSecond.MayTakeover)
	pollFirst, err := cache.PollOpenAIUserAffinityFollower(ctx, first, now)
	require.NoError(t, err)
	require.True(t, pollFirst.MayTakeover)
	require.Equal(t, int64(7), pollFirst.ExpectedLeaderVersion)
}

func TestOpenAIUserAffinityReentryQueueFollowerCanPublishMissingBatch(t *testing.T) {
	cache := newOpenAIUserAffinityReentryCacheTest(t)
	ctx := context.Background()
	admission := service.OpenAIUserAffinityReentryAdmission{
		AccountID: 71, UserID: 81, Generation: 4, BatchToken: "batch-4",
		LeaderToken: "leader-4", LeaderVersion: 2, LeaderLeaseUntil: time.Now().Add(30 * time.Second),
		WaiterToken: "waiter-1", Deadline: time.Now().Add(time.Minute),
	}

	err := cache.EnqueueOpenAIUserAffinityFollower(ctx, admission)
	require.ErrorIs(t, err, service.ErrOpenAIUserAffinityReentryBatchNotReady)
	require.NoError(t, cache.InitializeOpenAIUserAffinityReentry(ctx, admission))
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, admission))

	other := admission
	other.BatchToken = "newer-batch"
	require.ErrorIs(t, cache.InitializeOpenAIUserAffinityReentry(ctx, other), service.ErrOpenAIUserAffinityReentryBatchNotReady)
}

func TestOpenAIUserAffinityReentryQueueFollowerRestoresReleasePhase(t *testing.T) {
	cache := newOpenAIUserAffinityReentryCacheTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	admission := service.OpenAIUserAffinityReentryAdmission{
		AccountID: 72, UserID: 82, Generation: 4, BatchToken: "batch-5",
		LeaderToken: "leader-5", LeaderVersion: 2, ReentryState: "stagger_releasing",
		WaiterToken: "waiter-1", Deadline: now.Add(time.Minute), JitterMinMS: 0, JitterMaxMS: 0,
	}

	require.NoError(t, cache.InitializeOpenAIUserAffinityReentry(ctx, admission))
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, admission))
	poll, err := cache.PollOpenAIUserAffinityFollower(ctx, admission, time.Now().Add(time.Millisecond))
	require.NoError(t, err)
	require.True(t, poll.Released)
	require.False(t, poll.MayTakeover)
}

func TestOpenAIUserAffinityReentryQueueAppliesFollowerLimitAndJitter(t *testing.T) {
	cache := newOpenAIUserAffinityReentryCacheTest(t)
	ctx := context.Background()
	now := time.Now().UTC()
	leader := service.OpenAIUserAffinityReentryAdmission{
		AccountID: 51, UserID: 61, Generation: 2, BatchToken: "batch-3",
		LeaderVersion: 1, JitterMinMS: 20, JitterMaxMS: 20,
		MaxFollowers: 1, Deadline: now.Add(time.Minute),
	}
	require.NoError(t, cache.InitializeOpenAIUserAffinityReentry(ctx, leader))
	first := leader
	first.WaiterToken = "waiter-1"
	second := leader
	second.WaiterToken = "waiter-2"
	require.NoError(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, first))
	require.Error(t, cache.EnqueueOpenAIUserAffinityFollower(ctx, second))

	_, err := cache.ActivateOpenAIUserAffinityFollowers(ctx, leader, now)
	require.NoError(t, err)
	poll, err := cache.PollOpenAIUserAffinityFollower(ctx, first, now.Add(19*time.Millisecond))
	require.NoError(t, err)
	require.False(t, poll.Released)
	poll, err = cache.PollOpenAIUserAffinityFollower(ctx, first, now.Add(20*time.Millisecond))
	require.NoError(t, err)
	require.True(t, poll.Released)
}

func TestNormalizedOpenAIUserAffinityDemand(t *testing.T) {
	require.Equal(t, 0.05, normalizedOpenAIUserAffinityDemand(0, 0))
	require.Equal(t, 0.05, normalizedOpenAIUserAffinityDemand(100, 100))
	require.Equal(t, 0.10, normalizedOpenAIUserAffinityDemand(200, 100))
	require.Equal(t, 0.50, normalizedOpenAIUserAffinityDemand(5000, 100))
}
