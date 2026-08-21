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

func normalizeOpenAIUserAffinityScopeKey(scopeKey string) string {
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return "openai"
	}
	return scopeKey
}

// GetOpenAIUserAffinityCandidateStats 返回账号当前有效触达数、账号级上限和新居民冷却状态。
func (r *accountRepository) GetOpenAIUserAffinityCandidateStats(ctx context.Context, userID int64, accountIDs []int64) (map[int64]service.OpenAIUserAffinityCandidate, error) {
	stats := make(map[int64]service.OpenAIUserAffinityCandidate, len(accountIDs))
	if len(accountIDs) == 0 {
		return stats, nil
	}
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}

	args := make([]any, 0, len(accountIDs))
	placeholders := make([]string, 0, len(accountIDs))
	for i, accountID := range accountIDs {
		args = append(args, accountID)
		placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
	}
	userPlaceholder := fmt.Sprintf("$%d", len(args)+1)
	args = append(args, userID)
	rows, err := r.sql.QueryContext(ctx, fmt.Sprintf(`
		SELECT a.id, a.max_contact_users, a.new_resident_cooldown_seconds,
		       a.new_resident_cooldown_until,
		       COUNT(c.user_id) FILTER (
		           WHERE c.touch_expires_at > NOW()
		              OR (c.reservation_until IS NOT NULL AND c.reservation_until > NOW())
		       ) AS active_contact_users,
		       COALESCE(BOOL_OR(c.user_id = %s AND (
		           c.touch_expires_at > NOW() OR c.reservation_until > NOW()
		       )), FALSE) AS user_already_active,
		       EXISTS (
		           SELECT 1 FROM user_account_placements p
		           WHERE p.user_id = %s AND p.account_id = a.id
		             AND p.status = 'active' AND p.expires_at > NOW()
		       ) AS user_already_resident
		FROM accounts a
		LEFT JOIN account_user_contacts c ON c.account_id = a.id
		WHERE a.id IN (%s)
		GROUP BY a.id, a.max_contact_users, a.new_resident_cooldown_seconds,
		         a.new_resident_cooldown_until`, userPlaceholder, userPlaceholder, strings.Join(placeholders, ", ")), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var candidate service.OpenAIUserAffinityCandidate
		var maxContactUsers, cooldownSeconds sql.NullInt64
		var cooldownUntil sql.NullTime
		if err := rows.Scan(&candidate.AccountID, &maxContactUsers, &cooldownSeconds, &cooldownUntil, &candidate.ActiveContactUsers, &candidate.UserAlreadyActive, &candidate.UserAlreadyResident); err != nil {
			return nil, err
		}
		if maxContactUsers.Valid {
			candidate.MaxContactUsers = int(maxContactUsers.Int64)
		}
		if cooldownSeconds.Valid {
			candidate.NewResidentCooldownSeconds = int(cooldownSeconds.Int64)
		}
		if cooldownUntil.Valid {
			until := cooldownUntil.Time.UTC()
			candidate.CooldownUntil = &until
		}
		stats[candidate.AccountID] = candidate
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stats, nil
}

// AssignOpenAIUserAffinityPlacement 在账号行锁内完成容量校验、归属、触达预留和长冷却。
func (r *accountRepository) AssignOpenAIUserAffinityPlacement(ctx context.Context, placement service.OpenAIUserPlacement, config service.OpenAIUserAffinityConfig) (bool, error) {
	if r == nil || r.client == nil || placement.AccountID == nil {
		return false, errors.New("openai user affinity storage unavailable")
	}
	if strings.TrimSpace(placement.ProvisionalToken) == "" {
		return false, errors.New("openai user affinity placement requires provisional token")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	var maxContactUsers, cooldownSeconds sql.NullInt64
	var cooldownUntil sql.NullTime
	var accountStatus, accountPlatform string
	var accountSchedulable, userAlreadyResident bool
	if err := scanSingleRow(ctx, exec, `
		SELECT max_contact_users, new_resident_cooldown_seconds, new_resident_cooldown_until,
		       status, schedulable, platform,
		       EXISTS (SELECT 1 FROM user_account_placements p
		               WHERE p.user_id = $2 AND p.account_id = accounts.id
		                 AND p.status = 'active' AND p.expires_at > $3)
		FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`,
		[]any{*placement.AccountID, placement.UserID, now}, &maxContactUsers, &cooldownSeconds,
		&cooldownUntil, &accountStatus, &accountSchedulable, &accountPlatform, &userAlreadyResident); err != nil {
		return false, err
	}
	if accountStatus != service.StatusActive || !accountSchedulable || accountPlatform != service.PlatformOpenAI {
		return false, nil
	}
	effectiveMax := config.DefaultMaxContactUsers
	if maxContactUsers.Valid && maxContactUsers.Int64 > 0 {
		effectiveMax = int(maxContactUsers.Int64)
	}
	var activeContacts int
	var userAlreadyActive bool
	if err := scanSingleRow(ctx, exec, `
		SELECT COUNT(*), COALESCE(BOOL_OR(user_id = $3), FALSE) FROM account_user_contacts
		WHERE account_id = $1
		  AND (touch_expires_at > $2 OR reservation_until > $2)`, []any{*placement.AccountID, now, placement.UserID}, &activeContacts, &userAlreadyActive); err != nil {
		return false, err
	}
	if activeContacts >= effectiveMax && !userAlreadyActive && !userAlreadyResident {
		return false, nil
	}
	if cooldownUntil.Valid && now.Before(cooldownUntil.Time) && !userAlreadyActive && !userAlreadyResident {
		return false, nil
	}

	var currentAccount sql.NullInt64
	var currentStatus string
	var currentExpires time.Time
	currentErr := scanSingleRow(ctx, exec, `
		SELECT account_id, status, expires_at FROM user_account_placements
		WHERE user_id = $1 AND scope_key = $2 FOR UPDATE`, []any{placement.UserID, placement.ScopeKey}, &currentAccount, &currentStatus, &currentExpires)
	if currentErr != nil && !errors.Is(currentErr, sql.ErrNoRows) {
		return false, currentErr
	}
	if currentErr == nil && currentStatus == "active" && currentAccount.Valid && now.Before(currentExpires) {
		return currentAccount.Int64 == *placement.AccountID, nil
	}

	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placements
			(user_id, scope_key, account_id, generation, status, assigned_at, expires_at,
			 assignment_reason, predicted_5h_demand, predicted_7d_demand, prediction_version,
			 provisional_token, updated_at)
		VALUES ($1, $2, $3, $4, 'active', $5, $6, $7, $8, $9, $10, $11, $5)
		ON CONFLICT (user_id, scope_key) DO UPDATE SET
			account_id = EXCLUDED.account_id, generation = EXCLUDED.generation,
			status = 'active', assigned_at = EXCLUDED.assigned_at,
			last_active_at = NULL, expires_at = EXCLUDED.expires_at,
			last_moved_at = CASE WHEN user_account_placements.account_id IS DISTINCT FROM EXCLUDED.account_id THEN EXCLUDED.assigned_at ELSE user_account_placements.last_moved_at END,
			assignment_reason = EXCLUDED.assignment_reason,
			predicted_5h_demand = EXCLUDED.predicted_5h_demand,
			predicted_7d_demand = EXCLUDED.predicted_7d_demand,
			prediction_version = EXCLUDED.prediction_version,
			provisional_token = EXCLUDED.provisional_token,
			reset_at = NULL, reset_by_admin_id = NULL, reset_reason = NULL,
			reset_source_account_id = NULL, updated_at = EXCLUDED.updated_at`,
		placement.UserID, placement.ScopeKey, placement.AccountID, placement.Generation,
		now, placement.ExpiresAt, placement.AssignmentReason, placement.Predicted5HDemand,
		placement.Predicted7DDemand, placement.PredictionVersion, placement.ProvisionalToken); err != nil {
		return false, err
	}
	reservationUntil := now.Add(2 * time.Minute)
	reservationKind := "new_resident"
	if userAlreadyActive || userAlreadyResident {
		reservationKind = "resident_scope"
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO account_user_contacts
			(account_id, user_id, reservation_kind, reservation_token, reservation_until, reservation_generation,
			 reentry_config_version, follower_jitter_min_ms, follower_jitter_max_ms, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (account_id, user_id) DO UPDATE SET
			reservation_kind = EXCLUDED.reservation_kind,
			reservation_token = EXCLUDED.reservation_token,
			reservation_until = EXCLUDED.reservation_until,
			reservation_generation = EXCLUDED.reservation_generation,
			reentry_config_version = EXCLUDED.reentry_config_version,
			follower_jitter_min_ms = EXCLUDED.follower_jitter_min_ms,
			follower_jitter_max_ms = EXCLUDED.follower_jitter_max_ms,
			updated_at = EXCLUDED.updated_at`, *placement.AccountID, placement.UserID,
		reservationKind, placement.ProvisionalToken, reservationUntil, placement.Generation, config.ConfigVersion,
		config.FollowerJitterMinMS, config.FollowerJitterMaxMS, now); err != nil {
		return false, err
	}
	effectiveCooldown := config.DefaultNewResidentCooldownSeconds
	if cooldownSeconds.Valid && cooldownSeconds.Int64 > 0 {
		effectiveCooldown = int(cooldownSeconds.Int64)
	}
	if !userAlreadyActive && !userAlreadyResident {
		if _, err := exec.ExecContext(ctx, `
			UPDATE accounts SET new_resident_cooldown_until = $2,
				affinity_config_version = $3, updated_at = $1 WHERE id = $4`,
			now, now.Add(time.Duration(effectiveCooldown)*time.Second), config.ConfigVersion, *placement.AccountID); err != nil {
			return false, err
		}
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, target_account_id, event_type,
			 reason, config_version, effective_source)
		VALUES ($1, $2, $3, $4, 'assigned', $5, $6, 'global')`,
		placement.UserID, placement.ScopeKey, placement.Generation, placement.AccountID,
		placement.AssignmentReason, config.ConfigVersion); err != nil {
		return false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}

// TouchOpenAIUserAffinity 在上游接受请求后刷新 7 天触达 TTL 和 14 天居住 TTL。
func (r *accountRepository) TouchOpenAIUserAffinity(ctx context.Context, userID, accountID, generation int64, scopeKey string, config service.OpenAIUserAffinityConfig) error {
	if r == nil || r.client == nil || userID <= 0 || accountID <= 0 {
		return errors.New("openai user affinity storage unavailable")
	}
	scopeKey = normalizeOpenAIUserAffinityScopeKey(scopeKey)
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	touchExpiresAt := now.Add(7 * 24 * time.Hour)
	residenceExpiresAt := now.Add(14 * 24 * time.Hour)
	placementMatches := false
	if generation > 0 {
		var placementAccount sql.NullInt64
		var placementGeneration int64
		var placementStatus string
		placementErr := scanSingleRow(ctx, exec, `
			SELECT account_id, generation, status FROM user_account_placements
			WHERE user_id = $1 AND scope_key = $2 FOR UPDATE`, []any{userID, scopeKey},
			&placementAccount, &placementGeneration, &placementStatus)
		if placementErr != nil && !errors.Is(placementErr, sql.ErrNoRows) {
			return placementErr
		}
		placementMatches = placementErr == nil && placementAccount.Valid && placementAccount.Int64 == accountID &&
			placementGeneration == generation && placementStatus == "active"
	}
	var activePeriodID sql.NullInt64
	var previousTouchExpiry sql.NullTime
	contactErr := scanSingleRow(ctx, exec, `
		SELECT active_period_id, touch_expires_at FROM account_user_contacts
		WHERE account_id = $1 AND user_id = $2 FOR UPDATE`, []any{accountID, userID}, &activePeriodID, &previousTouchExpiry)
	if contactErr != nil && !errors.Is(contactErr, sql.ErrNoRows) {
		return contactErr
	}
	if contactErr != nil || !previousTouchExpiry.Valid || !now.Before(previousTouchExpiry.Time) || !activePeriodID.Valid {
		activationKind := "new_resident"
		if contactErr == nil {
			activationKind = "resident_reentry"
		}
		if err := scanSingleRow(ctx, exec, `
			INSERT INTO account_user_contact_periods
				(account_id, user_id, period_started_at, first_touched_at, last_touched_at,
				 touch_expires_at, activation_kind, touch_success_mode, config_version, request_count)
			VALUES ($1, $2, $3, $3, $3, $4, $5, $6, $7, 1)
			RETURNING id`, []any{accountID, userID, now, touchExpiresAt, activationKind, config.TouchSuccessMode, config.ConfigVersion}, &activePeriodID); err != nil {
			return err
		}
	} else if _, err := exec.ExecContext(ctx, `
		UPDATE account_user_contact_periods SET last_touched_at = $2, touch_expires_at = $3,
			request_count = request_count + 1, updated_at = $2 WHERE id = $1`,
		activePeriodID.Int64, now, touchExpiresAt); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO account_user_contacts
			(account_id, user_id, last_touched_at, touch_expires_at, active_period_id,
			 account_affinity_config_version, follower_jitter_min_ms, follower_jitter_max_ms, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $3)
		ON CONFLICT (account_id, user_id) DO UPDATE SET
			last_touched_at = EXCLUDED.last_touched_at,
			touch_expires_at = EXCLUDED.touch_expires_at,
			reservation_kind = NULL, reservation_token = NULL, reservation_until = NULL,
			active_period_id = EXCLUDED.active_period_id,
			account_affinity_config_version = EXCLUDED.account_affinity_config_version,
			follower_jitter_min_ms = EXCLUDED.follower_jitter_min_ms,
			follower_jitter_max_ms = EXCLUDED.follower_jitter_max_ms,
			updated_at = EXCLUDED.updated_at`, accountID, userID, now, touchExpiresAt,
		activePeriodID.Int64, config.ConfigVersion, config.FollowerJitterMinMS, config.FollowerJitterMaxMS); err != nil {
		return err
	}
	if placementMatches {
		if _, err := exec.ExecContext(ctx, `
			UPDATE user_account_placements SET last_active_at = $4, expires_at = $5,
				provisional_token = NULL, updated_at = $4
			WHERE user_id = $1 AND scope_key = $2 AND account_id = $3 AND generation = $6 AND status = 'active'`,
			userID, scopeKey, accountID, now, residenceExpiresAt, generation); err != nil {
			return err
		}
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

// ConfirmOpenAIUserAffinitySuccess 只在真实成功终态关闭当前归属的容量事故。
func (r *accountRepository) ConfirmOpenAIUserAffinitySuccess(ctx context.Context, userID, accountID, generation int64, scopeKey string) error {
	if r == nil || r.sql == nil || userID <= 0 || accountID <= 0 || generation <= 0 {
		return errors.New("openai user affinity storage unavailable")
	}
	now := time.Now().UTC()
	_, err := r.sql.ExecContext(ctx, `
		UPDATE user_account_capacity_incidents SET status = 'closed', close_reason = 'resident_recovered',
			closed_at = $5, updated_at = $5
		WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3
		  AND placement_generation = $4 AND closed_at IS NULL`,
		userID, normalizeOpenAIUserAffinityScopeKey(scopeKey), accountID, generation, now)
	return err
}

// RollbackOpenAIUserAffinityPlacement 按请求 token 恢复失败前归属，避免撤销并发请求已提交的状态。
func (r *accountRepository) RollbackOpenAIUserAffinityPlacement(ctx context.Context, transition service.OpenAIUserAffinityProvisionalTransition, config service.OpenAIUserAffinityConfig) (bool, error) {
	if r == nil || r.client == nil || transition.Token == "" || transition.TargetPlacement.AccountID == nil {
		return false, nil
	}
	target := transition.TargetPlacement
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	var result sql.Result
	if previous := transition.PreviousPlacement; previous != nil {
		result, err = exec.ExecContext(ctx, `
			UPDATE user_account_placements SET account_id = $5, generation = $6, status = $7,
				assigned_at = $8, last_active_at = $9, expires_at = $10, last_moved_at = $11,
				assignment_reason = $12, reset_exclude_source_account = $13,
				reset_source_account_id = $14, reset_at = $15, reset_by_admin_id = $16,
				reset_reason = $17, predicted_5h_demand = $18, predicted_7d_demand = $19,
				prediction_version = $20, provisional_token = $21, updated_at = $4
			WHERE user_id = $1 AND scope_key = $2 AND account_id = $3 AND generation = $22
			  AND provisional_token = $23`,
			target.UserID, target.ScopeKey, target.AccountID, now, previous.AccountID,
			previous.Generation, previous.Status, previous.AssignedAt, previous.LastActiveAt,
			previous.ExpiresAt, previous.LastMovedAt, previous.AssignmentReason,
			previous.ResetExcludeSourceAccount, previous.ResetSourceAccountID, previous.ResetAt,
			previous.ResetByAdminID, previous.ResetReason, previous.Predicted5HDemand,
			previous.Predicted7DDemand, previous.PredictionVersion, previous.ProvisionalToken,
			target.Generation, transition.Token)
	} else {
		result, err = exec.ExecContext(ctx, `
			UPDATE user_account_placements SET status = 'expired', expires_at = $4,
				provisional_token = NULL, updated_at = $4
			WHERE user_id = $1 AND scope_key = $2 AND account_id = $3 AND generation = $5
			  AND provisional_token = $6`, target.UserID, target.ScopeKey, target.AccountID,
			now, target.Generation, transition.Token)
	}
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	var reservationKind sql.NullString
	reservationErr := scanSingleRow(ctx, exec, `
		SELECT reservation_kind FROM account_user_contacts
		WHERE account_id = $1 AND user_id = $2 AND reservation_token = $3 FOR UPDATE`,
		[]any{*target.AccountID, target.UserID, transition.Token}, &reservationKind)
	if reservationErr != nil && !errors.Is(reservationErr, sql.ErrNoRows) {
		return false, reservationErr
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE account_user_contacts SET reservation_kind = NULL, reservation_token = NULL,
			reservation_until = NULL, updated_at = $4
		WHERE account_id = $1 AND user_id = $2 AND reservation_token = $3`,
		*target.AccountID, target.UserID, transition.Token, now); err != nil {
		return false, err
	}
	startedCooldown := reservationKind.Valid && (reservationKind.String == "new_resident" || reservationKind.String == "migration")
	if startedCooldown {
		if _, err := exec.ExecContext(ctx, `
			UPDATE accounts SET new_resident_cooldown_until = NULL, updated_at = $2
			WHERE id = $1 AND NOT EXISTS (
				SELECT 1 FROM account_user_contacts c
				WHERE c.account_id = $1 AND c.reservation_until > $2
			)`, *target.AccountID, now); err != nil {
			return false, err
		}
	}
	if transition.Kind == "migration" && transition.PreviousPlacement != nil && transition.PreviousPlacement.AccountID != nil {
		if _, err := exec.ExecContext(ctx, `
			UPDATE user_account_capacity_incidents SET migration_target_account_id = NULL,
				status = 'migration_eligible', close_reason = NULL, closed_at = NULL, updated_at = $5
			WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3
			  AND placement_generation = $4 AND close_reason = 'migration_succeeded'`,
			target.UserID, target.ScopeKey, *transition.PreviousPlacement.AccountID,
			transition.PreviousPlacement.Generation, now); err != nil {
			return false, err
		}
	}
	var sourceAccountID *int64
	if transition.PreviousPlacement != nil {
		sourceAccountID = transition.PreviousPlacement.AccountID
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, source_account_id, target_account_id,
			 event_type, reason, config_version, effective_source)
		VALUES ($1, $2, $3, $4, $5, 'rollback', 'request_failed_before_success', $6, 'global')`,
		target.UserID, target.ScopeKey, target.Generation, target.AccountID, sourceAccountID, config.ConfigVersion); err != nil {
		return false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}

// RecordOpenAIUserAffinityCapacityFailure 在固定窗口内累计客户端重试可见的容量失败。
func (r *accountRepository) RecordOpenAIUserAffinityCapacityFailure(ctx context.Context, userID, accountID, generation int64, scopeKey, requestIDHash, reason string, config service.OpenAIUserAffinityConfig) (*time.Time, error) {
	if r == nil || r.client == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	scopeKey = normalizeOpenAIUserAffinityScopeKey(scopeKey)
	requestIDHash = strings.TrimSpace(requestIDHash)
	if len(requestIDHash) != 64 {
		return nil, errors.New("openai user affinity capacity failure requires request id hash")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "capacity_unavailable"
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return nil, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	threshold := config.CapacityFailureMigrationThreshold
	windowSeconds := config.CapacityFailureWindowSeconds
	var accountThreshold, accountWindow sql.NullInt64
	if err := scanSingleRow(ctx, exec, `
		SELECT capacity_failure_migration_threshold, capacity_failure_window_seconds
		FROM accounts WHERE id = $1`, []any{accountID}, &accountThreshold, &accountWindow); err != nil {
		return nil, err
	}
	if accountThreshold.Valid && accountThreshold.Int64 > 0 {
		threshold = int(accountThreshold.Int64)
	}
	if accountWindow.Valid && accountWindow.Int64 > 0 {
		windowSeconds = int(accountWindow.Int64)
	}
	now := time.Now().UTC()
	var currentAccount sql.NullInt64
	var currentGeneration int64
	var currentStatus string
	if err := scanSingleRow(ctx, exec, `
		SELECT account_id, generation, status FROM user_account_placements
		WHERE user_id = $1 AND scope_key = $2 FOR UPDATE`, []any{userID, scopeKey}, &currentAccount, &currentGeneration, &currentStatus); err != nil {
		return nil, err
	}
	if !currentAccount.Valid || currentAccount.Int64 != accountID || currentGeneration != generation || currentStatus != "active" {
		return nil, nil
	}
	var incidentID int64
	var failureCount int
	var authorizedAt sql.NullTime
	incidentErr := scanSingleRow(ctx, exec, `
		SELECT id, failure_count, migration_authorized_at
		FROM user_account_capacity_incidents
		WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3
		  AND placement_generation = $4 AND closed_at IS NULL AND window_expires_at > $5
		ORDER BY window_started_at DESC LIMIT 1 FOR UPDATE`,
		[]any{userID, scopeKey, accountID, generation, now}, &incidentID, &failureCount, &authorizedAt)
	if incidentErr != nil && !errors.Is(incidentErr, sql.ErrNoRows) {
		return nil, incidentErr
	}
	if errors.Is(incidentErr, sql.ErrNoRows) {
		failureCount = 0
		if err := scanSingleRow(ctx, exec, `
			INSERT INTO user_account_capacity_incidents
				(user_id, scope_key, source_account_id, placement_generation,
				 window_started_at, window_expires_at, failure_threshold, failure_count,
				 config_version, status)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, 'collecting')
			RETURNING id`, []any{userID, scopeKey, accountID, generation, now,
			now.Add(time.Duration(windowSeconds) * time.Second), threshold, config.ConfigVersion}, &incidentID); err != nil {
			return nil, err
		}
	}
	var failureID int64
	insertErr := scanSingleRow(ctx, exec, `
		INSERT INTO user_account_capacity_failures
			(incident_id, request_id_hash, failure_reason, failed_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (incident_id, request_id_hash) DO NOTHING
		RETURNING id`, []any{incidentID, requestIDHash, reason, now}, &failureID)
	if errors.Is(insertErr, sql.ErrNoRows) {
		if tx != nil {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
		}
		if authorizedAt.Valid {
			value := authorizedAt.Time.UTC()
			return &value, nil
		}
		return nil, nil
	}
	if insertErr != nil {
		return nil, insertErr
	}
	if failureID > 0 {
		failureCount++
		if failureCount >= threshold && !authorizedAt.Valid {
			authorizedAt = sql.NullTime{Time: now, Valid: true}
		}
		status := "collecting"
		if authorizedAt.Valid {
			status = "migration_eligible"
		}
		stableWindowExpiresAt := now.Add(time.Duration(config.MigrationStabilitySeconds+windowSeconds) * time.Second)
		if _, err := exec.ExecContext(ctx, `
			UPDATE user_account_capacity_incidents SET failure_count = $2,
				last_failure_reason = $3, last_failure_at = $4,
				migration_authorized_at = $5::timestamptz, status = $6,
				window_expires_at = CASE WHEN $5::timestamptz IS NOT NULL THEN GREATEST(window_expires_at, $7) ELSE window_expires_at END,
				updated_at = $4
			WHERE id = $1`, incidentID, failureCount, reason, now, authorizedAt, status, stableWindowExpiresAt); err != nil {
			return nil, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	if authorizedAt.Valid {
		value := authorizedAt.Time.UTC()
		return &value, nil
	}
	return nil, nil
}

// GetOpenAIUserAffinityMigrationAuthorizedAt 读取当前 generation 尚未关闭的搬迁授权。
func (r *accountRepository) GetOpenAIUserAffinityMigrationAuthorizedAt(ctx context.Context, userID, accountID, generation int64, scopeKey string) (*time.Time, error) {
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	scopeKey = normalizeOpenAIUserAffinityScopeKey(scopeKey)
	var authorizedAt sql.NullTime
	err := scanSingleRow(ctx, r.sql, `
		SELECT migration_authorized_at FROM user_account_capacity_incidents
		WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3
		  AND placement_generation = $4 AND closed_at IS NULL
		  AND migration_authorized_at IS NOT NULL AND window_expires_at > NOW()
		ORDER BY migration_authorized_at DESC LIMIT 1`, []any{userID, scopeKey, accountID, generation}, &authorizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value := authorizedAt.Time.UTC()
	return &value, nil
}

// MigrateOpenAIUserAffinityPlacement 在归属 generation 未变化时原子搬迁到新账号。
func (r *accountRepository) MigrateOpenAIUserAffinityPlacement(ctx context.Context, userID, sourceAccountID, targetAccountID, generation int64, scopeKey, provisionalToken, reason string, config service.OpenAIUserAffinityConfig) (bool, error) {
	if r == nil || r.client == nil || sourceAccountID <= 0 || targetAccountID <= 0 || sourceAccountID == targetAccountID {
		return false, nil
	}
	provisionalToken = strings.TrimSpace(provisionalToken)
	if provisionalToken == "" {
		return false, errors.New("openai user affinity migration requires provisional token")
	}
	scopeKey = normalizeOpenAIUserAffinityScopeKey(scopeKey)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "capacity_retry_threshold"
	}
	demand, err := r.PredictOpenAIUserAffinityDemand(ctx, userID, config.ColdStartDemandQuantile)
	if err != nil {
		return false, err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	firstID, secondID := sourceAccountID, targetAccountID
	if firstID > secondID {
		firstID, secondID = secondID, firstID
	}
	rows, err := exec.QueryContext(ctx, `SELECT id FROM accounts WHERE id IN ($1, $2) ORDER BY id FOR UPDATE`, firstID, secondID)
	if err != nil {
		return false, err
	}
	locked := 0
	for rows.Next() {
		locked++
	}
	if closeErr := rows.Close(); closeErr != nil {
		return false, closeErr
	}
	if locked != 2 {
		return false, nil
	}
	var currentAccount sql.NullInt64
	var currentGeneration int64
	var status string
	var sourceProvisionalToken sql.NullString
	if err := scanSingleRow(ctx, exec, `
		SELECT account_id, generation, status, provisional_token FROM user_account_placements
		WHERE user_id = $1 AND scope_key = $2 FOR UPDATE`, []any{userID, scopeKey}, &currentAccount, &currentGeneration, &status, &sourceProvisionalToken); err != nil {
		return false, err
	}
	if !currentAccount.Valid || currentAccount.Int64 != sourceAccountID || currentGeneration != generation || status != "active" ||
		(sourceProvisionalToken.Valid && strings.TrimSpace(sourceProvisionalToken.String) != "") {
		return false, nil
	}
	var maxContactUsers, cooldownSeconds sql.NullInt64
	var cooldownUntil sql.NullTime
	var targetStatus, targetPlatform string
	var targetSchedulable, userAlreadyResident bool
	if err := scanSingleRow(ctx, exec, `
		SELECT max_contact_users, new_resident_cooldown_seconds, new_resident_cooldown_until,
		       status, schedulable, platform,
		       EXISTS (SELECT 1 FROM user_account_placements p
		               WHERE p.user_id = $2 AND p.account_id = accounts.id
		                 AND p.status = 'active' AND p.expires_at > $3)
		FROM accounts WHERE id = $1 AND deleted_at IS NULL`,
		[]any{targetAccountID, userID, now}, &maxContactUsers, &cooldownSeconds, &cooldownUntil,
		&targetStatus, &targetSchedulable, &targetPlatform, &userAlreadyResident); err != nil {
		return false, err
	}
	if targetStatus != service.StatusActive || !targetSchedulable || targetPlatform != service.PlatformOpenAI {
		return false, nil
	}
	effectiveMax := config.DefaultMaxContactUsers
	if maxContactUsers.Valid && maxContactUsers.Int64 > 0 {
		effectiveMax = int(maxContactUsers.Int64)
	}
	var activeContacts int
	var userAlreadyActive bool
	if err := scanSingleRow(ctx, exec, `
		SELECT COUNT(*), COALESCE(BOOL_OR(user_id = $3), FALSE) FROM account_user_contacts
		WHERE account_id = $1 AND (touch_expires_at > $2 OR reservation_until > $2)`,
		[]any{targetAccountID, now, userID}, &activeContacts, &userAlreadyActive); err != nil {
		return false, err
	}
	if activeContacts >= effectiveMax && !userAlreadyActive && !userAlreadyResident {
		return false, nil
	}
	if cooldownUntil.Valid && now.Before(cooldownUntil.Time) && !userAlreadyActive && !userAlreadyResident {
		return false, nil
	}
	newGeneration := generation + 1
	if _, err := exec.ExecContext(ctx, `
		UPDATE user_account_placements SET account_id = $2, generation = $3,
			assigned_at = $4, last_active_at = NULL, expires_at = $5,
			last_moved_at = $4, assignment_reason = $10,
			predicted_5h_demand = $6, predicted_7d_demand = $7, prediction_version = $8,
			provisional_token = $11, updated_at = $4
		WHERE user_id = $1 AND scope_key = $9`, userID, targetAccountID,
		newGeneration, now, now.Add(14*24*time.Hour), demand.Demand5H, demand.Demand7D, demand.Version, scopeKey, reason,
		provisionalToken); err != nil {
		return false, err
	}
	reservationKind := "migration"
	if userAlreadyActive || userAlreadyResident {
		reservationKind = "resident_scope_migration"
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO account_user_contacts
			(account_id, user_id, reservation_kind, reservation_token, reservation_until, reservation_generation,
			 reentry_config_version, follower_jitter_min_ms, follower_jitter_max_ms, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (account_id, user_id) DO UPDATE SET
			reservation_kind = EXCLUDED.reservation_kind,
			reservation_token = EXCLUDED.reservation_token,
			reservation_until = EXCLUDED.reservation_until,
			reservation_generation = EXCLUDED.reservation_generation,
			reentry_config_version = EXCLUDED.reentry_config_version,
			follower_jitter_min_ms = EXCLUDED.follower_jitter_min_ms,
			follower_jitter_max_ms = EXCLUDED.follower_jitter_max_ms,
			updated_at = EXCLUDED.updated_at`, targetAccountID, userID, reservationKind, provisionalToken, now.Add(2*time.Minute),
		newGeneration, config.ConfigVersion, config.FollowerJitterMinMS, config.FollowerJitterMaxMS, now); err != nil {
		return false, err
	}
	effectiveCooldown := config.DefaultNewResidentCooldownSeconds
	if cooldownSeconds.Valid && cooldownSeconds.Int64 > 0 {
		effectiveCooldown = int(cooldownSeconds.Int64)
	}
	if !userAlreadyActive && !userAlreadyResident {
		if _, err := exec.ExecContext(ctx, `UPDATE accounts SET new_resident_cooldown_until = $2,
			affinity_config_version = $3, updated_at = $1 WHERE id = $4`, now,
			now.Add(time.Duration(effectiveCooldown)*time.Second), config.ConfigVersion, targetAccountID); err != nil {
			return false, err
		}
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE user_account_capacity_incidents SET migration_target_account_id = $5,
			status = 'closed', close_reason = 'migration_succeeded', closed_at = $6, updated_at = $6
		WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3
		  AND placement_generation = $4 AND closed_at IS NULL`, userID, scopeKey, sourceAccountID,
		generation, targetAccountID, now); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, source_account_id, target_account_id,
			 event_type, reason, config_version, effective_source)
		VALUES ($1, $2, $3, $4, $5, 'migrated', $6, $7, 'global')`,
		userID, scopeKey, newGeneration, sourceAccountID, targetAccountID, reason, config.ConfigVersion); err != nil {
		return false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}
