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

func TestOpenAIPersonaIDMappingRepository_DynamicScopeAndLegacyNulls(t *testing.T) {
	ctx := context.Background()
	accountRepo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	account := &service.Account{
		Name: fmt.Sprintf("persona-mapping-%d", time.Now().UnixNano()), Platform: service.PlatformOpenAI,
		Type: service.AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "acct-mapping"},
		Extra: map[string]any{}, Concurrency: 1, Priority: 50, Status: service.StatusActive, Schedulable: true,
	}
	primary := service.OpenAIPrimaryPersonaCreate{
		ProfileVersion: "0.149.0", CredentialChainID: "mapping-chain",
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"mapping-cipher"}`),
		ChatGPTAccountID: "acct-mapping", DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		InstallationID: "mapping-installation", UpstreamSessionID: "mapping-session",
	}
	require.NoError(t, accountRepo.CreateWithPrimaryOpenAIPersona(ctx, account, nil, primary))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})

	personas, err := NewOpenAIAccountPersonaRepository(integrationDB).ListAccountPersonas(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, personas, 1)
	persona := personas[0]

	dynamicScope := service.OpenAIPersonaIDMappingScope{
		UserID: 101, APIKeyID: 202, AccountID: account.ID, AccountPersonaID: persona.ID,
		ScopeKey: "pm_dynamic_scope", Persona: service.SessionPersonaCodexCLIStrict,
		ProfileVersion: persona.ProfileVersion, SessionEpoch: persona.CurrentSessionEpoch,
		PersonaGeneration: persona.PersonaGeneration, SlotGeneration: persona.PersonaGeneration,
		SlotSetGeneration: persona.PersonaGeneration, CredentialChainID: persona.CurrentCredentialChainID,
		ThreadID: "client-thread-dynamic",
	}
	written, err := accountRepo.UpsertOpenAIPersonaIDMapping(ctx, &service.OpenAIPersonaIDMapping{
		Scope: dynamicScope, MappingType: service.OpenAIPersonaMappingResponse,
		ClientID: "client-response-dynamic", OpenCodeID: "upstream-response-dynamic",
	})
	require.NoError(t, err)
	require.Equal(t, dynamicScope.AccountPersonaID, written.Scope.AccountPersonaID)
	require.Equal(t, dynamicScope.SessionEpoch, written.Scope.SessionEpoch)
	require.Equal(t, dynamicScope.PersonaGeneration, written.Scope.PersonaGeneration)
	require.Equal(t, dynamicScope.Persona, written.Scope.Persona)
	require.Equal(t, dynamicScope.ProfileVersion, written.Scope.ProfileVersion)

	readBack, err := accountRepo.GetOpenAIPersonaIDMappingByClient(ctx, dynamicScope, service.OpenAIPersonaMappingResponse, "client-response-dynamic")
	require.NoError(t, err)
	require.Equal(t, written.Scope, readBack.Scope)

	_, err = integrationDB.ExecContext(ctx, `INSERT INTO openai_persona_id_mappings
    (user_id, api_key_id, account_id, scope_key, persona, slot_id, session_epoch,
     slot_generation, slot_set_generation, credential_chain_id, thread_id,
     mapping_type, client_id, opencode_id, account_persona_id, persona_generation,
     persona_session_epoch, profile_id, profile_version)
VALUES ($1::bigint, $2::bigint, $3::bigint, $4, 'opencode', 1, 7, 2, 3, 'legacy-chain',
        'legacy-thread', 'response', 'client-response-legacy', 'upstream-response-legacy',
        NULL::bigint, NULL::bigint, NULL::bigint, NULL::varchar, NULL::varchar)`,
		int64(303), int64(404), account.ID, "pm_legacy_scope")
	require.NoError(t, err)
	legacyScope := service.OpenAIPersonaIDMappingScope{
		UserID: 303, APIKeyID: 404, AccountID: account.ID, ScopeKey: "pm_legacy_scope",
		Persona: service.SessionPersonaOpenCode, SlotID: 1, SessionEpoch: 7,
		SlotGeneration: 2, SlotSetGeneration: 3, CredentialChainID: "legacy-chain", ThreadID: "legacy-thread",
	}
	legacy, err := accountRepo.GetOpenAIPersonaIDMappingByClient(ctx, legacyScope, service.OpenAIPersonaMappingResponse, "client-response-legacy")
	require.NoError(t, err)
	require.Zero(t, legacy.Scope.AccountPersonaID)
	require.Zero(t, legacy.Scope.PersonaGeneration)
	require.Equal(t, int64(7), legacy.Scope.SessionEpoch)
	require.Equal(t, service.SessionPersonaOpenCode, legacy.Scope.Persona)
	require.Empty(t, legacy.Scope.ProfileVersion)
}
