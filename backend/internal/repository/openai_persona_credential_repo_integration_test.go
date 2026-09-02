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

func TestOpenAIPersonaCredentialRepository_AuthorizeRefreshCASAndRevoke(t *testing.T) {
	ctx := context.Background()
	var accountID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts
    (name, platform, type, status, credentials, extra)
VALUES ($1, 'openai', 'oauth', 'active', $2::jsonb, '{}'::jsonb)
RETURNING id`, fmt.Sprintf("persona-credential-%d", time.Now().UnixNano()),
		[]byte(`{"chatgpt_account_id":"acct-persona"}`)).Scan(&accountID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})

	repo := NewOpenAIPersonaCredentialRepository(integrationDB)
	write := service.OpenAIPersonaCredentialWrite{
		AccountID: accountID, PersonaID: service.SessionPersonaOpenCode, SlotID: 1,
		CredentialChainID: "opencode-chain-1",
		EncryptedPayload:  json.RawMessage(`{"format_version":1,"ciphertext":"cipher-v1"}`),
		ChatGPTAccountID:  "acct-persona", InstallationID: "opencode-installation-1",
		SlotGeneration: 1, SlotSetGeneration: 1,
	}
	require.NoError(t, repo.Authorize(ctx, write))

	record, err := repo.GetCredential(ctx, accountID, service.SessionPersonaOpenCode, 1, write.CredentialChainID)
	require.NoError(t, err)
	require.Equal(t, int64(1), record.TokenVersion)
	require.Equal(t, "ready", record.State)

	var extraRaw []byte
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra FROM accounts WHERE id = $1`, accountID).Scan(&extraRaw))
	var extra map[string]any
	require.NoError(t, json.Unmarshal(extraRaw, &extra))
	require.Equal(t, write.CredentialChainID, nestedJSONText(extra, service.OpenAIPersonaActiveChainsExtraKey, "1"))
	require.Equal(t, "opencode-installation-1", nestedJSONText(extra, service.OpenAIPersonaInstallationIDsExtraKey, "1"))
	require.Equal(t, true, nestedJSONValue(extra, service.OpenAIPersonaSlotAuthorizedExtraKey, "1"))

	claimed, err := repo.ClaimRefresh(ctx, accountID, service.SessionPersonaOpenCode, 1, write.CredentialChainID, 1)
	require.NoError(t, err)
	require.True(t, claimed)
	claimedAgain, err := repo.ClaimRefresh(ctx, accountID, service.SessionPersonaOpenCode, 1, write.CredentialChainID, 1)
	require.NoError(t, err)
	require.False(t, claimedAgain)

	write.EncryptedPayload = json.RawMessage(`{"format_version":1,"ciphertext":"cipher-v2"}`)
	swapped, err := repo.CompareAndSwapToken(ctx, write, 1)
	require.NoError(t, err)
	require.True(t, swapped)
	staleSwap, err := repo.CompareAndSwapToken(ctx, write, 1)
	require.NoError(t, err)
	require.False(t, staleSwap)

	record, err = repo.GetCredential(ctx, accountID, service.SessionPersonaOpenCode, 1, write.CredentialChainID)
	require.NoError(t, err)
	require.Equal(t, int64(2), record.TokenVersion)
	require.Equal(t, "ready", record.State)

	chains, err := repo.RevokeSlotAuthorization(ctx, accountID, service.SessionPersonaOpenCode, 1)
	require.NoError(t, err)
	require.Equal(t, []string{write.CredentialChainID}, chains)
	record, err = repo.GetCredential(ctx, accountID, service.SessionPersonaOpenCode, 1, write.CredentialChainID)
	require.NoError(t, err)
	require.Equal(t, "revoked", record.State)
	require.JSONEq(t, `{}`, string(record.EncryptedPayload))

	slots, err := repo.ListSlots(ctx, accountID)
	require.NoError(t, err)
	require.Len(t, slots, 1)
	require.False(t, slots[0].Authorized)
	require.Empty(t, slots[0].CredentialChainID)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT extra FROM accounts WHERE id = $1`, accountID).Scan(&extraRaw))
	require.NoError(t, json.Unmarshal(extraRaw, &extra))
	require.Empty(t, nestedJSONText(extra, service.OpenAIPersonaActiveChainsExtraKey, "1"))
	require.Equal(t, false, nestedJSONValue(extra, service.OpenAIPersonaSlotAuthorizedExtraKey, "1"))
}

func TestOpenAIPersonaCredentialRepository_RejectsPlaintextReadyPayload(t *testing.T) {
	ctx := context.Background()
	var accountID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO accounts (name, platform, type)
VALUES ($1, 'openai', 'oauth') RETURNING id`, fmt.Sprintf("persona-plaintext-%d", time.Now().UnixNano())).Scan(&accountID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, accountID)
	})
	_, err := integrationDB.ExecContext(ctx, `INSERT INTO openai_account_persona_slots
    (account_id, slot_id, persona, credential_chain_id, authorized)
VALUES ($1, 1, 'opencode', 'plaintext-chain', TRUE)`, accountID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO openai_account_persona_credentials
    (account_id, persona, credential_chain_id, slot_id, credentials, state)
VALUES ($1, 'opencode', 'plaintext-chain', 1, $2::jsonb, 'ready')`, accountID,
		[]byte(`{"access_token":"plaintext","refresh_token":"plaintext"}`))
	require.Error(t, err)
}

func nestedJSONValue(root map[string]any, key, nestedKey string) any {
	nested, _ := root[key].(map[string]any)
	return nested[nestedKey]
}

func nestedJSONText(root map[string]any, key, nestedKey string) string {
	value, _ := nestedJSONValue(root, key, nestedKey).(string)
	return value
}
