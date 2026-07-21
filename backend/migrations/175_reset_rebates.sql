CREATE TABLE IF NOT EXISTS reset_rebate_batches (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL,
    group_name VARCHAR(100) NOT NULL,
    admin_id BIGINT NOT NULL,
    admin_email VARCHAR(255) NOT NULL DEFAULT '',
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'running',
    progress_total INTEGER NOT NULL DEFAULT 0,
    progress_completed INTEGER NOT NULL DEFAULT 0,
    progress_succeeded INTEGER NOT NULL DEFAULT 0,
    progress_failed INTEGER NOT NULL DEFAULT 0,
    participant_count INTEGER NOT NULL DEFAULT 0,
    actual_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    refundable_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    weekly_usage_percent DECIMAL(12,8) NOT NULL DEFAULT 0,
    refundable_percent DECIMAL(12,8) NOT NULL DEFAULT 0,
    suggested_ratio INTEGER NOT NULL DEFAULT 0,
    configured_ratio INTEGER NULL,
    issued_user_count INTEGER NOT NULL DEFAULT 0,
    excluded_user_count INTEGER NOT NULL DEFAULT 0,
    issued_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    failure_code VARCHAR(64) NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    execution_attempts INTEGER NOT NULL DEFAULT 0,
    completed_at TIMESTAMPTZ NULL,
    snapshot_expires_at TIMESTAMPTZ NULL,
    issued_at TIMESTAMPTZ NULL,
    executed_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT reset_rebate_period_check CHECK (period_end > period_start),
    CONSTRAINT reset_rebate_status_check CHECK (status IN ('running','ready','incomplete','not_eligible','expired','executed','failed')),
    CONSTRAINT reset_rebate_ratio_check CHECK (configured_ratio IS NULL OR configured_ratio BETWEEN 1 AND 80)
);

CREATE INDEX IF NOT EXISTS idx_reset_rebate_batches_group_period ON reset_rebate_batches(group_id, period_start, period_end);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_batches_admin_created ON reset_rebate_batches(admin_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_batches_status_created ON reset_rebate_batches(status, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_reset_rebate_running_dedupe
    ON reset_rebate_batches(admin_id, group_id, period_start, period_end) WHERE status = 'running';

CREATE TABLE IF NOT EXISTS reset_rebate_account_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES reset_rebate_batches(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL,
    account_name VARCHAR(100) NOT NULL DEFAULT '',
    platform VARCHAR(50) NOT NULL DEFAULT '',
    account_type VARCHAR(20) NOT NULL DEFAULT '',
    is_shadow BOOLEAN NOT NULL DEFAULT FALSE,
    in_group BOOLEAN NOT NULL DEFAULT FALSE,
    schedulable BOOLEAN NOT NULL DEFAULT FALSE,
    consumed_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    available_count INTEGER NULL,
    weekly_used_percent DECIMAL(12,8) NULL,
    weekly_window_seconds BIGINT NULL,
    included BOOLEAN NOT NULL DEFAULT FALSE,
    exclusion_reason VARCHAR(100) NOT NULL DEFAULT '',
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    error_message VARCHAR(240) NOT NULL DEFAULT '',
    fetched_at TIMESTAMPTZ NULL,
    CONSTRAINT reset_rebate_account_batch_uniq UNIQUE(batch_id, account_id)
);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_account_batch_included ON reset_rebate_account_items(batch_id, included);

CREATE TABLE IF NOT EXISTS reset_rebate_user_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES reset_rebate_batches(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL,
    email VARCHAR(255) NOT NULL DEFAULT '',
    username VARCHAR(100) NOT NULL DEFAULT '',
    user_status VARCHAR(20) NOT NULL DEFAULT '',
    user_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    actual_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    rebate_ratio INTEGER NULL,
    rebate_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    issued BOOLEAN NOT NULL DEFAULT FALSE,
    exclusion_reason VARCHAR(100) NOT NULL DEFAULT '',
    grant_id BIGINT NULL REFERENCES user_limited_credit_grants(id) ON DELETE RESTRICT,
    expires_at TIMESTAMPTZ NULL,
    CONSTRAINT reset_rebate_user_batch_uniq UNIQUE(batch_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_user_batch_amount ON reset_rebate_user_items(batch_id, rebate_amount DESC);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_user_batch_email ON reset_rebate_user_items(batch_id, email);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_user_batch_username ON reset_rebate_user_items(batch_id, username);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_limited_credit_grants_reset_rebate_user
    ON user_limited_credit_grants(source_type, source_id, user_id)
    WHERE source_type = 'reset_rebate' AND source_id IS NOT NULL;
