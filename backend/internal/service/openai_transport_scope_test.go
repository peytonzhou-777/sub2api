package service

import (
	"context"
	"testing"
	"time"
)

func TestOpenAITransportScopeFromExecutionTargetUsesDynamicPersonaIdentity(t *testing.T) {
	proxyID := int64(9)
	target := OpenAIExecutionTarget{
		AccountID: 42, AccountPersonaID: 81, PersonaGeneration: 3, SessionEpoch: 7,
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
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

func TestResolveOpenAIUpstreamProxyURLUsesExecutionTargetSnapshot(t *testing.T) {
	accountProxyID := int64(1)
	targetProxyID := int64(2)
	account := &Account{ID: 42, ProxyID: &accountProxyID, Proxy: &Proxy{ID: accountProxyID, Host: "account-proxy", Port: 8080}}
	target := OpenAIExecutionTarget{
		AccountID: account.ID, AccountPersonaID: 81, PersonaGeneration: 3, SessionEpoch: 7,
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		CredentialChainID: "chain-dynamic", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: "1.18.23", InstallationID: "install-dynamic",
		UpstreamSessionID: "session-dynamic", EffectiveProxyID: &targetProxyID,
		ProxyRevision: 11, EffectiveProxyURL: "http://persona-proxy:9000",
	}
	ctx := ContextWithOpenAIExecutionTarget(context.Background(), target)

	if got := resolveOpenAIUpstreamProxyURL(ctx, account); got != target.EffectiveProxyURL {
		t.Fatalf("proxy = %q, want Persona snapshot %q", got, target.EffectiveProxyURL)
	}
	target.EffectiveProxyID = nil
	target.ProxyRevision = 0
	target.EffectiveProxyURL = ""
	ctx = ContextWithOpenAIExecutionTarget(context.Background(), target)
	if got := resolveOpenAIUpstreamProxyURL(ctx, account); got != "" {
		t.Fatalf("direct Persona snapshot fell back to account proxy: %q", got)
	}
}

func TestOpenAITransportScopeFromContextRejectsLegacySlotBinding(t *testing.T) {
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

	if _, ok := OpenAITransportScopeFromContext(ctx, 42); ok {
		t.Fatal("fixed-slot binding was routed to the dynamic CPA manager")
	}
}
