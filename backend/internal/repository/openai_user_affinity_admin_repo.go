package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// ListOpenAIUserAffinityResidents 从多槽位权威表分页列出账号当前居民及排空状态。
func (r *accountRepository) ListOpenAIUserAffinityResidents(ctx context.Context, accountID int64, limit, offset int) ([]service.OpenAIUserAffinityResident, int64, error) {
	if r == nil || r.sql == nil {
		return nil, 0, errors.New("openai user affinity storage unavailable")
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT s.user_id, COALESCE(u.email, ''), s.account_id, s.scope_key, s.id,
		       s.slot_index, s.generation, s.status, s.admitted_at, s.last_success_at,
		       s.expires_at, s.usage_score,
		       EXISTS (SELECT 1 FROM openai_user_active_routes r
		        WHERE r.user_id = s.user_id AND r.scope_key = s.scope_key
		          AND r.resident_slot_id = s.id AND r.account_id = s.account_id
		          AND r.slot_generation = s.generation AND r.active_until > NOW()),
		       c.touch_expires_at,
		       COUNT(*) OVER()
		FROM openai_user_resident_slots s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN account_user_contacts c ON c.account_id = s.account_id AND c.user_id = s.user_id
		WHERE s.account_id = $1
		  AND (s.scope_key = 'openai' OR s.scope_key LIKE 'openai:v1:%')
		  AND ((s.status IN ('provisional', 'active', 'replacement_pending', 'draining') AND s.expires_at > NOW())
		       OR (s.status = 'reset' AND EXISTS (
		           SELECT 1 FROM openai_user_conversation_bindings b
		           WHERE b.resident_slot_id = s.id AND b.status = 'draining' AND b.expires_at > NOW())))
		ORDER BY (s.status = 'active') DESC, s.last_success_at DESC NULLS LAST, s.admitted_at DESC
		LIMIT $2 OFFSET $3`, accountID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.OpenAIUserAffinityResident, 0, limit)
	var total int64
	for rows.Next() {
		var item service.OpenAIUserAffinityResident
		var lastActive, touchExpires sql.NullTime
		if err := rows.Scan(&item.UserID, &item.UserEmail, &item.AccountID, &item.ScopeKey,
			&item.ResidentSlotID, &item.SlotIndex, &item.Generation, &item.Status,
			&item.AssignedAt, &lastActive, &item.ExpiresAt, &item.UsageScore,
			&item.ActiveRoute, &touchExpires, &total); err != nil {
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
	occupancies, err := r.ListOpenAIAccountSoftOccupancies(ctx, []int64{accountID})
	if err != nil {
		return nil, 0, err
	}
	ownerUserID := occupancies[accountID].OwnerUserID
	for i := range items {
		items[i].SoftOwner = ownerUserID > 0 && items[i].UserID == ownerUserID
	}
	return items, total, nil
}

// GetOpenAIUserAffinityUserDetail 返回用户当前居住账号和最近搬迁/重置记录。
func (r *accountRepository) GetOpenAIUserAffinityUserDetail(ctx context.Context, userID int64, eventLimit int) (*service.OpenAIUserAffinityUserDetail, error) {
	placements, err := r.listOpenAIUserAffinityPlacements(ctx, userID)
	if err != nil {
		return nil, err
	}
	slots, err := r.listOpenAIUserAffinityAdminResidentSlots(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, scope_key, placement_generation, source_account_id, target_account_id,
		       event_type, reason, actor_admin_id, resident_slot_id, created_at
		FROM user_account_placement_events
		WHERE user_id = $1 AND (scope_key = 'openai' OR scope_key LIKE 'openai:v1:%')
		ORDER BY created_at DESC, id DESC LIMIT $2`, userID, eventLimit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	events := make([]service.OpenAIUserAffinityAdminEvent, 0, eventLimit)
	for rows.Next() {
		var event service.OpenAIUserAffinityAdminEvent
		var sourceID, targetID, actorID, residentSlotID sql.NullInt64
		if err := rows.Scan(&event.ID, &event.ScopeKey, &event.PlacementGeneration, &sourceID, &targetID,
			&event.EventType, &event.Reason, &actorID, &residentSlotID, &event.CreatedAt); err != nil {
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
		if residentSlotID.Valid {
			value := residentSlotID.Int64
			event.ResidentSlotID = &value
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	detail := &service.OpenAIUserAffinityUserDetail{Placements: placements, ResidentSlots: slots, Events: events}
	if len(slots) > 0 && slots[0].Status == service.OpenAIUserResidentSlotStatusActive {
		primary := openAIUserResidentSlotPlacement(slots[0])
		detail.Placement = &primary
	} else if len(placements) > 0 {
		placement := placements[0]
		detail.Placement = &placement
	}
	return detail, nil
}

func (r *accountRepository) listOpenAIUserAffinityAdminResidentSlots(ctx context.Context, userID int64) ([]service.OpenAIUserResidentSlot, error) {
	rows, err := r.sql.QueryContext(ctx, `
		WITH affinity_config AS (
			SELECT GREATEST(COALESCE((
				SELECT NULLIF((value::jsonb ->> 'resident_ttl_seconds')::double precision, 0)
				FROM settings WHERE key = 'openai_user_affinity_scheduling'
			), 604800), 1) AS resident_ttl_seconds
		)
		SELECT s.id, s.user_id, s.scope_key, s.slot_index, s.account_id, s.generation, s.status,
		       s.admitted_at, s.last_success_at, s.expires_at, s.usage_score, s.score_updated_at,
		       s.replacement_source_slot_id, s.config_version
		FROM openai_user_resident_slots s CROSS JOIN affinity_config c
		WHERE s.user_id = $1 AND (s.scope_key = 'openai' OR s.scope_key LIKE 'openai:v1:%')
		  AND ((s.status IN ('provisional', 'active', 'replacement_pending', 'draining') AND s.expires_at > NOW())
		       OR (s.status = 'reset' AND EXISTS (
		           SELECT 1 FROM openai_user_conversation_bindings b
		           WHERE b.resident_slot_id = s.id AND b.status = 'draining' AND b.expires_at > NOW())))
		ORDER BY (s.status = 'active') DESC,
		         s.usage_score * POWER(0.5, GREATEST(EXTRACT(EPOCH FROM (NOW() - s.score_updated_at)), 0) / c.resident_ttl_seconds) DESC,
		         s.last_success_at DESC NULLS LAST, s.admitted_at, s.account_id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	slots := make([]service.OpenAIUserResidentSlot, 0)
	for rows.Next() {
		var slot service.OpenAIUserResidentSlot
		var lastSuccess sql.NullTime
		var replacementSource sql.NullInt64
		if err := rows.Scan(&slot.ID, &slot.UserID, &slot.ScopeKey, &slot.SlotIndex, &slot.AccountID,
			&slot.Generation, &slot.Status, &slot.AdmittedAt, &lastSuccess, &slot.ExpiresAt,
			&slot.UsageScore, &slot.ScoreUpdatedAt, &replacementSource, &slot.ConfigVersion); err != nil {
			return nil, err
		}
		if lastSuccess.Valid {
			value := lastSuccess.Time.UTC()
			slot.LastSuccessAt = &value
		}
		if replacementSource.Valid {
			value := replacementSource.Int64
			slot.ReplacementSourceSlotID = &value
		}
		slots = append(slots, slot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accountIDs := make([]int64, 0, len(slots))
	for _, slot := range slots {
		accountIDs = append(accountIDs, slot.AccountID)
	}
	occupancies, err := r.ListOpenAIAccountSoftOccupancies(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	for i := range slots {
		occupancy := occupancies[slots[i].AccountID]
		slots[i].ActiveRouteUserCount = occupancy.ActiveUserCount
		slots[i].SoftOwnerUserID = occupancy.OwnerUserID
	}
	return slots, nil
}

func openAIUserResidentSlotPlacement(slot service.OpenAIUserResidentSlot) service.OpenAIUserPlacement {
	accountID := slot.AccountID
	return service.OpenAIUserPlacement{
		UserID: slot.UserID, ScopeKey: slot.ScopeKey, AccountID: &accountID,
		Generation: slot.Generation, Status: slot.Status, AssignedAt: slot.AdmittedAt,
		LastActiveAt: slot.LastSuccessAt, ExpiresAt: slot.ExpiresAt,
		AssignmentReason: "resident_slot_primary",
	}
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
	defer func() { _ = rows.Close() }()
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

// ResetOpenAIUserAffinityPlacement 原子重置整个 scope；旧会话保留 draining 绑定，新会话重新 BestFit。
func (r *accountRepository) ResetOpenAIUserAffinityPlacement(ctx context.Context, userID, actorAdminID int64, scopeKey string, excludeSource bool) error {
	if r == nil || r.client == nil {
		return errors.New("openai user affinity storage unavailable")
	}
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return r.resetOpenAIUserAffinityAllScopes(ctx, userID, actorAdminID, excludeSource)
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
	lockKey := fmt.Sprintf("%d:%s", userID, scopeKey)
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return err
	}
	var legacyAccountID sql.NullInt64
	var legacyGeneration int64
	legacyErr := scanSingleRow(ctx, exec, `
		SELECT account_id, generation FROM user_account_placements
		WHERE user_id = $1 AND scope_key = $2 FOR UPDATE`, []any{userID, scopeKey}, &legacyAccountID, &legacyGeneration)
	if legacyErr != nil && !errors.Is(legacyErr, sql.ErrNoRows) {
		return legacyErr
	}
	type resetSlot struct {
		id, accountID, generation int64
	}
	rows, err := exec.QueryContext(ctx, `
		SELECT id, account_id, generation FROM openai_user_resident_slots
		WHERE user_id = $1 AND scope_key = $2
		  AND status IN ('provisional', 'active', 'replacement_pending', 'draining')
		ORDER BY account_id, id FOR UPDATE`, userID, scopeKey)
	if err != nil {
		return err
	}
	slots := make([]resetSlot, 0)
	accountIDs := make(map[int64]struct{})
	maxGeneration := legacyGeneration
	for rows.Next() {
		var slot resetSlot
		if err := rows.Scan(&slot.id, &slot.accountID, &slot.generation); err != nil {
			_ = rows.Close()
			return err
		}
		slots = append(slots, slot)
		accountIDs[slot.accountID] = struct{}{}
		if slot.generation > maxGeneration {
			maxGeneration = slot.generation
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if legacyAccountID.Valid {
		accountIDs[legacyAccountID.Int64] = struct{}{}
	}
	pendingRows, err := exec.QueryContext(ctx, `
		SELECT account_id FROM openai_user_affinity_reset_exclusions
		WHERE user_id = $1 AND scope_key = $2 AND consumed_at IS NULL
		ORDER BY account_id FOR UPDATE`, userID, scopeKey)
	if err != nil {
		return err
	}
	for pendingRows.Next() {
		var accountID int64
		if err := pendingRows.Scan(&accountID); err != nil {
			_ = pendingRows.Close()
			return err
		}
		accountIDs[accountID] = struct{}{}
	}
	if err := pendingRows.Close(); err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_affinity_reset_exclusions SET consumed_at = $3
		WHERE user_id = $1 AND scope_key = $2 AND consumed_at IS NULL`, userID, scopeKey, now); err != nil {
		return err
	}
	nextGeneration := maxGeneration + 1
	if nextGeneration < 1 {
		nextGeneration = 1
	}
	var compatibilitySource any
	if legacyAccountID.Valid {
		compatibilitySource = legacyAccountID.Int64
	} else if len(slots) > 0 {
		compatibilitySource = slots[0].accountID
	} else {
		var smallestAccountID int64
		for accountID := range accountIDs {
			if smallestAccountID == 0 || accountID < smallestAccountID {
				smallestAccountID = accountID
			}
		}
		if smallestAccountID > 0 {
			compatibilitySource = smallestAccountID
		}
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placements
			(user_id, scope_key, account_id, generation, status, assigned_at, expires_at,
			 assignment_reason, reset_at, reset_by_admin_id, reset_reason,
			 reset_exclude_source_account, reset_source_account_id, created_at, updated_at)
		VALUES ($1, $2, NULL, $3, 'reset', $4, $4, 'admin_reset', $4, $5, NULL, $6, $7, $4, $4)
		ON CONFLICT (user_id, scope_key) DO UPDATE SET
			account_id = NULL, generation = EXCLUDED.generation, status = 'reset',
			assigned_at = EXCLUDED.assigned_at, last_active_at = NULL, expires_at = EXCLUDED.expires_at,
			assignment_reason = EXCLUDED.assignment_reason, reset_at = EXCLUDED.reset_at,
			reset_by_admin_id = EXCLUDED.reset_by_admin_id, reset_reason = NULL,
			reset_exclude_source_account = EXCLUDED.reset_exclude_source_account,
			reset_source_account_id = EXCLUDED.reset_source_account_id,
			provisional_token = NULL, updated_at = EXCLUDED.updated_at`,
		userID, scopeKey, nextGeneration, now, actorAdminID, excludeSource, compatibilitySource); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_resident_slots SET status = 'reset', provisional_token = NULL, updated_at = $3
		WHERE user_id = $1 AND scope_key = $2
		  AND status IN ('provisional', 'active', 'replacement_pending', 'draining')`, userID, scopeKey, now); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			status = CASE WHEN first_output_committed THEN 'draining' ELSE 'reset' END,
			provisional_token = NULL,
			pending_resident_slot_id = NULL, pending_account_id = NULL, pending_slot_generation = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $3
		WHERE user_id = $1 AND scope_key = $2 AND status IN ('provisional', 'active', 'draining')`, userID, scopeKey, now); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		DELETE FROM openai_user_active_routes
		WHERE user_id = $1 AND scope_key = $2`, userID, scopeKey); err != nil {
		return err
	}
	for accountID := range accountIDs {
		if _, err := exec.ExecContext(ctx, `
			UPDATE account_user_contacts SET reservation_kind = NULL, reservation_token = NULL,
				reservation_until = NULL, updated_at = $3
			WHERE account_id = $1 AND user_id = $2`, accountID, userID, now); err != nil {
			return err
		}
		if excludeSource {
			if _, err := exec.ExecContext(ctx, `
				INSERT INTO openai_user_affinity_reset_exclusions
					(user_id, scope_key, account_id, reset_generation, actor_admin_id, created_at)
				VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (user_id, scope_key, account_id, reset_generation) DO NOTHING`,
				userID, scopeKey, accountID, nextGeneration, actorAdminID, now); err != nil {
				return err
			}
		}
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE user_account_capacity_incidents SET status = 'closed', close_reason = 'manual_reset',
			closed_at = $2, updated_at = $2
		WHERE user_id = $1 AND scope_key = $3 AND closed_at IS NULL`, userID, now, scopeKey); err != nil {
		return err
	}
	if len(slots) == 0 {
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO user_account_placement_events
				(user_id, scope_key, placement_generation, source_account_id, event_type,
				 reason, effective_source, actor_admin_id)
			VALUES ($1, $2, $3, $4, 'admin_reset', 'admin_manual_reset', 'global', $5)`,
			userID, scopeKey, nextGeneration, compatibilitySource, actorAdminID); err != nil {
			return err
		}
	} else {
		for _, slot := range slots {
			if _, err := exec.ExecContext(ctx, `
				INSERT INTO user_account_placement_events
					(user_id, scope_key, placement_generation, source_account_id, event_type,
					 reason, effective_source, actor_admin_id, resident_slot_id)
				VALUES ($1, $2, $3, $4, 'slot_admin_reset', 'admin_manual_reset', 'global', $5, $6)`,
				userID, scopeKey, slot.generation, slot.accountID, actorAdminID, slot.id); err != nil {
				return err
			}
		}
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

// resetOpenAIUserAffinityAllScopes 兼容旧的单 scope 重置实现，覆盖用户已经出现过的所有 affinity scope。
func (r *accountRepository) resetOpenAIUserAffinityAllScopes(ctx context.Context, userID, actorAdminID int64, excludeSource bool) error {
	if r == nil || r.sql == nil || userID <= 0 {
		return errors.New("openai user affinity storage unavailable")
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT scope_key FROM user_account_placements WHERE user_id = $1
		UNION
		SELECT scope_key FROM openai_user_resident_slots WHERE user_id = $1
		UNION
		SELECT scope_key FROM openai_user_conversation_bindings WHERE user_id = $1
		UNION
		SELECT scope_key FROM openai_user_conversation_aliases WHERE user_id = $1
		UNION
		SELECT scope_key FROM openai_user_active_routes WHERE user_id = $1
		UNION
		SELECT scope_key FROM openai_user_affinity_reset_exclusions WHERE user_id = $1
		ORDER BY scope_key`, userID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	scopeKeys := make([]string, 0)
	for rows.Next() {
		var scopeKey string
		if err := rows.Scan(&scopeKey); err != nil {
			return err
		}
		scopeKey = strings.TrimSpace(scopeKey)
		if scopeKey != "" {
			scopeKeys = append(scopeKeys, scopeKey)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, scopeKey := range scopeKeys {
		if err := r.ResetOpenAIUserAffinityPlacement(ctx, userID, actorAdminID, scopeKey, excludeSource); err != nil {
			return err
		}
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
