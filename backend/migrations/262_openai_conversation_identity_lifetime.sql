-- 仅迁移切换时仍有效的身份；已过期、reset 或旧代记录不自动复活。
WITH retained AS (
    UPDATE openai_user_conversation_bindings
    SET expires_at = GREATEST(expires_at, COALESCE(last_success_at, created_at) + INTERVAL '7 days')
    WHERE binding_epoch = 2 AND first_output_committed
      AND status IN ('active', 'draining')
      AND openai_conversation_is_live(id, active_until, expires_at)
    RETURNING id, expires_at
)
UPDATE openai_user_conversation_aliases a
SET expires_at = r.expires_at FROM retained r
WHERE a.binding_id = r.id
  AND (a.expires_at > NOW() OR openai_conversation_has_activity(a.binding_id));

-- active_until 仅用于活动占用；expires_at 才是身份保留截止时间。
-- 保留函数签名，所有持久化读路径共享同一身份判定；旧实例必须在迁移前排空。
CREATE OR REPLACE FUNCTION openai_conversation_is_live(target_id BIGINT, active_deadline TIMESTAMPTZ, deadline TIMESTAMPTZ)
RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$
    SELECT deadline > NOW() OR openai_conversation_has_activity(target_id)
$$;

COMMENT ON COLUMN openai_user_conversation_bindings.active_until IS 'Short active occupancy deadline; idle identity remains resumable';
COMMENT ON COLUMN openai_user_conversation_bindings.expires_at IS 'Independent conversation identity deadline, additionally constrained by Persona epoch and revocation';
