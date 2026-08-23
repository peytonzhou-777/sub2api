package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistrationLegacyEligibilityMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("238_registration_legacy_eligibilities.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS registration_legacy_eligibilities")
	require.Contains(t, sql, "normalized_email TEXT PRIMARY KEY")
	require.Contains(t, sql, "eligible BOOLEAN NOT NULL")
	require.Contains(t, sql, "failure_reasons TEXT[] NOT NULL")
	require.Contains(t, sql, "source_batch TEXT NOT NULL")
	require.Contains(t, sql, "normalized_email = LOWER(BTRIM(normalized_email))")
	require.Contains(t, sql, "(eligible AND CARDINALITY(failure_reasons) = 0)")
	require.Contains(t, sql, "(NOT eligible AND CARDINALITY(failure_reasons) > 0)")
	require.Contains(t, sql, "registration_legacy_eligibilities_reason_values")
}
