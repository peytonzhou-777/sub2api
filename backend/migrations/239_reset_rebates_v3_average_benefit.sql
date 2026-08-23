-- 重置返利 v3：统一结束时间，并支持全账号平均受益周期与比例。
ALTER TABLE reset_rebate_batches
    ADD COLUMN IF NOT EXISTS average_benefit_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS average_benefit_duration_us bigint NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS average_benefit_ratio decimal(11,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS combined_payout_ratio decimal(11,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS excluded_account_count integer NOT NULL DEFAULT 0;

ALTER TABLE reset_rebate_batches
    ALTER COLUMN mechanism_version SET DEFAULT 3;

ALTER TABLE reset_rebate_account_items
    ADD COLUMN IF NOT EXISTS included_in_statistics boolean NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS statistics_exclusion_reason text NOT NULL DEFAULT '';
