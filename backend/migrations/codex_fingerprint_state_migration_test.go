package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration229AddsProtectedCodexFingerprintState(t *testing.T) {
	content, err := FS.ReadFile("229_openai_codex_fingerprint_state.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS codex_fingerprint_seed VARCHAR(64)")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS codex_fingerprint_version VARCHAR(16) NOT NULL DEFAULT ''")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS codex_fingerprint_epoch BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS codex_fingerprint_epoch_started_at TIMESTAMPTZ")
	require.Contains(t, sql, "codex_fingerprint_seed ~ '^[0-9a-f]{64}$'")
	require.Contains(t, sql, "CHECK (codex_fingerprint_version IN ('', 'v2'))")
	require.Contains(t, sql, "CHECK (codex_fingerprint_epoch >= 0)")
}

func TestMigration230AddsThreadEpochBindings(t *testing.T) {
	content, err := FS.ReadFile("230_openai_codex_fingerprint_thread_epochs.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS codex_fingerprint_thread_epochs")
	require.Contains(t, sql, "PRIMARY KEY (account_id, source_hash)")
	require.Contains(t, sql, "CHECK (source_hash ~ '^[0-9a-f]{64}$')")
	require.Contains(t, sql, "CHECK (session_epoch > 0)")
}
