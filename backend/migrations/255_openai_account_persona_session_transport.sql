-- Persona Session 需要冻结代理快照，避免代理原地编辑后旧 epoch 复用新出口。
ALTER TABLE openai_account_persona_sessions
    ADD COLUMN IF NOT EXISTS effective_proxy_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS installation_id VARCHAR(256) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS proxy_snapshot_set BOOLEAN NOT NULL DEFAULT FALSE;

UPDATE openai_account_persona_sessions session
SET installation_id = persona.installation_id
FROM openai_account_personas persona
WHERE session.account_persona_id = persona.id
  AND session.installation_id = '';

CREATE INDEX IF NOT EXISTS idx_openai_account_persona_sessions_lookup
    ON openai_account_persona_sessions(account_persona_id, session_epoch, state, expires_at);

COMMENT ON COLUMN openai_account_persona_sessions.effective_proxy_url IS
    'epoch 创建时冻结的完整代理 URL；只供出站 Transport 使用，禁止管理 API 和日志返回';
COMMENT ON COLUMN openai_account_persona_sessions.installation_id IS
    'epoch 创建时冻结的应用安装身份；历史 continuation 不读取 Persona 当前值';
COMMENT ON COLUMN openai_account_persona_sessions.proxy_snapshot_set IS
    '区分已冻结直连与迁移后尚未初始化的代理快照';
