CREATE TABLE IF NOT EXISTS recharge_bonus_campaigns (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    start_at TIMESTAMPTZ NOT NULL,
    end_at TIMESTAMPTZ NOT NULL,
    participation_limit INTEGER NOT NULL DEFAULT 0,
    tiers JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT recharge_bonus_campaigns_time_check CHECK (end_at > start_at),
    CONSTRAINT recharge_bonus_campaigns_participation_limit_check CHECK (participation_limit >= 0)
);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'recharge_bonus_campaigns_no_overlap'
          AND conrelid = 'recharge_bonus_campaigns'::regclass
    ) THEN
        ALTER TABLE recharge_bonus_campaigns
            ADD CONSTRAINT recharge_bonus_campaigns_no_overlap
            EXCLUDE USING gist (tstzrange(start_at, end_at, '[)') WITH &&);
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS idx_recharge_bonus_campaigns_time
    ON recharge_bonus_campaigns(start_at, end_at);

CREATE TABLE IF NOT EXISTS recharge_bonus_participations (
    id BIGSERIAL PRIMARY KEY,
    campaign_id BIGINT NOT NULL REFERENCES recharge_bonus_campaigns(id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    completed_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT recharge_bonus_participations_count_check CHECK (completed_count >= 0),
    CONSTRAINT recharge_bonus_participations_campaign_user_uniq UNIQUE (campaign_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_recharge_bonus_participations_user
    ON recharge_bonus_participations(user_id);

ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS recharge_bonus_campaign_id BIGINT NULL REFERENCES recharge_bonus_campaigns(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS recharge_bonus_campaign_name VARCHAR(100) NULL,
    ADD COLUMN IF NOT EXISTS recharge_bonus_rate DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS recharge_bonus_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS recharge_bonus_status VARCHAR(20) NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS recharge_bonus_expires_at TIMESTAMPTZ NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'payment_orders_recharge_bonus_status_check'
          AND conrelid = 'payment_orders'::regclass
    ) THEN
        ALTER TABLE payment_orders
            ADD CONSTRAINT payment_orders_recharge_bonus_status_check
            CHECK (recharge_bonus_status IN ('none', 'eligible', 'granted', 'limit_reached'));
    END IF;
END
$$;

CREATE INDEX IF NOT EXISTS idx_payment_orders_recharge_bonus_campaign_user
    ON payment_orders(recharge_bonus_campaign_id, user_id)
    WHERE recharge_bonus_campaign_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_limited_credit_grants_recharge_bonus_order
    ON user_limited_credit_grants(source_type, source_id)
    WHERE source_type = 'recharge_bonus' AND source_id IS NOT NULL;
