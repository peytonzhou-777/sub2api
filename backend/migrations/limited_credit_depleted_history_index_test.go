package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLimitedCreditDepletedHistoryIndexMigration(t *testing.T) {
	content, err := FS.ReadFile("244_limited_credit_depleted_history_index_notx.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_limited_credit_grants_depleted_history")
	require.Contains(t, sql, "ON user_limited_credit_grants (user_id, updated_at DESC, id DESC)")
	require.Contains(t, sql, "WHERE status = 'depleted'")
}
