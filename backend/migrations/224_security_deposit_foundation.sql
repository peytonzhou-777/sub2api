-- 网络安全保证金第一阶段：双资金桶、来源批次、不可变流水与风险/协议审计基础。
-- 本迁移只建立数据能力，不开启准入、处罚或退款执法。

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS security_deposit_base_required_cents BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS security_deposit_policy_version VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS security_locked_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS security_lock_violation_id BIGINT,
    ADD COLUMN IF NOT EXISTS security_lock_reason VARCHAR(64),
    ADD COLUMN IF NOT EXISTS disabled_reason VARCHAR(64),
    ADD COLUMN IF NOT EXISTS disabled_financial_event_type VARCHAR(32),
    ADD COLUMN IF NOT EXISTS disabled_financial_event_id BIGINT,
    ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'groups_security_deposit_base_required_nonnegative'
    ) THEN
        ALTER TABLE groups
            ADD CONSTRAINT groups_security_deposit_base_required_nonnegative
            CHECK (security_deposit_base_required_cents >= 0);
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS security_deposit_accounts (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    bucket_type VARCHAR(20) NOT NULL,
    currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
    balance_cents BIGINT NOT NULL DEFAULT 0,
    refund_reserved_cents BIGINT NOT NULL DEFAULT 0,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_accounts_user_bucket_unique UNIQUE (user_id, bucket_type),
    CONSTRAINT security_deposit_accounts_bucket_type_check CHECK (bucket_type IN ('paid', 'admin_grant')),
    CONSTRAINT security_deposit_accounts_currency_check CHECK (currency = 'CNY'),
    CONSTRAINT security_deposit_accounts_amounts_check CHECK (
        balance_cents >= 0
        AND refund_reserved_cents >= 0
        AND refund_reserved_cents <= balance_cents
        AND version >= 1
    ),
    CONSTRAINT security_deposit_accounts_admin_reserved_check CHECK (
        bucket_type <> 'admin_grant' OR refund_reserved_cents = 0
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_accounts_user
    ON security_deposit_accounts (user_id);

CREATE TABLE IF NOT EXISTS security_deposit_risk_profiles (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL UNIQUE REFERENCES users(id),
    cyber_strike_count BIGINT NOT NULL DEFAULT 0,
    risk_multiplier BIGINT NOT NULL DEFAULT 1,
    last_violation_id BIGINT,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_risk_profiles_values_check CHECK (
        cyber_strike_count >= 0 AND risk_multiplier >= 1 AND version >= 1
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_risk_profiles_last_violation
    ON security_deposit_risk_profiles (last_violation_id);

CREATE TABLE IF NOT EXISTS security_deposit_lots (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    bucket_type VARCHAR(20) NOT NULL,
    source_type VARCHAR(20) NOT NULL,
    payment_order_id BIGINT REFERENCES payment_orders(id),
    original_cents BIGINT NOT NULL,
    remaining_cents BIGINT NOT NULL,
    refund_reserved_cents BIGINT NOT NULL DEFAULT 0,
    forfeited_cents BIGINT NOT NULL DEFAULT 0,
    refunded_cents BIGINT NOT NULL DEFAULT 0,
    admin_deducted_cents BIGINT NOT NULL DEFAULT 0,
    revoked_cents BIGINT NOT NULL DEFAULT 0,
    currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
    locked_until TIMESTAMPTZ,
    refund_policy VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    source_reference VARCHAR(191),
    notes TEXT,
    created_by BIGINT REFERENCES users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_lots_bucket_type_check CHECK (bucket_type IN ('paid', 'admin_grant')),
    CONSTRAINT security_deposit_lots_source_type_check CHECK (source_type IN ('payment', 'admin', 'compensation')),
    CONSTRAINT security_deposit_lots_refund_policy_check CHECK (refund_policy IN ('timed_original_channel', 'never')),
    CONSTRAINT security_deposit_lots_currency_check CHECK (currency = 'CNY'),
    CONSTRAINT security_deposit_lots_amounts_check CHECK (
        original_cents > 0
        AND remaining_cents >= 0
        AND refund_reserved_cents >= 0
        AND forfeited_cents >= 0
        AND refunded_cents >= 0
        AND admin_deducted_cents >= 0
        AND revoked_cents >= 0
        AND remaining_cents + forfeited_cents + refunded_cents + admin_deducted_cents + revoked_cents = original_cents
        AND refund_reserved_cents <= remaining_cents
    ),
    CONSTRAINT security_deposit_lots_source_bucket_check CHECK (
        (
            source_type = 'payment'
            AND bucket_type = 'paid'
            AND payment_order_id IS NOT NULL
            AND locked_until IS NOT NULL
            AND refund_policy = 'timed_original_channel'
            AND admin_deducted_cents = 0
            AND revoked_cents = 0
        )
        OR
        (
            source_type IN ('admin', 'compensation')
            AND bucket_type = 'admin_grant'
            AND payment_order_id IS NULL
            AND locked_until IS NULL
            AND refund_policy = 'never'
            AND refunded_cents = 0
            AND refund_reserved_cents = 0
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_security_deposit_lots_payment_order
    ON security_deposit_lots (payment_order_id)
    WHERE payment_order_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_security_deposit_lots_user_bucket_created
    ON security_deposit_lots (user_id, bucket_type, created_at, id);
CREATE INDEX IF NOT EXISTS idx_security_deposit_lots_user_status
    ON security_deposit_lots (user_id, status);

CREATE TABLE IF NOT EXISTS security_deposit_violations (
    id BIGSERIAL PRIMARY KEY,
    event_key VARCHAR(191) NOT NULL UNIQUE,
    request_id VARCHAR(191) NOT NULL,
    upstream_response_id VARCHAR(191),
    turn_index BIGINT,
    user_id BIGINT NOT NULL REFERENCES users(id),
    api_key_id BIGINT NOT NULL REFERENCES api_keys(id),
    group_id BIGINT NOT NULL REFERENCES groups(id),
    policy_code VARCHAR(64) NOT NULL,
    detector_version VARCHAR(64) NOT NULL,
    base_required_snapshot_cents BIGINT NOT NULL,
    risk_multiplier_before BIGINT NOT NULL,
    required_snapshot_cents BIGINT NOT NULL,
    risk_multiplier_after BIGINT NOT NULL,
    forfeited_cents BIGINT NOT NULL DEFAULT 0,
    shortfall_cents BIGINT NOT NULL DEFAULT 0,
    state VARCHAR(32) NOT NULL,
    error_code VARCHAR(64),
    retry_count INTEGER NOT NULL DEFAULT 0,
    api_key_name_snapshot VARCHAR(100) NOT NULL,
    group_name_snapshot VARCHAR(100) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    CONSTRAINT security_deposit_violations_values_check CHECK (
        (turn_index IS NULL OR turn_index >= 0)
        AND base_required_snapshot_cents >= 0
        AND risk_multiplier_before >= 1
        AND required_snapshot_cents >= 0
        AND risk_multiplier_after >= 1
        AND forfeited_cents >= 0
        AND shortfall_cents >= 0
        AND retry_count >= 0
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_violations_user_created
    ON security_deposit_violations (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_deposit_violations_api_key_created
    ON security_deposit_violations (api_key_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_deposit_violations_state_created
    ON security_deposit_violations (state, created_at);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'security_deposit_risk_profiles_last_violation_fk'
    ) THEN
        ALTER TABLE security_deposit_risk_profiles
            ADD CONSTRAINT security_deposit_risk_profiles_last_violation_fk
            FOREIGN KEY (last_violation_id) REFERENCES security_deposit_violations(id);
    END IF;
END
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'api_keys_security_lock_violation_fk'
    ) THEN
        ALTER TABLE api_keys
            ADD CONSTRAINT api_keys_security_lock_violation_fk
            FOREIGN KEY (security_lock_violation_id) REFERENCES security_deposit_violations(id);
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS security_deposit_risk_events (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    event_type VARCHAR(32) NOT NULL,
    violation_id BIGINT REFERENCES security_deposit_violations(id),
    strike_count_before BIGINT NOT NULL,
    strike_count_after BIGINT NOT NULL,
    multiplier_before BIGINT NOT NULL,
    multiplier_after BIGINT NOT NULL,
    operator_id BIGINT REFERENCES users(id),
    reason TEXT,
    idempotency_key VARCHAR(191) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_risk_events_type_check CHECK (event_type IN ('cyber_escalation', 'admin_adjustment')),
    CONSTRAINT security_deposit_risk_events_values_check CHECK (
        strike_count_before >= 0
        AND strike_count_after >= 0
        AND multiplier_before >= 1
        AND multiplier_after >= 1
    ),
    CONSTRAINT security_deposit_risk_events_shape_check CHECK (
        (event_type = 'cyber_escalation' AND violation_id IS NOT NULL)
        OR event_type = 'admin_adjustment'
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_security_deposit_risk_events_violation
    ON security_deposit_risk_events (violation_id)
    WHERE violation_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_security_deposit_risk_events_user_created
    ON security_deposit_risk_events (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS security_deposit_refunds (
    id BIGSERIAL PRIMARY KEY,
    refund_id VARCHAR(64) NOT NULL UNIQUE,
    user_id BIGINT NOT NULL REFERENCES users(id),
    lot_id BIGINT NOT NULL REFERENCES security_deposit_lots(id),
    payment_order_id BIGINT NOT NULL REFERENCES payment_orders(id),
    principal_cents BIGINT NOT NULL,
    gateway_amount VARCHAR(64) NOT NULL,
    gateway_currency VARCHAR(3) NOT NULL DEFAULT 'CNY',
    mode VARCHAR(40) NOT NULL,
    state VARCHAR(32) NOT NULL,
    requested_by BIGINT REFERENCES users(id),
    reason TEXT,
    quote_hash VARCHAR(128) NOT NULL,
    idempotency_key VARCHAR(191) NOT NULL UNIQUE,
    provider_request_id VARCHAR(191),
    provider_response_snapshot JSONB,
    external_refund_id VARCHAR(191),
    external_refunded_at TIMESTAMPTZ,
    external_evidence JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    submitted_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    CONSTRAINT security_deposit_refunds_principal_check CHECK (principal_cents > 0),
    CONSTRAINT security_deposit_refunds_currency_check CHECK (gateway_currency = 'CNY'),
    CONSTRAINT security_deposit_refunds_mode_check CHECK (mode IN ('automatic_original_channel', 'manual_external')),
    CONSTRAINT security_deposit_refunds_external_shape_check CHECK (
        mode <> 'automatic_original_channel'
        OR (external_refund_id IS NULL AND external_refunded_at IS NULL AND external_evidence IS NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_security_deposit_refunds_external_id
    ON security_deposit_refunds (external_refund_id)
    WHERE external_refund_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_security_deposit_refunds_user_created
    ON security_deposit_refunds (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_security_deposit_refunds_lot_state
    ON security_deposit_refunds (lot_id, state);
CREATE INDEX IF NOT EXISTS idx_security_deposit_refunds_payment_order
    ON security_deposit_refunds (payment_order_id);

CREATE TABLE IF NOT EXISTS security_deposit_ledger (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    lot_id BIGINT NOT NULL REFERENCES security_deposit_lots(id),
    bucket_type VARCHAR(20) NOT NULL,
    entry_type VARCHAR(32) NOT NULL,
    delta_cents BIGINT NOT NULL DEFAULT 0,
    reserved_delta_cents BIGINT NOT NULL DEFAULT 0,
    bucket_balance_after_cents BIGINT NOT NULL,
    bucket_reserved_after_cents BIGINT NOT NULL,
    group_id BIGINT REFERENCES groups(id),
    api_key_id BIGINT REFERENCES api_keys(id),
    violation_id BIGINT REFERENCES security_deposit_violations(id),
    refund_id BIGINT REFERENCES security_deposit_refunds(id),
    payment_order_id BIGINT REFERENCES payment_orders(id),
    operator_id BIGINT REFERENCES users(id),
    reason TEXT,
    idempotency_key VARCHAR(191) NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_ledger_bucket_type_check CHECK (bucket_type IN ('paid', 'admin_grant')),
    CONSTRAINT security_deposit_ledger_entry_type_check CHECK (
        entry_type IN ('payment_credit', 'admin_add', 'compensation', 'forfeit', 'refund_reserve', 'refund_release', 'refund_success', 'admin_deduct', 'admin_revoke')
    ),
    CONSTRAINT security_deposit_ledger_balances_check CHECK (
        bucket_balance_after_cents >= 0
        AND bucket_reserved_after_cents >= 0
        AND bucket_reserved_after_cents <= bucket_balance_after_cents
    ),
    CONSTRAINT security_deposit_ledger_bucket_operation_check CHECK (
        (bucket_type = 'paid' AND entry_type NOT IN ('admin_add', 'compensation', 'admin_deduct', 'admin_revoke'))
        OR
        (bucket_type = 'admin_grant' AND entry_type NOT IN ('payment_credit', 'refund_reserve', 'refund_release', 'refund_success'))
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_ledger_user_created
    ON security_deposit_ledger (user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_security_deposit_ledger_lot_created
    ON security_deposit_ledger (lot_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_security_deposit_ledger_violation
    ON security_deposit_ledger (violation_id);
CREATE INDEX IF NOT EXISTS idx_security_deposit_ledger_refund
    ON security_deposit_ledger (refund_id);

CREATE TABLE IF NOT EXISTS security_deposit_agreements (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    policy_version VARCHAR(64) NOT NULL,
    content_hash VARCHAR(128) NOT NULL,
    group_id BIGINT NOT NULL REFERENCES groups(id),
    base_required_snapshot_cents BIGINT NOT NULL,
    risk_multiplier_snapshot BIGINT NOT NULL,
    required_snapshot_cents BIGINT NOT NULL,
    accepted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    client_ip VARCHAR(64) NOT NULL,
    user_agent TEXT NOT NULL,
    CONSTRAINT security_deposit_agreements_user_group_policy_unique UNIQUE (user_id, group_id, policy_version, content_hash),
    CONSTRAINT security_deposit_agreements_values_check CHECK (
        base_required_snapshot_cents >= 0
        AND risk_multiplier_snapshot >= 1
        AND required_snapshot_cents >= 0
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_agreements_user_accepted
    ON security_deposit_agreements (user_id, accepted_at DESC);

CREATE INDEX IF NOT EXISTS idx_api_keys_security_lock_violation
    ON api_keys (security_lock_violation_id)
    WHERE security_lock_violation_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_api_keys_disabled_financial_event
    ON api_keys (disabled_financial_event_type, disabled_financial_event_id)
    WHERE disabled_financial_event_id IS NOT NULL;

-- 账本、倍率事件和协议证据只允许追加；纠错必须写反向流水或新事件。
CREATE OR REPLACE FUNCTION reject_security_deposit_immutable_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS security_deposit_ledger_append_only ON security_deposit_ledger;
CREATE TRIGGER security_deposit_ledger_append_only
BEFORE UPDATE OR DELETE ON security_deposit_ledger
FOR EACH ROW EXECUTE FUNCTION reject_security_deposit_immutable_mutation();

DROP TRIGGER IF EXISTS security_deposit_risk_events_append_only ON security_deposit_risk_events;
CREATE TRIGGER security_deposit_risk_events_append_only
BEFORE UPDATE OR DELETE ON security_deposit_risk_events
FOR EACH ROW EXECUTE FUNCTION reject_security_deposit_immutable_mutation();

DROP TRIGGER IF EXISTS security_deposit_agreements_append_only ON security_deposit_agreements;
CREATE TRIGGER security_deposit_agreements_append_only
BEFORE UPDATE OR DELETE ON security_deposit_agreements
FOR EACH ROW EXECUTE FUNCTION reject_security_deposit_immutable_mutation();

COMMENT ON TABLE security_deposit_accounts IS '用户保证金双资金桶权威汇总；总保证金只可派生，不单独存储。';
COMMENT ON TABLE security_deposit_lots IS '按支付、管理员发放或补偿来源保存的保证金批次。';
COMMENT ON TABLE security_deposit_ledger IS '按资金桶保存的不可变保证金流水。';
COMMENT ON TABLE security_deposit_risk_profiles IS '用户官方网安处罚次数与当前保证金风险倍率。';
COMMENT ON TABLE security_deposit_risk_events IS '保证金风险倍率变化的不可变审计事件。';
COMMENT ON TABLE security_deposit_violations IS '可信官方网安策略事件的脱敏处罚事实。';
COMMENT ON TABLE security_deposit_refunds IS '保证金自动原路退款和人工外部退款状态。';
COMMENT ON TABLE security_deposit_agreements IS '用户接受版本化保证金规则的服务端证据。';
