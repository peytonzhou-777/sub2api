-- 用户级重置返额排除计次：官方 Cyber 告警递增，返额批次完成后消费一次。
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS reset_rebate_skip_count BIGINT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'users_reset_rebate_skip_count_nonnegative'
    ) THEN
        ALTER TABLE users
            ADD CONSTRAINT users_reset_rebate_skip_count_nonnegative
            CHECK (reset_rebate_skip_count >= 0);
    END IF;
END
$$;

ALTER TABLE reset_rebate_user_items
    ADD COLUMN IF NOT EXISTS skip_count_consumed BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN users.reset_rebate_skip_count IS '官方 Cyber 告警后不参与重置返额的剩余计次';
COMMENT ON COLUMN reset_rebate_user_items.skip_count_consumed IS '该用户排除计次是否已在本批次终态消费';
