ALTER TABLE codex_fingerprint_session_scopes
    ADD COLUMN IF NOT EXISTS scope_version SMALLINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS slot_index SMALLINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS slot_count SMALLINT NOT NULL DEFAULT 1;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'codex_fingerprint_session_scopes_version_check'
    ) THEN
        ALTER TABLE codex_fingerprint_session_scopes
            ADD CONSTRAINT codex_fingerprint_session_scopes_version_check
            CHECK (scope_version BETWEEN 1 AND 2);
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'codex_fingerprint_session_scopes_slot_check'
    ) THEN
        ALTER TABLE codex_fingerprint_session_scopes
            ADD CONSTRAINT codex_fingerprint_session_scopes_slot_check
            CHECK (slot_count BETWEEN 1 AND 4 AND slot_index >= 0 AND slot_index < slot_count);
    END IF;
END $$;

COMMENT ON COLUMN codex_fingerprint_session_scopes.scope_version IS
    'Session scope 算法版本：1=旧 transport-sensitive，2=transport-neutral 槽位';
COMMENT ON COLUMN codex_fingerprint_session_scopes.slot_index IS
    '账号逻辑身份内的稳定 Session 槽位序号';
COMMENT ON COLUMN codex_fingerprint_session_scopes.slot_count IS
    '创建该 scope 时的账号 Session 槽位数，用于安全缩容与排空';
