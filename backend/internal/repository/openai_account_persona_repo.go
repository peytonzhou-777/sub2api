package repository

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type openAIAccountPersonaRepository struct {
	db *sql.DB
}

func NewOpenAIAccountPersonaRepository(db *sql.DB) service.OpenAIAccountPersonaRepository {
	return &openAIAccountPersonaRepository{db: db}
}

const openAIAccountPersonaSelect = `SELECT id, account_id, position, profile_id, profile_version,
       credential_owner, state, enabled, persona_generation,
       COALESCE(current_credential_chain_id, ''), current_session_epoch, device_seed,
       installation_id, proxy_id, max_active_client_sessions_override, row_version,
       created_at, updated_at, draining_started_at, disabled_at, retired_at
FROM openai_account_personas`

func scanOpenAIAccountPersona(scanner interface{ Scan(...any) error }) (*service.OpenAIAccountPersona, error) {
	var (
		persona                           service.OpenAIAccountPersona
		proxyID                           sql.NullInt64
		maxActive                         sql.NullInt64
		drainingAt, disabledAt, retiredAt sql.NullTime
	)
	if err := scanner.Scan(
		&persona.ID, &persona.AccountID, &persona.Position, &persona.ProfileID, &persona.ProfileVersion,
		&persona.CredentialOwner, &persona.State, &persona.Enabled, &persona.PersonaGeneration,
		&persona.CurrentCredentialChainID, &persona.CurrentSessionEpoch, &persona.DeviceSeed,
		&persona.InstallationID, &proxyID, &maxActive, &persona.RowVersion,
		&persona.CreatedAt, &persona.UpdatedAt, &drainingAt, &disabledAt, &retiredAt,
	); err != nil {
		return nil, err
	}
	if proxyID.Valid {
		value := proxyID.Int64
		persona.ProxyID = &value
	}
	if maxActive.Valid {
		value := int(maxActive.Int64)
		persona.MaxActiveClientSessionsOverride = &value
	}
	if drainingAt.Valid {
		persona.DrainingStartedAt = &drainingAt.Time
	}
	if disabledAt.Valid {
		persona.DisabledAt = &disabledAt.Time
	}
	if retiredAt.Valid {
		persona.RetiredAt = &retiredAt.Time
	}
	return &persona, nil
}

const openAIAccountPersonaSessionSelect = `SELECT account_persona_id, session_epoch, upstream_session_id,
       state, persona_generation, credential_chain_id, profile_id, profile_version,
       effective_proxy_id, proxy_revision, effective_proxy_url, installation_id, proxy_snapshot_set, started_at, last_active_at,
       draining_started_at, expires_at
FROM openai_account_persona_sessions`

func scanOpenAIAccountPersonaSession(scanner interface{ Scan(...any) error }) (*service.OpenAIAccountPersonaSession, error) {
	var (
		session                           service.OpenAIAccountPersonaSession
		proxyID                           sql.NullInt64
		lastActive, drainingAt, expiresAt sql.NullTime
	)
	if err := scanner.Scan(
		&session.AccountPersonaID, &session.SessionEpoch, &session.UpstreamSessionID,
		&session.State, &session.PersonaGeneration, &session.CredentialChainID,
		&session.ProfileID, &session.ProfileVersion, &proxyID, &session.ProxyRevision,
		&session.EffectiveProxyURL, &session.InstallationID, &session.ProxySnapshotSet,
		&session.StartedAt, &lastActive, &drainingAt, &expiresAt,
	); err != nil {
		return nil, err
	}
	if proxyID.Valid {
		value := proxyID.Int64
		session.EffectiveProxyID = &value
	}
	if lastActive.Valid {
		session.LastActiveAt = &lastActive.Time
	}
	if drainingAt.Valid {
		session.DrainingStartedAt = &drainingAt.Time
	}
	if expiresAt.Valid {
		session.ExpiresAt = &expiresAt.Time
	}
	return &session, nil
}

func (r *openAIAccountPersonaRepository) ListAccountPersonas(ctx context.Context, accountID int64) ([]service.OpenAIAccountPersona, error) {
	if r == nil || r.db == nil || accountID <= 0 {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	rows, err := r.db.QueryContext(ctx, openAIAccountPersonaSelect+`
WHERE account_id = $1 AND state <> 'retired' ORDER BY position, id`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.OpenAIAccountPersona, 0)
	for rows.Next() {
		persona, scanErr := scanOpenAIAccountPersona(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *persona)
	}
	return result, rows.Err()
}

func (r *openAIAccountPersonaRepository) GetAccountPersona(ctx context.Context, accountID, accountPersonaID int64) (*service.OpenAIAccountPersona, error) {
	if r == nil || r.db == nil || accountID <= 0 || accountPersonaID <= 0 {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	persona, err := scanOpenAIAccountPersona(r.db.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE account_id = $1 AND id = $2 AND state <> 'retired'`, accountID, accountPersonaID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	return persona, err
}

func (r *openAIAccountPersonaRepository) CreateAccountPersona(ctx context.Context, input service.OpenAIAccountPersonaCreate) (*service.OpenAIAccountPersona, error) {
	if r == nil || r.db == nil || input.AccountID <= 0 || input.ProfileID == "" ||
		strings.TrimSpace(input.ProfileVersion) == "" || len(input.DeviceSeed) < 16 || strings.TrimSpace(input.InstallationID) == "" {
		return nil, errors.New("invalid OpenAI AccountPersona create")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var platform, accountType string
	if err = tx.QueryRowContext(ctx, `SELECT platform, type FROM accounts
WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, input.AccountID).Scan(&platform, &accountType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, service.ErrAccountNotFound
		}
		return nil, err
	}
	if platform != service.PlatformOpenAI || accountType != service.AccountTypeOAuth {
		return nil, errors.New("account is not an OpenAI OAuth account")
	}
	var hasDefault bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM openai_account_personas
    WHERE account_id = $1 AND position = 0 AND state <> 'retired'
)`, input.AccountID).Scan(&hasDefault); err != nil {
		return nil, err
	}
	if !hasDefault {
		return nil, errors.New("OpenAI account default Persona is missing")
	}
	if input.ProxyID != nil {
		var valid bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM proxies WHERE id = $1::bigint AND status = 'active' AND deleted_at IS NULL
)`, input.ProxyID).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, errors.New("Persona proxy is unavailable")
		}
	}
	var position int
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1
FROM openai_account_personas WHERE account_id = $1 AND state <> 'retired'`, input.AccountID).Scan(&position); err != nil {
		return nil, err
	}
	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, `INSERT INTO openai_account_personas
    (account_id, position, profile_id, profile_version, credential_owner, state, enabled,
     persona_generation, current_session_epoch, device_seed, installation_id, proxy_id,
     max_active_client_sessions_override, row_version)
VALUES ($1, $2, $3, $4, 'persona_independent', 'draft', TRUE, 1, 0, $5, $6, $7::bigint, $8::int, 1)
RETURNING id, account_id, position, profile_id, profile_version, credential_owner, state,
          enabled, persona_generation, COALESCE(current_credential_chain_id, ''),
          current_session_epoch, device_seed, installation_id, proxy_id,
          max_active_client_sessions_override, row_version, created_at, updated_at,
          draining_started_at, disabled_at, retired_at`, input.AccountID, position,
		string(input.ProfileID), strings.TrimSpace(input.ProfileVersion), input.DeviceSeed,
		strings.TrimSpace(input.InstallationID), input.ProxyID, input.MaxActiveClientSessionsOverride))
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &input.AccountID, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return persona, nil
}

func (r *openAIAccountPersonaRepository) UpdateAccountPersona(ctx context.Context, input service.OpenAIAccountPersonaUpdate) (*service.OpenAIAccountPersona, error) {
	if r == nil || r.db == nil || input.AccountID <= 0 || input.AccountPersonaID <= 0 || input.ExpectedRowVersion <= 0 {
		return nil, errors.New("invalid OpenAI AccountPersona update")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE account_id = $1 AND id = $2 AND state <> 'retired' FOR UPDATE`, input.AccountID, input.AccountPersonaID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	if err != nil {
		return nil, err
	}
	if current.RowVersion != input.ExpectedRowVersion {
		return nil, service.ErrOpenAIAccountPersonaCASConflict
	}

	enabled := current.Enabled
	state := current.State
	proxyID := current.ProxyID
	maxActive := current.MaxActiveClientSessionsOverride
	if input.Enabled != nil {
		enabled = *input.Enabled
		if !enabled && state == service.OpenAIAccountPersonaStateActive {
			state = service.OpenAIAccountPersonaStateDraining
		}
	}
	if input.State != nil {
		state = *input.State
	}
	if input.ProxyConfigured {
		proxyID = input.ProxyID
	}
	if input.MaxActiveSessionsConfigured {
		maxActive = input.MaxActiveClientSessionsOverride
	}
	requiresNewSession := input.ProxyConfigured && !sameNullableInt64(current.ProxyID, proxyID)
	if (current.State == service.OpenAIAccountPersonaStateDisabled || current.State == service.OpenAIAccountPersonaStateDraining) &&
		state == service.OpenAIAccountPersonaStateActive && enabled {
		requiresNewSession = true
	}
	if requiresNewSession && strings.TrimSpace(input.NewUpstreamSessionID) == "" {
		return nil, errors.New("new upstream Session ID is required for Persona generation change")
	}
	if proxyID != nil {
		var valid bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM proxies WHERE id = $1::bigint AND status = 'active' AND deleted_at IS NULL
)`, proxyID).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, errors.New("Persona proxy is unavailable")
		}
	}

	personaGeneration := current.PersonaGeneration
	sessionEpoch := current.CurrentSessionEpoch
	if requiresNewSession {
		if current.CurrentCredentialChainID == "" {
			return nil, errors.New("cannot rotate an unauthorized Persona Session")
		}
		if input.OldSessionExpiresAt.IsZero() {
			return nil, errors.New("old Persona Session expiry is required")
		}
		snapshotPersona := *current
		snapshotPersona.ProxyID = proxyID
		effectiveProxyID, proxyRevision, proxyURL, snapshotErr := resolveAccountPersonaProxySnapshot(ctx, tx, &snapshotPersona)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		personaGeneration++
		sessionEpoch++
		if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_sessions
SET state = 'draining', draining_started_at = NOW(), expires_at = $1::timestamptz, updated_at = NOW()
WHERE account_persona_id = $2 AND state = 'current'`, input.OldSessionExpiresAt, current.ID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO openai_account_persona_sessions
    (account_persona_id, session_epoch, upstream_session_id, state, persona_generation,
     credential_chain_id, profile_id, profile_version, effective_proxy_id, proxy_revision,
     effective_proxy_url, installation_id, proxy_snapshot_set)
VALUES ($1, $2, $3, 'current', $4, $5, $6, $7, $8::bigint, $9, $10, $11, TRUE)`,
			current.ID, sessionEpoch, strings.TrimSpace(input.NewUpstreamSessionID), personaGeneration,
			current.CurrentCredentialChainID, string(current.ProfileID), current.ProfileVersion,
			effectiveProxyID, proxyRevision, proxyURL, current.InstallationID); err != nil {
			return nil, err
		}
	}

	updated, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, `UPDATE openai_account_personas SET
    enabled = $1, state = $2, proxy_id = $3::bigint,
    max_active_client_sessions_override = $4::int,
    persona_generation = $5, current_session_epoch = $6,
    row_version = row_version + 1, updated_at = NOW(),
    draining_started_at = CASE WHEN $2 = 'draining' THEN COALESCE(draining_started_at, NOW()) ELSE NULL END,
    disabled_at = CASE WHEN $2 = 'disabled' THEN COALESCE(disabled_at, NOW()) ELSE NULL END
WHERE id = $7 AND account_id = $8 AND row_version = $9
RETURNING id, account_id, position, profile_id, profile_version, credential_owner, state,
          enabled, persona_generation, COALESCE(current_credential_chain_id, ''),
          current_session_epoch, device_seed, installation_id, proxy_id,
          max_active_client_sessions_override, row_version, created_at, updated_at,
          draining_started_at, disabled_at, retired_at`, enabled, string(state), proxyID, maxActive,
		personaGeneration, sessionEpoch, current.ID, current.AccountID, current.RowVersion))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaCASConflict
	}
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &input.AccountID, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func sameNullableInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (r *openAIAccountPersonaRepository) RetireAccountPersona(ctx context.Context, accountID, accountPersonaID, expectedRowVersion int64) error {
	if r == nil || r.db == nil || accountID <= 0 || accountPersonaID <= 0 || expectedRowVersion <= 0 {
		return errors.New("invalid OpenAI AccountPersona retire")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE account_id = $1 AND id = $2 AND state <> 'retired' FOR UPDATE`, accountID, accountPersonaID))
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrOpenAIAccountPersonaNotFound
	}
	if err != nil {
		return err
	}
	if persona.IsDefaultProtected() {
		return service.ErrOpenAIDefaultPersonaProtected
	}
	if persona.RowVersion != expectedRowVersion {
		return service.ErrOpenAIAccountPersonaCASConflict
	}
	var inUse bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM openai_persona_client_session_leases l
    WHERE l.account_persona_id = $1 AND l.state IN ('provisional', 'active')
      AND (l.active_until > NOW() OR EXISTS (
          SELECT 1 FROM openai_persona_request_holds h
          WHERE h.lease_id = l.id AND h.expires_at > NOW()
      ))
    UNION ALL
    SELECT 1 FROM openai_user_conversation_bindings b
    WHERE b.account_persona_id = $1 AND b.status IN ('provisional', 'active', 'draining') AND b.expires_at > NOW()
)`, accountPersonaID).Scan(&inUse); err != nil {
		return err
	}
	if inUse {
		return errors.New("OpenAI AccountPersona is still draining")
	}
	result, err := tx.ExecContext(ctx, `UPDATE openai_account_personas
SET state = 'retired', enabled = FALSE, retired_at = NOW(), updated_at = NOW(), row_version = row_version + 1
WHERE id = $1 AND account_id = $2 AND row_version = $3`, accountPersonaID, accountID, expectedRowVersion)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrOpenAIAccountPersonaCASConflict
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *openAIAccountPersonaRepository) AuthorizeAccountPersona(ctx context.Context, input service.OpenAIAccountPersonaAuthorization) (*service.OpenAIAccountPersona, error) {
	if r == nil || r.db == nil || input.AccountID <= 0 || input.AccountPersonaID <= 0 ||
		input.ExpectedRowVersion <= 0 || input.PersonaGeneration <= 0 || strings.TrimSpace(input.CredentialChainID) == "" ||
		len(input.EncryptedPayload) == 0 || strings.TrimSpace(input.ChatGPTAccountID) == "" ||
		strings.TrimSpace(input.InstallationID) == "" || strings.TrimSpace(input.UpstreamSessionID) == "" {
		return nil, errors.New("invalid OpenAI AccountPersona authorization")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE account_id = $1 AND id = $2 AND state <> 'retired' FOR UPDATE`, input.AccountID, input.AccountPersonaID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	if err != nil {
		return nil, err
	}
	if persona.IsDefaultProtected() {
		return nil, service.ErrOpenAIDefaultPersonaProtected
	}
	if persona.RowVersion != input.ExpectedRowVersion || persona.PersonaGeneration != input.PersonaGeneration ||
		persona.InstallationID != strings.TrimSpace(input.InstallationID) {
		return nil, service.ErrOpenAIAccountPersonaCASConflict
	}
	nextGeneration := persona.PersonaGeneration + 1
	nextEpoch := persona.CurrentSessionEpoch + 1
	if nextEpoch < 1 {
		nextEpoch = 1
	}
	if input.OldSessionExpiresAt.IsZero() {
		return nil, errors.New("old Persona Session expiry is required")
	}
	effectiveProxyID, proxyRevision, proxyURL, err := resolveAccountPersonaProxySnapshot(ctx, tx, persona)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_credentials
SET state = 'draining', updated_at = NOW()
WHERE account_persona_id = $1 AND state IN ('ready', 'refreshing')`, persona.ID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_sessions
SET state = 'draining', draining_started_at = NOW(), expires_at = $1::timestamptz, updated_at = NOW()
WHERE account_persona_id = $2 AND state = 'current'`, input.OldSessionExpiresAt, persona.ID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_account_persona_credentials
    (account_id, persona, credential_chain_id, slot_id, credentials, chatgpt_account_id,
     installation_id, token_version, state, last_error, last_refreshed_at,
     account_persona_id, profile_id, profile_version, persona_generation)
VALUES ($1, $2, $3, NULL, $4::jsonb, $5, $6, 1, 'ready', '', NOW(), $7, $2, $8, $9)`,
		persona.AccountID, string(persona.ProfileID), strings.TrimSpace(input.CredentialChainID),
		[]byte(input.EncryptedPayload), strings.TrimSpace(input.ChatGPTAccountID),
		strings.TrimSpace(input.InstallationID), persona.ID, persona.ProfileVersion, nextGeneration); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_account_persona_sessions
    (account_persona_id, session_epoch, upstream_session_id, state, persona_generation,
     credential_chain_id, profile_id, profile_version, effective_proxy_id, proxy_revision,
     effective_proxy_url, installation_id, proxy_snapshot_set)
VALUES ($1, $2, $3, 'current', $4, $5, $6, $7, $8::bigint, $9, $10, $11, TRUE)`, persona.ID, nextEpoch,
		strings.TrimSpace(input.UpstreamSessionID), nextGeneration, strings.TrimSpace(input.CredentialChainID),
		string(persona.ProfileID), persona.ProfileVersion, effectiveProxyID, proxyRevision,
		proxyURL, strings.TrimSpace(input.InstallationID)); err != nil {
		return nil, err
	}
	updated, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, `UPDATE openai_account_personas
SET current_credential_chain_id = $1, current_session_epoch = $2, persona_generation = $3,
    state = 'active', enabled = TRUE, row_version = row_version + 1, updated_at = NOW(),
    draining_started_at = NULL, disabled_at = NULL
WHERE id = $4 AND account_id = $5 AND row_version = $6
RETURNING id, account_id, position, profile_id, profile_version, credential_owner, state,
          enabled, persona_generation, COALESCE(current_credential_chain_id, ''),
          current_session_epoch, device_seed, installation_id, proxy_id,
          max_active_client_sessions_override, row_version, created_at, updated_at,
          draining_started_at, disabled_at, retired_at`, strings.TrimSpace(input.CredentialChainID),
		nextEpoch, nextGeneration, persona.ID, persona.AccountID, persona.RowVersion))
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &input.AccountID, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (r *openAIAccountPersonaRepository) RevokeAccountPersonaAuthorization(ctx context.Context, accountID, accountPersonaID, expectedRowVersion int64) ([]string, error) {
	if r == nil || r.db == nil || accountID <= 0 || accountPersonaID <= 0 || expectedRowVersion <= 0 {
		return nil, errors.New("invalid OpenAI AccountPersona revoke")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE account_id = $1 AND id = $2 AND state <> 'retired' FOR UPDATE`, accountID, accountPersonaID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	if err != nil {
		return nil, err
	}
	if persona.IsDefaultProtected() {
		return nil, service.ErrOpenAIDefaultPersonaProtected
	}
	if persona.RowVersion != expectedRowVersion {
		return nil, service.ErrOpenAIAccountPersonaCASConflict
	}
	rows, err := tx.QueryContext(ctx, `UPDATE openai_account_persona_credentials
SET credentials = '{}'::jsonb, token_version = token_version + 1, state = 'revoked',
    last_error = 'revoked by administrator', updated_at = NOW()
WHERE account_persona_id = $1 AND state <> 'revoked' RETURNING credential_chain_id`, persona.ID)
	if err != nil {
		return nil, err
	}
	chainIDs := make([]string, 0)
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
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_sessions
SET state = 'revoked', expires_at = NOW(), updated_at = NOW()
WHERE account_persona_id = $1 AND state IN ('current', 'draining')`, persona.ID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_personas
SET current_credential_chain_id = NULL, current_session_epoch = 0, state = 'disabled',
    enabled = FALSE, persona_generation = persona_generation + 1,
    disabled_at = NOW(), updated_at = NOW(), row_version = row_version + 1
WHERE id = $1 AND account_id = $2 AND row_version = $3`, persona.ID, accountID, expectedRowVersion); err != nil {
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

const openAIAccountPersonaCredentialSelect = `SELECT account_persona_id, account_id, profile_id,
       profile_version, persona_generation, credential_chain_id, COALESCE(slot_id, -1),
       credentials, chatgpt_account_id, installation_id, token_version, state,
       last_error, created_at, updated_at, last_refreshed_at
FROM openai_account_persona_credentials`

func (r *openAIAccountPersonaRepository) GetAccountPersonaCredential(ctx context.Context, accountPersonaID int64, credentialChainID string) (*service.OpenAIPersonaCredentialRecord, error) {
	if r == nil || r.db == nil || accountPersonaID <= 0 || strings.TrimSpace(credentialChainID) == "" {
		return nil, service.ErrOpenAIPersonaCredentialChainMissing
	}
	var record service.OpenAIPersonaCredentialRecord
	var refreshed sql.NullTime
	err := r.db.QueryRowContext(ctx, openAIAccountPersonaCredentialSelect+`
WHERE account_persona_id = $1 AND credential_chain_id = $2`, accountPersonaID, strings.TrimSpace(credentialChainID)).Scan(
		&record.AccountPersonaID, &record.AccountID, &record.PersonaID, &record.ProfileVersion,
		&record.PersonaGeneration, &record.CredentialChainID, &record.SlotID,
		&record.EncryptedPayload, &record.ChatGPTAccountID, &record.InstallationID,
		&record.TokenVersion, &record.State, &record.LastError, &record.CreatedAt,
		&record.UpdatedAt, &refreshed,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIPersonaCredentialChainMissing
	}
	if err != nil {
		return nil, err
	}
	if refreshed.Valid {
		record.LastRefreshedAt = &refreshed.Time
	}
	return &record, nil
}

func (r *openAIAccountPersonaRepository) ClaimAccountPersonaCredentialRefresh(ctx context.Context, accountPersonaID int64, credentialChainID string, expectedVersion int64) (bool, error) {
	if r == nil || r.db == nil || accountPersonaID <= 0 || strings.TrimSpace(credentialChainID) == "" || expectedVersion < 0 {
		return false, errors.New("invalid OpenAI AccountPersona refresh claim")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE openai_account_persona_credentials
SET state = 'refreshing', last_error = '', updated_at = NOW()
WHERE account_persona_id = $1 AND credential_chain_id = $2 AND token_version = $3 AND state = 'ready'`,
		accountPersonaID, strings.TrimSpace(credentialChainID), expectedVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *openAIAccountPersonaRepository) CompareAndSwapAccountPersonaToken(ctx context.Context, input service.OpenAIAccountPersonaCredentialUpdate, expectedVersion int64) (bool, error) {
	if r == nil || r.db == nil || input.AccountPersonaID <= 0 || strings.TrimSpace(input.CredentialChainID) == "" ||
		len(input.EncryptedPayload) == 0 || expectedVersion < 0 {
		return false, errors.New("invalid OpenAI AccountPersona token update")
	}
	result, err := r.db.ExecContext(ctx, `UPDATE openai_account_persona_credentials
SET credentials = $1::jsonb, chatgpt_account_id = $2, installation_id = $3,
    token_version = token_version + 1, state = 'ready', last_error = '',
    updated_at = NOW(), last_refreshed_at = NOW()
WHERE account_persona_id = $4 AND credential_chain_id = $5
  AND token_version = $6 AND state = 'refreshing'`, []byte(input.EncryptedPayload),
		strings.TrimSpace(input.ChatGPTAccountID), strings.TrimSpace(input.InstallationID),
		input.AccountPersonaID, strings.TrimSpace(input.CredentialChainID), expectedVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (r *openAIAccountPersonaRepository) MarkAccountPersonaCredentialInvalid(ctx context.Context, accountPersonaID int64, credentialChainID string, expectedVersion int64, reason string) error {
	if r == nil || r.db == nil || accountPersonaID <= 0 || strings.TrimSpace(credentialChainID) == "" || expectedVersion < 0 {
		return errors.New("invalid OpenAI AccountPersona credential invalidation")
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE openai_account_persona_credentials
SET state = 'invalid', last_error = $1, updated_at = NOW()
WHERE account_persona_id = $2 AND credential_chain_id = $3 AND token_version = $4
  AND state IN ('ready', 'refreshing')`, reason, accountPersonaID, strings.TrimSpace(credentialChainID), expectedVersion)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_personas
SET state = 'disabled', enabled = FALSE, disabled_at = NOW(), updated_at = NOW(), row_version = row_version + 1
WHERE id = $1 AND current_credential_chain_id = $2`, accountPersonaID, strings.TrimSpace(credentialChainID)); err != nil {
		return err
	}
	return tx.Commit()
}

// PrepareAccountPersonaSession 在数据库锁内判断占用和轮转，避免不同实例同时创建 current epoch。
func (r *openAIAccountPersonaRepository) PrepareAccountPersonaSession(ctx context.Context, input service.OpenAIAccountPersonaSessionPrepareInput) (*service.OpenAIAccountPersonaSessionPrepareResult, error) {
	if r == nil || r.db == nil || input.AccountID <= 0 || input.AccountPersonaID <= 0 || input.Now.IsZero() ||
		strings.TrimSpace(input.NewUpstreamSession) == "" {
		return nil, errors.New("invalid OpenAI AccountPersona Session prepare")
	}
	if err := service.ValidateCodexFingerprintEpochPolicy(input.Policy); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE account_id = $1 AND id = $2 AND state <> 'retired' FOR UPDATE`, input.AccountID, input.AccountPersonaID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	if err != nil {
		return nil, err
	}
	if input.ExpectedRowVersion > 0 && persona.RowVersion != input.ExpectedRowVersion {
		return nil, service.ErrOpenAIAccountPersonaCASConflict
	}
	if !persona.AcceptsNewRoot() {
		return nil, service.ErrOpenAIAccountPersonaIdentityMismatch
	}
	session, err := scanOpenAIAccountPersonaSession(tx.QueryRowContext(ctx, openAIAccountPersonaSessionSelect+`
WHERE account_persona_id = $1 AND session_epoch = $2 AND state = 'current' FOR UPDATE`,
		persona.ID, persona.CurrentSessionEpoch))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaSessionNotFound
	}
	if err != nil {
		return nil, err
	}

	proxyID, proxyRevision, proxyURL, err := resolveAccountPersonaProxySnapshot(ctx, tx, persona)
	if err != nil {
		return nil, err
	}
	// 253/254 阶段创建的 Session 尚无显式代理快照；首次进入新运行时前补齐。
	if !session.ProxySnapshotSet || session.InstallationID == "" {
		if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_sessions
SET effective_proxy_id = $1::bigint, proxy_revision = $2, effective_proxy_url = $3,
    installation_id = $4, proxy_snapshot_set = TRUE, updated_at = $5::timestamptz
WHERE account_persona_id = $6 AND session_epoch = $7 AND state = 'current'`,
			proxyID, proxyRevision, proxyURL, persona.InstallationID, input.Now, persona.ID, session.SessionEpoch); err != nil {
			return nil, err
		}
		session.EffectiveProxyID = proxyID
		session.ProxyRevision = proxyRevision
		session.EffectiveProxyURL = proxyURL
		session.InstallationID = persona.InstallationID
		session.ProxySnapshotSet = true
	}

	proxyChanged := !sameNullableInt64(session.EffectiveProxyID, proxyID) ||
		session.ProxyRevision != proxyRevision || session.EffectiveProxyURL != proxyURL
	shouldRotate := input.Manual || proxyChanged || shouldRotateAccountPersonaSession(*session, persona.ID, input.Policy, input.Now)
	if !shouldRotate {
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return &service.OpenAIAccountPersonaSessionPrepareResult{Persona: *persona, Session: *session}, nil
	}
	occupied, err := accountPersonaSessionOccupied(ctx, tx, persona.ID, input.Now)
	if err != nil {
		return nil, err
	}
	if occupied && !input.Force {
		if input.Manual {
			return nil, service.ErrOpenAIAccountPersonaSessionOccupied
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return &service.OpenAIAccountPersonaSessionPrepareResult{Persona: *persona, Session: *session}, nil
	}

	oldState := service.OpenAIPersonaSessionDraining
	expiresAt := input.Now.Add(time.Duration(input.Policy.OldEpochGraceHours) * time.Hour)
	if input.Force {
		oldState = service.OpenAIPersonaSessionRevoked
		expiresAt = input.Now
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_sessions
SET state = $1, draining_started_at = $2::timestamptz, expires_at = $3::timestamptz, updated_at = $2::timestamptz
WHERE account_persona_id = $4 AND session_epoch = $5 AND state = 'current'`,
		string(oldState), input.Now, expiresAt, persona.ID, session.SessionEpoch); err != nil {
		return nil, err
	}
	nextEpoch := session.SessionEpoch + 1
	nextPersonaGeneration := persona.PersonaGeneration
	if proxyChanged {
		nextPersonaGeneration++
	}
	created, err := scanOpenAIAccountPersonaSession(tx.QueryRowContext(ctx, `INSERT INTO openai_account_persona_sessions
    (account_persona_id, session_epoch, upstream_session_id, state, persona_generation,
     credential_chain_id, profile_id, profile_version, effective_proxy_id, proxy_revision,
     effective_proxy_url, installation_id, proxy_snapshot_set, started_at, updated_at)
VALUES ($1, $2, $3, 'current', $4, $5, $6, $7, $8::bigint, $9, $10, $11, TRUE, $12::timestamptz, $12::timestamptz)
RETURNING account_persona_id, session_epoch, upstream_session_id, state, persona_generation,
          credential_chain_id, profile_id, profile_version, effective_proxy_id, proxy_revision,
          effective_proxy_url, installation_id, proxy_snapshot_set, started_at, last_active_at, draining_started_at, expires_at`,
		persona.ID, nextEpoch, strings.TrimSpace(input.NewUpstreamSession), nextPersonaGeneration,
		persona.CurrentCredentialChainID, string(persona.ProfileID), persona.ProfileVersion,
		proxyID, proxyRevision, proxyURL, persona.InstallationID, input.Now))
	if err != nil {
		return nil, err
	}
	updated, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, `UPDATE openai_account_personas
SET current_session_epoch = $1, persona_generation = $2, row_version = row_version + 1, updated_at = $3::timestamptz
WHERE id = $4 AND account_id = $5 AND row_version = $6
RETURNING id, account_id, position, profile_id, profile_version, credential_owner, state,
          enabled, persona_generation, COALESCE(current_credential_chain_id, ''),
          current_session_epoch, device_seed, installation_id, proxy_id,
          max_active_client_sessions_override, row_version, created_at, updated_at,
          draining_started_at, disabled_at, retired_at`, nextEpoch, nextPersonaGeneration, input.Now,
		persona.ID, persona.AccountID, persona.RowVersion))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaCASConflict
	}
	if err != nil {
		return nil, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &persona.AccountID, nil, nil); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &service.OpenAIAccountPersonaSessionPrepareResult{Persona: *updated, Session: *created, Rotated: true}, nil
}

func shouldRotateAccountPersonaSession(session service.OpenAIAccountPersonaSession, personaID int64, policy service.CodexFingerprintEpochPolicy, now time.Time) bool {
	age := now.Sub(session.StartedAt)
	if age < 0 {
		return false
	}
	lastActive := session.StartedAt
	if session.LastActiveAt != nil && session.LastActiveAt.After(lastActive) {
		lastActive = *session.LastActiveAt
	}
	if age >= time.Duration(policy.MaxSessionAgeHours)*time.Hour {
		return true
	}
	if age < time.Duration(policy.MinSessionAgeHours)*time.Hour || now.Sub(lastActive) < time.Duration(policy.IdleGateMinutes)*time.Minute {
		return false
	}
	if policy.RotationJitterHours <= 0 {
		return true
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(strings.Join([]string{
		strconv.FormatInt(personaID, 10), strconv.FormatInt(session.PersonaGeneration, 10), strconv.FormatInt(session.SessionEpoch, 10),
	}, ":")))
	jitter := time.Duration(hash.Sum64()%uint64(policy.RotationJitterHours+1)) * time.Hour
	threshold := time.Duration(policy.MinSessionAgeHours)*time.Hour + jitter
	if threshold > time.Duration(policy.MaxSessionAgeHours)*time.Hour {
		threshold = time.Duration(policy.MaxSessionAgeHours) * time.Hour
	}
	return age >= threshold
}

func accountPersonaSessionOccupied(ctx context.Context, tx *sql.Tx, accountPersonaID int64, now time.Time) (bool, error) {
	var occupied bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM openai_persona_client_session_leases lease
    WHERE lease.account_persona_id = $1
      AND lease.state IN ('provisional', 'active')
      AND (
        lease.active_until > $2::timestamptz OR EXISTS (
          SELECT 1 FROM openai_persona_request_holds hold
          WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz
        )
      )
)`, accountPersonaID, now).Scan(&occupied)
	return occupied, err
}

func resolveAccountPersonaProxySnapshot(ctx context.Context, tx *sql.Tx, persona *service.OpenAIAccountPersona) (*int64, int64, string, error) {
	proxyID := persona.ProxyID
	if proxyID == nil {
		var accountProxyID sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT proxy_id FROM accounts WHERE id = $1 AND deleted_at IS NULL`, persona.AccountID).Scan(&accountProxyID); err != nil {
			return nil, 0, "", err
		}
		if accountProxyID.Valid {
			value := accountProxyID.Int64
			proxyID = &value
		}
	}
	if proxyID == nil {
		return nil, 0, "", nil
	}
	var proxy service.Proxy
	if err := tx.QueryRowContext(ctx, `SELECT id, name, protocol, host, port,
       COALESCE(username, ''), COALESCE(password, ''), status, created_at, updated_at
FROM proxies WHERE id = $1::bigint AND status = 'active' AND deleted_at IS NULL`, proxyID).Scan(
		&proxy.ID, &proxy.Name, &proxy.Protocol, &proxy.Host, &proxy.Port,
		&proxy.Username, &proxy.Password, &proxy.Status, &proxy.CreatedAt, &proxy.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, "", errors.New("Persona proxy is unavailable")
		}
		return nil, 0, "", err
	}
	return proxyID, proxy.UpdatedAt.UnixMicro(), proxy.URL(), nil
}

func (r *openAIAccountPersonaRepository) GetAccountPersonaSession(ctx context.Context, accountID, accountPersonaID, sessionEpoch int64, now time.Time) (*service.OpenAIAccountPersonaSession, error) {
	if r == nil || r.db == nil || accountID <= 0 || accountPersonaID <= 0 || sessionEpoch <= 0 || now.IsZero() {
		return nil, service.ErrOpenAIAccountPersonaSessionNotFound
	}
	session, err := scanOpenAIAccountPersonaSession(r.db.QueryRowContext(ctx, openAIAccountPersonaSessionSelect+`
WHERE account_persona_id = $1 AND session_epoch = $2
  AND EXISTS (SELECT 1 FROM openai_account_personas p WHERE p.id = $1 AND p.account_id = $3)
  AND state IN ('current', 'draining')
  AND (expires_at IS NULL OR expires_at > $4::timestamptz)`, accountPersonaID, sessionEpoch, accountID, now))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaSessionExpired
	}
	return session, err
}

func (r *openAIAccountPersonaRepository) TouchAccountPersonaSession(ctx context.Context, accountPersonaID, sessionEpoch int64, now time.Time) error {
	if r == nil || r.db == nil || accountPersonaID <= 0 || sessionEpoch <= 0 || now.IsZero() {
		return service.ErrOpenAIAccountPersonaSessionNotFound
	}
	result, err := r.db.ExecContext(ctx, `UPDATE openai_account_persona_sessions
SET last_active_at = $1::timestamptz, updated_at = $1::timestamptz
WHERE account_persona_id = $2 AND session_epoch = $3
  AND state IN ('current', 'draining') AND (expires_at IS NULL OR expires_at > $1::timestamptz)`,
		now, accountPersonaID, sessionEpoch)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrOpenAIAccountPersonaSessionExpired
	}
	return nil
}

var _ service.OpenAIAccountPersonaRepository = (*openAIAccountPersonaRepository)(nil)
