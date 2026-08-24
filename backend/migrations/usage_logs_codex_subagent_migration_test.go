package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUsageLogsCodexSubagentMigration(t *testing.T) {
	content, err := FS.ReadFile("242_usage_logs_codex_subagent.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS is_subagent BOOLEAN NOT NULL DEFAULT FALSE")
	require.NotContains(t, sql, "subagent_kind")
	require.NotContains(t, sql, "thread_id")
	require.NotContains(t, sql, "turn_id")
}
