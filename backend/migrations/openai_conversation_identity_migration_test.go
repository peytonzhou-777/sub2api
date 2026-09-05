package migrations

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestMigration262KeepsOnlyLiveConversationIdentities(t *testing.T) {
	body, err := FS.ReadFile("262_openai_conversation_identity_lifetime.sql")
	require.NoError(t, err)
	sql := strings.ToLower(string(body))
	require.Contains(t, sql, "binding_epoch = 2 and first_output_committed")
	require.Contains(t, sql, "status in ('active', 'draining')")
	require.Contains(t, sql, "and openai_conversation_is_live(id, active_until, expires_at)")
	require.Contains(t, sql, "select deadline > now() or openai_conversation_has_activity(target_id)")
	require.NotContains(t, sql, "set status")
	require.NotContains(t, sql, "delete from")
}
