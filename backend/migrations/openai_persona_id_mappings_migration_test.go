package migrations

import (
	"strings"
	"testing"
)

func TestOpenAIPersonaIDMappingsMigrationIsScopedAndAdditive(t *testing.T) {
	content, err := FS.ReadFile("248_openai_persona_id_mappings.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sqlText := string(content)
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS openai_persona_id_mappings",
		"credential_chain_id",
		"slot_generation",
		"slot_set_generation",
		"UNIQUE (scope_key, mapping_type, client_id)",
		"UNIQUE (scope_key, mapping_type, opencode_id)",
		"idx_openai_persona_id_mapping_client_principal",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("Persona ID mapping migration missing %q", required)
		}
	}
	if strings.Contains(sqlText, "DROP TABLE") || strings.Contains(sqlText, "DROP COLUMN") {
		t.Fatal("Persona ID mapping migration must remain additive")
	}
}
