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

func TestSanitizeCodexSessionSlotCountExtra(t *testing.T) {
	for _, value := range []any{1, json.Number("2"), float64(4)} {
		extra := map[string]any{"codex_session_slot_count": value}
		require.NoError(t, sanitizeCodexSubagentConcurrencyExtra(extra))
	}
	for _, value := range []any{0, 5, float64(1.5), "2"} {
		extra := map[string]any{"codex_session_slot_count": value}
		require.Error(t, sanitizeCodexSubagentConcurrencyExtra(extra))
	}
}

func TestSanitizeCodexOutboundProfileExtra(t *testing.T) {
	for _, profile := range []string{"", "legacy", "codex_cli_0_149_0", " CODEX_CLI_0_149_0 "} {
		extra := map[string]any{"codex_outbound_profile": profile}
		require.NoError(t, sanitizeCodexSubagentConcurrencyExtra(extra))
	}

	for _, invalid := range []any{"random", 149, true} {
		extra := map[string]any{"codex_outbound_profile": invalid}
		require.Error(t, sanitizeCodexSubagentConcurrencyExtra(extra))
	}
}
