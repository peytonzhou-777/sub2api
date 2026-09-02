-- Persona v3 OAuth 凭据改由独立表加密保存。
-- 旧 JSON 内嵌链无法证明旋转版本一致性，统一本地撤销并要求管理员重新授权。
UPDATE openai_account_persona_credentials
SET credentials = '{}'::jsonb,
    token_version = token_version + 1,
    state = 'revoked',
    last_error = 'reauthorization required after credential authority migration',
    updated_at = NOW()
WHERE state <> 'revoked';

UPDATE openai_account_persona_slots
SET credential_chain_id = NULL,
    authorized = FALSE,
    updated_at = NOW();

UPDATE accounts
SET credentials = credentials
        - 'persona_credentials'
        - 'oauth_credential_chains'
        - 'openai_persona_slot_active_chain_ids',
    extra = extra
        - 'openai_persona_slot_active_chain_ids'
        - 'openai_persona_slot_authorized'
        - 'openai_persona_slot_installation_ids',
    updated_at = NOW()
WHERE platform = 'openai' AND type = 'oauth';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'openai_persona_credentials_encrypted_payload_check'
          AND conrelid = 'openai_account_persona_credentials'::regclass
    ) THEN
        ALTER TABLE openai_account_persona_credentials
            ADD CONSTRAINT openai_persona_credentials_encrypted_payload_check CHECK (
                state NOT IN ('ready', 'refreshing')
                OR (
                    COALESCE(credentials->>'format_version', '') = '1'
                    AND COALESCE(btrim(credentials->>'ciphertext'), '') <> ''
                )
            );
    END IF;
END
$$;

COMMENT ON COLUMN openai_account_persona_credentials.credentials IS
    'AES-256-GCM 加密封装；ready/refreshing 状态禁止保存明文 OAuth Token';
