CREATE TABLE IF NOT EXISTS user_limited_credit_grants (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source_type VARCHAR(32) NOT NULL DEFAULT 'redeem_code',
    source_id BIGINT NULL,
    initial_amount DECIMAL(20,8) NOT NULL CHECK (initial_amount > 0),
    used_amount DECIMAL(20,8) NOT NULL DEFAULT 0 CHECK (used_amount >= 0),
    frozen_amount DECIMAL(20,8) NOT NULL DEFAULT 0 CHECK (frozen_amount >= 0),
    expires_at TIMESTAMPTZ NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    notes TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT user_limited_credit_grants_amount_check
        CHECK (used_amount + frozen_amount <= initial_amount + 0.00000001)
);

CREATE INDEX IF NOT EXISTS idx_user_limited_credit_grants_user_status_expires
    ON user_limited_credit_grants(user_id, status, expires_at, id);

CREATE INDEX IF NOT EXISTS idx_user_limited_credit_grants_source
    ON user_limited_credit_grants(source_type, source_id);

CREATE TABLE IF NOT EXISTS user_limited_credit_ledger (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    grant_id BIGINT NOT NULL REFERENCES user_limited_credit_grants(id) ON DELETE CASCADE,
    event_type VARCHAR(32) NOT NULL,
    amount DECIMAL(20,8) NOT NULL CHECK (amount >= 0),
    request_id VARCHAR(128) NULL,
    api_key_id BIGINT NULL REFERENCES api_keys(id) ON DELETE SET NULL,
    batch_id VARCHAR(128) NULL,
    usage_log_id BIGINT NULL REFERENCES usage_logs(id) ON DELETE SET NULL,
    notes TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_limited_credit_ledger_user_created
    ON user_limited_credit_ledger(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_user_limited_credit_ledger_grant_event
    ON user_limited_credit_ledger(grant_id, event_type);

CREATE INDEX IF NOT EXISTS idx_user_limited_credit_ledger_batch
    ON user_limited_credit_ledger(batch_id)
    WHERE batch_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_user_limited_credit_ledger_request_key
    ON user_limited_credit_ledger(request_id, api_key_id)
    WHERE request_id IS NOT NULL;