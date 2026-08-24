-- 为 Codex 根线程和派生线程增加只保存 HMAC 的长期会话别名。
ALTER TABLE openai_user_conversation_aliases
    DROP CONSTRAINT IF EXISTS openai_user_conversation_aliases_type_check;

ALTER TABLE openai_user_conversation_aliases
    ADD CONSTRAINT openai_user_conversation_aliases_type_check CHECK (
        alias_type IN (
            'previous_response_id', 'response_id', 'session_id',
            'prompt_cache_key', 'websocket', 'codex_thread'
        )
    );
