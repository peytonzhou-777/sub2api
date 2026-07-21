-- 为赠额任务增加一次性立即执行类型及独立有效期快照。
ALTER TABLE recurring_credit_tasks
    ADD COLUMN IF NOT EXISTS validity_days INTEGER NULL;

ALTER TABLE recurring_credit_batches
    ADD COLUMN IF NOT EXISTS validity_days INTEGER NULL;

ALTER TABLE recurring_credit_tasks
    DROP CONSTRAINT IF EXISTS recurring_credit_task_schedule_check;

ALTER TABLE recurring_credit_tasks
    ADD CONSTRAINT recurring_credit_task_schedule_check CHECK (
        (schedule_type = 'monthly' AND day_of_month BETWEEN 1 AND 28 AND day_of_week IS NULL AND validity_days IS NULL) OR
        (schedule_type = 'weekly' AND day_of_week BETWEEN 1 AND 7 AND day_of_month IS NULL AND validity_days IS NULL) OR
        (schedule_type = 'immediate' AND day_of_month IS NULL AND day_of_week IS NULL AND validity_days BETWEEN 1 AND 36500)
    );

ALTER TABLE recurring_credit_tasks
    DROP CONSTRAINT IF EXISTS recurring_credit_task_immediate_mode_check;

ALTER TABLE recurring_credit_tasks
    ADD CONSTRAINT recurring_credit_task_immediate_mode_check CHECK (
        schedule_type <> 'immediate' OR (
            execution_mode = 'finite' AND
            remaining_runs BETWEEN 0 AND 1 AND
            status IN ('active', 'completed', 'deleted')
        )
    );

ALTER TABLE recurring_credit_batches
    DROP CONSTRAINT IF EXISTS recurring_credit_batch_validity_check;

ALTER TABLE recurring_credit_batches
    ADD CONSTRAINT recurring_credit_batch_validity_check CHECK (
        (schedule_type = 'immediate' AND validity_days BETWEEN 1 AND 36500) OR
        (schedule_type IN ('monthly', 'weekly') AND validity_days IS NULL)
    );
