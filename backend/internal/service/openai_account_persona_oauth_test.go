//go:build unit

package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
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

type openAIAccountPersonaRepoStub struct {
	persona          OpenAIAccountPersona
	authorizeCall    int
	primaryAuthCall  int
	lastPrimaryInput OpenAIAccountPersonaAuthorization
}

func (s *openAIAccountPersonaRepoStub) ListAccountPersonas(context.Context, int64) ([]OpenAIAccountPersona, error) {
	return []OpenAIAccountPersona{s.persona}, nil
}

func (s *openAIAccountPersonaRepoStub) GetAccountPersona(_ context.Context, accountID, personaID int64) (*OpenAIAccountPersona, error) {
	if s.persona.AccountID != accountID || s.persona.ID != personaID {
		return nil, ErrOpenAIAccountPersonaNotFound
	}
	persona := s.persona
	return &persona, nil
}

func (*openAIAccountPersonaRepoStub) CreateAccountPersona(context.Context, OpenAIAccountPersonaCreate) (*OpenAIAccountPersona, error) {
	return nil, ErrOpenAIAccountPersonaNotFound
}

func (*openAIAccountPersonaRepoStub) UpdateAccountPersona(context.Context, OpenAIAccountPersonaUpdate) (*OpenAIAccountPersona, error) {
	return nil, ErrOpenAIAccountPersonaNotFound
}

func (*openAIAccountPersonaRepoStub) RetireAccountPersona(context.Context, int64, int64, int64) error {
	return ErrOpenAIAccountPersonaNotFound
}

func (s *openAIAccountPersonaRepoStub) AuthorizeAccountPersona(context.Context, OpenAIAccountPersonaAuthorization) (*OpenAIAccountPersona, error) {
	s.authorizeCall++
	persona := s.persona
	return &persona, nil
}

func (s *openAIAccountPersonaRepoStub) ReauthorizePrimaryAccountPersona(_ context.Context, input OpenAIAccountPersonaAuthorization) (*OpenAIAccountPersona, error) {
	s.primaryAuthCall++
	s.lastPrimaryInput = input
	persona := s.persona
	return &persona, nil
}

func (*openAIAccountPersonaRepoStub) RevokeAccountPersonaAuthorization(context.Context, int64, int64, int64) ([]string, error) {
	return nil, ErrOpenAIAccountPersonaNotFound
}

func (*openAIAccountPersonaRepoStub) GetAccountPersonaCredential(context.Context, int64, string) (*OpenAIPersonaCredentialRecord, error) {
	return nil, ErrOpenAIPersonaCredentialChainNotReady
}

func (*openAIAccountPersonaRepoStub) ClaimAccountPersonaCredentialRefresh(context.Context, int64, string, int64) (bool, error) {
	return false, nil
}

func (*openAIAccountPersonaRepoStub) CompareAndSwapAccountPersonaToken(context.Context, OpenAIAccountPersonaCredentialUpdate, int64) (bool, error) {
	return false, nil
}

func (*openAIAccountPersonaRepoStub) MarkAccountPersonaCredentialInvalid(context.Context, int64, string, int64, string) error {
	return nil
}

func (*openAIAccountPersonaRepoStub) PrepareAccountPersonaSession(context.Context, OpenAIAccountPersonaSessionPrepareInput) (*OpenAIAccountPersonaSessionPrepareResult, error) {
	return nil, ErrOpenAIAccountPersonaSessionNotFound
}

func (*openAIAccountPersonaRepoStub) GetAccountPersonaSession(context.Context, int64, int64, int64, time.Time) (*OpenAIAccountPersonaSession, error) {
	return nil, ErrOpenAIAccountPersonaSessionNotFound
}

func (*openAIAccountPersonaRepoStub) TouchAccountPersonaSession(context.Context, int64, int64, time.Time) error {
	return ErrOpenAIAccountPersonaSessionNotFound
}

func TestAccountPersonaOAuthRejectsDefaultPersonaAuthorization(t *testing.T) {
	repo := &openAIAccountPersonaRepoStub{persona: OpenAIAccountPersona{
		ID: 11, AccountID: 7, Position: 0, ProfileID: SessionPersonaCodexCLIStrict,
		CredentialOwner: OpenAICredentialOwnerAccountPrimary,
	}}
	svc := NewOpenAIOAuthService(nil, nil)
	defer svc.Stop()
	svc.configureAccountPersonaStore(repo)

	_, err := svc.GenerateAccountPersonaAuthURL(context.Background(), &Account{
		ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
	}, 11)
	require.Error(t, err)
	require.Equal(t, 409, infraerrors.Code(err))
	require.Equal(t, "DEFAULT_PERSONA_PROTECTED", infraerrors.Reason(err))
}

func TestPrimaryAccountPersonaReauthorizationUsesProtectedServerBoundPath(t *testing.T) {
	profile, ok := NewDefaultSessionPersonaRegistry().Get(string(SessionPersonaCodexCLIStrict))
	require.True(t, ok)
	repo := &openAIAccountPersonaRepoStub{persona: OpenAIAccountPersona{
		ID: 21, AccountID: 9, Position: 0, ProfileID: SessionPersonaCodexCLIStrict,
		ProfileVersion: profile.EffectiveVersion(), CredentialOwner: OpenAICredentialOwnerAccountPrimary,
		PersonaGeneration: 3, RowVersion: 5, InstallationID: "primary-installation",
	}}
	client := &openAIPersonaOAuthClientStub{exchangeResponse: &openai.TokenResponse{
		AccessToken: "new-access", RefreshToken: "new-refresh",
		IDToken: openAIPersonaTestJWT("account-9"), ExpiresIn: 3600,
	}}
	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()
	svc.configureAccountPersonaStore(repo)
	svc.configurePersonaCredentialStore(openAIPersonaTestEncryptor{}, nil)
	account := &Account{
		ID: 9, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "account-9"},
	}

	auth, err := svc.GeneratePrimaryAccountPersonaAuthURL(context.Background(), account)
	require.NoError(t, err)
	session, ok := svc.sessionStore.Get(auth.SessionID)
	require.True(t, ok)
	require.Equal(t, int64(21), session.AccountPersonaID)
	require.NotEmpty(t, session.CredentialChainID)

	result, err := svc.ExchangePrimaryAccountPersonaCode(context.Background(), account.ID, &OpenAIExchangeCodeInput{
		SessionID: auth.SessionID, Code: "code", State: session.State,
	})
	require.NoError(t, err)
	_, err = svc.PersistPrimaryAccountPersonaAuthorization(context.Background(), account, result)
	require.NoError(t, err)
	require.Equal(t, 1, repo.primaryAuthCall)
	require.Zero(t, repo.authorizeCall)
	require.Equal(t, account.ID, repo.lastPrimaryInput.AccountID)
	require.Equal(t, repo.persona.ID, repo.lastPrimaryInput.AccountPersonaID)
	require.Equal(t, repo.persona.PersonaGeneration, repo.lastPrimaryInput.PersonaGeneration)
	require.Equal(t, "account-9", repo.lastPrimaryInput.ChatGPTAccountID)
	require.NotEmpty(t, repo.lastPrimaryInput.EncryptedPayload)
	require.NotEmpty(t, repo.lastPrimaryInput.UpstreamSessionID)
}

func TestDecryptPersonaCredentialAllowsDrainingThreadChain(t *testing.T) {
	svc := NewOpenAIOAuthService(nil, nil)
	defer svc.Stop()
	svc.configurePersonaCredentialStore(openAIPersonaTestEncryptor{}, nil)
	payload, err := svc.encryptPersonaCredential(&OpenAITokenInfo{
		AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	require.NoError(t, err)

	info, err := svc.decryptPersonaCredential(&OpenAIPersonaCredentialRecord{
		State: "draining", EncryptedPayload: payload,
	})
	require.NoError(t, err)
	require.Equal(t, "old-access", info.AccessToken)
}

func TestAccountPersonaOAuthRejectsAccountMismatchBeforePersistence(t *testing.T) {
	repo := &openAIAccountPersonaRepoStub{persona: OpenAIAccountPersona{
		ID: 12, AccountID: 8, Position: 1, ProfileID: SessionPersonaOpenCode,
		ProfileVersion: "1.18.23", CredentialOwner: OpenAICredentialOwnerPersonaIndependent,
		PersonaGeneration: 1, RowVersion: 1, InstallationID: "installation-12",
	}}
	client := &openAIPersonaOAuthClientStub{exchangeResponse: &openai.TokenResponse{
		AccessToken: "access", RefreshToken: "refresh",
		IDToken: openAIPersonaTestJWT("different-account"), ExpiresIn: 3600,
	}}
	svc := NewOpenAIOAuthService(nil, client)
	defer svc.Stop()
	svc.configureAccountPersonaStore(repo)
	account := &Account{
		ID: 8, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "expected-account"},
	}

	auth, err := svc.GenerateAccountPersonaAuthURL(context.Background(), account, 12)
	require.NoError(t, err)
	session, ok := svc.sessionStore.Get(auth.SessionID)
	require.True(t, ok)

	_, err = svc.ExchangeAccountPersonaCode(context.Background(), account.ID, 12, &OpenAIExchangeCodeInput{
		SessionID: auth.SessionID, Code: "code", State: session.State,
	})
	require.Error(t, err)
	require.Equal(t, "OPENAI_PERSONA_ACCOUNT_MISMATCH", infraerrors.Reason(err))
	require.Zero(t, repo.authorizeCall)
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
