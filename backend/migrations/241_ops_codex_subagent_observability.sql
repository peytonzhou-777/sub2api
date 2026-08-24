-- 为 Codex 子代理限流诊断补充无敏感标识的请求形态字段。
ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS is_subagent BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS subagent_kind VARCHAR(64),
    ADD COLUMN IF NOT EXISTS inbound_transport VARCHAR(32),
    ADD COLUMN IF NOT EXISTS upstream_transport VARCHAR(64);

COMMENT ON COLUMN ops_error_logs.is_subagent IS '是否识别为 Codex 子代理请求';
COMMENT ON COLUMN ops_error_logs.subagent_kind IS '规范化后的 Codex 子代理类型';
COMMENT ON COLUMN ops_error_logs.inbound_transport IS '规范化后的客户端入站传输';
COMMENT ON COLUMN ops_error_logs.upstream_transport IS '规范化后的上游出站传输';

CREATE INDEX IF NOT EXISTS idx_ops_error_logs_codex_subagent
    ON ops_error_logs (account_id, request_started_at DESC)
    WHERE is_subagent = TRUE;
