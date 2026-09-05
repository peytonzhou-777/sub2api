-- 执行前必须完成备份、目标快照和入口排空；默认只读预演。
-- psql -v group_id=4 -f expire_openai_conversations.sql
-- psql -v group_id=4 -v apply=true -v maintenance_confirmed=true -f expire_openai_conversations.sql
\set ON_ERROR_STOP on
\if :{?group_id}
\else
  \echo 'group_id is required'
  DO $guard$ BEGIN RAISE EXCEPTION 'conversation cleanup precondition failed; see diagnostic above'; END $guard$;
\endif
\if :{?apply}
\else
  \set apply false
\endif
\if :{?maintenance_confirmed}
\else
  \set maintenance_confirmed false
\endif

\if :apply
  \if :maintenance_confirmed
  \else
    \echo 'backup, snapshot and drained ingress must be confirmed first'
    DO $guard$ BEGIN RAISE EXCEPTION 'conversation cleanup precondition failed; see diagnostic above'; END $guard$;
  \endif
  BEGIN;
  SET LOCAL lock_timeout = '10s';
  SET LOCAL statement_timeout = '60s';
  -- 按固定顺序阻止绑定与 hold 并发写入；仅在排空窗口执行。
  LOCK TABLE openai_user_conversation_bindings IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE openai_conversation_request_holds IN SHARE ROW EXCLUSIVE MODE;
  LOCK TABLE openai_user_conversation_aliases IN SHARE ROW EXCLUSIVE MODE;
\else
  BEGIN READ ONLY;
  SET LOCAL statement_timeout = '30s';
\endif
SELECT EXISTS (SELECT 1 FROM groups WHERE id=:'group_id'::bigint AND platform='openai' AND deleted_at IS NULL)
 AND EXISTS (SELECT 1 FROM settings WHERE key='openai_user_affinity_scheduling'
             AND (value::jsonb->>'conversation_active_ttl_seconds')::bigint>0)
 AS valid_setup \gset
\if :valid_setup
\else
 ROLLBACK;
 \echo 'active OpenAI group and explicit positive activity TTL are required'
 DO $guard$ BEGIN RAISE EXCEPTION 'conversation cleanup precondition failed; see diagnostic above'; END $guard$;
\endif
SELECT NOW() AS cleanup_cutoff \gset

WITH cfg AS (
 SELECT COALESCE((value::jsonb->>'conversation_active_ttl_seconds')::bigint,2400) AS ttl
 FROM settings WHERE key='openai_user_affinity_scheduling'
), candidates AS (
 SELECT b.id,b.user_id,b.account_persona_id,
        LEAST(b.expires_at,COALESCE(b.active_until,COALESCE(b.last_success_at,b.created_at)+cfg.ttl*INTERVAL '1 second'),
              COALESCE(b.last_success_at,b.created_at)+cfg.ttl*INTERVAL '1 second') AS deadline
 FROM openai_user_conversation_bindings b CROSS JOIN cfg
 WHERE b.scope_key LIKE 'openai:v1:group:' || :'group_id'::bigint::text || ':%'
   AND b.binding_epoch=2 AND b.status IN ('active','draining')
)
SELECT COUNT(*) AS bindings,
       COUNT(*) FILTER (WHERE deadline<=:'cleanup_cutoff'::timestamptz) AS expired_by_activity,
       COUNT(DISTINCT user_id) FILTER (WHERE deadline<=:'cleanup_cutoff'::timestamptz) AS affected_users,
       COUNT(*) FILTER (WHERE openai_conversation_has_activity(id)) AS protected_by_hold
FROM candidates;

\if :apply
  -- 任意目标在途 hold 均使整次写入失败，不猜测其对应客户端是否仍在执行。
  SELECT NOT EXISTS (
    SELECT 1 FROM openai_user_conversation_bindings b
    WHERE b.scope_key LIKE 'openai:v1:group:' || :'group_id'::bigint::text || ':%'
      AND b.status IN ('provisional','active','draining') AND openai_conversation_has_activity(b.id)
  ) AS drained \gset
  \if :drained
  \else
    ROLLBACK;
    \echo 'active conversation holds remain; cleanup aborted'
    DO $guard$ BEGIN RAISE EXCEPTION 'conversation cleanup precondition failed; see diagnostic above'; END $guard$;
  \endif

  CREATE TEMP TABLE conversation_cleanup_targets ON COMMIT DROP AS
  SELECT b.id,b.user_id,b.account_persona_id,
         LEAST(b.expires_at,COALESCE(b.active_until,COALESCE(b.last_success_at,b.created_at)+cfg.ttl*INTERVAL '1 second'),
               COALESCE(b.last_success_at,b.created_at)+cfg.ttl*INTERVAL '1 second') AS deadline
  FROM openai_user_conversation_bindings b CROSS JOIN (
    SELECT (value::jsonb->>'conversation_active_ttl_seconds')::bigint AS ttl
    FROM settings WHERE key='openai_user_affinity_scheduling'
  ) cfg
  WHERE b.scope_key LIKE 'openai:v1:group:' || :'group_id'::bigint::text || ':%'
    AND b.binding_epoch=2 AND b.status IN ('active','draining');

  UPDATE openai_user_conversation_bindings b
  SET active_until=t.deadline,expires_at=t.deadline,
      status=CASE WHEN t.deadline<=:'cleanup_cutoff'::timestamptz THEN 'expired' ELSE b.status END,
      pending_resident_slot_id=CASE WHEN t.deadline<=:'cleanup_cutoff'::timestamptz THEN NULL ELSE b.pending_resident_slot_id END,
      pending_account_id=CASE WHEN t.deadline<=:'cleanup_cutoff'::timestamptz THEN NULL ELSE b.pending_account_id END,
      pending_slot_generation=CASE WHEN t.deadline<=:'cleanup_cutoff'::timestamptz THEN NULL ELSE b.pending_slot_generation END,
      pending_token=CASE WHEN t.deadline<=:'cleanup_cutoff'::timestamptz THEN NULL ELSE b.pending_token END,
      pending_expires_at=CASE WHEN t.deadline<=:'cleanup_cutoff'::timestamptz THEN NULL ELSE b.pending_expires_at END,
      updated_at=:'cleanup_cutoff'::timestamptz
  FROM conversation_cleanup_targets t WHERE b.id=t.id
    AND (b.expires_at<>t.deadline OR b.active_until IS DISTINCT FROM t.deadline OR t.deadline<=:'cleanup_cutoff'::timestamptz);

  UPDATE openai_user_conversation_aliases a SET expires_at=LEAST(a.expires_at,t.deadline)
  FROM conversation_cleanup_targets t WHERE a.binding_id=t.id AND a.expires_at>t.deadline;

  -- 仅回收自身已过期且没有其他活动的共享占用，不能因某个 Thread 过期影响同用户其他会话。
  UPDATE openai_persona_active_user_leases l SET state='expired',updated_at=:'cleanup_cutoff'::timestamptz
  WHERE l.state='active' AND l.active_until<=:'cleanup_cutoff'::timestamptz
    AND EXISTS (SELECT 1 FROM conversation_cleanup_targets t WHERE t.user_id=l.user_id AND t.account_persona_id=l.account_persona_id)
    AND NOT EXISTS (SELECT 1 FROM openai_persona_user_request_holds h WHERE h.lease_id=l.id AND h.expires_at>:'cleanup_cutoff'::timestamptz)
    AND NOT EXISTS (SELECT 1 FROM openai_user_conversation_bindings b WHERE b.user_id=l.user_id AND b.account_persona_id=l.account_persona_id
                    AND b.status IN ('provisional','active','draining') AND openai_conversation_is_live(b.id,b.active_until,b.expires_at));

  SELECT COUNT(*) AS expired_bindings FROM conversation_cleanup_targets WHERE deadline<=:'cleanup_cutoff'::timestamptz;
  SELECT NOT EXISTS (
    SELECT 1 FROM conversation_cleanup_targets t JOIN openai_user_conversation_bindings b ON b.id=t.id
    WHERE b.expires_at<>t.deadline OR (t.deadline<=:'cleanup_cutoff'::timestamptz AND b.status<>'expired')
  ) AND NOT EXISTS (
    SELECT 1 FROM conversation_cleanup_targets t JOIN openai_user_conversation_aliases a ON a.binding_id=t.id WHERE a.expires_at>t.deadline
  ) AS verified \gset
  \if :verified
    COMMIT;
  \else
    ROLLBACK;
    \echo 'cleanup assertions failed; transaction rolled back'
    DO $guard$ BEGIN RAISE EXCEPTION 'conversation cleanup precondition failed; see diagnostic above'; END $guard$;
  \endif
\else
  COMMIT;
\endif
