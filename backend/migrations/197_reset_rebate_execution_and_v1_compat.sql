-- 重置返利执行任务持久化，以及旧版批次摘要兼容回填。
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS execution_mode VARCHAR(16) NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS execution_cursor_user_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS execution_admin_id BIGINT NULL;
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS execution_admin_email VARCHAR(255) NOT NULL DEFAULT '';
ALTER TABLE reset_rebate_batches ADD COLUMN IF NOT EXISTS initial_issued_at TIMESTAMPTZ NULL;

ALTER TABLE reset_rebate_batches DROP CONSTRAINT IF EXISTS reset_rebate_status_check;
ALTER TABLE reset_rebate_batches ADD CONSTRAINT reset_rebate_status_check
    CHECK (status IN ('running','ready','executing','not_eligible','partial','failed','executed','incomplete','expired'));

-- 仅已部署过 v1 的数据库包含这些旧列；动态 SQL 避免新安装数据库解析不存在的列。
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'reset_rebate_batches' AND column_name = 'actual_amount'
    ) THEN
        EXECUTE $compat$
            UPDATE reset_rebate_batches AS batch
            SET account_count = COALESCE((
                    SELECT COUNT(*) FROM reset_rebate_account_items AS account_item
                    WHERE account_item.batch_id = batch.id
                ), 0),
                raw_amount = COALESCE(batch.actual_amount, 0),
                weighted_amount = COALESCE(batch.refundable_amount, 0),
                expected_amount = CASE WHEN batch.status = 'executed' THEN COALESCE(batch.issued_amount, 0) ELSE 0 END,
                successful_amount = CASE WHEN batch.status = 'executed' THEN COALESCE(batch.issued_amount, 0) ELSE 0 END,
                expected_user_count = CASE WHEN batch.status = 'executed' THEN COALESCE(batch.issued_user_count, 0) ELSE 0 END,
                successful_user_count = CASE WHEN batch.status = 'executed' THEN COALESCE(batch.issued_user_count, 0) ELSE 0 END,
                first_executed_at = CASE WHEN batch.status = 'executed' THEN COALESCE(batch.executed_at, batch.issued_at) ELSE NULL END
            WHERE batch.mechanism_version = 1
        $compat$;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'reset_rebate_account_items' AND column_name = 'consumed_amount'
    ) THEN
        EXECUTE $compat$
            UPDATE reset_rebate_account_items AS item
            SET raw_amount = COALESCE(item.consumed_amount, 0),
                weighted_amount = CASE WHEN item.included THEN COALESCE(item.consumed_amount, 0) ELSE 0 END,
                ratio_mode = 'auto',
                auto_stat_ratio = CASE WHEN item.included THEN 100 ELSE 0 END,
                effective_stat_ratio = CASE WHEN item.included THEN 100 ELSE 0 END,
                default_window_source = 'legacy_v1',
                window_risk = CASE WHEN item.error_code <> '' THEN 'legacy_account_error' ELSE '' END
            FROM reset_rebate_batches AS batch
            WHERE item.batch_id = batch.id AND batch.mechanism_version = 1
        $compat$;
    END IF;

    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'reset_rebate_user_items' AND column_name = 'actual_amount'
    ) THEN
        EXECUTE $compat$
            UPDATE reset_rebate_user_items AS item
            SET raw_amount = COALESCE(item.actual_amount, 0),
                weighted_amount = COALESCE(item.actual_amount, 0),
                expected_amount = COALESCE(item.rebate_amount, 0),
                actual_issued_amount = CASE WHEN item.issued THEN COALESCE(item.rebate_amount, 0) ELSE 0 END,
                result = CASE
                    WHEN item.issued THEN 'succeeded'
                    WHEN item.user_deleted OR COALESCE(item.rebate_amount, 0) = 0 THEN 'excluded'
                    ELSE 'pending'
                END,
                issued_at = CASE WHEN item.issued THEN COALESCE(batch.issued_at, batch.executed_at) ELSE NULL END
            FROM reset_rebate_batches AS batch
            WHERE item.batch_id = batch.id AND batch.mechanism_version = 1
        $compat$;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_reset_rebate_batches_executing
    ON reset_rebate_batches(status, execution_mode, id)
    WHERE status = 'executing';
