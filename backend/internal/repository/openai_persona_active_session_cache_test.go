package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIPersonaActiveSessionCacheEnforcesPersonaScopedLimit(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache, ok := NewGatewayCache(client).(service.OpenAIPersonaActiveSessionCache)
	require.True(t, ok)
	ctx := context.Background()
	sessionA := strings.Repeat("a", 64)
	sessionB := strings.Repeat("b", 64)

	state, err := cache.ReserveOpenAIPersonaActiveSession(ctx, 17, "codex_cli_strict", sessionA, "request-a", 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaActiveSessionPendingCreated, state)

	state, err = cache.ReserveOpenAIPersonaActiveSession(ctx, 17, "codex_cli_strict", sessionA, "request-a2", 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaActiveSessionPendingJoined, state)

	state, err = cache.ReserveOpenAIPersonaActiveSession(ctx, 17, "codex_cli_strict", sessionB, "request-b", 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaActiveSessionRejected, state)

	require.NoError(t, cache.ReleaseOpenAIPersonaActiveSessionReservation(ctx, 17, "codex_cli_strict", sessionA, "request-a"))
	state, err = cache.ReserveOpenAIPersonaActiveSession(ctx, 17, "codex_cli_strict", sessionB, "request-b2", 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaActiveSessionRejected, state)

	committed, err := cache.CommitOpenAIPersonaActiveSession(ctx, 17, "codex_cli_strict", sessionA, time.Minute)
	require.NoError(t, err)
	require.True(t, committed)
	expiresKey, _, _ := openAIPersonaActiveSessionKeys(17, "codex_cli_strict", sessionA)
	expiresAt, err := client.ZScore(ctx, expiresKey, sessionA).Result()
	require.NoError(t, err)
	require.Greater(t, expiresAt, float64(time.Now().Add(50*time.Second).UnixMilli()), "accepted activity must refresh the Session occupancy window")
	require.NoError(t, cache.ReleaseOpenAIPersonaActiveSessionReservation(ctx, 17, "codex_cli_strict", sessionA, "request-a2"))
	state, err = cache.ReserveOpenAIPersonaActiveSession(ctx, 17, "codex_cli_strict", sessionA, "request-a3", 1, 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaActiveSessionAlreadyActive, state)
	refreshedAt, err := client.ZScore(ctx, expiresKey, sessionA).Result()
	require.NoError(t, err)
	require.Greater(t, refreshedAt, expiresAt, "a new request from the active Session must refresh its occupancy window")

	// Persona 之间独立计数；strict 已满不会阻止同账号 OpenCode 接入。
	state, err = cache.ReserveOpenAIPersonaActiveSession(ctx, 17, "opencode", sessionB, "request-open", 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaActiveSessionPendingCreated, state)
}

func TestOpenAIPersonaActiveSessionCacheReleasesFailedPendingReservation(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache := NewGatewayCache(client).(service.OpenAIPersonaActiveSessionCache)
	ctx := context.Background()
	sessionA := strings.Repeat("c", 64)
	sessionB := strings.Repeat("d", 64)

	state, err := cache.ReserveOpenAIPersonaActiveSession(ctx, 23, "opencode", sessionA, "failed-request", 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaActiveSessionPendingCreated, state)
	require.NoError(t, cache.ReleaseOpenAIPersonaActiveSessionReservation(ctx, 23, "opencode", sessionA, "failed-request"))

	state, err = cache.ReserveOpenAIPersonaActiveSession(ctx, 23, "opencode", sessionB, "next-request", 1, time.Minute)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaActiveSessionPendingCreated, state)
}
