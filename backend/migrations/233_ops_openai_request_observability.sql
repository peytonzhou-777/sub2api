-- 补齐 OpenAI 上游失败请求的时序、响应头和安全会话观测字段。
ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS request_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS upstream_error_code VARCHAR(128),
    ADD COLUMN IF NOT EXISTS upstream_error_type VARCHAR(128),
    ADD COLUMN IF NOT EXISTS upstream_request_id VARCHAR(255),
    ADD COLUMN IF NOT EXISTS retry_after VARCHAR(128),
    ADD COLUMN IF NOT EXISTS upstream_rate_limit_headers JSONB,
    ADD COLUMN IF NOT EXISTS service_tier VARCHAR(64),
    ADD COLUMN IF NOT EXISTS proxy_id BIGINT REFERENCES proxies(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS egress_identifier VARCHAR(64),
    ADD COLUMN IF NOT EXISTS upstream_retry_attempts INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS account_concurrency INT,
    ADD COLUMN IF NOT EXISTS explicit_session_id_present BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS explicit_session_id_hash VARCHAR(64),
    ADD COLUMN IF NOT EXISTS session_scope_hash VARCHAR(128),
    ADD COLUMN IF NOT EXISTS session_source_hash VARCHAR(64),
    ADD COLUMN IF NOT EXISTS prompt_cache_key_present BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS prompt_cache_key_hash VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_ops_error_logs_account_request_started
    ON ops_error_logs (account_id, request_started_at DESC)
    WHERE account_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_error_logs_account_scope_time
    ON ops_error_logs (account_id, session_scope_hash, request_started_at DESC)
    WHERE account_id IS NOT NULL AND session_scope_hash IS NOT NULL;

COMMENT ON COLUMN ops_error_logs.upstream_rate_limit_headers IS
    '仅包含全部 x-ratelimit-* 响应头的净化 JSON，不包含其它上游响应头。';
COMMENT ON COLUMN ops_error_logs.egress_identifier IS
    '安全出口标识：direct 或 proxy:<id>，不保存代理主机、出口 IP 或凭据。';
COMMENT ON COLUMN ops_error_logs.explicit_session_id_hash IS
    '客户端显式 session_id 的截断 SHA-256；不得保存原始值。';
COMMENT ON COLUMN ops_error_logs.session_scope_hash IS
    '内部 Codex session scope 的 HMAC 哈希；不得保存原始 scope。';
COMMENT ON COLUMN ops_error_logs.session_source_hash IS
    '内部逻辑 turn source 的截断 SHA-256；不得保存原始 source。';
COMMENT ON COLUMN ops_error_logs.prompt_cache_key_hash IS
    '客户端 prompt_cache_key 的截断 SHA-256；不得保存原始值。';

-- 成功请求只保存相同的安全哈希，供账号窗口统计补齐分母与去重维度。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS session_scope_hash VARCHAR(128),
    ADD COLUMN IF NOT EXISTS session_source_hash VARCHAR(64),
    ADD COLUMN IF NOT EXISTS prompt_cache_key_hash VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_usage_logs_account_session_scope_time
    ON usage_logs (account_id, session_scope_hash, created_at DESC)
    WHERE session_scope_hash IS NOT NULL;
