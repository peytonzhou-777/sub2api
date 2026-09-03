//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIPersonaMigrationTestEncryptor struct{}

func (openAIPersonaMigrationTestEncryptor) Encrypt(plaintext string) (string, error) {
	return "encrypted:" + plaintext, nil
}

func (openAIPersonaMigrationTestEncryptor) Decrypt(ciphertext string) (string, error) {
	return ciphertext, nil
}

func TestOpenAIAccountPersonaMigrationConvertsLegacyPairAndActivatesGuards(t *testing.T) {
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `UPDATE openai_persona_architecture_state
SET architecture_version='account_persona_v1', state='pending', migration_report='{}'::jsonb, updated_at=NOW()
WHERE singleton=TRUE`)
	require.NoError(t, err)

	var accountID int64
	accountName := fmt.Sprintf("persona-migration-%d", time.Now().UnixNano())
	err = integrationDB.QueryRowContext(ctx, `INSERT INTO accounts
    (name, platform, type, credentials, extra, concurrency, priority, status)
VALUES ($1, 'openai', 'oauth', $2::jsonb, '{}'::jsonb, 2, 50, 'active') RETURNING id`,
		accountName, `{"access_token":"legacy-access","refresh_token":"legacy-refresh","id_token":"legacy-id","expires_at":"2099-01-01T00:00:00Z","client_id":"legacy-client","chatgpt_account_id":"acct-migration"}`).Scan(&accountID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `UPDATE openai_persona_architecture_state
SET architecture_version='account_persona_v1', state='pending', migration_report='{}'::jsonb, updated_at=NOW()
WHERE singleton=TRUE`)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id=$1`, accountID)
	})

	_, err = integrationDB.ExecContext(ctx, `INSERT INTO openai_account_persona_slots
    (account_id, slot_id, persona, credential_chain_id, enabled, state, authorized,
     session_epoch, slot_generation, slot_set_generation, upstream_session_id)
VALUES
    ($1,0,'codex_cli_strict','legacy-strict',TRUE,'active',TRUE,3,2,4,'legacy-strict-session'),
    ($1,1,'opencode','legacy-opencode',TRUE,'active',TRUE,5,3,4,'legacy-opencode-session')`, accountID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `INSERT INTO openai_account_persona_credentials
    (account_id, persona, credential_chain_id, slot_id, credentials, chatgpt_account_id,
     installation_id, token_version, state)
VALUES
    ($1,'codex_cli_strict','legacy-strict',0,$2::jsonb,'acct-migration','strict-install',2,'ready'),
    ($1,'opencode','legacy-opencode',1,$3::jsonb,'acct-migration','opencode-install',3,'ready')`,
		accountID,
		`{"format_version":1,"ciphertext":"strict-cipher"}`,
		`{"format_version":1,"ciphertext":"opencode-cipher"}`)
	require.NoError(t, err)

	dryRun, err := RunOpenAIAccountPersonaMigration(ctx, integrationDB, openAIPersonaMigrationTestEncryptor{}, false, "")
	require.NoError(t, err)
	require.True(t, dryRun.Ready)
	require.GreaterOrEqual(t, dryRun.PlannedAccountCount, 1)

	report, err := RunOpenAIAccountPersonaMigration(ctx, integrationDB, openAIPersonaMigrationTestEncryptor{}, true, openAIAccountPersonaMigrationConfirmation)
	require.NoError(t, err)
	require.True(t, report.Ready)
	require.GreaterOrEqual(t, report.MigratedAccounts, 1)

	var personaCount, sessionCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_account_personas WHERE account_id=$1`, accountID).Scan(&personaCount))
	require.Equal(t, 2, personaCount)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*)
FROM openai_account_persona_sessions s JOIN openai_account_personas p ON p.id=s.account_persona_id
WHERE p.account_id=$1 AND s.state='current'`, accountID).Scan(&sessionCount))
	require.Equal(t, 2, sessionCount)
	var unsnapshottedSessions int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*)
FROM openai_account_persona_sessions s JOIN openai_account_personas p ON p.id=s.account_persona_id
WHERE p.account_id=$1 AND s.state='current' AND s.proxy_snapshot_set=FALSE`, accountID).Scan(&unsnapshottedSessions))
	require.Zero(t, unsnapshottedSessions)

	var hasLegacyRuntimeToken bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT credentials ?| ARRAY['access_token','refresh_token','id_token','expires_at','client_id']
FROM accounts WHERE id=$1`, accountID).Scan(&hasLegacyRuntimeToken))
	require.False(t, hasLegacyRuntimeToken)

	_, err = integrationDB.ExecContext(ctx, `INSERT INTO accounts
    (name,platform,type,credentials,extra,concurrency,priority,status)
VALUES ('forbidden-legacy-openai','openai','oauth','{"access_token":"forbidden"}'::jsonb,'{}'::jsonb,1,50,'active')`)
	require.Error(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_account_persona_slots SET updated_at=NOW() WHERE account_id=$1`, accountID)
	require.Error(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE accounts SET credentials=jsonb_set(credentials,'{access_token}','"forbidden"'::jsonb) WHERE id=$1`, accountID)
	require.Error(t, err)
}

func TestOpenAIAccountPersonaArchitectureGuardStatementTriggerReturnsNull(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	_, err := tx.ExecContext(ctx, `UPDATE openai_persona_architecture_state
SET architecture_version='account_persona_v1', state='ready' WHERE singleton=TRUE`)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_account_persona_slots
    (account_id,slot_id,persona) SELECT id,0,'codex_cli_strict' FROM accounts WHERE FALSE`)
	require.Error(t, err)
}
