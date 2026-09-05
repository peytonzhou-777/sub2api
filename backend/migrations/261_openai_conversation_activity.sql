-- 会话在途保护独立于用户常驻偏好，不修改存量身份或清理线上绑定。
CREATE TABLE IF NOT EXISTS openai_conversation_request_holds (
    request_token UUID PRIMARY KEY,
    binding_id BIGINT NOT NULL REFERENCES openai_user_conversation_bindings(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL,
    slot_generation BIGINT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    persona_reservation_token UUID
);
CREATE INDEX IF NOT EXISTS idx_openai_conversation_holds_binding_expiry
    ON openai_conversation_request_holds(binding_id, expires_at);
CREATE INDEX IF NOT EXISTS idx_openai_conversation_holds_expiry
    ON openai_conversation_request_holds(expires_at);

CREATE OR REPLACE FUNCTION openai_conversation_has_activity(target_id BIGINT)
RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM openai_conversation_request_holds h
                   JOIN openai_user_conversation_bindings b ON b.id = h.binding_id
                   WHERE h.binding_id = target_id AND h.expires_at > NOW()
                     AND h.account_id = b.account_id AND h.slot_generation = b.slot_generation)
$$;

CREATE OR REPLACE FUNCTION openai_conversation_is_live(target_id BIGINT, active_deadline TIMESTAMPTZ, deadline TIMESTAMPTZ)
RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
    SELECT LEAST(COALESCE(active_deadline, deadline), deadline) > NOW()
           OR openai_conversation_has_activity(target_id)
$$;

CREATE OR REPLACE FUNCTION openai_persona_user_has_activity(persona_id BIGINT, principal_id BIGINT)
RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
    SELECT EXISTS (SELECT 1 FROM openai_user_conversation_bindings b
                   JOIN openai_conversation_request_holds h ON h.binding_id = b.id
                   WHERE b.account_persona_id = persona_id AND b.user_id = principal_id
                     AND b.status IN ('provisional', 'active', 'draining') AND h.expires_at > NOW()
                     AND h.account_id = b.account_id AND h.slot_generation = b.slot_generation)
$$;
