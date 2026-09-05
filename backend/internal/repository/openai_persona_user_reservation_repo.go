package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

type openAIPersonaUserReservationRepository struct{ db *sql.DB }

// NewOpenAIPersonaUserReservationRepository 创建 Persona x User 的 PostgreSQL 权威仓储。
func NewOpenAIPersonaUserReservationRepository(db *sql.DB) service.OpenAIPersonaUserReservationRepository {
	return &openAIPersonaUserReservationRepository{db: db}
}

func validPersonaUserReservation(token string, now, holdUntil time.Time) bool {
	_, tokenErr := uuid.Parse(strings.TrimSpace(token))
	return tokenErr == nil && !now.IsZero() && holdUntil.After(now)
}

func (r *openAIPersonaUserReservationRepository) ReservePersonaUser(ctx context.Context, input service.OpenAIPersonaUserReserveInput) (*service.OpenAIPersonaUserLeaseReservation, error) {
	if r == nil || r.db == nil || input.AccountID <= 0 || input.AccountPersonaID <= 0 || input.UserID <= 0 || input.MaxUsers < 1 ||
		!validPersonaUserReservation(input.ReservationToken, input.Now, input.HoldUntil) {
		return nil, errors.New("invalid OpenAI AccountPersona user reservation")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE id = $1 AND account_id = $2 FOR UPDATE`, input.AccountPersonaID, input.AccountID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrOpenAIAccountPersonaNotFound
	}
	if err != nil {
		return nil, err
	}
	if !persona.AcceptsNewRoot() && !(input.ExistingThread && persona.AcceptsExistingThread()) {
		return nil, service.ErrOpenAIPersonaUserCapacity
	}

	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_persona_user_request_holds
WHERE account_persona_id = $1 AND expires_at <= $2::timestamptz`, input.AccountPersonaID, input.Now); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_active_user_leases lease
SET state = 'expired', updated_at = $1::timestamptz
WHERE lease.account_persona_id = $2 AND lease.state = 'active'
  AND lease.active_until <= $1::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM openai_persona_user_request_holds hold
      WHERE hold.lease_id = lease.id AND hold.expires_at > $1::timestamptz
  )`, input.Now, input.AccountPersonaID); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_active_user_leases lease
SET state = 'expired', active_until = $2::timestamptz, updated_at = $2::timestamptz
WHERE lease.account_persona_id = $1 AND lease.state = 'provisional'
  AND NOT EXISTS (
      SELECT 1 FROM openai_persona_user_request_holds hold
      WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz
  )`, input.AccountPersonaID, input.Now); err != nil {
		return nil, err
	}

	var leaseID int64
	var state string
	err = tx.QueryRowContext(ctx, `SELECT id, state FROM openai_persona_active_user_leases
WHERE account_persona_id = $1 AND user_id = $2 FOR UPDATE`, input.AccountPersonaID, input.UserID).Scan(&leaseID, &state)
	created := false
	alreadyActive := err == nil && state == "active"
	if errors.Is(err, sql.ErrNoRows) {
		if err = ensureOpenAIPersonaUserCapacity(ctx, tx, input.AccountPersonaID, input.MaxUsers, input.Now); err != nil {
			return nil, err
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO openai_persona_active_user_leases
    (account_persona_id, account_id, user_id, state, generation, active_until, created_at, updated_at)
VALUES ($1, $2, $3, 'provisional', 1, NULL::timestamptz, $4::timestamptz, $4::timestamptz)
RETURNING id`, input.AccountPersonaID, input.AccountID, input.UserID, input.Now).Scan(&leaseID)
		created = true
	} else if err != nil {
		return nil, err
	} else if state == "expired" || state == "revoked" {
		if err = ensureOpenAIPersonaUserCapacity(ctx, tx, input.AccountPersonaID, input.MaxUsers, input.Now); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_active_user_leases
SET state = 'provisional', generation = generation + 1,
    last_active_at = NULL::timestamptz, active_until = NULL::timestamptz,
    updated_at = $1::timestamptz
WHERE id = $2`, input.Now, leaseID); err != nil {
			return nil, err
		}
		created = true
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO openai_persona_user_request_holds
    (reservation_token, lease_id, account_persona_id, expires_at, created_at)
VALUES ($1::uuid, $2, $3, $4::timestamptz, $5::timestamptz)`, input.ReservationToken,
		leaseID, input.AccountPersonaID, input.HoldUntil, input.Now); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &service.OpenAIPersonaUserLeaseReservation{
		ReservationToken: input.ReservationToken, LeaseID: leaseID,
		Created: created, AlreadyActive: alreadyActive,
	}, nil
}

func ensureOpenAIPersonaUserCapacity(ctx context.Context, tx *sql.Tx, personaID int64, maxUsers int, now time.Time) error {
	var occupied int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_persona_active_user_leases lease
WHERE lease.account_persona_id = $1
  AND ((lease.state = 'active' AND lease.active_until > $2::timestamptz)
       OR EXISTS (
           SELECT 1 FROM openai_persona_user_request_holds hold
           WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz
       ))`, personaID, now).Scan(&occupied); err != nil {
		return err
	}
	if occupied >= maxUsers {
		return service.ErrOpenAIPersonaUserCapacity
	}
	return nil
}

func (r *openAIPersonaUserReservationRepository) CommitPersonaUserReservation(ctx context.Context, input service.OpenAIPersonaUserReservationCommit) (service.OpenAIExecutionTarget, error) {
	if r == nil || r.db == nil || input.ActiveUntil.Before(input.Now) {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIPersonaUserReservation
	}
	if _, err := uuid.Parse(strings.TrimSpace(input.ReservationToken)); err != nil {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIPersonaUserReservation
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var accountID, personaID, leaseID int64
	if err = tx.QueryRowContext(ctx, `SELECT lease.account_id, lease.account_persona_id, lease.id
FROM openai_persona_user_request_holds hold
JOIN openai_persona_active_user_leases lease ON lease.id = hold.lease_id
WHERE hold.reservation_token = $1::uuid AND hold.expires_at > $2::timestamptz`,
		input.ReservationToken, input.Now).Scan(&accountID, &personaID, &leaseID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.OpenAIExecutionTarget{}, service.ErrOpenAIPersonaUserReservation
		}
		return service.OpenAIExecutionTarget{}, err
	}
	persona, err := scanOpenAIAccountPersona(tx.QueryRowContext(ctx, openAIAccountPersonaSelect+`
WHERE id = $1 AND account_id = $2 FOR UPDATE`, personaID, accountID))
	if err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	var checkedLeaseID int64
	if err = tx.QueryRowContext(ctx, `SELECT lease.id
FROM openai_persona_user_request_holds hold
JOIN openai_persona_active_user_leases lease ON lease.id = hold.lease_id
WHERE hold.reservation_token = $1::uuid AND hold.expires_at > $2::timestamptz
FOR UPDATE OF hold, lease`, input.ReservationToken, input.Now).Scan(&checkedLeaseID); err != nil || checkedLeaseID != leaseID {
		return service.OpenAIExecutionTarget{}, service.ErrOpenAIPersonaUserReservation
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_active_user_leases
SET state = 'active', last_active_at = $1::timestamptz,
    active_until = GREATEST(COALESCE(active_until, $2::timestamptz), $2::timestamptz),
    updated_at = $1::timestamptz
WHERE id = $3`, input.Now, input.ActiveUntil, leaseID); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_persona_user_request_holds
WHERE reservation_token = $1::uuid`, input.ReservationToken); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	session, err := scanOpenAIAccountPersonaSession(tx.QueryRowContext(ctx, openAIAccountPersonaSessionSelect+`
WHERE account_persona_id = $1 AND session_epoch = $2 AND state = 'current'`, personaID, persona.CurrentSessionEpoch))
	if err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	target, err := service.OpenAIExecutionTargetFromPersonaSession(*persona, *session)
	if err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	target.PersonaUserLeaseID = leaseID
	target.ReservationToken = input.ReservationToken
	if err = tx.Commit(); err != nil {
		return service.OpenAIExecutionTarget{}, err
	}
	return target, nil
}

func (r *openAIPersonaUserReservationRepository) RollbackPersonaUserReservation(ctx context.Context, token string, now time.Time) error {
	if r == nil || r.db == nil {
		return service.ErrOpenAIPersonaUserReservation
	}
	if _, err := uuid.Parse(strings.TrimSpace(token)); err != nil {
		return service.ErrOpenAIPersonaUserReservation
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var accountID, personaID, leaseID int64
	err = tx.QueryRowContext(ctx, `SELECT lease.account_id, lease.account_persona_id, lease.id
FROM openai_persona_user_request_holds hold
JOIN openai_persona_active_user_leases lease ON lease.id = hold.lease_id
WHERE hold.reservation_token = $1::uuid`, token).Scan(&accountID, &personaID, &leaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	var lockedPersonaID int64
	if err = tx.QueryRowContext(ctx, `SELECT id FROM openai_account_personas
WHERE id = $1 AND account_id = $2 FOR UPDATE`, personaID, accountID).Scan(&lockedPersonaID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM openai_persona_user_request_holds
WHERE reservation_token = $1::uuid`, token); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE openai_persona_active_user_leases lease
SET state = 'expired', active_until = $2::timestamptz, updated_at = $2::timestamptz
WHERE id = $1 AND state = 'provisional'
  AND NOT EXISTS (
      SELECT 1 FROM openai_persona_user_request_holds hold
      WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz
  )`, leaseID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *openAIPersonaUserReservationRepository) ListOpenAIPersonaCapacityCandidates(ctx context.Context, accountIDs []int64, userID int64, now time.Time) ([]service.OpenAIPersonaCapacityCandidate, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("OpenAI Persona capacity repository unavailable")
	}
	if len(accountIDs) == 0 {
		return []service.OpenAIPersonaCapacityCandidate{}, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT persona.id, persona.account_id, persona.position, persona.profile_id, persona.profile_version,
       persona.credential_owner, persona.state, persona.enabled, persona.persona_generation,
       COALESCE(persona.current_credential_chain_id, ''), persona.current_session_epoch, persona.device_seed,
       persona.installation_id, persona.proxy_id, persona.max_active_users_override, persona.row_version,
       persona.created_at, persona.updated_at, persona.draining_started_at, persona.disabled_at, persona.retired_at,
       session.account_persona_id, session.session_epoch, session.upstream_session_id, session.state,
       session.persona_generation, session.credential_chain_id, session.profile_id, session.profile_version,
       session.effective_proxy_id, session.proxy_revision, session.effective_proxy_url, session.installation_id,
       session.proxy_snapshot_set, session.started_at, session.last_active_at, session.draining_started_at, session.expires_at,
       (SELECT COUNT(*) FROM openai_persona_active_user_leases lease
        WHERE lease.account_persona_id = persona.id
          AND ((lease.state = 'active' AND lease.active_until > $2::timestamptz)
               OR EXISTS (SELECT 1 FROM openai_persona_user_request_holds hold
                          WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz))) AS active_users,
       (SELECT MIN(lease.active_until) FROM openai_persona_active_user_leases lease
        WHERE lease.account_persona_id = persona.id AND lease.state = 'active'
          AND lease.active_until > $2::timestamptz) AS earliest_release,
       EXISTS (SELECT 1 FROM openai_persona_active_user_leases lease
               WHERE lease.account_persona_id = persona.id AND lease.user_id = $3
                 AND ((lease.state = 'active' AND lease.active_until > $2::timestamptz)
                      OR EXISTS (SELECT 1 FROM openai_persona_user_request_holds hold
                                 WHERE hold.lease_id = lease.id AND hold.expires_at > $2::timestamptz))) AS user_already_active
FROM openai_account_personas persona
JOIN openai_account_persona_sessions session ON session.account_persona_id = persona.id
    AND session.session_epoch = persona.current_session_epoch AND session.state = 'current'
JOIN openai_account_persona_credentials credential ON credential.account_persona_id = persona.id
    AND credential.credential_chain_id = persona.current_credential_chain_id AND credential.state = 'ready'
LEFT JOIN proxies proxy ON proxy.id = session.effective_proxy_id
WHERE persona.account_id = ANY($1::bigint[]) AND persona.state = 'active' AND persona.enabled = TRUE
  AND session.proxy_snapshot_set = TRUE
  AND (session.effective_proxy_id IS NULL OR (proxy.status = 'active' AND proxy.deleted_at IS NULL))
ORDER BY persona.account_id, persona.position, persona.id`, pq.Array(accountIDs), now, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.OpenAIPersonaCapacityCandidate, 0)
	for rows.Next() {
		var candidate service.OpenAIPersonaCapacityCandidate
		var personaProxy, personaLimit, sessionProxy sql.NullInt64
		var personaDrain, personaDisabled, personaRetired, sessionLast, sessionDrain, sessionExpires, earliest sql.NullTime
		if err = rows.Scan(&candidate.Persona.ID, &candidate.Persona.AccountID, &candidate.Persona.Position, &candidate.Persona.ProfileID, &candidate.Persona.ProfileVersion,
			&candidate.Persona.CredentialOwner, &candidate.Persona.State, &candidate.Persona.Enabled, &candidate.Persona.PersonaGeneration,
			&candidate.Persona.CurrentCredentialChainID, &candidate.Persona.CurrentSessionEpoch, &candidate.Persona.DeviceSeed,
			&candidate.Persona.InstallationID, &personaProxy, &personaLimit, &candidate.Persona.RowVersion,
			&candidate.Persona.CreatedAt, &candidate.Persona.UpdatedAt, &personaDrain, &personaDisabled, &personaRetired,
			&candidate.Session.AccountPersonaID, &candidate.Session.SessionEpoch, &candidate.Session.UpstreamSessionID, &candidate.Session.State,
			&candidate.Session.PersonaGeneration, &candidate.Session.CredentialChainID, &candidate.Session.ProfileID, &candidate.Session.ProfileVersion,
			&sessionProxy, &candidate.Session.ProxyRevision, &candidate.Session.EffectiveProxyURL, &candidate.Session.InstallationID,
			&candidate.Session.ProxySnapshotSet, &candidate.Session.StartedAt, &sessionLast, &sessionDrain, &sessionExpires,
			&candidate.ActiveUsers, &earliest, &candidate.UserAlreadyActive); err != nil {
			return nil, err
		}
		if personaProxy.Valid {
			value := personaProxy.Int64
			candidate.Persona.ProxyID = &value
		}
		if personaLimit.Valid {
			value := int(personaLimit.Int64)
			candidate.Persona.MaxActiveUsersOverride = &value
		}
		if personaDrain.Valid {
			candidate.Persona.DrainingStartedAt = &personaDrain.Time
		}
		if personaDisabled.Valid {
			candidate.Persona.DisabledAt = &personaDisabled.Time
		}
		if personaRetired.Valid {
			candidate.Persona.RetiredAt = &personaRetired.Time
		}
		if sessionProxy.Valid {
			value := sessionProxy.Int64
			candidate.Session.EffectiveProxyID = &value
		}
		if sessionLast.Valid {
			candidate.Session.LastActiveAt = &sessionLast.Time
		}
		if sessionDrain.Valid {
			candidate.Session.DrainingStartedAt = &sessionDrain.Time
		}
		if sessionExpires.Valid {
			candidate.Session.ExpiresAt = &sessionExpires.Time
		}
		if earliest.Valid {
			candidate.EarliestReleaseAt = &earliest.Time
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

var _ service.OpenAIPersonaUserReservationRepository = (*openAIPersonaUserReservationRepository)(nil)

func (r *accountRepository) openAIPersonaUserReservationRepository() *openAIPersonaUserReservationRepository {
	return &openAIPersonaUserReservationRepository{db: r.db}
}

func (r *accountRepository) ReservePersonaUser(ctx context.Context, input service.OpenAIPersonaUserReserveInput) (*service.OpenAIPersonaUserLeaseReservation, error) {
	return r.openAIPersonaUserReservationRepository().ReservePersonaUser(ctx, input)
}

func (r *accountRepository) CommitPersonaUserReservation(ctx context.Context, input service.OpenAIPersonaUserReservationCommit) (service.OpenAIExecutionTarget, error) {
	return r.openAIPersonaUserReservationRepository().CommitPersonaUserReservation(ctx, input)
}

func (r *accountRepository) RollbackPersonaUserReservation(ctx context.Context, token string, now time.Time) error {
	return r.openAIPersonaUserReservationRepository().RollbackPersonaUserReservation(ctx, token, now)
}

func (r *accountRepository) ListOpenAIPersonaCapacityCandidates(ctx context.Context, accountIDs []int64, userID int64, now time.Time) ([]service.OpenAIPersonaCapacityCandidate, error) {
	return r.openAIPersonaUserReservationRepository().ListOpenAIPersonaCapacityCandidates(ctx, accountIDs, userID, now)
}

var _ service.OpenAIPersonaUserReservationRepository = (*accountRepository)(nil)
