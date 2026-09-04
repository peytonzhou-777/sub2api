package repository

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type openAIClientSessionReservationRepository struct{ db *sql.DB }

// NewOpenAIClientSessionReservationRepository 创建 PostgreSQL 权威的两级 Session 预留仓储。
func NewOpenAIClientSessionReservationRepository(db *sql.DB) service.OpenAIClientSessionReservationRepository {
	return &openAIClientSessionReservationRepository{db: db}
}

func validClientSessionReservation(token, hash string, now, holdUntil time.Time) bool {
	_, tokenErr := uuid.Parse(strings.TrimSpace(token))
	decoded, hashErr := hex.DecodeString(strings.TrimSpace(hash))
	return tokenErr == nil && hashErr == nil && len(decoded) == 32 && !now.IsZero() && holdUntil.After(now)
}

func (r *openAIClientSessionReservationRepository) ReserveUserGroupSession(ctx context.Context, input service.OpenAIUserGroupSessionReserveInput) (*service.OpenAIClientSessionLeaseReservation, error) {
	if r == nil || r.db == nil || input.UserID <= 0 || input.EffectiveGroupID <= 0 || input.MaxSessions < 1 ||
		!validClientSessionReservation(input.ReservationToken, input.ClientSessionHash, input.Now, input.HoldUntil) {
		return nil, errors.New("invalid OpenAI User x Group Session reservation")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_user_group_client_session_scopes
    (user_id, effective_group_id, updated_at) VALUES ($1, $2, $3::timestamptz)
ON CONFLICT (user_id, effective_group_id) DO NOTHING`, input.UserID, input.EffectiveGroupID, input.Now); err != nil {
		return nil, err
	}
	var scopeVersion int64
	if err = tx.QueryRowContext(ctx, `SELECT row_version FROM openai_user_group_client_session_scopes
WHERE user_id = $1 AND effective_group_id = $2 FOR UPDATE`, input.UserID, input.EffectiveGroupID).Scan(&scopeVersion); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_user_group_session_request_holds WHERE expires_at <= $1::timestamptz`, input.Now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_user_group_client_session_leases lease
SET state = 'expired', updated_at = $1::timestamptz
WHERE lease.user_id = $2 AND lease.effective_group_id = $3 AND lease.state = 'active'
  AND lease.active_until <= $1::timestamptz
  AND NOT EXISTS (SELECT 1 FROM openai_user_group_session_request_holds hold
                  WHERE hold.lease_id = lease.id AND hold.expires_at > $1::timestamptz)`, input.Now, input.UserID, input.EffectiveGroupID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_user_group_client_session_leases lease
WHERE lease.user_id = $1 AND lease.effective_group_id = $2 AND lease.state = 'provisional'
  AND NOT EXISTS (SELECT 1 FROM openai_user_group_session_request_holds hold
                  WHERE hold.lease_id = lease.id AND hold.expires_at > $3::timestamptz)`, input.UserID, input.EffectiveGroupID, input.Now); err != nil {
		return nil, err
	}

	var leaseID, generation int64
	var state string
	err = tx.QueryRowContext(ctx, `SELECT id, state, generation FROM openai_user_group_client_session_leases
WHERE user_id = $1 AND effective_group_id = $2 AND client_session_hash = $3 FOR UPDATE`,
		input.UserID, input.EffectiveGroupID, strings.ToLower(input.ClientSessionHash)).Scan(&leaseID, &state, &generation)
	created := false
	alreadyActive := err == nil && state == "active"
	if errors.Is(err, sql.ErrNoRows) {
		var occupied int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_user_group_client_session_leases lease
WHERE lease.user_id = $1 AND lease.effective_group_id = $2
  AND ((lease.state = 'active' AND lease.active_until > $3::timestamptz)
       OR EXISTS (SELECT 1 FROM openai_user_group_session_request_holds hold
                  WHERE hold.lease_id = lease.id AND hold.expires_at > $3::timestamptz))`,
			input.UserID, input.EffectiveGroupID, input.Now).Scan(&occupied); err != nil {
			return nil, err
		}
		if occupied >= input.MaxSessions {
			return nil, service.ErrOpenAIUserGroupSessionCapacity
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO openai_user_group_client_session_leases
    (user_id, effective_group_id, client_session_hash, state, generation, active_until, created_at, updated_at)
VALUES ($1, $2, $3, 'provisional', 1, NULL::timestamptz, $4::timestamptz, $4::timestamptz)
RETURNING id, generation`, input.UserID, input.EffectiveGroupID, strings.ToLower(input.ClientSessionHash), input.Now).Scan(&leaseID, &generation)
		created = true
	} else if err != nil {
		return nil, err
	} else if state == "expired" || state == "revoked" {
		var occupied int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_user_group_client_session_leases lease
WHERE lease.user_id = $1 AND lease.effective_group_id = $2
  AND ((lease.state = 'active' AND lease.active_until > $3::timestamptz)
       OR EXISTS (SELECT 1 FROM openai_user_group_session_request_holds hold
                  WHERE hold.lease_id = lease.id AND hold.expires_at > $3::timestamptz))`,
			input.UserID, input.EffectiveGroupID, input.Now).Scan(&occupied); err != nil {
			return nil, err
		}
		if occupied >= input.MaxSessions {
			return nil, service.ErrOpenAIUserGroupSessionCapacity
		}
		if _, err = tx.ExecContext(ctx, `UPDATE openai_user_group_client_session_leases
SET state = 'provisional', generation = generation + 1, last_active_at = NULL::timestamptz,
    active_until = NULL::timestamptz, updated_at = $1::timestamptz WHERE id = $2`, input.Now, leaseID); err != nil {
			return nil, err
		}
		created = true
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_user_group_session_request_holds
    (reservation_token, lease_id, expires_at, created_at) VALUES ($1::uuid, $2, $3::timestamptz, $4::timestamptz)`,
		input.ReservationToken, leaseID, input.HoldUntil, input.Now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_user_group_client_session_scopes
SET row_version = row_version + 1, updated_at = $1::timestamptz WHERE user_id = $2 AND effective_group_id = $3`,
		input.Now, input.UserID, input.EffectiveGroupID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &service.OpenAIClientSessionLeaseReservation{ReservationToken: input.ReservationToken, LeaseID: leaseID, Created: created, AlreadyActive: alreadyActive}, nil
}

func (r *openAIClientSessionReservationRepository) ReservePersonaSession(ctx context.Context, input service.OpenAIPersonaSessionReserveInput) (*service.OpenAIClientSessionLeaseReservation, error) {
	if r == nil || r.db == nil || input.AccountID <= 0 || input.AccountPersonaID <= 0 || input.UserID <= 0 || input.APIKeyID <= 0 || input.MaxSessions < 1 ||
		!validClientSessionReservation(input.ReservationToken, input.ClientSessionHash, input.Now, input.HoldUntil) {
		return nil, errors.New("invalid OpenAI AccountPersona Session reservation")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var lockedAccountID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, input.AccountID).Scan(&lockedAccountID); err != nil {
		return nil, err
	}
	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE id = $1 AND account_id = $2 FOR UPDATE`, input.AccountPersonaID, input.AccountID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	if err != nil {
		return nil, err
	}
	if !persona.AcceptsNewRoot() {
		return nil, service.ErrOpenAIPersonaSessionCapacity
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_persona_request_holds WHERE account_persona_id = $1 AND expires_at <= $2::timestamptz`, input.AccountPersonaID, input.Now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_client_session_leases lease SET state = 'expired', updated_at = $1::timestamptz
WHERE lease.account_persona_id = $2 AND lease.state = 'active' AND lease.active_until <= $1::timestamptz
  AND NOT EXISTS (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $1::timestamptz)`, input.Now, input.AccountPersonaID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_persona_client_session_leases lease
WHERE lease.account_persona_id = $1 AND lease.state = 'provisional'
  AND NOT EXISTS (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz)`, input.AccountPersonaID, input.Now); err != nil {
		return nil, err
	}

	var claimedPersonaID int64
	var claimUntil time.Time
	claimErr := tx.QueryRowContext(ctx, `SELECT account_persona_id, active_until FROM openai_account_user_persona_claims
WHERE account_id = $1 AND user_id = $2 FOR UPDATE`, input.AccountID, input.UserID).Scan(&claimedPersonaID, &claimUntil)
	if claimErr != nil && !errors.Is(claimErr, sql.ErrNoRows) {
		return nil, claimErr
	}
	if claimErr == nil && claimedPersonaID != input.AccountPersonaID {
		var live bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM openai_persona_client_session_leases lease
WHERE lease.account_persona_id = $1 AND lease.user_id = $2
  AND ((lease.state = 'active' AND lease.active_until > $3::timestamptz)
       OR EXISTS (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $3::timestamptz)))`,
			claimedPersonaID, input.UserID, input.Now).Scan(&live); err != nil {
			return nil, err
		}
		if live || claimUntil.After(input.Now) {
			return nil, service.ErrOpenAIAccountPersonaClaim
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM openai_account_user_persona_claims WHERE account_id = $1 AND user_id = $2`, input.AccountID, input.UserID); err != nil {
			return nil, err
		}
		claimErr = sql.ErrNoRows
	}

	var leaseID int64
	var state string
	err = tx.QueryRowContext(ctx, `SELECT id, state FROM openai_persona_client_session_leases
WHERE account_persona_id = $1 AND user_id = $2 AND api_key_id = $3 AND client_session_hash = $4 FOR UPDATE`,
		input.AccountPersonaID, input.UserID, input.APIKeyID, strings.ToLower(input.ClientSessionHash)).Scan(&leaseID, &state)
	created := false
	alreadyActive := err == nil && state == "active"
	if errors.Is(err, sql.ErrNoRows) {
		var occupied int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_persona_client_session_leases lease
WHERE lease.account_persona_id = $1
  AND ((lease.state = 'active' AND lease.active_until > $2::timestamptz)
       OR EXISTS (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz))`,
			input.AccountPersonaID, input.Now).Scan(&occupied); err != nil {
			return nil, err
		}
		if occupied >= input.MaxSessions {
			return nil, service.ErrOpenAIPersonaSessionCapacity
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO openai_persona_client_session_leases
    (account_persona_id, account_id, user_id, api_key_id, client_session_hash, state, generation,
     active_until, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'provisional', 1, NULL::timestamptz, $6::timestamptz, $6::timestamptz)
RETURNING id`, input.AccountPersonaID, input.AccountID, input.UserID, input.APIKeyID,
			strings.ToLower(input.ClientSessionHash), input.Now).Scan(&leaseID)
		created = true
	} else if err != nil {
		return nil, err
	} else if state == "expired" || state == "revoked" {
		var occupied int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_persona_client_session_leases lease
WHERE lease.account_persona_id = $1
  AND ((lease.state = 'active' AND lease.active_until > $2::timestamptz)
       OR EXISTS (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz))`,
			input.AccountPersonaID, input.Now).Scan(&occupied); err != nil {
			return nil, err
		}
		if occupied >= input.MaxSessions {
			return nil, service.ErrOpenAIPersonaSessionCapacity
		}
		if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_client_session_leases
SET state = 'provisional', generation = generation + 1, last_active_at = NULL::timestamptz,
    active_until = NULL::timestamptz, updated_at = $1::timestamptz WHERE id = $2`, input.Now, leaseID); err != nil {
			return nil, err
		}
		created = true
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_persona_request_holds
    (reservation_token, lease_id, account_persona_id, expires_at, created_at)
VALUES ($1::uuid, $2, $3, $4::timestamptz, $5::timestamptz)`, input.ReservationToken, leaseID, input.AccountPersonaID, input.HoldUntil, input.Now); err != nil {
		return nil, err
	}
	if claimErr == sql.ErrNoRows {
		_, err = tx.ExecContext(ctx, `INSERT INTO openai_account_user_persona_claims
    (account_id, user_id, account_persona_id, active_until, created_at, updated_at)
VALUES ($1, $2, $3, $4::timestamptz, $5::timestamptz, $5::timestamptz)`, input.AccountID, input.UserID, input.AccountPersonaID, input.HoldUntil, input.Now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE openai_account_user_persona_claims
SET active_until = GREATEST(active_until, $1::timestamptz), row_version = row_version + 1, updated_at = $2::timestamptz
WHERE account_id = $3 AND user_id = $4 AND account_persona_id = $5`, input.HoldUntil, input.Now, input.AccountID, input.UserID, input.AccountPersonaID)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &service.OpenAIClientSessionLeaseReservation{ReservationToken: input.ReservationToken, LeaseID: leaseID, Created: created, AlreadyActive: alreadyActive}, nil
}

func (r *openAIClientSessionReservationRepository) CommitClientSessionReservation(ctx context.Context, input service.OpenAIClientSessionReservationCommit) (service.OpenAIExecutionTarget, error) {
	if r == nil || r.db == nil || input.ActiveUntil.Before(input.Now) {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIClientSessionReservation
	}
	if _, err := uuid.Parse(strings.TrimSpace(input.ReservationToken)); err != nil {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIClientSessionReservation
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var userID, groupID, userLeaseID int64
	if err = tx.QueryRowContext(ctx, `SELECT lease.user_id, lease.effective_group_id, lease.id
FROM openai_user_group_session_request_holds hold JOIN openai_user_group_client_session_leases lease ON lease.id = hold.lease_id
WHERE hold.reservation_token = $1::uuid AND hold.expires_at > $2::timestamptz`, input.ReservationToken, input.Now).Scan(&userID, &groupID, &userLeaseID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.OpenAIExecutionTarget{}, service.ErrOpenAIClientSessionReservation
		}
		return service.OpenAIExecutionTarget{}, err
	}
	var scopeVersion int64
	if err = tx.QueryRowContext(ctx, `SELECT row_version FROM openai_user_group_client_session_scopes WHERE user_id = $1 AND effective_group_id = $2 FOR UPDATE`, userID, groupID).Scan(&scopeVersion); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	var checkedUserLease int64
	if err = tx.QueryRowContext(ctx, `SELECT lease.id FROM openai_user_group_session_request_holds hold
JOIN openai_user_group_client_session_leases lease ON lease.id = hold.lease_id
WHERE hold.reservation_token = $1::uuid AND hold.expires_at > $2::timestamptz FOR UPDATE OF hold, lease`, input.ReservationToken, input.Now).Scan(&checkedUserLease); err != nil {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIClientSessionReservation
	}

	var accountID, personaID, personaLeaseID int64
	if err = tx.QueryRowContext(ctx, `SELECT lease.account_id, lease.account_persona_id, lease.id
FROM openai_persona_request_holds hold JOIN openai_persona_client_session_leases lease ON lease.id = hold.lease_id
WHERE hold.reservation_token = $1::uuid AND hold.expires_at > $2::timestamptz`, input.ReservationToken, input.Now).Scan(&accountID, &personaID, &personaLeaseID); err != nil {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIClientSessionReservation
	}
	var lockedAccountID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&lockedAccountID); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+` WHERE id = $1 AND account_id = $2 FOR UPDATE`, personaID, accountID))
	if err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	var checkedPersonaLease int64
	if err = tx.QueryRowContext(ctx, `SELECT lease.id FROM openai_persona_request_holds hold
JOIN openai_persona_client_session_leases lease ON lease.id = hold.lease_id
WHERE hold.reservation_token = $1::uuid AND hold.expires_at > $2::timestamptz FOR UPDATE OF hold, lease`, input.ReservationToken, input.Now).Scan(&checkedPersonaLease); err != nil {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIClientSessionReservation
	}
	if checkedUserLease != userLeaseID || checkedPersonaLease != personaLeaseID {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIClientSessionReservation
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_user_group_client_session_leases SET state = 'active', last_active_at = $1::timestamptz, active_until = $2::timestamptz, updated_at = $1::timestamptz WHERE id = $3`, input.Now, input.ActiveUntil, userLeaseID); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_client_session_leases SET state = 'active', last_active_at = $1::timestamptz, active_until = $2::timestamptz, updated_at = $1::timestamptz WHERE id = $3`, input.Now, input.ActiveUntil, personaLeaseID); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_account_user_persona_claims SET active_until = $1::timestamptz, row_version = row_version + 1, updated_at = $2::timestamptz WHERE account_id = $3 AND user_id = $4 AND account_persona_id = $5`, input.ActiveUntil, input.Now, accountID, userID, personaID); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_user_group_session_request_holds WHERE reservation_token = $1::uuid`, input.ReservationToken); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_persona_request_holds WHERE reservation_token = $1::uuid`, input.ReservationToken); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	session, err := scanOpenAIAccountPersonaSession(tx.QueryRowContext(ctx, openAIAccountPersonaSessionSelect+` WHERE account_persona_id = $1 AND session_epoch = $2 AND state = 'current'`, personaID, persona.CurrentSessionEpoch))
	if err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	target, err := service.OpenAIExecutionTargetFromPersonaSession(*persona, *session)
	if err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	target.UserGroupLeaseID, target.PersonaLeaseID, target.ReservationToken = userLeaseID, personaLeaseID, input.ReservationToken
	if err = tx.Commit(); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	return target, nil
}

func (r *openAIClientSessionReservationRepository) RollbackClientSessionReservation(ctx context.Context, token string, now time.Time) error {
	if r == nil || r.db == nil {
		return service.ErrOpenAIClientSessionReservation
	}
	if _, err := uuid.Parse(strings.TrimSpace(token)); err != nil {
		return service.ErrOpenAIClientSessionReservation
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var userID, groupID, userLeaseID int64
	userErr := tx.QueryRowContext(ctx, `SELECT lease.user_id, lease.effective_group_id, lease.id FROM openai_user_group_session_request_holds hold JOIN openai_user_group_client_session_leases lease ON lease.id = hold.lease_id WHERE hold.reservation_token = $1::uuid`, token).Scan(&userID, &groupID, &userLeaseID)
	if userErr == nil {
		var version int64
		if err = tx.QueryRowContext(ctx, `SELECT row_version FROM openai_user_group_client_session_scopes WHERE user_id = $1 AND effective_group_id = $2 FOR UPDATE`, userID, groupID).Scan(&version); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM openai_user_group_session_request_holds WHERE reservation_token = $1::uuid`, token); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM openai_user_group_client_session_leases lease WHERE id = $1 AND state = 'provisional' AND NOT EXISTS (SELECT 1 FROM openai_user_group_session_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz)`, userLeaseID, now); err != nil {
			return err
		}
	} else if !errors.Is(userErr, sql.ErrNoRows) {
		return userErr
	}
	var accountID, personaID, personaLeaseID, personaUserID int64
	personaErr := tx.QueryRowContext(ctx, `SELECT lease.account_id, lease.account_persona_id, lease.id, lease.user_id FROM openai_persona_request_holds hold JOIN openai_persona_client_session_leases lease ON lease.id = hold.lease_id WHERE hold.reservation_token = $1::uuid`, token).Scan(&accountID, &personaID, &personaLeaseID, &personaUserID)
	if personaErr == nil {
		var locked int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, accountID).Scan(&locked); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT id FROM openai_account_personas WHERE id = $1 AND account_id = $2 FOR UPDATE`, personaID, accountID).Scan(&locked); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM openai_persona_request_holds WHERE reservation_token = $1::uuid`, token); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM openai_persona_client_session_leases lease WHERE id = $1 AND state = 'provisional' AND NOT EXISTS (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz)`, personaLeaseID, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM openai_account_user_persona_claims claim WHERE account_id = $1 AND user_id = $2 AND account_persona_id = $3 AND NOT EXISTS (SELECT 1 FROM openai_persona_client_session_leases lease WHERE lease.account_persona_id = claim.account_persona_id AND lease.user_id = claim.user_id AND ((lease.state = 'active' AND lease.active_until > $4::timestamptz) OR EXISTS (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $4::timestamptz)))`, accountID, personaUserID, personaID, now); err != nil {
			return err
		}
	} else if !errors.Is(personaErr, sql.ErrNoRows) {
		return personaErr
	}
	return tx.Commit()
}

func (r *openAIClientSessionReservationRepository) ListOpenAIPersonaCapacityCandidates(ctx context.Context, accountIDs []int64, userID, apiKeyID int64, clientSessionHash string, now time.Time) ([]service.OpenAIPersonaCapacityCandidate, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("OpenAI Persona capacity repository unavailable")
	}
	if len(accountIDs) == 0 {
		return []service.OpenAIPersonaCapacityCandidate{}, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT persona.id, persona.account_id, persona.position, persona.profile_id, persona.profile_version,
       persona.credential_owner, persona.state, persona.enabled, persona.persona_generation,
       COALESCE(persona.current_credential_chain_id, ''), persona.current_session_epoch, persona.device_seed,
       persona.installation_id, persona.proxy_id, persona.max_active_client_sessions_override, persona.row_version,
       persona.created_at, persona.updated_at, persona.draining_started_at, persona.disabled_at, persona.retired_at,
       session.account_persona_id, session.session_epoch, session.upstream_session_id, session.state,
       session.persona_generation, session.credential_chain_id, session.profile_id, session.profile_version,
       session.effective_proxy_id, session.proxy_revision, session.effective_proxy_url, session.installation_id,
       session.proxy_snapshot_set, session.started_at, session.last_active_at, session.draining_started_at, session.expires_at,
       (SELECT COUNT(*) FROM openai_persona_client_session_leases lease WHERE lease.account_persona_id = persona.id
          AND ((lease.state = 'active' AND lease.active_until > $3::timestamptz) OR EXISTS
              (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $3::timestamptz))) AS active_clients,
       (SELECT MIN(lease.active_until) FROM openai_persona_client_session_leases lease WHERE lease.account_persona_id = persona.id
          AND lease.state = 'active' AND lease.active_until > $3::timestamptz) AS earliest_release,
		COALESCE(claim.account_persona_id = persona.id, FALSE) AS claimed_by_user,
		EXISTS (SELECT 1 FROM openai_persona_client_session_leases lease
		        WHERE lease.account_persona_id = persona.id AND lease.user_id = $4 AND lease.api_key_id = $5
		          AND lease.client_session_hash = $6
		          AND ((lease.state = 'active' AND lease.active_until > $3::timestamptz) OR EXISTS
		              (SELECT 1 FROM openai_persona_request_holds hold WHERE hold.lease_id = lease.id AND hold.expires_at > $3::timestamptz))) AS current_client_lease
FROM openai_account_personas persona
JOIN openai_account_persona_sessions session ON session.account_persona_id = persona.id
    AND session.session_epoch = persona.current_session_epoch AND session.state = 'current'
JOIN openai_account_persona_credentials credential ON credential.account_persona_id = persona.id
    AND credential.credential_chain_id = persona.current_credential_chain_id AND credential.state = 'ready'
LEFT JOIN openai_account_user_persona_claims claim ON claim.account_id = persona.account_id AND claim.user_id = $2
    AND claim.active_until > $3::timestamptz
LEFT JOIN proxies proxy ON proxy.id = session.effective_proxy_id
WHERE persona.account_id = ANY($1::bigint[]) AND persona.state = 'active' AND persona.enabled = TRUE
  AND session.proxy_snapshot_set = TRUE
  AND (session.effective_proxy_id IS NULL OR (proxy.status = 'active' AND proxy.deleted_at IS NULL))
  AND (claim.account_persona_id IS NULL OR claim.account_persona_id = persona.id)
ORDER BY persona.account_id, persona.position, persona.id`, pq.Array(accountIDs), userID, now, userID, apiKeyID, strings.ToLower(clientSessionHash))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.OpenAIPersonaCapacityCandidate, 0)
	for rows.Next() {
		var c service.OpenAIPersonaCapacityCandidate
		var personaProxy, personaLimit, sessionProxy sql.NullInt64
		var personaDrain, personaDisabled, personaRetired, sessionLast, sessionDrain, sessionExpires, earliest sql.NullTime
		if err = rows.Scan(&c.Persona.ID, &c.Persona.AccountID, &c.Persona.Position, &c.Persona.ProfileID, &c.Persona.ProfileVersion,
			&c.Persona.CredentialOwner, &c.Persona.State, &c.Persona.Enabled, &c.Persona.PersonaGeneration,
			&c.Persona.CurrentCredentialChainID, &c.Persona.CurrentSessionEpoch, &c.Persona.DeviceSeed,
			&c.Persona.InstallationID, &personaProxy, &personaLimit, &c.Persona.RowVersion,
			&c.Persona.CreatedAt, &c.Persona.UpdatedAt, &personaDrain, &personaDisabled, &personaRetired,
			&c.Session.AccountPersonaID, &c.Session.SessionEpoch, &c.Session.UpstreamSessionID, &c.Session.State,
			&c.Session.PersonaGeneration, &c.Session.CredentialChainID, &c.Session.ProfileID, &c.Session.ProfileVersion,
			&sessionProxy, &c.Session.ProxyRevision, &c.Session.EffectiveProxyURL, &c.Session.InstallationID,
			&c.Session.ProxySnapshotSet, &c.Session.StartedAt, &sessionLast, &sessionDrain, &sessionExpires,
			&c.ActiveClientSessions, &earliest, &c.ClaimedByUser, &c.CurrentClientLease); err != nil {
			return nil, err
		}
		if personaProxy.Valid {
			v := personaProxy.Int64
			c.Persona.ProxyID = &v
		}
		if personaLimit.Valid {
			v := int(personaLimit.Int64)
			c.Persona.MaxActiveClientSessionsOverride = &v
		}
		if personaDrain.Valid {
			c.Persona.DrainingStartedAt = &personaDrain.Time
		}
		if personaDisabled.Valid {
			c.Persona.DisabledAt = &personaDisabled.Time
		}
		if personaRetired.Valid {
			c.Persona.RetiredAt = &personaRetired.Time
		}
		if sessionProxy.Valid {
			v := sessionProxy.Int64
			c.Session.EffectiveProxyID = &v
		}
		if sessionLast.Valid {
			c.Session.LastActiveAt = &sessionLast.Time
		}
		if sessionDrain.Valid {
			c.Session.DrainingStartedAt = &sessionDrain.Time
		}
		if sessionExpires.Valid {
			c.Session.ExpiresAt = &sessionExpires.Time
		}
		if earliest.Valid {
			c.EarliestReleaseAt = &earliest.Time
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

var _ service.OpenAIClientSessionReservationRepository = (*openAIClientSessionReservationRepository)(nil)

func (r *accountRepository) openAIClientSessionReservationRepository() *openAIClientSessionReservationRepository {
	return &openAIClientSessionReservationRepository{db: r.db}
}

func (r *accountRepository) ReserveUserGroupSession(ctx context.Context, input service.OpenAIUserGroupSessionReserveInput) (*service.OpenAIClientSessionLeaseReservation, error) {
	return r.openAIClientSessionReservationRepository().ReserveUserGroupSession(ctx, input)
}

func (r *accountRepository) ReservePersonaSession(ctx context.Context, input service.OpenAIPersonaSessionReserveInput) (*service.OpenAIClientSessionLeaseReservation, error) {
	return r.openAIClientSessionReservationRepository().ReservePersonaSession(ctx, input)
}

func (r *accountRepository) CommitClientSessionReservation(ctx context.Context, input service.OpenAIClientSessionReservationCommit) (service.OpenAIExecutionTarget, error) {
	return r.openAIClientSessionReservationRepository().CommitClientSessionReservation(ctx, input)
}

func (r *accountRepository) RollbackClientSessionReservation(ctx context.Context, token string, now time.Time) error {
	return r.openAIClientSessionReservationRepository().RollbackClientSessionReservation(ctx, token, now)
}

func (r *accountRepository) ListOpenAIPersonaCapacityCandidates(ctx context.Context, accountIDs []int64, userID, apiKeyID int64, clientSessionHash string, now time.Time) ([]service.OpenAIPersonaCapacityCandidate, error) {
	return r.openAIClientSessionReservationRepository().ListOpenAIPersonaCapacityCandidates(ctx, accountIDs, userID, apiKeyID, clientSessionHash, now)
}

var _ service.OpenAIClientSessionReservationRepository = (*accountRepository)(nil)
