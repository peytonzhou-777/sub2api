//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type openAIPersonaOAuthClientStub struct {
	exchangeResponse *openai.TokenResponse
	refreshResponse  *openai.TokenResponse
	refreshErr       error
	exchangeProfile  OpenAIOAuthClientProfile
	refreshProfile   OpenAIOAuthClientProfile
}

func (s *openAIPersonaOAuthClientStub) ExchangeCode(context.Context, string, string, string, string, string) (*openai.TokenResponse, error) {
	return s.exchangeResponse, nil
}

func (s *openAIPersonaOAuthClientStub) RefreshToken(context.Context, string, string) (*openai.TokenResponse, error) {
	return s.refreshResponse, s.refreshErr
}

func (s *openAIPersonaOAuthClientStub) RefreshTokenWithClientID(context.Context, string, string, string) (*openai.TokenResponse, error) {
	return s.refreshResponse, s.refreshErr
}

func (s *openAIPersonaOAuthClientStub) ExchangeCodeWithProfile(_ context.Context, _, _, _, _, _ string, profile OpenAIOAuthClientProfile) (*openai.TokenResponse, error) {
	s.exchangeProfile = profile
	return s.exchangeResponse, nil
}

func (s *openAIPersonaOAuthClientStub) RefreshTokenWithProfile(_ context.Context, _, _, _ string, profile OpenAIOAuthClientProfile) (*openai.TokenResponse, error) {
	s.refreshProfile = profile
	return s.refreshResponse, s.refreshErr
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
	personaRepo := newOpenAIPersonaCredentialRepoStub()
	cache := newOpenAITokenCacheStub()
	svc.configurePersonaCredentialStore(personaRepo, openAIPersonaTestEncryptor{}, cache)
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

	require.NoError(t, svc.PersistPersonaAuthorization(context.Background(), account, result))
	record, err := personaRepo.GetCredential(context.Background(), account.ID, SessionPersonaOpenCode, 1, result.CredentialChainID)
	require.NoError(t, err)
	require.Equal(t, int64(1), record.TokenVersion)
	require.Equal(t, accountID, record.ChatGPTAccountID)
	stored, err := svc.decryptPersonaCredential(record)
	require.NoError(t, err)
	require.Equal(t, "opencode-access", stored.AccessToken)
	require.Equal(t, "opencode-refresh", stored.RefreshToken)
	require.Equal(t, "codex-access", account.Credentials["access_token"], "Persona authorization must not rewrite account credentials")
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

func TestOpenAITokenProvider_RefreshesOnlyBoundOpenCodeChain(t *testing.T) {
	chainID := "opencode-refresh-chain"
	account := &Account{
		ID:       44,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":       "codex-access",
			"refresh_token":      "codex-refresh",
			"chatgpt_account_id": "acct-shared",
		},
	}
	client := &openAIPersonaOAuthClientStub{refreshResponse: &openai.TokenResponse{
		AccessToken:  "fresh-opencode-access",
		RefreshToken: "fresh-opencode-refresh",
		ExpiresIn:    3600,
	}}
	oauthService := NewOpenAIOAuthService(nil, client)
	defer oauthService.Stop()
	personaRepo := newOpenAIPersonaCredentialRepoStub()
	cache := newOpenAITokenCacheStub()
	oauthService.configurePersonaCredentialStore(personaRepo, openAIPersonaTestEncryptor{}, cache)
	repo := &openAIAccountRepoStub{account: account}
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
		SlotGeneration:    1,
		SlotSetGeneration: 1,
	}
	require.NoError(t, seedOpenAIPersonaCredential(personaRepo, oauthService, account, binding, &OpenAITokenInfo{
		AccessToken: "expired-opencode-access", RefreshToken: "opencode-refresh", ExpiresAt: time.Now().Add(-time.Hour).Unix(),
		ClientID: openai.ClientID, ChatGPTAccountID: "acct-shared",
	}))
	cache.tokens[OpenAITokenCacheKeyForBinding(account, binding)] = "stale-opencode-access"

	token, err := provider.GetAccessTokenForBinding(context.Background(), account, binding)
	require.NoError(t, err)
	require.Equal(t, "fresh-opencode-access", token)
	require.Equal(t, "codex-access", repo.account.Credentials["access_token"])
	updated, err := personaRepo.GetCredential(context.Background(), account.ID, SessionPersonaOpenCode, 1, chainID)
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.TokenVersion)
	updatedInfo, err := oauthService.decryptPersonaCredential(updated)
	require.NoError(t, err)
	require.Equal(t, "fresh-opencode-access", updatedInfo.AccessToken)
	require.Equal(t, "fresh-opencode-refresh", updatedInfo.RefreshToken)
	require.Equal(t, 1, personaRepo.claimCalls)
	require.Equal(t, 1, personaRepo.casCalls)
	require.Equal(t, "opencode", client.refreshProfile.Originator)
	require.False(t, client.refreshProfile.IncludeRefreshScope)
}

func TestOpenAITokenProvider_RefreshFailureInvalidatesBoundPersonaChain(t *testing.T) {
	chainID := "opencode-failed-chain"
	account := &Account{
		ID: 49, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "acct-shared"},
	}
	binding := SessionPersonaSlotBinding{
		AccountID: account.ID, PersonaID: SessionPersonaOpenCode, SlotID: 1,
		SlotCount: DefaultSessionPersonaSlotCount, ScopeVersion: SessionPersonaScopeVersionV3,
		MappingVersion: SessionPersonaScopeVersionV3, CredentialChainID: chainID,
		State: SessionPersonaSlotStateActive, Enabled: true, Authorized: true,
		SlotGeneration: 1, SlotSetGeneration: 1,
	}
	client := &openAIPersonaOAuthClientStub{refreshErr: errors.New("upstream rejected refresh token")}
	personaRepo := newOpenAIPersonaCredentialRepoStub()
	cache := newOpenAITokenCacheStub()
	oauthService := NewOpenAIOAuthService(nil, client)
	defer oauthService.Stop()
	oauthService.configurePersonaCredentialStore(personaRepo, openAIPersonaTestEncryptor{}, cache)
	invalidator := &openAIPersonaTransportInvalidatorStub{}
	oauthService.SetPersonaTransportInvalidator(invalidator)
	require.NoError(t, seedOpenAIPersonaCredential(personaRepo, oauthService, account, binding, &OpenAITokenInfo{
		AccessToken: "expired-access", RefreshToken: "rotating-refresh",
		ExpiresAt: time.Now().Add(-time.Minute).Unix(), ClientID: openai.ClientID,
		ChatGPTAccountID: "acct-shared",
	}))
	cache.tokens[OpenAITokenCacheKeyForBinding(account, binding)] = "stale-cache"
	provider := NewOpenAITokenProvider(nil, cache, oauthService)

	_, err := provider.GetAccessTokenForBinding(context.Background(), account, binding)
	require.ErrorContains(t, err, "upstream rejected refresh token")
	record, loadErr := personaRepo.GetCredential(context.Background(), account.ID, binding.PersonaID, binding.SlotID, chainID)
	require.NoError(t, loadErr)
	require.Equal(t, "invalid", record.State)
	require.False(t, personaRepo.slots[1].Authorized)
	require.Empty(t, personaRepo.slots[1].CredentialChainID)
	require.Empty(t, cache.tokens[OpenAITokenCacheKeyForBinding(account, binding)])
	require.Equal(t, []string{chainID}, invalidator.chains)
}

type openAIPersonaTransportInvalidatorStub struct {
	chains []string
}

func (s *openAIPersonaTransportInvalidatorStub) InvalidateOpenAIPersonaTransport(_ int64, _ SessionPersonaID, _ int, credentialChainID string) {
	s.chains = append(s.chains, credentialChainID)
}

func (s *openAIPersonaTransportInvalidatorStub) InvalidateOpenAIAccountPersonaCredentialTransport(_, _ int64, credentialChainID string) {
	s.chains = append(s.chains, credentialChainID)
}

func (*openAIPersonaTransportInvalidatorStub) InvalidateOpenAIAccountPersonaSessionTransport(_, _, _ int64) {
}

func TestOpenAIPersonaOAuth_RevokeDestroysAllLocalSlotChains(t *testing.T) {
	account := &Account{
		ID: 45, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "acct-shared"},
	}
	personaRepo := newOpenAIPersonaCredentialRepoStub()
	cache := newOpenAITokenCacheStub()
	oauthService := NewOpenAIOAuthService(nil, nil)
	defer oauthService.Stop()
	oauthService.configurePersonaCredentialStore(personaRepo, openAIPersonaTestEncryptor{}, cache)
	invalidator := &openAIPersonaTransportInvalidatorStub{}
	oauthService.SetPersonaTransportInvalidator(invalidator)

	for _, chainID := range []string{"opencode-old", "opencode-current"} {
		binding := SessionPersonaSlotBinding{
			AccountID: account.ID, PersonaID: SessionPersonaOpenCode, SlotID: 1,
			CredentialChainID: chainID, SlotGeneration: 1, SlotSetGeneration: 1,
		}
		require.NoError(t, seedOpenAIPersonaCredential(personaRepo, oauthService, account, binding, &OpenAITokenInfo{
			AccessToken: chainID + "-access", RefreshToken: chainID + "-refresh",
			ExpiresAt: time.Now().Add(time.Hour).Unix(), ChatGPTAccountID: "acct-shared",
		}))
		cache.tokens[OpenAITokenCacheKeyForBinding(account, binding)] = chainID + "-cached"
	}

	require.NoError(t, oauthService.RevokePersonaAuthorization(context.Background(), account, 1))
	require.Equal(t, 1, personaRepo.revokeCalls)
	require.ElementsMatch(t, []string{"opencode-old", "opencode-current"}, invalidator.chains)
	for _, chainID := range invalidator.chains {
		record, err := personaRepo.GetCredential(context.Background(), account.ID, SessionPersonaOpenCode, 1, chainID)
		require.NoError(t, err)
		require.Equal(t, "revoked", record.State)
		require.JSONEq(t, `{}`, string(record.EncryptedPayload))
		binding := SessionPersonaSlotBinding{AccountID: account.ID, PersonaID: SessionPersonaOpenCode, SlotID: 1, CredentialChainID: chainID}
		require.Empty(t, cache.tokens[OpenAITokenCacheKeyForBinding(account, binding)])
	}
	require.False(t, personaRepo.slots[1].Authorized)
	require.Empty(t, personaRepo.slots[1].CredentialChainID)
}

func TestOpenAIPersonaOAuth_WaitAcceptsAlreadyRefreshedCredential(t *testing.T) {
	account := &Account{
		ID: 46, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "acct-shared"},
	}
	binding := SessionPersonaSlotBinding{
		AccountID: account.ID, PersonaID: SessionPersonaOpenCode, SlotID: 1,
		CredentialChainID: "opencode-refreshed", SlotGeneration: 1, SlotSetGeneration: 1,
	}
	personaRepo := newOpenAIPersonaCredentialRepoStub()
	oauthService := NewOpenAIOAuthService(nil, nil)
	defer oauthService.Stop()
	oauthService.configurePersonaCredentialStore(personaRepo, openAIPersonaTestEncryptor{}, newOpenAITokenCacheStub())
	require.NoError(t, seedOpenAIPersonaCredential(personaRepo, oauthService, account, binding, &OpenAITokenInfo{
		AccessToken: "refreshed-access", RefreshToken: "refreshed-refresh",
		ExpiresAt: time.Now().Add(time.Hour).Unix(), ChatGPTAccountID: "acct-shared",
	}))

	info, err := oauthService.waitForPersonaCredentialRefresh(context.Background(), account, binding)
	require.NoError(t, err)
	require.Equal(t, "refreshed-access", info.AccessToken)
}

func TestOpenAIPersonaOAuth_WaitAllowsNormalOAuthRefreshLatency(t *testing.T) {
	account := &Account{
		ID: 48, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "acct-shared"},
	}
	binding := SessionPersonaSlotBinding{
		AccountID: account.ID, PersonaID: SessionPersonaOpenCode, SlotID: 1,
		CredentialChainID: "opencode-refreshing", SlotGeneration: 1, SlotSetGeneration: 1,
	}
	personaRepo := newOpenAIPersonaCredentialRepoStub()
	oauthService := NewOpenAIOAuthService(nil, nil)
	defer oauthService.Stop()
	oauthService.configurePersonaCredentialStore(personaRepo, openAIPersonaTestEncryptor{}, newOpenAITokenCacheStub())
	require.NoError(t, seedOpenAIPersonaCredential(personaRepo, oauthService, account, binding, &OpenAITokenInfo{
		AccessToken: "stale-access", RefreshToken: "refresh-token",
		ExpiresAt: time.Now().Add(-time.Minute).Unix(), ChatGPTAccountID: "acct-shared",
	}))
	key := openAIPersonaCredentialTestKey(account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID)
	personaRepo.mu.Lock()
	personaRepo.credentials[key].State = "refreshing"
	personaRepo.mu.Unlock()
	payload, err := oauthService.encryptPersonaCredential(&OpenAITokenInfo{
		AccessToken: "fresh-access", RefreshToken: "fresh-refresh",
		ExpiresAt: time.Now().Add(time.Hour).Unix(), ChatGPTAccountID: "acct-shared",
	})
	require.NoError(t, err)

	go func() {
		time.Sleep(400 * time.Millisecond)
		personaRepo.mu.Lock()
		record := personaRepo.credentials[key]
		record.State = "ready"
		record.TokenVersion++
		record.EncryptedPayload = payload
		personaRepo.mu.Unlock()
	}()

	info, err := oauthService.waitForPersonaCredentialRefresh(context.Background(), account, binding)
	require.NoError(t, err)
	require.Equal(t, "fresh-access", info.AccessToken)
}

func TestOpenAIPersonaOAuth_StatusKeepsRefreshingChainAuthorized(t *testing.T) {
	account := &Account{
		ID: 47, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "acct-shared"},
		Extra: map[string]any{
			openAIPersonaMappingEnabledExtraKey: true,
			openAIPersonaMappingVersionExtraKey: SessionPersonaScopeVersionV3,
			openAIPersonaSlotGenerationsKey:     map[string]any{"1": int64(1)},
			openAIPersonaSlotSetGenerationKey:   int64(1),
		},
	}
	binding := SessionPersonaSlotBinding{
		AccountID: account.ID, PersonaID: SessionPersonaOpenCode, SlotID: 1,
		CredentialChainID: "opencode-refreshing", SlotGeneration: 1, SlotSetGeneration: 1,
	}
	personaRepo := newOpenAIPersonaCredentialRepoStub()
	oauthService := NewOpenAIOAuthService(nil, nil)
	defer oauthService.Stop()
	oauthService.configurePersonaCredentialStore(personaRepo, openAIPersonaTestEncryptor{}, newOpenAITokenCacheStub())
	require.NoError(t, seedOpenAIPersonaCredential(personaRepo, oauthService, account, binding, &OpenAITokenInfo{
		AccessToken: "refreshing-access", RefreshToken: "refreshing-refresh",
		ExpiresAt: time.Now().Add(time.Hour).Unix(), ChatGPTAccountID: "acct-shared",
	}))
	key := openAIPersonaCredentialTestKey(account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID)
	personaRepo.credentials[key].State = "refreshing"

	status, err := oauthService.GetPersonaOAuthStatus(context.Background(), account)
	require.NoError(t, err)
	require.Len(t, status.Slots, DefaultSessionPersonaSlotCount)
	require.True(t, status.Slots[1].Authorized)
	require.NotEmpty(t, status.Slots[1].AccessTokenExpires)
	encoded, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "refreshing-access")
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
