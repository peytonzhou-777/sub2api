package migrations

import (
	"strings"
	"testing"
)

func TestOpenAIUserAffinityMultiSlotMigrationKeepsAdditiveCompatibility(t *testing.T) {
	content, err := FS.ReadFile("235_openai_user_affinity_multi_slot_foundation.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sqlText := string(content)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS openai_user_resident_slots",
		"CREATE TABLE IF NOT EXISTS openai_user_conversation_bindings",
		"CREATE TABLE IF NOT EXISTS openai_user_conversation_aliases",
		"ADD COLUMN IF NOT EXISTS conversation_hash CHAR(64)",
		"ADD COLUMN IF NOT EXISTS resident_slot_id",
		"INSERT INTO openai_user_resident_slots",
		"ON CONFLICT DO NOTHING",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
	if strings.Contains(sqlText, "DROP TABLE") || strings.Contains(sqlText, "DROP COLUMN") {
		t.Fatal("P1 migration must remain additive")
	}
}

func TestOpenAIUserAffinityResetExclusionMigrationIsAdditive(t *testing.T) {
	content, err := FS.ReadFile("236_openai_user_affinity_reset_exclusions.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sqlText := string(content)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS openai_user_affinity_reset_exclusions",
		"reset_generation",
		"consumed_at",
		"WHERE consumed_at IS NULL",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("reset exclusion migration missing %q", required)
		}
	}
	if strings.Contains(sqlText, "DROP TABLE") || strings.Contains(sqlText, "DROP COLUMN") {
		t.Fatal("reset exclusion migration must remain additive")
	}
}

func TestOpenAICodexThreadAliasMigrationExtendsAliasConstraint(t *testing.T) {
	content, err := FS.ReadFile("243_openai_codex_thread_alias.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sqlText := string(content)
	for _, required := range []string{
		"openai_user_conversation_aliases_type_check",
		"'codex_thread'",
		"DROP CONSTRAINT IF EXISTS",
		"ADD CONSTRAINT",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("Codex thread alias migration missing %q", required)
		}
	}
	if strings.Contains(sqlText, "DROP TABLE") || strings.Contains(sqlText, "DROP COLUMN") {
		t.Fatal("Codex thread alias migration must not remove table data")
	}
}
