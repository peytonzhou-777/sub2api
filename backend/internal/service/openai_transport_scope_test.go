package service

import (
	"context"
	"testing"
)

func TestOpenAITransportScopeFromExecutionTargetUsesDynamicPersonaIdentity(t *testing.T) {
	proxyID := int64(9)
	target := OpenAIExecutionTarget{
		AccountID: 42, AccountPersonaID: 81, PersonaGeneration: 3, SessionEpoch: 7,
		CredentialChainID: "chain-dynamic", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: "1.18.23", InstallationID: "install-dynamic",
		UpstreamSessionID: "session-dynamic", EffectiveProxyID: &proxyID, ProxyRevision: 11,
	}
	ctx := ContextWithOpenAIExecutionTarget(context.Background(), target)
	scope, ok := OpenAITransportScopeFromContext(ctx, target.AccountID)
	if !ok {
		t.Fatal("complete dynamic execution target was not accepted")
	}
	if scope.AccountPersonaID != target.AccountPersonaID || scope.ProfileID != target.ProfileID ||
		scope.PersonaGeneration != target.PersonaGeneration || scope.ProxyRevision != target.ProxyRevision {
		t.Fatalf("dynamic transport scope mismatch: %#v", scope)
	}
	other := scope
	other.AccountPersonaID++
	if scope.OpenAICPAScopeFingerprint("proxy") == other.OpenAICPAScopeFingerprint("proxy") {
		t.Fatal("different AccountPersona instances shared a transport fingerprint")
	}
	other = scope
	other.ProxyRevision++
	if scope.OpenAICPAScopeFingerprint("proxy") == other.OpenAICPAScopeFingerprint("proxy") {
		t.Fatal("different proxy revisions shared a transport fingerprint")
	}
}

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
