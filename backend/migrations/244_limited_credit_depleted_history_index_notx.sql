CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_limited_credit_grants_depleted_history
    ON user_limited_credit_grants (user_id, updated_at DESC, id DESC)
    WHERE status = 'depleted';
