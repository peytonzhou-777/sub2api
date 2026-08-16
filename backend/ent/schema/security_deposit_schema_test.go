package schema

import (
	"testing"

	"entgo.io/ent/entc/load"
	"github.com/stretchr/testify/require"
)

func TestSecurityDepositFoundationSchemas(t *testing.T) {
	spec, err := (&load.Config{Path: "."}).Load()
	require.NoError(t, err)

	schemas := map[string]*load.Schema{}
	for _, loadedSchema := range spec.Schemas {
		schemas[loadedSchema.Name] = loadedSchema
	}

	requireSchemaFields(t, requireSchema(t, schemas, "Group"),
		"security_deposit_base_required_cents",
		"security_deposit_policy_version",
	)
	requireSchemaFields(t, requireSchema(t, schemas, "APIKey"),
		"security_locked_at",
		"security_lock_violation_id",
		"security_lock_reason",
		"disabled_reason",
		"disabled_financial_event_type",
		"disabled_financial_event_id",
		"disabled_at",
	)

	account := requireSchema(t, schemas, "SecurityDepositAccount")
	requireSchemaFields(t, account, "user_id", "bucket_type", "currency", "balance_cents", "refund_reserved_cents", "version")
	requireHasUniqueIndex(t, account, "user_id", "bucket_type")

	riskProfile := requireSchema(t, schemas, "SecurityDepositRiskProfile")
	requireSchemaFields(t, riskProfile, "user_id", "cyber_strike_count", "risk_multiplier", "last_violation_id", "version")

	riskEvent := requireSchema(t, schemas, "SecurityDepositRiskEvent")
	requireSchemaFields(t, riskEvent, "event_type", "violation_id", "multiplier_before", "multiplier_after", "idempotency_key")
	requireHasUniqueIndex(t, riskEvent, "violation_id")

	lot := requireSchema(t, schemas, "SecurityDepositLot")
	requireSchemaFields(t, lot,
		"bucket_type", "source_type", "payment_order_id", "original_cents", "remaining_cents",
		"refund_reserved_cents", "forfeited_cents", "refunded_cents", "admin_deducted_cents",
		"revoked_cents", "locked_until", "refund_policy",
	)
	requireHasUniqueIndex(t, lot, "payment_order_id")

	ledger := requireSchema(t, schemas, "SecurityDepositLedger")
	requireSchemaFields(t, ledger, "lot_id", "bucket_type", "entry_type", "delta_cents", "reserved_delta_cents", "idempotency_key")

	violation := requireSchema(t, schemas, "SecurityDepositViolation")
	requireSchemaFields(t, violation,
		"event_key", "request_id", "user_id", "api_key_id", "group_id", "policy_code",
		"required_snapshot_cents", "risk_multiplier_before", "risk_multiplier_after", "forfeited_cents", "shortfall_cents",
	)

	refund := requireSchema(t, schemas, "SecurityDepositRefund")
	requireSchemaFields(t, refund, "refund_id", "lot_id", "principal_cents", "mode", "state", "idempotency_key", "external_evidence")

	agreement := requireSchema(t, schemas, "SecurityDepositAgreement")
	requireSchemaFields(t, agreement, "policy_version", "content_hash", "group_id", "required_snapshot_cents", "accepted_at", "client_ip", "user_agent")
	requireHasUniqueIndex(t, agreement, "user_id", "group_id", "policy_version", "content_hash")
}
