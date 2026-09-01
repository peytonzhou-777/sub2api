package repository

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// BeginOpenAIUserAffinityReentry 在 placement/contact 行锁内选出唯一 leader，或返回当前批次的 follower 快照。
func (r *accountRepository) BeginOpenAIUserAffinityReentry(ctx context.Context, input service.OpenAIUserAffinityReentryBegin) (*service.OpenAIUserAffinityReentryAdmission, error) {
	if r == nil || r.client == nil || input.UserID <= 0 || input.AccountID <= 0 || input.Generation <= 0 {
		return nil, errors.New("openai user affinity storage unavailable")
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
	now := time.Now().UTC()
	input.ScopeKey = normalizeOpenAIUserAffinityScopeKey(input.ScopeKey)
	// 全部回流事务统一按 account -> placement -> contact 加锁，避免成功触达反向死锁。
	var lockedAccountID int64
	if err := scanSingleRow(ctx, exec, `SELECT id FROM accounts WHERE id = $1 FOR UPDATE`, []any{input.AccountID}, &lockedAccountID); err != nil {
		return nil, err
	}
	var accountID sql.NullInt64
	var generation int64
	var status string
	var expiresAt time.Time
	if err := scanSingleRow(ctx, exec, `
		SELECT account_id, generation, status, expires_at FROM user_account_placements
		WHERE user_id = $1 AND scope_key = $2 FOR UPDATE`, []any{input.UserID, input.ScopeKey},
		&accountID, &generation, &status, &expiresAt); err != nil {
		return nil, err
	}
	if !accountID.Valid || accountID.Int64 != input.AccountID || generation != input.Generation || status != "active" || !now.Before(expiresAt) {
		return nil, nil
	}

	var touchExpiry, reservationUntil, leaderLease sql.NullTime
	var reservationGeneration sql.NullInt64
	var batchToken, reentryState, leaderToken sql.NullString
	var leaderVersion int64
	var jitterMin, jitterMax sql.NullInt64
	contactErr := scanSingleRow(ctx, exec, `
		SELECT touch_expires_at, reservation_until, reservation_generation, reentry_batch_token, reentry_state,
		       leader_token, leader_version, leader_lease_until,
		       follower_jitter_min_ms, follower_jitter_max_ms
		FROM account_user_contacts WHERE account_id = $1 AND user_id = $2 FOR UPDATE`,
		[]any{input.AccountID, input.UserID}, &touchExpiry, &reservationUntil, &reservationGeneration, &batchToken,
		&reentryState, &leaderToken, &leaderVersion, &leaderLease, &jitterMin, &jitterMax)
	if contactErr != nil && !errors.Is(contactErr, sql.ErrNoRows) {
		return nil, contactErr
	}
	if contactErr == nil && touchExpiry.Valid && now.Before(touchExpiry.Time) {
		return &service.OpenAIUserAffinityReentryAdmission{Required: false}, nil
	}
	if contactErr == nil && reservationUntil.Valid && now.Before(reservationUntil.Time) && batchToken.Valid &&
		(reentryState.String == "leader_pending" || reentryState.String == "stagger_releasing") {
		coordinationGeneration := input.Generation
		if reservationGeneration.Valid && reservationGeneration.Int64 > 0 {
			coordinationGeneration = reservationGeneration.Int64
		}
		return &service.OpenAIUserAffinityReentryAdmission{
			Required: true, Leader: false, AccountID: input.AccountID, UserID: input.UserID,
			Generation: input.Generation, CoordinationGeneration: coordinationGeneration,
			ScopeKey: input.ScopeKey, BatchToken: batchToken.String,
			LeaderToken: leaderToken.String, LeaderVersion: leaderVersion,
			LeaderLeaseUntil: leaderLease.Time, ReentryState: reentryState.String,
			JitterMinMS: int(jitterMin.Int64), JitterMaxMS: int(jitterMax.Int64),
		}, nil
	}

	leaseUntil := input.LeaderLeaseUntil.UTC()
	if !leaseUntil.After(now) {
		leaseUntil = now.Add(30 * time.Second)
	}
	reservationExpiry := now.Add(2 * time.Minute)
	newVersion := leaderVersion + 1
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO account_user_contacts
			(account_id, user_id, reservation_kind, reservation_token, reservation_until,
			 reservation_generation, reentry_batch_token, reentry_state, leader_token,
			 leader_version, leader_lease_until, reentry_config_version,
			 follower_jitter_min_ms, follower_jitter_max_ms, updated_at)
		VALUES ($1, $2, 'resident_reentry', $3, $4, $5, $6, 'leader_pending', $7,
			$8, $9, $10, $11, $12, $13)
		ON CONFLICT (account_id, user_id) DO UPDATE SET
			reservation_kind = EXCLUDED.reservation_kind,
			reservation_token = EXCLUDED.reservation_token,
			reservation_until = EXCLUDED.reservation_until,
			reservation_generation = EXCLUDED.reservation_generation,
			reentry_batch_token = EXCLUDED.reentry_batch_token,
			reentry_state = EXCLUDED.reentry_state,
			leader_token = EXCLUDED.leader_token,
			leader_version = EXCLUDED.leader_version,
			leader_lease_until = EXCLUDED.leader_lease_until,
			reentry_config_version = EXCLUDED.reentry_config_version,
			follower_jitter_min_ms = EXCLUDED.follower_jitter_min_ms,
			follower_jitter_max_ms = EXCLUDED.follower_jitter_max_ms,
			updated_at = EXCLUDED.updated_at`, input.AccountID, input.UserID, input.LeaderToken,
		reservationExpiry, input.Generation, input.BatchToken, input.LeaderToken,
		newVersion, leaseUntil, input.Config.ConfigVersion, input.Config.FollowerJitterMinMS,
		input.Config.FollowerJitterMaxMS, now); err != nil {
		return nil, err
	}
	if _, err := exec.ExecContext(ctx, `INSERT INTO user_account_placement_events
		(user_id, scope_key, placement_generation, source_account_id, target_account_id,
		 event_type, reason, config_version, effective_source)
		VALUES ($1, $2, $3, $4, $4, 'resident_reentry_started', 'touch_ttl_expired', $5, 'global')`,
		input.UserID, input.ScopeKey, input.Generation, input.AccountID, input.Config.ConfigVersion); err != nil {
		return nil, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return &service.OpenAIUserAffinityReentryAdmission{
		Required: true, Leader: true, AccountID: input.AccountID, UserID: input.UserID,
		Generation: input.Generation, CoordinationGeneration: input.Generation,
		ScopeKey: input.ScopeKey, BatchToken: input.BatchToken, LeaderToken: input.LeaderToken,
		LeaderVersion: newVersion, LeaderLeaseUntil: leaseUntil, ReentryState: "leader_pending",
		JitterMinMS: input.Config.FollowerJitterMinMS,
		JitterMaxMS: input.Config.FollowerJitterMaxMS,
	}, nil
}

// ActivateOpenAIUserAffinityReentry 把成功 leader 的批次切换为错峰释放态。
func (r *accountRepository) ActivateOpenAIUserAffinityReentry(ctx context.Context, input service.OpenAIUserAffinityReentryTransition) (bool, error) {
	coordinationGeneration := openAIUserAffinityCoordinationGeneration(input.Generation, input.CoordinationGeneration)
	result, err := r.sql.ExecContext(ctx, `UPDATE account_user_contacts SET reentry_state = 'stagger_releasing',
		leader_lease_until = NULL, updated_at = NOW()
		WHERE account_id = $1 AND user_id = $2 AND reservation_generation = $3
		  AND reentry_batch_token = $4 AND leader_token = $5 AND leader_version = $6
		  AND reentry_state = 'leader_pending'`, input.AccountID, input.UserID, coordinationGeneration,
		input.BatchToken, input.LeaderToken, input.LeaderVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// FailOpenAIUserAffinityReentryLeader 只让当前版本 leader 的租约失效，followers 仍按 FIFO 接棒。
func (r *accountRepository) FailOpenAIUserAffinityReentryLeader(ctx context.Context, input service.OpenAIUserAffinityReentryTransition) (bool, error) {
	coordinationGeneration := openAIUserAffinityCoordinationGeneration(input.Generation, input.CoordinationGeneration)
	result, err := r.sql.ExecContext(ctx, `UPDATE account_user_contacts SET leader_lease_until = NOW(), updated_at = NOW()
		WHERE account_id = $1 AND user_id = $2 AND reservation_generation = $3
		  AND reentry_batch_token = $4 AND leader_token = $5 AND leader_version = $6
		  AND reentry_state = 'leader_pending'`, input.AccountID, input.UserID, coordinationGeneration,
		input.BatchToken, input.LeaderToken, input.LeaderVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// TakeoverOpenAIUserAffinityReentry 使用 batch/version CAS 只允许队首 follower 成为下一 leader。
func (r *accountRepository) TakeoverOpenAIUserAffinityReentry(ctx context.Context, input service.OpenAIUserAffinityReentryTakeover) (*service.OpenAIUserAffinityReentryAdmission, error) {
	coordinationGeneration := openAIUserAffinityCoordinationGeneration(input.Generation, input.CoordinationGeneration)
	var version int64
	var jitterMin, jitterMax sql.NullInt64
	err := scanSingleRow(ctx, r.sql, `UPDATE account_user_contacts SET leader_token = $6,
		leader_version = leader_version + 1, leader_lease_until = $7, updated_at = NOW()
		WHERE account_id = $1 AND user_id = $2 AND reservation_generation = $3
		  AND reentry_batch_token = $4 AND leader_version = $5
		  AND reentry_state = 'leader_pending' AND leader_lease_until <= NOW()
		RETURNING leader_version, follower_jitter_min_ms, follower_jitter_max_ms`,
		[]any{input.AccountID, input.UserID, coordinationGeneration, input.BatchToken,
			input.ExpectedLeaderVersion, input.WaiterToken, input.LeaderLeaseUntil.UTC()},
		&version, &jitterMin, &jitterMax)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &service.OpenAIUserAffinityReentryAdmission{
		Required: true, Leader: true, AccountID: input.AccountID, UserID: input.UserID,
		Generation: input.Generation, CoordinationGeneration: coordinationGeneration,
		ScopeKey: input.ScopeKey, BatchToken: input.BatchToken, LeaderToken: input.WaiterToken,
		LeaderVersion: version, LeaderLeaseUntil: input.LeaderLeaseUntil.UTC(),
		ReentryState: "leader_pending",
		JitterMinMS:  int(jitterMin.Int64), JitterMaxMS: int(jitterMax.Int64),
	}, nil
}

func openAIUserAffinityCoordinationGeneration(placementGeneration, coordinationGeneration int64) int64 {
	if coordinationGeneration > 0 {
		return coordinationGeneration
	}
	return placementGeneration
}

// CompleteOpenAIUserAffinityReentry 幂等关闭已无等待者的批次，不删除触达摘要和历史。
func (r *accountRepository) CompleteOpenAIUserAffinityReentry(ctx context.Context, accountID, userID, generation int64, batchToken string) error {
	_, err := r.sql.ExecContext(ctx, `UPDATE account_user_contacts SET reentry_state = 'completed',
		reentry_batch_token = NULL, leader_token = NULL, leader_lease_until = NULL,
		reservation_kind = NULL, reservation_token = NULL, reservation_until = NULL,
		updated_at = NOW()
		WHERE account_id = $1 AND user_id = $2 AND reservation_generation = $3
		  AND reentry_batch_token = $4`, accountID, userID, generation, batchToken)
	return err
}

// PredictOpenAIUserAffinityDemand 用近窗 token 相对活跃用户分位数生成保守的额度比例预测。
func (r *accountRepository) PredictOpenAIUserAffinityDemand(ctx context.Context, userID int64, quantile float64) (service.OpenAIUserAffinityDemand, error) {
	result := service.OpenAIUserAffinityDemand{Demand5H: 0.05, Demand7D: 0.05, Version: "token_quantile_v1"}
	if r == nil || r.sql == nil || userID <= 0 {
		return result, errors.New("openai user affinity storage unavailable")
	}
	quantile = math.Max(0.5, math.Min(0.99, quantile))
	var user5H, user7D, population5H, population7D float64
	err := scanSingleRow(ctx, r.sql, `WITH per_user AS (
		SELECT l.user_id,
		       SUM(l.input_tokens + l.output_tokens + l.cache_creation_tokens + l.cache_read_tokens)
		           FILTER (WHERE l.created_at >= NOW() - INTERVAL '5 hours')::double precision AS tokens_5h,
		       SUM(l.input_tokens + l.output_tokens + l.cache_creation_tokens + l.cache_read_tokens)::double precision AS tokens_7d
		FROM usage_logs l JOIN accounts a ON a.id = l.account_id
		WHERE l.created_at >= NOW() - INTERVAL '7 days' AND a.platform = 'openai'
		GROUP BY l.user_id
	), population AS (
		SELECT percentile_cont($2) WITHIN GROUP (ORDER BY COALESCE(tokens_5h, 0)) AS p5h,
		       percentile_cont($2) WITHIN GROUP (ORDER BY COALESCE(tokens_7d, 0)) AS p7d
		FROM per_user
	)
	SELECT COALESCE(u.tokens_5h, 0), COALESCE(u.tokens_7d, 0),
	       COALESCE(p.p5h, 0), COALESCE(p.p7d, 0)
	FROM population p LEFT JOIN per_user u ON u.user_id = $1`, []any{userID, quantile},
		&user5H, &user7D, &population5H, &population7D)
	if err != nil {
		return result, err
	}
	result.Demand5H = normalizedOpenAIUserAffinityDemand(user5H, population5H)
	result.Demand7D = normalizedOpenAIUserAffinityDemand(user7D, population7D)
	return result, nil
}

func normalizedOpenAIUserAffinityDemand(userTokens, populationQuantile float64) float64 {
	if populationQuantile <= 0 {
		return 0.05
	}
	if userTokens <= 0 {
		userTokens = populationQuantile
	}
	return math.Max(0.01, math.Min(0.50, 0.05*userTokens/populationQuantile))
}
