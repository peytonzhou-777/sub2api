package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration233AddsSafeOpenAIRequestObservability(t *testing.T) {
	content, err := FS.ReadFile("233_ops_openai_request_observability.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS request_started_at TIMESTAMPTZ")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS upstream_rate_limit_headers JSONB")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS account_concurrency INT")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS explicit_session_id_hash VARCHAR(64)")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS session_scope_hash VARCHAR(128)")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS prompt_cache_key_hash VARCHAR(64)")
	require.NotContains(t, sql, "explicit_session_id VARCHAR")
	require.NotContains(t, sql, "prompt_cache_key VARCHAR")
	require.NotContains(t, sql, "session_scope VARCHAR")
}
