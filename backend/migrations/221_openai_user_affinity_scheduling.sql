-- OpenAI 用户粘性调度基础数据模型。
-- 居住归属、触达 TTL、容量失败窗口和搬迁审计相互独立，便于按各自生命周期清理/统计。

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS max_contact_users INT;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS new_resident_cooldown_seconds INT;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS new_resident_cooldown_until TIMESTAMPTZ;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS capacity_failure_migration_threshold INT;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS capacity_failure_window_seconds INT;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS affinity_config_version BIGINT NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'accounts_affinity_contact_users_check') THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_affinity_contact_users_check
            CHECK (max_contact_users IS NULL OR max_contact_users BETWEEN 1 AND 10000);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'accounts_affinity_cooldown_seconds_check') THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_affinity_cooldown_seconds_check
            CHECK (new_resident_cooldown_seconds IS NULL OR new_resident_cooldown_seconds BETWEEN 1 AND 86400);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'accounts_affinity_failure_threshold_check') THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_affinity_failure_threshold_check
            CHECK (capacity_failure_migration_threshold IS NULL OR capacity_failure_migration_threshold BETWEEN 2 AND 100);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'accounts_affinity_failure_window_check') THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_affinity_failure_window_check
            CHECK (capacity_failure_window_seconds IS NULL OR capacity_failure_window_seconds BETWEEN 10 AND 3600);
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS user_account_placements (
    id                              BIGSERIAL PRIMARY KEY,
    user_id                         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_key                       TEXT NOT NULL DEFAULT 'openai',
    account_id                      BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    generation                      BIGINT NOT NULL DEFAULT 1,
    status                          VARCHAR(20) NOT NULL DEFAULT 'active',
    assigned_at                     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at                  TIMESTAMPTZ,
    expires_at                      TIMESTAMPTZ NOT NULL,
    last_moved_at                   TIMESTAMPTZ,
    assignment_reason               VARCHAR(40) NOT NULL DEFAULT 'new_resident',
    predicted_5h_demand             DOUBLE PRECISION,
    predicted_7d_demand             DOUBLE PRECISION,
    prediction_version              VARCHAR(40),
    reset_at                        TIMESTAMPTZ,
    reset_by_admin_id               BIGINT REFERENCES users(id) ON DELETE SET NULL,
    reset_reason                    TEXT,
    reset_exclude_source_account    BOOLEAN,
    reset_source_account_id         BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT user_account_placements_status_check CHECK (status IN ('active', 'expired', 'reset')),
    CONSTRAINT user_account_placements_active_account_check CHECK (status <> 'active' OR account_id IS NOT NULL),
    CONSTRAINT user_account_placements_reset_account_check CHECK (status <> 'reset' OR account_id IS NULL),
    CONSTRAINT user_account_placements_scope_unique UNIQUE (user_id, scope_key)
);
CREATE INDEX IF NOT EXISTS idx_user_account_placements_account_expiry
    ON user_account_placements(account_id, expires_at);
CREATE INDEX IF NOT EXISTS idx_user_account_placements_expiry
    ON user_account_placements(expires_at);

CREATE TABLE IF NOT EXISTS account_user_contacts (
    id                              BIGSERIAL PRIMARY KEY,
    account_id                      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id                         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_touched_at                 TIMESTAMPTZ,
    touch_expires_at                TIMESTAMPTZ,
    reservation_kind                VARCHAR(30),
    reservation_token               VARCHAR(100),
    reservation_until               TIMESTAMPTZ,
    reservation_generation          BIGINT,
    reentry_batch_token             VARCHAR(100),
    reentry_state                   VARCHAR(30),
    leader_token                    VARCHAR(100),
    leader_version                  BIGINT NOT NULL DEFAULT 0,
    leader_lease_until              TIMESTAMPTZ,
    reentry_config_version          BIGINT,
    account_affinity_config_version BIGINT,
    follower_jitter_min_ms          INT,
    follower_jitter_max_ms          INT,
    active_period_id                BIGINT,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT account_user_contacts_unique UNIQUE (account_id, user_id),
    CONSTRAINT account_user_contacts_jitter_check CHECK (
        follower_jitter_min_ms IS NULL OR
        (follower_jitter_min_ms >= 0 AND follower_jitter_max_ms >= follower_jitter_min_ms)
    )
);
CREATE INDEX IF NOT EXISTS idx_account_user_contacts_touch_expiry
    ON account_user_contacts(account_id, touch_expires_at);
CREATE INDEX IF NOT EXISTS idx_account_user_contacts_user
    ON account_user_contacts(user_id, updated_at DESC);

CREATE TABLE IF NOT EXISTS account_user_contact_periods (
    id                              BIGSERIAL PRIMARY KEY,
    account_id                      BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id                         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    period_started_at               TIMESTAMPTZ NOT NULL,
    first_touched_at                TIMESTAMPTZ,
    last_touched_at                 TIMESTAMPTZ,
    touch_expires_at                TIMESTAMPTZ,
    closed_at                       TIMESTAMPTZ,
    activation_kind                 VARCHAR(30) NOT NULL,
    placement_generation            BIGINT,
    touch_success_mode              VARCHAR(30),
    config_version                  BIGINT,
    request_count                   BIGINT NOT NULL DEFAULT 0,
    usage                           JSONB,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_account_user_contact_periods_account_user
    ON account_user_contact_periods(account_id, user_id, period_started_at DESC);
CREATE INDEX IF NOT EXISTS idx_account_user_contact_periods_started
    ON account_user_contact_periods(period_started_at);

CREATE TABLE IF NOT EXISTS user_account_capacity_incidents (
    id                              BIGSERIAL PRIMARY KEY,
    user_id                         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_key                       TEXT NOT NULL DEFAULT 'openai',
    source_account_id               BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    placement_generation            BIGINT NOT NULL,
    window_started_at               TIMESTAMPTZ NOT NULL,
    window_expires_at               TIMESTAMPTZ NOT NULL,
    failure_threshold               INT NOT NULL,
    failure_count                   INT NOT NULL DEFAULT 0,
    last_failure_reason             VARCHAR(80),
    last_failure_at                 TIMESTAMPTZ,
    status                          VARCHAR(30) NOT NULL DEFAULT 'collecting',
    migration_token                 VARCHAR(100),
    migration_lease_until           TIMESTAMPTZ,
    migration_authorized_at         TIMESTAMPTZ,
    migration_target_account_id     BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    config_version                  BIGINT,
    account_affinity_config_version BIGINT,
    closed_at                       TIMESTAMPTZ,
    close_reason                    VARCHAR(80),
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT user_account_capacity_incidents_scope_unique
        UNIQUE (user_id, scope_key, source_account_id, placement_generation, window_started_at)
);
CREATE INDEX IF NOT EXISTS idx_user_account_capacity_incidents_open
    ON user_account_capacity_incidents(user_id, source_account_id, closed_at, window_expires_at);

CREATE TABLE IF NOT EXISTS user_account_capacity_failures (
    id                              BIGSERIAL PRIMARY KEY,
    incident_id                     BIGINT NOT NULL REFERENCES user_account_capacity_incidents(id) ON DELETE CASCADE,
    request_id_hash                 CHAR(64) NOT NULL,
    failure_reason                  VARCHAR(80) NOT NULL,
    failed_at                       TIMESTAMPTZ NOT NULL,
    capacity_snapshot               JSONB,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT user_account_capacity_failures_request_unique UNIQUE (incident_id, request_id_hash)
);
CREATE INDEX IF NOT EXISTS idx_user_account_capacity_failures_failed_at
    ON user_account_capacity_failures(failed_at);

CREATE TABLE IF NOT EXISTS user_account_placement_events (
    id                              BIGSERIAL PRIMARY KEY,
    user_id                         BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_key                       TEXT NOT NULL DEFAULT 'openai',
    placement_generation            BIGINT NOT NULL,
    source_account_id               BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    target_account_id               BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    event_type                      VARCHAR(40) NOT NULL,
    reason                          VARCHAR(80) NOT NULL,
    config_version                  BIGINT,
    account_affinity_config_version BIGINT,
    effective_source                VARCHAR(20),
    effective_values               JSONB,
    actor_admin_id                  BIGINT REFERENCES users(id) ON DELETE SET NULL,
    metadata                        JSONB,
    created_at                      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_user_account_placement_events_user
    ON user_account_placement_events(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_user_account_placement_events_account
    ON user_account_placement_events(target_account_id, created_at DESC);
