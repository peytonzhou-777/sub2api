-- 为赠额任务批次增加滚动活跃资格策略和不可变的逐用户快照。
ALTER TABLE recurring_credit_batches
    ADD COLUMN IF NOT EXISTS eligibility_policy VARCHAR(40) NOT NULL DEFAULT 'period_usage_or_recharge',
    ADD COLUMN IF NOT EXISTS api_active_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS site_active_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS both_active_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS snapshot_completed_at TIMESTAMPTZ NULL;

ALTER TABLE recurring_credit_user_items
    ADD COLUMN IF NOT EXISTS api_last_used_at TIMESTAMPTZ NULL,
    ADD COLUMN IF NOT EXISTS site_last_active_at TIMESTAMPTZ NULL;

ALTER TABLE recurring_credit_batches
    DROP CONSTRAINT IF EXISTS recurring_credit_batch_eligibility_policy_check;

ALTER TABLE recurring_credit_batches
    ADD CONSTRAINT recurring_credit_batch_eligibility_policy_check CHECK (
        eligibility_policy IN ('period_usage_or_recharge', 'rolling_30d_activity_v1')
    );

ALTER TABLE recurring_credit_batches
    DROP CONSTRAINT IF EXISTS recurring_credit_batch_activity_counts_check;

ALTER TABLE recurring_credit_batches
    ADD CONSTRAINT recurring_credit_batch_activity_counts_check CHECK (
        api_active_count >= 0 AND
        site_active_count >= 0 AND
        both_active_count >= 0
    );
