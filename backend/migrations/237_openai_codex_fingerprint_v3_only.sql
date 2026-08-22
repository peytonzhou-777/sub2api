-- v3 已全量上线：迁移残余账号并移除旧算法和无作用域 Thread 的数据库兼容面。
UPDATE accounts
SET codex_fingerprint_version = 'v3'
WHERE codex_fingerprint_version = 'v2';

ALTER TABLE accounts
    DROP CONSTRAINT IF EXISTS accounts_codex_fingerprint_version_check;

ALTER TABLE accounts
    ADD CONSTRAINT accounts_codex_fingerprint_version_check
    CHECK (codex_fingerprint_version IN ('', 'v3'));

-- v3 Thread 必须绑定明确的 Session 作用域和稳定 epoch 时间。
UPDATE codex_fingerprint_thread_epochs
SET session_epoch_started_at = created_at
WHERE session_epoch_started_at IS NULL;

DELETE FROM codex_fingerprint_thread_epochs
WHERE session_scope_hash IS NULL;

DELETE FROM codex_fingerprint_thread_epochs AS t
WHERE NOT EXISTS (
    SELECT 1
    FROM codex_fingerprint_session_scopes AS s
    WHERE s.account_id = t.account_id
      AND s.scope_hash = t.session_scope_hash
);

ALTER TABLE codex_fingerprint_thread_epochs
    ALTER COLUMN session_epoch_started_at SET NOT NULL,
    ALTER COLUMN session_scope_hash SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'codex_fingerprint_thread_epochs_session_scope_fkey'
    ) THEN
        ALTER TABLE codex_fingerprint_thread_epochs
            ADD CONSTRAINT codex_fingerprint_thread_epochs_session_scope_fkey
            FOREIGN KEY (account_id, session_scope_hash)
            REFERENCES codex_fingerprint_session_scopes (account_id, scope_hash)
            ON DELETE CASCADE;
    END IF;
END $$;

COMMENT ON COLUMN accounts.codex_fingerprint_version
    IS 'Codex 指纹算法版本；空值表示尚未初始化，已初始化状态固定为 v3';
