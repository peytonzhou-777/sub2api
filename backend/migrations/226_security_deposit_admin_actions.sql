-- 网络安全保证金第六阶段：管理员资金操作与密钥解锁的幂等审计事实。

CREATE TABLE IF NOT EXISTS security_deposit_admin_actions (
    id BIGSERIAL PRIMARY KEY,
    action_key VARCHAR(191) NOT NULL UNIQUE,
    action_type VARCHAR(32) NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id),
    lot_id BIGINT REFERENCES security_deposit_lots(id),
    api_key_id BIGINT REFERENCES api_keys(id),
    amount_cents BIGINT NOT NULL DEFAULT 0,
    operator_id BIGINT NOT NULL REFERENCES users(id),
    reason TEXT,
    result_snapshot JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_admin_actions_type_check CHECK (
        action_type IN ('admin_add', 'compensation', 'admin_deduct', 'admin_revoke', 'key_unlock')
    ),
    CONSTRAINT security_deposit_admin_actions_amount_check CHECK (amount_cents >= 0),
    CONSTRAINT security_deposit_admin_actions_shape_check CHECK (
        (action_type IN ('admin_add', 'compensation') AND lot_id IS NOT NULL AND api_key_id IS NULL AND amount_cents > 0)
        OR (action_type = 'admin_deduct' AND lot_id IS NULL AND api_key_id IS NULL AND amount_cents > 0)
        OR (action_type = 'admin_revoke' AND lot_id IS NOT NULL AND api_key_id IS NULL AND amount_cents > 0)
        OR (action_type = 'key_unlock' AND lot_id IS NULL AND api_key_id IS NOT NULL AND amount_cents = 0)
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_admin_actions_user_created
    ON security_deposit_admin_actions (user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_security_deposit_admin_actions_operator_created
    ON security_deposit_admin_actions (operator_id, created_at DESC, id DESC);

DROP TRIGGER IF EXISTS security_deposit_admin_actions_append_only ON security_deposit_admin_actions;
CREATE TRIGGER security_deposit_admin_actions_append_only
BEFORE UPDATE OR DELETE ON security_deposit_admin_actions
FOR EACH ROW EXECUTE FUNCTION reject_security_deposit_immutable_mutation();

COMMENT ON TABLE security_deposit_admin_actions IS '管理员保证金发放、扣除、撤销和密钥解锁的不可变幂等审计事实。';
