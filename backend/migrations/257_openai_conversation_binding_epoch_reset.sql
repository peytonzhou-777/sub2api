-- AccountPersona 续链切代：旧绑定保留审计，但不再参与运行时恢复。
ALTER TABLE openai_user_conversation_bindings
    ADD COLUMN IF NOT EXISTS binding_epoch BIGINT;

UPDATE openai_user_conversation_bindings
SET binding_epoch = 1
WHERE binding_epoch IS NULL;

ALTER TABLE openai_user_conversation_bindings
    ALTER COLUMN binding_epoch SET DEFAULT 2,
    ALTER COLUMN binding_epoch SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'openai_user_conversation_bindings'::regclass
          AND conname = 'openai_user_conversation_bindings_epoch_check'
    ) THEN
        ALTER TABLE openai_user_conversation_bindings
            ADD CONSTRAINT openai_user_conversation_bindings_epoch_check CHECK (binding_epoch > 0);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_openai_user_conversation_bindings_current_epoch
    ON openai_user_conversation_bindings(binding_epoch, user_id, api_key_id, scope_key, conversation_hash)
    WHERE status IN ('provisional', 'active', 'draining');

-- Thread 身份不再依赖短期客户端 Session lease 的存活；该列只保留审计关联。
DO $$
DECLARE
    fk_name TEXT;
BEGIN
    SELECT c.conname INTO fk_name
    FROM pg_constraint c
    JOIN pg_attribute a
      ON a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
    WHERE c.conrelid = 'openai_user_conversation_bindings'::regclass
      AND c.contype = 'f'
      AND a.attname = 'user_group_client_session_lease_id'
    LIMIT 1;

    IF fk_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE openai_user_conversation_bindings DROP CONSTRAINT %I', fk_name);
    END IF;
END $$;

ALTER TABLE openai_user_conversation_bindings
    ADD CONSTRAINT openai_user_conversation_bindings_user_group_lease_fk
    FOREIGN KEY (user_group_client_session_lease_id)
    REFERENCES openai_user_group_client_session_leases(id)
    ON DELETE SET NULL;

-- 部署采用一次受控切代：旧会话自然新建根请求，不迁移不完整的 Persona/lease 状态。
UPDATE openai_user_conversation_bindings
SET status = 'expired',
    active_until = NULL,
    expires_at = LEAST(expires_at, NOW()),
    provisional_token = NULL,
    pending_resident_slot_id = NULL,
    pending_account_id = NULL,
    pending_slot_generation = NULL,
    pending_token = NULL,
    pending_expires_at = NULL,
    updated_at = NOW()
WHERE binding_epoch < 2
  AND status IN ('provisional', 'active', 'draining');

UPDATE openai_user_conversation_aliases a
SET expires_at = LEAST(a.expires_at, NOW())
FROM openai_user_conversation_bindings b
WHERE b.id = a.binding_id AND b.binding_epoch < 2;

DELETE FROM openai_user_group_session_request_holds;
DELETE FROM openai_persona_request_holds;

UPDATE openai_user_group_client_session_leases
SET state = 'expired', active_until = NOW(), updated_at = NOW()
WHERE state IN ('provisional', 'active');

UPDATE openai_persona_client_session_leases
SET state = 'expired', active_until = NOW(), updated_at = NOW()
WHERE state IN ('provisional', 'active');

DELETE FROM openai_account_user_persona_claims;

UPDATE openai_user_group_client_session_scopes
SET row_version = row_version + 1, updated_at = NOW();

UPDATE openai_user_active_routes
SET resident_slot_id = NULL,
    account_id = NULL,
    slot_generation = NULL,
    claimed_at = NULL,
    active_until = NULL,
    pending_resident_slot_id = NULL,
    pending_account_id = NULL,
    pending_slot_generation = NULL,
    pending_claimed_at = NULL,
    pending_token = NULL,
    pending_expires_at = NULL,
    updated_at = NOW();

COMMENT ON COLUMN openai_user_conversation_bindings.binding_epoch IS
    '持久化 Thread 绑定代际；仅当前应用代际参与运行时续链恢复';
COMMENT ON COLUMN openai_user_conversation_bindings.user_group_client_session_lease_id IS
    '首轮占用 lease 的可空审计引用；Thread 身份恢复不依赖该 lease 存活';
