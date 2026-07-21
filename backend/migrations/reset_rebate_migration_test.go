package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration175AddsResetRebateAuditTables(t *testing.T) {
	content, err := FS.ReadFile("175_reset_rebates.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS reset_rebate_batches")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS reset_rebate_account_items")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS reset_rebate_user_items")
	require.Contains(t, sql, "status IN ('running','ready','incomplete','not_eligible','expired','executed','failed')")
	require.Contains(t, sql, "WHERE status = 'running'")
	require.Contains(t, sql, "WHERE source_type = 'reset_rebate' AND source_id IS NOT NULL")
	require.Contains(t, sql, "REFERENCES reset_rebate_batches(id) ON DELETE CASCADE")
}

func TestMigration176AddsForceExecutionAuditAmount(t *testing.T) {
	content, err := FS.ReadFile("176_reset_rebate_force_execution.sql")
	require.NoError(t, err)
	require.Contains(t, string(content), "failed_account_amount DECIMAL(20,8) NOT NULL DEFAULT 0")
	require.Contains(t, string(content), "item.error_code <> ''")
}

func TestMigration178AddsResetRebateReason(t *testing.T) {
	content, err := FS.ReadFile("178_reset_rebate_reason.sql")
	require.NoError(t, err)
	require.Contains(t, string(content), "ADD COLUMN IF NOT EXISTS rebate_reason VARCHAR(100) NOT NULL DEFAULT ''")
}
