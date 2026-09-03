package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration253AddsDynamicOpenAIAccountPersonaFoundation(t *testing.T) {
	content, err := FS.ReadFile("253_openai_account_persona_foundation.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS openai_account_personas",
		"position = 0 AND profile_id = 'codex_cli_strict' AND credential_owner = 'account_primary'",
		"CREATE TABLE IF NOT EXISTS openai_account_persona_sessions",
		"WHERE state = 'current'",
		"CREATE TABLE IF NOT EXISTS openai_user_group_client_session_scopes",
		"CREATE TABLE IF NOT EXISTS openai_user_group_client_session_leases",
		"CREATE TABLE IF NOT EXISTS openai_user_group_session_request_holds",
		"CREATE TABLE IF NOT EXISTS openai_persona_client_session_leases",
		"CREATE TABLE IF NOT EXISTS openai_account_user_persona_claims",
		"CREATE TABLE IF NOT EXISTS openai_persona_request_holds",
		"ADD COLUMN IF NOT EXISTS account_persona_id BIGINT",
		"ADD COLUMN IF NOT EXISTS root_client_session_hash CHAR(64)",
	} {
		require.Contains(t, sql, required)
	}
	require.NotContains(t, sql, "DROP TABLE")
	require.NotContains(t, sql, "DROP COLUMN")
}
