-- 管理员整组重置后保留全部原常驻账号的一次性排除事实。
-- 排除在新居民首次成功落槽后消费，避免永久禁止用户未来再次使用这些账号。

CREATE TABLE IF NOT EXISTS openai_user_affinity_reset_exclusions (
    id                  BIGSERIAL PRIMARY KEY,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_key           TEXT NOT NULL DEFAULT 'openai',
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    reset_generation    BIGINT NOT NULL,
    actor_admin_id      BIGINT REFERENCES users(id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    consumed_at         TIMESTAMPTZ,
    CONSTRAINT openai_user_affinity_reset_exclusions_generation_check CHECK (reset_generation > 0),
    CONSTRAINT openai_user_affinity_reset_exclusions_unique
        UNIQUE (user_id, scope_key, account_id, reset_generation)
);

CREATE INDEX IF NOT EXISTS idx_openai_user_affinity_reset_exclusions_pending
    ON openai_user_affinity_reset_exclusions(user_id, scope_key, account_id)
    WHERE consumed_at IS NULL;
