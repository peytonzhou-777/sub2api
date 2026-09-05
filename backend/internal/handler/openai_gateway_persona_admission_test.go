package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func openAIPersonaAdmissionTestBinding() service.SessionPersonaSlotBinding {
	return service.SessionPersonaSlotBinding{
		AccountID:         42,
		SlotID:            1,
		SlotCount:         2,
		ScopeVersion:      service.SessionPersonaScopeVersionV3,
		MappingVersion:    service.SessionPersonaScopeVersionV3,
		PersonaID:         service.SessionPersonaOpenCode,
		PersonaVersion:    service.SessionPersonaOpenCodeVersion,
		CredentialChainID: "opencode-chain-1",
		State:             service.SessionPersonaSlotStateDraining,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      7,
		SlotGeneration:    4,
		SlotSetGeneration: 9,
		Mapping:           service.SessionPersonaMappingPersonaV3,
	}
}

func TestPopulateOpenAIPersonaAdmissionRequest(t *testing.T) {
	binding := openAIPersonaAdmissionTestBinding()
	req := service.OpenAIAccountAdmissionRequest{AccountID: 42, SlotID: 99}
	populateOpenAIPersonaAdmissionRequest(context.Background(), &req, binding, true)
	if req.Persona != string(service.SessionPersonaOpenCode) || req.SlotID != 1 {
		t.Fatalf("unexpected Persona placement: %+v", req)
	}
	if req.SlotGeneration != 4 || req.SlotSetGeneration != 9 || req.CredentialChainID != "opencode-chain-1" {
		t.Fatalf("unexpected generation/credential metadata: %+v", req)
	}

	legacy := service.OpenAIAccountAdmissionRequest{AccountID: 42, SlotID: 99}
	populateOpenAIPersonaAdmissionRequest(context.Background(), &legacy, binding, false)
	if legacy.Persona != "" || legacy.SlotID != 99 || legacy.SlotGeneration != 0 || legacy.CredentialChainID != "" {
		t.Fatalf("legacy request was modified: %+v", legacy)
	}
}

func TestPopulateOpenAIPersonaAdmissionRequestPrefersExecutionTarget(t *testing.T) {
	target := service.OpenAIExecutionTarget{
		AccountID: 42, AccountPersonaID: 108, PersonaGeneration: 5, SessionEpoch: 9,
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		CredentialChainID: "dynamic-chain", ProfileID: service.SessionPersonaCodexCLIStrict,
		ProfileVersion: "0.149.0", InstallationID: "install", UpstreamSessionID: "session",
	}
	ctx := service.ContextWithOpenAIExecutionTarget(context.Background(), target)
	req := service.OpenAIAccountAdmissionRequest{AccountID: 42}
	populateOpenAIPersonaAdmissionRequest(ctx, &req, openAIPersonaAdmissionTestBinding(), true)
	if req.AccountPersonaID != 108 || req.PersonaGeneration != 5 || req.SessionEpoch != 9 ||
		req.CredentialChainID != "dynamic-chain" || req.Persona != string(service.SessionPersonaCodexCLIStrict) {
		t.Fatalf("execution target was not authoritative: %+v", req)
	}
}

func TestApplyOpenAIPersonaAdmissionPolicy(t *testing.T) {
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.RequestsPerMinute = 10
	cfg.TokensPerMinute = 1000
	cfg.PersonaPolicies = map[string]service.OpenAIPersonaAdmissionPolicy{
		"opencode": {
			MaxConcurrency:          3,
			MaxActiveUsers:          2,
			RequestsPerMinute:       20,
			TokensPerMinute:         2000,
			MaxQueueDepthPerAccount: 7,
		},
	}

	account := &service.Account{Concurrency: 12}
	got, accountConcurrency, personaConcurrency := applyOpenAIPersonaAdmissionPolicy(context.Background(), cfg, account, openAIPersonaAdmissionTestBinding(), 12)
	if accountConcurrency != 12 || personaConcurrency != 3 {
		t.Fatalf("effective concurrency = account %d persona %d, want 12/3", accountConcurrency, personaConcurrency)
	}
	if got.DefaultMaxActiveUsersPerPersona != 2 || got.RequestsPerMinute != 20 || got.TokensPerMinute != 2000 || got.MaxQueueDepthPerAccount != 7 {
		t.Fatalf("Persona rate/queue policy not applied: %+v", got)
	}
}
