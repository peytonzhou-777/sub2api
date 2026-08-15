package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListOpenAIUserAffinityResidents 分页列出账号当前 14 天居住期内的用户。
func (r *accountRepository) ListOpenAIUserAffinityResidents(ctx context.Context, accountID int64, limit, offset int) ([]service.OpenAIUserAffinityResident, int64, error) {
	if r == nil || r.sql == nil {
		return nil, 0, errors.New("openai user affinity storage unavailable")
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT p.user_id, COALESCE(u.email, ''), p.account_id, p.scope_key, p.generation,
		       p.assigned_at, p.last_active_at, p.expires_at, c.touch_expires_at,
		       COUNT(*) OVER()
		FROM user_account_placements p
		JOIN users u ON u.id = p.user_id
		LEFT JOIN account_user_contacts c ON c.account_id = p.account_id AND c.user_id = p.user_id
		WHERE p.status = 'active' AND p.account_id = $1
		  AND (p.scope_key = 'openai' OR p.scope_key LIKE 'openai:v1:%')
		  AND p.expires_at > NOW()
		ORDER BY p.last_active_at DESC NULLS LAST, p.assigned_at DESC
		LIMIT $2 OFFSET $3`, accountID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]service.OpenAIUserAffinityResident, 0, limit)
	var total int64
	for rows.Next() {
		var item service.OpenAIUserAffinityResident
		var lastActive, touchExpires sql.NullTime
		if err := rows.Scan(&item.UserID, &item.UserEmail, &item.AccountID, &item.ScopeKey, &item.Generation,
			&item.AssignedAt, &lastActive, &item.ExpiresAt, &touchExpires, &total); err != nil {
			return nil, 0, err
		}
		if lastActive.Valid {
			value := lastActive.Time.UTC()
			item.LastActiveAt = &value
		}
		if touchExpires.Valid {
			value := touchExpires.Time.UTC()
			item.TouchExpiresAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// GetOpenAIUserAffinityUserDetail 返回用户当前居住账号和最近搬迁/重置记录。
func (r *accountRepository) GetOpenAIUserAffinityUserDetail(ctx context.Context, userID int64, eventLimit int) (*service.OpenAIUserAffinityUserDetail, error) {
	placements, err := r.listOpenAIUserAffinityPlacements(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, scope_key, placement_generation, source_account_id, target_account_id,
		       event_type, reason, actor_admin_id, created_at
		FROM user_account_placement_events
		WHERE user_id = $1 AND (scope_key = 'openai' OR scope_key LIKE 'openai:v1:%')
		ORDER BY created_at DESC, id DESC LIMIT $2`, userID, eventLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]service.OpenAIUserAffinityAdminEvent, 0, eventLimit)
	for rows.Next() {
		var event service.OpenAIUserAffinityAdminEvent
		var sourceID, targetID, actorID sql.NullInt64
		if err := rows.Scan(&event.ID, &event.ScopeKey, &event.PlacementGeneration, &sourceID, &targetID,
			&event.EventType, &event.Reason, &actorID, &event.CreatedAt); err != nil {
			return nil, err
		}
		if sourceID.Valid {
			value := sourceID.Int64
			event.SourceAccountID = &value
		}
		if targetID.Valid {
			value := targetID.Int64
			event.TargetAccountID = &value
		}
		if actorID.Valid {
			value := actorID.Int64
			event.ActorAdminID = &value
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	detail := &service.OpenAIUserAffinityUserDetail{Placements: placements, Events: events}
	if len(placements) > 0 {
		placement := placements[0]
		detail.Placement = &placement
	}
	return detail, nil
}

func (r *accountRepository) listOpenAIUserAffinityPlacements(ctx context.Context, userID int64) ([]service.OpenAIUserPlacement, error) {
	rows, err := r.sql.QueryContext(ctx, `
		SELECT user_id, scope_key, account_id, generation, status, assigned_at,
		       last_active_at, expires_at, last_moved_at, assignment_reason,
		       reset_exclude_source_account, reset_source_account_id,
		       predicted_5h_demand, predicted_7d_demand, prediction_version
		FROM user_account_placements
		WHERE user_id = $1 AND (scope_key = 'openai' OR scope_key LIKE 'openai:v1:%')
		ORDER BY (status = 'active' AND expires_at > NOW()) DESC, updated_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	placements := make([]service.OpenAIUserPlacement, 0)
	for rows.Next() {
		var placement service.OpenAIUserPlacement
		var accountID, resetSource sql.NullInt64
		var lastActive, lastMoved sql.NullTime
		var resetExclude sql.NullBool
		var predicted5H, predicted7D sql.NullFloat64
		var predictionVersion sql.NullString
		if err := rows.Scan(&placement.UserID, &placement.ScopeKey, &accountID, &placement.Generation,
			&placement.Status, &placement.AssignedAt, &lastActive, &placement.ExpiresAt, &lastMoved,
			&placement.AssignmentReason, &resetExclude, &resetSource, &predicted5H, &predicted7D,
			&predictionVersion); err != nil {
			return nil, err
		}
		if accountID.Valid {
			value := accountID.Int64
			placement.AccountID = &value
		}
		if lastActive.Valid {
			value := lastActive.Time.UTC()
			placement.LastActiveAt = &value
		}
		if lastMoved.Valid {
			value := lastMoved.Time.UTC()
			placement.LastMovedAt = &value
		}
		if resetExclude.Valid {
			value := resetExclude.Bool
			placement.ResetExcludeSourceAccount = &value
		}
		if resetSource.Valid {
			value := resetSource.Int64
			placement.ResetSourceAccountID = &value
		}
		if predicted5H.Valid {
			value := predicted5H.Float64
			placement.Predicted5HDemand = &value
		}
		if predicted7D.Valid {
			value := predicted7D.Float64
			placement.Predicted7DDemand = &value
		}
		if predictionVersion.Valid {
			placement.PredictionVersion = predictionVersion.String
		}
		placements = append(placements, placement)
	}
	return placements, rows.Err()
}

// ResetOpenAIUserAffinityPlacement 原子清除当前归属，用户下次请求按新居民重新装箱。
func (r *accountRepository) ResetOpenAIUserAffinityPlacement(ctx context.Context, userID, actorAdminID int64, scopeKey, reason string, excludeSource bool) error {
	if r == nil || r.client == nil {
		return errors.New("openai user affinity storage unavailable")
	}
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return errors.New("scope_key is required")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	var sourceAccountID sql.NullInt64
	var generation int64
	if err := scanSingleRow(ctx, exec, `
		SELECT account_id, generation FROM user_account_placements
		WHERE user_id = $1 AND scope_key = $2 FOR UPDATE`, []any{userID, scopeKey}, &sourceAccountID, &generation); err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := exec.ExecContext(ctx, `
		UPDATE user_account_placements SET account_id = NULL, generation = generation + 1,
			status = 'reset', reset_at = $2, reset_by_admin_id = $3, reset_reason = $4,
			reset_exclude_source_account = $5, reset_source_account_id = $6,
			expires_at = $2, updated_at = $2
		WHERE user_id = $1 AND scope_key = $7`, userID, now, actorAdminID, reason, excludeSource, sourceAccountID, scopeKey); err != nil {
		return err
	}
	if sourceAccountID.Valid {
		if _, err := exec.ExecContext(ctx, `
			UPDATE account_user_contacts SET reservation_kind = NULL, reservation_token = NULL,
				reservation_until = NULL, updated_at = $3
			WHERE account_id = $1 AND user_id = $2`, sourceAccountID.Int64, userID, now); err != nil {
			return err
		}
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE user_account_capacity_incidents SET status = 'closed', close_reason = 'manual_reset',
			closed_at = $2, updated_at = $2
		WHERE user_id = $1 AND scope_key = $3 AND closed_at IS NULL`, userID, now, scopeKey); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, source_account_id, event_type,
			 reason, effective_source, actor_admin_id)
		VALUES ($1, $2, $3, $4, 'admin_reset', $5, 'global', $6)`,
		userID, scopeKey, generation+1, sourceAccountID, reason, actorAdminID); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

// GetOpenAIUserAffinityAccountPolicy 读取账号级粘性策略覆盖。
func (r *accountRepository) GetOpenAIUserAffinityAccountPolicy(ctx context.Context, accountID int64) (*service.OpenAIUserAffinityAccountPolicy, error) {
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	policy := &service.OpenAIUserAffinityAccountPolicy{AccountID: accountID}
	var maxUsers, cooldownSeconds, threshold, windowSeconds sql.NullInt64
	var cooldownUntil sql.NullTime
	if err := scanSingleRow(ctx, r.sql, `
		SELECT max_contact_users, new_resident_cooldown_seconds,
		       capacity_failure_migration_threshold, capacity_failure_window_seconds,
		       new_resident_cooldown_until, affinity_config_version
		FROM accounts WHERE id = $1`, []any{accountID}, &maxUsers, &cooldownSeconds,
		&threshold, &windowSeconds, &cooldownUntil, &policy.AffinityConfigVersion); err != nil {
		return nil, err
	}
	if maxUsers.Valid {
		value := int(maxUsers.Int64)
		policy.MaxContactUsers = &value
	}
	if cooldownSeconds.Valid {
		value := int(cooldownSeconds.Int64)
		policy.NewResidentCooldownSeconds = &value
	}
	if threshold.Valid {
		value := int(threshold.Int64)
		policy.CapacityFailureMigrationThreshold = &value
	}
	if windowSeconds.Valid {
		value := int(windowSeconds.Int64)
		policy.CapacityFailureWindowSeconds = &value
	}
	if cooldownUntil.Valid {
		value := cooldownUntil.Time.UTC()
		policy.NewResidentCooldownUntil = &value
	}
	return policy, nil
}

// UpdateOpenAIUserAffinityAccountPolicy 更新账号级覆盖并递增版本。
func (r *accountRepository) UpdateOpenAIUserAffinityAccountPolicy(ctx context.Context, policy service.OpenAIUserAffinityAccountPolicy) error {
	if r == nil || r.client == nil {
		return errors.New("openai user affinity storage unavailable")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	var accountID int64
	if err := scanSingleRow(ctx, exec, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, []any{policy.AccountID}, &accountID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrAccountNotFound
		}
		return err
	}
	if policy.MaxContactUsers != nil {
		var activeContacts int
		if err := scanSingleRow(ctx, exec, `SELECT COUNT(*) FROM account_user_contacts
			WHERE account_id = $1 AND (touch_expires_at > NOW() OR reservation_until > NOW())`,
			[]any{policy.AccountID}, &activeContacts); err != nil {
			return err
		}
		if activeContacts > *policy.MaxContactUsers {
			return service.ErrOpenAIUserAffinityContactLimitConflict
		}
	}
	result, err := exec.ExecContext(ctx, `
		UPDATE accounts SET max_contact_users = $2,
			new_resident_cooldown_seconds = $3,
			capacity_failure_migration_threshold = $4,
			capacity_failure_window_seconds = $5,
			affinity_config_version = affinity_config_version + 1,
			updated_at = NOW()
		WHERE id = $1`, policy.AccountID, policy.MaxContactUsers,
		policy.NewResidentCooldownSeconds, policy.CapacityFailureMigrationThreshold,
		policy.CapacityFailureWindowSeconds)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAccountNotFound
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}
