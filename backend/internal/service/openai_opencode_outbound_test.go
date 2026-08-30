package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestPrepareOpenCodeOutboundBodyStripsCodexIdentity(t *testing.T) {
	body := []byte(`{"model":"gpt-5","system":"be concise","session_id":"client-session","client_metadata":{"session_id":"client-thread"},"x-codex-turn-metadata":"secret","store":false,"input":[{"type":"reasoning","id":"r1"},{"type":"message","role":"user","content":"hi"}]}`)
	projected, err := PrepareOpenCodeOutboundBody(body, SessionPersonaTransportHTTP, false)
	if err != nil {
		t.Fatalf("PrepareOpenCodeOutboundBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(projected, &got); err != nil {
		t.Fatalf("decode projected body: %v", err)
	}
	for _, key := range []string{"session_id", "client_metadata", "x-codex-turn-metadata", "system"} {
		if _, ok := got[key]; ok {
			t.Fatalf("Codex field %q leaked into OpenCode body: %s", key, projected)
		}
	}
	if got["instructions"] != "be concise" {
		t.Fatalf("system prompt was not lowered to instructions: %#v", got["instructions"])
	}
	if got["stream"] != true {
		t.Fatalf("HTTP OpenCode body stream = %#v, want true", got["stream"])
	}
	input, _ := got["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("store=false reasoning item was not filtered: %#v", got["input"])
	}
}

func TestPrepareOpenCodeOutboundBodyWSEnvelope(t *testing.T) {
	projected, err := PrepareOpenCodeOutboundBody([]byte(`{"model":"gpt-5","input":[]}`), SessionPersonaTransportWS, false)
	if err != nil {
		t.Fatalf("PrepareOpenCodeOutboundBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(projected, &got); err != nil {
		t.Fatalf("decode projected body: %v", err)
	}
	if got["type"] != "response.create" || got["stream"] != true {
		t.Fatalf("unexpected WS envelope: %#v", got)
	}
}

func TestApplyOpenCodeOutboundHeadersUsesOpaqueSessionAndNoCodexIdentity(t *testing.T) {
	binding := SessionPersonaSlotBinding{
		AccountID:         9,
		SlotID:            1,
		SlotCount:         2,
		ScopeVersion:      SessionPersonaScopeVersionV3,
		PersonaID:         SessionPersonaOpenCode,
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      3,
		SlotGeneration:    2,
		SlotSetGeneration: 4,
		ClientThreadID:    "client-thread-id",
	}
	// Resolve the registry snapshot explicitly so the binding mirrors the
	// request path's hydrated Persona metadata.
	persona, err := NewDefaultSessionPersonaRegistry().MustGet(string(SessionPersonaOpenCode))
	if err != nil {
		t.Fatal(err)
	}
	binding.Persona = persona
	headers := http.Header{}
	headers.Set("version", "codex")
	headers.Set("session_id", "client-session")
	headers.Set("session-id", "client-session-hyphen")
	headers.Set("thread-id", "client-thread")
	headers.Set("conversation_id", "client-conversation")
	headers.Set("x-client-request-id", "client-request")
	headers.Set("x-session-affinity", "client-affinity")
	headers.Set("x-codex-turn-state", "client-state")
	headers.Set("X-Codex-Installation-Id", "client-installation")
	headers.Set("x-codex-turn-metadata", `{"thread_id":"client-thread"}`)
	headers.Set("x-openai-subagent", "client-subagent")
	headers.Set("x-openai-internal-codex-responses-lite", "true")
	headers.Set("OpenAI-Beta", "responses=codex")
	ApplyOpenCodeOutboundHeaders(headers, binding, "linux", "6.8", "x86_64")
	if got := headers.Get("originator"); got != "opencode" {
		t.Fatalf("originator = %q", got)
	}
	if got := headers.Get("User-Agent"); !strings.HasPrefix(got, "opencode/"+SessionPersonaOpenCodeVersion) {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := headers.Get("session-id"); got == "" || got == "client-session" {
		t.Fatalf("session-id is not opaque: %q", got)
	}
	for _, key := range []string{
		"version",
		"session_id",
		"thread-id",
		"conversation_id",
		"x-client-request-id",
		"x-codex-turn-state",
		"x-codex-installation-id",
		"x-codex-turn-metadata",
		"x-openai-subagent",
		"x-openai-internal-codex-responses-lite",
		"OpenAI-Beta",
	} {
		if headers.Get(key) != "" {
			t.Fatalf("inherited identity header %q leaked: %#v", key, headers)
		}
	}
	if headers.Get("session-id") == "" {
		t.Fatal("OpenCode session-id was not set")
	}
	if headers.Get("X-Session-Id") != headers.Get("session-id") {
		t.Fatalf("OpenCode X-Session-Id does not match session-id: %#v", headers)
	}
	if headers.Get("x-session-affinity") != headers.Get("session-id") {
		t.Fatalf("session affinity does not match session-id: %#v", headers)
	}
}

func TestPrepareOpenCodeOutboundBodyDoesNotLeakLegacySystemWhenInstructionsExist(t *testing.T) {
	projected, err := PrepareOpenCodeOutboundBody(
		[]byte(`{"model":"gpt-5","instructions":"canonical","system":"legacy"}`),
		SessionPersonaTransportHTTP,
		false,
	)
	if err != nil {
		t.Fatalf("PrepareOpenCodeOutboundBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(projected, &got); err != nil {
		t.Fatalf("decode projected body: %v", err)
	}
	if _, ok := got["system"]; ok {
		t.Fatalf("legacy system field leaked when instructions already existed: %s", projected)
	}
	if got["instructions"] != "canonical" {
		t.Fatalf("canonical instructions changed: %#v", got["instructions"])
	}
}

func TestPrepareOpenCodeOutboundBodyHTTPRemovesWSEnvelopeType(t *testing.T) {
	projected, err := PrepareOpenCodeOutboundBody(
		[]byte(`{"model":"gpt-5","type":"response.create","input":[]}`),
		SessionPersonaTransportHTTP,
		false,
	)
	if err != nil {
		t.Fatalf("PrepareOpenCodeOutboundBody() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(projected, &got); err != nil {
		t.Fatalf("decode projected body: %v", err)
	}
	if _, ok := got["type"]; ok {
		t.Fatalf("WS envelope type leaked into HTTP body: %s", projected)
	}
}

func TestPrepareOpenCodeOutboundBodyRejectsCodexContinuation(t *testing.T) {
	for _, body := range []string{
		`{"model":"gpt-5","previous_response_id":"resp_codex","input":[]}`,
		`{"model":"gpt-5","x-codex-turn-state":"continuation","input":[]}`,
	} {
		projected, err := PrepareOpenCodeOutboundBody([]byte(body), SessionPersonaTransportHTTP, false)
		if !errors.Is(err, ErrOpenCodePersonaContinuationUnsupported) {
			t.Fatalf("PrepareOpenCodeOutboundBody(%s) error = %v, want continuation rejection", body, err)
		}
		if projected != nil {
			t.Fatalf("continuation rejection returned a projected body: %s", projected)
		}
	}
}

func TestApplyOpenCodeWebSocketHeadersReplacesInheritedProtocol(t *testing.T) {
	binding := SessionPersonaSlotBinding{
		AccountID:         9,
		SlotID:            1,
		SlotCount:         2,
		ScopeVersion:      SessionPersonaScopeVersionV3,
		PersonaID:         SessionPersonaOpenCode,
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
		SessionEpoch:      3,
		SlotGeneration:    2,
		SlotSetGeneration: 4,
		ClientThreadID:    "client-thread-id",
	}
	headers := http.Header{
		"X-Codex-Turn-State": {"stale"},
		"X-OpenAI-Subagent":  {"stale"},
		"OpenAI-Beta":        {"responses=codex"},
	}
	ApplyOpenCodeWebSocketHeaders(headers, binding, "darwin", "14.0", "arm64")
	if got, want := headers.Get("OpenAI-Beta"), OpenCodeResponsesWebSocketProtocolHeader; got != want {
		t.Fatalf("WS protocol header = %q, want %q", got, want)
	}
	if headers.Get("x-codex-turn-state") != "" || headers.Get("x-openai-subagent") != "" {
		t.Fatalf("Codex WS identity leaked: %#v", headers)
	}
}
