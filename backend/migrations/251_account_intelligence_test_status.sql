-- 为 OpenAI OAuth 降智检测保存管理员人工判定；空字符串表示尚未标记。
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS intelligence_test_status VARCHAR(16) NOT NULL DEFAULT '';

ALTER TABLE accounts
    ADD CONSTRAINT accounts_intelligence_test_status_check
    CHECK (intelligence_test_status IN ('', 'passed', 'failed'));
