package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

var _ service.OpenAIUserAffinityActiveRoutingStore = (*accountRepository)(nil)

// GetOpenAIUserActiveRoute 读取用户新会话当前活动账号及尚未提交的切换目标。
func (r *accountRepository) GetOpenAIUserActiveRoute(ctx context.Context, userID int64, scopeKey string) (*service.OpenAIUserActiveRoute, error) {
	if r == nil || r.sql == nil || userID <= 0 {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	var route service.OpenAIUserActiveRoute
	var residentSlotID, accountID, slotGeneration sql.NullInt64
	var claimedAt, activeUntil sql.NullTime
	var pendingResidentSlotID, pendingAccountID, pendingSlotGeneration sql.NullInt64
	var pendingClaimedAt, pendingExpiresAt sql.NullTime
	err := scanSingleRow(ctx, r.sql, `
		SELECT user_id, scope_key, resident_slot_id, account_id, slot_generation,
		       claimed_at, active_until, pending_resident_slot_id, pending_account_id,
		       pending_slot_generation, pending_claimed_at, pending_expires_at
		FROM openai_user_active_routes
		WHERE user_id = $1 AND scope_key = $2`,
		[]any{userID, normalizeOpenAIUserAffinityScopeKey(scopeKey)}, &route.UserID, &route.ScopeKey,
		&residentSlotID, &accountID, &slotGeneration, &claimedAt, &activeUntil,
		&pendingResidentSlotID, &pendingAccountID, &pendingSlotGeneration, &pendingClaimedAt, &pendingExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if residentSlotID.Valid {
		route.ResidentSlotID = residentSlotID.Int64
	}
	if accountID.Valid {
		route.AccountID = accountID.Int64
	}
	if slotGeneration.Valid {
		route.SlotGeneration = slotGeneration.Int64
	}
	if claimedAt.Valid {
		value := claimedAt.Time.UTC()
		route.ClaimedAt = &value
	}
	if activeUntil.Valid {
		value := activeUntil.Time.UTC()
		route.ActiveUntil = &value
	}
	if pendingResidentSlotID.Valid {
		route.PendingResidentSlotID = pendingResidentSlotID.Int64
	}
	if pendingAccountID.Valid {
		route.PendingAccountID = pendingAccountID.Int64
	}
	if pendingSlotGeneration.Valid {
		route.PendingSlotGeneration = pendingSlotGeneration.Int64
	}
	if pendingClaimedAt.Valid {
		value := pendingClaimedAt.Time.UTC()
		route.PendingClaimedAt = &value
	}
	if pendingExpiresAt.Valid {
		value := pendingExpiresAt.Time.UTC()
		route.PendingExpiresAt = &value
	}
	return &route, nil
}

// ListOpenAIAccountSoftOccupancies 以最早有效活动路由作为账号稳定软驻留主用户。
func (r *accountRepository) ListOpenAIAccountSoftOccupancies(ctx context.Context, accountIDs []int64) (map[int64]service.OpenAIAccountSoftOccupancy, error) {
	result := make(map[int64]service.OpenAIAccountSoftOccupancy, len(accountIDs))
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	if len(accountIDs) == 0 {
		return result, nil
	}
	now := time.Now().UTC()
	rows, err := r.sql.QueryContext(ctx, `
		WITH claims AS (
			SELECT r.account_id, r.user_id, r.claimed_at
			FROM openai_user_active_routes r
			JOIN openai_user_resident_slots s ON s.id = r.resident_slot_id
			 AND s.account_id = r.account_id AND s.generation = r.slot_generation
			WHERE r.account_id = ANY($1::bigint[]) AND r.active_until > $2
			  AND s.status = 'active' AND s.expires_at > $2
			UNION ALL
			SELECT r.pending_account_id, r.user_id, r.pending_claimed_at
			FROM openai_user_active_routes r
			JOIN openai_user_resident_slots s ON s.id = r.pending_resident_slot_id
			 AND s.account_id = r.pending_account_id AND s.generation = r.pending_slot_generation
			WHERE r.pending_account_id = ANY($1::bigint[]) AND r.pending_expires_at > $2
			  AND s.status IN ('provisional', 'active') AND s.expires_at > $2
		), deduplicated AS (
			SELECT account_id, user_id, MIN(claimed_at) AS claimed_at
			FROM claims GROUP BY account_id, user_id
		)
		SELECT account_id, COUNT(*), (ARRAY_AGG(user_id ORDER BY claimed_at, user_id))[1]
		FROM deduplicated GROUP BY account_id`, pq.Array(accountIDs), now)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var occupancy service.OpenAIAccountSoftOccupancy
		if err := rows.Scan(&occupancy.AccountID, &occupancy.ActiveUserCount, &occupancy.OwnerUserID); err != nil {
			return nil, err
		}
		result[occupancy.AccountID] = occupancy
	}
	return result, rows.Err()
}

// reserveOpenAIUserActiveRoute 在会话首输出前暂存活动路由切换；已有相同有效路由只需后续续期。
func reserveOpenAIUserActiveRoute(ctx context.Context, exec sqlQueryExecutor, reservation service.OpenAIUserConversationReservation, residentSlotID, slotGeneration int64, now time.Time) (accepted, pending bool, err error) {
	if !reservation.ManageActiveRoute {
		return true, false, nil
	}
	var currentSlotID, currentAccountID, currentGeneration sql.NullInt64
	var activeUntil sql.NullTime
	var pendingToken sql.NullString
	var pendingExpiresAt sql.NullTime
	err = scanSingleRow(ctx, exec, `
		SELECT resident_slot_id, account_id, slot_generation, active_until, pending_token, pending_expires_at
		FROM openai_user_active_routes WHERE user_id = $1 AND scope_key = $2 FOR UPDATE`,
		[]any{reservation.UserID, reservation.ScopeKey}, &currentSlotID, &currentAccountID,
		&currentGeneration, &activeUntil, &pendingToken, &pendingExpiresAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, false, err
	}
	if err == nil && pendingToken.Valid && pendingExpiresAt.Valid && now.Before(pendingExpiresAt.Time) {
		return false, false, nil
	}
	if err == nil && currentSlotID.Valid && currentAccountID.Valid && currentGeneration.Valid && activeUntil.Valid &&
		now.Before(activeUntil.Time) && currentSlotID.Int64 == residentSlotID &&
		currentAccountID.Int64 == reservation.AccountID && currentGeneration.Int64 == slotGeneration {
		_, updateErr := exec.ExecContext(ctx, `
			UPDATE openai_user_active_routes SET
				pending_resident_slot_id = NULL, pending_account_id = NULL,
				pending_slot_generation = NULL, pending_claimed_at = NULL,
				pending_token = NULL, pending_expires_at = NULL, updated_at = $3
			WHERE user_id = $1 AND scope_key = $2`, reservation.UserID, reservation.ScopeKey, now)
		return updateErr == nil, false, updateErr
	}
	pendingExpires := now.Add(openAIUserAffinityProvisionalTTL(reservation.Config))
	_, err = exec.ExecContext(ctx, `
		INSERT INTO openai_user_active_routes
			(user_id, scope_key, pending_resident_slot_id, pending_account_id,
			 pending_slot_generation, pending_claimed_at, pending_token, pending_expires_at,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $6, $6)
		ON CONFLICT (user_id, scope_key) DO UPDATE SET
			pending_resident_slot_id = EXCLUDED.pending_resident_slot_id,
			pending_account_id = EXCLUDED.pending_account_id,
			pending_slot_generation = EXCLUDED.pending_slot_generation,
			pending_claimed_at = EXCLUDED.pending_claimed_at,
			pending_token = EXCLUDED.pending_token,
			pending_expires_at = EXCLUDED.pending_expires_at,
			updated_at = EXCLUDED.updated_at`, reservation.UserID, reservation.ScopeKey,
		residentSlotID, reservation.AccountID, slotGeneration, now, reservation.ProvisionalToken, pendingExpires)
	if err != nil {
		return false, false, err
	}
	return true, true, nil
}

// commitOpenAIUserActiveRoute 提交新会话活动路由；旧会话只允许续期完全匹配的现有路由。
func commitOpenAIUserActiveRoute(ctx context.Context, exec sqlQueryExecutor, transition service.OpenAIUserConversationTransition, now time.Time) (bool, error) {
	activeUntil := now.Add(transition.Config.ConversationActiveTTL())
	if transition.ActiveRoutePending {
		result, err := exec.ExecContext(ctx, `
			UPDATE openai_user_active_routes SET
				resident_slot_id = pending_resident_slot_id,
				account_id = pending_account_id,
				slot_generation = pending_slot_generation,
				claimed_at = pending_claimed_at,
				active_until = $4,
				pending_resident_slot_id = NULL, pending_account_id = NULL,
				pending_slot_generation = NULL, pending_claimed_at = NULL,
				pending_token = NULL, pending_expires_at = NULL, updated_at = $5
			WHERE user_id = $1 AND scope_key = $2 AND pending_token = $3
			  AND pending_account_id = $6 AND pending_resident_slot_id = $7
			  AND pending_slot_generation = $8 AND pending_expires_at > $5`,
			transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), transition.ProvisionalToken,
			activeUntil, now, transition.AccountID, transition.ResidentSlotID, transition.SlotGeneration)
		if err != nil {
			return false, err
		}
		affected, err := result.RowsAffected()
		return affected > 0, err
	}
	result, err := exec.ExecContext(ctx, `
		UPDATE openai_user_active_routes SET active_until = $6, updated_at = $7
		WHERE user_id = $1 AND scope_key = $2 AND resident_slot_id = $3
		  AND account_id = $4 AND slot_generation = $5`, transition.UserID,
		normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), transition.ResidentSlotID,
		transition.AccountID, transition.SlotGeneration, activeUntil, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return !transition.ManageActiveRoute || affected > 0, nil
}

// rollbackOpenAIUserActiveRoute 仅清理 token 匹配的活动路由暂存目标。
func rollbackOpenAIUserActiveRoute(ctx context.Context, exec sqlQueryExecutor, transition service.OpenAIUserConversationTransition, now time.Time) error {
	if !transition.ActiveRoutePending || strings.TrimSpace(transition.ProvisionalToken) == "" {
		return nil
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_active_routes SET
			pending_resident_slot_id = NULL, pending_account_id = NULL,
			pending_slot_generation = NULL, pending_claimed_at = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $4
		WHERE user_id = $1 AND scope_key = $2 AND pending_token = $3`,
		transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), transition.ProvisionalToken, now); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, `
		DELETE FROM openai_user_active_routes
		WHERE user_id = $1 AND scope_key = $2 AND account_id IS NULL AND pending_account_id IS NULL`,
		transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey))
	return err
}

// cleanupOpenAIUserActiveRoute 清理已超时或不再指向有效居民槽位的活动路由。
func cleanupOpenAIUserActiveRoute(ctx context.Context, exec sqlQueryExecutor, userID int64, scopeKey string, now time.Time) error {
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_active_routes r SET
			resident_slot_id = NULL, account_id = NULL, slot_generation = NULL,
			claimed_at = NULL, active_until = NULL, updated_at = $3
		WHERE r.user_id = $1 AND r.scope_key = $2 AND r.account_id IS NOT NULL
		  AND (r.active_until <= $3 OR NOT EXISTS (
			SELECT 1 FROM openai_user_resident_slots s
			WHERE s.id = r.resident_slot_id AND s.account_id = r.account_id
			  AND s.generation = r.slot_generation AND s.status = 'active' AND s.expires_at > $3
		  ))`, userID, normalizeOpenAIUserAffinityScopeKey(scopeKey), now); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_active_routes r SET
			pending_resident_slot_id = NULL, pending_account_id = NULL,
			pending_slot_generation = NULL, pending_claimed_at = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $3
		WHERE r.user_id = $1 AND r.scope_key = $2 AND r.pending_account_id IS NOT NULL
		  AND (r.pending_expires_at <= $3 OR NOT EXISTS (
			SELECT 1 FROM openai_user_resident_slots s
			WHERE s.id = r.pending_resident_slot_id AND s.account_id = r.pending_account_id
			  AND s.generation = r.pending_slot_generation
			  AND s.status IN ('provisional', 'active') AND s.expires_at > $3
		  ))`, userID, normalizeOpenAIUserAffinityScopeKey(scopeKey), now); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, `
		DELETE FROM openai_user_active_routes
		WHERE user_id = $1 AND scope_key = $2 AND account_id IS NULL AND pending_account_id IS NULL`,
		userID, normalizeOpenAIUserAffinityScopeKey(scopeKey))
	return err
}
