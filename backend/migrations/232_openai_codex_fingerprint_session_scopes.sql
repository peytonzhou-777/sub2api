-- Session epoch 按上游可见作用域独立轮换，不复用账号活动或用户粘性状态。
CREATE TABLE IF NOT EXISTS codex_fingerprint_session_scopes (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    scope_hash CHAR(64) NOT NULL,
    session_epoch BIGINT NOT NULL,
    epoch_started_at TIMESTAMPTZ NOT NULL,
    last_active_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    rotation_count BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, scope_hash),
    CONSTRAINT codex_fingerprint_session_scopes_hash_check
        CHECK (scope_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT codex_fingerprint_session_scopes_epoch_positive_check
        CHECK (session_epoch > 0)
);

CREATE TABLE IF NOT EXISTS codex_fingerprint_cluster_secrets (
    singleton_id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton_id),
    secret_hash CHAR(64) NOT NULL CHECK (secret_hash ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE codex_fingerprint_session_scopes
    ADD COLUMN IF NOT EXISTS rotation_count BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_codex_fingerprint_session_scopes_activity
    ON codex_fingerprint_session_scopes (account_id, last_active_at);

ALTER TABLE codex_fingerprint_thread_epochs
    ADD COLUMN IF NOT EXISTS session_scope_hash CHAR(64);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'codex_fingerprint_thread_epochs_scope_hash_check'
    ) THEN
        ALTER TABLE codex_fingerprint_thread_epochs
            ADD CONSTRAINT codex_fingerprint_thread_epochs_scope_hash_check
            CHECK (session_scope_hash IS NULL OR session_scope_hash ~ '^[0-9a-f]{64}$');
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_codex_fingerprint_thread_epochs_scope_cleanup
    ON codex_fingerprint_thread_epochs (account_id, session_scope_hash, session_epoch, last_seen_at);

COMMENT ON TABLE codex_fingerprint_session_scopes IS 'Codex 上游可见 Session 作用域的独立 epoch 与活动状态';
COMMENT ON TABLE codex_fingerprint_cluster_secrets IS 'Codex 指纹集群密钥的不可逆一致性标识';
COMMENT ON COLUMN codex_fingerprint_thread_epochs.session_scope_hash IS '新 Thread 绑定的 Session 作用域；NULL 表示 v2 存量绑定';
