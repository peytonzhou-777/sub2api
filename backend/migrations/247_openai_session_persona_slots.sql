-- OpenAI OAuth 双 Persona 槽位基础表。
-- 旧 accounts.credentials 保留作为兼容读取路径；新授权链按
-- Account × Persona × credential_chain_id 独立保存，应用层负责机密加密。
CREATE TABLE IF NOT EXISTS openai_account_persona_slots (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    slot_id SMALLINT NOT NULL,
    persona VARCHAR(64) NOT NULL,
    -- NULL 表示旧版账号级 OAuth 兼容链；v3 Persona 凭据由应用层要求非空链 ID。
    credential_chain_id VARCHAR(128),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    state VARCHAR(16) NOT NULL DEFAULT 'active',
    authorized BOOLEAN NOT NULL DEFAULT FALSE,
    session_epoch BIGINT NOT NULL DEFAULT 0,
    slot_generation BIGINT NOT NULL DEFAULT 1,
    slot_set_generation BIGINT NOT NULL DEFAULT 1,
    upstream_session_id VARCHAR(256),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    draining_started_at TIMESTAMPTZ,
    disabled_at TIMESTAMPTZ,
    PRIMARY KEY (account_id, slot_id),
    CONSTRAINT openai_persona_slots_slot_id_check CHECK (slot_id >= 0 AND slot_id < 4),
    CONSTRAINT openai_persona_slots_state_check CHECK (state IN ('active', 'draining', 'disabled')),
    CONSTRAINT openai_persona_slots_generation_check CHECK (slot_generation > 0 AND slot_set_generation > 0),
    CONSTRAINT openai_persona_slots_session_epoch_check CHECK (session_epoch >= 0),
    CONSTRAINT openai_persona_slots_chain_check CHECK (
        credential_chain_id IS NULL OR btrim(credential_chain_id) <> ''
    ),
    CONSTRAINT openai_persona_slots_persona_check CHECK (persona IN ('codex_cli_strict', 'opencode')),
    UNIQUE (account_id, slot_id, persona),
    UNIQUE (account_id, persona, credential_chain_id)
);

CREATE TABLE IF NOT EXISTS openai_account_persona_credentials (
    account_id BIGINT NOT NULL,
    persona VARCHAR(64) NOT NULL,
    credential_chain_id VARCHAR(128) NOT NULL,
    slot_id SMALLINT NOT NULL,
    -- 由应用层加密/脱敏后写入；禁止将明文 token 写入日志或审计事件。
    credentials JSONB NOT NULL DEFAULT '{}'::jsonb,
    chatgpt_account_id VARCHAR(256) NOT NULL DEFAULT '',
    installation_id VARCHAR(256) NOT NULL DEFAULT '',
    token_version BIGINT NOT NULL DEFAULT 0,
    state VARCHAR(32) NOT NULL DEFAULT 'ready',
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_refreshed_at TIMESTAMPTZ,
    PRIMARY KEY (account_id, persona, credential_chain_id),
    FOREIGN KEY (account_id, slot_id, persona)
        REFERENCES openai_account_persona_slots (account_id, slot_id, persona)
        ON DELETE CASCADE,
    CONSTRAINT openai_persona_credentials_persona_check CHECK (persona IN ('codex_cli_strict', 'opencode')),
    CONSTRAINT openai_persona_credentials_chain_check CHECK (btrim(credential_chain_id) <> ''),
    CONSTRAINT openai_persona_credentials_state_check CHECK (state IN ('pending', 'ready', 'refreshing', 'invalid', 'revoked')),
    CONSTRAINT openai_persona_credentials_token_version_check CHECK (token_version >= 0)
);

CREATE INDEX IF NOT EXISTS idx_openai_persona_slots_active
    ON openai_account_persona_slots (account_id, state, enabled, authorized);

CREATE INDEX IF NOT EXISTS idx_openai_persona_credentials_slot
    ON openai_account_persona_credentials (account_id, slot_id, persona, updated_at);

COMMENT ON TABLE openai_account_persona_slots IS
    'OpenAI OAuth 账号的永久 Persona 槽位记录；禁用不删除，Thread 依靠旧绑定排空';
COMMENT ON COLUMN openai_account_persona_slots.enabled IS
    '对外兼容开关；内部 state 区分 active/draining/disabled';
COMMENT ON COLUMN openai_account_persona_slots.slot_generation IS
    '槽位重新启用或 Session 重建时单调递增';
COMMENT ON COLUMN openai_account_persona_slots.slot_set_generation IS
    '槽位集合变更代次，参与新根映射和审计';
COMMENT ON TABLE openai_account_persona_credentials IS
    '按 Account × Persona × credential_chain_id 隔离的 OAuth 授权链';
