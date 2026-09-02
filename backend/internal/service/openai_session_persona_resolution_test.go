package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func newSessionPersonaResolverTestContext(ids *codexFingerprintIDs) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	stageCodexFingerprintIDs(c, ids)
	return c
}

func newSessionPersonaOAuthAccount() *Account {
	return &Account{
		ID:       42,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "legacy-access",
			"refresh_token":      "legacy-refresh",
			"chatgpt_account_id": "chatgpt-account",
			openAIPersonaCredentialsKey: []any{
				map[string]any{
					"persona_id":          string(SessionPersonaOpenCode),
					"slot_id":             1,
					"credential_chain_id": "opencode-chain",
					"access_token":        "opencode-access",
					"refresh_token":       "opencode-refresh",
					"ready":               true,
					"state":               "ready",
				},
			},
		},
		Extra: map[string]any{},
	}
}
func newSessionPersonaResolverIDs(scope, slot, count int) *codexFingerprintIDs {
	return &codexFingerprintIDs{
		accountID:           42,
		sessionScopeVersion: scope,
		sessionSlot:         slot,
		sessionSlotCount:    count,
		sessionEpoch:        11,
	}
}

func TestSessionPersonaRequestTurnStateAloneIsNewRoot(t *testing.T) {
	c := newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 0, 2))
	c.Request.Header.Set(openAIWSTurnStateHeader, "stale-turn-state")
	body := []byte(`{"model":"gpt-5.1","client_metadata":{"x-codex-turn-state":"stale-turn-state"}}`)
	if SessionPersonaRequestHasContinuation(c, body) {
		t.Fatal("turn-state alone must not pin a request to a legacy Persona continuation")
	}
	c.Request.Header.Set("previous_response_id", "resp_1")
	if !SessionPersonaRequestHasContinuation(c, body) {
		t.Fatal("previous_response_id must remain a Persona continuation signal")
	}
}

func TestResolveSessionPersonaBindingForNewRootUsesOnlyActiveAuthorizedSlots(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	account.Extra[openAIPersonaSlotStateExtraKey] = map[string]any{"1": string(SessionPersonaSlotStateDraining)}

	binding, ok := ResolveSessionPersonaBindingForNewRoot(
		newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 1, 2)),
		account,
	)
	if !ok {
		t.Fatal("new-root resolver did not fall back to active strict Codex slot")
	}
	if binding.PersonaID != SessionPersonaCodexCLIStrict || binding.SlotID != 0 {
		t.Fatalf("fallback binding = %+v, want strict Codex slot 0", binding)
	}
	if binding.State != SessionPersonaSlotStateActive || !binding.AcceptsNewRoot() {
		t.Fatalf("fallback binding is not active/eligible: %+v", binding)
	}
	if !binding.CompatibilityFallback || binding.Mapping != SessionPersonaMappingCompatibility {
		t.Fatalf("fallback provenance was not recorded: %+v", binding)
	}

	account.Extra[openAIPersonaSlotStateExtraKey] = map[string]any{
		"0": string(SessionPersonaSlotStateDisabled),
		"1": string(SessionPersonaSlotStateDisabled),
	}
	if _, ok := ResolveSessionPersonaBindingForNewRoot(
		newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 1, 2)),
		account,
	); ok {
		t.Fatal("new-root resolver returned a disabled slot")
	}
}

func TestOpenAIPersonaSlotEnabledPresenceDistinguishesDrainFromLegacyDefault(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	if got := account.GetOpenAIPersonaSlotState(1); got != SessionPersonaSlotStateActive {
		t.Fatalf("missing enabled field state = %q, want active", got)
	}
	account.Extra[openAIPersonaSlotEnabledExtraKey] = map[string]any{"1": false}
	if !account.IsOpenAIPersonaSlotEnabledConfigured(1) {
		t.Fatal("explicit enabled=false was not detected as configured")
	}
	if got := account.GetOpenAIPersonaSlotState(1); got != SessionPersonaSlotStateDraining {
		t.Fatalf("explicit enabled=false state = %q, want draining", got)
	}
	if account.GetOpenAIPersonaSlotEnabled(1) {
		t.Fatal("draining slot must not be enabled for new roots")
	}
}

func TestResolveSessionPersonaBindingForNewRootUsesOpenCodeWhenReady(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	binding, ok := ResolveSessionPersonaBindingForNewRoot(
		newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 1, 2)),
		account,
	)
	if !ok {
		t.Fatal("new-root resolver rejected ready OpenCode slot")
	}
	if binding.PersonaID != SessionPersonaOpenCode || binding.SlotID != 1 {
		t.Fatalf("binding = %+v, want OpenCode slot 1", binding)
	}
	if binding.CredentialChainID != "opencode-chain" {
		t.Fatalf("credential chain = %q, want opencode-chain", binding.CredentialChainID)
	}
	if binding.CompatibilityFallback || binding.Mapping != SessionPersonaMappingPersonaV3 {
		t.Fatalf("ready OpenCode binding unexpectedly marked fallback: %+v", binding)
	}
}

func TestResolveSessionPersonaBindingForNewRootCodexClientPrefersStrictCodex(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	account.Extra[openAIPersonaMappingEnabledExtraKey] = true
	c := newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 1, 2))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.149.0 (Linux; x86_64)")
	c.Request.Header.Set("originator", "codex_cli_rs")

	binding, ok := ResolveSessionPersonaBindingForNewRoot(c, account)
	if !ok {
		t.Fatal("Codex new-root resolver rejected an eligible strict Codex slot")
	}
	if binding.PersonaID != SessionPersonaCodexCLIStrict || binding.SlotID != 0 {
		t.Fatalf("Codex binding = %+v, want strict Codex slot 0", binding)
	}
	if binding.CompatibilityFallback || binding.Mapping != SessionPersonaMappingPersonaV3 {
		t.Fatalf("Codex preference was incorrectly marked as compatibility fallback: %+v", binding)
	}
}

func TestResolveSessionPersonaBindingForNewRootCodexPreferenceFallsBackToExistingOrder(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	account.Extra[openAIPersonaMappingEnabledExtraKey] = true
	account.Extra[openAIPersonaSlotStateExtraKey] = map[string]any{
		"0": string(SessionPersonaSlotStateDraining),
	}
	c := newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 1, 2))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.149.0 (Linux; x86_64)")
	c.Request.Header.Set("originator", "codex_cli_rs")

	binding, ok := ResolveSessionPersonaBindingForNewRoot(c, account)
	if !ok {
		t.Fatal("Codex resolver did not fall back after strict Codex became unavailable")
	}
	if binding.PersonaID != SessionPersonaOpenCode || binding.SlotID != 1 {
		t.Fatalf("fallback binding = %+v, want OpenCode slot 1", binding)
	}
}

func TestResolveSessionPersonaBindingForNewRootNonCodexPrefersOpenCode(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	account.Extra[openAIPersonaMappingEnabledExtraKey] = true
	c := newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 0, 2))

	binding, ok := ResolveSessionPersonaBindingForNewRoot(c, account)
	if !ok {
		t.Fatal("non-Codex new-root resolver rejected an eligible OpenCode slot")
	}
	if binding.PersonaID != SessionPersonaOpenCode || binding.SlotID != 1 {
		t.Fatalf("non-Codex binding = %+v, want OpenCode slot 1", binding)
	}
}

func TestResolveSessionPersonaBindingForNewRootKeepsOpenCodeWhenSlotZeroDisabled(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	account.Extra[openAIPersonaSlotStateExtraKey] = map[string]any{
		"0": string(SessionPersonaSlotStateDisabled),
	}

	binding, ok := ResolveSessionPersonaBindingForNewRoot(
		newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 0, 2)),
		account,
	)
	if !ok {
		t.Fatal("new-root resolver rejected the remaining active OpenCode slot")
	}
	if binding.PersonaID != SessionPersonaOpenCode || binding.SlotID != 1 {
		t.Fatalf("binding = %+v, want OpenCode slot 1", binding)
	}
	if binding.CompatibilityFallback || binding.Mapping != SessionPersonaMappingPersonaV3 || !binding.AcceptsNewRoot() {
		t.Fatalf("OpenCode slot was incorrectly treated as compatibility fallback: %+v", binding)
	}
}

func TestResolveSessionPersonaBindingForNewRootDoesNotRouteOpenCodeToUnreadyWSAdapter(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	account.Extra[openAIPersonaMappingEnabledExtraKey] = true
	credentials, ok := account.Credentials[openAIPersonaCredentialsKey].([]any)
	if !ok {
		t.Fatal("OpenAI persona credentials fixture has an unexpected type")
	}
	account.Credentials[openAIPersonaCredentialsKey] = append(credentials, map[string]any{
		"persona_id":          string(SessionPersonaCodexCLIStrict),
		"slot_id":             0,
		"credential_chain_id": "codex-chain",
		"access_token":        "codex-access",
		"refresh_token":       "codex-refresh",
		"ready":               true,
		"state":               "ready",
	})

	c := newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionV3, 1, 2))
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)
	binding, ok := ResolveSessionPersonaBindingForNewRoot(c, account)
	if !ok {
		t.Fatal("WS new-root resolver did not fall back to strict Codex slot 0")
	}
	if binding.PersonaID != SessionPersonaCodexCLIStrict || binding.SlotID != 0 {
		t.Fatalf("WS binding = %+v, want strict Codex slot 0", binding)
	}
	if !binding.CompatibilityFallback || binding.Mapping != SessionPersonaMappingCompatibility {
		t.Fatalf("WS fallback provenance was not recorded: %+v", binding)
	}

	account.Extra[openAIPersonaSlotStateExtraKey] = map[string]any{"0": string(SessionPersonaSlotStateDisabled)}
	if _, ok := ResolveSessionPersonaBindingForNewRoot(c, account); ok {
		t.Fatal("WS new-root resolver selected OpenCode while its WS adapter is unavailable")
	}
}

func TestResolveSessionPersonaBindingForNewRootKeepsLegacyV2PairCompatible(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	binding, ok := ResolveSessionPersonaBindingForNewRoot(
		newSessionPersonaResolverTestContext(newSessionPersonaResolverIDs(SessionPersonaScopeVersionLegacyV2, 1, 2)),
		account,
	)
	if !ok {
		t.Fatal("legacy v2 slot 1 was rejected despite account-level Codex credentials")
	}
	if binding.PersonaID != SessionPersonaCodexCLIStrict || binding.SlotID != 1 || !binding.Legacy {
		t.Fatalf("legacy binding = %+v", binding)
	}
	if binding.CredentialChainID != "" {
		t.Fatalf("legacy slot 1 unexpectedly invented a chain ID: %q", binding.CredentialChainID)
	}
}

func TestResolveSessionPersonaBindingForExistingThreadPreservesDrainingBinding(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	account.Extra[openAIPersonaSlotStateExtraKey] = map[string]any{"1": string(SessionPersonaSlotStateDraining)}
	persisted := SessionPersonaSlotBinding{
		AccountID:         account.ID,
		SlotID:            1,
		SlotCount:         2,
		ScopeVersion:      SessionPersonaScopeVersionV3,
		MappingVersion:    SessionPersonaScopeVersionV3,
		PersonaID:         SessionPersonaOpenCode,
		CredentialChainID: "opencode-chain",
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      19,
		SlotGeneration:    3,
		SlotSetGeneration: 7,
		ClientThreadID:    "client-thread",
	}

	binding, ok := ResolveSessionPersonaBindingForExistingThread(account, persisted)
	if !ok {
		t.Fatal("existing-thread resolver rejected a valid persisted binding")
	}
	if binding.PersonaID != persisted.PersonaID || binding.SlotID != persisted.SlotID ||
		binding.SessionEpoch != persisted.SessionEpoch || binding.CredentialChainID != persisted.CredentialChainID {
		t.Fatalf("existing binding crossed identity boundary: got=%+v persisted=%+v", binding, persisted)
	}
	if binding.State != SessionPersonaSlotStateDraining || !binding.KeepsExistingThread() {
		t.Fatalf("existing binding did not enter draining state: %+v", binding)
	}
	if binding.CompatibilityFallback {
		t.Fatalf("existing binding unexpectedly became a fallback: %+v", binding)
	}
}

func TestResolveSessionPersonaBindingForExistingThreadGenerationAndDisable(t *testing.T) {
	base := SessionPersonaSlotBinding{
		AccountID:         42,
		SlotID:            1,
		SlotCount:         2,
		ScopeVersion:      SessionPersonaScopeVersionV3,
		MappingVersion:    SessionPersonaScopeVersionV3,
		PersonaID:         SessionPersonaOpenCode,
		CredentialChainID: "opencode-chain",
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      19,
		SlotGeneration:    3,
		SlotSetGeneration: 7,
	}

	t.Run("new generation drains old thread", func(t *testing.T) {
		account := newSessionPersonaOAuthAccount()
		account.Extra[openAIPersonaSlotGenerationsKey] = map[string]any{"1": float64(4)}
		got, ok := ResolveSessionPersonaBindingForExistingThread(account, base)
		if !ok {
			t.Fatal("generation rollover rejected existing binding")
		}
		if got.State != SessionPersonaSlotStateDraining || got.SlotGeneration != base.SlotGeneration {
			t.Fatalf("old binding was not preserved as draining: %+v", got)
		}
	})

	t.Run("security disable is not active", func(t *testing.T) {
		account := newSessionPersonaOAuthAccount()
		account.Extra[openAIPersonaSlotStateExtraKey] = map[string]any{"1": string(SessionPersonaSlotStateDisabled)}
		got, ok := ResolveSessionPersonaBindingForExistingThread(account, base)
		if !ok {
			t.Fatal("hard-disabled existing binding was rejected instead of being returned for termination")
		}
		if got.State != SessionPersonaSlotStateDisabled || got.Enabled || got.AcceptsNewRoot() {
			t.Fatalf("disabled binding was represented as active: %+v", got)
		}
	})
}

func TestResolveSessionPersonaBindingForExistingThreadRejectsCrossAccountOrIncompleteV3(t *testing.T) {
	account := newSessionPersonaOAuthAccount()
	binding := SessionPersonaSlotBinding{
		AccountID:         99,
		SlotID:            1,
		SlotCount:         2,
		ScopeVersion:      SessionPersonaScopeVersionV3,
		MappingVersion:    SessionPersonaScopeVersionV3,
		PersonaID:         SessionPersonaOpenCode,
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      1,
		SlotGeneration:    1,
		SlotSetGeneration: 1,
	}
	if _, ok := ResolveSessionPersonaBindingForExistingThread(account, binding); ok {
		t.Fatal("existing binding from another account was accepted")
	}

	binding.AccountID = account.ID
	if _, ok := ResolveSessionPersonaBindingForExistingThread(account, binding); ok {
		t.Fatal("v3 binding without an explicit credential chain was accepted")
	}

	strict := binding
	strict.PersonaID = SessionPersonaCodexCLIStrict
	strict.SlotID = 0
	strict.CredentialChainID = ""
	if _, ok := ResolveSessionPersonaBindingForExistingThread(account, strict); !ok {
		t.Fatal("strict Codex v3 binding without a chain should preserve account-level OAuth compatibility")
	}

	strictSlot1 := strict
	strictSlot1.SlotID = 1
	if _, ok := ResolveSessionPersonaBindingForExistingThread(account, strictSlot1); ok {
		t.Fatal("strict Codex v3 non-zero slot without a chain must not borrow account-level OAuth")
	}
}

func TestFindPersonaCredentialRequiresExplicitPersonaSlotAndChain(t *testing.T) {
	valid := map[string]any{
		"persona_id":          string(SessionPersonaOpenCode),
		"slot_id":             1,
		"credential_chain_id": "chain-1",
		"access_token":        "access",
		"refresh_token":       "refresh",
	}
	if got := findPersonaCredentialInValue(valid, SessionPersonaOpenCode, 1); got == nil {
		t.Fatal("persona_id alias was not accepted")
	}

	personaAlias := map[string]any{
		"persona":             string(SessionPersonaOpenCode),
		"slot_id":             1,
		"credential_chain_id": "chain-2",
	}
	if got := findPersonaCredentialInValue(personaAlias, SessionPersonaOpenCode, 1); got == nil {
		t.Fatal("persona alias was not accepted")
	}

	invalid := []map[string]any{
		{"slot_id": 1, "credential_chain_id": "missing-persona"},
		{"persona_id": string(SessionPersonaOpenCode), "credential_chain_id": "missing-slot"},
		{"persona_id": string(SessionPersonaOpenCode), "slot_id": 1},
		{"persona": string(SessionPersonaOpenCode), "persona_id": string(SessionPersonaCodexCLIStrict), "slot_id": 1, "credential_chain_id": "conflict"},
	}
	for index, candidate := range invalid {
		if got := findPersonaCredentialInValue(candidate, SessionPersonaOpenCode, 1); got != nil {
			t.Fatalf("invalid candidate %d was accepted: %#v", index, got)
		}
	}

	account := newSessionPersonaOAuthAccount()
	account.Credentials[openAIPersonaCredentialsKey] = []any{map[string]any{
		"persona_id":          string(SessionPersonaOpenCode),
		"slot_id":             1,
		"credential_chain_id": "revoked-chain",
		"access_token":        "access",
		"refresh_token":       "refresh",
		"state":               "revoked",
	}}
	if account.HasOpenAIPersonaCredential(SessionPersonaOpenCode, 1) {
		t.Fatal("revoked Persona credential was treated as ready")
	}
}
