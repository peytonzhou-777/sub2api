package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration257ResetsConversationBindingEpochAndDecouplesLeases(t *testing.T) {
	content, err := FS.ReadFile("257_openai_conversation_binding_epoch_reset.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS binding_epoch BIGINT")
	require.Contains(t, sql, "ALTER COLUMN binding_epoch SET DEFAULT 2")
	require.Contains(t, sql, "ON DELETE SET NULL")
	require.Contains(t, sql, "WHERE binding_epoch < 2")
	require.Contains(t, sql, "DELETE FROM openai_user_group_session_request_holds")
	require.Contains(t, sql, "DELETE FROM openai_persona_request_holds")
	require.Contains(t, sql, "SET state = 'expired'")
	require.Contains(t, sql, "DELETE FROM openai_account_user_persona_claims")
}
