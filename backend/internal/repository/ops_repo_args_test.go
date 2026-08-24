//go:build unit

package repository

import (
	"database/sql"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpsInsertErrorLogArgsPreservesExplicitZeroUpstreamStatus(t *testing.T) {
	zero := 0
	args := opsInsertErrorLogArgs(&service.OpsInsertErrorLogInput{UpstreamStatusCode: &zero})

	require.Len(t, args, 60)
	encoded, ok := args[27].(sql.NullInt64)
	require.True(t, ok)
	require.True(t, encoded.Valid)
	require.Zero(t, encoded.Int64)
}

func TestOpsNullableIntPointerDistinguishesNilZeroAndStatus(t *testing.T) {
	missing := opsNullableIntPointer(nil).(sql.NullInt64)
	require.False(t, missing.Valid)

	zeroValue := 0
	zero := opsNullableIntPointer(&zeroValue).(sql.NullInt64)
	require.True(t, zero.Valid)
	require.Zero(t, zero.Int64)

	statusValue := 503
	status := opsNullableIntPointer(&statusValue).(sql.NullInt64)
	require.True(t, status.Valid)
	require.EqualValues(t, 503, status.Int64)
}

func TestOpsInsertErrorLogArgsKeepsCodexSubagentObservabilityOrder(t *testing.T) {
	args := opsInsertErrorLogArgs(&service.OpsInsertErrorLogInput{
		IsSubagent:        true,
		SubagentKind:      "thread_spawn",
		InboundTransport:  "ws",
		UpstreamTransport: "responses_websockets_v2",
	})

	require.Len(t, args, 60)
	require.Equal(t, true, args[54])
	require.Equal(t, "thread_spawn", args[55].(sql.NullString).String)
	require.Equal(t, "ws", args[56].(sql.NullString).String)
	require.Equal(t, "responses_websockets_v2", args[57].(sql.NullString).String)
}
