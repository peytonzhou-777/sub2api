package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

const openAIAccountPersonaMigrationConfirmation = "ACCOUNT_PERSONA_V1"

// OpenAIAccountPersonaMigrationReport 是不含 Token、Session 原文和代理凭据的迁移报告。
type OpenAIAccountPersonaMigrationReport struct {
	ArchitectureVersion     string  `json:"architecture_version"`
	Mode                    string  `json:"mode"`
	AccountCount            int     `json:"account_count"`
	PlannedAccountCount     int     `json:"planned_account_count"`
	ExistingDynamicAccounts int     `json:"existing_dynamic_accounts"`
	MigratedAccounts        int     `json:"migrated_accounts"`
	PersonaCount            int     `json:"persona_count"`
	AuthorizedReadyCount    int     `json:"authorized_ready_count"`
	AccountIDConflictIDs    []int64 `json:"account_id_conflict_ids,omitempty"`
	MissingCredentialIDs    []int64 `json:"missing_credential_ids,omitempty"`
	InvalidProxyAccountIDs  []int64 `json:"invalid_proxy_account_ids,omitempty"`
	UnresolvedBindingCount  int     `json:"unresolved_binding_count"`
	BackfilledMappingCount  int64   `json:"backfilled_mapping_count"`
	BackfilledBindingCount  int64   `json:"backfilled_binding_count"`
	Ready                   bool    `json:"ready"`
}

type openAIAccountPersonaMigrationAccount struct {
	ID          int64
	Credentials map[string]any
	ProxyID     *int64
	Personas    []openAIAccountPersonaMigrationPersona
}

type openAIAccountPersonaMigrationPersona struct {
	Position          int
	ProfileID         service.SessionPersonaID
	ProfileVersion    string
	State             string
	Enabled           bool
	Generation        int64
	SessionEpoch      int64
	CredentialChainID string
	CredentialPayload json.RawMessage
	CredentialVersion int64
	CredentialSource  string
	ChatGPTAccountID  string
	InstallationID    string
	UpstreamSessionID string
	DeviceSeed        []byte
	Ready             bool
}

type legacyOpenAIPersonaSlot struct {
	SlotID            int
	Persona           service.SessionPersonaID
	CredentialChainID string
	Enabled           bool
	State             string
	Authorized        bool
	SessionEpoch      int64
	Generation        int64
	UpstreamSessionID string
}

type legacyOpenAIPersonaCredential struct {
	ChainID          string
	Payload          json.RawMessage
	ChatGPTAccountID string
	InstallationID   string
	TokenVersion     int64
	State            string
}

// RunOpenAIAccountPersonaMigration 执行 dry-run 或维护窗口内的一次性整组迁移。
func RunOpenAIAccountPersonaMigration(ctx context.Context, db *sql.DB, encryptor service.SecretEncryptor, apply bool, confirmation string) (OpenAIAccountPersonaMigrationReport, error) {
	report := OpenAIAccountPersonaMigrationReport{ArchitectureVersion: openAIAccountPersonaArchitectureVersion, Mode: "dry-run"}
	if apply {
		report.Mode = "apply"
		if confirmation != openAIAccountPersonaMigrationConfirmation {
			return report, errors.New("apply requires --confirm ACCOUNT_PERSONA_V1")
		}
	}
	if db == nil || encryptor == nil {
		return report, errors.New("OpenAI AccountPersona migration requires database and encryptor")
	}
	accounts, err := loadOpenAIAccountPersonaMigrationAccounts(ctx, db, encryptor, &report)
	if err != nil {
		return report, err
	}
	report.AccountCount = len(accounts)
	report.PlannedAccountCount = len(accounts)
	report.Ready = len(report.AccountIDConflictIDs) == 0 && len(report.MissingCredentialIDs) == 0 &&
		len(report.InvalidProxyAccountIDs) == 0
	if !apply || !report.Ready {
		return report, nil
	}

	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return report, err
	}
	defer func() { _ = tx.Rollback() }()
	for i := range accounts {
		if len(accounts[i].Personas) > 0 {
			if err = applyOpenAIAccountPersonaMigrationAccount(ctx, tx, &accounts[i]); err != nil {
				return report, fmt.Errorf("migrate OpenAI account %d: %w", accounts[i].ID, err)
			}
		}
		if err = clearOpenAIAccountTopLevelRuntimeTokens(ctx, tx, accounts[i].ID); err != nil {
			return report, fmt.Errorf("clear OpenAI account %d top-level runtime tokens: %w", accounts[i].ID, err)
		}
		report.MigratedAccounts++
	}
	if err = backfillOpenAIAccountPersonaSessionSnapshots(ctx, tx); err != nil {
		return report, err
	}
	if err = backfillOpenAIAccountPersonaMappings(ctx, tx, &report); err != nil {
		return report, err
	}
	if err = backfillOpenAIAccountPersonaConversationBindings(ctx, tx, &report); err != nil {
		return report, err
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return report, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_architecture_state
SET architecture_version = $1, state = 'ready', migration_report = $2::jsonb, updated_at = NOW()
WHERE singleton = TRUE`, openAIAccountPersonaArchitectureVersion, reportJSON); err != nil {
		return report, err
	}
	if err = tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

// clearOpenAIAccountTopLevelRuntimeTokens removes the legacy runtime authority
// after every account has a validated dynamic Persona topology.
func clearOpenAIAccountTopLevelRuntimeTokens(ctx context.Context, tx *sql.Tx, accountID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE accounts
SET credentials = COALESCE(credentials, '{}'::jsonb)
    - 'access_token' - 'refresh_token' - 'id_token' - 'expires_at' - 'client_id',
    updated_at = NOW()
WHERE id = $1 AND platform = 'openai' AND type = 'oauth' AND deleted_at IS NULL`, accountID)
	return err
}

func loadOpenAIAccountPersonaMigrationAccounts(ctx context.Context, db *sql.DB, encryptor service.SecretEncryptor, report *OpenAIAccountPersonaMigrationReport) ([]openAIAccountPersonaMigrationAccount, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, credentials, proxy_id
FROM accounts WHERE platform = 'openai' AND type = 'oauth' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	accounts := make([]openAIAccountPersonaMigrationAccount, 0)
	for rows.Next() {
		var account openAIAccountPersonaMigrationAccount
		var rawCredentials []byte
		var proxyID sql.NullInt64
		if err = rows.Scan(&account.ID, &rawCredentials, &proxyID); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(rawCredentials, &account.Credentials); err != nil {
			return nil, fmt.Errorf("decode account %d credentials: %w", account.ID, err)
		}
		if proxyID.Valid {
			value := proxyID.Int64
			account.ProxyID = &value
		}
		accounts = append(accounts, account)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range accounts {
		if err = planOpenAIAccountPersonaMigration(ctx, db, encryptor, &accounts[i], report); err != nil {
			return nil, err
		}
	}
	return accounts, nil
}

func planOpenAIAccountPersonaMigration(ctx context.Context, db *sql.DB, encryptor service.SecretEncryptor, account *openAIAccountPersonaMigrationAccount, report *OpenAIAccountPersonaMigrationReport) error {
	var existing, ready int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE p.state = 'active' AND p.enabled AND c.state = 'ready')
FROM openai_account_personas p
LEFT JOIN openai_account_persona_credentials c
  ON c.account_persona_id = p.id AND c.credential_chain_id = p.current_credential_chain_id
WHERE p.account_id = $1 AND p.state <> 'retired'`, account.ID).Scan(&existing, &ready); err != nil {
		return err
	}
	if existing > 0 {
		report.ExistingDynamicAccounts++
		report.PersonaCount += existing
		report.AuthorizedReadyCount += ready
		var validPrimary bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM openai_account_personas p
JOIN openai_account_persona_credentials c
  ON c.account_persona_id = p.id AND c.credential_chain_id = p.current_credential_chain_id
WHERE p.account_id = $1 AND p.position = 0 AND p.profile_id = 'codex_cli_strict'
  AND p.credential_owner = 'account_primary' AND c.state = 'ready')`, account.ID).Scan(&validPrimary); err != nil {
			return err
		}
		if !validPrimary {
			report.MissingCredentialIDs = appendUniqueInt64(report.MissingCredentialIDs, account.ID)
		}
		if err := validateExistingOpenAIAccountPersonaCredentialIDs(ctx, db, account, report); err != nil {
			return err
		}
		return reportInvalidAccountProxy(ctx, db, account, report)
	}

	slots, err := loadLegacyOpenAIPersonaSlots(ctx, db, account.ID)
	if err != nil {
		return err
	}
	if len(slots) == 0 {
		slots = []legacyOpenAIPersonaSlot{{SlotID: 0, Persona: service.SessionPersonaCodexCLIStrict, Enabled: true, State: "active", Generation: 1}}
	}
	hasPositionZero := false
	for _, slot := range slots {
		if slot.SlotID == 0 {
			hasPositionZero = true
			if slot.Persona != service.SessionPersonaCodexCLIStrict {
				report.AccountIDConflictIDs = appendUniqueInt64(report.AccountIDConflictIDs, account.ID)
				return nil
			}
		}
	}
	if !hasPositionZero {
		slots = append(slots, legacyOpenAIPersonaSlot{SlotID: 0, Persona: service.SessionPersonaCodexCLIStrict, Enabled: true, State: "active", Generation: 1})
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].SlotID < slots[j].SlotID })

	canonicalAccountID := migrationString(account.Credentials["chatgpt_account_id"])
	credentialsBySlot := make(map[int]*legacyOpenAIPersonaCredential, len(slots))
	for _, slot := range slots {
		credential, credentialErr := loadLegacyOpenAIPersonaCredential(ctx, db, account.ID, slot)
		if credentialErr != nil && !errors.Is(credentialErr, sql.ErrNoRows) {
			return credentialErr
		}
		if credential != nil && credential.State == "ready" && credential.ChatGPTAccountID != "" {
			if canonicalAccountID == "" {
				canonicalAccountID = credential.ChatGPTAccountID
			} else if !strings.EqualFold(canonicalAccountID, credential.ChatGPTAccountID) {
				report.AccountIDConflictIDs = appendUniqueInt64(report.AccountIDConflictIDs, account.ID)
			}
		}
		credentialsBySlot[slot.SlotID] = credential
	}
	for _, slot := range slots {
		credential := credentialsBySlot[slot.SlotID]
		planned, planErr := buildOpenAIAccountPersonaMigrationPersona(encryptor, account, slot, credential, canonicalAccountID)
		if planErr != nil {
			return planErr
		}
		account.Personas = append(account.Personas, planned)
		report.PersonaCount++
		if planned.Ready {
			report.AuthorizedReadyCount++
		} else if slot.Authorized || slot.SlotID == 0 {
			report.MissingCredentialIDs = appendUniqueInt64(report.MissingCredentialIDs, account.ID)
		}
	}
	if canonicalAccountID == "" {
		report.MissingCredentialIDs = appendUniqueInt64(report.MissingCredentialIDs, account.ID)
	}
	return reportInvalidAccountProxy(ctx, db, account, report)
}

func validateExistingOpenAIAccountPersonaCredentialIDs(ctx context.Context, db *sql.DB, account *openAIAccountPersonaMigrationAccount, report *OpenAIAccountPersonaMigrationReport) error {
	var distinctIDs int
	var credentialAccountID sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT LOWER(BTRIM(c.chatgpt_account_id))),
       MIN(NULLIF(BTRIM(c.chatgpt_account_id), ''))
FROM openai_account_persona_credentials c
JOIN openai_account_personas p ON p.id = c.account_persona_id
WHERE p.account_id = $1 AND c.state = 'ready' AND BTRIM(c.chatgpt_account_id) <> ''`, account.ID).
		Scan(&distinctIDs, &credentialAccountID); err != nil {
		return err
	}
	canonicalAccountID := strings.TrimSpace(migrationString(account.Credentials["chatgpt_account_id"]))
	if distinctIDs > 1 || (canonicalAccountID != "" && credentialAccountID.Valid &&
		!strings.EqualFold(canonicalAccountID, credentialAccountID.String)) {
		report.AccountIDConflictIDs = appendUniqueInt64(report.AccountIDConflictIDs, account.ID)
	}
	return nil
}

func loadLegacyOpenAIPersonaSlots(ctx context.Context, db *sql.DB, accountID int64) ([]legacyOpenAIPersonaSlot, error) {
	rows, err := db.QueryContext(ctx, `SELECT slot_id, persona, COALESCE(credential_chain_id, ''), enabled, state,
       authorized, session_epoch, slot_generation, COALESCE(upstream_session_id, '')
FROM openai_account_persona_slots WHERE account_id = $1 ORDER BY slot_id`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []legacyOpenAIPersonaSlot
	for rows.Next() {
		var slot legacyOpenAIPersonaSlot
		if err = rows.Scan(&slot.SlotID, &slot.Persona, &slot.CredentialChainID, &slot.Enabled, &slot.State,
			&slot.Authorized, &slot.SessionEpoch, &slot.Generation, &slot.UpstreamSessionID); err != nil {
			return nil, err
		}
		result = append(result, slot)
	}
	return result, rows.Err()
}

func loadLegacyOpenAIPersonaCredential(ctx context.Context, db *sql.DB, accountID int64, slot legacyOpenAIPersonaSlot) (*legacyOpenAIPersonaCredential, error) {
	var credential legacyOpenAIPersonaCredential
	err := db.QueryRowContext(ctx, `SELECT credential_chain_id, credentials, chatgpt_account_id,
       installation_id, token_version, state
FROM openai_account_persona_credentials
WHERE account_id = $1 AND slot_id = $2 AND persona = $3
ORDER BY CASE WHEN state = 'ready' THEN 0 ELSE 1 END, updated_at DESC LIMIT 1`,
		accountID, slot.SlotID, string(slot.Persona)).Scan(&credential.ChainID, &credential.Payload,
		&credential.ChatGPTAccountID, &credential.InstallationID, &credential.TokenVersion, &credential.State)
	if err != nil {
		return nil, err
	}
	return &credential, nil
}

func buildOpenAIAccountPersonaMigrationPersona(encryptor service.SecretEncryptor, account *openAIAccountPersonaMigrationAccount, slot legacyOpenAIPersonaSlot, credential *legacyOpenAIPersonaCredential, canonicalAccountID string) (openAIAccountPersonaMigrationPersona, error) {
	profileVersion := service.SessionPersonaCodexCLIStrictVersion
	if slot.Persona == service.SessionPersonaOpenCode {
		profileVersion = service.SessionPersonaOpenCodeVersion
	}
	planned := openAIAccountPersonaMigrationPersona{
		Position: slot.SlotID, ProfileID: slot.Persona, ProfileVersion: profileVersion,
		State: slot.State, Enabled: slot.Enabled, Generation: max(slot.Generation, 1),
		SessionEpoch: max(slot.SessionEpoch, 1), UpstreamSessionID: strings.TrimSpace(slot.UpstreamSessionID),
		ChatGPTAccountID: canonicalAccountID,
	}
	if planned.UpstreamSessionID == "" {
		planned.UpstreamSessionID = "sess_" + uuid.NewString()
	}
	planned.DeviceSeed = make([]byte, 32)
	if _, err := rand.Read(planned.DeviceSeed); err != nil {
		return planned, err
	}
	if credential != nil && credential.State == "ready" && len(credential.Payload) > 0 &&
		strings.EqualFold(strings.TrimSpace(credential.ChatGPTAccountID), strings.TrimSpace(canonicalAccountID)) {
		planned.Ready = true
		planned.CredentialSource = "legacy"
		planned.CredentialChainID = credential.ChainID
		planned.CredentialPayload = append([]byte(nil), credential.Payload...)
		planned.CredentialVersion = max(credential.TokenVersion, 1)
		planned.InstallationID = strings.TrimSpace(credential.InstallationID)
	}
	if slot.SlotID == 0 && !planned.Ready {
		refreshToken := migrationString(account.Credentials["refresh_token"])
		if refreshToken != "" && canonicalAccountID != "" {
			payload, err := encryptMigratedOpenAICredential(encryptor, account.Credentials)
			if err != nil {
				return planned, err
			}
			planned.Ready = true
			planned.CredentialSource = "account_primary"
			planned.CredentialChainID = "migrated-primary-" + uuid.NewString()
			planned.CredentialPayload = payload
			planned.CredentialVersion = 1
		}
	}
	if planned.InstallationID == "" {
		planned.InstallationID = "inst_migrated_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if !planned.Ready {
		planned.State = "draft"
		planned.Enabled = false
		planned.SessionEpoch = 0
		planned.UpstreamSessionID = ""
	}
	return planned, nil
}

func encryptMigratedOpenAICredential(encryptor service.SecretEncryptor, credentials map[string]any) (json.RawMessage, error) {
	expiresAt := migrationExpiresAt(credentials["expires_at"])
	plain, err := json.Marshal(map[string]any{
		"access_token":    migrationString(credentials["access_token"]),
		"refresh_token":   migrationString(credentials["refresh_token"]),
		"id_token":        migrationString(credentials["id_token"]),
		"expires_at":      expiresAt,
		"oauth_client_id": migrationString(credentials["client_id"]),
		"email":           migrationString(credentials["email"]),
	})
	if err != nil {
		return nil, err
	}
	ciphertext, err := encryptor.Encrypt(string(plain))
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"format_version": 1, "ciphertext": ciphertext})
}

func applyOpenAIAccountPersonaMigrationAccount(ctx context.Context, tx *sql.Tx, account *openAIAccountPersonaMigrationAccount) error {
	for _, persona := range account.Personas {
		owner := "persona_independent"
		if persona.Position == 0 {
			owner = "account_primary"
		}
		var personaID int64
		err := tx.QueryRowContext(ctx, `INSERT INTO openai_account_personas
    (account_id, position, profile_id, profile_version, credential_owner, state, enabled,
     persona_generation, current_credential_chain_id, current_session_epoch, device_seed,
     installation_id, proxy_id, row_version)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULL,1)
RETURNING id`, account.ID, persona.Position, string(persona.ProfileID), persona.ProfileVersion,
			owner, persona.State, persona.Enabled, persona.Generation, nullableString(persona.CredentialChainID),
			persona.SessionEpoch, persona.DeviceSeed, persona.InstallationID).Scan(&personaID)
		if err != nil {
			return err
		}
		if !persona.Ready {
			continue
		}
		if persona.CredentialSource == "legacy" {
			result, updateErr := tx.ExecContext(ctx, `UPDATE openai_account_persona_credentials
SET account_persona_id=$1, profile_id=$2, profile_version=$3, persona_generation=$4
WHERE account_id=$5 AND persona=$2 AND credential_chain_id=$6 AND state='ready'`,
				personaID, string(persona.ProfileID), persona.ProfileVersion, persona.Generation,
				account.ID, persona.CredentialChainID)
			if updateErr != nil {
				return updateErr
			}
			if affected, _ := result.RowsAffected(); affected != 1 {
				return errors.New("legacy credential changed during migration")
			}
		} else {
			if _, err = tx.ExecContext(ctx, `INSERT INTO openai_account_persona_credentials
    (account_id, persona, credential_chain_id, slot_id, credentials, chatgpt_account_id,
     installation_id, token_version, state, last_error, last_refreshed_at,
     account_persona_id, profile_id, profile_version, persona_generation)
VALUES ($1,$2,$3,NULL,$4::jsonb,$5,$6,$7,'ready','',NOW(),$8,$2,$9,$10)`,
				account.ID, string(persona.ProfileID), persona.CredentialChainID, []byte(persona.CredentialPayload),
				persona.ChatGPTAccountID, persona.InstallationID, persona.CredentialVersion,
				personaID, persona.ProfileVersion, persona.Generation); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO openai_account_persona_sessions
    (account_persona_id, session_epoch, upstream_session_id, state, persona_generation,
     credential_chain_id, profile_id, profile_version, effective_proxy_id, proxy_revision,
     effective_proxy_url, installation_id, proxy_snapshot_set)
VALUES ($1,$2,$3,'current',$4,$5,$6,$7,NULL,0,'',$8,FALSE)`,
			personaID, persona.SessionEpoch, persona.UpstreamSessionID, persona.Generation,
			persona.CredentialChainID, string(persona.ProfileID), persona.ProfileVersion, persona.InstallationID); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE accounts SET
credentials = credentials - 'access_token' - 'refresh_token' - 'id_token' - 'expires_at' - 'client_id',
updated_at = NOW() WHERE id = $1`, account.ID)
	return err
}

// backfillOpenAIAccountPersonaSessionSnapshots 在切换门变为 ready 前冻结每个 current
// Session 的实际代理出口；候选查询只接受已完成该快照的 Session。
func backfillOpenAIAccountPersonaSessionSnapshots(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT account_persona_id
FROM openai_account_persona_sessions
WHERE state = 'current' AND proxy_snapshot_set = FALSE
ORDER BY account_persona_id`)
	if err != nil {
		return err
	}
	personaIDs := make([]int64, 0)
	for rows.Next() {
		var personaID int64
		if err = rows.Scan(&personaID); err != nil {
			_ = rows.Close()
			return err
		}
		personaIDs = append(personaIDs, personaID)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}

	for _, personaID := range personaIDs {
		persona, loadErr := scanOpenAIAccountPersona(tx.QueryRowContext(ctx,
			openAIAccountPersonaSelect+` WHERE id = $1 AND state <> 'retired' FOR UPDATE`, personaID))
		if loadErr != nil {
			return loadErr
		}
		proxyID, proxyRevision, proxyURL, snapshotErr := resolveAccountPersonaProxySnapshot(ctx, tx, persona)
		if snapshotErr != nil {
			return snapshotErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE openai_account_persona_sessions
SET effective_proxy_id = $1::bigint, proxy_revision = $2, effective_proxy_url = $3,
    installation_id = $4, proxy_snapshot_set = TRUE, updated_at = NOW()
WHERE account_persona_id = $5 AND state = 'current' AND proxy_snapshot_set = FALSE`,
			proxyID, proxyRevision, proxyURL, persona.InstallationID, persona.ID); err != nil {
			return err
		}
	}
	return nil
}

func backfillOpenAIAccountPersonaMappings(ctx context.Context, tx *sql.Tx, report *OpenAIAccountPersonaMigrationReport) error {
	result, err := tx.ExecContext(ctx, `UPDATE openai_persona_id_mappings m SET
account_persona_id=p.id, persona_generation=p.persona_generation,
persona_session_epoch=COALESCE(NULLIF(m.session_epoch,0),p.current_session_epoch),
profile_id=p.profile_id, profile_version=p.profile_version, updated_at=NOW()
FROM openai_account_personas p
WHERE m.account_persona_id IS NULL AND p.account_id=m.account_id AND p.position=m.slot_id
  AND p.profile_id=m.persona AND p.state <> 'retired'`)
	if err != nil {
		return err
	}
	report.BackfilledMappingCount, _ = result.RowsAffected()
	return nil
}

func backfillOpenAIAccountPersonaConversationBindings(ctx context.Context, tx *sql.Tx, report *OpenAIAccountPersonaMigrationReport) error {
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_user_conversation_bindings
WHERE account_persona_id IS NULL AND status IN ('provisional','active','draining')`).Scan(&report.UnresolvedBindingCount); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `WITH unique_persona AS (
    SELECT p.account_id, MIN(p.id) AS persona_id, MIN(p.current_session_epoch) AS session_epoch,
           MIN(p.current_credential_chain_id) AS chain_id, MIN(p.profile_id) AS profile_id,
           MIN(p.profile_version) AS profile_version
    FROM openai_account_personas p
    JOIN openai_account_persona_credentials c
      ON c.account_persona_id=p.id AND c.credential_chain_id=p.current_credential_chain_id AND c.state='ready'
    WHERE p.state IN ('active','draining')
    GROUP BY p.account_id HAVING COUNT(*)=1
)
UPDATE openai_user_conversation_bindings b SET
account_persona_id=u.persona_id, persona_session_epoch=u.session_epoch,
credential_chain_id=u.chain_id, profile_id=u.profile_id, profile_version=u.profile_version,
updated_at=NOW()
FROM unique_persona u
WHERE b.account_id=u.account_id AND b.account_persona_id IS NULL
  AND b.status IN ('provisional','active','draining')`)
	if err != nil {
		return err
	}
	report.BackfilledBindingCount, _ = result.RowsAffected()
	report.UnresolvedBindingCount -= int(report.BackfilledBindingCount)
	if report.UnresolvedBindingCount < 0 {
		report.UnresolvedBindingCount = 0
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_user_conversation_bindings SET
status='expired', context_rebuildable=FALSE, active_until=NULL, expires_at=LEAST(expires_at,NOW()), updated_at=NOW()
WHERE account_persona_id IS NULL AND status IN ('provisional','active','draining')`)
	return err
}

func reportInvalidAccountProxy(ctx context.Context, db *sql.DB, account *openAIAccountPersonaMigrationAccount, report *OpenAIAccountPersonaMigrationReport) error {
	var invalidPersonaProxy bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (
SELECT 1 FROM openai_account_personas p
LEFT JOIN proxies proxy ON proxy.id = p.proxy_id AND proxy.status = 'active' AND proxy.deleted_at IS NULL
WHERE p.account_id = $1 AND p.state <> 'retired' AND p.proxy_id IS NOT NULL AND proxy.id IS NULL)`, account.ID).
		Scan(&invalidPersonaProxy); err != nil {
		return err
	}
	invalidAccountProxy := false
	if account.ProxyID != nil {
		var valid bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM proxies WHERE id=$1 AND status='active' AND deleted_at IS NULL)`, *account.ProxyID).Scan(&valid); err != nil {
			return err
		}
		invalidAccountProxy = !valid
	}
	if invalidAccountProxy || invalidPersonaProxy {
		report.InvalidProxyAccountIDs = appendUniqueInt64(report.InvalidProxyAccountIDs, account.ID)
	}
	return nil
}

func migrationString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func migrationExpiresAt(value any) int64 {
	raw := migrationString(value)
	if raw == "" {
		return 0
	}
	if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return parsed
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.Unix()
	}
	return 0
}

func appendUniqueInt64(values []int64, value int64) []int64 {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}
