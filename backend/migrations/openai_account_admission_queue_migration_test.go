package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration234AddsAccountAdmissionObservability(t *testing.T) {
	content, err := FS.ReadFile("234_openai_account_admission_queue.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "account_queue_wait_ms")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS usage_logs_request_type_check")
	require.Contains(t, sql, "request_type <= 6) NOT VALID")
}
