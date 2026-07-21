package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration177AddsRecurringCreditTablesAndConstraints(t *testing.T) {
	content, err := FS.ReadFile("177_recurring_credit_grants.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS recurring_credit_tasks")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS recurring_credit_batches")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS recurring_credit_user_items")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS recurring_credit_task_audits")
	require.Contains(t, sql, "day_of_month BETWEEN 1 AND 28")
	require.Contains(t, sql, "amount BETWEEN 0.01 AND 10000")
	require.Contains(t, sql, "remaining_runs > 0 OR status IN ('completed','deleted')")
	require.Contains(t, sql, "UNIQUE(task_id, scheduled_at)")
	require.Contains(t, sql, "source_type = 'recurring_grant'")
}

func TestMigration179AddsImmediateGrantConfiguration(t *testing.T) {
	content, err := FS.ReadFile("179_recurring_credit_immediate.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS validity_days")
	require.Contains(t, sql, "schedule_type = 'immediate'")
	require.Contains(t, sql, "validity_days BETWEEN 1 AND 36500")
	require.Contains(t, sql, "remaining_runs BETWEEN 0 AND 1")
}

func TestMigration180AddsRollingActivitySnapshots(t *testing.T) {
	content, err := FS.ReadFile("180_recurring_credit_rolling_activity.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "eligibility_policy")
	require.Contains(t, sql, "rolling_30d_activity_v1")
	require.Contains(t, sql, "api_active_count")
	require.Contains(t, sql, "site_active_count")
	require.Contains(t, sql, "both_active_count")
	require.Contains(t, sql, "snapshot_completed_at")
	require.Contains(t, sql, "api_last_used_at")
	require.Contains(t, sql, "site_last_active_at")
}

func TestMigration181AddsRollingActivityIndexes(t *testing.T) {
	content, err := FS.ReadFile("181_recurring_credit_activity_indexes_notx.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_api_keys_last_used_user")
	require.Contains(t, sql, "INCLUDE (user_id)")
	require.NotContains(t, sql, "idx_api_keys_last_used_user\n    ON api_keys (last_used_at)\n    INCLUDE (user_id)\n    WHERE deleted_at IS NULL")
	require.Contains(t, sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_active_last_active")
}
