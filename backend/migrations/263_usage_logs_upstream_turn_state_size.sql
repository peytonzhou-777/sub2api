-- 仅记录上游原始状态头大小；历史记录及未携带状态头的请求保持 NULL。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS upstream_turn_state_size_bytes integer;

COMMENT ON COLUMN usage_logs.upstream_turn_state_size_bytes IS
    'Upstream raw x-codex-turn-state response header value size in bytes, before wrapping';
