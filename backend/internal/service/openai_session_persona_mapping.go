package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// These keys are deliberately independent from codex_fingerprint scope_version.
	// The latter remains the v1/v2 storage/transport axis; these keys gate the
	// Account -> Persona mapping rollout for new roots.
	openAIPersonaMappingVersionExtraKey = "openai_persona_mapping_version"
	openAIPersonaMappingEnabledExtraKey = "openai_persona_mapping_enabled"
)

// GetOpenAIPersonaMappingVersion returns the explicitly configured Persona
// mapping version. Zero means the account is still on the legacy mapping path.
func (a *Account) GetOpenAIPersonaMappingVersion() int {
	if a == nil || a.Extra == nil {
		return 0
	}
	for _, key := range []string{
		openAIPersonaMappingVersionExtraKey,
		"openai_persona_scope_version",
		"persona_mapping_version",
	} {
		if raw, ok := a.Extra[key]; ok {
			value := parseSessionPersonaInt64(raw)
			if value > 0 && value <= int64(SessionPersonaScopeVersionV3) {
				return int(value)
			}
		}
	}
	return 0
}

// IsOpenAIPersonaMappingEnabled gates the v3 Account×Persona mapper. An
// explicit false always wins, which lets operators pause the canary without
// rewriting historical v2 rows. No implicit enablement is inferred merely
// from a newly imported credential chain.
func (a *Account) IsOpenAIPersonaMappingEnabled() bool {
	if a == nil || a.Extra == nil {
		return false
	}
	for _, key := range []string{
		openAIPersonaMappingEnabledExtraKey,
		"openai_persona_mapping_active",
	} {
		if raw, ok := a.Extra[key]; ok {
			if enabled, parsed := parseSessionPersonaBool(raw); parsed {
				return enabled
			}
		}
	}
	return a.GetOpenAIPersonaMappingVersion() >= SessionPersonaScopeVersionV3
}

// SessionPersonaMappingScopeKey returns the non-secret, deterministic scope
// key used by persistent client↔Persona ID mappings. CredentialChainID is part
// of the scope so an OAuth rotation cannot reuse an old OpenCode ID under a
// newly authorized chain.
func SessionPersonaMappingScopeKey(binding SessionPersonaSlotBinding) string {
	binding = binding.NormalizeLifecycle()
	seed := strings.Join([]string{
		"openai-persona-map:v3",
		formatInt64(binding.AccountID),
		string(binding.PersonaID),
		formatInt(binding.SlotID),
		formatInt64(binding.SessionEpoch),
		formatInt64(binding.SlotGeneration),
		formatInt64(binding.SlotSetGeneration),
		strings.TrimSpace(binding.CredentialChainID),
		strings.TrimSpace(binding.ClientThreadID),
	}, "|")
	digest := sha256.Sum256([]byte(seed))
	return "pm_" + hex.EncodeToString(digest[:16])
}

// SessionPersonaMappingKey is retained as the public compatibility name for
// callers that only need the v3 scope key.
func SessionPersonaMappingKey(binding SessionPersonaSlotBinding) string {
	return SessionPersonaMappingScopeKey(binding)
}

// GetOpenAIPersonaInstallationID returns the installation identity owned by a
// Persona/slot. A credential-chain-provided value wins; otherwise derive a
// stable app-specific installation ID from the account's converged device
// identity. The derivation intentionally includes Persona and slot so two
// applications on the same physical device can remain distinct installations
// while still sharing the account-level device group.
func (a *Account) GetOpenAIPersonaInstallationID(persona SessionPersonaID, slotID int, fallback string) string {
	if a == nil {
		return ""
	}
	if a.IsOpenAIPersonaMappingEnabled() && a.GetOpenAIPersonaMappingVersion() >= SessionPersonaScopeVersionV3 {
		if installationID := a.extraPersonaSlotString(OpenAIPersonaInstallationIDsExtraKey, slotID); installationID != "" {
			return installationID
		}
	}
	if chain := a.findPersonaCredential(persona, slotID); chain != nil {
		if installationID := strings.TrimSpace(openAIMapString(chain, "installation_id")); installationID != "" {
			return installationID
		}
	}
	deviceID := strings.TrimSpace(a.GetOpenAIDeviceID())
	if deviceID == "" {
		deviceID = strings.TrimSpace(fallback)
	}
	if deviceID == "" {
		deviceID = formatInt64(a.ID)
	}
	chainID := strings.TrimSpace(a.GetOpenAIPersonaCredentialChainID(persona, slotID))
	seed := strings.Join([]string{
		"openai-persona-installation:v1",
		formatInt64(a.ID),
		deviceID,
		string(persona),
		formatInt(slotID),
		chainID,
	}, "|")
	digest := sha256.Sum256([]byte(seed))
	return "inst_" + hex.EncodeToString(digest[:16])
}

// SessionPersonaRequestHasContinuation identifies requests that must not be
// treated as new roots when no durable Persona binding has been loaded. The
// detector is intentionally conservative: a false positive keeps the legacy
// strict-Codex path, while a false negative could cross Persona state.
func SessionPersonaRequestHasContinuation(c *gin.Context, body []byte) bool {
	if c != nil && c.Request != nil {
		for _, name := range []string{
			"previous_response_id",
			"previous-response-id",
			"x-codex-parent-thread-id",
			"x-codex-forked-from-thread-id",
			"parent-thread-id",
			"forked-from-thread-id",
		} {
			if strings.TrimSpace(c.Request.Header.Get(name)) != "" {
				return true
			}
		}
	}
	if len(body) == 0 {
		return false
	}
	var payload any
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	return sessionPersonaValueHasContinuation(payload, 0)
}

func sessionPersonaValueHasContinuation(value any, depth int) bool {
	if depth > 8 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"))
			switch normalized {
			case "previous_response_id", "x_codex_parent_thread_id",
				"parent_thread_id", "forked_from_thread_id", "parent_turn_id", "root_turn_id",
				"continuation", "resume", "resume_from":
				if sessionPersonaValueIsNonEmpty(child) {
					return true
				}
			}
			if sessionPersonaValueHasContinuation(child, depth+1) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if sessionPersonaValueHasContinuation(child, depth+1) {
				return true
			}
		}
	}
	return false
}

func sessionPersonaValueIsNonEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case bool:
		return typed
	case float64:
		return typed != 0
	default:
		return true
	}
}

func formatInt(value int) string {
	return strconv.Itoa(value)
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}
