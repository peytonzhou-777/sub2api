//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type openAIPersonaOAuthClientStub struct {
	exchangeResponse *openai.TokenResponse
	refreshResponse  *openai.TokenResponse
	exchangeProfile  OpenAIOAuthClientProfile
	refreshProfile   OpenAIOAuthClientProfile
}

func (s *openAIPersonaOAuthClientStub) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	return s.exchangeResponse, nil
}

func (s *openAIPersonaOAuthClientStub) RefreshToken(context.Context, string, string) (*openai.TokenResponse, error) {
	return s.refreshResponse, nil
}

func (s *openAIPersonaOAuthClientStub) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	return s.refreshResponse, nil
}

func (s *openAIPersonaOAuthClientStub) ExchangeCodeWithProfile(_ context.Context, _, _, _, _, _ string, profile OpenAIOAuthClientProfile) (*openai.TokenResponse, error) {
	s.exchangeProfile = profile
	return s.exchangeResponse, nil
}

func (s *openAIPersonaOAuthClientStub) RefreshTokenWithProfile(_ context.Context, _, _, _ string, profile OpenAIOAuthClientProfile) (*openai.TokenResponse, error) {
	s.refreshProfile = profile
	return s.refreshResponse, nil
}

func TestOpenAIPersonaOAuth_OpenCodeAuthorizationBuildsIndependentChain(t *testing.T) {
	accountID := "acct-shared"
	client := &openAIPersonaOAuthClientStub{exchangeResponse: &openai.TokenResponse{
		AccessToken:  "opencode-access",
		RefreshToken: "opencode-refresh",
		IDToken:      openAIPersonaTestJWT(accountID),
		ExpiresIn:    3600,
	}}
	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()
	account := &Account{
		ID:       42,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "codex-access",
			"refresh_token":      "codex-refresh",
			"chatgpt_account_id": accountID,
		},
		Extra: map[string]any{
			openAIPersonaSlotGenerationsKey:   map[string]any{"1": int64(3)},
			openAIPersonaSlotSetGenerationKey: int64(7),
		},
	}

	auth, err := svc.GeneratePersonaAuthURL(context.Background(), account, 1)
	require.NoError(t, err)
	parsed, err := url.Parse(auth.AuthURL)
	require.NoError(t, err)
	require.Equal(t, "opencode", parsed.Query().Get("originator"))
	require.Equal(t, openai.ClientID, parsed.Query().Get("client_id"))

	result, err := svc.ExchangePersonaCode(context.Background(), account.ID, 1, &OpenAIExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "code",
		State:     parsed.Query().Get("state"),
	})
	require.NoError(t, err)
	require.Equal(t, SessionPersonaOpenCode, result.PersonaID)
	require.Equal(t, int64(3), result.SlotGeneration)
	require.Equal(t, int64(7), result.SlotSetGeneration)
	require.Equal(t, "opencode/1.18.23", client.exchangeProfile.UserAgent)
	require.Equal(t, "opencode", client.exchangeProfile.Originator)
	require.False(t, client.exchangeProfile.IncludeRefreshScope)

	credentials, err := svc.BuildPersonaOAuthCredentials(account, result)
	require.NoError(t, err)
	require.Equal(t, "codex-access", credentials["access_token"])
	active := credentials[openAIPersonaActiveChainsKey].(map[string]any)
	require.Equal(t, result.CredentialChainID, active["1"])
	chains := credentials[openAIOAuthCredentialChainsKey].(map[string]any)
	chain := chains[result.CredentialChainID].(map[string]any)
	require.Equal(t, "opencode-access", chain["access_token"])
	require.Equal(t, "opencode-refresh", chain["refresh_token"])
	require.Equal(t, accountID, chain["chatgpt_account_id"])
}

func TestOpenAIPersonaOAuth_OpenCodeRejectsDifferentAccount(t *testing.T) {
	client := &openAIPersonaOAuthClientStub{exchangeResponse: &openai.TokenResponse{
		AccessToken:  "access",
		RefreshToken: "refresh",
		IDToken:      openAIPersonaTestJWT("acct-other"),
		ExpiresIn:    3600,
	}}
	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()
	account := &Account{
		ID:       43,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "codex-access",
			"refresh_token":      "codex-refresh",
			"chatgpt_account_id": "acct-shared",
		},
	}
	auth, err := svc.GeneratePersonaAuthURL(context.Background(), account, 1)
	require.NoError(t, err)
	parsed, err := url.Parse(auth.AuthURL)
	require.NoError(t, err)

	_, err = svc.ExchangePersonaCode(context.Background(), account.ID, 1, &OpenAIExchangeCodeInput{
		SessionID: auth.SessionID,
		Code:      "code",
		State:     parsed.Query().Get("state"),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "different ChatGPT account")
}

func TestOpenAIPersonaOAuth_ActivePointerSelectsLatestChain(t *testing.T) {
	account := &Account{Credentials: map[string]any{
		openAIOAuthCredentialChainsKey: map[string]any{
			"old": map[string]any{"persona": "opencode", "slot_id": 1, "credential_chain_id": "old"},
			"new": map[string]any{"persona": "opencode", "slot_id": 1, "credential_chain_id": "new"},
		},
		openAIPersonaActiveChainsKey: map[string]any{"1": "new"},
	}}
	require.Equal(t, "new", openAIMapString(account.findPersonaCredential(SessionPersonaOpenCode, 1), "credential_chain_id"))
}

func TestOpenAITokenProvider_RefreshesOnlyBoundOpenCodeChain(t *testing.T) {
	chainID := "opencode-refresh-chain"
	account := &Account{
		ID:       44,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":               "codex-access",
			"refresh_token":              "codex-refresh",
			"chatgpt_account_id":         "acct-shared",
			openAIPersonaActiveChainsKey: map[string]any{"1": chainID},
			openAIOAuthCredentialChainsKey: map[string]any{
				chainID: map[string]any{
					"persona":             "opencode",
					"slot_id":             1,
					"credential_chain_id": chainID,
					"chatgpt_account_id":  "acct-shared",
					"access_token":        "expired-opencode-access",
					"refresh_token":       "opencode-refresh",
					"expires_at":          "2020-01-01T00:00:00Z",
					"ready":               true,
					"state":               "ready",
					"oauth_client_id":     openai.ClientID,
				},
			},
		},
	}
	client := &openAIPersonaOAuthClientStub{refreshResponse: &openai.TokenResponse{
		AccessToken:  "fresh-opencode-access",
		RefreshToken: "fresh-opencode-refresh",
		ExpiresIn:    3600,
	}}
	oauthService := NewOpenAIOAuthService(nil, client)
	defer oauthService.Stop()
	repo := &openAIAccountRepoStub{account: account}
	cache := newOpenAITokenCacheStub()
	provider := NewOpenAITokenProvider(repo, cache, oauthService)
	binding := SessionPersonaSlotBinding{
		AccountID:         account.ID,
		PersonaID:         SessionPersonaOpenCode,
		SlotID:            1,
		SlotCount:         DefaultSessionPersonaSlotCount,
		ScopeVersion:      SessionPersonaScopeVersionV3,
		MappingVersion:    SessionPersonaScopeVersionV3,
		CredentialChainID: chainID,
		State:             SessionPersonaSlotStateActive,
		Enabled:           true,
		Authorized:        true,
	}

	token, err := provider.GetAccessTokenForBinding(context.Background(), account, binding)
	require.NoError(t, err)
	require.Equal(t, "fresh-opencode-access", token)
	require.Equal(t, "codex-access", repo.account.Credentials["access_token"])
	updated := repo.account.findPersonaCredentialByChainID(SessionPersonaOpenCode, 1, chainID)
	require.Equal(t, "fresh-opencode-access", updated["access_token"])
	require.Equal(t, "fresh-opencode-refresh", updated["refresh_token"])
	require.Equal(t, "opencode", client.refreshProfile.Originator)
	require.False(t, client.refreshProfile.IncludeRefreshScope)
}

func openAIPersonaTestJWT(accountID string) string {
	payload, _ := json.Marshal(map[string]any{
		"email": "operator@example.com",
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": accountID,
			"chatgpt_user_id":    "user-1",
			"chatgpt_plan_type":  "pro",
		},
	})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return "e30." + encoded + ".signature"
}
