package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexFingerprintSessionRepoStub struct {
	AccountRepository
	state          CodexFingerprintState
	lastRequest    CodexFingerprintSessionRequest
	matchedHash    string
	boundScopeHash string
}

func (r *codexFingerprintSessionRepoStub) ValidateCodexFingerprintSecret(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (r *codexFingerprintSessionRepoStub) ResolveCodexFingerprintSessionState(
	_ context.Context,
	request CodexFingerprintSessionRequest,
) (*CodexFingerprintSessionResolution, error) {
	r.lastRequest = request
	state := r.state
	matchedHash := r.matchedHash
	if matchedHash == "" {
		matchedHash = request.ThreadSourceHashes[0]
	}
	boundScopeHash := r.boundScopeHash
	if boundScopeHash == "" {
		boundScopeHash = request.SessionScopeHash
	}
	return &CodexFingerprintSessionResolution{
		State:                   state,
		BoundEpoch:              state.Epoch,
		MatchedThreadSourceHash: matchedHash,
		BoundSessionScopeHash:   boundScopeHash,
	}, nil
}

func TestResolveCodexFingerprintContextForAttemptUsesStableFallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(openAITurnStateSessionHashContextKey, "stable-session-hash")

	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID:                             27,
		Platform:                       PlatformOpenAI,
		Type:                           AccountTypeOAuth,
		Extra:                          map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed:           testCodexFingerprintV2Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV2,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexFingerprintSecret: string(testCodexFingerprintV2Secret()),
		}},
		accountRepo: &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
			Seed: testCodexFingerprintV2Seed(), Version: codexFingerprintAlgorithmV2,
			Epoch: 3, EpochStartedAt: startedAt,
		}},
	}
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)

	first, err := svc.resolveCodexFingerprintContextForAttempt(context.Background(), c, account, c.Request.Header, body)
	require.NoError(t, err)
	second, err := svc.resolveCodexFingerprintContextForAttempt(context.Background(), c, account, c.Request.Header, body)
	require.NoError(t, err)

	require.NotEmpty(t, first.ThreadID())
	require.NotEmpty(t, first.TurnID())
	assert.Equal(t, first.ThreadID(), second.ThreadID())
	assert.Equal(t, first.TurnID(), second.TurnID())
	source, ok := c.Get(codexFingerprintLogicalTurnSourceContextKey)
	require.True(t, ok)
	require.NotEmpty(t, source)
}

func TestResolveCodexFingerprintContextScopesThreadsAndDerivesRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID:                             27,
		Platform:                       PlatformOpenAI,
		Type:                           AccountTypeOAuth,
		Extra:                          map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed:           testCodexFingerprintV2Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV2,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	repo := &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
		Seed: testCodexFingerprintV2Seed(), Version: codexFingerprintAlgorithmV2,
		Epoch: 3, EpochStartedAt: startedAt,
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexFingerprintSecret: string(testCodexFingerprintV2Secret()),
		}},
		accountRepo: repo,
	}
	resolve := func(apiKeyID int64, transport OpenAIClientTransport) (*CodexFingerprintContext, string) {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("session-id", "shared-client-session")
		c.Set("api_key", &APIKey{ID: apiKeyID})
		SetOpenAIClientTransport(c, transport)
		fp, err := svc.resolveCodexFingerprintContextForAttempt(context.Background(), c, account, c.Request.Header, []byte(`{"input":"hello"}`))
		require.NoError(t, err)
		return fp, repo.lastRequest.SessionScopeHash
	}

	httpFirst, httpScopeHash := resolve(101, OpenAIClientTransportHTTP)
	httpSecond, httpSecondScopeHash := resolve(202, OpenAIClientTransportHTTP)
	wsFirst, wsScopeHash := resolve(303, OpenAIClientTransportWS)
	wsSecond, wsSecondScopeHash := resolve(404, OpenAIClientTransportWS)
	require.Equal(t, httpFirst.SessionID(), httpSecond.SessionID(), "相同入站传输的不同 API Key 应共用 Session")
	require.Equal(t, wsFirst.SessionID(), wsSecond.SessionID(), "相同入站传输的不同 API Key 应共用 Session")
	require.NotEqual(t, httpFirst.SessionID(), wsFirst.SessionID(), "HTTP 与 WS 入站必须使用独立 Session")
	require.Equal(t, httpScopeHash, httpSecondScopeHash)
	require.Equal(t, wsScopeHash, wsSecondScopeHash)
	require.NotEqual(t, httpScopeHash, wsScopeHash, "HTTP 与 WS 必须使用独立的持久化轮换作用域")
	require.NotEqual(t, httpFirst.ThreadID(), httpSecond.ThreadID(), "不同 API Key 的同名客户端 Session 必须隔离")
	require.NotEmpty(t, httpFirst.RequestID())
	require.NotEqual(t, httpFirst.ThreadID(), httpFirst.RequestID(), "请求 ID 不得回退为固定 Thread ID")
}

func TestResolveCodexFingerprintSessionScopeUsesUpstreamVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolve := func(path, userAgent, originator string, identityEnforced bool, transport OpenAIClientTransport) string {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, path, nil)
		c.Request.Header.Set("User-Agent", userAgent)
		c.Request.Header.Set("originator", originator)
		c.Set("api_key", &APIKey{ID: 101})
		SetOpenAIClientTransport(c, transport)
		return resolveCodexFingerprintSessionScope(c, c.Request.Header, identityEnforced)
	}

	codexResponses := resolve("/v1/responses", "codex_cli_rs/0.101.0", "codex_cli_rs", true, OpenAIClientTransportHTTP)
	openClawResponses := resolve("/v1/responses", "OpenClaw/2026.9.0", "openclaw", true, OpenAIClientTransportHTTP)
	codexResponsesWS := resolve("/v1/responses", "codex_cli_rs/0.101.0", "codex_cli_rs", true, OpenAIClientTransportWS)
	codexChat := resolve("/v1/chat/completions", "codex_cli_rs/0.102.0", "codex_cli_rs", true, OpenAIClientTransportHTTP)
	require.Equal(t, codexResponses, openClawResponses, "身份被统一后同协议必须共用 Session")
	require.NotEqual(t, codexResponses, codexResponsesWS, "身份被统一后仍须按 HTTP/WS 入站传输隔离")
	require.NotEqual(t, codexResponses, codexChat, "身份不可见时按语义协议隔离")

	codexVisible := resolve("/v1/responses", "codex_cli_rs/0.101.0", "codex_cli_rs", false, OpenAIClientTransportHTTP)
	vscodeVisible := resolve("/v1/responses", "codex_vscode/0.101.0", "codex_vscode", false, OpenAIClientTransportHTTP)
	openClawFallback := resolve("/v1/responses", "OpenClaw/2026.9.0", "openclaw", false, OpenAIClientTransportHTTP)
	codexVisibleChat := resolve("/v1/chat/completions", "codex_cli_rs/0.102.0", "codex_cli_rs", false, OpenAIClientTransportHTTP)
	codexVisibleWS := resolve("/v1/responses", "codex_cli_rs/0.102.0", "codex_cli_rs", false, OpenAIClientTransportWS)
	require.NotEqual(t, codexVisible, vscodeVisible, "可配对且真实出站的不同官方客户端必须分槽")
	require.Equal(t, codexVisible, codexVisibleChat, "同一可见客户端不因协议或版本变化切换 Session")
	require.NotEqual(t, codexVisible, codexVisibleWS, "同一可见客户端的 HTTP/WS 入站必须分槽")
	require.Equal(t, openClawFallback, codexResponses, "无法真实出站的 OpenClaw 身份必须按协议收敛")
	require.NotEqual(t,
		resolve("/v1/chat/completions", "", "", false, OpenAIClientTransportHTTP),
		resolve("/v1/responses", "", "", false, OpenAIClientTransportHTTP),
		"身份缺失时必须按协议族兜底隔离",
	)
	require.Equal(t,
		"protocol:responses",
		resolve("/v1/responses", "", "", true, OpenAIClientTransportUnknown),
		"无法识别传输时必须保留原有最收敛作用域",
	)
}

func TestResolveCodexFingerprintContextPreservesTransportAgnosticThreadBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{
		Seed: testCodexFingerprintV2Seed(), Version: codexFingerprintAlgorithmV2,
		Epoch: 3, EpochStartedAt: startedAt,
	}
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials:          map[string]any{"user_agent": "codex_cli_rs/0.101.0"},
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed: state.Seed, CodexFingerprintVersion: state.Version,
		CodexFingerprintEpoch: state.Epoch, CodexFingerprintEpochStartedAt: &startedAt,
	}
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session-id", "legacy-client-session")
	c.Set("api_key", &APIKey{ID: 101})
	SetOpenAIClientTransport(c, OpenAIClientTransportWS)

	secret := testCodexFingerprintV2Secret()
	legacyScope := resolveCodexFingerprintTransportAgnosticSessionScope(c, c.Request.Header, true)
	legacyThreadScope := resolveCodexFingerprintThreadScope(c, legacyScope)
	legacyThreadHash := codexFingerprintThreadSourceHash(
		secret,
		codexFingerprintScopedDerivationSource(legacyThreadScope, "legacy-client-session"),
	)
	legacyScopeHash := codexFingerprintSessionScopeHash(secret, legacyScope)
	repo := &codexFingerprintSessionRepoStub{
		state: state, matchedHash: legacyThreadHash, boundScopeHash: legacyScopeHash,
	}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexFingerprintSecret: string(secret),
		}},
		accountRepo: repo,
	}

	fp, err := svc.resolveCodexFingerprintContextForAttempt(
		context.Background(), c, account, c.Request.Header, []byte(`{"input":"hello"}`),
	)
	require.NoError(t, err)
	legacyFP, err := newCodexFingerprintContextV2(
		secret,
		state.Seed,
		state.Epoch,
		codexFingerprintSession,
		"",
		codexFingerprintOriginalIDs{
			clientScope:     legacyScope,
			threadScope:     legacyThreadScope,
			clientSessionID: "legacy-client-session",
			threadID:        "legacy-client-session",
		},
	)
	require.NoError(t, err)
	require.NotEqual(t, legacyScopeHash, repo.lastRequest.SessionScopeHash, "新 WS 作用域必须区别于旧无传输作用域")
	require.Contains(t, repo.lastRequest.ThreadSourceHashes, legacyThreadHash)
	require.Equal(t, legacyScopeHash, fp.sessionScopeHash)
	require.Equal(t, legacyFP.SessionID(), fp.SessionID(), "旧 Thread 必须继续使用旧 Session 派生输入")
	require.Equal(t, legacyFP.ThreadID(), fp.ThreadID(), "旧 Thread 不得因新增传输维度改变")

	// 子线程别名写入后会先命中新哈希，仍必须服从持久化的历史 scope。
	repo.matchedHash = repo.lastRequest.ThreadSourceHashes[0]
	continued, err := svc.resolveCodexFingerprintContextForAttempt(
		context.Background(), c, account, c.Request.Header, []byte(`{"input":"continued"}`),
	)
	require.NoError(t, err)
	require.Equal(t, legacyScopeHash, continued.sessionScopeHash)
	require.Equal(t, legacyFP.SessionID(), continued.SessionID())
	require.Equal(t, legacyFP.ThreadID(), continued.ThreadID())
}

func TestResolveCodexFingerprintLegacyClientScopePreservesCompactFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolve := func(path string) string {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, path, nil)
		return resolveCodexFingerprintLegacyClientScope(c, c.Request.Header)
	}

	require.Equal(t, "client:unknown-responses", resolve("/v1/responses/compact"))
	require.Equal(t, "client:unknown-chat", resolve("/v1/chat/completions"))
	require.Equal(t, "client:unknown", resolve("/v1/messages"))
}

func TestCodexFingerprintV2RewritesRequestAndPromptCacheIDs(t *testing.T) {
	fp, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 4,
		codexFingerprintSession, "", codexFingerprintOriginalIDs{
			clientSessionID: "client-session",
			turnID:          "turn-1",
			promptCacheKey:  "prompt-1",
			requestID:       "request-1",
		},
	)
	require.NoError(t, err)
	ids := codexFingerprintIDsFromContext(fp)
	headers := http.Header{"X-Client-Request-Id": []string{"request-1"}}
	body := map[string]any{"prompt_cache_key": "prompt-1"}

	applyCodexFingerprintHeaders(headers, ids)
	require.True(t, applyCodexFingerprintClientMetadata(body, ids))

	assert.Equal(t, fp.RequestID(), headers.Get("x-client-request-id"))
	assert.Equal(t, fp.PromptCacheKey(), body["prompt_cache_key"])
	metadata, ok := body["client_metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, fp.ThreadID(), metadata["thread_id"])
}

func TestCodexFingerprintV2FullModeUsesOneAccountThread(t *testing.T) {
	first, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 5,
		codexFingerprintFull, "", codexFingerprintOriginalIDs{clientScope: "client:codex", threadScope: "api-key:101:client:codex", clientSessionID: "client-a", turnID: "turn-a"},
	)
	require.NoError(t, err)
	second, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 5,
		codexFingerprintFull, "", codexFingerprintOriginalIDs{clientScope: "client:codex", threadScope: "api-key:101:client:codex", clientSessionID: "client-b", turnID: "turn-b"},
	)
	require.NoError(t, err)
	otherClient, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 5,
		codexFingerprintFull, "", codexFingerprintOriginalIDs{clientScope: "client:openclaw", threadScope: "api-key:101:client:openclaw", clientSessionID: "client-a", turnID: "turn-a"},
	)
	require.NoError(t, err)

	assert.Equal(t, first.ThreadID(), second.ThreadID())
	assert.NotEqual(t, first.TurnID(), second.TurnID())
	assert.NotEqual(t, first.SessionID(), otherClient.SessionID())
	assert.NotEqual(t, first.ThreadID(), otherClient.ThreadID())
}

func TestCodexFingerprintSessionRotationThresholdsUseScopeAge(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		CodexFingerprintMinSessionAgeHours:  72,
		CodexFingerprintMaxSessionAgeHours:  168,
		CodexFingerprintRotationJitterHours: 0,
		CodexFingerprintIdleGateMinutes:     120,
	}}}
	thresholds := svc.codexFingerprintRotationThresholds(27, strings.Repeat("cd", 32), now)
	require.Equal(t, now.Add(-72*time.Hour), thresholds.MinAgeBefore)
	require.Equal(t, now.Add(-168*time.Hour), thresholds.MaxAgeBefore)
	require.Equal(t, now.Add(-2*time.Hour), thresholds.IdleBefore)
}

func TestCodexFingerprintThreadSourceHashDoesNotExposeSource(t *testing.T) {
	source := "client-session-sensitive-value"
	first := codexFingerprintThreadSourceHash(testCodexFingerprintV2Secret(), source)
	second := codexFingerprintThreadSourceHash(testCodexFingerprintV2Secret(), source)
	otherCluster := codexFingerprintThreadSourceHash([]byte("abcdef0123456789abcdef0123456789"), source)

	require.Len(t, first, 64)
	require.Equal(t, first, second)
	require.NotEqual(t, source, first)
	require.NotEqual(t, first, otherCluster)
}

func testCodexFingerprintV2Secret() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func testCodexFingerprintV2Seed() string {
	return strings.Repeat("ab", codexFingerprintSeedBytes)
}

func TestCodexFingerprintV2ContextKeepsOneSessionWithMultipleThreads(t *testing.T) {
	base := codexFingerprintOriginalIDs{clientScope: "client:codex", threadScope: "api-key:101:client:codex", clientSessionID: "client-session-a", turnID: "turn-a"}
	first, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 7,
		codexFingerprintSession, "", base,
	)
	require.NoError(t, err)

	secondInput := base
	secondInput.clientSessionID = "client-session-b"
	second, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 7,
		codexFingerprintSession, "", secondInput,
	)
	require.NoError(t, err)

	assert.Equal(t, first.InstallationID(), second.InstallationID())
	assert.Equal(t, first.SessionID(), second.SessionID())
	assert.NotEqual(t, first.ThreadID(), second.ThreadID())
	assert.Equal(t, int64(7), first.SessionEpoch())
	assert.Equal(t, codexFingerprintAlgorithmV2, first.AlgorithmVersion())
}

func TestCodexFingerprintV2ContextSeparatesClientScopes(t *testing.T) {
	codexInput := codexFingerprintOriginalIDs{
		clientScope:     "client:codex",
		threadScope:     "api-key:101:client:codex",
		clientSessionID: "shared-client-session",
		turnID:          "shared-turn",
		windowID:        "shared-window",
		promptCacheKey:  "shared-cache",
		requestID:       "shared-request",
	}
	openClawInput := codexInput
	openClawInput.clientScope = "client:openclaw"
	openClawInput.threadScope = "api-key:101:client:openclaw"
	codex, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 7,
		codexFingerprintSession, "", codexInput,
	)
	require.NoError(t, err)
	openClaw, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 7,
		codexFingerprintSession, "", openClawInput,
	)
	require.NoError(t, err)

	require.Equal(t, codex.InstallationID(), openClaw.InstallationID(), "同一账号仍模拟同一设备")
	for _, pair := range [][2]string{
		{codex.SessionID(), openClaw.SessionID()},
		{codex.ThreadID(), openClaw.ThreadID()},
		{codex.TurnID(), openClaw.TurnID()},
		{codex.WindowID(), openClaw.WindowID()},
		{codex.PromptCacheKey(), openClaw.PromptCacheKey()},
		{codex.RequestID(), openClaw.RequestID()},
	} {
		require.NotEqual(t, pair[0], pair[1])
	}
}

func TestCodexFingerprintV2ContextSharesClientSessionButSeparatesAPIKeyThreads(t *testing.T) {
	firstInput := codexFingerprintOriginalIDs{
		clientScope:     "client:codex",
		threadScope:     "api-key:101:client:codex",
		clientSessionID: "shared-client-session",
		turnID:          "shared-turn",
		windowID:        "shared-window",
		promptCacheKey:  "shared-cache",
		requestID:       "shared-request",
	}
	secondInput := firstInput
	secondInput.threadScope = "api-key:202:client:codex"
	first, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 7,
		codexFingerprintSession, "", firstInput,
	)
	require.NoError(t, err)
	second, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 7,
		codexFingerprintSession, "", secondInput,
	)
	require.NoError(t, err)

	require.Equal(t, first.InstallationID(), second.InstallationID())
	require.Equal(t, first.SessionID(), second.SessionID())
	for _, pair := range [][2]string{
		{first.ThreadID(), second.ThreadID()},
		{first.TurnID(), second.TurnID()},
		{first.WindowID(), second.WindowID()},
		{first.PromptCacheKey(), second.PromptCacheKey()},
		{first.RequestID(), second.RequestID()},
	} {
		require.NotEqual(t, pair[0], pair[1])
	}
}

func TestCodexFingerprintV2ContextSeparatesAccountsClustersEpochsAndKinds(t *testing.T) {
	input := codexFingerprintOriginalIDs{clientSessionID: "client-session", turnID: "same-source"}
	base, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 3,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)

	otherSeed, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), strings.Repeat("cd", codexFingerprintSeedBytes), 3,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)
	otherCluster, err := newCodexFingerprintContextV2(
		[]byte("abcdef0123456789abcdef0123456789"), testCodexFingerprintV2Seed(), 3,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)
	nextEpoch, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 4,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)

	assert.NotEqual(t, base.InstallationID(), otherSeed.InstallationID())
	assert.NotEqual(t, base.InstallationID(), otherCluster.InstallationID())
	assert.Equal(t, base.InstallationID(), nextEpoch.InstallationID(), "设备 ID 不随 Session epoch 轮换")
	assert.NotEqual(t, base.SessionID(), nextEpoch.SessionID())
	assert.NotEqual(t, base.ThreadID(), nextEpoch.ThreadID())
	assert.NotEqual(t, base.ThreadID(), base.TurnID(), "kind 必须形成独立输入域")
}

func TestCodexFingerprintV2ContextReusesLogicalTurnAndProducesUUIDv4(t *testing.T) {
	input := codexFingerprintOriginalIDs{
		clientSessionID: "client-session",
		turnID:          "logical-turn-123",
		windowID:        "window-1",
		promptCacheKey:  "prompt-1",
		requestID:       "request-1",
	}
	first, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 9,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)
	second, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 9,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)

	assert.Equal(t, first.TurnID(), second.TurnID(), "同一逻辑 turn 的重试必须复用映射值")
	for _, value := range []string{
		first.InstallationID(), first.SessionID(), first.ThreadID(), first.TurnID(),
		first.WindowID(), first.PromptCacheKey(), first.RequestID(),
	} {
		parsed, parseErr := uuid.Parse(value)
		require.NoError(t, parseErr)
		assert.Equal(t, uuid.Version(4), parsed.Version())
		assert.Equal(t, uuid.RFC4122, parsed.Variant())
	}
}

func TestCodexFingerprintV2ContextHonorsConfiguredDeviceAndDeviceMode(t *testing.T) {
	ctx, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 1,
		codexFingerprintDevice, "configured-device-id", codexFingerprintOriginalIDs{},
	)
	require.NoError(t, err)
	assert.Equal(t, "configured-device-id", ctx.InstallationID())
	assert.Empty(t, ctx.SessionID())
	assert.Empty(t, ctx.ThreadID())
}

func TestCodexFingerprintV2ContextRejectsUnsafeState(t *testing.T) {
	tests := []struct {
		name   string
		secret []byte
		seed   string
		epoch  int64
		input  codexFingerprintOriginalIDs
		want   error
	}{
		{"短集群密钥", []byte("short"), testCodexFingerprintV2Seed(), 1, codexFingerprintOriginalIDs{clientSessionID: "s"}, errCodexFingerprintSecretInvalid},
		{"非法种子", testCodexFingerprintV2Secret(), strings.Repeat("zz", codexFingerprintSeedBytes), 1, codexFingerprintOriginalIDs{clientSessionID: "s"}, errCodexFingerprintSeedInvalid},
		{"epoch 未初始化", testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 0, codexFingerprintOriginalIDs{clientSessionID: "s"}, errCodexFingerprintEpochInvalid},
		{"缺少 Thread 来源", testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 1, codexFingerprintOriginalIDs{}, errCodexFingerprintThreadMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := newCodexFingerprintContextV2(tt.secret, tt.seed, tt.epoch, codexFingerprintSession, "", tt.input)
			assert.Nil(t, ctx)
			assert.True(t, errors.Is(err, tt.want), "got %v, want %v", err, tt.want)
		})
	}
}

func TestResolveCodexFingerprintContextRequiresConfiguredSecret(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID:                             27,
		Platform:                       PlatformOpenAI,
		Type:                           AccountTypeOAuth,
		Extra:                          map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed:           testCodexFingerprintV2Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV2,
		CodexFingerprintEpoch:          1,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)

	_, err := (&OpenAIGatewayService{cfg: &config.Config{}}).resolveCodexFingerprintContextForAttempt(
		context.Background(), c, account, c.Request.Header, []byte(`{"input":"hello"}`),
	)
	require.ErrorIs(t, err, errCodexFingerprintSecretInvalid)
}

func TestApplyCodexFingerprintForAttemptSharesChatSessionAcrossAPIKeys(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{Seed: testCodexFingerprintV2Seed(), Version: codexFingerprintAlgorithmV2, Epoch: 3, EpochStartedAt: startedAt}
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed: state.Seed, CodexFingerprintVersion: state.Version,
		CodexFingerprintEpoch: state.Epoch, CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV2Secret())}},
		accountRepo: &codexFingerprintSessionRepoStub{state: state},
	}
	apply := func(path string, apiKeyID int64) (string, string) {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, path, nil)
		c.Request.Header.Set("session-id", "same-client-session")
		c.Set("api_key", &APIKey{ID: apiKeyID})
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		body, err := svc.applyCodexFingerprintForAttempt(context.Background(), c, account, []byte(`{"input":"hello"}`), false, true)
		require.NoError(t, err)
		return gjson.GetBytes(body, "client_metadata.session_id").String(), gjson.GetBytes(body, "client_metadata.thread_id").String()
	}

	chatSession, chatThread := apply("/v1/chat/completions", 101)
	messagesSession, messagesThread := apply("/v1/messages", 202)
	require.NotEmpty(t, chatSession)
	require.Equal(t, chatSession, messagesSession)
	require.NotEqual(t, chatThread, messagesThread)
}

func TestApplyCodexFingerprintForAttemptCompactOnlyStagesHeaders(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{Seed: testCodexFingerprintV2Seed(), Version: codexFingerprintAlgorithmV2, Epoch: 3, EpochStartedAt: startedAt}
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed: state.Seed, CodexFingerprintVersion: state.Version,
		CodexFingerprintEpoch: state.Epoch, CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV2Secret())}},
		accountRepo: &codexFingerprintSessionRepoStub{state: state},
	}
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	c.Request.Header.Set("session-id", "compact-session")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	body := []byte(`{"model":"gpt-5.6-sol","input":"hello"}`)

	updated, err := svc.applyCodexFingerprintForAttempt(context.Background(), c, account, body, false, false)
	require.NoError(t, err)
	require.Equal(t, body, updated)
	headers := make(http.Header)
	applyStagedCodexFingerprintHeaders(c, account, headers)
	require.NotEmpty(t, headers.Get("session_id"))
	require.NotEmpty(t, headers.Get("x-codex-installation-id"))
}

func TestAccountCodexFingerprintStateIsExcludedFromJSON(t *testing.T) {
	account := Account{
		ID:                             42,
		CodexFingerprintSeed:           testCodexFingerprintV2Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV2,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: nil,
	}

	payload, err := json.Marshal(account)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "CodexFingerprint")
	assert.NotContains(t, string(payload), testCodexFingerprintV2Seed())
}

func TestCodexFingerprintSubagentMapsClosedTopology(t *testing.T) {
	original := codexFingerprintOriginalIDs{
		clientScope:      "client:codex:transport:http",
		threadScope:      "api-key:101:client:codex:transport:http",
		clientSessionID:  "root-session",
		threadID:         "child-thread",
		parentThreadID:   "root-thread",
		forkedThreadID:   "root-thread",
		turnID:           "child-turn",
		windowID:         "child-window",
		subagentMarker:   "worker",
		isSubagent:       true,
		sessionScopeHash: strings.Repeat("cd", 32),
	}
	child, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 7,
		codexFingerprintSession, "", original,
	)
	require.NoError(t, err)
	rootInput := original
	rootInput.threadID = original.parentThreadID
	rootInput.parentThreadID = ""
	rootInput.forkedThreadID = ""
	rootInput.isSubagent = false
	root, err := newCodexFingerprintContextV2(
		testCodexFingerprintV2Secret(), testCodexFingerprintV2Seed(), 7,
		codexFingerprintSession, "", rootInput,
	)
	require.NoError(t, err)

	require.Equal(t, root.SessionID(), child.SessionID())
	require.Equal(t, root.ThreadID(), child.parentThreadID)
	require.Equal(t, root.ThreadID(), child.forkedThreadID)
	require.NotEqual(t, root.ThreadID(), child.ThreadID())

	ids := codexFingerprintIDsFromContext(child)
	headers := http.Header{
		"X-Openai-Subagent":     []string{"worker"},
		"X-Codex-Turn-Metadata": []string{`{"request_kind":"subagent","custom":"kept"}`},
	}
	body := map[string]any{"client_metadata": map[string]any{
		"x-codex-turn-metadata": `{"request_kind":"subagent","custom":"kept"}`,
	}}
	applyCodexFingerprintHeaders(headers, ids)
	require.True(t, applyCodexFingerprintClientMetadata(body, ids))
	require.Equal(t, root.ThreadID(), headers.Get("x-codex-parent-thread-id"))
	require.Equal(t, "worker", headers.Get("x-openai-subagent"))
	require.Equal(t, "kept", gjson.Get(headers.Get("x-codex-turn-metadata"), "custom").String())
	metadata := body["client_metadata"].(map[string]any)
	require.Equal(t, root.ThreadID(), metadata["parent_thread_id"])
	require.Equal(t, "kept", gjson.Get(metadata["x-codex-turn-metadata"].(string), "custom").String())
}

func TestExtractCodexFingerprintOriginalIDsPrefersBodyTopology(t *testing.T) {
	headers := http.Header{
		"Thread-Id":                []string{"header-child"},
		"X-Codex-Parent-Thread-Id": []string{"header-parent"},
		"X-Openai-Subagent":        []string{"true"},
		"X-Codex-Turn-Metadata":    []string{`{"thread_id":"header-metadata-child","parent_thread_id":"header-metadata-parent"}`},
	}
	body := []byte(`{"client_metadata":{"thread_id":"flat-child","parent_thread_id":"flat-parent","x-codex-turn-metadata":"{\"thread_id\":\"body-child\",\"parent_thread_id\":\"body-parent\",\"forked_from_thread_id\":\"body-fork\",\"subagent_kind\":\"worker\"}"}}`)

	original := extractCodexFingerprintOriginalIDs(headers, body)
	require.Equal(t, "body-child", original.threadID)
	require.Equal(t, "body-parent", original.parentThreadID)
	require.Equal(t, "body-fork", original.forkedThreadID)
	require.Equal(t, "worker", original.subagentMarker)
	require.True(t, original.isSubagent)
}

func TestExtractCodexFingerprintOriginalIDsDetectsMetadataOnlySubagent(t *testing.T) {
	body := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"child\",\"request_kind\":\"subagent_task\"}"}}`)
	original := extractCodexFingerprintOriginalIDs(nil, body)
	require.True(t, original.isSubagent)
	require.Equal(t, "subagent_task", original.subagentMarker)
}

func TestCodexFingerprintFullModeRejectsSubagent(t *testing.T) {
	startedAt := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                          map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintFull)},
		CodexFingerprintSeed:           testCodexFingerprintV2Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV2,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV2Secret())}},
		accountRepo: &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
			Seed: testCodexFingerprintV2Seed(), Version: codexFingerprintAlgorithmV2,
			Epoch: 3, EpochStartedAt: startedAt,
		}},
	}
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 101})
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	body := []byte(`{"client_metadata":{"session_id":"root-session","x-codex-turn-metadata":"{\"thread_id\":\"child\",\"parent_thread_id\":\"parent\",\"subagent_kind\":\"worker\"}"}}`)

	_, err := svc.resolveCodexFingerprintContextForAttempt(context.Background(), c, account, c.Request.Header, body)
	require.ErrorIs(t, err, errCodexFingerprintFullSubagent)
}

func TestCodexFingerprintFullModeWithGatePreservesSubagentTopology(t *testing.T) {
	startedAt := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra: map[string]any{
			codexFingerprintModeExtraKey:     string(codexFingerprintFull),
			codexSubagentMaxInflightExtraKey: 4,
		},
		CodexFingerprintSeed:           testCodexFingerprintV2Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV2,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	repo := &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
		Seed: testCodexFingerprintV2Seed(), Version: codexFingerprintAlgorithmV2,
		Epoch: 3, EpochStartedAt: startedAt,
	}}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV2Secret())}},
		accountRepo: repo,
	}
	resolve := func(body []byte) *CodexFingerprintContext {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set("api_key", &APIKey{ID: 101})
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		fp, err := svc.resolveCodexFingerprintContextForAttempt(context.Background(), c, account, c.Request.Header, body)
		require.NoError(t, err)
		return fp
	}

	root := resolve([]byte(`{"client_metadata":{"session_id":"root-session","x-codex-turn-metadata":"{\"thread_id\":\"root-thread\",\"turn_id\":\"root-turn\"}"}}`))
	child := resolve([]byte(`{"client_metadata":{"session_id":"root-session","x-codex-turn-metadata":"{\"thread_id\":\"child-thread\",\"parent_thread_id\":\"root-thread\",\"subagent_kind\":\"worker\"}"}}`))

	require.Equal(t, string(codexFingerprintSession), root.Mode())
	require.Equal(t, root.SessionID(), child.SessionID())
	require.Equal(t, root.ThreadID(), child.parentThreadID)
	require.NotEqual(t, root.ThreadID(), child.ThreadID())
}

func TestAccountCodexSubagentConcurrencyGateRequiresSessionMode(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			codexFingerprintModeExtraKey:     string(codexFingerprintSession),
			codexSubagentMaxInflightExtraKey: float64(4),
		},
	}
	require.Equal(t, 4, account.GetCodexSubagentMaxInflightPerSession())
	account.Extra[codexFingerprintModeExtraKey] = string(codexFingerprintDevice)
	require.Zero(t, account.GetCodexSubagentMaxInflightPerSession())
	account.Extra[codexFingerprintModeExtraKey] = string(codexFingerprintFull)
	account.Extra[codexSubagentMaxInflightExtraKey] = 65
	require.Zero(t, account.GetCodexSubagentMaxInflightPerSession())
}
