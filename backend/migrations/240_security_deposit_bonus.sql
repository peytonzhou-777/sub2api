-- 保证金赠额：单份滚动限时额度、每日批次快照与资金减少后的撤销审计。

ALTER TABLE user_limited_credit_grants
    ADD COLUMN IF NOT EXISTS security_deposit_bonus_pending_revoke_amount DECIMAL(20, 8) NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'user_limited_credit_grants_security_deposit_bonus_pending_check'
    ) THEN
        ALTER TABLE user_limited_credit_grants
            ADD CONSTRAINT user_limited_credit_grants_security_deposit_bonus_pending_check
            CHECK (
                security_deposit_bonus_pending_revoke_amount >= 0
                AND security_deposit_bonus_pending_revoke_amount <= frozen_amount + 0.00000001
            );
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_limited_credit_grants_security_deposit_bonus_user
    ON user_limited_credit_grants (user_id)
    WHERE source_type = 'security_deposit_bonus';

CREATE TABLE IF NOT EXISTS security_deposit_bonus_batches (
    id BIGSERIAL PRIMARY KEY,
    business_date DATE NOT NULL UNIQUE,
    scheduled_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    daily_amount DECIMAL(20, 8) NOT NULL,
    cap_ratio DECIMAL(20, 8) NOT NULL,
    enforcement_enabled BOOLEAN NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'running',
    eligible_user_count INT NOT NULL DEFAULT 0,
    issued_user_count INT NOT NULL DEFAULT 0,
    renewed_user_count INT NOT NULL DEFAULT 0,
    failed_user_count INT NOT NULL DEFAULT 0,
    issued_amount DECIMAL(20, 8) NOT NULL DEFAULT 0,
    failure_message TEXT NOT NULL DEFAULT '',
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_bonus_batches_values_check CHECK (
        daily_amount >= 0
        AND cap_ratio >= 0
        AND eligible_user_count >= 0
        AND issued_user_count >= 0
        AND renewed_user_count >= 0
        AND failed_user_count >= 0
        AND issued_amount >= 0
        AND status IN ('running', 'succeeded', 'skipped', 'failed')
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_bonus_batches_status_date
    ON security_deposit_bonus_batches (status, business_date);

CREATE TABLE IF NOT EXISTS security_deposit_bonus_batch_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES security_deposit_bonus_batches(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    effective_balance_cents BIGINT NOT NULL,
    risk_multiplier BIGINT NOT NULL,
    qualifying_group_id BIGINT NOT NULL REFERENCES groups(id),
    qualifying_group_name VARCHAR(100) NOT NULL,
    required_cents BIGINT NOT NULL,
    cap_amount DECIMAL(20, 8) NOT NULL,
    result VARCHAR(20) NOT NULL DEFAULT 'pending',
    attempt_count INT NOT NULL DEFAULT 0,
    grant_id BIGINT REFERENCES user_limited_credit_grants(id) ON DELETE SET NULL,
    amount_before DECIMAL(20, 8) NOT NULL DEFAULT 0,
    granted_amount DECIMAL(20, 8) NOT NULL DEFAULT 0,
    amount_after DECIMAL(20, 8) NOT NULL DEFAULT 0,
    failure_message TEXT NOT NULL DEFAULT '',
    processing_started_at TIMESTAMPTZ,
    processed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_bonus_batch_items_unique UNIQUE (batch_id, user_id),
    CONSTRAINT security_deposit_bonus_batch_items_values_check CHECK (
        effective_balance_cents >= 0
        AND risk_multiplier >= 1
        AND required_cents > 0
        AND cap_amount >= 0
        AND attempt_count >= 0
        AND amount_before >= 0
        AND granted_amount >= 0
        AND amount_after >= 0
        AND result IN ('pending', 'processing', 'issued', 'renewed', 'skipped', 'failed')
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_bonus_batch_items_claim
    ON security_deposit_bonus_batch_items (batch_id, result, id);

CREATE TABLE IF NOT EXISTS security_deposit_bonus_reconciliations (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type VARCHAR(32) NOT NULL,
    event_id BIGINT NOT NULL,
    target_amount DECIMAL(20, 8) NOT NULL,
    revoked_amount DECIMAL(20, 8) NOT NULL DEFAULT 0,
    pending_revoke_amount DECIMAL(20, 8) NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT security_deposit_bonus_reconciliations_event_unique UNIQUE (user_id, event_type, event_id),
    CONSTRAINT security_deposit_bonus_reconciliations_values_check CHECK (
        target_amount >= 0
        AND revoked_amount >= 0
        AND pending_revoke_amount >= 0
    )
);

CREATE INDEX IF NOT EXISTS idx_security_deposit_bonus_reconciliations_user_created
    ON security_deposit_bonus_reconciliations (user_id, created_at DESC);
