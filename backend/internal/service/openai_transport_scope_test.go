package service

import (
	"context"
	"testing"
)

func TestOpenAITransportScopeFromContextRequiresV3AndFullGenerations(t *testing.T) {
	binding := SessionPersonaSlotBinding{
		AccountID:         42,
		SlotID:            1,
		SlotCount:         2,
		MappingVersion:    SessionPersonaScopeVersionV3,
		PersonaID:         SessionPersonaOpenCode,
		PersonaVersion:    "1.18.23",
		CredentialChainID: "chain-opencode",
		InstallationID:    "install-opencode",
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      3,
		SlotGeneration:    2,
		SlotSetGeneration: 4,
	}
	ctx := ContextWithSessionPersonaBinding(context.Background(), binding)

	scope, ok := OpenAITransportScopeFromContext(ctx, 42)
	if !ok {
		t.Fatal("complete v3 binding was not accepted")
	}
	if scope.AccountID != binding.AccountID || scope.Persona != binding.PersonaID ||
		scope.SlotID != binding.SlotID || scope.SessionEpoch != binding.SessionEpoch ||
		scope.CredentialChainID != binding.CredentialChainID {
		t.Fatalf("scope mismatch: %#v", scope)
	}

	if _, ok := OpenAITransportScopeFromContext(ctx, 7); ok {
		t.Fatal("scope with mismatched account was accepted")
	}

	legacy := binding
	legacy.MappingVersion = SessionPersonaScopeVersionLegacyV2
	legacyCtx := ContextWithSessionPersonaBinding(context.Background(), legacy)
	if _, ok := OpenAITransportScopeFromContext(legacyCtx, 42); ok {
		t.Fatal("legacy v2 binding was routed to CPA transport")
	}

	incomplete := binding
	incomplete.CredentialChainID = ""
	incompleteCtx := ContextWithSessionPersonaBinding(context.Background(), incomplete)
	if _, ok := OpenAITransportScopeFromContext(incompleteCtx, 42); ok {
		t.Fatal("binding without a credential chain was accepted")
	}
}
