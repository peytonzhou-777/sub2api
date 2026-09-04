-- Persist the selected OpenAI Persona on usage and error records.
-- These are snapshots: historical rows remain readable after a Persona is retired.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS account_persona_id BIGINT,
    ADD COLUMN IF NOT EXISTS persona_profile VARCHAR(64);

ALTER TABLE ops_error_logs
    ADD COLUMN IF NOT EXISTS account_persona_id BIGINT,
    ADD COLUMN IF NOT EXISTS persona_profile VARCHAR(64);

CREATE INDEX IF NOT EXISTS idx_usage_logs_persona_time
    ON usage_logs (account_persona_id, created_at DESC)
    WHERE account_persona_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_error_logs_persona_time
    ON ops_error_logs (account_persona_id, created_at DESC)
    WHERE account_persona_id IS NOT NULL;
