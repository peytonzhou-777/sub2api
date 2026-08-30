-- OpenAI OAuth Persona 双向 ID 映射。
-- 映射按 Account × Persona × Slot × Session Epoch × Slot Generation ×
-- Slot Set Generation × Credential Chain 隔离；槽位禁用不删除历史记录。
CREATE TABLE IF NOT EXISTS openai_persona_id_mappings (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL DEFAULT 0,
    api_key_id BIGINT NOT NULL DEFAULT 0,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    scope_key VARCHAR(128) NOT NULL,
    persona VARCHAR(64) NOT NULL,
    slot_id SMALLINT NOT NULL,
    session_epoch BIGINT NOT NULL DEFAULT 0,
    slot_generation BIGINT NOT NULL DEFAULT 1,
    slot_set_generation BIGINT NOT NULL DEFAULT 1,
    credential_chain_id VARCHAR(128) NOT NULL DEFAULT '',
    thread_id VARCHAR(256) NOT NULL DEFAULT '',
    mapping_type VARCHAR(32) NOT NULL,
    client_id VARCHAR(512) NOT NULL,
    opencode_id VARCHAR(512) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'active',
    parent_mapping_id BIGINT,
    root_mapping_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '30 days'),
    CONSTRAINT openai_persona_id_mapping_persona_check CHECK (persona IN ('codex_cli_strict', 'opencode')),
    CONSTRAINT openai_persona_id_mapping_type_check CHECK (mapping_type IN ('thread', 'response', 'message', 'compaction', 'tool_call')),
    CONSTRAINT openai_persona_id_mapping_status_check CHECK (status IN ('active', 'draining', 'expired', 'revoked')),
    CONSTRAINT openai_persona_id_mapping_slot_check CHECK (slot_id >= 0 AND slot_id < 4),
    CONSTRAINT openai_persona_id_mapping_generation_check CHECK (slot_generation > 0 AND slot_set_generation > 0),
    CONSTRAINT openai_persona_id_mapping_ids_check CHECK (btrim(client_id) <> '' AND btrim(opencode_id) <> ''),
    UNIQUE (scope_key, mapping_type, client_id),
    UNIQUE (scope_key, mapping_type, opencode_id)
);

CREATE INDEX IF NOT EXISTS idx_openai_persona_id_mapping_client_principal
    ON openai_persona_id_mappings (user_id, api_key_id, mapping_type, client_id, status);

CREATE INDEX IF NOT EXISTS idx_openai_persona_id_mapping_scope_thread
    ON openai_persona_id_mappings (scope_key, mapping_type, thread_id, status);

CREATE INDEX IF NOT EXISTS idx_openai_persona_id_mapping_expiry
    ON openai_persona_id_mappings (expires_at, status);

COMMENT ON TABLE openai_persona_id_mappings IS
    'OpenAI OAuth 客户端 ID 与 OpenCode 原生 ID 的持久化映射；不跨 Persona、槽位、Session Epoch 或 OAuth 授权链复用';
COMMENT ON COLUMN openai_persona_id_mappings.scope_key IS
    '由 Account×Persona×Slot×Epoch×Generation×Credential Chain 派生的非机密作用域';
