-- 将多槽位的新会话选择从“活跃会话占槽”调整为用户级单一活动路由。
CREATE TABLE IF NOT EXISTS openai_user_active_routes (
    user_id                     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_key                   VARCHAR(255) NOT NULL,
    resident_slot_id            BIGINT REFERENCES openai_user_resident_slots(id) ON DELETE CASCADE,
    account_id                  BIGINT REFERENCES accounts(id) ON DELETE CASCADE,
    slot_generation             BIGINT,
    claimed_at                  TIMESTAMPTZ,
    active_until                TIMESTAMPTZ,
    pending_resident_slot_id    BIGINT REFERENCES openai_user_resident_slots(id) ON DELETE CASCADE,
    pending_account_id          BIGINT REFERENCES accounts(id) ON DELETE CASCADE,
    pending_slot_generation     BIGINT,
    pending_claimed_at          TIMESTAMPTZ,
    pending_token               VARCHAR(64),
    pending_expires_at          TIMESTAMPTZ,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, scope_key),
    CONSTRAINT openai_user_active_routes_current_complete CHECK (
        (account_id IS NULL AND resident_slot_id IS NULL AND slot_generation IS NULL AND claimed_at IS NULL AND active_until IS NULL)
        OR
        (account_id IS NOT NULL AND resident_slot_id IS NOT NULL AND slot_generation IS NOT NULL AND
         slot_generation > 0 AND claimed_at IS NOT NULL AND active_until IS NOT NULL)
    ),
    CONSTRAINT openai_user_active_routes_pending_complete CHECK (
        (pending_account_id IS NULL AND pending_resident_slot_id IS NULL AND pending_slot_generation IS NULL AND
         pending_claimed_at IS NULL AND pending_token IS NULL AND pending_expires_at IS NULL)
        OR
        (pending_account_id IS NOT NULL AND pending_resident_slot_id IS NOT NULL AND pending_slot_generation IS NOT NULL AND
         pending_slot_generation > 0 AND pending_claimed_at IS NOT NULL AND pending_token IS NOT NULL AND pending_expires_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_openai_user_active_routes_current_account
    ON openai_user_active_routes(account_id, active_until, claimed_at, user_id)
    WHERE account_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_openai_user_active_routes_pending_account
    ON openai_user_active_routes(pending_account_id, pending_expires_at, pending_claimed_at, user_id)
    WHERE pending_account_id IS NOT NULL;
