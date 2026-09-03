package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	// OpenCodeResponsesWebSocketProtocolHeader is pinned from the selected
	// OpenCode release and must not be inherited from Codex WS headers.
	OpenCodeResponsesWebSocketProtocolHeader = "responses_websockets=2026-02-06"
	openCodePersonaSessionIDHeader           = "session-id"
	// The OpenCode source-compatible Persona currently advertises HTTP only.
	// Keep the WS adapter implementation behind a readiness gate until the
	// source contract and dedicated upstream pool are both enabled; requests
	// fail closed instead of borrowing the strict Codex bridge.
	openCodeWebSocketAdapterEnabled = false
)

// ErrOpenCodePersonaWebSocketUnavailable identifies a disabled OpenCode WS
// rollout. Existing OpenCode Threads must fail closed rather than being
// silently remapped to the strict Codex Persona.
var ErrOpenCodePersonaWebSocketUnavailable = errors.New("OpenCode Persona WebSocket adapter is not enabled")

// ErrOpenCodePersonaContinuationUnsupported identifies a continuation that
// cannot yet be translated without the durable client↔OpenCode ID mapper.
var ErrOpenCodePersonaContinuationUnsupported = errors.New("OpenCode Persona continuation is not supported")

// OpenCodePersonaTransportReady reports whether the gateway has a complete
// adapter for the requested upstream transport. The registry may describe the
// source protocol's WS capability before this rollout switch is enabled;
// routing must use this stricter readiness check.
func OpenCodePersonaTransportReady(transport SessionPersonaTransport) bool {
	switch transport {
	case SessionPersonaTransportHTTP:
		return true
	case SessionPersonaTransportWS:
		return openCodeWebSocketAdapterEnabled
	default:
		return false
	}
}

// IsOpenCodePersona reports whether a binding selects the OpenCode adapter.
func IsOpenCodePersona(binding SessionPersonaSlotBinding) bool {
	return binding.PersonaID == SessionPersonaOpenCode
}

// EffectiveOpenCodeSessionID returns an opaque upstream session ID. It never
// forwards a client Thread/Session ID verbatim and remains stable for a fixed
// slot generation and Thread binding.
func EffectiveOpenCodeSessionID(binding SessionPersonaSlotBinding) string {
	if value := strings.TrimSpace(binding.UpstreamSessionID); value != "" {
		return value
	}
	seed := fmt.Sprintf("%d|%s|%d|%d|%d|%d|%s",
		binding.AccountID,
		binding.PersonaID,
		binding.SlotID,
		binding.SlotGeneration,
		binding.SlotSetGeneration,
		binding.SessionEpoch,
		strings.TrimSpace(binding.ClientThreadID),
	)
	digest := sha256.Sum256([]byte(seed))
	return "oc_" + hex.EncodeToString(digest[:16])
}

// ApplyOpenCodeOutboundHeaders applies only headers owned by the OpenCode
// Persona. Callers should add Bearer and ChatGPT-Account-Id separately from
// the credential chain; this helper intentionally never handles secrets.
func ApplyOpenCodeOutboundHeaders(headers http.Header, binding SessionPersonaSlotBinding, platform, release, arch string) {
	applyOpenCodeOutboundHeaders(headers, binding, SessionPersonaTransportHTTP, platform, release, arch)
}

// ApplyOpenCodeWebSocketHeaders applies the OpenCode identity and pinned WS
// protocol header for an upstream WebSocket handshake.
func ApplyOpenCodeWebSocketHeaders(headers http.Header, binding SessionPersonaSlotBinding, platform, release, arch string) {
	applyOpenCodeOutboundHeaders(headers, binding, SessionPersonaTransportWS, platform, release, arch)
}

func applyOpenCodeOutboundHeaders(headers http.Header, binding SessionPersonaSlotBinding, transport SessionPersonaTransport, platform, release, arch string) {
	if headers == nil || !IsOpenCodePersona(binding) {
		return
	}
	clearOpenCodeInheritedIdentityHeaders(headers)
	binding = binding.NormalizeLifecycle()
	persona := binding.Persona
	if !persona.Valid() {
		persona, _ = ResolveDefaultSessionPersona(binding.SlotID)
	}
	ua := persona.BuildUserAgent(platform, release, arch)
	if strings.TrimSpace(ua) != "" {
		headers.Set("User-Agent", ua)
	}
	headers.Set("originator", "opencode")
	sessionID := EffectiveOpenCodeSessionID(binding)
	// OpenCode's two OpenAI request layers use both spellings: the Codex
	// plugin uses `session-id`, while the generic Responses request builder
	// sends `X-Session-Id`. They must carry the same opaque Persona session,
	// never a client Thread/Session identifier.
	headers.Set(openCodePersonaSessionIDHeader, sessionID)
	headers.Set("X-Session-Id", sessionID)
	// OpenCode's WS pool uses this affinity key when present. Keeping it opaque
	// avoids coupling the upstream session key to the client protocol IDs.
	headers.Set("x-session-affinity", sessionID)
	if transport == SessionPersonaTransportWS {
		headers.Set("OpenAI-Beta", OpenCodeResponsesWebSocketProtocolHeader)
	}
}

// clearOpenCodeInheritedIdentityHeaders removes every known Codex identity
// surface before the OpenCode adapter writes its own values. Header names are
// matched case-insensitively because the forwarding whitelist may preserve the
// client's original spelling and may therefore leave multiple map keys.
func clearOpenCodeInheritedIdentityHeaders(headers http.Header) {
	for key := range headers {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(lowerKey, "x-codex-") || isOpenCodeInheritedIdentityHeader(lowerKey) {
			delete(headers, key)
		}
	}
}

func isOpenCodeInheritedIdentityHeader(lowerKey string) bool {
	switch lowerKey {
	case "x-openai-subagent",
		"x-openai-internal-codex-responses-lite",
		"session-id",
		"session_id",
		"thread-id",
		"thread_id",
		"conversation-id",
		"conversation_id",
		"x-client-request-id",
		"originator",
		"version",
		"openai-beta",
		"x-session-id",
		"x-session-affinity":
		return true
	default:
		return false
	}
}

// PrepareOpenCodeOutboundBody projects the canonical Responses body onto the
// OpenCode wire contract. It strips Codex-only identity/transport metadata and
// does not copy client session IDs or turn-state blobs.
func PrepareOpenCodeOutboundBody(body []byte, transport SessionPersonaTransport, compact bool) ([]byte, error) {
	return prepareOpenCodeOutboundBody(body, transport, compact, false)
}

// PrepareOpenCodeOutboundBodyWithMappedContinuation is used only after the
// gateway has resolved a client previous_response_id through the durable
// Account×Persona×Slot×Epoch×Credential Chain mapper. The mapped ID may cross
// the OpenCode wire; an untrusted client continuation may not.
func PrepareOpenCodeOutboundBodyWithMappedContinuation(body []byte, transport SessionPersonaTransport, compact bool) ([]byte, error) {
	return prepareOpenCodeOutboundBody(body, transport, compact, true)
}

func prepareOpenCodeOutboundBody(body []byte, transport SessionPersonaTransport, compact, mappedContinuation bool) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode OpenCode outbound body: %w", err)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	// The canonical request may still carry a Codex previous_response_id or
	// another continuation marker. OpenCode IDs are deliberately separate, and
	// forwarding the raw value would cross Persona state, so fail closed until a
	// durable mapping/compaction checkpoint is available.
	if sessionPersonaValueHasContinuation(payload, 0) && !mappedContinuation {
		return nil, ErrOpenCodePersonaContinuationUnsupported
	}
	for _, key := range []string{
		"client_metadata",
		"x-codex-turn-state",
		"x_codex_turn_state",
		"x-codex-turn-metadata",
		"x_codex_turn_metadata",
		"x-openai-subagent",
		"x_openai_subagent",
		"codex_session_id",
		"codex_thread_id",
		"conversation_id",
		"session_id",
		"generate",
	} {
		delete(payload, key)
	}
	// `type=response.create` belongs only to the OpenCode WebSocket envelope.
	// Remove any stale value before selecting the transport-specific shape so a
	// request that passed through a WS/canonical path cannot leak WS metadata
	// into HTTP or compact requests.
	delete(payload, "type")
	// OpenCode sends a Responses request over HTTP with stream=true and wraps
	// WS payloads in response.create. Compact remains unary JSON.
	if compact {
		payload["stream"] = false
	} else if transport == SessionPersonaTransportWS {
		payload["type"] = "response.create"
		payload["stream"] = true
	} else {
		payload["stream"] = true
	}
	// OpenCode's lowerer uses instructions as the system channel. If the
	// canonical request carries the legacy system field, move it once here.
	if _, hasInstructions := payload["instructions"]; !hasInstructions {
		if system, ok := payload["system"]; ok {
			payload["instructions"] = system
		}
	}
	// `system` is a Codex/canonical compatibility field. Once it has been
	// lowered (or when instructions already existed), never leak it to the
	// OpenCode wire shape.
	delete(payload, "system")
	if store, ok := payload["store"].(bool); ok && !store {
		stripOpenCodeReasoningItems(payload)
	}
	return json.Marshal(payload)
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

func stripOpenCodeReasoningItems(payload map[string]any) {
	input, ok := payload["input"].([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(input))
	for _, item := range input {
		object, ok := item.(map[string]any)
		if ok && strings.EqualFold(fmt.Sprint(object["type"]), "reasoning") {
			continue
		}
		filtered = append(filtered, item)
	}
	payload["input"] = filtered
}
