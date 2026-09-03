//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountPersonaRepository_PrimaryCreateAndDynamicLifecycle(t *testing.T) {
	ctx := context.Background()
	accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	account := &service.Account{
		Name: fmt.Sprintf("dynamic-persona-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "acct-dynamic"},
		Extra: map[string]any{}, Concurrency: 1, Priority: 50, Status: service.StatusActive,
		Schedulable: true, AutoPauseOnExpired: true,
	}
	primary := service.OpenAIPrimaryPersonaCreate{
		ProfileVersion: "0.149.0", CredentialChainID: "primary-chain",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"primary-cipher"}`),
		ChatGPTAccountID: "acct-dynamic", DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		InstallationID: "primary-installation", UpstreamSessionID: "primary-session",
	}
	require.NoError(t, accountRepo.CreateWithPrimaryOpenAIPersona(ctx, account, nil, primary))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})

	personaRepo := NewOpenAIAccountPersonaRepository(integrationDB)
	personas, err := personaRepo.ListAccountPersonas(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, personas, 1)
	require.True(t, personas[0].IsDefaultProtected())
	require.Equal(t, int64(1), personas[0].CurrentSessionEpoch)
	_, err = personaRepo.RevokeAccountPersonaAuthorization(ctx, account.ID, personas[0].ID, personas[0].RowVersion)
	require.ErrorIs(t, err, service.ErrOpenAIDefaultPersonaProtected)
	require.ErrorIs(t, personaRepo.RetireAccountPersona(ctx, account.ID, personas[0].ID, personas[0].RowVersion), service.ErrOpenAIDefaultPersonaProtected)

	maxActive := 1
	dynamic, err := personaRepo.CreateAccountPersona(ctx, service.OpenAIAccountPersonaCreate{
		AccountID: account.ID, ProfileID: service.SessionPersonaOpenCode, ProfileVersion: "1.18.23",
		MaxActiveClientSessionsOverride: &maxActive,
		DeviceSeed:                      []byte("abcdef0123456789abcdef0123456789"), InstallationID: "opencode-installation",
	})
	require.NoError(t, err)
	require.Equal(t, 1, dynamic.Position)
	require.Equal(t, service.OpenAIAccountPersonaStateDraft, dynamic.State)

	authorized, err := personaRepo.AuthorizeAccountPersona(ctx, service.OpenAIAccountPersonaAuthorization{
		AccountID: account.ID, AccountPersonaID: dynamic.ID, ExpectedRowVersion: dynamic.RowVersion,
		PersonaGeneration: dynamic.PersonaGeneration, CredentialChainID: "opencode-chain",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"opencode-cipher"}`),
		ChatGPTAccountID: "acct-dynamic", InstallationID: dynamic.InstallationID,
		UpstreamSessionID: "opencode-session",
	})
	require.NoError(t, err)
	require.True(t, authorized.AcceptsNewRoot())
	require.Equal(t, int64(1), authorized.CurrentSessionEpoch)
	require.Equal(t, int64(2), authorized.PersonaGeneration)

	credential, err := personaRepo.GetAccountPersonaCredential(ctx, dynamic.ID, "opencode-chain")
	require.NoError(t, err)
	require.Equal(t, dynamic.ID, credential.AccountPersonaID)
	require.Equal(t, service.SessionPersonaOpenCode, credential.PersonaID)
	require.Equal(t, "ready", credential.State)
	claimed, err := personaRepo.ClaimAccountPersonaCredentialRefresh(ctx, dynamic.ID, "opencode-chain", credential.TokenVersion)
	require.NoError(t, err)
	require.True(t, claimed)
	swapped, err := personaRepo.CompareAndSwapAccountPersonaToken(ctx, service.OpenAIAccountPersonaCredentialUpdate{
		AccountPersonaID: dynamic.ID, CredentialChainID: "opencode-chain",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"opencode-cipher-v2"}`),
		ChatGPTAccountID: "acct-dynamic", InstallationID: dynamic.InstallationID,
	}, credential.TokenVersion)
	require.NoError(t, err)
	require.True(t, swapped)

	disabled := false
	draining, err := personaRepo.UpdateAccountPersona(ctx, service.OpenAIAccountPersonaUpdate{
		AccountID: account.ID, AccountPersonaID: dynamic.ID, ExpectedRowVersion: authorized.RowVersion,
		Enabled: &disabled,
	})
	require.NoError(t, err)
	require.Equal(t, service.OpenAIAccountPersonaStateDraining, draining.State)

	chains, err := personaRepo.RevokeAccountPersonaAuthorization(ctx, account.ID, dynamic.ID, draining.RowVersion)
	require.NoError(t, err)
	require.Equal(t, []string{"opencode-chain"}, chains)

	revoked, err := personaRepo.GetAccountPersona(ctx, account.ID, dynamic.ID)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIAccountPersonaStateDisabled, revoked.State)
	require.Empty(t, revoked.CurrentCredentialChainID)
	require.NoError(t, personaRepo.RetireAccountPersona(ctx, account.ID, dynamic.ID, revoked.RowVersion))
}

func TestCreateWithPrimaryOpenAIPersonaRollsBackWholeAccount(t *testing.T) {
	ctx := context.Background()
	accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	name := fmt.Sprintf("dynamic-persona-rollback-%d", time.Now().UnixNano())
	account := &service.Account{
		Name: name, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "acct-rollback"}, Extra: map[string]any{},
		Concurrency: 1, Priority: 50, Status: service.StatusActive, Schedulable: true,
	}
	primary := service.OpenAIPrimaryPersonaCreate{
		ProfileVersion: "0.149.0", CredentialChainID: "rollback-chain",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"rollback-cipher"}`),
		ChatGPTAccountID: "acct-rollback", DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		InstallationID: "rollback-installation", UpstreamSessionID: "rollback-session",
	}
	err := accountRepo.CreateWithPrimaryOpenAIPersona(ctx, account, []int64{int64(^uint64(0) >> 1)}, primary)
	require.Error(t, err)
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts WHERE name = $1`, name).Scan(&count))
	require.Zero(t, count)
}
