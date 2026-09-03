//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type openAIAccountPersonaRepoStub struct {
	persona       OpenAIAccountPersona
	authorizeCall int
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
