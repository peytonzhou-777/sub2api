package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type openAIPersonaCredentialRepository struct {
	db *sql.DB
}

func NewOpenAIPersonaCredentialRepository(db *sql.DB) service.OpenAIPersonaCredentialRepository {
	return &openAIPersonaCredentialRepository{db: db}
}

const openAIPersonaSlotSelect = `SELECT account_id, slot_id, persona, COALESCE(credential_chain_id, ''),
       enabled, state, authorized, session_epoch, slot_generation, slot_set_generation,
       COALESCE(upstream_session_id, ''), created_at, updated_at,
       draining_started_at, disabled_at
FROM openai_account_persona_slots`

const openAIPersonaCredentialSelect = `SELECT account_id, persona, credential_chain_id, slot_id,
       credentials, chatgpt_account_id, installation_id, token_version, state,
       last_error, created_at, updated_at, last_refreshed_at
FROM openai_account_persona_credentials`

func (r *openAIPersonaCredentialRepository) ListSlots(ctx context.Context, accountID int64) ([]service.OpenAIPersonaSlotRecord, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrOpenAIPersonaCredentialStoreUnavailable
	}
	rows, err := r.db.QueryContext(ctx, openAIPersonaSlotSelect+` WHERE account_id = $1 ORDER BY slot_id`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []service.OpenAIPersonaSlotRecord
	for rows.Next() {
		record, scanErr := scanOpenAIPersonaSlot(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func scanOpenAIPersonaSlot(scanner interface{ Scan(...any) error }) (service.OpenAIPersonaSlotRecord, error) {
	var record service.OpenAIPersonaSlotRecord
	var draining, disabled sql.NullTime
	if err := scanner.Scan(
		&record.AccountID, &record.SlotID, &record.PersonaID, &record.CredentialChainID,
		&record.Enabled, &record.State, &record.Authorized, &record.SessionEpoch,
		&record.SlotGeneration, &record.SlotSetGeneration, &record.UpstreamSessionID,
		&record.CreatedAt, &record.UpdatedAt, &draining, &disabled,
	); err != nil {
		return record, err
	}
	if draining.Valid {
		record.DrainingStartedAt = &draining.Time
	}
	if disabled.Valid {
		record.DisabledAt = &disabled.Time
	}
	return record, nil
}

func (r *openAIPersonaCredentialRepository) GetCredential(ctx context.Context, accountID int64, persona service.SessionPersonaID, slotID int, credentialChainID string) (*service.OpenAIPersonaCredentialRecord, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrOpenAIPersonaCredentialStoreUnavailable
	}
	row := r.db.QueryRowContext(ctx, openAIPersonaCredentialSelect+`
WHERE account_id = $1 AND persona = $2 AND slot_id = $3 AND credential_chain_id = $4`,
		accountID, string(persona), slotID, strings.TrimSpace(credentialChainID))
	record, err := scanOpenAIPersonaCredential(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIPersonaCredentialChainMissing
	}
	return record, err
}

func scanOpenAIPersonaCredential(scanner interface{ Scan(...any) error }) (*service.OpenAIPersonaCredentialRecord, error) {
	var record service.OpenAIPersonaCredentialRecord
	var refreshed sql.NullTime
	if err := scanner.Scan(
		&record.AccountID, &record.PersonaID, &record.CredentialChainID, &record.SlotID,
		&record.EncryptedPayload, &record.ChatGPTAccountID, &record.InstallationID,
		&record.TokenVersion, &record.State, &record.LastError, &record.CreatedAt,
		&record.UpdatedAt, &refreshed,
	); err != nil {
		return nil, err
	}
	if refreshed.Valid {
		record.LastRefreshedAt = &refreshed.Time
	}
	return &record, nil
}

func (r *openAIPersonaCredentialRepository) Authorize(ctx context.Context, input service.OpenAIPersonaCredentialWrite) error {
	if r == nil || r.db == nil {
		return service.ErrOpenAIPersonaCredentialStoreUnavailable
	}
	if err := validateOpenAIPersonaCredentialWrite(input); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	extra, err := lockOpenAIPersonaAccountExtra(ctx, tx, input.AccountID)
	if err != nil {
		return err
	}
	var currentPersona string
	var currentSlotGeneration, currentSetGeneration int64
	err = tx.QueryRowContext(ctx, `SELECT persona, slot_generation, slot_set_generation
FROM openai_account_persona_slots WHERE account_id = $1 AND slot_id = $2 FOR UPDATE`, input.AccountID, input.SlotID).
		Scan(&currentPersona, &currentSlotGeneration, &currentSetGeneration)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (currentPersona != string(input.PersonaID) || currentSlotGeneration != input.SlotGeneration || currentSetGeneration != input.SlotSetGeneration) {
		return service.ErrOpenAIPersonaCredentialCASConflict
	}

	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_account_persona_slots
    (account_id, slot_id, persona, credential_chain_id, enabled, state, authorized,
     session_epoch, slot_generation, slot_set_generation, created_at, updated_at)
VALUES ($1, $2, $3, $4, TRUE, 'active', TRUE, 0, $5, $6, NOW(), NOW())
ON CONFLICT (account_id, slot_id) DO UPDATE SET
    credential_chain_id = EXCLUDED.credential_chain_id,
    authorized = TRUE,
    updated_at = NOW()`, input.AccountID, input.SlotID, string(input.PersonaID),
		input.CredentialChainID, input.SlotGeneration, input.SlotSetGeneration); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_account_persona_credentials
    (account_id, persona, credential_chain_id, slot_id, credentials,
     chatgpt_account_id, installation_id, token_version, state, last_error,
     created_at, updated_at, last_refreshed_at)
VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, 1, 'ready', '', NOW(), NOW(), NOW())`,
		input.AccountID, string(input.PersonaID), input.CredentialChainID, input.SlotID,
		[]byte(input.EncryptedPayload), input.ChatGPTAccountID, input.InstallationID); err != nil {
		return err
	}
	if err = writeOpenAIPersonaProjection(ctx, tx, input.AccountID, extra); err != nil {
		return err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &input.AccountID, nil, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *openAIPersonaCredentialRepository) CompareAndSwapToken(ctx context.Context, input service.OpenAIPersonaCredentialWrite, expectedVersion int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, service.ErrOpenAIPersonaCredentialStoreUnavailable
	}
	if err := validateOpenAIPersonaCredentialWrite(input); err != nil {
		return false, err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE openai_account_persona_credentials
SET credentials = $1::jsonb, token_version = token_version + 1,
    state = 'ready', last_error = '', updated_at = NOW(), last_refreshed_at = NOW()
WHERE account_id = $2 AND persona = $3 AND slot_id = $4
  AND credential_chain_id = $5 AND token_version = $6 AND state = 'refreshing'`,
		[]byte(input.EncryptedPayload), input.AccountID, string(input.PersonaID), input.SlotID,
		input.CredentialChainID, expectedVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *openAIPersonaCredentialRepository) ClaimRefresh(ctx context.Context, accountID int64, persona service.SessionPersonaID, slotID int, credentialChainID string, expectedVersion int64) (bool, error) {
	if r == nil || r.db == nil {
		return false, service.ErrOpenAIPersonaCredentialStoreUnavailable
	}
	result, err := r.db.ExecContext(ctx, `UPDATE openai_account_persona_credentials
SET state = 'refreshing', last_error = '', updated_at = NOW()
WHERE account_id = $1 AND persona = $2 AND slot_id = $3
  AND credential_chain_id = $4 AND token_version = $5 AND state = 'ready'`,
		accountID, string(persona), slotID, strings.TrimSpace(credentialChainID), expectedVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *openAIPersonaCredentialRepository) MarkInvalidIfVersion(ctx context.Context, accountID int64, persona service.SessionPersonaID, slotID int, credentialChainID string, expectedVersion int64, lastError string) error {
	if r == nil || r.db == nil {
		return service.ErrOpenAIPersonaCredentialStoreUnavailable
	}
	lastError = strings.TrimSpace(lastError)
	if len(lastError) > 1000 {
		lastError = lastError[:1000]
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	extra, err := lockOpenAIPersonaAccountExtra(ctx, tx, accountID)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE openai_account_persona_credentials
SET state = 'invalid', last_error = $1, updated_at = NOW()
WHERE account_id = $2 AND persona = $3 AND slot_id = $4
  AND credential_chain_id = $5 AND token_version = $6 AND state IN ('ready', 'refreshing')`,
		lastError, accountID, string(persona), slotID, strings.TrimSpace(credentialChainID), expectedVersion)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_slots
SET credential_chain_id = NULL, authorized = FALSE, updated_at = NOW()
WHERE account_id = $1 AND persona = $2 AND slot_id = $3 AND credential_chain_id = $4`,
		accountID, string(persona), slotID, strings.TrimSpace(credentialChainID)); err != nil {
		return err
	}
	if err = writeOpenAIPersonaProjection(ctx, tx, accountID, extra); err != nil {
		return err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *openAIPersonaCredentialRepository) RevokeSlotAuthorization(ctx context.Context, accountID int64, persona service.SessionPersonaID, slotID int) ([]string, error) {
	if r == nil || r.db == nil {
		return nil, service.ErrOpenAIPersonaCredentialStoreUnavailable
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	extra, err := lockOpenAIPersonaAccountExtra(ctx, tx, accountID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `UPDATE openai_account_persona_credentials
SET credentials = '{}'::jsonb, token_version = token_version + 1,
    state = 'revoked', last_error = 'revoked by administrator', updated_at = NOW()
WHERE account_id = $1 AND persona = $2 AND slot_id = $3 AND state <> 'revoked'
RETURNING credential_chain_id`, accountID, string(persona), slotID)
	if err != nil {
		return nil, err
	}
	var chainIDs []string
	for rows.Next() {
		var chainID string
		if err = rows.Scan(&chainID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		chainIDs = append(chainIDs, chainID)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_slots
SET credential_chain_id = NULL, authorized = FALSE, updated_at = NOW()
WHERE account_id = $1 AND persona = $2 AND slot_id = $3`, accountID, string(persona), slotID); err != nil {
		return nil, err
	}
	if err = writeOpenAIPersonaProjection(ctx, tx, accountID, extra); err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return chainIDs, nil
}

func validateOpenAIPersonaCredentialWrite(input service.OpenAIPersonaCredentialWrite) error {
	if input.AccountID <= 0 || input.SlotID < 0 || strings.TrimSpace(string(input.PersonaID)) == "" ||
		strings.TrimSpace(input.CredentialChainID) == "" || len(input.EncryptedPayload) == 0 ||
		input.SlotGeneration <= 0 || input.SlotSetGeneration <= 0 {
		return errors.New("invalid OpenAI Persona credential write")
	}
	return nil
}

func lockOpenAIPersonaAccountExtra(ctx context.Context, tx *sql.Tx, accountID int64) (map[string]any, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT extra FROM accounts
WHERE id = $1 AND platform = 'openai' AND type = 'oauth' FOR UPDATE`, accountID).Scan(&raw); err != nil {
		return nil, err
	}
	extra := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &extra); err != nil {
			return nil, fmt.Errorf("decode OpenAI account extra: %w", err)
		}
	}
	return extra, nil
}

func writeOpenAIPersonaProjection(ctx context.Context, tx *sql.Tx, accountID int64, extra map[string]any) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.account_id, s.slot_id, s.persona, COALESCE(s.credential_chain_id, ''),
       s.enabled, s.state, s.authorized, s.session_epoch, s.slot_generation, s.slot_set_generation,
       COALESCE(s.upstream_session_id, ''), s.created_at, s.updated_at,
       s.draining_started_at, s.disabled_at, COALESCE(c.installation_id, '')
FROM openai_account_persona_slots s
LEFT JOIN openai_account_persona_credentials c
  ON c.account_id = s.account_id
 AND c.persona = s.persona
 AND c.slot_id = s.slot_id
 AND c.credential_chain_id = s.credential_chain_id
WHERE s.account_id = $1
ORDER BY s.slot_id`, accountID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	activeChains := map[string]any{}
	states := map[string]any{}
	enabled := map[string]any{}
	authorized := map[string]any{}
	generations := map[string]any{}
	installationIDs := map[string]any{}
	var setGeneration int64 = 1
	for rows.Next() {
		var slot service.OpenAIPersonaSlotRecord
		var draining, disabled sql.NullTime
		var installationID string
		if scanErr := rows.Scan(
			&slot.AccountID, &slot.SlotID, &slot.PersonaID, &slot.CredentialChainID,
			&slot.Enabled, &slot.State, &slot.Authorized, &slot.SessionEpoch,
			&slot.SlotGeneration, &slot.SlotSetGeneration, &slot.UpstreamSessionID,
			&slot.CreatedAt, &slot.UpdatedAt, &draining, &disabled, &installationID,
		); scanErr != nil {
			return scanErr
		}
		key := fmt.Sprintf("%d", slot.SlotID)
		states[key] = string(slot.State)
		enabled[key] = slot.Enabled
		authorized[key] = slot.Authorized
		generations[key] = slot.SlotGeneration
		if slot.CredentialChainID != "" {
			activeChains[key] = slot.CredentialChainID
			if installationID != "" {
				installationIDs[key] = installationID
			}
		}
		if slot.SlotSetGeneration > setGeneration {
			setGeneration = slot.SlotSetGeneration
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	extra[service.OpenAIPersonaActiveChainsExtraKey] = activeChains
	extra[service.OpenAIPersonaSlotStateExtraKey] = states
	extra[service.OpenAIPersonaSlotEnabledExtraKey] = enabled
	extra[service.OpenAIPersonaSlotAuthorizedExtraKey] = authorized
	extra[service.OpenAIPersonaSlotGenerationsExtraKey] = generations
	extra[service.OpenAIPersonaInstallationIDsExtraKey] = installationIDs
	extra[service.OpenAIPersonaSlotSetGenerationExtraKey] = setGeneration
	encoded, err := json.Marshal(extra)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra = $1::jsonb, updated_at = NOW() WHERE id = $2`, encoded, accountID)
	return err
}

var _ service.OpenAIPersonaCredentialRepository = (*openAIPersonaCredentialRepository)(nil)
