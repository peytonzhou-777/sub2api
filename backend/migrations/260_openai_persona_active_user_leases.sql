-- OpenAI Persona capacity changes from client Sessions to active users.
-- Legacy Session lease tables stay in place as rollback data.

ALTER TABLE openai_account_personas
    ADD COLUMN IF NOT EXISTS max_active_users_override INT;

UPDATE openai_account_personas
SET max_active_users_override = max_active_client_sessions_override
WHERE max_active_users_override IS NULL
  AND max_active_client_sessions_override IS NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'openai_account_personas_user_limit_check'
    ) THEN
        ALTER TABLE openai_account_personas
            ADD CONSTRAINT openai_account_personas_user_limit_check CHECK (
                max_active_users_override IS NULL OR max_active_users_override >= 1
            );
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS openai_persona_active_user_leases (
    id                  BIGSERIAL PRIMARY KEY,
    account_persona_id  BIGINT NOT NULL REFERENCES openai_account_personas(id) ON DELETE CASCADE,
    account_id          BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    state               VARCHAR(16) NOT NULL DEFAULT 'provisional',
    generation          BIGINT NOT NULL DEFAULT 1,
    last_active_at      TIMESTAMPTZ,
    active_until        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT openai_persona_active_user_leases_state_check
        CHECK (state IN ('provisional', 'active', 'expired', 'revoked')),
    CONSTRAINT openai_persona_active_user_leases_generation_check CHECK (generation > 0),
    CONSTRAINT openai_persona_active_user_leases_scope_unique UNIQUE (account_persona_id, user_id)
);

CREATE TABLE IF NOT EXISTS openai_persona_user_request_holds (
    reservation_token   UUID PRIMARY KEY,
    lease_id            BIGINT NOT NULL REFERENCES openai_persona_active_user_leases(id) ON DELETE CASCADE,
    account_persona_id  BIGINT NOT NULL REFERENCES openai_account_personas(id) ON DELETE CASCADE,
    expires_at          TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_openai_persona_active_user_leases_capacity
    ON openai_persona_active_user_leases(account_persona_id, state, active_until);
CREATE INDEX IF NOT EXISTS idx_openai_persona_active_user_leases_user
    ON openai_persona_active_user_leases(user_id, state, active_until);
CREATE INDEX IF NOT EXISTS idx_openai_persona_user_request_holds_expiry
    ON openai_persona_user_request_holds(account_persona_id, expires_at);

-- Collapse every live legacy Session lease into one Persona x User lease.
INSERT INTO openai_persona_active_user_leases
    (account_persona_id, account_id, user_id, state, generation,
     last_active_at, active_until, created_at, updated_at)
SELECT lease.account_persona_id,
       MIN(lease.account_id),
       lease.user_id,
       CASE WHEN BOOL_OR(lease.state = 'active' AND lease.active_until > NOW())
            THEN 'active' ELSE 'provisional' END,
       GREATEST(MAX(lease.generation), 1),
       MAX(lease.last_active_at),
       MAX(lease.active_until),
       MIN(lease.created_at),
       MAX(lease.updated_at)
FROM openai_persona_client_session_leases lease
WHERE (lease.state = 'active' AND lease.active_until > NOW())
   OR EXISTS (
       SELECT 1 FROM openai_persona_request_holds hold
       WHERE hold.lease_id = lease.id AND hold.expires_at > NOW()
   )
GROUP BY lease.account_persona_id, lease.user_id
ON CONFLICT (account_persona_id, user_id) DO UPDATE SET
    state = CASE
        WHEN openai_persona_active_user_leases.state = 'active'
          OR EXCLUDED.state = 'active' THEN 'active'
        ELSE EXCLUDED.state
    END,
    generation = GREATEST(openai_persona_active_user_leases.generation, EXCLUDED.generation),
    last_active_at = GREATEST(openai_persona_active_user_leases.last_active_at, EXCLUDED.last_active_at),
    active_until = GREATEST(openai_persona_active_user_leases.active_until, EXCLUDED.active_until),
    updated_at = GREATEST(openai_persona_active_user_leases.updated_at, EXCLUDED.updated_at);

INSERT INTO openai_persona_user_request_holds
    (reservation_token, lease_id, account_persona_id, expires_at, created_at)
SELECT hold.reservation_token,
       user_lease.id,
       hold.account_persona_id,
       hold.expires_at,
       hold.created_at
FROM openai_persona_request_holds hold
JOIN openai_persona_client_session_leases session_lease ON session_lease.id = hold.lease_id
JOIN openai_persona_active_user_leases user_lease
  ON user_lease.account_persona_id = session_lease.account_persona_id
 AND user_lease.user_id = session_lease.user_id
WHERE hold.expires_at > NOW()
ON CONFLICT (reservation_token) DO NOTHING;

COMMENT ON TABLE openai_persona_active_user_leases IS
    'AccountPersona x User active occupancy; API keys and client Sessions share one seat';
COMMENT ON TABLE openai_persona_user_request_holds IS
    'Short request holds for Persona user seats';
