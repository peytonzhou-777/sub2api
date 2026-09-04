-- AccountPersona 重新授权后，旧授权链进入 draining 供既有 Thread 在宽限期内续链。
-- 旧约束来自固定槽位时期，未包含该长期状态。
ALTER TABLE openai_account_persona_credentials
    DROP CONSTRAINT IF EXISTS openai_persona_credentials_state_check;

ALTER TABLE openai_account_persona_credentials
    ADD CONSTRAINT openai_persona_credentials_state_check
    CHECK (state IN ('pending', 'ready', 'refreshing', 'draining', 'invalid', 'revoked'));
