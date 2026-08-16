-- Thread 只保存来源哈希与绑定 epoch，避免原始客户端会话标识进入数据库。
CREATE TABLE IF NOT EXISTS codex_fingerprint_thread_epochs (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    source_hash CHAR(64) NOT NULL,
    session_epoch BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, source_hash),
    CONSTRAINT codex_fingerprint_thread_epochs_source_hash_check
        CHECK (source_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT codex_fingerprint_thread_epochs_epoch_positive_check
        CHECK (session_epoch > 0)
);

CREATE INDEX IF NOT EXISTS idx_codex_fingerprint_thread_epochs_cleanup
    ON codex_fingerprint_thread_epochs (account_id, session_epoch, last_seen_at);

COMMENT ON TABLE codex_fingerprint_thread_epochs IS 'Codex Thread 来源哈希到 Session epoch 的内部绑定';
