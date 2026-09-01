package service

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
)

const (
	openAIPersonaCredentialsKey       = "persona_credentials"
	openAIOAuthCredentialChainsKey    = "oauth_credential_chains"
	openAIPersonaActiveChainsKey      = "openai_persona_slot_active_chain_ids"
	openAIPersonaSlotStateExtraKey    = "openai_persona_slot_states"
	openAIPersonaSlotEnabledExtraKey  = "openai_persona_slot_enabled"
	openAIPersonaSlotGenerationsKey   = "openai_persona_slot_generations"
	openAIPersonaSlotSetGenerationKey = "openai_persona_slot_set_generation"
)

var (
	// ErrSessionPersonaNewRootUnavailable indicates that no active, authorized
	// slot can accept a new root request. Callers must not silently remap an
	// existing Thread when this occurs.
	ErrSessionPersonaNewRootUnavailable = fmt.Errorf("no active authorized session persona slot")
	// ErrSessionPersonaExistingBindingInvalid indicates that a persisted Thread
	// binding is incomplete or belongs to another account.
	ErrSessionPersonaExistingBindingInvalid = fmt.Errorf("invalid existing session persona binding")
)

// ResolveSessionPersonaBindingForRequest is the compatibility entry point for
// new-root requests. Existing Thread continuations must call
// ResolveSessionPersonaBindingForExistingThread with their persisted binding.
// Keeping the distinction at the API boundary prevents a draining slot from
// being accidentally remapped to another Persona.
func ResolveSessionPersonaBindingForRequest(c *gin.Context, account *Account) (SessionPersonaSlotBinding, bool) {
	return ResolveSessionPersonaBindingForNewRoot(c, account)
}

// ResolveSessionPersonaBindingForAdminProbe 严格解析管理员显式选择的 v3 slot。
// 该入口不枚举候选、不回退 Persona，也不建立 Thread/response ID 映射。
func ResolveSessionPersonaBindingForAdminProbe(account *Account, slotID int) (SessionPersonaSlotBinding, error) {
	if account == nil || !account.IsOpenAIOAuth() {
		return SessionPersonaSlotBinding{}, fmt.Errorf("Persona slot probe only supports OpenAI OAuth accounts")
	}
	if !account.IsOpenAIPersonaMappingEnabled() || account.GetOpenAIPersonaMappingVersion() < SessionPersonaScopeVersionV3 {
		return SessionPersonaSlotBinding{}, fmt.Errorf("persona_slot_id is only supported by Persona v3 accounts")
	}
	if slotID < 0 || slotID >= DefaultSessionPersonaSlotCount {
		return SessionPersonaSlotBinding{}, fmt.Errorf("%w: slot=%d", ErrSessionPersonaInvalidSlot, slotID)
	}

	binding, err := NewDefaultSessionPersonaRegistry().ResolveSlot(
		SessionPersonaScopeVersionV3,
		slotID,
		DefaultSessionPersonaSlotCount,
	)
	if err != nil {
		return SessionPersonaSlotBinding{}, err
	}
	binding.AccountID = account.ID
	binding.SessionEpoch = account.CodexFingerprintEpoch
	binding.SlotGeneration = account.GetOpenAIPersonaSlotGeneration(slotID)
	binding.SlotSetGeneration = account.GetOpenAIPersonaSlotSetGeneration()
	binding.State = account.GetOpenAIPersonaSlotState(slotID)
	binding.Enabled = account.GetOpenAIPersonaSlotEnabled(slotID)
	binding.EnabledConfigured = account.IsOpenAIPersonaSlotEnabledConfigured(slotID)
	binding.Authorized = account.HasOpenAIPersonaCredential(binding.PersonaID, slotID)
	binding.CredentialChainID = account.GetOpenAIPersonaCredentialChainID(binding.PersonaID, slotID)
	binding.InstallationID = account.GetOpenAIPersonaInstallationID(binding.PersonaID, slotID, "")
	binding = binding.NormalizeLifecycle()
	binding.MappingKey = SessionPersonaMappingKey(binding)

	if !binding.AcceptsNewRoot() {
		return SessionPersonaSlotBinding{}, fmt.Errorf(
			"Persona slot %d is not available for a new probe (state=%s, enabled=%t, authorized=%t)",
			slotID,
			binding.State,
			binding.Enabled,
			binding.Authorized,
		)
	}
	if !sessionPersonaBindingHasSafeCredentialChain(binding) {
		return SessionPersonaSlotBinding{}, fmt.Errorf("Persona slot %d has no safe credential chain", slotID)
	}
	if IsOpenCodePersona(binding) && !OpenCodePersonaTransportReady(SessionPersonaTransportHTTP) {
		return SessionPersonaSlotBinding{}, fmt.Errorf("OpenCode Persona HTTP adapter is not enabled")
	}
	return binding, nil
}

// ResolveSessionPersonaBindingForNewRoot resolves a new root onto an active,
// authorized slot only. If the preferred OpenCode slot is not ready, strict
// Codex slot 0 is tried as a compatibility fallback, but its actual lifecycle
// state is preserved. No disabled or draining binding is returned as active.
func ResolveSessionPersonaBindingForNewRoot(c *gin.Context, account *Account) (SessionPersonaSlotBinding, bool) {
	if account == nil || !account.IsOpenAIOAuth() {
		return SessionPersonaSlotBinding{}, false
	}
	ids := stagedCodexFingerprintIDsForAccount(c, account)
	if ids == nil {
		return SessionPersonaSlotBinding{}, false
	}
	mappingEnabled := account.IsOpenAIPersonaMappingEnabled()

	preferredSlot := ids.sessionSlot
	if preferredSlot < 0 {
		return SessionPersonaSlotBinding{}, false
	}
	slotCount := ids.sessionSlotCount
	if slotCount < 1 {
		slotCount = DefaultSessionPersonaSlotCount
	}
	mappingVersion := ids.sessionScopeVersion
	if mappingEnabled {
		// Persona v3 is independent from the Codex fingerprint storage scope.
		// Existing v1/v2 rows remain the stable device/session input; only the
		// new-root slot projection changes to the fixed Codex/OpenCode pair.
		slotCount = DefaultSessionPersonaSlotCount
		preferredSlot %= slotCount
		mappingVersion = SessionPersonaScopeVersionV3
	}

	// Persona v3 has a client-family preference before falling back to the
	// fingerprint-selected order:
	//   - non-Codex clients prefer OpenCode slot 1;
	//   - official Codex clients prefer strict Codex slot 0 when it is eligible
	//     for a new root (active, authorized, and transport-ready).
	//
	// The candidate loop below is still the source of truth for lifecycle and
	// credential checks. If the preferred Persona is not eligible, selection
	// returns to the historical fingerprint/slot order without remapping an
	// existing Thread.
	candidateSlots := make([]int, 0, slotCount+1)
	codexClientPreferred := mappingEnabled && isCodexPersonaClientRequest(c)
	appendCandidate := func(slotID int) {
		if slotID < 0 || slotID >= slotCount {
			return
		}
		for _, existing := range candidateSlots {
			if existing == slotID {
				return
			}
		}
		candidateSlots = append(candidateSlots, slotID)
	}
	if mappingEnabled {
		if codexClientPreferred {
			appendCandidate(0)
		} else {
			appendCandidate(1)
		}
	}
	appendCandidate(preferredSlot)
	appendCandidate(0)
	for slotID := 0; slotID < slotCount; slotID++ {
		appendCandidate(slotID)
	}

	for _, slotID := range candidateSlots {
		binding, ok := resolveSessionPersonaBindingForSlot(account, ids, slotID, slotCount, mappingVersion)
		if !ok || !binding.AcceptsNewRoot() || !sessionPersonaBindingHasSafeCredentialChain(binding) {
			continue
		}
		// The source registry already describes OpenCode's WS contract, but the
		// gateway rollout is HTTP-first and the live WS bridge is still Codex
		// shaped. Never select slot 1 for a new WS root until the dedicated
		// OpenCode event/pool adapter is enabled; slot 0 remains the stable
		// compatibility anchor when available.
		if GetOpenAIClientTransport(c) == OpenAIClientTransportWS &&
			binding.PersonaID == SessionPersonaOpenCode &&
			!OpenCodePersonaTransportReady(SessionPersonaTransportWS) {
			continue
		}
		if mappingEnabled {
			if binding.Legacy || binding.CompatibilityFallback {
				continue
			}
			binding.ScopeVersion = SessionPersonaScopeVersionV3
			binding.MappingVersion = SessionPersonaScopeVersionV3
			binding.FingerprintScopeVersion = ids.sessionScopeVersion
			binding.ClientThreadID = strings.TrimSpace(ids.threadID)
			if binding.ClientThreadID == "" {
				binding.ClientThreadID = strings.TrimSpace(ids.sessionID)
			}
			binding.InstallationID = account.GetOpenAIPersonaInstallationID(binding.PersonaID, slotID, ids.installationID)
			binding.MappingKey = SessionPersonaMappingKey(binding)
		}
		// Only a strict Codex slot 0 selected after the preferred OpenCode
		// candidate is unavailable is a legacy compatibility fallback. A
		// healthy OpenCode slot must remain a first-class v3 mapping even when
		// fingerprint preference points at a disabled/unready slot 0.
		if binding.PersonaID == SessionPersonaCodexCLIStrict && binding.SlotID == 0 && preferredSlot != 0 && !codexClientPreferred {
			binding.CompatibilityFallback = true
			binding.Mapping = SessionPersonaMappingCompatibility
		}
		return binding, true
	}
	return SessionPersonaSlotBinding{}, false
}

// isCodexPersonaClientRequest identifies the official Codex client family for
// v3 new-root preference. Unknown, generic, and third-party clients are
// intentionally treated as non-Codex so they prefer OpenCode when available.
func isCodexPersonaClientRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	return openai.IsCodexOfficialClientByHeaders(
		c.GetHeader("User-Agent"),
		c.GetHeader("originator"),
	)
}

// ResolveSessionPersonaBindingForExistingThread validates and preserves a
// previously persisted Thread binding. It may return a draining binding (and
// even a disabled binding after a security hard-disable) so the caller can
// terminate or drain it without crossing Persona. It never falls back to
// another slot or Persona.
func ResolveSessionPersonaBindingForExistingThread(account *Account, persisted SessionPersonaSlotBinding) (SessionPersonaSlotBinding, bool) {
	binding, ok := normalizePersistedSessionPersonaBinding(persisted)
	if !ok {
		return SessionPersonaSlotBinding{}, false
	}
	if account == nil {
		return binding, true
	}
	if account.Platform != PlatformOpenAI || !account.IsOpenAIOAuth() {
		return SessionPersonaSlotBinding{}, false
	}
	if binding.AccountID != 0 && binding.AccountID != account.ID {
		return SessionPersonaSlotBinding{}, false
	}
	if binding.AccountID == 0 {
		// Old persisted bindings did not always carry account_id. Once the
		// account has already been selected, filling this non-secret field from
		// that authoritative selection is safe and preserves compatibility.
		binding.AccountID = account.ID
	}

	currentGeneration := account.GetOpenAIPersonaSlotGeneration(binding.SlotID)
	currentState := account.GetOpenAIPersonaSlotState(binding.SlotID)
	currentEnabled := account.GetOpenAIPersonaSlotEnabled(binding.SlotID)
	// A re-enabled slot has a new Session generation. The old Thread remains on
	// its original Session/epoch and is naturally draining, even if the current
	// slot is active for new roots.
	if currentGeneration > binding.SlotGeneration && binding.State == SessionPersonaSlotStateActive {
		binding.State = SessionPersonaSlotStateDraining
		binding.Enabled = currentEnabled
	}
	if currentState == SessionPersonaSlotStateDisabled {
		binding.State = SessionPersonaSlotStateDisabled
		binding.Enabled = false
	} else if currentState == SessionPersonaSlotStateDraining && binding.State != SessionPersonaSlotStateDisabled {
		binding.State = SessionPersonaSlotStateDraining
		binding.Enabled = currentEnabled
	} else if currentState == SessionPersonaSlotStateActive && !currentEnabled {
		// An explicitly disabled external switch paired with an active state is
		// an inconsistent legacy record. Treat it as normal draining instead of
		// escalating an ordinary disable into a security hard-disable.
		binding.State = SessionPersonaSlotStateDraining
		binding.Enabled = false
	}
	return binding.NormalizeLifecycle(), true
}

// PreserveExistingThreadSessionPersonaBinding is the account-independent
// helper for callers that already loaded a complete persisted binding.
func PreserveExistingThreadSessionPersonaBinding(persisted SessionPersonaSlotBinding) (SessionPersonaSlotBinding, bool) {
	return ResolveSessionPersonaBindingForExistingThread(nil, persisted)
}

func resolveSessionPersonaBindingForSlot(account *Account, ids *codexFingerprintIDs, slotID, slotCount, mappingVersion int) (SessionPersonaSlotBinding, bool) {
	if account == nil || ids == nil {
		return SessionPersonaSlotBinding{}, false
	}
	binding, err := NewDefaultSessionPersonaRegistry().ResolveSlot(
		mappingVersion,
		slotID,
		slotCount,
	)
	if err != nil {
		return SessionPersonaSlotBinding{}, false
	}
	binding.AccountID = account.ID
	binding.SessionEpoch = ids.sessionEpoch
	binding.SlotGeneration = account.GetOpenAIPersonaSlotGeneration(slotID)
	binding.SlotSetGeneration = account.GetOpenAIPersonaSlotSetGeneration()
	binding.State = account.GetOpenAIPersonaSlotState(slotID)
	binding.Enabled = account.GetOpenAIPersonaSlotEnabled(slotID)
	binding.EnabledConfigured = account.IsOpenAIPersonaSlotEnabledConfigured(slotID)
	binding.Authorized = account.HasOpenAIPersonaCredential(binding.PersonaID, slotID)
	if !binding.Authorized && binding.PersonaID == SessionPersonaCodexCLIStrict {
		// Existing dual-Codex accounts intentionally keep their account-level
		// OAuth row for the strict slot while v3 is rolled out. Preserve that
		// read-only compatibility (including v3 slot 0) without allowing an
		// OpenCode slot to borrow the Codex refresh chain.
		binding.Authorized = strings.TrimSpace(account.GetOpenAIAccessToken()) != "" &&
			strings.TrimSpace(account.GetOpenAIRefreshToken()) != ""
	}
	binding.CredentialChainID = account.GetOpenAIPersonaCredentialChainID(binding.PersonaID, slotID)
	return binding.NormalizeLifecycle(), true
}

func sessionPersonaBindingHasSafeCredentialChain(binding SessionPersonaSlotBinding) bool {
	if !binding.Authorized {
		return false
	}
	if binding.Legacy {
		return true
	}
	// Strict Codex v3 can temporarily read the historical account-level OAuth
	// row. Its token provider remains on the old account-scoped path until the
	// independent strict chain is persisted; OpenCode never gets this fallback.
	if binding.PersonaID == SessionPersonaCodexCLIStrict && binding.SlotID == 0 && strings.TrimSpace(binding.CredentialChainID) == "" {
		return true
	}
	return strings.TrimSpace(binding.CredentialChainID) != ""
}

func normalizePersistedSessionPersonaBinding(persisted SessionPersonaSlotBinding) (SessionPersonaSlotBinding, bool) {
	canonical, ok := ParseSessionPersonaID(string(persisted.PersonaID))
	if !ok {
		return SessionPersonaSlotBinding{}, false
	}
	persisted.PersonaID = canonical
	if persisted.Persona.ID != "" && persisted.Persona.ID != canonical {
		return SessionPersonaSlotBinding{}, false
	}
	if persisted.EffectiveMappingVersion() >= SessionPersonaScopeVersionV3 &&
		!persisted.Legacy &&
		(persisted.PersonaID != SessionPersonaCodexCLIStrict || persisted.SlotID != 0) &&
		strings.TrimSpace(persisted.CredentialChainID) == "" {
		return SessionPersonaSlotBinding{}, false
	}
	if persisted.MappingVersion == 0 && persisted.ScopeVersion > 0 {
		persisted.MappingVersion = persisted.ScopeVersion
	}
	if persisted.ScopeVersion == 0 && persisted.MappingVersion > 0 {
		persisted.ScopeVersion = persisted.MappingVersion
	}
	persisted = persisted.NormalizeLifecycle()
	if !persisted.Valid() {
		return SessionPersonaSlotBinding{}, false
	}
	if persisted.Persona.ID == "" {
		persona, found := NewDefaultSessionPersonaRegistry().Get(string(canonical))
		if !found {
			return SessionPersonaSlotBinding{}, false
		}
		persisted.Persona = persona
	}
	persisted.PersonaVersion = persisted.Persona.EffectiveVersion()
	if !persisted.Persona.Valid() {
		return SessionPersonaSlotBinding{}, false
	}
	return persisted, true
}

// GetOpenAIPersonaSlotState reads the internal lifecycle overlay. Missing or
// malformed legacy records remain active to avoid taking the only stable slot
// offline during migration.
func (a *Account) GetOpenAIPersonaSlotState(slotID int) SessionPersonaSlotState {
	if a == nil || slotID < 0 {
		return SessionPersonaSlotStateDisabled
	}
	if state, ok := a.openAIPersonaSlotStateFromExtra(slotID); ok {
		return state
	}
	// Older external configuration only persisted enabled=false. Treat that as
	// a normal draining request when no explicit lifecycle state is available;
	// only an explicit internal disabled state is a security hard-disable.
	if raw, ok := a.extraPersonaSlotValue(openAIPersonaSlotEnabledExtraKey, slotID); ok {
		if enabled, parsed := parseSessionPersonaBool(raw); parsed && !enabled {
			return SessionPersonaSlotStateDraining
		}
	}
	return SessionPersonaSlotStateActive
}

// GetOpenAIPersonaSlotEnabled reads the external compatibility switch. The
// external switch remains a compatibility gate for new roots. A draining
// slot may still serve existing Threads even when enabled=false.
func (a *Account) GetOpenAIPersonaSlotEnabled(slotID int) bool {
	if a == nil || slotID < 0 {
		return false
	}
	if state, ok := a.openAIPersonaSlotStateFromExtra(slotID); ok && state == SessionPersonaSlotStateDisabled {
		return false
	}
	if raw, ok := a.extraPersonaSlotValue(openAIPersonaSlotEnabledExtraKey, slotID); ok {
		if enabled, parsed := parseSessionPersonaBool(raw); parsed {
			return enabled
		}
	}
	return a.GetOpenAIPersonaSlotState(slotID) == SessionPersonaSlotStateActive
}

// IsOpenAIPersonaSlotEnabledConfigured reports whether the compatibility
// enabled switch was explicitly persisted for this slot. It prevents a
// historical bool zero value from being mistaken for an operator disable.
func (a *Account) IsOpenAIPersonaSlotEnabledConfigured(slotID int) bool {
	if a == nil || slotID < 0 {
		return false
	}
	_, ok := a.extraPersonaSlotValue(openAIPersonaSlotEnabledExtraKey, slotID)
	return ok
}

func (a *Account) openAIPersonaSlotStateFromExtra(slotID int) (SessionPersonaSlotState, bool) {
	if a == nil || slotID < 0 {
		return SessionPersonaSlotStateDisabled, false
	}
	raw, ok := a.extraPersonaSlotValue(openAIPersonaSlotStateExtraKey, slotID)
	if !ok {
		return SessionPersonaSlotStateDisabled, false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(SessionPersonaSlotStateDraining):
		return SessionPersonaSlotStateDraining, true
	case string(SessionPersonaSlotStateDisabled):
		return SessionPersonaSlotStateDisabled, true
	case string(SessionPersonaSlotStateActive):
		return SessionPersonaSlotStateActive, true
	default:
		return SessionPersonaSlotStateDisabled, false
	}
}

// GetOpenAIPersonaSlotGeneration returns a monotonic slot generation. The
// compatibility default is 1; enabling code must persist the increment before
// creating a new Session.
func (a *Account) GetOpenAIPersonaSlotGeneration(slotID int) int64 {
	if value := a.extraPersonaSlotInt64(openAIPersonaSlotGenerationsKey, slotID); value > 0 {
		return value
	}
	return 1
}

// GetOpenAIPersonaSlotSetGeneration returns the collection generation used in
// new-root mapping and audit records.
func (a *Account) GetOpenAIPersonaSlotSetGeneration() int64 {
	if a == nil || a.Extra == nil {
		return 1
	}
	if value := parseSessionPersonaInt64(a.Extra[openAIPersonaSlotSetGenerationKey]); value > 0 {
		return value
	}
	return 1
}

// GetOpenAIPersonaCredentialChainID returns the chain ID without exposing token
// material. Strict Codex keeps a stable legacy chain until its independent
// v3 credential row is provisioned; OpenCode has no implicit fallback chain.
func (a *Account) GetOpenAIPersonaCredentialChainID(persona SessionPersonaID, slotID int) string {
	if a == nil {
		return ""
	}
	if chain := a.findPersonaCredential(persona, slotID); chain != nil {
		return strings.TrimSpace(openAIMapString(chain, "credential_chain_id"))
	}
	if persona == SessionPersonaCodexCLIStrict && a.IsOpenAIOAuth() && slotID == 0 {
		return "legacy-codex"
	}
	return ""
}

// HasOpenAIPersonaCredential checks readiness for one Persona/slot. It never
// copies or borrows refresh tokens across Persona boundaries.
func (a *Account) HasOpenAIPersonaCredential(persona SessionPersonaID, slotID int) bool {
	if a == nil || !a.IsOpenAIOAuth() {
		return false
	}
	chain := a.findPersonaCredential(persona, slotID)
	if chain != nil {
		// Explicit Persona chains take precedence over the legacy account-level
		// row. A malformed or non-ready chain must not silently borrow strict's
		// top-level refresh token, otherwise an OAuth rotation can cross Persona.
		if ready, ok := openAIMapBool(chain, "ready"); ok && !ready {
			return false
		}
		if state := strings.ToLower(strings.TrimSpace(openAIMapString(chain, "state"))); state != "" && state != "ready" {
			return false
		}
		if chainPersona := strings.TrimSpace(openAIMapString(chain, "persona")); chainPersona != "" {
			parsedPersona, ok := ParseSessionPersonaID(chainPersona)
			if !ok || parsedPersona != persona {
				return false
			}
		}
		if chainAccountID := strings.TrimSpace(openAIMapString(chain, "chatgpt_account_id")); chainAccountID != "" {
			accountID := strings.TrimSpace(a.GetChatGPTAccountID())
			if accountID != "" && chainAccountID != accountID {
				return false
			}
		}
		return strings.TrimSpace(openAIMapString(chain, "access_token")) != "" &&
			strings.TrimSpace(openAIMapString(chain, "refresh_token")) != "" &&
			strings.TrimSpace(openAIMapString(chain, "credential_chain_id")) != ""
	}
	if persona == SessionPersonaCodexCLIStrict && slotID == 0 {
		// Legacy v1/v2 and the initial strict slot 0 migration may still use the
		// account-level OAuth row. This fallback is intentionally unavailable to
		// OpenCode and to strict non-zero slots.
		return strings.TrimSpace(a.GetOpenAIAccessToken()) != "" && strings.TrimSpace(a.GetOpenAIRefreshToken()) != ""
	}
	return false
}

func (a *Account) findPersonaCredential(persona SessionPersonaID, slotID int) map[string]any {
	if a == nil || a.Credentials == nil {
		return nil
	}
	if chainID := a.openAIPersonaActiveCredentialChainID(slotID); chainID != "" {
		// An explicit pointer is authoritative. If it is stale or malformed,
		// fail closed instead of selecting an older chain by map iteration.
		return a.findPersonaCredentialByChainID(persona, slotID, chainID)
	}
	for _, key := range []string{openAIPersonaCredentialsKey, openAIOAuthCredentialChainsKey} {
		raw, ok := a.Credentials[key]
		if !ok {
			continue
		}
		if found := findPersonaCredentialInValue(raw, persona, slotID); found != nil {
			return found
		}
	}
	return nil
}

func (a *Account) openAIPersonaActiveCredentialChainID(slotID int) string {
	if a == nil || a.Credentials == nil || slotID < 0 {
		return ""
	}
	raw, ok := a.Credentials[openAIPersonaActiveChainsKey]
	if !ok {
		return ""
	}
	key := strconv.Itoa(slotID)
	switch value := raw.(type) {
	case map[string]any:
		return strings.TrimSpace(openAIMapString(value, key))
	case map[string]string:
		return strings.TrimSpace(value[key])
	case string:
		var decoded map[string]any
		if json.Unmarshal([]byte(value), &decoded) == nil {
			return strings.TrimSpace(openAIMapString(decoded, key))
		}
	case json.RawMessage:
		var decoded map[string]any
		if json.Unmarshal(value, &decoded) == nil {
			return strings.TrimSpace(openAIMapString(decoded, key))
		}
	}
	return ""
}

func findPersonaCredentialInValue(raw any, persona SessionPersonaID, slotID int) map[string]any {
	canonical, ok := ParseSessionPersonaID(string(persona))
	if !ok || slotID < 0 {
		return nil
	}
	return findPersonaCredentialInValueStrict(raw, canonical, slotID)
}

// findPersonaCredentialInValueStrict accepts both persona and persona_id as
// input aliases, but requires an explicit persona, slot_id, and non-empty
// credential_chain_id on every matching leaf. Container keys are not treated
// as identity metadata, preventing an incomplete object from silently becoming
// slot 0's legacy credential.
func findPersonaCredentialInValueStrict(raw any, canonical SessionPersonaID, slotID int) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		if chainID, hasChain := credentialChainIDFromMap(value); hasChain {
			candidatePersona, hasPersona := credentialPersonaFromMap(value)
			candidateSlot, hasSlot := credentialSlotIDFromMap(value)
			if hasPersona && hasSlot && candidatePersona == canonical && candidateSlot == slotID && chainID != "" {
				return value
			}
		}
		for _, item := range value {
			if found := findPersonaCredentialInValueStrict(item, canonical, slotID); found != nil {
				return found
			}
		}
	case map[string]string:
		converted := make(map[string]any, len(value))
		for key, item := range value {
			converted[key] = item
		}
		return findPersonaCredentialInValueStrict(converted, canonical, slotID)
	case map[int]any:
		for _, item := range value {
			if found := findPersonaCredentialInValueStrict(item, canonical, slotID); found != nil {
				return found
			}
		}
	case map[int]string:
		for _, item := range value {
			if found := findPersonaCredentialInValueStrict(item, canonical, slotID); found != nil {
				return found
			}
		}
	case []any:
		for _, item := range value {
			if found := findPersonaCredentialInValueStrict(item, canonical, slotID); found != nil {
				return found
			}
		}
	case string:
		// JSON-encoded nested maps can occur in imported credentials.
		trimmed := strings.TrimSpace(value)
		if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var decoded any
			if json.Unmarshal([]byte(trimmed), &decoded) == nil {
				return findPersonaCredentialInValueStrict(decoded, canonical, slotID)
			}
		}
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			return findPersonaCredentialInValueStrict(decoded, canonical, slotID)
		}
	}
	return nil
}

func credentialChainIDFromMap(value map[string]any) (string, bool) {
	raw, ok := value["credential_chain_id"]
	if !ok || raw == nil {
		return "", false
	}
	chainID, ok := raw.(string)
	chainID = strings.TrimSpace(chainID)
	return chainID, ok && chainID != ""
}

func credentialPersonaFromMap(value map[string]any) (SessionPersonaID, bool) {
	var canonical SessionPersonaID
	found := false
	for _, key := range []string{"persona", "persona_id"} {
		raw, exists := value[key]
		if !exists || raw == nil {
			continue
		}
		text, ok := raw.(string)
		if !ok {
			return "", false
		}
		parsed, ok := ParseSessionPersonaID(text)
		if !ok {
			return "", false
		}
		if found && parsed != canonical {
			return "", false
		}
		canonical = parsed
		found = true
	}
	return canonical, found
}

func credentialSlotIDFromMap(value map[string]any) (int, bool) {
	raw, ok := value["slot_id"]
	if !ok || raw == nil {
		return 0, false
	}
	parsed, ok := parseStrictSessionPersonaInt64(raw)
	if !ok || parsed < 0 || parsed > int64(^uint(0)>>1) {
		return 0, false
	}
	return int(parsed), true
}

func parseStrictSessionPersonaInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		if uint64(typed) > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		if typed > uint64(^uint64(0)>>1) {
			return 0, false
		}
		return int64(typed), true
	case float64:
		if math.Trunc(typed) != typed || typed < math.MinInt64 || typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (a *Account) extraPersonaSlotValue(key string, slotID int) (string, bool) {
	if a == nil || a.Extra == nil {
		return "", false
	}
	raw, ok := a.Extra[key]
	if !ok {
		return "", false
	}
	if values, ok := raw.(map[string]any); ok {
		value, exists := values[fmt.Sprint(slotID)]
		return fmt.Sprint(value), exists
	}
	if values, ok := raw.(map[int]any); ok {
		value, exists := values[slotID]
		return fmt.Sprint(value), exists
	}
	if values, ok := raw.(map[string]string); ok {
		value, exists := values[fmt.Sprint(slotID)]
		return value, exists
	}
	if values, ok := raw.(map[int]string); ok {
		value, exists := values[slotID]
		return value, exists
	}
	return "", false
}

func parseSessionPersonaBool(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		return parsed != 0, err == nil
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	case int64:
		return typed != 0, true
	default:
		return false, false
	}
}

func (a *Account) extraPersonaSlotInt64(key string, slotID int) int64 {
	value, ok := a.extraPersonaSlotValue(key, slotID)
	if !ok {
		return 0
	}
	return parseSessionPersonaInt64(value)
}

func parseSessionPersonaInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case int32:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		var parsed int64
		_, _ = fmt.Sscan(strings.TrimSpace(typed), &parsed)
		return parsed
	default:
		return 0
	}
}

func openAIMapString(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	raw, ok := value[key]
	if !ok || raw == nil {
		return ""
	}
	switch typed := raw.(type) {
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func openAIMapBool(value map[string]any, key string) (bool, bool) {
	if value == nil {
		return false, false
	}
	parsed, ok := value[key].(bool)
	return parsed, ok
}
