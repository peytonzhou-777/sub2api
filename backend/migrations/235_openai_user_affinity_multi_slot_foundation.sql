-- OpenAI 用户粘性多槽位调度基础模型。
-- 本迁移建立加法式权威状态和单槽回填；旧 placement 仅保留为兼容投影。

CREATE TABLE IF NOT EXISTS openai_user_resident_slots (
    id                          BIGSERIAL PRIMARY KEY,
    user_id                     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_key                   TEXT NOT NULL DEFAULT 'openai',
    slot_index                  INT NOT NULL DEFAULT 1,
    account_id                  BIGINT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    generation                  BIGINT NOT NULL DEFAULT 1,
    status                      VARCHAR(30) NOT NULL DEFAULT 'provisional',
    admitted_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_success_at             TIMESTAMPTZ,
    expires_at                  TIMESTAMPTZ NOT NULL,
    usage_score                 DOUBLE PRECISION NOT NULL DEFAULT 0,
    score_updated_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    replacement_source_slot_id  BIGINT REFERENCES openai_user_resident_slots(id) ON DELETE SET NULL,
    provisional_token           VARCHAR(100),
    config_version              BIGINT NOT NULL DEFAULT 0,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_user_resident_slots_slot_index_check CHECK (slot_index BETWEEN 1 AND 5),
    CONSTRAINT openai_user_resident_slots_generation_check CHECK (generation > 0),
    CONSTRAINT openai_user_resident_slots_status_check CHECK (
        status IN ('provisional', 'active', 'replacement_pending', 'draining', 'expired', 'reset')
    ),
    CONSTRAINT openai_user_resident_slots_score_check CHECK (usage_score >= 0),
    CONSTRAINT openai_user_resident_slots_generation_unique
        UNIQUE (user_id, scope_key, account_id, generation)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_openai_user_resident_slots_current_account
    ON openai_user_resident_slots(user_id, scope_key, account_id)
    WHERE status IN ('provisional', 'active', 'replacement_pending', 'draining');
CREATE INDEX IF NOT EXISTS idx_openai_user_resident_slots_user_scope
    ON openai_user_resident_slots(user_id, scope_key, status, usage_score DESC, last_success_at DESC);
CREATE INDEX IF NOT EXISTS idx_openai_user_resident_slots_account
    ON openai_user_resident_slots(account_id, status, expires_at);

CREATE TABLE IF NOT EXISTS openai_user_conversation_bindings (
    id                          BIGSERIAL PRIMARY KEY,
    user_id                     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id                  BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    scope_key                   TEXT NOT NULL DEFAULT 'openai',
    conversation_hash           CHAR(64) NOT NULL,
    resident_slot_id            BIGINT NOT NULL REFERENCES openai_user_resident_slots(id) ON DELETE RESTRICT,
    account_id                  BIGINT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    slot_generation             BIGINT NOT NULL,
    status                      VARCHAR(20) NOT NULL DEFAULT 'provisional',
    context_rebuildable         BOOLEAN NOT NULL DEFAULT TRUE,
    first_output_committed      BOOLEAN NOT NULL DEFAULT FALSE,
    active_until                TIMESTAMPTZ,
    expires_at                  TIMESTAMPTZ NOT NULL,
    last_success_at             TIMESTAMPTZ,
    provisional_token           VARCHAR(100),
    pending_resident_slot_id    BIGINT REFERENCES openai_user_resident_slots(id) ON DELETE SET NULL,
    pending_account_id          BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    pending_slot_generation     BIGINT,
    pending_token               VARCHAR(100),
    pending_expires_at          TIMESTAMPTZ,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_user_conversation_bindings_hash_check CHECK (
        conversation_hash ~ '^[0-9a-f]{64}$'
    ),
    CONSTRAINT openai_user_conversation_bindings_generation_check CHECK (slot_generation > 0),
    CONSTRAINT openai_user_conversation_bindings_status_check CHECK (
        status IN ('provisional', 'active', 'draining', 'expired', 'reset')
    ),
    CONSTRAINT openai_user_conversation_bindings_scope_unique
        UNIQUE (user_id, api_key_id, scope_key, conversation_hash)
);

ALTER TABLE openai_user_conversation_bindings
    ADD COLUMN IF NOT EXISTS pending_resident_slot_id BIGINT REFERENCES openai_user_resident_slots(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS pending_account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS pending_slot_generation BIGINT,
    ADD COLUMN IF NOT EXISTS pending_token VARCHAR(100),
    ADD COLUMN IF NOT EXISTS pending_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_openai_user_conversation_bindings_slot
    ON openai_user_conversation_bindings(resident_slot_id, status, expires_at);
CREATE INDEX IF NOT EXISTS idx_openai_user_conversation_bindings_account_active
    ON openai_user_conversation_bindings(account_id, active_until)
    WHERE status IN ('provisional', 'active', 'draining');
CREATE INDEX IF NOT EXISTS idx_openai_user_conversation_bindings_pending
    ON openai_user_conversation_bindings(pending_account_id, pending_expires_at)
    WHERE pending_token IS NOT NULL;

CREATE TABLE IF NOT EXISTS openai_user_conversation_aliases (
    id                          BIGSERIAL PRIMARY KEY,
    binding_id                  BIGINT NOT NULL REFERENCES openai_user_conversation_bindings(id) ON DELETE CASCADE,
    user_id                     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    api_key_id                  BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    scope_key                   TEXT NOT NULL DEFAULT 'openai',
    alias_type                  VARCHAR(30) NOT NULL,
    alias_hash                  CHAR(64) NOT NULL,
    account_id                  BIGINT NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    expires_at                  TIMESTAMPTZ NOT NULL,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_user_conversation_aliases_hash_check CHECK (alias_hash ~ '^[0-9a-f]{64}$'),
    CONSTRAINT openai_user_conversation_aliases_type_check CHECK (
        alias_type IN ('previous_response_id', 'response_id', 'session_id', 'prompt_cache_key', 'websocket')
    ),
    CONSTRAINT openai_user_conversation_aliases_scope_unique
        UNIQUE (user_id, api_key_id, scope_key, alias_type, alias_hash)
);

CREATE INDEX IF NOT EXISTS idx_openai_user_conversation_aliases_expiry
    ON openai_user_conversation_aliases(expires_at);

ALTER TABLE user_account_capacity_incidents
    ADD COLUMN IF NOT EXISTS conversation_hash CHAR(64),
    ADD COLUMN IF NOT EXISTS resident_slot_id BIGINT REFERENCES openai_user_resident_slots(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS slot_generation BIGINT;

ALTER TABLE user_account_placement_events
    ADD COLUMN IF NOT EXISTS resident_slot_id BIGINT REFERENCES openai_user_resident_slots(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_user_account_capacity_incidents_conversation
    ON user_account_capacity_incidents(user_id, scope_key, conversation_hash, resident_slot_id, slot_generation)
    WHERE closed_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_account_placement_events_slot
    ON user_account_placement_events(resident_slot_id, created_at DESC);

-- 只回填迁移执行时仍有效的单归属；旧表继续作为首选槽投影。
INSERT INTO openai_user_resident_slots
    (user_id, scope_key, slot_index, account_id, generation, status, admitted_at,
     last_success_at, expires_at, usage_score, score_updated_at, provisional_token,
     config_version, created_at, updated_at)
SELECT p.user_id, p.scope_key, 1, p.account_id, p.generation, 'active', p.assigned_at,
       p.last_active_at, p.expires_at, CASE WHEN p.last_active_at IS NULL THEN 0 ELSE 1 END,
       COALESCE(p.last_active_at, p.assigned_at), p.provisional_token, 0, p.created_at, p.updated_at
FROM user_account_placements p
WHERE p.status = 'active' AND p.account_id IS NOT NULL AND p.expires_at > NOW()
ON CONFLICT DO NOTHING;
