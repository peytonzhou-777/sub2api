package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration260AddsPersonaActiveUserLeases(t *testing.T) {
	content, err := FS.ReadFile("260_openai_persona_active_user_leases.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS max_active_users_override INT")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS openai_persona_active_user_leases")
	require.Contains(t, sql, "UNIQUE (account_persona_id, user_id)")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS openai_persona_user_request_holds")
	require.Contains(t, sql, "GROUP BY lease.account_persona_id, lease.user_id")
	require.Contains(t, sql, "OR EXCLUDED.state = 'active' THEN 'active'")
	require.NotContains(t, sql, "DROP TABLE")
	require.NotContains(t, sql, "DROP COLUMN")
}
