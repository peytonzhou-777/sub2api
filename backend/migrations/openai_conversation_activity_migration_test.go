package migrations

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestMigration261ConversationActivityDoesNotExpireProductionBindings(t *testing.T) {
	content, err := FS.ReadFile("261_openai_conversation_activity.sql")
	require.NoError(t, err)
	text := strings.ToLower(string(content))
	require.Contains(t, text, "create table if not exists openai_conversation_request_holds")
	require.Contains(t, text, "openai_conversation_is_live")
	require.Contains(t, text, "openai_persona_user_has_activity")
	require.NotContains(t, text, "update openai_user_conversation_bindings")
	require.NotContains(t, text, "delete from")
}
