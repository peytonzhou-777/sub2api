package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const codexFingerprintThreadTouchInterval = 15 * time.Minute

// GetOrInitializeCodexFingerprintState 使用单条 SQL 原子初始化 v2 seed 与首个 epoch。
func (r *accountRepository) GetOrInitializeCodexFingerprintState(ctx context.Context, accountID int64, now time.Time) (*service.CodexFingerprintState, error) {
	if accountID <= 0 {
		return nil, service.ErrAccountNotFound
	}
	seedBytes := make([]byte, 32)
	if _, err := rand.Read(seedBytes); err != nil {
		return nil, err
	}
	seedHex := hex.EncodeToString(seedBytes)

	const query = `
UPDATE accounts
SET codex_fingerprint_seed = COALESCE(codex_fingerprint_seed, $2),
    codex_fingerprint_version = CASE WHEN codex_fingerprint_version = '' THEN 'v2' ELSE codex_fingerprint_version END,
    codex_fingerprint_epoch = CASE WHEN codex_fingerprint_epoch = 0 THEN 1 ELSE codex_fingerprint_epoch END,
    codex_fingerprint_epoch_started_at = COALESCE(codex_fingerprint_epoch_started_at, $3)
WHERE id = $1
  AND deleted_at IS NULL
  AND platform = 'openai'
  AND type = 'oauth'
  AND (
    (codex_fingerprint_seed IS NULL AND codex_fingerprint_version = '' AND codex_fingerprint_epoch = 0 AND codex_fingerprint_epoch_started_at IS NULL)
    OR
    (codex_fingerprint_seed IS NOT NULL AND codex_fingerprint_version = 'v2' AND codex_fingerprint_epoch > 0 AND codex_fingerprint_epoch_started_at IS NOT NULL)
  )
RETURNING codex_fingerprint_seed, codex_fingerprint_version, codex_fingerprint_epoch, codex_fingerprint_epoch_started_at`
	rows, err := r.sql.QueryContext(ctx, query, accountID, seedHex, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, service.ErrAccountNotFound
	}
	var state service.CodexFingerprintState
	if err := rows.Scan(
		&state.Seed,
		&state.Version,
		&state.Epoch,
		&state.EpochStartedAt,
	); err != nil {
		return nil, err
	}
	decodedSeed, decodeErr := hex.DecodeString(state.Seed)
	if state.Version != "v2" || state.Epoch <= 0 || state.EpochStartedAt.IsZero() ||
		state.Seed != strings.ToLower(state.Seed) || decodeErr != nil || len(decodedSeed) != 32 {
		return nil, errors.New("invalid codex fingerprint state")
	}
	return &state, nil
}

// getExistingCodexFingerprintSessionState 为已绑定 Thread 提供无账号锁快速路径。
// last_seen_at 仅按固定间隔触碰，避免每个 turn 都产生数据库写放大。
func (r *accountRepository) getExistingCodexFingerprintSessionState(
	ctx context.Context,
	accountID int64,
	threadSourceHash string,
	now time.Time,
) (*service.CodexFingerprintSessionResolution, bool, error) {
	const query = `
SELECT a.codex_fingerprint_seed, a.codex_fingerprint_version,
       a.codex_fingerprint_epoch, a.codex_fingerprint_epoch_started_at,
       t.session_epoch, t.last_seen_at
FROM codex_fingerprint_thread_epochs t
JOIN accounts a ON a.id = t.account_id
WHERE t.account_id = $1 AND t.source_hash = $2
  AND a.deleted_at IS NULL AND a.platform = 'openai' AND a.type = 'oauth'
  AND a.codex_fingerprint_seed IS NOT NULL
  AND a.codex_fingerprint_version = 'v2'
  AND a.codex_fingerprint_epoch > 0
  AND a.codex_fingerprint_epoch_started_at IS NOT NULL`
	rows, err := r.sql.QueryContext(ctx, query, accountID, threadSourceHash)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	var state service.CodexFingerprintState
	var boundEpoch int64
	var lastSeenAt time.Time
	if err := rows.Scan(
		&state.Seed,
		&state.Version,
		&state.Epoch,
		&state.EpochStartedAt,
		&boundEpoch,
		&lastSeenAt,
	); err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	decodedSeed, decodeErr := hex.DecodeString(state.Seed)
	if state.Version != "v2" || state.Epoch <= 0 || state.EpochStartedAt.IsZero() || boundEpoch <= 0 ||
		state.Seed != strings.ToLower(state.Seed) || decodeErr != nil || len(decodedSeed) != 32 {
		return nil, false, errors.New("invalid codex fingerprint session state")
	}
	if lastSeenAt.Before(now.Add(-codexFingerprintThreadTouchInterval)) {
		result, err := r.sql.ExecContext(ctx, `
UPDATE codex_fingerprint_thread_epochs
SET last_seen_at = $3
WHERE account_id = $1 AND source_hash = $2 AND last_seen_at < $4`,
			accountID, threadSourceHash, now.UTC(), now.Add(-codexFingerprintThreadTouchInterval).UTC())
		if err != nil {
			return nil, false, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return nil, false, err
		} else if affected == 0 {
			// 绑定可能刚被并发清理；回到事务路径重新确认并绑定。
			return nil, false, nil
		}
	}
	return &service.CodexFingerprintSessionResolution{
		State:      state,
		BoundEpoch: boundEpoch,
	}, true, nil
}

// ResolveCodexFingerprintSessionState 为 Thread 固定 epoch；仅新 Thread 可触发原子轮换。
func (r *accountRepository) ResolveCodexFingerprintSessionState(
	ctx context.Context,
	accountID int64,
	threadSourceHash string,
	now time.Time,
	allowRotation bool,
	expectedEpochStartedAt time.Time,
	idleBefore time.Time,
	oldEpochCutoff time.Time,
) (*service.CodexFingerprintSessionResolution, error) {
	if accountID <= 0 || len(threadSourceHash) != 64 || threadSourceHash != strings.ToLower(threadSourceHash) {
		return nil, service.ErrAccountNotFound
	}
	if _, err := hex.DecodeString(threadSourceHash); err != nil {
		return nil, service.ErrAccountNotFound
	}
	if resolved, ok, err := r.getExistingCodexFingerprintSessionState(ctx, accountID, threadSourceHash, now); err != nil {
		return nil, err
	} else if ok {
		return resolved, nil
	}
	beginner, ok := r.sql.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return nil, errors.New("codex fingerprint session repository requires transaction support")
	}
	ownedTx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	exec := sqlExecutor(ownedTx)
	defer func() { _ = ownedTx.Rollback() }()

	queryOne := func(query string, args []any, scan func(*sql.Rows) error) error {
		rows, err := exec.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return sql.ErrNoRows
		}
		return scan(rows)
	}

	var state service.CodexFingerprintState
	var lastUsedAt sql.NullTime
	err = queryOne(`
SELECT codex_fingerprint_seed, codex_fingerprint_version,
       codex_fingerprint_epoch, codex_fingerprint_epoch_started_at, last_used_at
FROM accounts
WHERE id = $1 AND deleted_at IS NULL AND platform = 'openai' AND type = 'oauth'
  AND codex_fingerprint_seed IS NOT NULL
  AND codex_fingerprint_version = 'v2'
  AND codex_fingerprint_epoch > 0
  AND codex_fingerprint_epoch_started_at IS NOT NULL
FOR UPDATE`, []any{accountID}, func(rows *sql.Rows) error {
		return rows.Scan(&state.Seed, &state.Version, &state.Epoch, &state.EpochStartedAt, &lastUsedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}

	boundEpoch := int64(0)
	rotated := false
	err = queryOne(`
SELECT session_epoch
FROM codex_fingerprint_thread_epochs
WHERE account_id = $1 AND source_hash = $2`, []any{accountID, threadSourceHash}, func(rows *sql.Rows) error {
		return rows.Scan(&boundEpoch)
	})
	if err == nil {
		if _, err := exec.ExecContext(ctx, `
UPDATE codex_fingerprint_thread_epochs
SET last_seen_at = $3
WHERE account_id = $1 AND source_hash = $2`, accountID, threadSourceHash, now.UTC()); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	} else {
		canRotate := allowRotation && state.EpochStartedAt.Equal(expectedEpochStartedAt) &&
			lastUsedAt.Valid && !lastUsedAt.Time.After(idleBefore)
		if canRotate {
			err = queryOne(`
UPDATE accounts
SET codex_fingerprint_epoch = codex_fingerprint_epoch + 1,
    codex_fingerprint_epoch_started_at = $2
WHERE id = $1
RETURNING codex_fingerprint_epoch, codex_fingerprint_epoch_started_at`, []any{accountID, now.UTC()}, func(rows *sql.Rows) error {
				return rows.Scan(&state.Epoch, &state.EpochStartedAt)
			})
			if err != nil {
				return nil, err
			}
			rotated = true
		}
		err = queryOne(`
INSERT INTO codex_fingerprint_thread_epochs
  (account_id, source_hash, session_epoch, created_at, last_seen_at)
VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (account_id, source_hash)
DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at
RETURNING session_epoch`, []any{accountID, threadSourceHash, state.Epoch, now.UTC()}, func(rows *sql.Rows) error {
			return rows.Scan(&boundEpoch)
		})
		if err != nil {
			return nil, err
		}
	}

	if _, err := exec.ExecContext(ctx, `
DELETE FROM codex_fingerprint_thread_epochs
WHERE account_id = $1 AND session_epoch < $2 - 2 AND last_seen_at < $3`,
		accountID, state.Epoch, oldEpochCutoff.UTC()); err != nil {
		return nil, err
	}
	decodedSeed, decodeErr := hex.DecodeString(state.Seed)
	if state.Version != "v2" || state.Epoch <= 0 || state.EpochStartedAt.IsZero() ||
		boundEpoch <= 0 || state.Seed != strings.ToLower(state.Seed) || decodeErr != nil || len(decodedSeed) != 32 {
		return nil, errors.New("invalid codex fingerprint session state")
	}
	if err := ownedTx.Commit(); err != nil {
		return nil, err
	}
	return &service.CodexFingerprintSessionResolution{
		State:      state,
		BoundEpoch: boundEpoch,
		Rotated:    rotated,
	}, nil
}
