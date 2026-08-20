package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetCodexFingerprintAdminStatus 汇总安全统计，不读取或返回 seed。
func (r *accountRepository) GetCodexFingerprintAdminStatus(ctx context.Context, accountID int64) (*service.CodexFingerprintAdminStatus, error) {
	account, err := r.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil || !account.IsOpenAIOAuth() {
		return nil, service.ErrCodexFingerprintAccountUnsupported
	}
	status := &service.CodexFingerprintAdminStatus{
		AccountID:        accountID,
		Mode:             string(account.GetCodexFingerprintMode()),
		AlgorithmVersion: account.CodexFingerprintVersion,
		AccountEpoch:     account.CodexFingerprintEpoch,
		EpochStartedAt:   account.CodexFingerprintEpochStartedAt,
	}
	rows, err := r.sql.QueryContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM codex_fingerprint_session_scopes WHERE account_id = $1),
  (SELECT COALESCE(SUM(rotation_count), 0) FROM codex_fingerprint_session_scopes WHERE account_id = $1),
  (SELECT COUNT(*) FROM codex_fingerprint_thread_epochs WHERE account_id = $1),
  (SELECT COUNT(*) FROM codex_fingerprint_thread_epochs WHERE account_id = $1 AND session_scope_hash IS NULL),
  COALESCE((SELECT secret_hash::text FROM codex_fingerprint_cluster_secrets WHERE singleton_id = TRUE), '')`, accountID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("codex fingerprint status query returned no row")
	}
	var secretID string
	if err := rows.Scan(
		&status.SessionScopeCount,
		&status.RotationCount,
		&status.ThreadCount,
		&status.LegacyThreadCount,
		&secretID,
	); err != nil {
		return nil, err
	}
	secretID = strings.TrimSpace(secretID)
	if len(secretID) > 12 {
		secretID = secretID[:12]
	}
	status.SecretID = secretID
	return status, nil
}

// RotateCodexFingerprintSessions 原子升级 v3 并推进账号基准和所有现有作用域，不写调度表。
func (r *accountRepository) RotateCodexFingerprintSessions(ctx context.Context, accountID int64, now time.Time) error {
	if accountID <= 0 {
		return service.ErrAccountNotFound
	}
	beginner, ok := r.sql.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return errors.New("codex fingerprint rotation requires transaction support")
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// 兼容迁移期间仍由旧节点写入的 NULL；同一 scope/epoch 统一取最早已知时间。
	if _, err := tx.ExecContext(ctx, `
WITH epoch_starts AS (
  SELECT account_id, session_scope_hash, session_epoch,
         MIN(COALESCE(session_epoch_started_at, created_at)) AS epoch_started_at
  FROM codex_fingerprint_thread_epochs
  WHERE account_id = $1
  GROUP BY account_id, session_scope_hash, session_epoch
)
UPDATE codex_fingerprint_thread_epochs AS t
SET session_epoch_started_at = e.epoch_started_at
FROM epoch_starts AS e
WHERE t.account_id = e.account_id
  AND t.session_scope_hash IS NOT DISTINCT FROM e.session_scope_hash
  AND t.session_epoch = e.session_epoch
  AND t.session_epoch_started_at IS NULL`, accountID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
UPDATE accounts
SET codex_fingerprint_version = 'v3',
    codex_fingerprint_epoch = codex_fingerprint_epoch + 1,
    codex_fingerprint_epoch_started_at = $2
WHERE id = $1 AND deleted_at IS NULL AND platform = 'openai' AND type = 'oauth'
  AND codex_fingerprint_seed IS NOT NULL AND codex_fingerprint_version IN ('v2', 'v3')
  AND codex_fingerprint_epoch > 0 AND codex_fingerprint_epoch_started_at IS NOT NULL
RETURNING id`, accountID, now.UTC())
	if err != nil {
		return err
	}
	hasAccount := rows.Next()
	if closeErr := rows.Close(); closeErr != nil {
		return closeErr
	}
	if !hasAccount {
		return service.ErrCodexFingerprintAccountUnsupported
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE codex_fingerprint_session_scopes
SET session_epoch = session_epoch + 1,
    epoch_started_at = $2,
    last_active_at = $2,
    updated_at = $2,
    rotation_count = rotation_count + 1
WHERE account_id = $1`, accountID, now.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}
