package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetAccountRequestWindowStats 联合成功用量和失败日志，生成账号短窗口诊断指标。
func (r *opsRepository) GetAccountRequestWindowStats(ctx context.Context, accountID *int64) ([]*service.OpsAccountRequestWindowStats, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("nil ops repository")
	}
	query := `
WITH params AS (
  SELECT $1::timestamptz AS now_at, $2::bigint AS account_id
), windows(window_label, window_minutes) AS (
  VALUES ('1m', 1), ('5m', 5), ('30m', 30)
), raw_requests AS (
  SELECT
    ul.account_id,
    COALESCE(NULLIF(ul.request_id, ''), 'usage:' || ul.id::text) AS request_key,
    0 AS source_priority,
    COALESCE(ul.created_at - make_interval(secs => GREATEST(COALESCE(ul.duration_ms, 0), 0)::double precision / 1000.0), ul.created_at) AS started_at,
    ul.created_at AS ended_at,
    false AS failed,
    0 AS error_status,
    ''::text AS error_code,
    ''::text AS error_type,
    ''::text AS error_message,
    NULLIF(ul.session_scope_hash, '') AS session_scope_hash,
    NULLIF(ul.session_source_hash, '') AS session_source_hash,
    NULLIF(ul.prompt_cache_key_hash, '') AS prompt_cache_key_hash,
    NULL::integer AS observed_concurrency
  FROM usage_logs ul, params p
  WHERE ul.account_id IS NOT NULL
    AND ul.created_at >= p.now_at - INTERVAL '31 minutes'
    AND (p.account_id IS NULL OR ul.account_id = p.account_id)
  UNION ALL
  SELECT
    e.account_id,
    COALESCE(NULLIF(e.request_id, ''), NULLIF(e.client_request_id, ''), 'ops:' || e.id::text) AS request_key,
    CASE WHEN e.status_code >= 400 THEN 2 ELSE 1 END AS source_priority,
    COALESCE(e.request_started_at, e.created_at - make_interval(secs => GREATEST(COALESCE(e.duration_ms, 0), 0)::double precision / 1000.0), e.created_at) AS started_at,
    COALESCE(e.request_started_at, e.created_at) + make_interval(secs => GREATEST(COALESCE(e.duration_ms, 0), 0)::double precision / 1000.0) AS ended_at,
    e.status_code >= 400 AS failed,
    COALESCE(e.upstream_status_code, e.status_code, 0) AS error_status,
    COALESCE(e.upstream_error_code, '') AS error_code,
    COALESCE(e.upstream_error_type, '') AS error_type,
    COALESCE(e.error_message, '') AS error_message,
    NULLIF(e.session_scope_hash, '') AS session_scope_hash,
    NULLIF(e.session_source_hash, '') AS session_source_hash,
    NULLIF(e.prompt_cache_key_hash, '') AS prompt_cache_key_hash,
    e.account_concurrency AS observed_concurrency
  FROM ops_error_logs e, params p
  WHERE e.account_id IS NOT NULL
    AND e.created_at >= p.now_at - INTERVAL '31 minutes'
    AND (p.account_id IS NULL OR e.account_id = p.account_id)
), requests AS (
  SELECT
    account_id, started_at, ended_at, failed, error_status, error_code, error_type,
    error_message, session_scope_hash, session_source_hash, prompt_cache_key_hash,
    observed_concurrency
  FROM (
    SELECT
      raw_requests.*,
      ROW_NUMBER() OVER (
        PARTITION BY account_id, request_key
        ORDER BY source_priority DESC
      ) AS row_number
    FROM raw_requests
  ) ranked
  WHERE row_number = 1
), windowed AS (
  SELECT w.window_label, w.window_minutes, r.*
  FROM windows w
  CROSS JOIN params p
  JOIN requests r
    ON r.started_at >= p.now_at - make_interval(mins => w.window_minutes)
   AND r.started_at <= p.now_at
), account_scope AS (
  SELECT DISTINCT account_id FROM requests
  UNION
  SELECT account_id FROM params WHERE account_id IS NOT NULL
), account_windows AS (
  SELECT a.account_id, w.window_label, w.window_minutes
  FROM account_scope a CROSS JOIN windows w
), aggregates AS (
  SELECT
    aw.account_id,
    aw.window_label,
    aw.window_minutes,
    COUNT(w.account_id) AS request_count,
    COUNT(DISTINCT w.session_scope_hash) AS distinct_session_scopes,
    COUNT(DISTINCT w.session_source_hash) AS distinct_session_sources,
    COUNT(DISTINCT w.prompt_cache_key_hash) AS distinct_prompt_cache_keys,
    COUNT(w.account_id) FILTER (
      WHERE w.failed AND (
        w.error_status = 529 OR lower(w.error_code) LIKE '%overload%' OR
        lower(w.error_type) LIKE '%overload%' OR lower(w.error_message) LIKE '%overload%'
      )
    ) AS overload_count,
    COUNT(w.account_id) FILTER (WHERE w.failed AND w.error_status = 429) AS http_429_count,
    COUNT(w.account_id) FILTER (WHERE w.failed AND w.error_status BETWEEN 500 AND 599) AS http_5xx_count,
    COALESCE(MAX(w.observed_concurrency), 0) AS observed_peak_concurrency
  FROM account_windows aw
  LEFT JOIN windowed w
    ON w.account_id = aw.account_id AND w.window_label = aw.window_label
  GROUP BY aw.account_id, aw.window_label, aw.window_minutes
), concurrency_events AS (
  SELECT
    w.window_label,
    w.window_minutes,
    r.account_id,
    GREATEST(r.started_at, p.now_at - make_interval(mins => w.window_minutes)) AS at,
    1 AS delta
  FROM windows w CROSS JOIN params p JOIN requests r
    ON r.ended_at >= p.now_at - make_interval(mins => w.window_minutes)
   AND r.started_at <= p.now_at
  UNION ALL
  SELECT
    w.window_label,
    w.window_minutes,
    r.account_id,
    LEAST(r.ended_at, p.now_at) AS at,
    -1 AS delta
  FROM windows w CROSS JOIN params p JOIN requests r
    ON r.ended_at >= p.now_at - make_interval(mins => w.window_minutes)
   AND r.started_at <= p.now_at
), concurrency_event_totals AS (
	SELECT account_id, window_label, window_minutes, at, SUM(delta) AS delta
	FROM concurrency_events
	GROUP BY account_id, window_label, window_minutes, at
), concurrency_running AS (
  SELECT
    account_id,
    window_label,
    window_minutes,
    SUM(delta) OVER (
      PARTITION BY account_id, window_label
      ORDER BY at ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
    ) AS current_concurrency
  FROM concurrency_event_totals
), concurrency_peak AS (
  SELECT account_id, window_label, MAX(GREATEST(current_concurrency, 0)) AS peak_concurrency
  FROM concurrency_running
  GROUP BY account_id, window_label
)
SELECT
  a.account_id,
  a.window_label,
  a.window_minutes,
  a.request_count,
  GREATEST(COALESCE(cp.peak_concurrency, 0), a.observed_peak_concurrency) AS peak_concurrency,
  a.distinct_session_scopes,
  a.distinct_session_sources,
  a.distinct_prompt_cache_keys,
  a.overload_count,
  a.http_429_count,
  a.http_5xx_count,
  CASE WHEN a.request_count = 0 THEN 0 ELSE a.overload_count::double precision / a.request_count END,
  CASE WHEN a.request_count = 0 THEN 0 ELSE a.http_429_count::double precision / a.request_count END,
  CASE WHEN a.request_count = 0 THEN 0 ELSE a.http_5xx_count::double precision / a.request_count END
FROM aggregates a
LEFT JOIN concurrency_peak cp
  ON cp.account_id = a.account_id AND cp.window_label = a.window_label
ORDER BY a.account_id, a.window_minutes`

	var accountArg any
	if accountID != nil && *accountID > 0 {
		accountArg = *accountID
	}
	rows, err := r.db.QueryContext(ctx, query, time.Now().UTC(), accountArg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	result := make([]*service.OpsAccountRequestWindowStats, 0)
	for rows.Next() {
		item := &service.OpsAccountRequestWindowStats{}
		if err := rows.Scan(
			&item.AccountID, &item.Window, &item.WindowMinutes, &item.RequestCount,
			&item.PeakConcurrency, &item.DistinctSessionScopes, &item.DistinctSessionSources,
			&item.DistinctPromptCacheKeys, &item.OverloadCount, &item.HTTP429Count,
			&item.HTTP5xxCount, &item.OverloadErrorRate, &item.HTTP429ErrorRate,
			&item.HTTP5xxErrorRate,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	return result, nil
}
