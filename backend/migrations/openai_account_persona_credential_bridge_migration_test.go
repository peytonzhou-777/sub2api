package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration254DetachesPersonaCredentialFromFixedSlots(t *testing.T) {
	content, err := FS.ReadFile("254_openai_account_persona_credential_bridge.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "c.confrelid = 'openai_account_persona_slots'::regclass")
	require.Contains(t, sql, "ALTER COLUMN slot_id DROP NOT NULL")
	require.Contains(t, sql, "SET profile_id = persona")
	require.NotContains(t, sql, "DROP TABLE")
	require.NotContains(t, sql, "DROP COLUMN")
}
