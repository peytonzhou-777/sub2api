CREATE TABLE IF NOT EXISTS credit_grant_events (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    credit_type VARCHAR(16) NOT NULL,
    amount DECIMAL(20,8) NOT NULL,
    validity_days INTEGER NULL,
    deleted_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT credit_grant_events_name_check CHECK (BTRIM(name) <> ''),
    CONSTRAINT credit_grant_events_type_check CHECK (credit_type IN ('permanent', 'limited')),
    CONSTRAINT credit_grant_events_amount_check CHECK (amount > 0 AND amount < 1000000000000),
    CONSTRAINT credit_grant_events_validity_check CHECK (
        (credit_type = 'permanent' AND validity_days IS NULL) OR
        (credit_type = 'limited' AND validity_days BETWEEN 1 AND 36500)
    )
);

CREATE INDEX IF NOT EXISTS idx_credit_grant_events_created_at
    ON credit_grant_events(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_credit_grant_events_deleted_at
    ON credit_grant_events(deleted_at);

CREATE TABLE IF NOT EXISTS user_credit_grant_event_triggers (
    id BIGSERIAL PRIMARY KEY,
    event_id BIGINT NOT NULL REFERENCES credit_grant_events(id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credit_type_snapshot VARCHAR(16) NOT NULL,
    amount_snapshot DECIMAL(20,8) NOT NULL,
    validity_days_snapshot INTEGER NULL,
    expires_at TIMESTAMPTZ NULL,
    balance_history_id BIGINT NULL REFERENCES redeem_codes(id) ON DELETE SET NULL,
    limited_credit_grant_id BIGINT NULL REFERENCES user_limited_credit_grants(id) ON DELETE SET NULL,
    triggered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT user_credit_grant_event_triggers_type_check
        CHECK (credit_type_snapshot IN ('permanent', 'limited')),
    CONSTRAINT user_credit_grant_event_triggers_amount_check
        CHECK (amount_snapshot > 0 AND amount_snapshot < 1000000000000),
    CONSTRAINT user_credit_grant_event_triggers_snapshot_check CHECK (
        (credit_type_snapshot = 'permanent' AND validity_days_snapshot IS NULL AND expires_at IS NULL) OR
        (credit_type_snapshot = 'limited' AND validity_days_snapshot BETWEEN 1 AND 36500 AND expires_at IS NOT NULL)
    ),
    CONSTRAINT user_credit_grant_event_triggers_event_user_uniq UNIQUE(event_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_user_credit_grant_event_triggers_user_time
    ON user_credit_grant_event_triggers(user_id, triggered_at DESC);

CREATE INDEX IF NOT EXISTS idx_user_credit_grant_event_triggers_event_time
    ON user_credit_grant_event_triggers(event_id, triggered_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_limited_credit_grants_credit_event_user
    ON user_limited_credit_grants(source_type, source_id, user_id)
    WHERE source_type = 'credit_grant_event' AND source_id IS NOT NULL;
