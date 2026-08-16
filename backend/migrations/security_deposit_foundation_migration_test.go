package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration224AddsSecurityDepositFoundation(t *testing.T) {
	content, err := FS.ReadFile("224_security_deposit_foundation.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	for _, table := range []string{
		"security_deposit_accounts",
		"security_deposit_risk_profiles",
		"security_deposit_risk_events",
		"security_deposit_lots",
		"security_deposit_ledger",
		"security_deposit_violations",
		"security_deposit_refunds",
		"security_deposit_agreements",
	} {
		require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS "+table)
	}

	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS security_deposit_base_required_cents BIGINT NOT NULL DEFAULT 0")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS security_lock_violation_id BIGINT")
	require.Contains(t, sql, "CONSTRAINT security_deposit_accounts_user_bucket_unique UNIQUE (user_id, bucket_type)")
	require.Contains(t, sql, "bucket_type <> 'admin_grant' OR refund_reserved_cents = 0")
	require.Contains(t, sql, "source_type = 'payment' AND bucket_type = 'paid'")
	require.Contains(t, sql, "source_type IN ('admin', 'compensation') AND bucket_type = 'admin_grant'")
	require.Contains(t, sql, "remaining_cents + forfeited_cents + refunded_cents + admin_deducted_cents + revoked_cents = original_cents")
	require.Contains(t, sql, "bucket_type = 'paid' AND entry_type NOT IN ('admin_add', 'compensation', 'admin_deduct', 'admin_revoke')")
	require.Contains(t, sql, "bucket_type = 'admin_grant' AND entry_type NOT IN ('payment_credit', 'refund_reserve', 'refund_release', 'refund_success')")
	require.Contains(t, sql, "CREATE OR REPLACE FUNCTION reject_security_deposit_immutable_mutation()")
	require.Contains(t, sql, "security_deposit_ledger_append_only")
	require.Contains(t, sql, "security_deposit_risk_events_append_only")
	require.Contains(t, sql, "security_deposit_agreements_append_only")
}

func TestMigration225ScopesAgreementUniquenessByGroup(t *testing.T) {
	content, err := FS.ReadFile("225_security_deposit_agreement_group_scope.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS security_deposit_agreements_user_policy_unique")
	require.Contains(t, sql, "UNIQUE (user_id, group_id, policy_version, content_hash)")
}

func TestMigration226AddsImmutableAdminActionAudit(t *testing.T) {
	content, err := FS.ReadFile("226_security_deposit_admin_actions.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS security_deposit_admin_actions")
	require.Contains(t, sql, "action_key VARCHAR(191) NOT NULL UNIQUE")
	require.Contains(t, sql, "'admin_add', 'compensation', 'admin_deduct', 'admin_revoke', 'key_unlock'")
	require.Contains(t, sql, "security_deposit_admin_actions_append_only")
}

func TestMigration227AllowsCompleteExternalRefundFactsForAutomaticReview(t *testing.T) {
	content, err := FS.ReadFile("227_security_deposit_refund_external_facts.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS security_deposit_refunds_external_shape_check")
	require.Contains(t, sql, "external_refund_id IS NULL AND external_refunded_at IS NULL AND external_evidence IS NULL")
	require.Contains(t, sql, "external_refund_id IS NOT NULL AND external_refunded_at IS NOT NULL AND external_evidence IS NOT NULL")
	require.NotContains(t, sql, "mode <> 'automatic_original_channel'")
}
