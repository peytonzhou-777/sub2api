package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSanitizeCodexSubagentConcurrencyExtra(t *testing.T) {
	t.Run("normalizes json number", func(t *testing.T) {
		extra := map[string]any{"codex_subagent_max_inflight_per_session": json.Number("4")}
		require.NoError(t, sanitizeCodexSubagentConcurrencyExtra(extra))
		require.Equal(t, 4, extra["codex_subagent_max_inflight_per_session"])
	})

	t.Run("keeps explicit zero for bulk merge", func(t *testing.T) {
		extra := map[string]any{"codex_subagent_max_inflight_per_session": float64(0)}
		require.NoError(t, sanitizeCodexSubagentConcurrencyExtra(extra))
		require.Equal(t, 0, extra["codex_subagent_max_inflight_per_session"])
	})

	for _, invalid := range []any{float64(1.5), -1, 65, "4"} {
		extra := map[string]any{"codex_subagent_max_inflight_per_session": invalid}
		require.Error(t, sanitizeCodexSubagentConcurrencyExtra(extra))
	}
}
