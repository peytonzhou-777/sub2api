package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexFingerprintSessionRepoStub struct {
	AccountRepository
	state               CodexFingerprintState
	boundEpoch          int64
	boundEpochStartedAt time.Time
	lastRequest         CodexFingerprintSessionRequest
	matchedHash         string
	boundScopeHash      string
	resolveCalls        int
}

// configureCodexFingerprintV3TestState 为整链路测试装配 UUIDv7 指纹状态。
func configureCodexFingerprintV3TestState(svc *OpenAIGatewayService, account *Account) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{
		Seed:           testCodexFingerprintV3Seed(),
		Version:        codexFingerprintAlgorithmV3,
		Epoch:          3,
		EpochStartedAt: startedAt,
	}
	account.CodexFingerprintSeed = state.Seed
	account.CodexFingerprintVersion = state.Version
	account.CodexFingerprintEpoch = state.Epoch
	account.CodexFingerprintEpochStartedAt = &startedAt
	if svc.cfg == nil {
		svc.cfg = &config.Config{}
	}
	svc.cfg.Gateway.CodexFingerprintSecret = string(testCodexFingerprintV3Secret())
	svc.accountRepo = &codexFingerprintSessionRepoStub{state: state}
}

type codexOAuthProbeTestRepo struct {
	AccountRepository
	accounts map[int64]*Account
	states   map[int64]CodexFingerprintState
}

func codexFingerprintV3TestStateForAccount(accountID int64) CodexFingerprintState {
	return CodexFingerprintState{
		Seed:           fmt.Sprintf("%064x", accountID),
		Version:        codexFingerprintAlgorithmV3,
		Epoch:          3,
		EpochStartedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (r *codexOAuthProbeTestRepo) GetByID(ctx context.Context, accountID int64) (*Account, error) {
	if account := r.accounts[accountID]; account != nil {
		return account, nil
	}
	if r.AccountRepository == nil {
		return nil, nil
	}
	return r.AccountRepository.GetByID(ctx, accountID)
}

func (r *codexOAuthProbeTestRepo) ValidateCodexFingerprintSecret(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (r *codexOAuthProbeTestRepo) GetOrInitializeCodexFingerprintState(_ context.Context, accountID int64, _ time.Time) (*CodexFingerprintState, error) {
	state := r.states[accountID]
	if !state.valid() {
		state = codexFingerprintV3TestStateForAccount(accountID)
	}
	return &state, nil
}

func (r *codexOAuthProbeTestRepo) ResolveCodexFingerprintSessionState(
	_ context.Context,
	request CodexFingerprintSessionRequest,
) (*CodexFingerprintSessionResolution, error) {
	state := r.states[request.AccountID]
	if !state.valid() {
		state = codexFingerprintV3TestStateForAccount(request.AccountID)
	}
	matchedThreadSourceHash := ""
	if len(request.ThreadSourceHashes) > 0 {
		matchedThreadSourceHash = request.ThreadSourceHashes[0]
	}
	return &CodexFingerprintSessionResolution{
		State:                   state,
		BoundEpoch:              state.Epoch,
		BoundEpochStartedAt:     state.EpochStartedAt,
		MatchedThreadSourceHash: matchedThreadSourceHash,
		BoundSessionScopeHash:   request.SessionScopeHash,
		BoundScopeVersion:       request.SessionScopeVersion,
		BoundSessionSlot:        request.SessionSlot,
		BoundSessionSlotCount:   request.SessionSlotCount,
		Created:                 true,
	}, nil
}

// configureOpenAICodexGatewayTest 为 OAuth 出站测试提供生产等价的 session v3 状态。
func configureOpenAICodexGatewayTest(svc *OpenAIGatewayService) {
	if svc.cfg == nil {
		svc.cfg = &config.Config{}
	}
	svc.cfg.Gateway.CodexFingerprintSecret = string(testCodexFingerprintV3Secret())
	baseRepo := svc.accountRepo
	svc.accountRepo = &codexOAuthProbeTestRepo{
		AccountRepository: baseRepo,
		accounts:          make(map[int64]*Account),
		states:            make(map[int64]CodexFingerprintState),
	}
}

func requireCodexFingerprintV3UUID(t *testing.T, value string) {
	t.Helper()
	parsed, err := uuid.Parse(value)
	require.NoError(t, err, "Codex v3 指纹必须是合法 UUID: %s", value)
	require.Equal(t, uuid.Version(7), parsed.Version())
}

// configureOpenAICodexOAuthProbeTest 让管理员探测测试走与生产一致的 session v3 指纹路径。
func configureOpenAICodexOAuthProbeTest(svc *AccountTestService, accounts ...*Account) {
	if svc.cfg == nil {
		svc.cfg = &config.Config{}
	}
	svc.cfg.Gateway.CodexFingerprintSecret = string(testCodexFingerprintV3Secret())
	baseRepo := svc.accountRepo
	repo := &codexOAuthProbeTestRepo{
		AccountRepository: baseRepo,
		accounts:          make(map[int64]*Account, len(accounts)),
		states:            make(map[int64]CodexFingerprintState, len(accounts)),
	}
	for _, account := range accounts {
		if account == nil {
			continue
		}
		if account.Extra == nil {
			account.Extra = make(map[string]any)
		}
		account.Extra[codexFingerprintModeExtraKey] = string(codexFingerprintSession)
		account.Extra[codexOutboundProfileExtraKey] = CodexOutboundProfileCLI0149
		state := codexFingerprintV3TestStateForAccount(account.ID)
		account.CodexFingerprintSeed = state.Seed
		account.CodexFingerprintVersion = state.Version
		account.CodexFingerprintEpoch = state.Epoch
		account.CodexFingerprintEpochStartedAt = &state.EpochStartedAt
		repo.accounts[account.ID] = account
		repo.states[account.ID] = state
	}
	svc.accountRepo = repo
}

func TestCodexFingerprintStateAcceptsOnlyV3(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{
		Seed: testCodexFingerprintV3Seed(), Epoch: 1, EpochStartedAt: startedAt,
	}
	state.Version = codexFingerprintAlgorithmV3
	require.True(t, state.valid())

	state.Version = "v2"
	require.False(t, state.valid())
}

func (r *codexFingerprintSessionRepoStub) ValidateCodexFingerprintSecret(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (r *codexFingerprintSessionRepoStub) ResolveCodexFingerprintSessionState(
	_ context.Context,
	request CodexFingerprintSessionRequest,
) (*CodexFingerprintSessionResolution, error) {
	r.resolveCalls++
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
	boundEpoch := r.boundEpoch
	if boundEpoch <= 0 {
		boundEpoch = state.Epoch
	}
	boundEpochStartedAt := r.boundEpochStartedAt
	if boundEpochStartedAt.IsZero() {
		boundEpochStartedAt = state.EpochStartedAt
	}
	return &CodexFingerprintSessionResolution{
		State:                   state,
		BoundEpoch:              boundEpoch,
		BoundEpochStartedAt:     boundEpochStartedAt,
		MatchedThreadSourceHash: matchedHash,
		BoundSessionScopeHash:   boundScopeHash,
	}, nil
}

func TestPrepareCodexFingerprintForAdmissionIsReusedByOutboundAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session-id", "root-session")
	c.Set("api_key", &APIKey{ID: 101, UserID: 17})
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed: testCodexFingerprintV3Seed(), CodexFingerprintVersion: codexFingerprintAlgorithmV3,
		CodexFingerprintEpoch: 3, CodexFingerprintEpochStartedAt: &startedAt,
	}
	repo := &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
		Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3, Epoch: 3, EpochStartedAt: startedAt,
	}}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
		accountRepo: repo,
	}
	body := []byte(`{"client_metadata":{"session_id":"root-session","thread_id":"root-thread"}}`)

	require.NoError(t, svc.PrepareCodexFingerprintForAdmission(context.Background(), c, account, body, false))
	prepared := stagedCodexFingerprintIDs(c)
	require.NotNil(t, prepared)
	reused, err := svc.prepareCodexFingerprintForAttempt(context.Background(), c, account, body, false)
	require.NoError(t, err)
	require.Same(t, prepared, reused)
	require.Equal(t, 1, repo.resolveCalls)
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
		CodexFingerprintSeed:           testCodexFingerprintV3Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV3,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexFingerprintSecret: string(testCodexFingerprintV3Secret()),
		}},
		accountRepo: &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
			Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3,
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

func TestResolveCodexFingerprintContextForAttemptInheritsLegacySessionScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session-id", "current-client-session")

	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{
		Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3,
		Epoch: 3, EpochStartedAt: startedAt,
	}
	account := &Account{
		ID:                             27,
		Platform:                       PlatformOpenAI,
		Type:                           AccountTypeOAuth,
		Extra:                          map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed:           state.Seed,
		CodexFingerprintVersion:        state.Version,
		CodexFingerprintEpoch:          state.Epoch,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexFingerprintSecret: string(testCodexFingerprintV3Secret()),
		}},
		accountRepo: &codexFingerprintSessionRepoStub{
			state: state, boundScopeHash: codexFingerprintSessionScopeHash(
				testCodexFingerprintV3Secret(), "protocol:responses:transport:http",
			),
		},
	}

	fingerprint, err := svc.resolveCodexFingerprintContextForAttempt(
		context.Background(), c, account, c.Request.Header, []byte(`{"input":"hello"}`),
	)
	require.NoError(t, err)
	require.NotEmpty(t, fingerprint.SessionID())
	require.Equal(t, codexFingerprintScopeV1, fingerprint.sessionScopeVersion)
}

func TestResolveCodexFingerprintSessionSlotIsStableAndBounded(t *testing.T) {
	secret := testCodexFingerprintV3Secret()
	seed, err := decodeCodexFingerprintSeed(testCodexFingerprintV3Seed())
	require.NoError(t, err)

	first := resolveCodexFingerprintSessionSlot(secret, seed, "client:codex", "user:17", "root-a", 2)
	second := resolveCodexFingerprintSessionSlot(secret, seed, "client:codex", "user:17", "root-a", 2)
	require.Equal(t, first, second)
	require.GreaterOrEqual(t, first, 0)
	require.Less(t, first, 2)
	require.Zero(t, resolveCodexFingerprintSessionSlot(secret, seed, "client:codex", "user:17", "root-a", 1))
	for userID := 18; userID <= 40; userID++ {
		require.Equal(t,
			resolveCodexFingerprintSessionSlot(secret, seed, "client:codex", "user:17", "root-a", 4),
			resolveCodexFingerprintSessionSlot(secret, seed, "client:codex", fmt.Sprintf("user:%d", userID), "root-a", 4),
			"有根谱系时不得因本站用户或 API Key 变化改变槽位",
		)
	}
}

func TestResolveCodexFingerprintMissingRootUsesStableUserFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession), codexSessionSlotCountExtraKey: 4},
		CodexFingerprintSeed: testCodexFingerprintV3Seed(), CodexFingerprintVersion: codexFingerprintAlgorithmV3,
		CodexFingerprintEpoch: 3, CodexFingerprintEpochStartedAt: &startedAt,
	}
	repo := &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
		Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3, Epoch: 3, EpochStartedAt: startedAt,
	}}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
		accountRepo: repo,
	}
	resolve := func() *CodexFingerprintContext {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Set("api_key", &APIKey{ID: 101, UserID: 17})
		fp, err := svc.resolveCodexFingerprintContextForAttempt(
			context.Background(), c, account, c.Request.Header, []byte(`{"model":"gpt-5.4","messages":[]}`),
		)
		require.NoError(t, err)
		return fp
	}

	first := resolve()
	second := resolve()
	require.Equal(t, first.sessionSlot, second.sessionSlot)
	require.Equal(t, first.SessionID(), second.SessionID())
	require.NotEqual(t, first.ThreadID(), second.ThreadID(), "临时 Thread 可变化，但不得改变用户回退槽位")
}

func TestResolveCodexFingerprintScopeCandidatesPreferCurrentSlotMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)

	candidates := resolveCodexFingerprintScopeCandidates(c, nil, true, "protocol:responses", 0, 4)
	require.NotEmpty(t, candidates)
	require.Equal(t, codexFingerprintScopeCandidate{
		scope: "protocol:responses:slot:0", version: codexFingerprintScopeV2, slot: 0, count: 4,
	}, candidates[0])
}

func TestResolveCodexFingerprintTwoSlotsStayTransportNeutral(t *testing.T) {
	gin.SetMode(gin.TestMode)
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession), codexSessionSlotCountExtraKey: 2},
		CodexFingerprintSeed: testCodexFingerprintV3Seed(), CodexFingerprintVersion: codexFingerprintAlgorithmV3,
		CodexFingerprintEpoch: 3, CodexFingerprintEpochStartedAt: &startedAt,
	}
	repo := &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
		Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3, Epoch: 3, EpochStartedAt: startedAt,
	}}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
		accountRepo: repo,
	}
	resolve := func(transport OpenAIClientTransport, root string) *CodexFingerprintContext {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Set("api_key", &APIKey{ID: 101, UserID: 17})
		SetOpenAIClientTransport(c, transport)
		body := []byte(fmt.Sprintf(`{"prompt_cache_key":%q,"client_metadata":{"session_id":%q,"thread_id":%q}}`, root, root, root))
		fingerprint, err := svc.resolveCodexFingerprintContextForAttempt(context.Background(), c, account, c.Request.Header, body)
		require.NoError(t, err)
		return fingerprint
	}

	httpFingerprint := resolve(OpenAIClientTransportHTTP, "root-a")
	wsFingerprint := resolve(OpenAIClientTransportWS, "root-a")
	require.Equal(t, httpFingerprint.SessionID(), wsFingerprint.SessionID())
	require.Equal(t, httpFingerprint.sessionScopeHash, wsFingerprint.sessionScopeHash)
	require.Equal(t, codexFingerprintScopeV2, httpFingerprint.sessionScopeVersion)
	require.Equal(t, 2, httpFingerprint.sessionSlotCount)

	seen := map[int]struct{}{httpFingerprint.sessionSlot: {}}
	for index := 0; index < 64 && len(seen) < 2; index++ {
		candidate := resolve(OpenAIClientTransportHTTP, fmt.Sprintf("root-%d", index))
		seen[candidate.sessionSlot] = struct{}{}
	}
	require.Len(t, seen, 2, "独立根谱系应能稳定分布到两个 Session 槽位")
}

func TestResolveCodexFingerprintContextScopesThreadsAndUsesThreadRequestID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID:       27,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			codexFingerprintModeExtraKey: string(codexFingerprintSession), codexSessionSlotCountExtraKey: 2,
		},
		CodexFingerprintSeed:           testCodexFingerprintV3Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV3,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	repo := &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
		Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3,
		Epoch: 3, EpochStartedAt: startedAt,
	}}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexFingerprintSecret: string(testCodexFingerprintV3Secret()),
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
	require.Equal(t, httpFirst.SessionID(), wsFirst.SessionID(), "HTTP 与 WS 只是传输维度，必须共用逻辑 Session")
	require.Equal(t, httpScopeHash, httpSecondScopeHash)
	require.Equal(t, wsScopeHash, wsSecondScopeHash)
	require.Equal(t, httpScopeHash, wsScopeHash, "HTTP 与 WS 必须共享持久化轮换作用域")
	require.NotEqual(t, httpFirst.ThreadID(), httpSecond.ThreadID(), "不同 API Key 的同名客户端 Session 必须隔离")
	require.Equal(t, httpFirst.ThreadID(), httpFirst.RequestID(), "CodexCLI 0.149.0 的请求头标识必须复用 Thread ID")
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
	require.Equal(t, codexResponses, codexResponsesWS, "身份被统一后 HTTP/WS 必须共享逻辑 Session")
	require.NotEqual(t, codexResponses, codexChat, "身份不可见时按语义协议隔离")

	codexVisible := resolve("/v1/responses", "codex_cli_rs/0.101.0", "codex_cli_rs", false, OpenAIClientTransportHTTP)
	vscodeVisible := resolve("/v1/responses", "codex_vscode/0.101.0", "codex_vscode", false, OpenAIClientTransportHTTP)
	openClawFallback := resolve("/v1/responses", "OpenClaw/2026.9.0", "openclaw", false, OpenAIClientTransportHTTP)
	codexVisibleChat := resolve("/v1/chat/completions", "codex_cli_rs/0.102.0", "codex_cli_rs", false, OpenAIClientTransportHTTP)
	codexVisibleWS := resolve("/v1/responses", "codex_cli_rs/0.102.0", "codex_cli_rs", false, OpenAIClientTransportWS)
	require.NotEqual(t, codexVisible, vscodeVisible, "可配对且真实出站的不同官方客户端必须分槽")
	require.Equal(t, codexVisible, codexVisibleChat, "同一可见客户端不因协议或版本变化切换 Session")
	require.Equal(t, codexVisible, codexVisibleWS, "同一可见客户端不因 HTTP/WS 切换 Session")
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

func TestCodexFingerprintV3RewritesRequestAndPromptCacheIDs(t *testing.T) {
	fp, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 4,
		codexFingerprintSession, "", codexFingerprintOriginalIDs{
			clientSessionID: "client-session",
			turnID:          "turn-1",
		},
	)
	require.NoError(t, err)
	ids := codexFingerprintIDsFromContext(fp)
	headers := http.Header{"X-Client-Request-Id": []string{"request-1"}}
	body := map[string]any{"prompt_cache_key": "prompt-1"}

	applyCodexFingerprintHeaders(headers, ids)
	require.True(t, applyCodexFingerprintClientMetadata(body, ids))

	assert.Equal(t, fp.ThreadID(), headers.Get("x-client-request-id"))
	assert.Equal(t, fp.PromptCacheKey(), body["prompt_cache_key"])
	assert.Equal(t, fp.SessionID(), body["prompt_cache_key"])
	metadata, ok := body["client_metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, fp.ThreadID(), metadata["thread_id"])
}

func TestCodexFingerprintV3FullModeUsesOneAccountThread(t *testing.T) {
	first, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 5,
		codexFingerprintFull, "", codexFingerprintOriginalIDs{clientScope: "client:codex", threadScope: "api-key:101:client:codex", clientSessionID: "client-a", turnID: "turn-a"},
	)
	require.NoError(t, err)
	second, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 5,
		codexFingerprintFull, "", codexFingerprintOriginalIDs{clientScope: "client:codex", threadScope: "api-key:101:client:codex", clientSessionID: "client-b", turnID: "turn-b"},
	)
	require.NoError(t, err)
	otherClient, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 5,
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
		CodexFingerprintOldEpochGraceHours:  48,
	}}}
	thresholds := buildCodexFingerprintRotationThresholds(svc.codexFingerprintEpochPolicy(context.Background()), 27, strings.Repeat("cd", 32), now)
	require.Equal(t, now.Add(-72*time.Hour), thresholds.MinAgeBefore)
	require.Equal(t, now.Add(-168*time.Hour), thresholds.MaxAgeBefore)
	require.Equal(t, now.Add(-2*time.Hour), thresholds.IdleBefore)
}

func TestCodexFingerprintThreadSourceHashDoesNotExposeSource(t *testing.T) {
	source := "client-session-sensitive-value"
	first := codexFingerprintThreadSourceHash(testCodexFingerprintV3Secret(), source)
	second := codexFingerprintThreadSourceHash(testCodexFingerprintV3Secret(), source)
	otherCluster := codexFingerprintThreadSourceHash([]byte("abcdef0123456789abcdef0123456789"), source)

	require.Len(t, first, 64)
	require.Equal(t, first, second)
	require.NotEqual(t, source, first)
	require.NotEqual(t, first, otherCluster)
}

func testCodexFingerprintV3Secret() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

func testCodexFingerprintV3Seed() string {
	return strings.Repeat("ab", codexFingerprintSeedBytes)
}

func newTestCodexFingerprintContextV3(
	clusterSecret []byte,
	seedHex string,
	epoch int64,
	mode codexFingerprintMode,
	configuredDeviceID string,
	original codexFingerprintOriginalIDs,
) (*CodexFingerprintContext, error) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(epoch) * time.Hour)
	return newCodexFingerprintContextV3(
		clusterSecret, seedHex, epoch, startedAt, mode, configuredDeviceID, original,
	)
}

func TestCodexFingerprintV3ContextKeepsOneSessionWithMultipleThreads(t *testing.T) {
	base := codexFingerprintOriginalIDs{clientScope: "client:codex", threadScope: "api-key:101:client:codex", clientSessionID: "client-session-a", turnID: "turn-a"}
	first, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
		codexFingerprintSession, "", base,
	)
	require.NoError(t, err)

	secondInput := base
	secondInput.clientSessionID = "client-session-b"
	second, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
		codexFingerprintSession, "", secondInput,
	)
	require.NoError(t, err)

	assert.Equal(t, first.InstallationID(), second.InstallationID())
	assert.Equal(t, first.SessionID(), second.SessionID())
	assert.NotEqual(t, first.ThreadID(), second.ThreadID())
	assert.Equal(t, int64(7), first.SessionEpoch())
	assert.Equal(t, codexFingerprintAlgorithmV3, first.AlgorithmVersion())
}

func TestCodexFingerprintV3ContextSeparatesClientScopes(t *testing.T) {
	codexInput := codexFingerprintOriginalIDs{
		clientScope:     "client:codex",
		threadScope:     "api-key:101:client:codex",
		clientSessionID: "shared-client-session",
		turnID:          "shared-turn",
		windowID:        "shared-window",
	}
	openClawInput := codexInput
	openClawInput.clientScope = "client:openclaw"
	openClawInput.threadScope = "api-key:101:client:openclaw"
	codex, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
		codexFingerprintSession, "", codexInput,
	)
	require.NoError(t, err)
	openClaw, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
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

func TestCodexFingerprintV3ContextSharesClientSessionButSeparatesAPIKeyThreads(t *testing.T) {
	firstInput := codexFingerprintOriginalIDs{
		clientScope:     "client:codex",
		threadScope:     "api-key:101:client:codex",
		clientSessionID: "shared-client-session",
		turnID:          "shared-turn",
		windowID:        "shared-window",
	}
	secondInput := firstInput
	secondInput.threadScope = "api-key:202:client:codex"
	first, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
		codexFingerprintSession, "", firstInput,
	)
	require.NoError(t, err)
	second, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
		codexFingerprintSession, "", secondInput,
	)
	require.NoError(t, err)

	require.Equal(t, first.InstallationID(), second.InstallationID())
	require.Equal(t, first.SessionID(), second.SessionID())
	require.Equal(t, first.PromptCacheKey(), second.PromptCacheKey())
	require.Equal(t, first.SessionID(), first.PromptCacheKey())
	for _, pair := range [][2]string{
		{first.ThreadID(), second.ThreadID()},
		{first.TurnID(), second.TurnID()},
		{first.WindowID(), second.WindowID()},
		{first.RequestID(), second.RequestID()},
	} {
		require.NotEqual(t, pair[0], pair[1])
	}
}

func TestCodexFingerprintV3ContextSeparatesAccountsClustersEpochsAndKinds(t *testing.T) {
	input := codexFingerprintOriginalIDs{clientSessionID: "client-session", turnID: "same-source"}
	base, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 3,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)

	otherSeed, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), strings.Repeat("cd", codexFingerprintSeedBytes), 3,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)
	otherCluster, err := newTestCodexFingerprintContextV3(
		[]byte("abcdef0123456789abcdef0123456789"), testCodexFingerprintV3Seed(), 3,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)
	nextEpoch, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 4,
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

func TestCodexFingerprintV3ContextReusesLogicalTurnWithCodexCLI0149UUIDShapes(t *testing.T) {
	input := codexFingerprintOriginalIDs{
		clientSessionID: "client-session",
		turnID:          "logical-turn-123",
		windowID:        "window-1",
	}
	first, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 9,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)
	second, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 9,
		codexFingerprintSession, "", input,
	)
	require.NoError(t, err)

	assert.Equal(t, first.TurnID(), second.TurnID(), "同一逻辑 turn 的重试必须复用映射值")
	assert.Equal(t, first.SessionID(), first.PromptCacheKey())
	parsedInstallation, parseErr := uuid.Parse(first.InstallationID())
	require.NoError(t, parseErr)
	assert.Equal(t, uuid.Version(4), parsedInstallation.Version())
	assert.Equal(t, uuid.RFC4122, parsedInstallation.Variant())
	for _, value := range []string{
		first.SessionID(), first.PromptCacheKey(), first.ThreadID(), first.TurnID(), first.RequestID(),
	} {
		parsed, parseErr := uuid.Parse(value)
		require.NoError(t, parseErr)
		assert.Equal(t, uuid.Version(7), parsed.Version())
		assert.Equal(t, uuid.RFC4122, parsed.Variant())
	}
	assert.Equal(t, first.ThreadID(), first.RequestID())
	assert.Equal(t, first.ThreadID()+":0", first.WindowID())
}

func TestCodexFingerprintV3PreservesOriginalUUIDv7Timestamps(t *testing.T) {
	startedAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	original := codexFingerprintOriginalIDs{
		clientScope:     "client:codex:transport:http",
		threadScope:     "api-key:101:client:codex:transport:http",
		clientSessionID: "0198a000-0000-7000-8000-000000000001",
		threadID:        "0198a001-0000-7000-8000-000000000002",
		parentThreadID:  "0198a002-0000-7000-8000-000000000003",
		forkedThreadID:  "0198a003-0000-7000-8000-000000000004",
		turnID:          "0198a004-0000-7000-8000-000000000005",
		windowID:        "0198a001-0000-7000-8000-000000000002:5",
	}
	fingerprint, err := newCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 9, startedAt,
		codexFingerprintSession, "", original,
	)
	require.NoError(t, err)

	assert.Equal(t, codexFingerprintUUIDV7UnixMilli(uuid.MustParse(original.threadID)), codexFingerprintUUIDV7UnixMilli(uuid.MustParse(fingerprint.ThreadID())))
	assert.Equal(t, codexFingerprintUUIDV7UnixMilli(uuid.MustParse(original.turnID)), codexFingerprintUUIDV7UnixMilli(uuid.MustParse(fingerprint.TurnID())))
	assert.Equal(t, fingerprint.ThreadID()+":5", fingerprint.WindowID())
	assert.Equal(t, codexFingerprintUUIDV7UnixMilli(uuid.MustParse(original.parentThreadID)), codexFingerprintUUIDV7UnixMilli(uuid.MustParse(fingerprint.parentThreadID)))
	assert.Equal(t, codexFingerprintUUIDV7UnixMilli(uuid.MustParse(original.forkedThreadID)), codexFingerprintUUIDV7UnixMilli(uuid.MustParse(fingerprint.forkedThreadID)))
	assert.Equal(t, fingerprint.ThreadID(), fingerprint.RequestID())
}

func TestCodexFingerprintV3SessionAndPromptUseSameStableUUIDv7(t *testing.T) {
	startedAt := time.Date(2026, 8, 20, 12, 34, 56, 789*int(time.Millisecond), time.UTC)
	input := codexFingerprintOriginalIDs{
		clientScope:     "client:codex:transport:http",
		threadScope:     "api-key:101:client:codex:transport:http",
		clientSessionID: "client-session",
		threadID:        "root-thread",
		turnID:          "logical-turn-123",
	}
	derive := func(scope string, epoch int64, epochStartedAt time.Time) *CodexFingerprintContext {
		candidate := input
		candidate.clientScope = scope
		fp, err := newCodexFingerprintContextV3(
			testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), epoch, epochStartedAt,
			codexFingerprintSession, "", candidate,
		)
		require.NoError(t, err)
		require.Equal(t, fp.SessionID(), fp.PromptCacheKey())
		return fp
	}

	first := derive(input.clientScope, 9, startedAt)
	second := derive(input.clientScope, 9, startedAt)
	require.Equal(t, first.SessionID(), second.SessionID())
	require.Equal(t, codexFingerprintAlgorithmV3, first.AlgorithmVersion())
	parsed, err := uuid.Parse(first.SessionID())
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), parsed.Version())
	require.Equal(t, uuid.RFC4122, parsed.Variant())
	require.Equal(t, startedAt.UnixMilli(), codexFingerprintUUIDV7UnixMilli(parsed))

	otherScope := derive("client:openclaw:transport:http", 9, startedAt)
	otherEpoch := derive(input.clientScope, 10, startedAt.Add(time.Hour))
	require.NotEqual(t, first.SessionID(), otherScope.SessionID())
	require.NotEqual(t, first.SessionID(), otherEpoch.SessionID())
	require.Equal(t, startedAt.Add(time.Hour).UnixMilli(), codexFingerprintUUIDV7UnixMilli(uuid.MustParse(otherEpoch.SessionID())))

	_, err = newCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 9, time.Time{},
		codexFingerprintSession, "", input,
	)
	require.ErrorIs(t, err, errCodexFingerprintEpochTimeInvalid)
}

func TestResolveCodexFingerprintV3UsesBoundThreadEpochTime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	currentStartedAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	boundStartedAt := time.Date(2026, 8, 10, 8, 30, 0, 0, time.UTC)
	state := CodexFingerprintState{
		Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3,
		Epoch: 11, EpochStartedAt: currentStartedAt,
	}
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                          map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed:           state.Seed,
		CodexFingerprintVersion:        state.Version,
		CodexFingerprintEpoch:          state.Epoch,
		CodexFingerprintEpochStartedAt: &currentStartedAt,
	}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{
			CodexFingerprintSecret: string(testCodexFingerprintV3Secret()),
		}},
		accountRepo: &codexFingerprintSessionRepoStub{
			state: state, boundEpoch: 7, boundEpochStartedAt: boundStartedAt,
		},
	}
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session-id", "existing-thread-session")
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	fingerprint, err := svc.resolveCodexFingerprintContextForAttempt(
		context.Background(), c, account, c.Request.Header,
		[]byte(`{"client_metadata":{"thread_id":"existing-thread"},"input":"hello"}`),
	)
	require.NoError(t, err)
	require.Equal(t, int64(7), fingerprint.SessionEpoch())
	require.Equal(t, boundStartedAt.UnixMilli(), codexFingerprintUUIDV7UnixMilli(uuid.MustParse(fingerprint.SessionID())))
	require.NotEqual(t, currentStartedAt.UnixMilli(), codexFingerprintUUIDV7UnixMilli(uuid.MustParse(fingerprint.SessionID())))
}

func codexFingerprintUUIDV7UnixMilli(value uuid.UUID) int64 {
	return int64(value[0])<<40 |
		int64(value[1])<<32 |
		int64(value[2])<<24 |
		int64(value[3])<<16 |
		int64(value[4])<<8 |
		int64(value[5])
}

func TestCodexPromptCacheKeyFollowsConvergedSessionScopes(t *testing.T) {
	startedAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	baseInput := codexFingerprintOriginalIDs{
		clientScope:     "client:codex:transport:http",
		threadScope:     "api-key:101:client:codex:transport:http",
		clientSessionID: "root-session",
		threadID:        "root-thread",
		turnID:          "turn-1",
		windowID:        "window-1",
	}
	derive := func(secret []byte, seed string, epoch int64, input codexFingerprintOriginalIDs) *CodexFingerprintContext {
		fp, err := newCodexFingerprintContextV3(secret, seed, epoch, startedAt, codexFingerprintSession, "", input)
		require.NoError(t, err)
		require.Equal(t, fp.SessionID(), fp.PromptCacheKey())
		return fp
	}

	base := derive(testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7, baseInput)
	repeated := derive(testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7, baseInput)
	require.Equal(t, base.SessionID(), repeated.SessionID())

	otherDownstream := baseInput
	otherDownstream.threadScope = "api-key:202:client:codex:transport:http"
	otherDownstream.clientSessionID = "other-client-session"
	otherDownstream.threadID = "other-thread"
	otherDownstream.turnID = "other-turn"
	otherDownstream.windowID = "other-window"
	downstreamFP := derive(testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7, otherDownstream)
	require.Equal(t, base.SessionID(), downstreamFP.SessionID(), "下游用户和逻辑会话不得拆分账号级缓存身份")

	otherClientInput := baseInput
	otherClientInput.clientScope = "client:openclaw:transport:http"
	otherClient := derive(testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7, otherClientInput)
	otherCluster := derive([]byte("abcdef0123456789abcdef0123456789"), testCodexFingerprintV3Seed(), 7, baseInput)
	otherAccount := derive(testCodexFingerprintV3Secret(), strings.Repeat("cd", codexFingerprintSeedBytes), 7, baseInput)
	otherEpoch := derive(testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 8, baseInput)
	for _, fp := range []*CodexFingerprintContext{otherClient, otherCluster, otherAccount, otherEpoch} {
		require.NotEqual(t, base.SessionID(), fp.SessionID())
		require.NotEqual(t, base.PromptCacheKey(), fp.PromptCacheKey())
	}
}

func TestCodexPromptCacheKeyRespectsModeBoundary(t *testing.T) {
	startedAt := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	input := codexFingerprintOriginalIDs{
		clientScope:     "client:codex",
		threadScope:     "api-key:101:client:codex",
		clientSessionID: "client-session",
		threadID:        "root-thread",
		turnID:          "root-turn",
	}
	device, err := newCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7, startedAt, codexFingerprintDevice, "", input,
	)
	require.NoError(t, err)
	require.Empty(t, device.SessionID())
	require.Empty(t, device.PromptCacheKey(), "device 模式不得在未收敛 Session 时强制账号级缓存身份")

	for _, mode := range []codexFingerprintMode{codexFingerprintSession, codexFingerprintFull} {
		t.Run(string(mode), func(t *testing.T) {
			fp, err := newCodexFingerprintContextV3(
				testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7, startedAt, mode, "", input,
			)
			require.NoError(t, err)
			require.NotEmpty(t, fp.PromptCacheKey())
			require.Equal(t, fp.SessionID(), fp.PromptCacheKey())
		})
	}

}

func TestCodexFingerprintV3ContextHonorsConfiguredDeviceAndDeviceMode(t *testing.T) {
	ctx, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 1,
		codexFingerprintDevice, "configured-device-id", codexFingerprintOriginalIDs{},
	)
	require.NoError(t, err)
	assert.Equal(t, "configured-device-id", ctx.InstallationID())
	assert.Empty(t, ctx.SessionID())
	assert.Empty(t, ctx.ThreadID())
}

func TestCodexFingerprintV3ContextRejectsUnsafeState(t *testing.T) {
	tests := []struct {
		name   string
		secret []byte
		seed   string
		epoch  int64
		input  codexFingerprintOriginalIDs
		want   error
	}{
		{"短集群密钥", []byte("short"), testCodexFingerprintV3Seed(), 1, codexFingerprintOriginalIDs{clientSessionID: "s"}, errCodexFingerprintSecretInvalid},
		{"非法种子", testCodexFingerprintV3Secret(), strings.Repeat("zz", codexFingerprintSeedBytes), 1, codexFingerprintOriginalIDs{clientSessionID: "s"}, errCodexFingerprintSeedInvalid},
		{"epoch 未初始化", testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 0, codexFingerprintOriginalIDs{clientSessionID: "s"}, errCodexFingerprintEpochInvalid},
		{"缺少 Thread 来源", testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 1, codexFingerprintOriginalIDs{}, errCodexFingerprintThreadMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := newTestCodexFingerprintContextV3(tt.secret, tt.seed, tt.epoch, codexFingerprintSession, "", tt.input)
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
		CodexFingerprintSeed:           testCodexFingerprintV3Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV3,
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
	state := CodexFingerprintState{Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3, Epoch: 3, EpochStartedAt: startedAt}
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed: state.Seed, CodexFingerprintVersion: state.Version,
		CodexFingerprintEpoch: state.Epoch, CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
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

func TestApplyCodexFingerprintForAttemptMakesPromptCacheFollowSession(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3, Epoch: 3, EpochStartedAt: startedAt}
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed: state.Seed, CodexFingerprintVersion: state.Version,
		CodexFingerprintEpoch: state.Epoch, CodexFingerprintEpochStartedAt: &startedAt,
	}
	repo := &codexFingerprintSessionRepoStub{state: state}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
		accountRepo: repo,
	}
	const originalPromptCacheKey = "018f0c7a-b740-7cc0-98c7-4f4a3f975e52"
	body := []byte(`{"prompt_cache_key":"` + originalPromptCacheKey + `","client_metadata":{"session_id":"` + originalPromptCacheKey + `","thread_id":"root-thread"},"input":"hello"}`)
	apply := func(transport OpenAIClientTransport, userID, apiKeyID int64, requestBody []byte) (sessionID, promptCacheKey string, headers http.Header, outgoing []byte, request CodexFingerprintSessionRequest) {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.141.0")
		c.Set("api_key", &APIKey{ID: apiKeyID})
		SetOpenAIClientTransport(c, transport)
		requestContext := context.Background()
		if userID > 0 {
			requestContext = context.WithValue(requestContext, ctxkey.UserID, userID)
		}
		updated, err := svc.applyCodexFingerprintForAttempt(requestContext, c, account, requestBody, false, true)
		require.NoError(t, err)
		outgoingHeaders := make(http.Header)
		applyStagedCodexFingerprintHeaders(c, account, outgoingHeaders)
		return gjson.GetBytes(updated, "client_metadata.session_id").String(),
			gjson.GetBytes(updated, "prompt_cache_key").String(), outgoingHeaders, updated, repo.lastRequest
	}
	assertSessionScope := func(sessionID, promptCacheKey string, request CodexFingerprintSessionRequest) {
		t.Helper()
		require.Equal(t, sessionID, promptCacheKey)
		require.Equal(t, codexFingerprintScopeV2, request.SessionScopeVersion)
		require.Equal(t, DefaultSessionPersonaSlotCount, request.SessionSlotCount)
		scope := resolveCodexFingerprintSlottedSessionScope("protocol:responses", request.SessionSlot, request.SessionSlotCount)
		require.Equal(t, codexFingerprintSessionScopeHashV2(testCodexFingerprintV3Secret(), scope), request.SessionScopeHash)
	}

	httpSession, httpCache, httpHeaders, httpBody, httpRequest := apply(OpenAIClientTransportHTTP, 42, 101, body)
	wsSession, wsCache, _, _, wsRequest := apply(OpenAIClientTransportWS, 42, 202, body)
	assertSessionScope(httpSession, httpCache, httpRequest)
	assertSessionScope(wsSession, wsCache, wsRequest)
	require.Equal(t, httpSession, wsSession, "HTTP/WS 只负责传输，必须复用同一逻辑 Session")
	require.Equal(t, httpCache, wsCache, "缓存身份必须跟随传输无关的 Session 作用域")
	require.Equal(t, httpRequest.SessionSlot, wsRequest.SessionSlot)
	require.NotContains(t, string(httpBody), originalPromptCacheKey)
	for name, values := range httpHeaders {
		require.NotContains(t, strings.Join(values, ","), originalPromptCacheKey, name)
	}

	otherBody := []byte(`{"prompt_cache_key":"other-explicit-cache","client_metadata":{"session_id":"other-client-session","thread_id":"other-thread"},"input":"hello"}`)
	otherSession, otherCache, _, otherOutgoing, otherRequest := apply(OpenAIClientTransportHTTP, 43, 202, otherBody)
	assertSessionScope(otherSession, otherCache, otherRequest)
	require.Equal(t, httpRequest.SessionSlot == otherRequest.SessionSlot, httpSession == otherSession,
		"独立根请求仅在命中同一槽位时共享账号 Session")
	require.NotContains(t, string(otherOutgoing), "other-explicit-cache")

	missingPromptBody := []byte(`{"client_metadata":{"session_id":"third-client-session","thread_id":"third-thread"},"input":"hello"}`)
	missingSession, missingCache, _, missingOutgoing, missingRequest := apply(OpenAIClientTransportHTTP, 0, 303, missingPromptBody)
	assertSessionScope(missingSession, missingCache, missingRequest)
	require.True(t, gjson.GetBytes(missingOutgoing, "prompt_cache_key").Exists())
}

func TestApplyCodexFingerprintRawForAttemptKeepsPromptCacheAfterWSReconnect(t *testing.T) {
	account := &Account{
		ID:       27,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
	}
	svc := &OpenAIGatewayService{}
	configureCodexFingerprintV3TestState(svc, account)
	requestContext := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	body := []byte(`{"prompt_cache_key":"root-cache-lineage","client_metadata":{"session_id":"client-session","thread_id":"root-thread"},"input":"hello"}`)
	applyOnConnection := func() (string, string) {
		c, _ := gin.CreateTestContext(nil)
		c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.141.0")
		c.Set("api_key", &APIKey{ID: 101})
		SetOpenAIClientTransport(c, OpenAIClientTransportWS)
		updated, err := svc.applyCodexFingerprintRawForAttempt(requestContext, c, account, body, true)
		require.NoError(t, err)
		return gjson.GetBytes(updated, "client_metadata.session_id").String(),
			gjson.GetBytes(updated, "prompt_cache_key").String()
	}

	firstSession, firstCache := applyOnConnection()
	secondSession, secondCache := applyOnConnection()
	require.Equal(t, firstSession, firstCache)
	require.Equal(t, secondSession, secondCache)
	require.Equal(t, firstCache, secondCache)
	require.Equal(t, uuid.Version(7), uuid.MustParse(firstSession).Version())
}

func TestApplyCodexFingerprintForAttemptInjectsPromptCacheKeyWhenMissing(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3, Epoch: 3, EpochStartedAt: startedAt}
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed: state.Seed, CodexFingerprintVersion: state.Version,
		CodexFingerprintEpoch: state.Epoch, CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
		accountRepo: &codexFingerprintSessionRepoStub{state: state},
	}
	c, _ := gin.CreateTestContext(nil)
	c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set("api_key", &APIKey{ID: 101})
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)

	updated, err := svc.applyCodexFingerprintForAttempt(
		context.WithValue(context.Background(), ctxkey.UserID, int64(42)), c, account,
		[]byte(`{"client_metadata":{"session_id":"client-session"},"input":"hello"}`), false, true,
	)
	require.NoError(t, err)
	require.Equal(t,
		gjson.GetBytes(updated, "client_metadata.session_id").String(),
		gjson.GetBytes(updated, "prompt_cache_key").String(),
	)
}

func TestApplyCodexFingerprintForAttemptKeepsPromptCacheInDeviceAndOffModes(t *testing.T) {
	for _, mode := range []codexFingerprintMode{codexFingerprintDevice, codexFingerprintOff} {
		t.Run(string(mode), func(t *testing.T) {
			account := &Account{
				ID:       27,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{codexFingerprintModeExtraKey: string(mode)},
			}
			svc := &OpenAIGatewayService{}
			if mode == codexFingerprintDevice {
				configureCodexFingerprintV3TestState(svc, account)
			}
			apply := func(body []byte) []byte {
				c, _ := gin.CreateTestContext(nil)
				c.Request, _ = http.NewRequest(http.MethodPost, "/v1/responses", nil)
				SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
				updated, err := svc.applyCodexFingerprintForAttempt(context.Background(), c, account, body, false, true)
				require.NoError(t, err)
				return updated
			}

			explicit := apply([]byte(`{"prompt_cache_key":"client-cache","input":"hello"}`))
			require.Equal(t, "client-cache", gjson.GetBytes(explicit, "prompt_cache_key").String())
			missing := apply([]byte(`{"input":"hello"}`))
			require.False(t, gjson.GetBytes(missing, "prompt_cache_key").Exists())
		})
	}
}

func TestApplyCodexFingerprintForAttemptCompactOnlyStagesHeaders(t *testing.T) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	state := CodexFingerprintState{Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3, Epoch: 3, EpochStartedAt: startedAt}
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintSession)},
		CodexFingerprintSeed: state.Seed, CodexFingerprintVersion: state.Version,
		CodexFingerprintEpoch: state.Epoch, CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
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
		CodexFingerprintSeed:           testCodexFingerprintV3Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV3,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: nil,
	}

	payload, err := json.Marshal(account)
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "CodexFingerprint")
	assert.NotContains(t, string(payload), testCodexFingerprintV3Seed())
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
		parentTurnID:     "parent-turn",
		rootTurnID:       "root-turn",
		windowID:         "child-thread:2",
		subagentHeader:   "collab_spawn",
		subagentKind:     "thread_spawn",
		threadSource:     "subagent",
		isSubagent:       true,
		sessionScopeHash: strings.Repeat("cd", 32),
	}
	child, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
		codexFingerprintSession, "", original,
	)
	require.NoError(t, err)
	rootInput := original
	rootInput.threadID = original.parentThreadID
	rootInput.parentThreadID = ""
	rootInput.forkedThreadID = ""
	rootInput.parentTurnID = ""
	rootInput.rootTurnID = ""
	rootInput.subagentHeader = ""
	rootInput.subagentKind = ""
	rootInput.threadSource = ""
	rootInput.isSubagent = false
	rootInput.turnID = original.parentTurnID
	root, err := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
		codexFingerprintSession, "", rootInput,
	)
	require.NoError(t, err)

	require.Equal(t, root.SessionID(), child.SessionID())
	require.Equal(t, root.PromptCacheKey(), child.PromptCacheKey())
	require.Equal(t, root.ThreadID(), child.parentThreadID)
	require.Equal(t, root.ThreadID(), child.forkedThreadID)
	require.NotEqual(t, root.ThreadID(), child.ThreadID())
	require.Equal(t, root.turnID, child.parentTurnID)
	rootTurnInput := rootInput
	rootTurnInput.turnID = original.rootTurnID
	rootTurn, rootTurnErr := newTestCodexFingerprintContextV3(
		testCodexFingerprintV3Secret(), testCodexFingerprintV3Seed(), 7,
		codexFingerprintSession, "", rootTurnInput,
	)
	require.NoError(t, rootTurnErr)
	require.Equal(t, rootTurn.turnID, child.rootTurnID)
	require.Equal(t, child.ThreadID()+":2", child.WindowID())
	require.NotEqual(t, original.parentTurnID, child.parentTurnID)
	require.NotEqual(t, original.rootTurnID, child.rootTurnID)

	ids := codexFingerprintIDsFromContext(child)
	headers := http.Header{
		"X-Openai-Subagent":     []string{"raw-worker"},
		"X-Codex-Turn-Metadata": []string{`{"request_kind":"subagent","custom":"kept"}`},
	}
	body := map[string]any{"client_metadata": map[string]any{
		"x-codex-turn-metadata": `{"request_kind":"subagent","custom":"kept"}`,
	}}
	applyCodexFingerprintHeaders(headers, ids)
	require.True(t, applyCodexFingerprintClientMetadata(body, ids))
	require.Equal(t, root.ThreadID(), headers.Get("x-codex-parent-thread-id"))
	require.Equal(t, "collab_spawn", headers.Get("x-openai-subagent"))
	require.Equal(t, "kept", gjson.Get(headers.Get("x-codex-turn-metadata"), "custom").String())
	metadata, ok := body["client_metadata"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, root.ThreadID(), metadata["parent_thread_id"])
	require.Equal(t, child.parentTurnID, metadata["parent_turn_id"])
	require.Equal(t, child.rootTurnID, metadata["root_turn_id"])
	require.Equal(t, "collab_spawn", metadata["x-openai-subagent"])
	turnMetadata, ok := metadata["x-codex-turn-metadata"].(string)
	require.True(t, ok)
	require.Equal(t, "kept", gjson.Get(turnMetadata, "custom").String())
	require.Equal(t, "thread_spawn", gjson.Get(turnMetadata, "subagent_kind").String())
	require.Equal(t, "subagent", gjson.Get(turnMetadata, "thread_source").String())
}

func TestExtractCodexFingerprintOriginalIDsPrefersBodyTopology(t *testing.T) {
	headers := http.Header{
		"Thread-Id":                []string{"header-child"},
		"X-Codex-Parent-Thread-Id": []string{"header-parent"},
		"X-Openai-Subagent":        []string{"collab_spawn"},
		"X-Codex-Turn-Metadata":    []string{`{"thread_id":"header-metadata-child","parent_thread_id":"header-metadata-parent"}`},
	}
	body := []byte(`{"client_metadata":{"thread_id":"flat-child","parent_thread_id":"flat-parent","parent_turn_id":"flat-parent-turn","root_turn_id":"flat-root-turn","x-codex-turn-metadata":"{\"thread_id\":\"body-child\",\"parent_thread_id\":\"body-parent\",\"forked_from_thread_id\":\"body-fork\",\"parent_turn_id\":\"body-parent-turn\",\"root_turn_id\":\"body-root-turn\",\"subagent_kind\":\"thread_spawn\",\"thread_source\":\"subagent\"}"}}`)

	original := extractCodexFingerprintOriginalIDs(headers, body)
	require.Equal(t, "body-child", original.threadID)
	require.Equal(t, "body-parent", original.parentThreadID)
	require.Equal(t, "body-fork", original.forkedThreadID)
	require.Equal(t, "body-parent-turn", original.parentTurnID)
	require.Equal(t, "body-root-turn", original.rootTurnID)
	require.Equal(t, "collab_spawn", original.subagentHeader)
	require.Equal(t, "thread_spawn", original.subagentKind)
	require.Equal(t, "subagent", original.threadSource)
	require.True(t, original.isSubagent)
}

func TestExtractCodexFingerprintOriginalIDsDetectsMetadataOnlySubagent(t *testing.T) {
	body := []byte(`{"client_metadata":{"x-codex-turn-metadata":"{\"thread_id\":\"child\",\"request_kind\":\"subagent_task\"}"}}`)
	original := extractCodexFingerprintOriginalIDs(nil, body)
	require.True(t, original.isSubagent)
	require.Equal(t, "collab_spawn", original.subagentHeader)
	require.Equal(t, "thread_spawn", original.subagentKind)
}

func TestExtractCodexFingerprintOriginalIDsNormalizesFlatSubagentIdentity(t *testing.T) {
	body := []byte(`{"client_metadata":{"x-openai-subagent":"collab_spawn","subagent_kind":"thread_spawn","thread_source":"subagent"}}`)
	original := extractCodexFingerprintOriginalIDs(nil, body)
	require.True(t, original.isSubagent)
	require.Equal(t, "collab_spawn", original.subagentHeader)
	require.Equal(t, "thread_spawn", original.subagentKind)
	require.Equal(t, "subagent", original.threadSource)
}

func TestCodexWindowGenerationUsesOfficialSuffix(t *testing.T) {
	require.Equal(t, uint64(7), codexWindowGeneration("0198a001-0000-7000-8000-000000000002:7"))
	require.Zero(t, codexWindowGeneration("0198a001-0000-7000-8000-000000000002"))
	require.Zero(t, codexWindowGeneration("thread:not-a-number"))
}

func TestCodexFingerprintFullModeRejectsSubagent(t *testing.T) {
	startedAt := time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)
	account := &Account{
		ID: 27, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Extra:                          map[string]any{codexFingerprintModeExtraKey: string(codexFingerprintFull)},
		CodexFingerprintSeed:           testCodexFingerprintV3Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV3,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	svc := &OpenAIGatewayService{
		cfg: &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
		accountRepo: &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
			Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3,
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
			codexSessionSlotCountExtraKey:    2,
			codexSubagentMaxInflightExtraKey: 4,
		},
		CodexFingerprintSeed:           testCodexFingerprintV3Seed(),
		CodexFingerprintVersion:        codexFingerprintAlgorithmV3,
		CodexFingerprintEpoch:          3,
		CodexFingerprintEpochStartedAt: &startedAt,
	}
	repo := &codexFingerprintSessionRepoStub{state: CodexFingerprintState{
		Seed: testCodexFingerprintV3Seed(), Version: codexFingerprintAlgorithmV3,
		Epoch: 3, EpochStartedAt: startedAt,
	}}
	svc := &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
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

func TestAccountCodexSessionSlotCountRequiresSessionMode(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			codexFingerprintModeExtraKey:  string(codexFingerprintSession),
			codexSessionSlotCountExtraKey: json.Number("2"),
		},
	}
	require.Equal(t, 2, account.GetCodexSessionSlotCount())
	account.Extra[codexFingerprintModeExtraKey] = string(codexFingerprintDevice)
	require.Equal(t, 1, account.GetCodexSessionSlotCount())
	account.Extra[codexFingerprintModeExtraKey] = string(codexFingerprintFull)
	account.Extra[codexSessionSlotCountExtraKey] = 5
	require.Equal(t, 1, account.GetCodexSessionSlotCount())
}

func TestAccountCodexSessionSlotCountDefaultsToTwoForOAuthSession(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			codexFingerprintModeExtraKey: string(codexFingerprintSession),
		},
	}
	require.Equal(t, DefaultSessionPersonaSlotCount, account.GetCodexSessionSlotCount())
}
