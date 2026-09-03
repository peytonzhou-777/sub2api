package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestMigration255FreezesPersonaSessionProxySnapshot(t *testing.T) {
	payload, err := os.ReadFile("255_openai_account_persona_session_transport.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(payload)
	for _, fragment := range []string{
		"effective_proxy_url TEXT NOT NULL DEFAULT ''",
		"installation_id VARCHAR(256) NOT NULL DEFAULT ''",
		"proxy_snapshot_set BOOLEAN NOT NULL DEFAULT FALSE",
		"session.installation_id = ''",
		"idx_openai_account_persona_sessions_lookup",
		"禁止管理 API 和日志返回",
	} {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("migration missing %q", fragment)
		}
	}
}
