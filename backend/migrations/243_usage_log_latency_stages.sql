-- 保存成功请求的上游阶段耗时，供使用记录展示与后续诊断复用。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS first_response_ms INT,
    ADD COLUMN IF NOT EXISTS first_event_ms INT,
    ADD COLUMN IF NOT EXISTS first_output_ms INT;

COMMENT ON COLUMN usage_logs.first_response_ms IS
    '上游最终成功尝试收到 HTTP 响应头的耗时；WS 为连接/租约确认耗时';
COMMENT ON COLUMN usage_logs.first_event_ms IS
    '首个可解析 SSE data 或 WS 事件的耗时';
COMMENT ON COLUMN usage_logs.first_output_ms IS
    '首次满足 startsClientOutput 的结构性输出事件耗时';
