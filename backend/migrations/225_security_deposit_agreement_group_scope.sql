-- 第二阶段：保证金协议接受证据按用户与目标分组隔离。
-- 同一用户可以对多个分组接受相同版本和内容的协议，各分组保留独立门槛快照。

ALTER TABLE security_deposit_agreements
    DROP CONSTRAINT IF EXISTS security_deposit_agreements_user_policy_unique;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'security_deposit_agreements_user_group_policy_unique'
    ) THEN
        ALTER TABLE security_deposit_agreements
            ADD CONSTRAINT security_deposit_agreements_user_group_policy_unique
            UNIQUE (user_id, group_id, policy_version, content_hash);
    END IF;
END
$$;
