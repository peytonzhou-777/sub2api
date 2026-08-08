-- 重置返利 v2：账号独立窗口、高精度统计比例和逐用户失败重试。
CREATE TABLE IF NOT EXISTS account_usage_window_histories (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    window_kind VARCHAR(32) NOT NULL DEFAULT 'codex_7d',
    window_started_at TIMESTAMPTZ NOT NULL,
    first_observed_at TIMESTAMPTZ NOT NULL,
    last_observed_at TIMESTAMPTZ NOT NULL,
    source_type VARCHAR(32) NOT NULL DEFAULT 'usage_refresh',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT account_usage_window_histories_unique
        UNIQUE(account_id, window_kind, window_started_at)
);

CREATE INDEX IF NOT EXISTS idx_account_usage_window_histories_latest
    ON account_usage_window_histories(account_id, window_kind, window_started_at DESC);

CREATE TABLE IF NOT EXISTS reset_rebate_batches (
    id BIGSERIAL PRIMARY KEY,
    mechanism_version INTEGER NOT NULL DEFAULT 2,
    group_id BIGINT NULL,
    group_name VARCHAR(100) NOT NULL DEFAULT '',
    admin_id BIGINT NOT NULL,
    admin_email VARCHAR(255) NOT NULL DEFAULT '',
    period_start TIMESTAMPTZ NULL,
    period_end TIMESTAMPTZ NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'running',
    failure_stage VARCHAR(20) NOT NULL DEFAULT '',
    force_stat_ratio_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    force_stat_ratio DECIMAL(11,8) NOT NULL DEFAULT 100,
    account_count INTEGER NOT NULL DEFAULT 0,
    risk_account_count INTEGER NOT NULL DEFAULT 0,
    progress_total INTEGER NOT NULL DEFAULT 0,
    progress_completed INTEGER NOT NULL DEFAULT 0,
    raw_amount DECIMAL(30,16) NOT NULL DEFAULT 0,
    weighted_amount DECIMAL(30,16) NOT NULL DEFAULT 0,
    expected_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    successful_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    failed_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    excluded_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    payout_ratio INTEGER NULL,
    rebate_reason VARCHAR(100) NOT NULL DEFAULT '',
    preview_version INTEGER NOT NULL DEFAULT 0,
    expected_user_count INTEGER NOT NULL DEFAULT 0,
    successful_user_count INTEGER NOT NULL DEFAULT 0,
    excluded_user_count INTEGER NOT NULL DEFAULT 0,
    failed_user_count INTEGER NOT NULL DEFAULT 0,
    failure_code VARCHAR(64) NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    executed_by_admin_id BIGINT NULL,
    executed_by_admin_email VARCHAR(255) NOT NULL DEFAULT '',
    first_executed_at TIMESTAMPTZ NULL,
    last_retry_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 已部署过 v1 时只追加字段，不删除原审计列。
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS mechanism_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS failure_stage VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS force_stat_ratio_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS force_stat_ratio DECIMAL(11,8) NOT NULL DEFAULT 100;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS account_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS risk_account_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS raw_amount DECIMAL(30,16) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS weighted_amount DECIMAL(30,16) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS expected_amount DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS successful_amount DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS failed_amount DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS excluded_amount DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS payout_ratio INTEGER NULL;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS rebate_reason VARCHAR(100) NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS preview_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS expected_user_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS successful_user_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS failed_user_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS executed_by_admin_id BIGINT NULL;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS executed_by_admin_email VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS first_executed_at TIMESTAMPTZ NULL;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS last_retry_at TIMESTAMPTZ NULL;
ALTER TABLE reset_rebate_batches ALTER COLUMN group_id DROP NOT NULL;
ALTER TABLE reset_rebate_batches ALTER COLUMN period_start DROP NOT NULL;
ALTER TABLE reset_rebate_batches ALTER COLUMN period_end DROP NOT NULL;
ALTER TABLE reset_rebate_batches ALTER COLUMN mechanism_version SET DEFAULT 2;
ALTER TABLE reset_rebate_batches DROP CONSTRAINT IF EXISTS reset_rebate_status_check;
ALTER TABLE reset_rebate_batches ADD CONSTRAINT reset_rebate_status_check
    CHECK (status IN ('running','ready','not_eligible','partial','failed','executed','incomplete','expired'));

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'reset_rebate_batches' AND column_name = 'configured_ratio'
    ) THEN
        EXECUTE 'UPDATE reset_rebate_batches SET payout_ratio = configured_ratio WHERE mechanism_version = 1 AND payout_ratio IS NULL';
    END IF;
END $$;

UPDATE reset_rebate_batches
SET status = 'failed',
    failure_stage = 'statistics',
    failure_code = 'LEGACY_MECHANISM_DISABLED',
    failure_message = '旧版未执行快照已失效，请按账号重新创建批次',
    updated_at = NOW()
WHERE mechanism_version = 1
  AND status IN ('running', 'ready', 'incomplete');

CREATE INDEX IF NOT EXISTS idx_reset_rebate_batches_status_created
    ON reset_rebate_batches(status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_batches_version_created
    ON reset_rebate_batches(mechanism_version, created_at DESC);

CREATE TABLE IF NOT EXISTS reset_rebate_account_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES reset_rebate_batches(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL,
    account_name VARCHAR(255) NOT NULL,
    platform VARCHAR(32) NOT NULL,
    account_type VARCHAR(32) NOT NULL,
    is_shadow BOOLEAN NOT NULL DEFAULT FALSE,
    account_status VARCHAR(20) NOT NULL DEFAULT '',
    account_error_message TEXT NOT NULL DEFAULT '',
    schedulable BOOLEAN NOT NULL DEFAULT TRUE,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    default_window_source VARCHAR(32) NOT NULL DEFAULT 'history',
    window_risk VARCHAR(32) NOT NULL DEFAULT '',
    ratio_mode VARCHAR(16) NOT NULL DEFAULT 'auto',
    auto_stat_ratio DECIMAL(11,8) NOT NULL DEFAULT 0,
    manual_stat_ratio DECIMAL(11,8) NULL,
    effective_stat_ratio DECIMAL(11,8) NOT NULL DEFAULT 0,
    raw_amount DECIMAL(30,16) NOT NULL DEFAULT 0,
    weighted_amount DECIMAL(30,16) NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE reset_rebate_account_items ALTER COLUMN account_name TYPE VARCHAR(255);
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS account_status VARCHAR(20) NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS account_error_message TEXT NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS period_start TIMESTAMPTZ NULL;
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS period_end TIMESTAMPTZ NULL;
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS default_window_source VARCHAR(32) NOT NULL DEFAULT 'history';
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS window_risk VARCHAR(32) NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS ratio_mode VARCHAR(16) NOT NULL DEFAULT 'auto';
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS auto_stat_ratio DECIMAL(11,8) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS manual_stat_ratio DECIMAL(11,8) NULL;
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS effective_stat_ratio DECIMAL(11,8) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS raw_amount DECIMAL(30,16) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_account_items ADD COLUMN IF NOT EXISTS weighted_amount DECIMAL(30,16) NOT NULL DEFAULT 0;

-- 旧批次保留只读可查能力；新版服务不使用这些回填值参与统计。
UPDATE reset_rebate_account_items AS item
SET period_start = batch.period_start,
    period_end = batch.period_end
FROM reset_rebate_batches AS batch
WHERE item.batch_id = batch.id
  AND batch.mechanism_version = 1
  AND (item.period_start IS NULL OR item.period_end IS NULL);

CREATE UNIQUE INDEX IF NOT EXISTS idx_reset_rebate_account_items_batch_account
    ON reset_rebate_account_items(batch_id, account_id);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_account_items_account_window
    ON reset_rebate_account_items(account_id, period_start, period_end);

CREATE TABLE IF NOT EXISTS reset_rebate_user_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES reset_rebate_batches(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL,
    email VARCHAR(255) NOT NULL DEFAULT '',
    username VARCHAR(100) NOT NULL DEFAULT '',
    user_status VARCHAR(20) NOT NULL DEFAULT '',
    user_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    raw_amount DECIMAL(30,16) NOT NULL DEFAULT 0,
    weighted_amount DECIMAL(30,16) NOT NULL DEFAULT 0,
    expected_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    actual_issued_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    result VARCHAR(20) NOT NULL DEFAULT 'pending',
    exclusion_reason TEXT NOT NULL DEFAULT '',
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0,
    first_failed_at TIMESTAMPTZ NULL,
    last_attempt_at TIMESTAMPTZ NULL,
    grant_id BIGINT NULL,
    issued_at TIMESTAMPTZ NULL,
    expires_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS raw_amount DECIMAL(30,16) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS weighted_amount DECIMAL(30,16) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS expected_amount DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS actual_issued_amount DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS result VARCHAR(20) NOT NULL DEFAULT 'pending';
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS error_code VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS error_message TEXT NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS first_failed_at TIMESTAMPTZ NULL;
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS last_attempt_at TIMESTAMPTZ NULL;
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS issued_at TIMESTAMPTZ NULL;
ALTER TABLE reset_rebate_user_items ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE UNIQUE INDEX IF NOT EXISTS idx_reset_rebate_user_items_batch_user
    ON reset_rebate_user_items(batch_id, user_id);
CREATE INDEX IF NOT EXISTS idx_reset_rebate_user_items_batch_result
    ON reset_rebate_user_items(batch_id, result, user_id);

CREATE TABLE IF NOT EXISTS reset_rebate_user_account_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES reset_rebate_batches(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    account_name VARCHAR(255) NOT NULL,
    period_start TIMESTAMPTZ NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    raw_amount DECIMAL(30,16) NOT NULL DEFAULT 0,
    effective_stat_ratio DECIMAL(11,8) NOT NULL DEFAULT 0,
    weighted_amount DECIMAL(30,16) NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT reset_rebate_user_account_items_unique UNIQUE(batch_id, user_id, account_id)
);

CREATE INDEX IF NOT EXISTS idx_reset_rebate_user_account_items_batch_user
    ON reset_rebate_user_account_items(batch_id, user_id);

CREATE TABLE IF NOT EXISTS reset_rebate_user_attempts (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES reset_rebate_batches(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL,
    attempt_no INTEGER NOT NULL,
    admin_id BIGINT NOT NULL,
    admin_email VARCHAR(255) NOT NULL DEFAULT '',
    attempt_type VARCHAR(16) NOT NULL,
    result VARCHAR(32) NOT NULL,
    expected_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    grant_id BIGINT NULL,
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT reset_rebate_user_attempts_unique UNIQUE(batch_id, user_id, attempt_no)
);

CREATE INDEX IF NOT EXISTS idx_reset_rebate_user_attempts_batch_time
    ON reset_rebate_user_attempts(batch_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_usage_logs_account_time_user
    ON usage_logs(account_id, created_at, user_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_limited_credit_grants_reset_rebate_user
    ON user_limited_credit_grants(source_type, source_id, user_id)
    WHERE source_type = 'reset_rebate' AND source_id IS NOT NULL;
