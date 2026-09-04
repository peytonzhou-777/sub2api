//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	_, err = personaRepo.AuthorizeAccountPersona(ctx, service.OpenAIAccountPersonaAuthorization{
		AccountID: account.ID, AccountPersonaID: personas[0].ID, ExpectedRowVersion: personas[0].RowVersion,
		PersonaGeneration: personas[0].PersonaGeneration, CredentialChainID: "forbidden-primary-chain",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"forbidden"}`),
		ChatGPTAccountID: "acct-dynamic", InstallationID: personas[0].InstallationID,
		UpstreamSessionID: "forbidden-primary-session", OldSessionExpiresAt: time.Now().Add(time.Hour),
	})
	require.ErrorIs(t, err, service.ErrOpenAIDefaultPersonaProtected)

	primaryReauthorized, err := personaRepo.ReauthorizePrimaryAccountPersona(ctx, service.OpenAIAccountPersonaAuthorization{
		AccountID: account.ID, AccountPersonaID: personas[0].ID, ExpectedRowVersion: personas[0].RowVersion,
		PersonaGeneration: personas[0].PersonaGeneration, CredentialChainID: "primary-chain-reauthorized",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"primary-cipher-reauthorized"}`),
		ChatGPTAccountID: "acct-dynamic", InstallationID: personas[0].InstallationID,
		UpstreamSessionID: "primary-session-reauthorized", OldSessionExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	require.True(t, primaryReauthorized.IsDefaultProtected())
	require.Equal(t, int64(2), primaryReauthorized.CurrentSessionEpoch)
	require.Equal(t, int64(2), primaryReauthorized.PersonaGeneration)
	var oldPrimaryState, newPrimaryState, oldPrimarySessionState, newPrimarySessionState string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM openai_account_persona_credentials
WHERE account_persona_id = $1 AND credential_chain_id = 'primary-chain'`, personas[0].ID).Scan(&oldPrimaryState))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM openai_account_persona_credentials
WHERE account_persona_id = $1 AND credential_chain_id = 'primary-chain-reauthorized'`, personas[0].ID).Scan(&newPrimaryState))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM openai_account_persona_sessions
WHERE account_persona_id = $1 AND session_epoch = 1`, personas[0].ID).Scan(&oldPrimarySessionState))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM openai_account_persona_sessions
WHERE account_persona_id = $1 AND session_epoch = 2`, personas[0].ID).Scan(&newPrimarySessionState))
	require.Equal(t, "draining", oldPrimaryState)
	require.Equal(t, "ready", newPrimaryState)
	require.Equal(t, "draining", oldPrimarySessionState)
	require.Equal(t, "current", newPrimarySessionState)

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
		UpstreamSessionID:   "opencode-session",
		OldSessionExpiresAt: time.Now().Add(48 * time.Hour),
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

	updatedMaxActive := 2
	capacityUpdated, err := personaRepo.UpdateAccountPersona(ctx, service.OpenAIAccountPersonaUpdate{
		AccountID: account.ID, AccountPersonaID: dynamic.ID, ExpectedRowVersion: authorized.RowVersion,
		MaxActiveSessionsConfigured: true, MaxActiveClientSessionsOverride: &updatedMaxActive,
	})
	require.NoError(t, err)
	require.NotNil(t, capacityUpdated.MaxActiveClientSessionsOverride)
	require.Equal(t, updatedMaxActive, *capacityUpdated.MaxActiveClientSessionsOverride)
	require.Equal(t, service.OpenAIAccountPersonaStateActive, capacityUpdated.State)

	disabled := false
	draining, err := personaRepo.UpdateAccountPersona(ctx, service.OpenAIAccountPersonaUpdate{
		AccountID: account.ID, AccountPersonaID: dynamic.ID, ExpectedRowVersion: capacityUpdated.RowVersion,
		Enabled: &disabled, OldSessionExpiresAt: time.Now().Add(48 * time.Hour),
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

func TestOpenAIAccountPersonaSessionRotationAndHistoricalGrace(t *testing.T) {
	ctx := context.Background()
	accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	account := &service.Account{
		Name: fmt.Sprintf("persona-session-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "acct-session"},
		Extra: map[string]any{}, Concurrency: 1, Priority: 50, Status: service.StatusActive, Schedulable: true,
	}
	primary := service.OpenAIPrimaryPersonaCreate{
		ProfileVersion: "0.149.0", CredentialChainID: "session-chain",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"session-cipher"}`),
		ChatGPTAccountID: "acct-session", DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		InstallationID: "session-installation", UpstreamSessionID: "session-epoch-1",
	}
	require.NoError(t, accountRepo.CreateWithPrimaryOpenAIPersona(ctx, account, nil, primary))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})

	personaRepo := NewOpenAIAccountPersonaRepository(integrationDB)
	personas, err := personaRepo.ListAccountPersonas(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, personas, 1)
	persona := personas[0]
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_account_persona_sessions
SET started_at = $1::timestamptz, last_active_at = $1::timestamptz
WHERE account_persona_id = $2 AND session_epoch = 1`, now.Add(-3*time.Hour), persona.ID)
	require.NoError(t, err)
	policy := service.CodexFingerprintEpochPolicy{
		MinSessionAgeHours: 1, MaxSessionAgeHours: 2, RotationJitterHours: 0,
		IdleGateMinutes: 1, OldEpochGraceHours: 1,
	}
	prepared, err := personaRepo.PrepareAccountPersonaSession(ctx, service.OpenAIAccountPersonaSessionPrepareInput{
		AccountID: account.ID, AccountPersonaID: persona.ID, Now: now, Policy: policy,
		NewUpstreamSession: "session-epoch-2",
	})
	require.NoError(t, err)
	require.True(t, prepared.Rotated)
	require.Equal(t, int64(2), prepared.Session.SessionEpoch)
	require.Equal(t, "session-installation", prepared.Session.InstallationID)

	historical, err := personaRepo.GetAccountPersonaSession(ctx, account.ID, persona.ID, 1, now)
	require.NoError(t, err)
	require.Equal(t, service.OpenAIPersonaSessionDraining, historical.State)
	_, err = personaRepo.GetAccountPersonaSession(ctx, account.ID, persona.ID, 1, now.Add(2*time.Hour))
	require.ErrorIs(t, err, service.ErrOpenAIAccountPersonaSessionExpired)

	var proxyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO proxies
    (name, protocol, host, port, status) VALUES ($1, 'http', '127.0.0.1', 18080, 'active') RETURNING id`,
		fmt.Sprintf("persona-session-proxy-%d", time.Now().UnixNano())).Scan(&proxyID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `UPDATE accounts SET proxy_id = NULL WHERE id = $1`, account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM proxies WHERE id = $1`, proxyID)
	})
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET proxy_id = $1 WHERE id = $2`, proxyID, account.ID)
	require.NoError(t, err)
	proxyRotated, err := personaRepo.PrepareAccountPersonaSession(ctx, service.OpenAIAccountPersonaSessionPrepareInput{
		AccountID: account.ID, AccountPersonaID: persona.ID, Now: now.Add(time.Minute), Policy: policy,
		NewUpstreamSession: "session-epoch-3",
	})
	require.NoError(t, err)
	require.True(t, proxyRotated.Rotated)
	require.Equal(t, int64(3), proxyRotated.Session.SessionEpoch)
	require.Equal(t, int64(2), proxyRotated.Persona.PersonaGeneration)
	require.NotNil(t, proxyRotated.Session.EffectiveProxyID)
	require.Equal(t, proxyID, *proxyRotated.Session.EffectiveProxyID)
	require.NotEmpty(t, proxyRotated.Session.EffectiveProxyURL)
}

func TestOpenAIAccountPersonaSessionManualRotationHonorsLeaseAndForceRevokes(t *testing.T) {
	ctx := context.Background()
	accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	account := &service.Account{
		Name: fmt.Sprintf("persona-session-occupied-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "acct-session-occupied"},
		Extra: map[string]any{}, Concurrency: 1, Priority: 50, Status: service.StatusActive, Schedulable: true,
	}
	primary := service.OpenAIPrimaryPersonaCreate{
		ProfileVersion: "0.149.0", CredentialChainID: "occupied-chain",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"occupied-cipher"}`),
		ChatGPTAccountID: "acct-session-occupied", DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		InstallationID: "occupied-installation", UpstreamSessionID: "occupied-session-1",
	}
	require.NoError(t, accountRepo.CreateWithPrimaryOpenAIPersona(ctx, account, nil, primary))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})
	personas, err := NewOpenAIAccountPersonaRepository(integrationDB).ListAccountPersonas(ctx, account.ID)
	require.NoError(t, err)
	persona := personas[0]

	var userID, groupID, apiKeyID int64
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users (email, password_hash)
VALUES ($1, 'test') RETURNING id`, "persona-session-"+suffix+"@example.com").Scan(&userID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups (name) VALUES ($1) RETURNING id`, "persona-session-"+suffix).Scan(&groupID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM groups WHERE id = $1`, groupID)
	})
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys (user_id, key, name, group_id)
VALUES ($1, $2, 'test', $3) RETURNING id`, userID, "sk-session-"+suffix, groupID).Scan(&apiKeyID))
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO openai_persona_client_session_leases
    (account_persona_id, account_id, user_id, api_key_id, client_session_hash, state, last_active_at, active_until)
VALUES ($1, $2, $3, $4, $5, 'active', NOW(), NOW() + INTERVAL '1 hour')`,
		persona.ID, account.ID, userID, apiKeyID, strings.Repeat("a", 64))
	require.NoError(t, err)

	policy := service.CodexFingerprintEpochPolicy{MinSessionAgeHours: 1, MaxSessionAgeHours: 2, IdleGateMinutes: 1, OldEpochGraceHours: 1}
	repo := NewOpenAIAccountPersonaRepository(integrationDB)
	_, err = repo.PrepareAccountPersonaSession(ctx, service.OpenAIAccountPersonaSessionPrepareInput{
		AccountID: account.ID, AccountPersonaID: persona.ID, ExpectedRowVersion: persona.RowVersion,
		Now: time.Now(), Policy: policy, NewUpstreamSession: "occupied-session-2", Manual: true,
	})
	require.ErrorIs(t, err, service.ErrOpenAIAccountPersonaSessionOccupied)
	forced, err := repo.PrepareAccountPersonaSession(ctx, service.OpenAIAccountPersonaSessionPrepareInput{
		AccountID: account.ID, AccountPersonaID: persona.ID, ExpectedRowVersion: persona.RowVersion,
		Now: time.Now(), Policy: policy, NewUpstreamSession: "occupied-session-2", Manual: true, Force: true,
	})
	require.NoError(t, err)
	require.True(t, forced.Rotated)
	var oldState string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM openai_account_persona_sessions
WHERE account_persona_id = $1 AND session_epoch = 1`, persona.ID).Scan(&oldState))
	require.Equal(t, "revoked", oldState)
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
