package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration258AllowsDrainingPersonaCredentialChains(t *testing.T) {
	content, err := FS.ReadFile("258_openai_persona_credential_draining_state.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS openai_persona_credentials_state_check")
	require.Contains(t, sql, "CHECK (state IN ('pending', 'ready', 'refreshing', 'draining', 'invalid', 'revoked'))")
}
