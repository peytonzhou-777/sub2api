package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIUserAffinityResidentCapacityMigrationKeepsRollingCompatibility(t *testing.T) {
	content, err := FS.ReadFile("250_openai_user_affinity_resident_capacity.sql")
	require.NoError(t, err)

	sqlText := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sqlText, "ADD COLUMN IF NOT EXISTS max_resident_users INT")
	require.Contains(t, sqlText, "SET max_resident_users = max_contact_users")
	require.Contains(t, sqlText, "default_max_resident_users")
	require.Contains(t, sqlText, "value::jsonb || CASE")
	require.NotContains(t, sqlText, "value::jsonb - 'default_max_contact_users'")
	require.Contains(t, sqlText, "last_touched_at")
	require.NotContains(t, sqlText, "DROP COLUMN")
}
