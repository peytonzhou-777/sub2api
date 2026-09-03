package service

import (
	"context"
	"encoding/json"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func TestDefaultOpenAIAccountAdmissionConfig(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	if cfg.Enabled || cfg.QueueEnabled {
		t.Fatalf("new admission control must default to disabled: %+v", cfg)
	}
	if cfg.MaxWaitSeconds != 45 || cfg.JitterMinMS != 100 || cfg.JitterMaxMS != 500 {
		t.Fatalf("unexpected queue defaults: %+v", cfg)
	}
	if cfg.InteractiveBurst != 4 || cfg.BackgroundAgingSeconds != 5 {
		t.Fatalf("unexpected priority defaults: %+v", cfg)
	}
	if cfg.MaxActiveClientSessions != 1 {
		t.Fatalf("default max active client Sessions = %d, want 1", cfg.MaxActiveClientSessions)
	}
	if cfg.MaxActiveClientSessionsPerUserGroup != 3 {
		t.Fatalf("default User x Group max active client Sessions = %d, want 3", cfg.MaxActiveClientSessionsPerUserGroup)
	}
}

func TestOpenAIAccountAdmissionConfigLegacyJSONKeepsUserGroupDefault(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	requireNoField := `{"max_wait_seconds":45,"max_active_client_sessions":1,"default_output_tokens":4096,"jitter_min_ms":100,"jitter_max_ms":500,"max_queue_depth_per_account":100,"interactive_burst":4,"background_aging_seconds":5}`
	if err := json.Unmarshal([]byte(requireNoField), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MaxActiveClientSessionsPerUserGroup != 3 {
		t.Fatalf("legacy JSON default = %d, want 3", cfg.MaxActiveClientSessionsPerUserGroup)
	}
}

func TestValidateOpenAIAccountAdmissionConfig(t *testing.T) {
	cases := []struct {
		name string
		edit func(*OpenAIAccountAdmissionConfig)
	}{
		{"max wait", func(c *OpenAIAccountAdmissionConfig) { c.MaxWaitSeconds = 121 }},
		{"rpm", func(c *OpenAIAccountAdmissionConfig) { c.RequestsPerMinute = -1 }},
		{"active client sessions", func(c *OpenAIAccountAdmissionConfig) { c.MaxActiveClientSessions = -1 }},
		{"user group active client sessions", func(c *OpenAIAccountAdmissionConfig) { c.MaxActiveClientSessionsPerUserGroup = 0 }},
		{"tpm", func(c *OpenAIAccountAdmissionConfig) { c.TokensPerMinute = 100000001 }},
		{"jitter order", func(c *OpenAIAccountAdmissionConfig) { c.JitterMaxMS = c.JitterMinMS - 1 }},
		{"queue depth", func(c *OpenAIAccountAdmissionConfig) { c.MaxQueueDepthPerAccount = 0 }},
		{"interactive burst", func(c *OpenAIAccountAdmissionConfig) { c.InteractiveBurst = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultOpenAIAccountAdmissionConfig()
			tc.edit(&cfg)
			if _, err := ValidateOpenAIAccountAdmissionConfig(cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestOpenAIAccountAdmissionConfigForPersona(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	cfg.RequestsPerMinute = 10
	cfg.TokensPerMinute = 1000
	cfg.PersonaPolicies = map[string]OpenAIPersonaAdmissionPolicy{
		" OpenCode ": {
			MaxActiveClientSessions: 2,
			RequestsPerMinute:       20,
			TokensPerMinute:         2000,
			MaxQueueDepthPerAccount: 7,
		},
	}

	if _, err := ValidateOpenAIAccountAdmissionConfig(cfg); err != nil {
		t.Fatalf("validate Persona policy: %v", err)
	}
	opencode := cfg.ForPersona(SessionPersonaOpenCode)
	if opencode.MaxActiveClientSessions != 2 || opencode.RequestsPerMinute != 20 || opencode.TokensPerMinute != 2000 || opencode.MaxQueueDepthPerAccount != 7 {
		t.Fatalf("unexpected OpenCode policy: %+v", opencode)
	}
	strict := cfg.ForPersona(SessionPersonaCodexCLIStrict)
	if strict.MaxActiveClientSessions != 1 || strict.RequestsPerMinute != 10 || strict.TokensPerMinute != 1000 || strict.MaxQueueDepthPerAccount != cfg.MaxQueueDepthPerAccount {
		t.Fatalf("legacy strict policy changed: %+v", strict)
	}
}

func TestEffectiveOpenAIAccountAdmissionCapacityUsesActiveAuthorizedPersonaSlots(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	account.Concurrency = 4
	account.Extra[openAIPersonaMappingEnabledExtraKey] = true
	account.Extra[openAIPersonaMappingVersionExtraKey] = SessionPersonaScopeVersionV3
	account.Extra[OpenAIPersonaSlotAuthorizedExtraKey] = map[string]any{"0": true, "1": true}
	account.Extra[OpenAIPersonaActiveChainsExtraKey] = map[string]any{"0": "strict-chain", "1": "opencode-chain"}
	cfg := DefaultOpenAIAccountAdmissionConfig()
	cfg.PersonaPolicies = map[string]OpenAIPersonaAdmissionPolicy{
		string(SessionPersonaCodexCLIStrict): {MaxConcurrency: 3},
		string(SessionPersonaOpenCode):       {MaxConcurrency: 2},
	}

	if got := EffectiveOpenAIAccountAdmissionCapacity(account, cfg); got != 5 {
		t.Fatalf("active Persona capacity = %d, want 5", got)
	}
	account.Extra[openAIPersonaSlotStateExtraKey] = map[string]any{"1": string(SessionPersonaSlotStateDraining)}
	if got := EffectiveOpenAIAccountAdmissionCapacity(account, cfg); got != 3 {
		t.Fatalf("draining OpenCode capacity = %d, want 3", got)
	}
}

func TestValidateOpenAIAccountAdmissionConfigRejectsUnknownPersona(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	cfg.PersonaPolicies = map[string]OpenAIPersonaAdmissionPolicy{
		"unknown": {RequestsPerMinute: 1},
	}
	if _, err := ValidateOpenAIAccountAdmissionConfig(cfg); err == nil {
		t.Fatal("expected unknown Persona policy to be rejected")
	}
}

func TestOpenAIAccountAdmissionConfigVersionCAS(t *testing.T) {
	repo := &openAIUserAffinitySettingRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, nil)
	initial, err := svc.GetOpenAIAccountAdmissionConfig(context.Background())
	if err != nil {
		t.Fatalf("get default config: %v", err)
	}
	next := initial
	next.Enabled = true
	next.QueueEnabled = true
	updated, err := svc.UpdateOpenAIAccountAdmissionConfig(context.Background(), next, initial.ConfigVersion)
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	if updated.ConfigVersion != 1 || !updated.Enabled || !updated.QueueEnabled {
		t.Fatalf("unexpected update: %+v", updated)
	}
	_, err = svc.UpdateOpenAIAccountAdmissionConfig(context.Background(), next, initial.ConfigVersion)
	if err == nil || !infraerrors.IsConflict(err) {
		t.Fatal("expected stale version conflict")
	}
}
