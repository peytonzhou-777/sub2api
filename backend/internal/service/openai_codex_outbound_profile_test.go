package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newCodexOutboundStrictTestService() *OpenAIGatewayService {
	return &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		CodexOutboundProfileDefault: CodexOutboundProfileCLI0149,
	}}}
}

func newCodexOutboundTestContext() *gin.Context {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "untrusted-client/9.9")
	c.Request.Header.Set("Accept-Language", "zh-CN")
	c.Request.Header.Set("session-id", "raw-session")
	c.Request.Header.Set("thread-id", "raw-thread")
	c.Request.Header.Set("x-client-request-id", "raw-request")
	return c
}

func stageCodexOutboundTestIDs(c *gin.Context, accountID int64) *codexFingerprintIDs {
	ids := &codexFingerprintIDs{
		accountID:           accountID,
		mode:                codexFingerprintSession,
		installationID:      "0198a000-0000-7000-8000-000000000001",
		sessionID:           "0198a000-0000-7000-8000-000000000002",
		threadID:            "0198a000-0000-7000-8000-000000000003",
		parentThreadID:      "0198a000-0000-7000-8000-000000000004",
		turnID:              "0198a000-0000-7000-8000-000000000005",
		windowID:            "0198a000-0000-7000-8000-000000000006",
		promptCacheKey:      "0198a000-0000-7000-8000-000000000002",
		requestID:           "0198a000-0000-7000-8000-000000000099",
		turnStartedAtUnixMs: 1770000000123,
	}
	stageCodexFingerprintIDs(c, ids)
	return ids
}

func decodeTopLevelJSONKeys(t *testing.T, body []byte) []string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	require.NoError(t, err)
	require.Equal(t, json.Delim('{'), token)
	keys := make([]string, 0)
	for decoder.More() {
		key, err := decoder.Token()
		require.NoError(t, err)
		keys = append(keys, key.(string))
		var value json.RawMessage
		require.NoError(t, decoder.Decode(&value))
	}
	_, err = decoder.Token()
	require.NoError(t, err)
	return keys
}

func TestCodexCLI0149FixtureIsSanitizedAndMatchesIdentity(t *testing.T) {
	raw, err := os.ReadFile("testdata/codex_cli_0_149_0_windows_outbound.json")
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(raw, "source.sanitized").Bool())
	require.Equal(t, codexCLI0149WindowsUserAgent, gjson.GetBytes(raw, "identity.user_agent").String())
	require.NotContains(t, string(raw), "Authorization")
	require.NotContains(t, string(raw), "ChatGPT-Account-ID")
}

func TestResolveCodexOutboundProfilePrecedence(t *testing.T) {
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	svc := newCodexOutboundStrictTestService()
	require.Equal(t, CodexOutboundProfileCLI0149, svc.resolveCodexOutboundProfile(account))

	account.Extra[codexOutboundProfileExtraKey] = CodexOutboundProfileLegacy
	require.Equal(t, CodexOutboundProfileLegacy, svc.resolveCodexOutboundProfile(account))

	svc.cfg.Gateway.CodexOutboundForceLegacy = true
	account.Extra[codexOutboundProfileExtraKey] = CodexOutboundProfileCLI0149
	require.Equal(t, CodexOutboundProfileLegacy, svc.resolveCodexOutboundProfile(account))

	apiKeyAccount := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	require.Equal(t, CodexOutboundProfileLegacy, svc.resolveCodexOutboundProfile(apiKeyAccount))
}

func TestCodexOutboundGlobalForceLegacyRestoresLegacyIdentity(t *testing.T) {
	previousProfile, _ := codexOutboundDefaultProfile.Load().(string)
	previousForceLegacy := codexOutboundForceLegacy.Load()
	t.Cleanup(func() { SetCodexOutboundProfileConfig(previousProfile, previousForceLegacy) })
	account := &Account{ID: 13, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	SetCodexOutboundProfileConfig(CodexOutboundProfileLegacy, false)
	legacyUserAgent := CodexCanonicalUserAgent()
	legacyVersion := CodexCanonicalClientVersion()

	SetCodexOutboundProfileConfig(CodexOutboundProfileCLI0149, false)
	require.Equal(t, CodexOutboundProfileCLI0149, ResolveCodexOutboundProfile(account))
	require.Equal(t, codexCLI0149WindowsUserAgent, CodexCanonicalUserAgent())
	require.Equal(t, codexCLI0149Version, CodexCanonicalClientVersion())

	SetCodexOutboundProfileConfig(CodexOutboundProfileCLI0149, true)
	require.Equal(t, CodexOutboundProfileLegacy, ResolveCodexOutboundProfile(account))
	require.Equal(t, legacyUserAgent, CodexCanonicalUserAgent())
	require.Equal(t, legacyVersion, CodexCanonicalClientVersion())
}

func TestCodexOutboundStrictHTTPProjection(t *testing.T) {
	metricsBefore := SnapshotCodexOutboundMetrics()
	svc := newCodexOutboundStrictTestService()
	account := &Account{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newCodexOutboundTestContext()
	ids := stageCodexOutboundTestIDs(c, account.ID)
	body := []byte(`{"client_metadata":{"session_id":"raw-session","traceparent":"untrusted","x-codex-turn-metadata":"{\"thread_id\":\"raw-thread\",\"tool_namespaces_info\":[1]}"},"tools":[{"z":1,"a":"<raw>"}],"input":[],"instructions":"i","model":"gpt-5.4","prompt_cache_key":"raw-session","service_tier":"priority"}`)

	ordered, snapshot, err := svc.prepareCodexOutboundBody(c, account, body, "http", false)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	require.Equal(t, []string{
		"model", "instructions", "input", "tools", "reasoning", "store", "stream", "include",
		"service_tier", "prompt_cache_key", "client_metadata",
	}, decodeTopLevelJSONKeys(t, ordered))
	require.Contains(t, string(ordered), `"tools":[{"z":1,"a":"<raw>"}]`)
	require.Equal(t, ids.sessionID, gjson.GetBytes(ordered, "prompt_cache_key").String())
	require.Equal(t, ids.sessionID, gjson.GetBytes(ordered, "client_metadata.session_id").String())
	require.Equal(t, ids.threadID, gjson.GetBytes(ordered, "client_metadata.thread_id").String())
	require.False(t, gjson.GetBytes(ordered, "client_metadata.traceparent").Exists())
	require.Equal(t, "turn", gjson.Get(gjson.GetBytes(ordered, "client_metadata.x-codex-turn-metadata").String(), "request_kind").String())
	require.False(t, gjson.Get(gjson.GetBytes(ordered, "client_metadata.x-codex-turn-metadata").String(), "tool_namespaces_info").Exists())

	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(ordered))
	req.Header.Set("Version", "9.9")
	req.Header.Set("session_id", "leak")
	req.Header.Set("conversation_id", "leak")
	req.Header.Set("x-codex-installation-id", "leak")
	req.Header.Set("x-responsesapi-include-timing-metrics", "true")
	req.Header.Set("traceparent", "00-untrusted")
	req.Header.Set("tracestate", "vendor=untrusted")
	svc.compressCodexOutboundHTTPRequest(t.Context(), c, account, req, ordered, false)
	svc.finalizeCodexOutboundHeaders(c, account, req.Header, false, "http", "", "")

	require.Equal(t, codexCLI0149WindowsUserAgent, req.Header.Get("User-Agent"))
	require.Equal(t, "codex_cli_rs", req.Header.Get("originator"))
	require.Equal(t, "zstd", req.Header.Get("Content-Encoding"))
	require.Equal(t, "text/event-stream", req.Header.Get("Accept"))
	require.Equal(t, ids.sessionID, req.Header.Get("session-id"))
	require.Equal(t, ids.threadID, req.Header.Get("thread-id"))
	require.Equal(t, ids.threadID, req.Header.Get("x-client-request-id"))
	require.Empty(t, req.Header.Get("Version"))
	require.Empty(t, req.Header.Get("session_id"))
	require.Empty(t, req.Header.Get("conversation_id"))
	require.Empty(t, req.Header.Get("Accept-Language"))
	require.Empty(t, req.Header.Get("x-codex-installation-id"))
	require.Empty(t, req.Header.Get("x-responsesapi-include-timing-metrics"))
	require.Empty(t, req.Header.Get("traceparent"))
	require.Empty(t, req.Header.Get("tracestate"))
	require.Equal(t, "model=gpt-5.4;tier=priority", req.Header.Get(openAICodexRoutingHintHeader))

	compressed, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	decoder, err := zstd.NewReader(nil)
	require.NoError(t, err)
	t.Cleanup(decoder.Close)
	decompressed, err := decoder.DecodeAll(compressed, nil)
	require.NoError(t, err)
	require.Equal(t, ordered, decompressed)
	metricsAfter := SnapshotCodexOutboundMetrics()
	require.Greater(t, metricsAfter.StrictRequestsTotal, metricsBefore.StrictRequestsTotal)
	require.Greater(t, metricsAfter.HTTPRequestsTotal, metricsBefore.HTTPRequestsTotal)
	require.Greater(t, metricsAfter.MetadataSynthesizedTotal, metricsBefore.MetadataSynthesizedTotal)
	require.Greater(t, metricsAfter.ForbiddenHeadersStrippedTotal, metricsBefore.ForbiddenHeadersStrippedTotal)
	require.Greater(t, metricsAfter.ZstdRequestsTotal, metricsBefore.ZstdRequestsTotal)
}

func TestAccountTestServiceCodexProbeUsesStrictOutboundProfile(t *testing.T) {
	cfg := &config.Config{Gateway: config.GatewayConfig{CodexOutboundProfileDefault: CodexOutboundProfileCLI0149}}
	probeService := &AccountTestService{cfg: cfg}
	account := &Account{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
		codexFingerprintModeExtraKey: "off",
	}}
	body := []byte(`{"model":"gpt-5.4","input":[]}`)
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(body))
	req.Header.Set("originator", "untrusted")
	req.Header.Set("User-Agent", "untrusted/1.0")
	req.Header.Set("Version", "1.0")

	require.NoError(t, probeService.applyCodexOutboundProbeProfile(t.Context(), account, req, body))
	require.Equal(t, codexCLI0149WindowsUserAgent, req.Header.Get("User-Agent"))
	require.Equal(t, "codex_cli_rs", req.Header.Get("originator"))
	require.Empty(t, req.Header.Get("Version"))
	require.Equal(t, "zstd", req.Header.Get("Content-Encoding"))
	require.NotEmpty(t, req.Header.Get("session-id"))

	compressed, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	decoder, err := zstd.NewReader(nil)
	require.NoError(t, err)
	t.Cleanup(decoder.Close)
	decompressed, err := decoder.DecodeAll(compressed, nil)
	require.NoError(t, err)
	require.Equal(t, req.Header.Get("session-id"), gjson.GetBytes(decompressed, "prompt_cache_key").String())
	require.Equal(t, req.Header.Get("session-id"), gjson.GetBytes(decompressed, "client_metadata.session_id").String())
}

func TestCodexOutboundStrictWSAndCompactProjection(t *testing.T) {
	svc := newCodexOutboundStrictTestService()
	account := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newCodexOutboundTestContext()
	ids := stageCodexOutboundTestIDs(c, account.ID)

	wsBody := []byte(`{"type":"response.create","generate":false,"model":"gpt-5.4","input":[],"client_metadata":{"session_id":"raw-session"}}`)
	wsBody, snapshot, err := svc.prepareCodexOutboundBody(c, account, wsBody, "ws_v2", false)
	require.NoError(t, err)
	require.Equal(t, "prewarm", snapshot.requestKind)
	require.Equal(t, ids.sessionID, gjson.GetBytes(wsBody, "prompt_cache_key").String())
	wsHeaders := http.Header{
		"Version":                               {"9.9"},
		"Accept-Language":                       {"zh-CN"},
		"X-Codex-Installation-Id":               {"leak"},
		"X-ResponsesAPI-Include-Timing-Metrics": {"true"},
		"Traceparent":                           {"00-untrusted"},
		"Tracestate":                            {"vendor=untrusted"},
	}
	svc.finalizeCodexOutboundHeaders(c, account, wsHeaders, false, "ws_v2", "gpt-5.4", "")
	require.Equal(t, openAIWSBetaV2Value, wsHeaders.Get("OpenAI-Beta"))
	require.Equal(t, ids.sessionID, wsHeaders.Get("session-id"))
	require.Equal(t, ids.threadID, wsHeaders.Get("x-client-request-id"))
	require.Empty(t, wsHeaders.Get("Content-Encoding"))
	require.Empty(t, wsHeaders.Get("x-codex-installation-id"))
	require.Empty(t, wsHeaders.Get("x-responsesapi-include-timing-metrics"))
	require.Empty(t, wsHeaders.Get("traceparent"))
	require.Empty(t, wsHeaders.Get("tracestate"))

	stageCodexFingerprintIDs(c, ids)
	compactBody := []byte(`{"text":{"format":{"type":"text"}},"model":"gpt-5.4","input":[]}`)
	compactBody, compactSnapshot, err := svc.prepareCodexOutboundBody(c, account, compactBody, "http", true)
	require.NoError(t, err)
	require.Equal(t, "compaction", compactSnapshot.requestKind)
	require.False(t, gjson.GetBytes(compactBody, "client_metadata").Exists())
	require.Equal(t, ids.sessionID, gjson.GetBytes(compactBody, "prompt_cache_key").String())
	compactHeaders := http.Header{"Accept": {"application/json"}, "X-Client-Request-Id": {"leak"}}
	svc.finalizeCodexOutboundHeaders(c, account, compactHeaders, true, "http", "", "")
	require.Empty(t, compactHeaders.Get("Accept"))
	require.Empty(t, compactHeaders.Get("x-client-request-id"))
	require.Empty(t, compactHeaders.Get("Content-Encoding"))
	require.Equal(t, ids.installationID, compactHeaders.Get("x-codex-installation-id"))
	require.Equal(t, ids.sessionID, compactHeaders.Get("session-id"))
}

func TestCodexOutboundStrictWSFinalFrameKeepsOrderAndRawSubtrees(t *testing.T) {
	svc := newCodexOutboundStrictTestService()
	account := &Account{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	c := newCodexOutboundTestContext()
	ids := stageCodexOutboundTestIDs(c, account.ID)
	original := []byte(`{"tools":[{"z":1,"a":"<raw>"}],"input":[{"type":"message","content":[{"text":"hi","type":"input_text"}],"role":"user"}],"model":"gpt-5.4","type":"response.create"}`)
	prepared, _, err := svc.prepareCodexOutboundBody(c, account, original, "ws_v2", false)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(prepared, &payload))
	wire, err := svc.marshalCodexOutboundWSPayload(c, account, payload, "ws_v2", "turn")
	require.NoError(t, err)
	require.Equal(t, []string{
		"type", "model", "input", "tools", "reasoning", "store", "stream", "include",
		"prompt_cache_key", "client_metadata",
	}, decodeTopLevelJSONKeys(t, wire))
	require.Contains(t, string(wire), `"tools":[{"z":1,"a":"<raw>"}]`)
	require.Equal(t, ids.sessionID, gjson.GetBytes(wire, "prompt_cache_key").String())
	require.Greater(t, gjson.GetBytes(wire, "client_metadata.x-codex-ws-stream-request-start-ms").Int(), int64(0))

	prewarmWire, err := svc.marshalCodexOutboundWSPayload(c, account, payload, "ws_v2", "prewarm")
	require.NoError(t, err)
	metadata := gjson.GetBytes(prewarmWire, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, "prewarm", gjson.Get(metadata, "request_kind").String())
}

func TestCodexOutboundLegacyAndNonOAuthRemainUnchanged(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","custom":true}`)
	c := newCodexOutboundTestContext()
	legacySvc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		CodexOutboundProfileDefault: CodexOutboundProfileLegacy,
	}}}
	oauth := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	got, snapshot, err := legacySvc.prepareCodexOutboundBody(c, oauth, body, "http", false)
	require.NoError(t, err)
	require.Nil(t, snapshot)
	require.Equal(t, body, got)

	strictSvc := newCodexOutboundStrictTestService()
	apiKey := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	got, snapshot, err = strictSvc.prepareCodexOutboundBody(c, apiKey, body, "http", false)
	require.NoError(t, err)
	require.Nil(t, snapshot)
	require.Equal(t, body, got)
}
