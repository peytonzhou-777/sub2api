package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration196AddsAccountResetRebateV2(t *testing.T) {
	content, err := FS.ReadFile("196_reset_rebates_v2.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "account_usage_window_histories")
	require.Contains(t, sql, "mechanism_version")
	require.Contains(t, sql, "LEGACY_MECHANISM_DISABLED")
	require.Contains(t, sql, "reset_rebate_user_account_items")
	require.Contains(t, sql, "reset_rebate_user_attempts")
	require.Contains(t, sql, "idx_user_limited_credit_grants_reset_rebate_user")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS reset_rebate_status_check")
	require.Contains(t, sql, "'partial'")
	require.Contains(t, sql, "batch.mechanism_version = 1")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS rebate_reason")
	require.NotContains(t, sql, "available_count")
	require.NotContains(t, sql, "suggested_ratio")
}
