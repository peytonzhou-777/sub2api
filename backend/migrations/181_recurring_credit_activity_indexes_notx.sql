CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_api_keys_last_used_user
    ON api_keys (last_used_at)
    INCLUDE (user_id)
    WHERE last_used_at IS NOT NULL;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_active_last_active
    ON users (last_active_at)
    INCLUDE (id)
    WHERE status = 'active'
      AND deleted_at IS NULL
      AND last_active_at IS NOT NULL;
