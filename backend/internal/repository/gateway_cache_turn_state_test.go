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

func TestGatewayCacheOpenAITurnStateScopeFirstWriteWins(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	writer, ok := NewGatewayCache(client).(service.OpenAITurnStateScopeCache)
	require.True(t, ok)
	reader, ok := NewGatewayCache(client).(service.OpenAITurnStateScopeCache)
	require.True(t, ok)

	ctx := context.Background()
	key := "opaque-hash"
	first := []byte(`{"version":2,"account_id":11}`)
	second := []byte(`{"version":2,"account_id":12}`)
	require.NoError(t, writer.SetOpenAITurnStateScope(ctx, 7, key, first, time.Minute))
	require.NoError(t, writer.SetOpenAITurnStateScope(ctx, 7, key, first, 2*time.Minute))
	require.ErrorIs(t, writer.SetOpenAITurnStateScope(ctx, 7, key, second, time.Minute), service.ErrOpenAITurnStateScopeConflict)

	actual, err := reader.GetOpenAITurnStateScope(ctx, 7, key)
	require.NoError(t, err)
	require.Equal(t, first, actual)
	_, err = reader.GetOpenAITurnStateScope(ctx, 8, key)
	require.ErrorIs(t, err, service.ErrOpenAITurnStateScopeNotFound)

	redisServer.FastForward(3 * time.Minute)
	_, err = reader.GetOpenAITurnStateScope(ctx, 7, key)
	require.ErrorIs(t, err, service.ErrOpenAITurnStateScopeNotFound)
}
