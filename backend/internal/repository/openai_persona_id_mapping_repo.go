package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.OpenAIPersonaIDMappingStore = (*accountRepository)(nil)

const openAIPersonaIDMappingProjection = `id, user_id, api_key_id, account_id, scope_key, persona, slot_id,
       session_epoch, slot_generation, slot_set_generation, credential_chain_id,
       thread_id, mapping_type, client_id, opencode_id, status,
       parent_mapping_id, root_mapping_id, created_at, updated_at,
       last_seen_at, expires_at,
       COALESCE(account_persona_id, 0), COALESCE(persona_generation, 0),
       COALESCE(persona_session_epoch, 0), COALESCE(profile_id, ''), COALESCE(profile_version, '')`

const openAIPersonaIDMappingSelect = `
SELECT ` + openAIPersonaIDMappingProjection + `
FROM openai_persona_id_mappings`

func validateOpenAIPersonaIDMappingLookup(scope service.OpenAIPersonaIDMappingScope, mappingType service.OpenAIPersonaIDMappingType, value string) error {
	if strings.TrimSpace(scope.ScopeKey) == "" || scope.AccountID <= 0 || strings.TrimSpace(string(scope.Persona)) == "" {
		return errors.New("invalid OpenAI Persona ID mapping scope")
	}
	if strings.TrimSpace(string(mappingType)) == "" || strings.TrimSpace(value) == "" {
		return errors.New("invalid OpenAI Persona ID mapping lookup")
	}
	return nil
}

func scanOpenAIPersonaIDMapping(rows *sql.Rows) (*service.OpenAIPersonaIDMapping, error) {
	var row service.OpenAIPersonaIDMapping
	var parentID, rootID sql.NullInt64
	var personaSessionEpoch int64
	var profileID string
	if err := rows.Scan(
		&row.ID,
		&row.Scope.UserID,
		&row.Scope.APIKeyID,
		&row.Scope.AccountID,
		&row.Scope.ScopeKey,
		&row.Scope.Persona,
		&row.Scope.SlotID,
		&row.Scope.SessionEpoch,
		&row.Scope.SlotGeneration,
		&row.Scope.SlotSetGeneration,
		&row.Scope.CredentialChainID,
		&row.Scope.ThreadID,
		&row.MappingType,
		&row.ClientID,
		&row.OpenCodeID,
		&row.Status,
		&parentID,
		&rootID,
		&row.CreatedAt,
		&row.UpdatedAt,
		&row.LastSeenAt,
		&row.ExpiresAt,
		&row.Scope.AccountPersonaID,
		&row.Scope.PersonaGeneration,
		&personaSessionEpoch,
		&profileID,
		&row.Scope.ProfileVersion,
	); err != nil {
		return nil, err
	}
	if parentID.Valid {
		value := parentID.Int64
		row.ParentMappingID = &value
	}
	if rootID.Valid {
		value := rootID.Int64
		row.RootMappingID = &value
	}
	if row.Scope.AccountPersonaID > 0 {
		if personaSessionEpoch > 0 {
			row.Scope.SessionEpoch = personaSessionEpoch
		}
		if profileID = strings.TrimSpace(profileID); profileID != "" {
			row.Scope.Persona = service.SessionPersonaID(profileID)
		}
	}
	return &row, nil
}

func nullablePositiveOpenAIMappingInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (r *accountRepository) GetOpenAIPersonaIDMappingByClient(ctx context.Context, scope service.OpenAIPersonaIDMappingScope, mappingType service.OpenAIPersonaIDMappingType, clientID string) (*service.OpenAIPersonaIDMapping, error) {
	if r == nil || r.sql == nil {
		return nil, service.ErrOpenAIPersonaIDMappingUnavailable
	}
	if err := validateOpenAIPersonaIDMappingLookup(scope, mappingType, clientID); err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, openAIPersonaIDMappingSelect+`
WHERE scope_key = $1 AND mapping_type = $2 AND client_id = $3
  AND status IN ('active', 'draining') AND expires_at > NOW()
LIMIT 1`, scope.ScopeKey, string(mappingType), strings.TrimSpace(clientID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return scanOpenAIPersonaIDMapping(rows)
}

func (r *accountRepository) GetOpenAIPersonaIDMappingByOpenCode(ctx context.Context, scope service.OpenAIPersonaIDMappingScope, mappingType service.OpenAIPersonaIDMappingType, openCodeID string) (*service.OpenAIPersonaIDMapping, error) {
	if r == nil || r.sql == nil {
		return nil, service.ErrOpenAIPersonaIDMappingUnavailable
	}
	if err := validateOpenAIPersonaIDMappingLookup(scope, mappingType, openCodeID); err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, openAIPersonaIDMappingSelect+`
WHERE scope_key = $1 AND mapping_type = $2 AND opencode_id = $3
  AND status IN ('active', 'draining') AND expires_at > NOW()
LIMIT 1`, scope.ScopeKey, string(mappingType), strings.TrimSpace(openCodeID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return scanOpenAIPersonaIDMapping(rows)
}

func (r *accountRepository) FindOpenAIPersonaIDMappingByPrincipal(ctx context.Context, userID, apiKeyID int64, mappingType service.OpenAIPersonaIDMappingType, clientID string) (*service.OpenAIPersonaIDMapping, error) {
	if r == nil || r.sql == nil {
		return nil, service.ErrOpenAIPersonaIDMappingUnavailable
	}
	if userID <= 0 || apiKeyID <= 0 || strings.TrimSpace(string(mappingType)) == "" || strings.TrimSpace(clientID) == "" {
		return nil, errors.New("invalid OpenAI Persona principal mapping lookup")
	}
	rows, err := r.sql.QueryContext(ctx, openAIPersonaIDMappingSelect+`
WHERE user_id = $1 AND api_key_id = $2 AND mapping_type = $3 AND client_id = $4
  AND status IN ('active', 'draining') AND expires_at > NOW()
ORDER BY updated_at DESC
LIMIT 1`, userID, apiKeyID, string(mappingType), strings.TrimSpace(clientID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return scanOpenAIPersonaIDMapping(rows)
}

func (r *accountRepository) UpsertOpenAIPersonaIDMapping(ctx context.Context, mapping *service.OpenAIPersonaIDMapping) (*service.OpenAIPersonaIDMapping, error) {
	if r == nil || r.sql == nil {
		return nil, service.ErrOpenAIPersonaIDMappingUnavailable
	}
	if mapping == nil || strings.TrimSpace(mapping.Scope.ScopeKey) == "" || mapping.Scope.AccountID <= 0 ||
		strings.TrimSpace(mapping.ClientID) == "" || strings.TrimSpace(mapping.OpenCodeID) == "" || strings.TrimSpace(string(mapping.MappingType)) == "" {
		return nil, errors.New("invalid OpenAI Persona ID mapping")
	}
	now := time.Now().UTC()
	if mapping.ExpiresAt.IsZero() {
		mapping.ExpiresAt = now.Add(30 * 24 * time.Hour)
	}
	if strings.TrimSpace(mapping.Status) == "" {
		mapping.Status = "active"
	}
	// PostgreSQL 同一语句快照看不到数据修改 CTE 刚写入的基础表行，必须直接读取 RETURNING 结果。
	query := `WITH upsert AS (
  INSERT INTO openai_persona_id_mappings
    (user_id, api_key_id, account_id, scope_key, persona, slot_id,
     session_epoch, slot_generation, slot_set_generation, credential_chain_id,
     thread_id, mapping_type, client_id, opencode_id, status,
     parent_mapping_id, root_mapping_id, created_at, updated_at, last_seen_at, expires_at,
     account_persona_id, persona_generation, persona_session_epoch, profile_id, profile_version)
  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$18,$18,$19,$20,$21,$22,$23,$24)
  ON CONFLICT (scope_key, mapping_type, client_id) DO UPDATE SET
    last_seen_at = EXCLUDED.last_seen_at,
    updated_at = EXCLUDED.updated_at,
    expires_at = GREATEST(openai_persona_id_mappings.expires_at, EXCLUDED.expires_at),
    status = CASE WHEN openai_persona_id_mappings.status IN ('expired','revoked') THEN EXCLUDED.status ELSE openai_persona_id_mappings.status END
  RETURNING *
)
SELECT ` + openAIPersonaIDMappingProjection + `
FROM upsert
LIMIT 1`
	rows, err := r.sql.QueryContext(ctx, query,
		mapping.Scope.UserID,
		mapping.Scope.APIKeyID,
		mapping.Scope.AccountID,
		mapping.Scope.ScopeKey,
		string(mapping.Scope.Persona),
		mapping.Scope.SlotID,
		mapping.Scope.SessionEpoch,
		mapping.Scope.SlotGeneration,
		mapping.Scope.SlotSetGeneration,
		mapping.Scope.CredentialChainID,
		mapping.Scope.ThreadID,
		string(mapping.MappingType),
		strings.TrimSpace(mapping.ClientID),
		strings.TrimSpace(mapping.OpenCodeID),
		mapping.Status,
		mapping.ParentMappingID,
		mapping.RootMappingID,
		now,
		mapping.ExpiresAt,
		nullablePositiveOpenAIMappingInt64(mapping.Scope.AccountPersonaID),
		nullablePositiveOpenAIMappingInt64(mapping.Scope.PersonaGeneration),
		nullablePositiveOpenAIMappingInt64(mapping.Scope.SessionEpoch),
		nullableString(string(mapping.Scope.Persona)),
		nullableString(mapping.Scope.ProfileVersion),
	)
	if err != nil {
		return nil, fmt.Errorf("upsert OpenAI Persona ID mapping: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("upsert OpenAI Persona ID mapping returned no row")
	}
	return scanOpenAIPersonaIDMapping(rows)
}
