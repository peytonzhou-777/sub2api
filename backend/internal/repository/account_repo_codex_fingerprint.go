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

// ValidateCodexFingerprintSecret 将不可逆密钥摘要绑定到共享数据库，阻止 HA 实例静默分叉。
func (r *accountRepository) ValidateCodexFingerprintSecret(ctx context.Context, secretHash string, now time.Time) error {
	if len(secretHash) != 64 || secretHash != strings.ToLower(secretHash) {
		return errors.New("invalid codex fingerprint secret hash")
	}
	if _, err := hex.DecodeString(secretHash); err != nil {
		return errors.New("invalid codex fingerprint secret hash")
	}
	rows, err := r.sql.QueryContext(ctx, `
INSERT INTO codex_fingerprint_cluster_secrets (singleton_id, secret_hash, created_at, updated_at)
VALUES (TRUE, $1, $2, $2)
ON CONFLICT (singleton_id) DO UPDATE
SET updated_at = EXCLUDED.updated_at
WHERE codex_fingerprint_cluster_secrets.secret_hash = EXCLUDED.secret_hash
RETURNING secret_hash`, secretHash, now.UTC())
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return errors.New("codex fingerprint cluster secret does not match persisted secret id")
	}
	var persisted string
	if err := rows.Scan(&persisted); err != nil {
		return err
	}
	if strings.TrimSpace(persisted) != secretHash {
		return errors.New("codex fingerprint cluster secret does not match persisted secret id")
	}
	return nil
}

// GetOrInitializeCodexFingerprintState 使用单条 SQL 原子初始化 v3 seed 与首个 epoch。
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
    codex_fingerprint_version = CASE WHEN codex_fingerprint_version = '' THEN 'v3' ELSE codex_fingerprint_version END,
    codex_fingerprint_epoch = CASE WHEN codex_fingerprint_epoch = 0 THEN 1 ELSE codex_fingerprint_epoch END,
    codex_fingerprint_epoch_started_at = COALESCE(codex_fingerprint_epoch_started_at, $3)
WHERE id = $1
  AND deleted_at IS NULL
  AND platform = 'openai'
  AND type = 'oauth'
  AND (
    (codex_fingerprint_seed IS NULL AND codex_fingerprint_version = '' AND codex_fingerprint_epoch = 0 AND codex_fingerprint_epoch_started_at IS NULL)
    OR
    (codex_fingerprint_seed IS NOT NULL AND codex_fingerprint_version = 'v3' AND codex_fingerprint_epoch > 0 AND codex_fingerprint_epoch_started_at IS NOT NULL)
  )
RETURNING codex_fingerprint_seed, codex_fingerprint_version, codex_fingerprint_epoch, codex_fingerprint_epoch_started_at`
	rows, err := r.sql.QueryContext(ctx, query, accountID, seedHex, now.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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
	if !validCodexFingerprintAlgorithmVersion(state.Version) || state.Epoch <= 0 || state.EpochStartedAt.IsZero() ||
		state.Seed != strings.ToLower(state.Seed) || decodeErr != nil || len(decodedSeed) != 32 {
		return nil, errors.New("invalid codex fingerprint state")
	}
	return &state, nil
}

// getExistingCodexFingerprintSessionState 为已绑定 Thread 提供无账号锁快速路径。
func (r *accountRepository) getExistingCodexFingerprintSessionState(
	ctx context.Context,
	exec sqlExecutor,
	accountID int64,
	threadSourceHash string,
	now time.Time,
) (*service.CodexFingerprintSessionResolution, bool, error) {
	const query = `
SELECT a.codex_fingerprint_seed, a.codex_fingerprint_version,
       s.session_epoch, s.epoch_started_at,
       t.session_epoch, t.session_epoch_started_at,
       t.last_seen_at, t.session_scope_hash::text
FROM codex_fingerprint_thread_epochs t
JOIN accounts a ON a.id = t.account_id
JOIN codex_fingerprint_session_scopes s
  ON s.account_id = t.account_id AND s.scope_hash = t.session_scope_hash
WHERE t.account_id = $1 AND t.source_hash = $2
  AND a.deleted_at IS NULL AND a.platform = 'openai' AND a.type = 'oauth'
  AND a.codex_fingerprint_seed IS NOT NULL
  AND a.codex_fingerprint_version = 'v3'
  AND a.codex_fingerprint_epoch > 0
  AND a.codex_fingerprint_epoch_started_at IS NOT NULL
  AND t.session_epoch_started_at IS NOT NULL`
	rows, err := exec.QueryContext(ctx, query, accountID, threadSourceHash)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
		return nil, false, nil
	}
	var state service.CodexFingerprintState
	var boundEpoch int64
	var boundEpochStartedAt time.Time
	var lastSeenAt time.Time
	var boundScopeHash string
	if err := rows.Scan(
		&state.Seed,
		&state.Version,
		&state.Epoch,
		&state.EpochStartedAt,
		&boundEpoch,
		&boundEpochStartedAt,
		&lastSeenAt,
		&boundScopeHash,
	); err != nil {
		return nil, false, err
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	decodedSeed, decodeErr := hex.DecodeString(state.Seed)
	if !validCodexFingerprintAlgorithmVersion(state.Version) || state.Epoch <= 0 || state.EpochStartedAt.IsZero() ||
		boundEpoch <= 0 || boundEpochStartedAt.IsZero() ||
		state.Seed != strings.ToLower(state.Seed) || decodeErr != nil || len(decodedSeed) != 32 {
		return nil, false, errors.New("invalid codex fingerprint session state")
	}
	if lastSeenAt.Before(now.Add(-codexFingerprintThreadTouchInterval)) {
		result, err := exec.ExecContext(ctx, `
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
		if boundScopeHash != "" && state.Epoch == boundEpoch {
			if _, err := exec.ExecContext(ctx, `
UPDATE codex_fingerprint_session_scopes
SET last_active_at = $3, updated_at = $3
WHERE account_id = $1 AND scope_hash = $2 AND last_active_at < $4`,
				accountID, boundScopeHash, now.UTC(), now.Add(-codexFingerprintThreadTouchInterval).UTC()); err != nil {
				return nil, false, err
			}
		}
	}
	return &service.CodexFingerprintSessionResolution{
		State:                   state,
		BoundEpoch:              boundEpoch,
		BoundEpochStartedAt:     boundEpochStartedAt,
		MatchedThreadSourceHash: threadSourceHash,
		BoundSessionScopeHash:   boundScopeHash,
	}, true, nil
}

func validateCodexFingerprintSessionRequest(request service.CodexFingerprintSessionRequest) error {
	if request.AccountID <= 0 || len(request.SessionScopeHash) != 64 ||
		request.SessionScopeHash != strings.ToLower(request.SessionScopeHash) || len(request.ThreadSourceHashes) == 0 {
		return service.ErrAccountNotFound
	}
	if _, err := hex.DecodeString(request.SessionScopeHash); err != nil {
		return service.ErrAccountNotFound
	}
	if request.SessionScopeVersion < 1 || request.SessionScopeVersion > 2 ||
		request.SessionSlot < 0 || request.SessionSlotCount < 1 || request.SessionSlotCount > 4 ||
		request.SessionSlot >= request.SessionSlotCount {
		return service.ErrAccountNotFound
	}
	allHashes := append(append([]string{}, request.ThreadSourceHashes...), request.BindSourceHashes...)
	for _, hash := range allHashes {
		if len(hash) != 64 || hash != strings.ToLower(hash) {
			return service.ErrAccountNotFound
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return service.ErrAccountNotFound
		}
	}
	return nil
}

func shouldRotateCodexFingerprintScope(
	request service.CodexFingerprintSessionRequest,
	state service.CodexFingerprintState,
	lastActiveAt time.Time,
) (bool, string) {
	if state.EpochStartedAt.IsZero() || lastActiveAt.IsZero() {
		return false, ""
	}
	canRotateAfterIdle := request.RotationAllowed && !state.EpochStartedAt.After(request.MinAgeBefore) && !lastActiveAt.After(request.IdleBefore)
	canRotateAtMaxAge := !state.EpochStartedAt.After(request.MaxAgeBefore)
	if canRotateAtMaxAge {
		return true, "max_age"
	}
	if canRotateAfterIdle {
		return true, "idle_after_min_age"
	}
	return false, ""
}

// ResolveCodexFingerprintSessionState 为 Thread 固定作用域与 epoch；仅新 Thread 可轮换。
func (r *accountRepository) ResolveCodexFingerprintSessionState(
	ctx context.Context,
	request service.CodexFingerprintSessionRequest,
) (*service.CodexFingerprintSessionResolution, error) {
	// 零值来自旧调用方和历史测试，统一按单槽 v1 兼容；新链路显式写入 v2。
	if request.SessionScopeVersion == 0 {
		request.SessionScopeVersion = 1
	}
	if request.SessionSlotCount == 0 {
		request.SessionSlotCount = 1
	}
	if err := validateCodexFingerprintSessionRequest(request); err != nil {
		return nil, err
	}
	for _, threadSourceHash := range request.ThreadSourceHashes {
		if resolved, ok, err := r.getExistingCodexFingerprintSessionState(ctx, r.sql, request.AccountID, threadSourceHash, request.Now); err != nil {
			return nil, err
		} else if ok {
			if len(request.BindSourceHashes) == 0 || hashInStringSlice(request.BindSourceHashes, threadSourceHash) {
				return resolved, nil
			}
			break
		}
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
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return sql.ErrNoRows
		}
		return scan(rows)
	}

	for _, threadSourceHash := range request.ThreadSourceHashes {
		if resolved, ok, err := r.getExistingCodexFingerprintSessionState(ctx, exec, request.AccountID, threadSourceHash, request.Now); err != nil {
			return nil, err
		} else if ok {
			created, bindErr := r.bindCodexFingerprintThreadAliases(ctx, exec, request, resolved)
			if bindErr != nil {
				return nil, bindErr
			}
			resolved.Created = resolved.Created || created
			if err := ownedTx.Commit(); err != nil {
				return nil, err
			}
			return resolved, nil
		}
	}

	var accountState service.CodexFingerprintState
	err = queryOne(`
SELECT codex_fingerprint_seed, codex_fingerprint_version,
       codex_fingerprint_epoch, codex_fingerprint_epoch_started_at
FROM accounts
WHERE id = $1 AND deleted_at IS NULL AND platform = 'openai' AND type = 'oauth'
  AND codex_fingerprint_seed IS NOT NULL
  AND codex_fingerprint_version = 'v3'
  AND codex_fingerprint_epoch > 0
  AND codex_fingerprint_epoch_started_at IS NOT NULL`, []any{request.AccountID}, func(rows *sql.Rows) error {
		return rows.Scan(&accountState.Seed, &accountState.Version, &accountState.Epoch, &accountState.EpochStartedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrAccountNotFound
	}
	if err != nil {
		return nil, err
	}

	if _, err := exec.ExecContext(ctx, `
INSERT INTO codex_fingerprint_session_scopes
  (account_id, scope_hash, session_epoch, epoch_started_at, last_active_at, created_at, updated_at,
   scope_version, slot_index, slot_count)
VALUES ($1, $2, $3, $4, $4, $4, $4, $5, $6, $7)
ON CONFLICT (account_id, scope_hash) DO NOTHING`,
		request.AccountID, request.SessionScopeHash, accountState.Epoch, request.Now.UTC(),
		request.SessionScopeVersion, request.SessionSlot, request.SessionSlotCount); err != nil {
		return nil, err
	}

	state := service.CodexFingerprintState{Seed: accountState.Seed, Version: accountState.Version}
	var lastActiveAt time.Time
	err = queryOne(`
SELECT session_epoch, epoch_started_at, last_active_at
FROM codex_fingerprint_session_scopes
WHERE account_id = $1 AND scope_hash = $2
FOR UPDATE`, []any{request.AccountID, request.SessionScopeHash}, func(rows *sql.Rows) error {
		return rows.Scan(&state.Epoch, &state.EpochStartedAt, &lastActiveAt)
	})
	if err != nil {
		return nil, err
	}

	rotated := false
	rotationReason := ""
	if shouldRotate, reason := shouldRotateCodexFingerprintScope(request, state, lastActiveAt); shouldRotate {
		err = queryOne(`
UPDATE codex_fingerprint_session_scopes
SET session_epoch = session_epoch + 1,
    epoch_started_at = $3,
    last_active_at = $3,
    updated_at = $3,
    rotation_count = rotation_count + 1
WHERE account_id = $1 AND scope_hash = $2
RETURNING session_epoch, epoch_started_at`, []any{request.AccountID, request.SessionScopeHash, request.Now.UTC()}, func(rows *sql.Rows) error {
			return rows.Scan(&state.Epoch, &state.EpochStartedAt)
		})
		if err != nil {
			return nil, err
		}
		rotated = true
		rotationReason = reason
	}

	boundEpoch := int64(0)
	boundEpochStartedAt := time.Time{}
	boundScopeHash := ""
	primaryThreadHash := request.ThreadSourceHashes[0]
	err = queryOne(`
INSERT INTO codex_fingerprint_thread_epochs
  (account_id, source_hash, session_epoch, session_epoch_started_at, created_at, last_seen_at, session_scope_hash)
VALUES ($1, $2, $3, $4, $5, $5, $6)
ON CONFLICT (account_id, source_hash)
DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at
RETURNING session_epoch, session_epoch_started_at, session_scope_hash::text`, []any{
		request.AccountID, primaryThreadHash, state.Epoch, state.EpochStartedAt.UTC(), request.Now.UTC(), request.SessionScopeHash,
	}, func(rows *sql.Rows) error {
		return rows.Scan(&boundEpoch, &boundEpochStartedAt, &boundScopeHash)
	})
	if err != nil {
		return nil, err
	}
	if _, err := exec.ExecContext(ctx, `
UPDATE codex_fingerprint_session_scopes
SET last_active_at = $3, updated_at = $3
WHERE account_id = $1 AND scope_hash = $2 AND session_epoch = $4`,
		request.AccountID, boundScopeHash, request.Now.UTC(), boundEpoch); err != nil {
		return nil, err
	}

	if _, err := exec.ExecContext(ctx, `
DELETE FROM codex_fingerprint_thread_epochs
WHERE account_id = $1 AND session_scope_hash = $2
  AND session_epoch < $3 - 2 AND last_seen_at < $4`,
		request.AccountID, boundScopeHash, boundEpoch, request.OldEpochCutoff.UTC()); err != nil {
		return nil, err
	}
	decodedSeed, decodeErr := hex.DecodeString(state.Seed)
	if !validCodexFingerprintAlgorithmVersion(state.Version) || state.Epoch <= 0 || state.EpochStartedAt.IsZero() ||
		boundEpoch <= 0 || boundEpochStartedAt.IsZero() || state.Seed != strings.ToLower(state.Seed) || decodeErr != nil || len(decodedSeed) != 32 {
		return nil, errors.New("invalid codex fingerprint session state")
	}
	if err := ownedTx.Commit(); err != nil {
		return nil, err
	}
	return &service.CodexFingerprintSessionResolution{
		State:                   state,
		BoundEpoch:              boundEpoch,
		BoundEpochStartedAt:     boundEpochStartedAt,
		MatchedThreadSourceHash: primaryThreadHash,
		BoundSessionScopeHash:   boundScopeHash,
		BoundScopeVersion:       request.SessionScopeVersion,
		BoundSessionSlot:        request.SessionSlot,
		BoundSessionSlotCount:   request.SessionSlotCount,
		RotationReason:          rotationReason,
		Rotated:                 rotated,
		Created:                 true,
	}, nil
}

func hashInStringSlice(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

// bindCodexFingerprintThreadAliases 将首次出现的子线程绑定到父线程已选定的 epoch。
// 若并发请求已先完成绑定，则以数据库中的实际 scope 与 epoch 为准。
func (r *accountRepository) bindCodexFingerprintThreadAliases(
	ctx context.Context,
	exec sqlExecutor,
	request service.CodexFingerprintSessionRequest,
	resolved *service.CodexFingerprintSessionResolution,
) (bool, error) {
	if resolved == nil || resolved.BoundEpoch <= 0 || resolved.BoundEpochStartedAt.IsZero() || len(request.BindSourceHashes) == 0 {
		return false, nil
	}
	created := false
	for _, sourceHash := range request.BindSourceHashes {
		if sourceHash == "" || sourceHash == resolved.MatchedThreadSourceHash {
			continue
		}
		rows, err := exec.QueryContext(ctx, `
INSERT INTO codex_fingerprint_thread_epochs
  (account_id, source_hash, session_epoch, session_epoch_started_at, created_at, last_seen_at, session_scope_hash)
VALUES ($1, $2, $3, $4, $5, $5, $6)
ON CONFLICT (account_id, source_hash)
	DO NOTHING
RETURNING session_epoch, session_epoch_started_at, session_scope_hash::text`,
			request.AccountID, sourceHash, resolved.BoundEpoch, resolved.BoundEpochStartedAt.UTC(), request.Now.UTC(), resolved.BoundSessionScopeHash)
		if err != nil {
			return false, err
		}
		if rows.Next() {
			if err := rows.Scan(&resolved.BoundEpoch, &resolved.BoundEpochStartedAt, &resolved.BoundSessionScopeHash); err != nil {
				_ = rows.Close()
				return false, err
			}
			if err := rows.Close(); err != nil {
				return false, err
			}
			resolved.MatchedThreadSourceHash = sourceHash
			created = true
			continue
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return false, err
		}
		if err := rows.Close(); err != nil {
			return false, err
		}

		// INSERT 等待并发事务后发生冲突时，重新读取赢家，避免返回父线程的旧绑定。
		actual, ok, err := r.getExistingCodexFingerprintSessionState(
			ctx, exec, request.AccountID, sourceHash, request.Now,
		)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, errors.New("codex fingerprint thread alias conflict without persisted binding")
		}
		*resolved = *actual
	}
	return created, nil
}

func validCodexFingerprintAlgorithmVersion(version string) bool {
	return version == "v3"
}
