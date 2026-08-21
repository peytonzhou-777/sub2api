-- 记录账号选定后的准入排队耗时，并为零成本准入拒绝保留请求类型。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS account_queue_wait_ms INTEGER;

COMMENT ON COLUMN usage_logs.account_queue_wait_ms IS
    '账号选定后在网关准入队列中的累计等待毫秒数；NULL 表示未进入账号队列。';

ALTER TABLE usage_logs
    DROP CONSTRAINT IF EXISTS usage_logs_request_type_check;

ALTER TABLE usage_logs
    ADD CONSTRAINT usage_logs_request_type_check
    CHECK (request_type >= 0 AND request_type <= 6) NOT VALID;
