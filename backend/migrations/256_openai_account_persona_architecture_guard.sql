-- 动态 AccountPersona 整组切换门。数据迁移命令在全部账号完成后将状态推进为 ready。
CREATE TABLE IF NOT EXISTS openai_persona_architecture_state (
    singleton            BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    architecture_version VARCHAR(64) NOT NULL,
    state                VARCHAR(16) NOT NULL,
    migration_report     JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_persona_architecture_state_check CHECK (state IN ('pending', 'ready'))
);

INSERT INTO openai_persona_architecture_state (singleton, architecture_version, state)
VALUES (TRUE, 'account_persona_v1', 'pending')
ON CONFLICT (singleton) DO NOTHING;

CREATE OR REPLACE FUNCTION reject_legacy_openai_persona_slot_write()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM openai_persona_architecture_state
        WHERE singleton = TRUE AND architecture_version = 'account_persona_v1' AND state = 'ready'
    ) THEN
        RAISE EXCEPTION 'legacy OpenAI Persona slot writes are disabled after AccountPersona activation'
            USING ERRCODE = '55000';
    END IF;
    RETURN NULL;
END
$$;

DROP TRIGGER IF EXISTS trg_reject_legacy_openai_persona_slots ON openai_account_persona_slots;
CREATE TRIGGER trg_reject_legacy_openai_persona_slots
BEFORE INSERT OR UPDATE OR DELETE ON openai_account_persona_slots
FOR EACH STATEMENT EXECUTE FUNCTION reject_legacy_openai_persona_slot_write();

CREATE OR REPLACE FUNCTION reject_openai_account_top_level_token_write()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.platform = 'openai' AND NEW.type = 'oauth'
       AND EXISTS (
           SELECT 1 FROM openai_persona_architecture_state
           WHERE singleton = TRUE AND architecture_version = 'account_persona_v1' AND state = 'ready'
       )
       AND (
           (TG_OP = 'INSERT' AND (
               COALESCE(NEW.credentials->>'access_token', '') <> ''
               OR COALESCE(NEW.credentials->>'refresh_token', '') <> ''
               OR COALESCE(NEW.credentials->>'id_token', '') <> ''
               OR COALESCE(NEW.credentials->>'expires_at', '') <> ''
               OR COALESCE(NEW.credentials->>'client_id', '') <> ''
           ))
           OR (TG_OP = 'UPDATE' AND (
               COALESCE(NEW.credentials->>'access_token', '') <> COALESCE(OLD.credentials->>'access_token', '')
               OR COALESCE(NEW.credentials->>'refresh_token', '') <> COALESCE(OLD.credentials->>'refresh_token', '')
               OR COALESCE(NEW.credentials->>'id_token', '') <> COALESCE(OLD.credentials->>'id_token', '')
               OR COALESCE(NEW.credentials->>'expires_at', '') <> COALESCE(OLD.credentials->>'expires_at', '')
               OR COALESCE(NEW.credentials->>'client_id', '') <> COALESCE(OLD.credentials->>'client_id', '')
           ))
       ) THEN
        RAISE EXCEPTION 'OpenAI OAuth runtime tokens belong to AccountPersona credentials'
            USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS trg_reject_openai_account_top_level_token_write ON accounts;
CREATE TRIGGER trg_reject_openai_account_top_level_token_write
BEFORE INSERT OR UPDATE OF credentials ON accounts
FOR EACH ROW EXECUTE FUNCTION reject_openai_account_top_level_token_write();

COMMENT ON TABLE openai_persona_architecture_state IS
    'AccountPersona 架构维护窗口切换门；ready 后固定槽位和账号顶层 Token 不再是运行时权威';
