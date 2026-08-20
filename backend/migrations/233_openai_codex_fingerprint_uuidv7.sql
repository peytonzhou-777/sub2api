-- v3 将 Session 与 prompt_cache_key 同步迁移为 UUIDv7；v2 保留用于滚动部署兼容。
ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_codex_fingerprint_version_check;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_codex_fingerprint_version_check
    CHECK (codex_fingerprint_version IN ('', 'v2', 'v3'));

-- Thread 绑定必须保存其实际 epoch 起点，不能用当前 epoch 时间替代旧会话时间。
-- 迁移期保持可空，允许尚未升级的 v2 节点继续写入；切换 v3 前由管理员轮换事务再次补齐。
ALTER TABLE codex_fingerprint_thread_epochs
    ADD COLUMN IF NOT EXISTS session_epoch_started_at TIMESTAMPTZ;

UPDATE codex_fingerprint_thread_epochs AS t
SET session_epoch_started_at = s.epoch_started_at
FROM codex_fingerprint_session_scopes AS s
WHERE t.account_id = s.account_id
  AND t.session_scope_hash = s.scope_hash
  AND t.session_epoch = s.session_epoch
  AND t.session_epoch_started_at IS NULL;

UPDATE codex_fingerprint_thread_epochs AS t
SET session_epoch_started_at = a.codex_fingerprint_epoch_started_at
FROM accounts AS a
WHERE t.account_id = a.id
  AND t.session_scope_hash IS NULL
  AND t.session_epoch = a.codex_fingerprint_epoch
  AND a.codex_fingerprint_epoch_started_at IS NOT NULL
  AND t.session_epoch_started_at IS NULL;

WITH inferred_epoch_starts AS (
    SELECT account_id, session_scope_hash, session_epoch, MIN(created_at) AS epoch_started_at
    FROM codex_fingerprint_thread_epochs
    WHERE session_epoch_started_at IS NULL
    GROUP BY account_id, session_scope_hash, session_epoch
)
UPDATE codex_fingerprint_thread_epochs AS t
SET session_epoch_started_at = e.epoch_started_at
FROM inferred_epoch_starts AS e
WHERE t.account_id = e.account_id
  AND t.session_scope_hash IS NOT DISTINCT FROM e.session_scope_hash
  AND t.session_epoch = e.session_epoch
  AND t.session_epoch_started_at IS NULL;

COMMENT ON COLUMN codex_fingerprint_thread_epochs.session_epoch_started_at
    IS 'Thread 所绑定 Session epoch 的稳定起始时间；v3 UUIDv7 的 48-bit 时间来源';
