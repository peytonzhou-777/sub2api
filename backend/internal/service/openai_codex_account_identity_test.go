package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type codexAccountIdentityRepoStub struct {
	AccountRepository
	account  *Account
	accounts map[int64]*Account
	states   map[int64]CodexFingerprintState
}

func (s *codexAccountIdentityRepoStub) GetByID(_ context.Context, accountID int64) (*Account, error) {
	if account := s.accounts[accountID]; account != nil {
		return account, nil
	}
	return s.account, nil
}

func (s *codexAccountIdentityRepoStub) ValidateCodexFingerprintSecret(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (s *codexAccountIdentityRepoStub) ResolveCodexFingerprintSessionState(
	_ context.Context,
	request CodexFingerprintSessionRequest,
) (*CodexFingerprintSessionResolution, error) {
	state := s.states[request.AccountID]
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
		Created:                 true,
	}, nil
}

// newCodexAccountIdentityFingerprintFixture 构造两套独立账号指纹，验证故障转移会切换上游身份。
func newCodexAccountIdentityFingerprintFixture() (*OpenAIGatewayService, *Account, *Account) {
	startedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newState := func(seed string) CodexFingerprintState {
		return CodexFingerprintState{
			Seed: seed, Version: codexFingerprintAlgorithmV3, Epoch: 3, EpochStartedAt: startedAt,
		}
	}
	newAccount := func(id int64, chatgptAccountID, seed string) *Account {
		return &Account{
			ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
			Credentials: map[string]any{"chatgpt_account_id": chatgptAccountID},
			Extra: map[string]any{
				codexFingerprintModeExtraKey: string(codexFingerprintSession),
				codexOutboundProfileExtraKey: CodexOutboundProfileCLI0149,
			},
			CodexFingerprintSeed: seed, CodexFingerprintVersion: codexFingerprintAlgorithmV3,
			CodexFingerprintEpoch: 3, CodexFingerprintEpochStartedAt: &startedAt,
		}
	}
	account11 := newAccount(11, "chatgpt-account-11", strings.Repeat("ab", codexFingerprintSeedBytes))
	account19 := newAccount(19, "chatgpt-account-19", strings.Repeat("cd", codexFingerprintSeedBytes))
	repo := &codexAccountIdentityRepoStub{
		accounts: map[int64]*Account{account11.ID: account11, account19.ID: account19},
		states: map[int64]CodexFingerprintState{
			account11.ID: newState(account11.CodexFingerprintSeed),
			account19.ID: newState(account19.CodexFingerprintSeed),
		},
	}
	return &OpenAIGatewayService{
		cfg:         &config.Config{Gateway: config.GatewayConfig{CodexFingerprintSecret: string(testCodexFingerprintV3Secret())}},
		accountRepo: repo,
	}, account11, account19
}

func TestCodexRequestBodyIdentityNamespaceIsStablePerOAuthAccount(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-codex","prompt_cache_key":"client-session","client_metadata":{"x-codex-installation-id":"client-installation","session_id":"client-session","thread_id":"client-thread","x-codex-window-id":"client-window","x-codex-turn-metadata":"{\"installation_id\":\"client-installation\",\"session_id\":\"client-session\",\"thread_id\":\"client-thread\",\"turn_id\":\"client-turn\",\"window_id\":\"client-window\"}"}}`)
	account11 := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-11"}}
	account19 := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "chatgpt-account-19"}}

	first, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account11, 77)
	require.NoError(t, err)
	require.True(t, changed)
	firstAgain, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account11, 77)
	require.NoError(t, err)
	require.True(t, changed)
	second, changed, err := applyCodexAccountIdentityClientMetadataRaw(body, account19, 77)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, string(first), string(firstAgain))

	paths := []string{
		"prompt_cache_key",
		"client_metadata.x-codex-installation-id",
		"client_metadata.session_id",
		"client_metadata.thread_id",
		"client_metadata.x-codex-window-id",
	}
	for _, path := range paths {
		require.NotEqual(t, gjson.GetBytes(body, path).String(), gjson.GetBytes(first, path).String(), path)
		require.NotEqual(t, gjson.GetBytes(first, path).String(), gjson.GetBytes(second, path).String(), path)
	}
	require.Equal(t, gjson.GetBytes(first, "prompt_cache_key").String(), gjson.GetBytes(first, "client_metadata.session_id").String())

	var embeddedFirst map[string]any
	var embeddedSecond map[string]any
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(first, "client_metadata.x-codex-turn-metadata").String()), &embeddedFirst))
	require.NoError(t, json.Unmarshal([]byte(gjson.GetBytes(second, "client_metadata.x-codex-turn-metadata").String()), &embeddedSecond))
	for _, field := range []string{"installation_id", "session_id", "thread_id", "turn_id", "window_id"} {
		require.NotEqual(t, embeddedFirst[field], embeddedSecond[field], field)
	}
}

func TestCodexAccountIdentityNamespaceUsesStableCredentialSource(t *testing.T) {
	firstRow := &Account{ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "shared-upstream-account"}}
	secondRow := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "shared-upstream-account"}}
	require.Equal(t, codexAccountIdentityNamespace(firstRow), codexAccountIdentityNamespace(secondRow))

	firstUser := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-1"}}
	sameUser := &Account{ID: 21, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-1"}}
	secondUser := &Account{ID: 22, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "team-account", "chatgpt_user_id": "user-2"}}
	require.Equal(t, codexAccountIdentityNamespace(firstUser), codexAccountIdentityNamespace(sameUser))
	require.NotEqual(t, codexAccountIdentityNamespace(firstUser), codexAccountIdentityNamespace(secondUser))

	seed := "11111111-1111-4111-8111-111111111111"
	seeded := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, CodexFingerprintSeed: seed}
	require.Equal(t, "seed:"+seed, codexAccountIdentityNamespace(seeded))

	// Local row IDs repeat across independent deployments, so they are not a
	// safe fallback for upstream identity.
	require.Empty(t, codexAccountIdentityNamespace(&Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth}))

	setupTokenA := &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-a"}}
	setupTokenADuplicate := &Account{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-a"}}
	setupTokenB := &Account{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeSetupToken, Credentials: map[string]any{"access_token": "setup-token-b"}}
	setupNamespace := codexAccountIdentityNamespace(setupTokenA)
	require.NotEmpty(t, setupNamespace)
	require.NotContains(t, setupNamespace, "setup-token-a")
	require.Equal(t, setupNamespace, codexAccountIdentityNamespace(setupTokenADuplicate))
	require.NotEqual(t, setupNamespace, codexAccountIdentityNamespace(setupTokenB))
}

func TestCodexAccountIdentitySourceResolvesShadowAndOverwritesFailoverContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	parentID := int64(11)
	parent := &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "team-account",
		"chatgpt_user_id":    "user-1",
	}}
	shadow := &Account{ID: 111, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	service := &OpenAIGatewayService{accountRepo: &codexAccountIdentityRepoStub{account: parent}}

	resolved, err := service.prepareCodexAccountIdentitySource(context.Background(), c, shadow)
	require.NoError(t, err)
	require.Same(t, parent, resolved)
	require.Same(t, parent, codexAccountIdentitySource(c, shadow))

	req, err := service.buildUpstreamRequest(
		context.Background(), c, shadow,
		[]byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`),
		"token", true, "client-session", true,
	)
	require.NoError(t, err)
	require.Equal(t, isolateOpenAISessionID(0, "client-session"), req.Header.Get("session_id"))

	next := &Account{ID: 19, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
		"chatgpt_account_id": "other-account",
		"chatgpt_user_id":    "user-2",
	}}
	resolved, err = service.prepareCodexAccountIdentitySource(context.Background(), c, next)
	require.NoError(t, err)
	require.Same(t, next, resolved)
	require.Same(t, next, codexAccountIdentitySource(c, shadow))
}

func TestBuildOpenAIWSHeadersNamespacesCodexIdentityByOAuthAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Set("api_key_id", int64(77))
	c.Request.Header.Set("x-codex-installation-id", "client-installation")
	c.Request.Header.Set("thread-id", "client-thread")
	c.Request.Header.Set("x-codex-window-id", "client-window")
	c.Request.Header.Set("x-client-request-id", "client-request")

	service, account11, account19 := newCodexAccountIdentityFingerprintFixture()
	body := []byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session","client_metadata":{"session_id":"client-session","thread_id":"client-thread"}}`)
	build := func(account *Account) http.Header {
		_, err := service.applyCodexFingerprintRawForAttempt(context.Background(), c, account, body, true)
		require.NoError(t, err)
		headers, _, err := service.buildOpenAIWSHeaders(
			context.Background(), c, account, "token",
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
			true, "", "", "client-session", "", "",
		)
		require.NoError(t, err)
		return headers
	}

	first := build(account11)
	firstAgain := build(account11)
	second := build(account19)
	for _, header := range []string{"session-id", "thread-id", "x-codex-window-id", "x-client-request-id"} {
		require.NotEmpty(t, first.Get(header), header)
		require.Equal(t, first.Get(header), firstAgain.Get(header), header)
		require.NotEqual(t, first.Get(header), second.Get(header), header)
	}
	require.Empty(t, first.Get("x-codex-installation-id"), "strict non-compact profile does not expose installation ID as a header")

	_, err := service.applyCodexFingerprintRawForAttempt(context.Background(), c, account11, body, true)
	require.NoError(t, err)
	httpRequest, err := service.buildUpstreamRequest(
		context.Background(), c, account11,
		body,
		"token", true, "client-session", true,
	)
	require.NoError(t, err)
	require.Equal(t, httpRequest.Header.Get("session-id"), first.Get("session-id"), "HTTP and WS must derive the same identity from the raw client key")
}

func TestBuildUpstreamRequestNamespacesCodexIdentityByOAuthAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc, account11, account19 := newCodexAccountIdentityFingerprintFixture()
	body := []byte(`{"model":"gpt-5.6-codex","stream":true,"prompt_cache_key":"client-session"}`)

	build := func(account *Account) http.Header {
		t.Helper()
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
		c.Set("api_key", &APIKey{ID: 77})
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.144.0")
		c.Request.Header.Set("x-codex-installation-id", "client-installation")
		c.Request.Header.Set("x-codex-window-id", "client-window")
		c.Request.Header.Set("session-id", "client-session")
		c.Request.Header.Set("thread-id", "client-thread")
		c.Request.Header.Set("x-client-request-id", "client-request")
		c.Request.Header.Set("x-codex-turn-metadata", `{"installation_id":"client-installation","session_id":"client-session","thread_id":"client-thread","turn_id":"client-turn","window_id":"client-window"}`)

		_, err := svc.applyCodexFingerprintRawForAttempt(context.Background(), c, account, body, true)
		require.NoError(t, err)
		req, err := svc.buildUpstreamRequest(
			context.Background(), c, account, body, "oauth-token", true, "client-session", true,
		)
		require.NoError(t, err)
		return req.Header
	}

	first := build(account11)
	firstAgain := build(account11)
	second := build(account19)

	identityHeaders := []string{
		"x-codex-installation-id",
		"x-codex-window-id",
		"session-id",
		"session_id",
		"conversation_id",
		"thread-id",
		"x-client-request-id",
	}
	checked := 0
	for _, header := range identityHeaders {
		if first.Get(header) == "" && second.Get(header) == "" {
			continue
		}
		checked++
		require.NotEmpty(t, first.Get(header), header)
		require.Equal(t, first.Get(header), firstAgain.Get(header), "same account must retain stable identity: %s", header)
		require.NotEqual(t, first.Get(header), second.Get(header), "account failover must rotate upstream identity: %s", header)
	}
	require.GreaterOrEqual(t, checked, 4, "test must exercise the real strict-profile header identity surface")

	decodeTurnMetadata := func(headers http.Header) map[string]any {
		t.Helper()
		raw := headers.Get("x-codex-turn-metadata")
		require.NotEmpty(t, raw)
		metadata := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(raw), &metadata))
		require.Contains(t, metadata, "turn_started_at_unix_ms")
		return metadata
	}
	firstMetadata := decodeTurnMetadata(first)
	firstAgainMetadata := decodeTurnMetadata(firstAgain)
	secondMetadata := decodeTurnMetadata(second)
	for _, field := range []string{"installation_id", "session_id", "thread_id", "turn_id", "window_id"} {
		require.NotEmpty(t, firstMetadata[field], field)
		require.Equal(t, firstMetadata[field], firstAgainMetadata[field], "same account must retain stable metadata identity: %s", field)
		require.NotEqual(t, firstMetadata[field], secondMetadata[field], "account failover must rotate metadata identity: %s", field)
	}
}
