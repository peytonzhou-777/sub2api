-- 老用户免邀请码注册资格。仅保留检测所需的邮箱身份、结论和原因，
-- 不把调用量、活跃天数等运维统计明细复制进业务库。
CREATE TABLE IF NOT EXISTS registration_legacy_eligibilities (
    normalized_email TEXT PRIMARY KEY,
    eligible BOOLEAN NOT NULL,
    failure_reasons TEXT[] NOT NULL DEFAULT '{}',
    source_batch TEXT NOT NULL,
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT registration_legacy_eligibilities_email_not_blank
        CHECK (
            BTRIM(normalized_email) <> ''
            AND LENGTH(normalized_email) <= 255
            AND normalized_email = LOWER(BTRIM(normalized_email))
        ),
    CONSTRAINT registration_legacy_eligibilities_source_batch_not_blank
        CHECK (BTRIM(source_batch) <> ''),
    CONSTRAINT registration_legacy_eligibilities_reason_consistency
        CHECK (
            (eligible AND CARDINALITY(failure_reasons) = 0)
            OR (NOT eligible AND CARDINALITY(failure_reasons) > 0)
        ),
    CONSTRAINT registration_legacy_eligibilities_reason_values
        CHECK (
            failure_reasons <@ ARRAY[
                'insufficient_success_calls',
                'insufficient_active_days',
                'cyber_policy_warning',
                'soft_deleted'
            ]::TEXT[]
        )
);

CREATE INDEX IF NOT EXISTS idx_registration_legacy_eligibilities_eligible
    ON registration_legacy_eligibilities (eligible);
