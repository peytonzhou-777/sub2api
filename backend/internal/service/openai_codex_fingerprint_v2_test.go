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
)

type codexFingerprintSessionRepoStub struct {
	AccountRepository
	state CodexFingerprintState
}

func (r *codexFingerprintSessionRepoStub) ResolveCodexFingerprintSessionState(
	context.Context,
	int64,
	string,
	time.Time,
	bool,
	time.Time,
	time.Time,
	time.Time,
) (*CodexFingerprintSessionResolution, error) {
	state := r.state
	return &CodexFingerprintSessionResolution{State: state, BoundEpoch: state.Epoch}, nil
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

func TestResolveCodexFingerprintClientScopeSeparatesClientsButNotTransports(t *testing.T) {
	gin.SetMode(gin.TestMode)
	resolve := func(path, userAgent, originator string) string {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, path, nil)
		c.Request.Header.Set("User-Agent", userAgent)
		c.Request.Header.Set("originator", originator)
		c.Set("api_key", &APIKey{ID: 101})
		return resolveCodexFingerprintClientScope(c, c.Request.Header)
	}

	codexResponses := resolve("/v1/responses", "codex_cli_rs/0.101.0", "codex_cli_rs")
	codexChat := resolve("/v1/chat/completions", "codex_cli_rs/0.102.0", "codex_cli_rs")
	openClawChat := resolve("/v1/chat/completions", "OpenClaw/2026.8.1", "openclaw")
	openClawResponses := resolve("/v1/responses", "OpenClaw/2026.9.0", "openclaw")

	require.Equal(t, codexResponses, codexChat, "Codex 的版本或传输入口变化不得切换客户端槽位")
	require.Equal(t, openClawChat, openClawResponses, "OpenClaw 的版本或传输入口变化不得切换客户端槽位")
	require.NotEqual(t, codexResponses, openClawChat, "Codex 与 OpenClaw 必须使用不同客户端槽位")
	require.NotEqual(t,
		resolve("/v1/chat/completions", "", ""),
		resolve("/v1/responses", "", ""),
		"缺少客户端身份时才按协议族兜底隔离",
	)
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

func TestCodexFingerprintSessionRotationRequiresAgeAndIdle(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	startedAt := now.Add(-15 * 24 * time.Hour)
	lastUsedAt := now.Add(-3 * time.Hour)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{
		CodexFingerprintMinSessionAgeHours: 14 * 24,
		CodexFingerprintIdleGateMinutes:    120,
	}}}
	account := &Account{ID: 27, LastUsedAt: &lastUsedAt}
	state := CodexFingerprintState{Epoch: 3, EpochStartedAt: startedAt}

	require.True(t, svc.shouldRotateCodexFingerprintSession(account, state, now))
	recent := now.Add(-time.Hour)
	account.LastUsedAt = &recent
	require.False(t, svc.shouldRotateCodexFingerprintSession(account, state, now))
	account.LastUsedAt = &lastUsedAt
	state.EpochStartedAt = now.Add(-7 * 24 * time.Hour)
	require.False(t, svc.shouldRotateCodexFingerprintSession(account, state, now))
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
