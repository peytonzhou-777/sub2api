package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// AcquireOpenAIConversationActivity 在有效绑定上登记在途请求，不续期业务活跃时间。
func (r *accountRepository) AcquireOpenAIConversationActivity(ctx context.Context, tr service.OpenAIUserConversationTransition, token string, until time.Time, personaToken ...string) (bool, error) {
	tokenValue := ""
	if len(personaToken) > 0 {
		tokenValue = personaToken[0]
	}
	result, err := r.sql.ExecContext(ctx, `WITH locked AS (
 SELECT id,account_id,slot_generation FROM openai_user_conversation_bindings
 WHERE id=$1 AND user_id=$2 AND api_key_id=$3 AND account_id=$4 AND slot_generation=$5
   AND binding_epoch=$6 AND status IN ('provisional','active','draining')
   AND openai_conversation_is_live(id,active_until,expires_at) FOR UPDATE
 ), held AS (
 INSERT INTO openai_conversation_request_holds(request_token,binding_id,account_id,slot_generation,expires_at,persona_reservation_token)
 SELECT $7::uuid,id,account_id,slot_generation,$8::timestamptz,NULLIF($9::text,'')::uuid FROM locked RETURNING binding_id
 ) UPDATE openai_user_conversation_bindings SET expires_at=GREATEST(expires_at,$8::timestamptz)
 WHERE id IN (SELECT binding_id FROM held)`, tr.BindingID, tr.UserID, tr.APIKeyID, tr.AccountID, tr.SlotGeneration, tr.BindingEpoch, token, until, tokenValue)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// RenewOpenAIConversationActivity 只延续尚未超时的在途凭据，失联实例不能复活旧绑定。
func (r *accountRepository) RenewOpenAIConversationActivity(ctx context.Context, token string, until time.Time) (bool, error) {
	result, err := r.sql.ExecContext(ctx, `WITH held AS (
 UPDATE openai_conversation_request_holds h SET expires_at=$2::timestamptz
 WHERE h.request_token=$1::uuid AND h.expires_at>NOW()
 AND EXISTS (SELECT 1 FROM openai_user_conversation_bindings b WHERE b.id=h.binding_id
             AND b.status IN ('provisional','active','draining')
             AND b.account_id=h.account_id AND b.slot_generation=h.slot_generation) RETURNING binding_id,persona_reservation_token
 ), persona_holds AS (
 UPDATE openai_persona_user_request_holds SET expires_at=GREATEST(expires_at,$2::timestamptz)
 WHERE reservation_token IN (SELECT persona_reservation_token FROM held)
 ) UPDATE openai_user_conversation_bindings SET expires_at=GREATEST(expires_at,$2::timestamptz)
 WHERE id IN (SELECT binding_id FROM held)`, token, until)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// ReleaseOpenAIConversationActivity 只释放本次请求令牌，不改变业务活跃截止时间。
func (r *accountRepository) ReleaseOpenAIConversationActivity(ctx context.Context, token string) error {
	_, err := r.sql.ExecContext(ctx, `DELETE FROM openai_conversation_request_holds WHERE request_token=$1::uuid`, token)
	return err
}

// HasExpiredOpenAIConversation 保留失效身份的拒绝证据，避免 alias 失效被当成新根。
func (r *accountRepository) HasExpiredOpenAIConversation(ctx context.Context, userID, apiKeyID int64, scope, hash string, aliases []service.OpenAIUserConversationAlias) (bool, error) {
	rows := make([]map[string]string, 0, len(aliases))
	for _, a := range aliases {
		rows = append(rows, map[string]string{"scope": a.ScopeKey, "kind": a.Type, "hash": a.Hash})
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return false, err
	}
	var expired bool
	err = scanSingleRow(ctx, r.sql, `SELECT EXISTS (
 SELECT 1 FROM openai_user_conversation_bindings b
 WHERE b.user_id=$1 AND b.api_key_id=$2 AND b.binding_epoch=$3
 AND b.first_output_committed AND (
    (b.scope_key=$4 AND b.conversation_hash=$5::char(64)) OR EXISTS (
      SELECT 1 FROM openai_user_conversation_aliases a,
      jsonb_to_recordset($6::jsonb) AS hints(scope text,kind text,hash text)
      WHERE a.binding_id=b.id AND a.user_id=$1 AND a.api_key_id=$2
      AND a.scope_key=hints.scope AND a.alias_type=hints.kind AND a.alias_hash=hints.hash))
 AND (b.status NOT IN ('active','draining') OR NOT openai_conversation_is_live(b.id,b.active_until,b.expires_at)))`,
		[]any{userID, apiKeyID, service.OpenAIConversationBindingEpoch, scope, hash, string(encoded)}, &expired)
	return expired, err
}

// syncOpenAIConversationUserLease 与当前 binding 使用同一成功时间，其他 Thread 不被续期。
func syncOpenAIConversationUserLease(ctx context.Context, exec sqlQueryExecutor, bindingID int64, now time.Time, ttl time.Duration) error {
	_, err := exec.ExecContext(ctx, `UPDATE openai_persona_active_user_leases l
 SET last_active_at=GREATEST(l.last_active_at,$2::timestamptz),
     active_until=GREATEST(l.active_until,$3::timestamptz),updated_at=$2::timestamptz
 FROM openai_user_conversation_bindings b WHERE b.id=$1
 AND l.account_persona_id=b.account_persona_id AND l.user_id=b.user_id AND l.state='active'`, bindingID, now, now.Add(ttl))
	return err
}

// TouchOpenAIConversationActivity 使用同一活动时间原子续期绑定、别名及精确 Persona 用户占用。
func (r *accountRepository) TouchOpenAIConversationActivity(ctx context.Context, tr service.OpenAIUserConversationTransition, now time.Time) (bool, error) {
	var count int
	err := scanSingleRow(ctx, r.sql, `WITH touched AS (
 UPDATE openai_user_conversation_bindings SET active_until=GREATEST(active_until,$7::timestamptz),
 expires_at=GREATEST(active_until,$7::timestamptz), last_success_at=GREATEST(last_success_at,$6::timestamptz),updated_at=$6::timestamptz
 WHERE id=$1 AND user_id=$2 AND api_key_id=$3 AND account_id=$4 AND slot_generation=$5
 AND status IN ('provisional','active','draining') AND openai_conversation_is_live(id,active_until,expires_at)
 RETURNING id,user_id,account_persona_id,active_until
 ), aliases AS (
 UPDATE openai_user_conversation_aliases a SET expires_at=t.active_until FROM touched t WHERE a.binding_id=t.id
 ), leases AS (
 UPDATE openai_persona_active_user_leases l SET last_active_at=GREATEST(last_active_at,$6::timestamptz),
 active_until=GREATEST(l.active_until,t.active_until),updated_at=$6::timestamptz FROM touched t
 WHERE l.account_persona_id=t.account_persona_id AND l.user_id=t.user_id AND l.state='active'
 ) SELECT COUNT(*) FROM touched`, []any{tr.BindingID, tr.UserID, tr.APIKeyID, tr.AccountID, tr.SlotGeneration, now, now.Add(tr.Config.ConversationActiveTTL())}, &count)
	return count == 1, err
}

var _ service.OpenAIConversationActivityStore = (*accountRepository)(nil)
