package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration247AddsOpenAISessionPersonaSlotsAndCredentialChains(t *testing.T) {
	content, err := FS.ReadFile("247_openai_session_persona_slots.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS openai_account_persona_slots")
	require.Contains(t, sql, "PRIMARY KEY (account_id, slot_id)")
	require.Contains(t, sql, "credential_chain_id VARCHAR(128),")
	require.Contains(t, sql, "slot_generation BIGINT NOT NULL DEFAULT 1")
	require.Contains(t, sql, "slot_set_generation BIGINT NOT NULL DEFAULT 1")
	require.Contains(t, sql, "state IN ('active', 'draining', 'disabled')")
	require.Contains(t, sql, "CHECK ( credential_chain_id IS NULL OR btrim(credential_chain_id) <> '' )")
	require.Contains(t, sql, "UNIQUE (account_id, slot_id, persona)")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS openai_account_persona_credentials")
	require.Contains(t, sql, "PRIMARY KEY (account_id, persona, credential_chain_id)")
	require.Contains(t, sql, "FOREIGN KEY (account_id, slot_id, persona)")
	require.Contains(t, sql, "REFERENCES openai_account_persona_slots (account_id, slot_id, persona)")
}
