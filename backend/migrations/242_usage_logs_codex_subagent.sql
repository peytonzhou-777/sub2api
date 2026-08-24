-- 在成功用量记录中保存无敏感标识的 Codex 子代理布尔标记。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS is_subagent BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN usage_logs.is_subagent IS '是否识别为 Codex 子代理请求，不保存原始线程或轮次标识';
