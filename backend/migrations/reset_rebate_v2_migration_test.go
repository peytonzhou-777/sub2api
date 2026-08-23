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

func TestMigration228AddsUserResetRebateSkipCount(t *testing.T) {
	content, err := FS.ReadFile("228_reset_rebate_user_skip_count.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "reset_rebate_skip_count BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "users_reset_rebate_skip_count_nonnegative")
	require.Contains(t, sql, "skip_count_consumed BOOLEAN NOT NULL DEFAULT FALSE")
}

func TestMigration239AddsResetRebateV3AverageBenefit(t *testing.T) {
	content, err := FS.ReadFile("239_reset_rebates_v3_average_benefit.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "average_benefit_enabled boolean NOT NULL DEFAULT false")
	require.Contains(t, sql, "average_benefit_duration_us bigint NOT NULL DEFAULT 0")
	require.Contains(t, sql, "average_benefit_ratio decimal(11,8) NOT NULL DEFAULT 0")
	require.Contains(t, sql, "combined_payout_ratio decimal(11,8) NOT NULL DEFAULT 0")
	require.Contains(t, sql, "excluded_account_count integer NOT NULL DEFAULT 0")
	require.Contains(t, sql, "included_in_statistics boolean NOT NULL DEFAULT true")
	require.Contains(t, sql, "statistics_exclusion_reason text NOT NULL DEFAULT ''")
	require.Contains(t, sql, "ALTER COLUMN mechanism_version SET DEFAULT 3")
}
