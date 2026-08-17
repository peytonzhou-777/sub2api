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
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexFingerprintSecret: string(testCodexFingerprintV2Secret()),
		}},
		accountRepo: &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
			Seed: testCodexFingerprintV2Seed(), Version: codexFingerprintAlgorithmV2,
			Epoch: 3, EpochStartedAt: startedAt,
		}},
	}
	resolve := func(apiKeyID int64) *CodexFingerprintContext {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("session-id", "shared-client-session")
		c.Set("api_key", &APIKey{ID: apiKeyID})
		fp, err := svc.resolveCodexFingerprintContextForAttempt(context.Background(), c, account, c.Request.Header, []byte(`{"input":"hello"}`))
		require.NoError(t, err)
		return fp
	}

	first := resolve(101)
	second := resolve(202)
	require.Equal(t, first.SessionID(), second.SessionID(), "相同客户端类型的不同 API Key 应共用 Session")
	require.NotEqual(t, first.ThreadID(), second.ThreadID(), "不同 API Key 的同名客户端 Session 必须隔离")
	require.NotEmpty(t, first.RequestID())
	require.NotEqual(t, first.ThreadID(), first.RequestID(), "请求 ID 不得回退为固定 Thread ID")
}

func TestResolveCodexFingerprintSessionScopeUsesUpstreamVisibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolve := func(path, userAgent, originator string, identityEnforced bool) string {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, path, nil)
		c.Request.Header.Set("User-Agent", userAgent)
		c.Request.Header.Set("originator", originator)
		c.Set("api_key", &APIKey{ID: 101})
		return resolveCodexFingerprintSessionScope(c, c.Request.Header, identityEnforced)
	}

	codexResponses := resolve("/v1/responses", "codex_cli_rs/0.101.0", "codex_cli_rs", true)
	openClawResponses := resolve("/v1/responses", "OpenClaw/2026.9.0", "openclaw", true)
	codexChat := resolve("/v1/chat/completions", "codex_cli_rs/0.102.0", "codex_cli_rs", true)
	require.Equal(t, codexResponses, openClawResponses, "身份被统一后同协议必须共用 Session")
	require.NotEqual(t, codexResponses, codexChat, "身份不可见时按语义协议隔离")

	codexVisible := resolve("/v1/responses", "codex_cli_rs/0.101.0", "codex_cli_rs", false)
	vscodeVisible := resolve("/v1/responses", "codex_vscode/0.101.0", "codex_vscode", false)
	openClawFallback := resolve("/v1/responses", "OpenClaw/2026.9.0", "openclaw", false)
	codexVisibleChat := resolve("/v1/chat/completions", "codex_cli_rs/0.102.0", "codex_cli_rs", false)
	require.NotEqual(t, codexVisible, vscodeVisible, "可配对且真实出站的不同官方客户端必须分槽")
	require.Equal(t, codexVisible, codexVisibleChat, "同一可见客户端不因协议或版本变化切换 Session")
	require.Equal(t, openClawFallback, codexResponses, "无法真实出站的 OpenClaw 身份必须按协议收敛")
	require.NotEqual(t,
		resolve("/v1/chat/completions", "", "", false),
		resolve("/v1/responses", "", "", false),
		"身份缺失时必须按协议族兜底隔离",
	)
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
