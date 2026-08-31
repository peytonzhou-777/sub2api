-- 立即执行赠额任务支持按小时配置额度有效期，并兼容旧版按天字段。
ALTER TABLE recurring_credit_tasks
    ADD COLUMN IF NOT EXISTS validity_hours INTEGER NULL;

ALTER TABLE recurring_credit_batches
    ADD COLUMN IF NOT EXISTS validity_hours INTEGER NULL;

UPDATE recurring_credit_tasks
SET validity_hours = validity_days * 24
WHERE schedule_type = 'immediate'
  AND validity_days IS NOT NULL
  AND validity_hours IS NULL;

UPDATE recurring_credit_batches
SET validity_hours = validity_days * 24
WHERE schedule_type = 'immediate'
  AND validity_days IS NOT NULL
  AND validity_hours IS NULL;

ALTER TABLE recurring_credit_tasks
    DROP CONSTRAINT IF EXISTS recurring_credit_task_schedule_check;

ALTER TABLE recurring_credit_tasks
    ADD CONSTRAINT recurring_credit_task_schedule_check CHECK (
        (schedule_type = 'monthly' AND day_of_month BETWEEN 1 AND 28 AND day_of_week IS NULL AND validity_days IS NULL AND validity_hours IS NULL) OR
        (schedule_type = 'weekly' AND day_of_week BETWEEN 1 AND 7 AND day_of_month IS NULL AND validity_days IS NULL AND validity_hours IS NULL) OR
        (schedule_type = 'immediate' AND day_of_month IS NULL AND day_of_week IS NULL
            AND (COALESCE(validity_days BETWEEN 1 AND 36500, FALSE) OR COALESCE(validity_hours BETWEEN 1 AND 876000, FALSE))
            AND (validity_days IS NULL OR validity_hours IS NULL OR validity_hours = validity_days * 24))
    );

ALTER TABLE recurring_credit_batches
    DROP CONSTRAINT IF EXISTS recurring_credit_batch_validity_check;

ALTER TABLE recurring_credit_batches
    ADD CONSTRAINT recurring_credit_batch_validity_check CHECK (
        (schedule_type = 'immediate'
            AND (COALESCE(validity_days BETWEEN 1 AND 36500, FALSE) OR COALESCE(validity_hours BETWEEN 1 AND 876000, FALSE))
            AND (validity_days IS NULL OR validity_hours IS NULL OR validity_hours = validity_days * 24)) OR
        (schedule_type IN ('monthly', 'weekly') AND validity_days IS NULL AND validity_hours IS NULL)
    );
