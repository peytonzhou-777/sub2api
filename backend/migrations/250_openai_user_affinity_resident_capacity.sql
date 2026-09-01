-- OpenAI 用户粘性准入改为按账号当前唯一居民数限制。
-- 旧触达上限仅保留为历史兼容字段，不再参与调度决策。

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS max_resident_users INT;

UPDATE accounts
SET max_resident_users = max_contact_users
WHERE max_resident_users IS NULL AND max_contact_users IS NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'accounts_affinity_resident_users_check'
    ) THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_affinity_resident_users_check
            CHECK (max_resident_users IS NULL OR max_resident_users BETWEEN 1 AND 10000);
    END IF;
END $$;

-- 为已持久化的全局配置补充居民容量键，同时保留旧键供滚动升级中的旧实例读取。
DO $$
BEGIN
    UPDATE settings
    SET value = (
        value::jsonb ||
        CASE
            WHEN value::jsonb ? 'default_max_resident_users' THEN '{}'::jsonb
            ELSE jsonb_build_object(
                'default_max_resident_users',
                COALESCE(value::jsonb -> 'default_max_contact_users', '10'::jsonb)
            )
        END
    )::text
    WHERE key = 'openai_user_affinity_scheduling';
EXCEPTION WHEN invalid_text_representation THEN
    NULL;
END $$;

CREATE INDEX IF NOT EXISTS idx_openai_user_resident_slots_account_capacity
    ON openai_user_resident_slots(account_id, user_id, expires_at)
    WHERE status IN ('provisional', 'active', 'replacement_pending', 'draining');

CREATE INDEX IF NOT EXISTS idx_user_account_placements_account_capacity
    ON user_account_placements(account_id, user_id, expires_at)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_account_user_contacts_last_touched
    ON account_user_contacts(account_id, last_touched_at)
    WHERE last_touched_at IS NOT NULL;
