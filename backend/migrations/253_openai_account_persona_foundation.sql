-- OpenAI 动态 AccountPersona、Persona Session 与客户端 Session 权威占用模型。
-- 本迁移保持加法式；固定槽位表仅作为后续维护窗口数据迁移的来源。

CREATE TABLE IF NOT EXISTS openai_account_personas (
    id                                  BIGSERIAL PRIMARY KEY,
    account_id                          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    position                            INT NOT NULL,
    profile_id                          VARCHAR(64) NOT NULL,
    profile_version                     VARCHAR(64) NOT NULL,
    credential_owner                    VARCHAR(24) NOT NULL,
    state                               VARCHAR(16) NOT NULL DEFAULT 'draft',
    enabled                             BOOLEAN NOT NULL DEFAULT TRUE,
    persona_generation                  BIGINT NOT NULL DEFAULT 1,
    current_credential_chain_id         VARCHAR(128),
    current_session_epoch               BIGINT NOT NULL DEFAULT 0,
    device_seed                         BYTEA NOT NULL,
    installation_id                     VARCHAR(256) NOT NULL,
    proxy_id                            BIGINT REFERENCES proxies(id) ON DELETE RESTRICT,
    max_active_client_sessions_override INT,
    row_version                         BIGINT NOT NULL DEFAULT 1,
    created_at                          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    draining_started_at                 TIMESTAMPTZ,
    disabled_at                         TIMESTAMPTZ,
    retired_at                          TIMESTAMPTZ,
    CONSTRAINT openai_account_personas_position_check CHECK (position >= 0),
    CONSTRAINT openai_account_personas_profile_check CHECK (profile_id IN ('codex_cli_strict', 'opencode')),
    CONSTRAINT openai_account_personas_owner_check CHECK (credential_owner IN ('account_primary', 'persona_independent')),
    CONSTRAINT openai_account_personas_state_check CHECK (state IN ('draft', 'active', 'draining', 'disabled', 'retired')),
    CONSTRAINT openai_account_personas_generation_check CHECK (persona_generation > 0 AND current_session_epoch >= 0 AND row_version > 0),
    CONSTRAINT openai_account_personas_chain_check CHECK (
        current_credential_chain_id IS NULL OR btrim(current_credential_chain_id) <> ''
    ),
    CONSTRAINT openai_account_personas_installation_check CHECK (btrim(installation_id) <> ''),
    CONSTRAINT openai_account_personas_client_limit_check CHECK (
        max_active_client_sessions_override IS NULL OR max_active_client_sessions_override >= 1
    ),
    CONSTRAINT openai_account_personas_position_owner_check CHECK (
        (position = 0 AND profile_id = 'codex_cli_strict' AND credential_owner = 'account_primary')
        OR (position > 0 AND credential_owner = 'persona_independent')
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_openai_account_personas_live_position
    ON openai_account_personas(account_id, position)
    WHERE state <> 'retired';
CREATE UNIQUE INDEX IF NOT EXISTS idx_openai_account_personas_default
    ON openai_account_personas(account_id)
    WHERE position = 0;
CREATE INDEX IF NOT EXISTS idx_openai_account_personas_schedulable
    ON openai_account_personas(account_id, profile_id, state, enabled)
    WHERE state <> 'retired';
CREATE INDEX IF NOT EXISTS idx_openai_account_personas_proxy
    ON openai_account_personas(proxy_id)
    WHERE proxy_id IS NOT NULL AND state <> 'retired';

CREATE TABLE IF NOT EXISTS openai_account_persona_sessions (
    account_persona_id  BIGINT NOT NULL REFERENCES openai_account_personas(id) ON DELETE CASCADE,
    session_epoch       BIGINT NOT NULL,
    upstream_session_id VARCHAR(256) NOT NULL,
    state               VARCHAR(16) NOT NULL DEFAULT 'current',
    persona_generation  BIGINT NOT NULL,
    credential_chain_id VARCHAR(128) NOT NULL,
    profile_id          VARCHAR(64) NOT NULL,
    profile_version     VARCHAR(64) NOT NULL,
    effective_proxy_id  BIGINT REFERENCES proxies(id) ON DELETE RESTRICT,
    proxy_revision      BIGINT NOT NULL DEFAULT 0,
    started_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active_at      TIMESTAMPTZ,
    draining_started_at TIMESTAMPTZ,
    expires_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_persona_id, session_epoch),
    CONSTRAINT openai_account_persona_sessions_epoch_check CHECK (session_epoch > 0 AND persona_generation > 0),
    CONSTRAINT openai_account_persona_sessions_state_check CHECK (state IN ('current', 'draining', 'expired', 'revoked')),
    CONSTRAINT openai_account_persona_sessions_profile_check CHECK (profile_id IN ('codex_cli_strict', 'opencode')),
    CONSTRAINT openai_account_persona_sessions_identity_check CHECK (
        btrim(upstream_session_id) <> '' AND btrim(credential_chain_id) <> '' AND btrim(profile_version) <> ''
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_openai_account_persona_sessions_current
    ON openai_account_persona_sessions(account_persona_id)
    WHERE state = 'current';
CREATE INDEX IF NOT EXISTS idx_openai_account_persona_sessions_expiry
    ON openai_account_persona_sessions(state, expires_at)
    WHERE state IN ('draining', 'expired');

-- 总门 scope 行用于序列化“当前还没有 lease”的并发预留。
CREATE TABLE IF NOT EXISTS openai_user_group_client_session_scopes (
    user_id            BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    effective_group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    row_version        BIGINT NOT NULL DEFAULT 1,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, effective_group_id),
    CONSTRAINT openai_user_group_client_session_scopes_version_check CHECK (row_version > 0)
);

CREATE TABLE IF NOT EXISTS openai_user_group_client_session_leases (
    id                  BIGSERIAL PRIMARY KEY,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    effective_group_id  BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    client_session_hash CHAR(64) NOT NULL,
    state               VARCHAR(16) NOT NULL DEFAULT 'provisional',
    generation          BIGINT NOT NULL DEFAULT 1,
    last_active_at      TIMESTAMPTZ,
    active_until        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_user_group_client_session_leases_hash_check CHECK (client_session_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT openai_user_group_client_session_leases_state_check CHECK (state IN ('provisional', 'active', 'expired', 'revoked')),
    CONSTRAINT openai_user_group_client_session_leases_generation_check CHECK (generation > 0),
    CONSTRAINT openai_user_group_client_session_leases_scope_unique UNIQUE (user_id, effective_group_id, client_session_hash)
);

CREATE TABLE IF NOT EXISTS openai_user_group_session_request_holds (
    reservation_token UUID PRIMARY KEY,
    lease_id           BIGINT NOT NULL REFERENCES openai_user_group_client_session_leases(id) ON DELETE CASCADE,
    expires_at         TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_openai_user_group_client_session_leases_active
    ON openai_user_group_client_session_leases(user_id, effective_group_id, state, active_until);
CREATE INDEX IF NOT EXISTS idx_openai_user_group_session_request_holds_expiry
    ON openai_user_group_session_request_holds(expires_at);

CREATE TABLE IF NOT EXISTS openai_persona_client_session_leases (
    id                  BIGSERIAL PRIMARY KEY,
    account_persona_id  BIGINT NOT NULL REFERENCES openai_account_personas(id) ON DELETE CASCADE,
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id          BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    client_session_hash CHAR(64) NOT NULL,
    state               VARCHAR(16) NOT NULL DEFAULT 'provisional',
    generation          BIGINT NOT NULL DEFAULT 1,
    last_active_at      TIMESTAMPTZ,
    active_until        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_persona_client_session_leases_hash_check CHECK (client_session_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT openai_persona_client_session_leases_state_check CHECK (state IN ('provisional', 'active', 'expired', 'revoked')),
    CONSTRAINT openai_persona_client_session_leases_generation_check CHECK (generation > 0),
    CONSTRAINT openai_persona_client_session_leases_scope_unique UNIQUE (account_persona_id, user_id, api_key_id, client_session_hash)
);

CREATE TABLE IF NOT EXISTS openai_account_user_persona_claims (
    account_id         BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id            BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_persona_id BIGINT NOT NULL REFERENCES openai_account_personas(id) ON DELETE CASCADE,
    active_until       TIMESTAMPTZ NOT NULL,
    row_version        BIGINT NOT NULL DEFAULT 1,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, user_id),
    CONSTRAINT openai_account_user_persona_claims_version_check CHECK (row_version > 0)
);

CREATE TABLE IF NOT EXISTS openai_persona_request_holds (
    reservation_token UUID PRIMARY KEY,
    lease_id           BIGINT NOT NULL REFERENCES openai_persona_client_session_leases(id) ON DELETE CASCADE,
    account_persona_id BIGINT NOT NULL REFERENCES openai_account_personas(id) ON DELETE CASCADE,
    expires_at         TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_openai_persona_client_session_leases_capacity
    ON openai_persona_client_session_leases(account_persona_id, state, active_until);
CREATE INDEX IF NOT EXISTS idx_openai_persona_client_session_leases_client
    ON openai_persona_client_session_leases(user_id, client_session_hash, state, active_until);
CREATE INDEX IF NOT EXISTS idx_openai_account_user_persona_claims_expiry
    ON openai_account_user_persona_claims(account_id, active_until);
CREATE INDEX IF NOT EXISTS idx_openai_persona_request_holds_expiry
    ON openai_persona_request_holds(account_persona_id, expires_at);

ALTER TABLE openai_account_persona_credentials
    ADD COLUMN IF NOT EXISTS account_persona_id BIGINT REFERENCES openai_account_personas(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS profile_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS profile_version VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS persona_generation BIGINT NOT NULL DEFAULT 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_openai_persona_credentials_persona_chain
    ON openai_account_persona_credentials(account_persona_id, credential_chain_id)
    WHERE account_persona_id IS NOT NULL;

ALTER TABLE openai_persona_id_mappings
    ADD COLUMN IF NOT EXISTS account_persona_id BIGINT REFERENCES openai_account_personas(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS persona_generation BIGINT,
    ADD COLUMN IF NOT EXISTS persona_session_epoch BIGINT,
    ADD COLUMN IF NOT EXISTS profile_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS profile_version VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_openai_persona_id_mappings_persona_scope
    ON openai_persona_id_mappings(account_persona_id, persona_session_epoch, credential_chain_id, status);

ALTER TABLE openai_user_conversation_bindings
    ADD COLUMN IF NOT EXISTS account_persona_id BIGINT REFERENCES openai_account_personas(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS persona_session_epoch BIGINT,
    ADD COLUMN IF NOT EXISTS credential_chain_id VARCHAR(128),
    ADD COLUMN IF NOT EXISTS root_client_session_hash CHAR(64),
    ADD COLUMN IF NOT EXISTS user_group_client_session_lease_id BIGINT REFERENCES openai_user_group_client_session_leases(id) ON DELETE RESTRICT,
    ADD COLUMN IF NOT EXISTS profile_id VARCHAR(64),
    ADD COLUMN IF NOT EXISTS profile_version VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_openai_user_conversation_bindings_persona
    ON openai_user_conversation_bindings(account_persona_id, persona_session_epoch, status, expires_at);

COMMENT ON TABLE openai_account_personas IS
    'OpenAI OAuth 账号下动态应用/设备 Persona；position 只用于管理排序，运行时使用稳定 id';
COMMENT ON TABLE openai_account_persona_sessions IS
    'AccountPersona 自轮转出站 Session 的 current/draining 历史 epoch';
COMMENT ON TABLE openai_user_group_client_session_leases IS
    'User × effective Group 的客户端 Session 总门权威 lease';
COMMENT ON TABLE openai_persona_client_session_leases IS
    'AccountPersona 的客户端 Session 活跃占用权威 lease';
COMMENT ON TABLE openai_account_user_persona_claims IS
    '同一用户在同一 OpenAI 账号内唯一 AccountPersona 占用';
