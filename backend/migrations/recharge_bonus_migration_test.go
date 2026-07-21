package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration174AddsRechargeBonusCampaignAndOrderConstraints(t *testing.T) {
	content, err := FS.ReadFile("174_recharge_bonus_campaigns.sql")
	require.NoError(t, err)

	sql := string(content)
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS recharge_bonus_campaigns")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS recharge_bonus_participations")
	require.Contains(t, sql, "UNIQUE (campaign_id, user_id)")
	require.Contains(t, sql, "recharge_bonus_campaign_id BIGINT NULL REFERENCES recharge_bonus_campaigns")
	require.Contains(t, sql, "recharge_bonus_status VARCHAR(20) NOT NULL DEFAULT 'none'")
	require.Contains(t, sql, "EXCLUDE USING gist (tstzrange(start_at, end_at, '[)') WITH &&)")
	require.Contains(t, sql, "CHECK (recharge_bonus_status IN ('none', 'eligible', 'granted', 'limit_reached'))")
	require.Contains(t, sql, "WHERE source_type = 'recharge_bonus' AND source_id IS NOT NULL")
}
