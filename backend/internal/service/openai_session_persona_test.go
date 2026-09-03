package service

import (
	"errors"
	"reflect"
	"testing"
)

func TestNewDefaultSessionPersonaRegistry(t *testing.T) {
	registry := NewDefaultSessionPersonaRegistry()
	personas := registry.List()
	if len(personas) != 2 {
		t.Fatalf("default registry has %d personas, want 2", len(personas))
	}
	if got, want := personas[0].ID, SessionPersonaCodexCLIStrict; got != want {
		t.Fatalf("first persona ID = %q, want %q", got, want)
	}
	if got, want := personas[1].ID, SessionPersonaOpenCode; got != want {
		t.Fatalf("second persona ID = %q, want %q", got, want)
	}

	strict, ok := registry.Get(string(SessionPersonaCodexCLIStrict))
	if !ok {
		t.Fatal("strict Codex persona is not registered")
	}
	if got, want := strict.EffectiveVersion(), SessionPersonaCodexCLIStrictVersion; got != want {
		t.Fatalf("strict version = %q, want %q", got, want)
	}
	if got, want := strict.Originator, "codex_cli_rs"; got != want {
		t.Fatalf("strict originator = %q, want %q", got, want)
	}
	if got, want := strict.Compression, SessionPersonaCompressionZstd; got != want {
		t.Fatalf("strict compression = %q, want %q", got, want)
	}

	opencode, ok := registry.Get(" OpenCode ")
	if !ok {
		t.Fatal("OpenCode persona is not registered")
	}
	if got, want := opencode.EffectiveVersion(), SessionPersonaOpenCodeVersion; got != want {
		t.Fatalf("OpenCode version = %q, want %q", got, want)
	}
	if got, want := SessionPersonaOpenCodeTag, "v1.18.23"; got != want {
		t.Fatalf("OpenCode tag = %q, want %q", got, want)
	}
	if got, want := SessionPersonaOpenCodeTagSHA, "ef2880f379129aa048be9e9353e30aa168d42c17"; got != want {
		t.Fatalf("OpenCode tag SHA = %q, want %q", got, want)
	}
	if got, want := opencode.Originator, "opencode"; got != want {
		t.Fatalf("OpenCode originator = %q, want %q", got, want)
	}
	if got, want := opencode.Compression, SessionPersonaCompressionNone; got != want {
		t.Fatalf("OpenCode compression = %q, want %q", got, want)
	}
	if opencode.SupportsTransport(SessionPersonaTransportWS) {
		t.Fatal("OpenCode WS transport was advertised before the gateway WS adapter rollout")
	}
	if got, want := opencode.BuildUserAgent("linux", "6.8.0", "x86_64"), "opencode/"+SessionPersonaOpenCodeVersion+" (linux 6.8.0; x86_64)"; got != want {
		t.Fatalf("OpenCode UA = %q, want %q", got, want)
	}
	if got, want := opencode.EndpointForTransport(SessionPersonaTransportWS), "wss://chatgpt.com/backend-api/codex/responses"; got != want {
		t.Fatalf("OpenCode WS endpoint = %q, want %q", got, want)
	}
}

func TestResolveSessionPersonaSlotV3Defaults(t *testing.T) {
	registry := NewDefaultSessionPersonaRegistry()

	tests := []struct {
		name        string
		slotID      int
		wantPersona SessionPersonaID
		wantMapping SessionPersonaMappingKind
	}{
		{name: "strict Codex slot", slotID: 0, wantPersona: SessionPersonaCodexCLIStrict, wantMapping: SessionPersonaMappingPersonaV3},
		{name: "OpenCode slot", slotID: 1, wantPersona: SessionPersonaOpenCode, wantMapping: SessionPersonaMappingPersonaV3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binding, err := registry.ResolveSlot(DefaultSessionPersonaScopeVersion, tt.slotID, 0)
			if err != nil {
				t.Fatalf("ResolveSlot() error = %v", err)
			}
			if !binding.Valid() {
				t.Fatal("resolved binding is invalid")
			}
			if got, want := binding.SlotCount, DefaultSessionPersonaSlotCount; got != want {
				t.Fatalf("slot count = %d, want %d", got, want)
			}
			if got, want := binding.PersonaID, tt.wantPersona; got != want {
				t.Fatalf("persona = %q, want %q", got, want)
			}
			if got, want := binding.Mapping, tt.wantMapping; got != want {
				t.Fatalf("mapping = %q, want %q", got, want)
			}
			if binding.Legacy || binding.CompatibilityFallback {
				t.Fatalf("v3 default binding unexpectedly marked legacy/fallback: %+v", binding)
			}
		})
	}

	if _, err := ResolveDefaultSessionPersonaSlot(2); !errors.Is(err, ErrSessionPersonaInvalidSlot) {
		t.Fatalf("slot 2 error = %v, want ErrSessionPersonaInvalidSlot", err)
	}
}

func TestResolveSessionPersonaSlotLegacyCodexCompatibility(t *testing.T) {
	for _, scopeVersion := range []int{0, SessionPersonaScopeVersionLegacyV2} {
		for _, slotID := range []int{0, 1} {
			binding, err := ResolveSessionPersonaSlot(scopeVersion, slotID, 0)
			if err != nil {
				t.Fatalf("scope=%d slot=%d: ResolveSessionPersonaSlot() error = %v", scopeVersion, slotID, err)
			}
			if got, want := binding.ScopeVersion, SessionPersonaScopeVersionLegacyV2; scopeVersion == 0 && got != want {
				t.Fatalf("missing scope resolved to %d, want legacy v2", got)
			}
			if got, want := binding.PersonaID, SessionPersonaCodexCLIStrict; got != want {
				t.Fatalf("scope=%d slot=%d persona = %q, want %q", scopeVersion, slotID, got, want)
			}
			if got, want := binding.Mapping, SessionPersonaMappingLegacyCodexPair; got != want {
				t.Fatalf("scope=%d slot=%d mapping = %q, want %q", scopeVersion, slotID, got, want)
			}
			if !binding.Legacy || binding.CompatibilityFallback {
				t.Fatalf("scope=%d slot=%d compatibility flags = legacy:%t fallback:%t", scopeVersion, slotID, binding.Legacy, binding.CompatibilityFallback)
			}
		}
	}

	v1, err := ResolveSessionPersonaSlot(SessionPersonaScopeVersionLegacyV1, 0, 0)
	if err != nil {
		t.Fatalf("v1 missing count: ResolveSessionPersonaSlot() error = %v", err)
	}
	if got, want := v1.SlotCount, LegacySingleSessionPersonaSlotCount; got != want {
		t.Fatalf("v1 missing count = %d, want %d", got, want)
	}
	if got, want := v1.PersonaID, SessionPersonaCodexCLIStrict; got != want {
		t.Fatalf("v1 persona = %q, want %q", got, want)
	}

	if _, err := ResolveLegacyCodexV2SessionPersonaSlot(1, 2); err != nil {
		t.Fatalf("explicit v2 slot 1: ResolveLegacyCodexV2SessionPersonaSlot() error = %v", err)
	}
}

func TestResolveSessionPersonaSlotV3HistoricalExtraSlotFallback(t *testing.T) {
	if binding, err := ResolveDefaultSessionPersonaSlot(0); err != nil || !binding.Valid() {
		t.Fatalf("bare default binding should be valid: binding=%+v err=%v", binding, err)
	}

	binding, err := ResolveSessionPersonaSlot(SessionPersonaScopeVersionV3, 2, 4)
	if err != nil {
		t.Fatalf("ResolveSessionPersonaSlot() error = %v", err)
	}
	if got, want := binding.PersonaID, SessionPersonaCodexCLIStrict; got != want {
		t.Fatalf("fallback persona = %q, want %q", got, want)
	}
	if got, want := binding.Mapping, SessionPersonaMappingCompatibility; got != want {
		t.Fatalf("fallback mapping = %q, want %q", got, want)
	}
	if !binding.CompatibilityFallback || binding.Legacy {
		t.Fatalf("unexpected fallback flags: %+v", binding)
	}
}

func TestSessionPersonaRegistryDefensiveCopiesAndValidation(t *testing.T) {
	registry := NewDefaultSessionPersonaRegistry()

	first, ok := registry.Get(string(SessionPersonaOpenCode))
	if !ok {
		t.Fatal("OpenCode persona is not registered")
	}
	first.SupportedTransports[0] = SessionPersonaTransport("mutated")
	second, ok := registry.Get(string(SessionPersonaOpenCode))
	if !ok {
		t.Fatal("OpenCode persona disappeared after mutation")
	}
	if got, want := second.SupportedTransports[0], SessionPersonaTransportHTTP; got != want {
		t.Fatalf("registry transport was mutated through Get(): %q, want %q", got, want)
	}

	if err := registry.Register(SessionPersona{ID: SessionPersonaID("unknown")}); !errors.Is(err, ErrSessionPersonaInvalidDefinition) {
		t.Fatalf("invalid Register() error = %v, want ErrSessionPersonaInvalidDefinition", err)
	}
	if _, err := registry.MustGet("missing"); !errors.Is(err, ErrSessionPersonaUnknown) {
		t.Fatalf("MustGet() error = %v, want ErrSessionPersonaUnknown", err)
	}

	cloned := registry.Clone()
	if !reflect.DeepEqual(registry.List(), cloned.List()) {
		t.Fatal("Clone() does not preserve the registry snapshot")
	}
	if err := cloned.Register(SessionPersona{
		ID:                  SessionPersonaOpenCode,
		PersonaVersion:      "test",
		UserAgent:           "test",
		Originator:          "test",
		Endpoint:            SessionPersonaOpenAICodexEndpoint,
		Transport:           SessionPersonaTransportHTTP,
		SupportedTransports: []SessionPersonaTransport{SessionPersonaTransportHTTP},
		Compression:         SessionPersonaCompressionNone,
	}); err != nil {
		t.Fatalf("Register() on clone error = %v", err)
	}
	original, _ := registry.Get(string(SessionPersonaOpenCode))
	if got, want := original.EffectiveVersion(), SessionPersonaOpenCodeVersion; got != want {
		t.Fatalf("original registry changed after clone mutation: version=%q, want %q", got, want)
	}
}

func TestSessionPersonaBindingNormalizeLifecycleTreatsLegacyDisabledAsDraining(t *testing.T) {
	binding := SessionPersonaSlotBinding{
		SlotID:            1,
		SlotCount:         2,
		MappingVersion:    SessionPersonaScopeVersionV3,
		PersonaID:         SessionPersonaOpenCode,
		Enabled:           false,
		EnabledConfigured: true,
		Authorized:        true,
	}
	normalized := binding.NormalizeLifecycle()
	if normalized.State != SessionPersonaSlotStateDraining {
		t.Fatalf("legacy disabled slot with ready credentials normalized to %q, want draining", normalized.State)
	}
	if normalized.AcceptsNewRoot() {
		t.Fatal("disabled slot must not accept a new root")
	}
}

func TestSessionPersonaBindingNormalizeLifecycleKeepsMissingEnabledLegacyActive(t *testing.T) {
	binding := SessionPersonaSlotBinding{
		SlotID:         1,
		SlotCount:      2,
		MappingVersion: SessionPersonaScopeVersionV3,
		PersonaID:      SessionPersonaOpenCode,
		Enabled:        false,
		Authorized:     true,
	}
	normalized := binding.NormalizeLifecycle()
	if normalized.State != SessionPersonaSlotStateActive {
		t.Fatalf("legacy binding without enabled field normalized to %q, want active", normalized.State)
	}
}

func TestSessionPersonaBindingNormalizeLifecyclePreservesExplicitHardDisable(t *testing.T) {
	binding := SessionPersonaSlotBinding{
		SlotID:         1,
		SlotCount:      2,
		MappingVersion: SessionPersonaScopeVersionV3,
		PersonaID:      SessionPersonaOpenCode,
		State:          SessionPersonaSlotStateDisabled,
		Enabled:        false,
		Authorized:     true,
	}
	normalized := binding.NormalizeLifecycle()
	if normalized.State != SessionPersonaSlotStateDisabled {
		t.Fatalf("explicit hard-disabled slot normalized to %q, want disabled", normalized.State)
	}
	if normalized.AcceptsNewRoot() {
		t.Fatal("hard-disabled slot must not accept a new root")
	}
}
