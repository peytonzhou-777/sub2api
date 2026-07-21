ALTER TABLE reset_rebate_batches
    ADD COLUMN IF NOT EXISTS failed_account_amount DECIMAL(20,8) NOT NULL DEFAULT 0;

UPDATE reset_rebate_batches AS batch
SET failed_account_amount = COALESCE((
    SELECT SUM(item.consumed_amount)
    FROM reset_rebate_account_items AS item
    WHERE item.batch_id = batch.id AND item.error_code <> ''
), 0);

COMMENT ON COLUMN reset_rebate_batches.failed_account_amount IS
    '统计失败账号在周期内承载的实际消费金额，用于强制返利风险确认';
