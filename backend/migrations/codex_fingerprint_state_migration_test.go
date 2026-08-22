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

func TestMigration232AddsScopedSessionRotation(t *testing.T) {
	content, err := FS.ReadFile("232_openai_codex_fingerprint_session_scopes.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS codex_fingerprint_session_scopes")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS codex_fingerprint_cluster_secrets")
	require.Contains(t, sql, "PRIMARY KEY (account_id, scope_hash)")
	require.Contains(t, sql, "rotation_count BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "secret_hash CHAR(64) NOT NULL")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS session_scope_hash CHAR(64)")
	require.Contains(t, sql, "CHECK (session_epoch > 0)")
}

func TestMigration233AddsUUIDv7EpochTimestamps(t *testing.T) {
	content, err := FS.ReadFile("233_openai_codex_fingerprint_uuidv7.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CHECK (codex_fingerprint_version IN ('', 'v2', 'v3'))")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS session_epoch_started_at TIMESTAMPTZ")
	require.Contains(t, sql, "MIN(created_at) AS epoch_started_at")
	require.Contains(t, sql, "session_scope_hash IS NOT DISTINCT FROM e.session_scope_hash")
}

func TestMigration237RemovesLegacyCodexFingerprintVersions(t *testing.T) {
	content, err := FS.ReadFile("237_openai_codex_fingerprint_v3_only.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "SET codex_fingerprint_version = 'v3' WHERE codex_fingerprint_version = 'v2'")
	require.Contains(t, sql, "CHECK (codex_fingerprint_version IN ('', 'v3'))")
	require.Contains(t, sql, "DELETE FROM codex_fingerprint_thread_epochs WHERE session_scope_hash IS NULL")
	require.Contains(t, sql, "WHERE NOT EXISTS ( SELECT 1 FROM codex_fingerprint_session_scopes AS s")
	require.Contains(t, sql, "ALTER COLUMN session_epoch_started_at SET NOT NULL")
	require.Contains(t, sql, "ALTER COLUMN session_scope_hash SET NOT NULL")
	require.Contains(t, sql, "FOREIGN KEY (account_id, session_scope_hash) REFERENCES codex_fingerprint_session_scopes (account_id, scope_hash) ON DELETE CASCADE")
}
