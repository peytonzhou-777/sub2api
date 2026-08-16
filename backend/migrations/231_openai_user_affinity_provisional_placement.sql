-- 为用户粘性归属增加请求级暂存标识，成功前允许按 token 原子回滚。
ALTER TABLE user_account_placements
    ADD COLUMN IF NOT EXISTS provisional_token VARCHAR(100);

CREATE INDEX IF NOT EXISTS idx_user_account_placements_provisional_token
    ON user_account_placements(provisional_token)
    WHERE provisional_token IS NOT NULL;
