-- 解除 credential 对固定槽位表的写入依赖。
-- 旧列保留为迁移审计快照，新运行时使用 account_persona_id 定位授权链。
DO $$
DECLARE
    constraint_name TEXT;
BEGIN
    FOR constraint_name IN
        SELECT c.conname
        FROM pg_constraint c
        WHERE c.conrelid = 'openai_account_persona_credentials'::regclass
          AND c.contype = 'f'
          AND c.confrelid = 'openai_account_persona_slots'::regclass
    LOOP
        EXECUTE format('ALTER TABLE openai_account_persona_credentials DROP CONSTRAINT %I', constraint_name);
    END LOOP;
END
$$;

ALTER TABLE openai_account_persona_credentials
    ALTER COLUMN slot_id TYPE INT,
    ALTER COLUMN slot_id DROP NOT NULL;

UPDATE openai_account_persona_credentials
SET profile_id = persona
WHERE profile_id IS NULL;

COMMENT ON COLUMN openai_account_persona_credentials.slot_id IS
    '固定 v3 槽位迁移来源；动态 AccountPersona 新记录不依赖此列';
COMMENT ON COLUMN openai_account_persona_credentials.account_persona_id IS
    '动态 AccountPersona 授权链的运行时权威外键';
