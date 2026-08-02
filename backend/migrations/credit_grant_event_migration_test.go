package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration192AddsCreditGrantEventsAndTriggerProtection(t *testing.T) {
	content, err := FS.ReadFile("192_credit_grant_events.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS credit_grant_events")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS user_credit_grant_event_triggers")
	require.Contains(t, sql, "UNIQUE(event_id, user_id)")
	require.Contains(t, sql, "credit_type_snapshot")
	require.Contains(t, sql, "limited_credit_grant_id")
	require.Contains(t, sql, "source_type = 'credit_grant_event'")
}
