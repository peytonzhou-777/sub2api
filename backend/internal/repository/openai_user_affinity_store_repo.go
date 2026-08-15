package repository

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetOpenAIUserPlacement 读取用户当前居住归属；过期归属按 expired 返回给上层决定是否重装箱。
func (r *accountRepository) GetOpenAIUserPlacement(ctx context.Context, userID int64, scopeKey string) (*service.OpenAIUserPlacement, error) {
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	scopeKey = normalizeOpenAIUserAffinityScopeKey(scopeKey)
	var placement service.OpenAIUserPlacement
	var accountID sql.NullInt64
	var lastActive, lastMovedAccount sql.NullTime
	var resetExclude sql.NullBool
	var resetSource sql.NullInt64
	var predicted5H, predicted7D sql.NullFloat64
	var predictionVersion sql.NullString
	rows, err := r.sql.QueryContext(ctx, `
		SELECT user_id, scope_key, account_id, generation, status, assigned_at,
		       last_active_at, expires_at, last_moved_at, assignment_reason,
		       reset_exclude_source_account, reset_source_account_id,
		       predicted_5h_demand, predicted_7d_demand, prediction_version
		FROM user_account_placements
		WHERE user_id = $1 AND scope_key = $2`, userID, scopeKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	err = rows.Scan(
		&placement.UserID, &placement.ScopeKey, &accountID, &placement.Generation,
		&placement.Status, &placement.AssignedAt, &lastActive, &placement.ExpiresAt,
		&lastMovedAccount, &placement.AssignmentReason, &resetExclude, &resetSource,
		&predicted5H, &predicted7D, &predictionVersion,
	)
	if err != nil {
		return nil, err
	}
	if accountID.Valid {
		placement.AccountID = &accountID.Int64
	}
	if lastActive.Valid {
		placement.LastActiveAt = &lastActive.Time
	}
	if lastMovedAccount.Valid {
		placement.LastMovedAt = &lastMovedAccount.Time
	}
	if resetExclude.Valid {
		placement.ResetExcludeSourceAccount = &resetExclude.Bool
	}
	if resetSource.Valid {
		placement.ResetSourceAccountID = &resetSource.Int64
	}
	if predicted5H.Valid {
		placement.Predicted5HDemand = &predicted5H.Float64
	}
	if predicted7D.Valid {
		placement.Predicted7DDemand = &predicted7D.Float64
	}
	if predictionVersion.Valid {
		placement.PredictionVersion = predictionVersion.String
	}
	return &placement, nil
}

// UpsertOpenAIUserPlacement 原子写入用户归属，generation 由服务层递增并随事件记录。
func (r *accountRepository) UpsertOpenAIUserPlacement(ctx context.Context, placement service.OpenAIUserPlacement) error {
	if r == nil || r.sql == nil {
		return errors.New("openai user affinity storage unavailable")
	}
	if placement.ScopeKey == "" {
		placement.ScopeKey = "openai"
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO user_account_placements
			(user_id, scope_key, account_id, generation, status, assigned_at, last_active_at,
			 expires_at, last_moved_at, assignment_reason, reset_exclude_source_account, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW())
		ON CONFLICT (user_id, scope_key) DO UPDATE SET
			account_id = EXCLUDED.account_id,
			generation = EXCLUDED.generation,
			status = EXCLUDED.status,
			assigned_at = EXCLUDED.assigned_at,
			last_active_at = EXCLUDED.last_active_at,
			expires_at = EXCLUDED.expires_at,
			last_moved_at = EXCLUDED.last_moved_at,
			assignment_reason = EXCLUDED.assignment_reason,
			reset_exclude_source_account = EXCLUDED.reset_exclude_source_account,
			updated_at = NOW()`,
		placement.UserID, placement.ScopeKey, placement.AccountID, placement.Generation,
		placement.Status, placement.AssignedAt, placement.LastActiveAt, placement.ExpiresAt,
		placement.LastMovedAt, placement.AssignmentReason, placement.ResetExcludeSourceAccount,
	)
	return err
}

// RecordOpenAIUserPlacementEvent 保存搬迁/重置事件，供管理员审计和用户反查。
func (r *accountRepository) RecordOpenAIUserPlacementEvent(ctx context.Context, event service.OpenAIUserPlacementEvent) error {
	if r == nil || r.sql == nil {
		return errors.New("openai user affinity storage unavailable")
	}
	if event.ScopeKey == "" {
		event.ScopeKey = "openai"
	}
	_, err := r.sql.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, source_account_id, target_account_id,
			 event_type, reason, config_version, account_affinity_config_version,
			 effective_source, actor_admin_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		event.UserID, event.ScopeKey, event.PlacementGeneration, event.SourceAccountID,
		event.TargetAccountID, event.EventType, event.Reason, event.ConfigVersion,
		event.AccountAffinityConfigVersion, event.EffectiveSource, event.ActorAdminID,
	)
	return err
}
