-- 为异常循环赠额批次增加补发来源关联和重复补发保护。
ALTER TABLE recurring_credit_batches
    ADD COLUMN IF NOT EXISTS reissue_of_batch_id BIGINT NULL
        REFERENCES recurring_credit_batches(id) ON DELETE RESTRICT;

CREATE INDEX IF NOT EXISTS idx_recurring_credit_batches_reissue_source
    ON recurring_credit_batches(reissue_of_batch_id, created_at DESC)
    WHERE reissue_of_batch_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_recurring_credit_batches_active_reissue
    ON recurring_credit_batches(reissue_of_batch_id)
    WHERE reissue_of_batch_id IS NOT NULL
      AND status IN ('running', 'succeeded', 'empty');
