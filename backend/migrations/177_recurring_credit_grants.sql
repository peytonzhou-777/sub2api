CREATE TABLE IF NOT EXISTS recurring_credit_tasks (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    admin_notes TEXT NOT NULL DEFAULT '',
    schedule_type VARCHAR(16) NOT NULL,
    day_of_month INTEGER NULL,
    day_of_week INTEGER NULL,
    local_time VARCHAR(5) NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    amount DECIMAL(20,8) NOT NULL,
    execution_mode VARCHAR(16) NOT NULL,
    remaining_runs INTEGER NULL,
    skip_count INTEGER NOT NULL DEFAULT 0,
    status VARCHAR(16) NOT NULL,
    next_run_at TIMESTAMPTZ NULL,
    version INTEGER NOT NULL DEFAULT 1,
    idempotency_key VARCHAR(128) NULL,
    created_by_admin_id BIGINT NOT NULL,
    created_by_admin_email VARCHAR(255) NOT NULL DEFAULT '',
    updated_by_admin_id BIGINT NOT NULL,
    updated_by_admin_email VARCHAR(255) NOT NULL DEFAULT '',
    deleted_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT recurring_credit_task_schedule_check CHECK (
        (schedule_type = 'monthly' AND day_of_month BETWEEN 1 AND 28 AND day_of_week IS NULL) OR
        (schedule_type = 'weekly' AND day_of_week BETWEEN 1 AND 7 AND day_of_month IS NULL)
    ),
    CONSTRAINT recurring_credit_task_amount_check CHECK (amount BETWEEN 0.01 AND 10000),
    CONSTRAINT recurring_credit_task_mode_check CHECK (
        (execution_mode = 'finite' AND remaining_runs >= 0 AND (remaining_runs > 0 OR status IN ('completed','deleted'))) OR
        (execution_mode = 'permanent' AND remaining_runs IS NULL)
    ),
    CONSTRAINT recurring_credit_task_skip_check CHECK (skip_count BETWEEN 0 AND 100),
    CONSTRAINT recurring_credit_task_status_check CHECK (status IN ('active','stopped','completed','deleted'))
);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_tasks_due ON recurring_credit_tasks(status, next_run_at) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_recurring_credit_tasks_created ON recurring_credit_tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_tasks_name ON recurring_credit_tasks(name);
CREATE UNIQUE INDEX IF NOT EXISTS idx_recurring_credit_tasks_idempotency ON recurring_credit_tasks(idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS recurring_credit_batches (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL REFERENCES recurring_credit_tasks(id) ON DELETE RESTRICT,
    task_name VARCHAR(100) NOT NULL,
    scheduled_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    qualification_start TIMESTAMPTZ NOT NULL,
    qualification_end TIMESTAMPTZ NOT NULL,
    qualification_cutoff_at TIMESTAMPTZ NULL,
    config_version INTEGER NOT NULL,
    schedule_type VARCHAR(16) NOT NULL,
    day_of_month INTEGER NULL,
    day_of_week INTEGER NULL,
    local_time VARCHAR(5) NOT NULL,
    timezone VARCHAR(64) NOT NULL,
    amount DECIMAL(20,8) NOT NULL,
    execution_mode VARCHAR(16) NOT NULL,
    status VARCHAR(16) NOT NULL,
    claimed_at TIMESTAMPTZ NULL,
    lease_owner VARCHAR(128) NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMPTZ NULL,
    heartbeat_at TIMESTAMPTZ NULL,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    eligible_user_count INTEGER NOT NULL DEFAULT 0,
    issued_user_count INTEGER NOT NULL DEFAULT 0,
    excluded_user_count INTEGER NOT NULL DEFAULT 0,
    usage_eligible_count INTEGER NOT NULL DEFAULT 0,
    recharge_eligible_count INTEGER NOT NULL DEFAULT 0,
    issued_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    failure_code VARCHAR(64) NOT NULL DEFAULT '',
    failure_message TEXT NOT NULL DEFAULT '',
    finished_at TIMESTAMPTZ NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT recurring_credit_batch_task_time_uniq UNIQUE(task_id, scheduled_at),
    CONSTRAINT recurring_credit_batch_status_check CHECK (status IN ('running','succeeded','empty','skipped','missed','failed'))
);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_batches_task_created ON recurring_credit_batches(task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_batches_lease ON recurring_credit_batches(status, lease_expires_at) WHERE status = 'running';

CREATE TABLE IF NOT EXISTS recurring_credit_user_items (
    id BIGSERIAL PRIMARY KEY,
    batch_id BIGINT NOT NULL REFERENCES recurring_credit_batches(id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL,
    email VARCHAR(255) NOT NULL DEFAULT '',
    username VARCHAR(100) NOT NULL DEFAULT '',
    user_status VARCHAR(20) NOT NULL DEFAULT '',
    user_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    actual_cost DECIMAL(20,8) NOT NULL DEFAULT 0,
    net_recharge DECIMAL(20,8) NOT NULL DEFAULT 0,
    qualification_reason VARCHAR(32) NOT NULL DEFAULT '',
    grant_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
    grant_id BIGINT NULL REFERENCES user_limited_credit_grants(id) ON DELETE RESTRICT,
    result VARCHAR(32) NOT NULL,
    exclusion_reason VARCHAR(100) NOT NULL DEFAULT '',
    CONSTRAINT recurring_credit_user_batch_uniq UNIQUE(batch_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_user_items_result ON recurring_credit_user_items(batch_id, result);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_user_items_email ON recurring_credit_user_items(batch_id, email);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_user_items_username ON recurring_credit_user_items(batch_id, username);

CREATE TABLE IF NOT EXISTS recurring_credit_task_audits (
    id BIGSERIAL PRIMARY KEY,
    task_id BIGINT NOT NULL REFERENCES recurring_credit_tasks(id) ON DELETE RESTRICT,
    admin_id BIGINT NOT NULL,
    admin_email VARCHAR(255) NOT NULL DEFAULT '',
    client_ip VARCHAR(64) NOT NULL DEFAULT '',
    action VARCHAR(32) NOT NULL,
    before_snapshot JSONB NULL,
    after_snapshot JSONB NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_task_audits_task_created ON recurring_credit_task_audits(task_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_recurring_credit_task_audits_admin_created ON recurring_credit_task_audits(admin_id, created_at DESC);

CREATE UNIQUE INDEX IF NOT EXISTS idx_user_limited_credit_grants_recurring_user
    ON user_limited_credit_grants(source_type, source_id, user_id)
    WHERE source_type = 'recurring_grant' AND source_id IS NOT NULL;
