ALTER TABLE reset_rebate_batches
    ADD COLUMN IF NOT EXISTS rebate_reason VARCHAR(100) NOT NULL DEFAULT '';
