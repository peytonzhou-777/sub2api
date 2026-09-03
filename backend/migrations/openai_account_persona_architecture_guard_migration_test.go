package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration256GuardsAccountPersonaArchitectureCutover(t *testing.T) {
	content, err := FS.ReadFile("256_openai_account_persona_architecture_guard.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "architecture_version = 'account_persona_v1'")
	require.Contains(t, sql, "state = 'ready'")
	require.Contains(t, sql, "FOR EACH STATEMENT EXECUTE FUNCTION reject_legacy_openai_persona_slot_write()")
	require.Contains(t, sql, "RETURN NULL")
	require.Contains(t, sql, "BEFORE INSERT OR UPDATE OF credentials ON accounts")
	require.Contains(t, sql, "TG_OP = 'INSERT'")
	require.Contains(t, sql, "TG_OP = 'UPDATE'")
	for _, key := range []string{"access_token", "refresh_token", "id_token", "expires_at", "client_id"} {
		require.Contains(t, sql, "NEW.credentials->>'"+key+"'")
		require.Contains(t, sql, "OLD.credentials->>'"+key+"'")
	}
}
