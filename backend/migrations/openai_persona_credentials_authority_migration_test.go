package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration252MakesPersonaCredentialTableAuthoritative(t *testing.T) {
	content, err := FS.ReadFile("252_openai_persona_credentials_authority.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "SET credentials = '{}'::jsonb")
	require.Contains(t, sql, "state = 'revoked'")
	require.Contains(t, sql, "credential_chain_id = NULL")
	require.Contains(t, sql, "- 'persona_credentials' - 'oauth_credential_chains'")
	require.Contains(t, sql, "openai_persona_credentials_encrypted_payload_check")
	require.Contains(t, sql, "COALESCE(credentials->>'format_version', '') = '1'")
	require.Contains(t, sql, "COALESCE(btrim(credentials->>'ciphertext'), '') <> ''")
}
