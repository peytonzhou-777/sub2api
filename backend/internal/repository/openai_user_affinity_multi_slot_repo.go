package repository

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.OpenAIUserAffinityMultiSlotStore = (*accountRepository)(nil)
var _ service.OpenAIUserAffinityConversationStore = (*accountRepository)(nil)
var _ service.OpenAIUserAffinityConversationFailoverStore = (*accountRepository)(nil)
var _ service.OpenAIUserAffinityResidentSlotMaintenanceStore = (*accountRepository)(nil)
var _ service.OpenAIUserAffinityResetExclusionStore = (*accountRepository)(nil)

func openAIUserAffinityProvisionalTTL(config service.OpenAIUserAffinityConfig) time.Duration {
	ttl := config.ConversationActiveTTL()
	if ttl < 2*time.Minute {
		return 2 * time.Minute
	}
	return ttl
}

// ListOpenAIUserAffinityResetExcludedAccountIDs 返回尚未被新居民首次成功落槽消费的账号集合。
func (r *accountRepository) ListOpenAIUserAffinityResetExcludedAccountIDs(ctx context.Context, userID int64, scopeKey string) ([]int64, error) {
	if r == nil || r.sql == nil || userID <= 0 {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT DISTINCT account_id FROM openai_user_affinity_reset_exclusions
		WHERE user_id = $1 AND scope_key = $2 AND consumed_at IS NULL
		ORDER BY account_id`, userID, normalizeOpenAIUserAffinityScopeKey(scopeKey))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	accountIDs := make([]int64, 0)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return nil, err
		}
		accountIDs = append(accountIDs, accountID)
	}
	return accountIDs, rows.Err()
}

// ConvergeOpenAIUserResidentSlots 按最新 TTL 和槽位数将过期或低热度槽位转入非接新会话状态。
func (r *accountRepository) ConvergeOpenAIUserResidentSlots(ctx context.Context, userID int64, scopeKey string, config service.OpenAIUserAffinityConfig, now time.Time) error {
	if r == nil || r.client == nil || r.sql == nil || userID <= 0 {
		return errors.New("openai user affinity storage unavailable")
	}
	scopeKey = normalizeOpenAIUserAffinityScopeKey(scopeKey)
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lockKey := fmt.Sprintf("%d:%s", userID, scopeKey)
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return err
	}
	residentTTLSeconds := int64(config.ResidentTTL().Seconds())
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_resident_slots SET
			expires_at = LEAST(expires_at, COALESCE(last_success_at, admitted_at) + ($4::bigint * INTERVAL '1 second')),
			status = CASE
				WHEN COALESCE(last_success_at, admitted_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN 'expired'
				ELSE status
			END,
			provisional_token = CASE
				WHEN COALESCE(last_success_at, admitted_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN NULL
				ELSE provisional_token
			END,
			updated_at = $3
		WHERE user_id = $1 AND scope_key = $2
		  AND status IN ('provisional', 'active', 'replacement_pending')`,
		userID, scopeKey, now, residentTTLSeconds); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			expires_at = LEAST(expires_at, COALESCE(last_success_at, created_at) + ($4::bigint * INTERVAL '1 second')),
			active_until = CASE WHEN active_until IS NULL THEN NULL ELSE
				LEAST(active_until, COALESCE(last_success_at, created_at) + ($5::bigint * INTERVAL '1 second')) END,
			status = CASE
				WHEN COALESCE(last_success_at, created_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN 'expired'
				ELSE status
			END,
			provisional_token = CASE
				WHEN COALESCE(last_success_at, created_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN NULL
				ELSE provisional_token
			END,
			pending_resident_slot_id = CASE WHEN COALESCE(last_success_at, created_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN NULL ELSE pending_resident_slot_id END,
			pending_account_id = CASE WHEN COALESCE(last_success_at, created_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN NULL ELSE pending_account_id END,
			pending_slot_generation = CASE WHEN COALESCE(last_success_at, created_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN NULL ELSE pending_slot_generation END,
			pending_token = CASE WHEN COALESCE(last_success_at, created_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN NULL ELSE pending_token END,
			pending_expires_at = CASE WHEN COALESCE(last_success_at, created_at) + ($4::bigint * INTERVAL '1 second') <= $3 THEN NULL ELSE pending_expires_at END,
			updated_at = $3
		WHERE user_id = $1 AND scope_key = $2 AND status IN ('provisional', 'active', 'draining')`,
		userID, scopeKey, now, residentTTLSeconds, int64(config.ConversationActiveTTL().Seconds())); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings b SET status = 'expired', expires_at = $3,
			pending_resident_slot_id = NULL, pending_account_id = NULL, pending_slot_generation = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $3
		FROM openai_user_resident_slots s
		WHERE b.resident_slot_id = s.id AND s.user_id = $1 AND s.scope_key = $2
		  AND s.status = 'expired' AND b.status IN ('active', 'draining')`, userID, scopeKey, now); err != nil {
		return err
	}
	if config.RuntimeResidentAccountSlotCount() > 1 {
		// 多槽位 projection 必须由同账号同 generation 的 live slot 支撑；
		// slot 收敛后及时过期孤立 placement，避免后续请求反复命中旧账号。
		if _, err := exec.ExecContext(ctx, `
			UPDATE user_account_placements p SET status = 'expired', expires_at = $3,
				provisional_token = NULL, updated_at = $3
			WHERE p.user_id = $1 AND p.scope_key = $2 AND p.status = 'active'
			  AND EXISTS (
				SELECT 1 FROM openai_user_resident_slots existing
				WHERE existing.user_id = p.user_id AND existing.scope_key = p.scope_key
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM openai_user_resident_slots s
				WHERE s.user_id = p.user_id AND s.scope_key = p.scope_key
				  AND s.account_id = p.account_id AND s.generation = p.generation
				  AND s.status IN ('provisional', 'active', 'replacement_pending')
				  AND s.expires_at > $3
			  )`, userID, scopeKey, now); err != nil {
			return err
		}
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_aliases a SET expires_at = LEAST(a.expires_at, b.expires_at)
		FROM openai_user_conversation_bindings b
		WHERE a.binding_id = b.id AND b.user_id = $1 AND b.scope_key = $2`, userID, scopeKey); err != nil {
		return err
	}
	maxSlots := config.RuntimeResidentAccountSlotCount()
	rows, err := exec.QueryContext(ctx, `
		SELECT id, account_id, generation FROM openai_user_resident_slots
		WHERE user_id = $1 AND scope_key = $2 AND status = 'active' AND expires_at > $3
		ORDER BY usage_score * POWER(0.5, GREATEST(EXTRACT(EPOCH FROM ($3 - score_updated_at)), 0) / $4::double precision) DESC,
		         last_success_at DESC NULLS LAST, admitted_at, account_id
		OFFSET $5 FOR UPDATE`, userID, scopeKey, now, config.ResidentTTL().Seconds(), maxSlots)
	if err != nil {
		return err
	}
	type drainingSlot struct {
		id, accountID, generation int64
	}
	drainingSlots := make([]drainingSlot, 0)
	for rows.Next() {
		var slot drainingSlot
		if err := rows.Scan(&slot.id, &slot.accountID, &slot.generation); err != nil {
			_ = rows.Close()
			return err
		}
		drainingSlots = append(drainingSlots, slot)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, slot := range drainingSlots {
		if _, err := exec.ExecContext(ctx, `
			UPDATE openai_user_resident_slots SET status = 'draining', updated_at = $2
			WHERE id = $1 AND status = 'active'`, slot.id, now); err != nil {
			return err
		}
		if _, err := exec.ExecContext(ctx, `
			UPDATE openai_user_conversation_bindings SET status = 'draining', updated_at = $2
			WHERE resident_slot_id = $1 AND status = 'active'`, slot.id, now); err != nil {
			return err
		}
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO user_account_placement_events
				(user_id, scope_key, placement_generation, source_account_id, event_type,
				 reason, config_version, effective_source, resident_slot_id)
			VALUES ($1, $2, $3, $4, 'slot_draining', 'slot_count_reduced', $5, 'global', $6)`,
			userID, scopeKey, slot.generation, slot.accountID, config.ConfigVersion, slot.id); err != nil {
			return err
		}
	}
	if err := cleanupOpenAIUserActiveRoute(ctx, exec, userID, scopeKey, now); err != nil {
		return err
	}
	if tx != nil {
		return tx.Commit()
	}
	return nil
}

// EvictOpenAIUserResidentSlot 将确认不可恢复的账号从常驻槽位中摘除，并释放该槽位的接入资格。
func (r *accountRepository) EvictOpenAIUserResidentSlot(ctx context.Context, userID int64, scopeKey string, slotID, accountID, generation int64, reason string, now time.Time) (bool, error) {
	if r == nil || r.client == nil || r.sql == nil || userID <= 0 || slotID <= 0 || accountID <= 0 || generation <= 0 {
		return false, errors.New("openai user affinity storage unavailable")
	}
	scopeKey = normalizeOpenAIUserAffinityScopeKey(scopeKey)
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now = now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lockKey := fmt.Sprintf("%d:%s", userID, scopeKey)
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return false, err
	}
	var currentAccountID, currentGeneration int64
	var status string
	err = scanSingleRow(ctx, exec, `
		SELECT account_id, generation, status
		FROM openai_user_resident_slots
		WHERE id = $1 AND user_id = $2 AND scope_key = $3
		FOR UPDATE`, []any{slotID, userID, scopeKey}, &currentAccountID, &currentGeneration, &status)
	if errors.Is(err, sql.ErrNoRows) {
		if tx != nil {
			if commitErr := tx.Commit(); commitErr != nil {
				return false, commitErr
			}
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if currentAccountID != accountID || currentGeneration != generation ||
		(status != service.OpenAIUserResidentSlotStatusActive &&
			status != service.OpenAIUserResidentSlotStatusProvisional &&
			status != service.OpenAIUserResidentSlotStatusReplacementPending) {
		if tx != nil {
			if commitErr := tx.Commit(); commitErr != nil {
				return false, commitErr
			}
		}
		return false, nil
	}
	result, err := exec.ExecContext(ctx, `
		UPDATE openai_user_resident_slots SET status = 'draining', provisional_token = NULL, updated_at = $6
		WHERE id = $1 AND user_id = $2 AND scope_key = $3 AND account_id = $4 AND generation = $5
		  AND status IN ('provisional', 'active', 'replacement_pending')`,
		slotID, userID, scopeKey, accountID, generation, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		if tx != nil {
			if commitErr := tx.Commit(); commitErr != nil {
				return false, commitErr
			}
		}
		return false, nil
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET status = 'draining', updated_at = $4
		WHERE user_id = $1 AND scope_key = $2 AND resident_slot_id = $3 AND status = 'active'`,
		userID, scopeKey, slotID, now); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_active_routes SET
			resident_slot_id = CASE WHEN resident_slot_id = $3 AND account_id = $4 AND slot_generation = $5 THEN NULL ELSE resident_slot_id END,
			account_id = CASE WHEN resident_slot_id = $3 AND account_id = $4 AND slot_generation = $5 THEN NULL ELSE account_id END,
			slot_generation = CASE WHEN resident_slot_id = $3 AND account_id = $4 AND slot_generation = $5 THEN NULL ELSE slot_generation END,
			claimed_at = CASE WHEN resident_slot_id = $3 AND account_id = $4 AND slot_generation = $5 THEN NULL ELSE claimed_at END,
			active_until = CASE WHEN resident_slot_id = $3 AND account_id = $4 AND slot_generation = $5 THEN NULL ELSE active_until END,
			pending_resident_slot_id = CASE WHEN pending_resident_slot_id = $3 AND pending_account_id = $4 AND pending_slot_generation = $5 THEN NULL ELSE pending_resident_slot_id END,
			pending_account_id = CASE WHEN pending_resident_slot_id = $3 AND pending_account_id = $4 AND pending_slot_generation = $5 THEN NULL ELSE pending_account_id END,
			pending_slot_generation = CASE WHEN pending_resident_slot_id = $3 AND pending_account_id = $4 AND pending_slot_generation = $5 THEN NULL ELSE pending_slot_generation END,
			pending_claimed_at = CASE WHEN pending_resident_slot_id = $3 AND pending_account_id = $4 AND pending_slot_generation = $5 THEN NULL ELSE pending_claimed_at END,
			pending_token = CASE WHEN pending_resident_slot_id = $3 AND pending_account_id = $4 AND pending_slot_generation = $5 THEN NULL ELSE pending_token END,
			pending_expires_at = CASE WHEN pending_resident_slot_id = $3 AND pending_account_id = $4 AND pending_slot_generation = $5 THEN NULL ELSE pending_expires_at END,
			updated_at = $6
		WHERE user_id = $1 AND scope_key = $2`,
		userID, scopeKey, slotID, accountID, generation, now); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE account_user_contacts SET reservation_kind = NULL, reservation_token = NULL,
			reservation_until = NULL, updated_at = $3
		WHERE account_id = $1 AND user_id = $2`, accountID, userID, now); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		DELETE FROM openai_user_active_routes
		WHERE user_id = $1 AND scope_key = $2 AND account_id IS NULL AND pending_account_id IS NULL`, userID, scopeKey); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, source_account_id, event_type,
			 reason, effective_source, resident_slot_id)
		VALUES ($1, $2, $3, $4, 'slot_evicted', $5, 'global', $6)`,
		userID, scopeKey, generation, accountID, strings.TrimSpace(reason), slotID); err != nil {
		return false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}

// ListOpenAIUserResidentSlots 从权威数据库读取用户在单个 scope 下的槽位。
func (r *accountRepository) ListOpenAIUserResidentSlots(ctx context.Context, userID int64, scopeKey string) ([]service.OpenAIUserResidentSlot, error) {
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, user_id, scope_key, slot_index, account_id, generation, status,
		       admitted_at, last_success_at, expires_at, usage_score, score_updated_at,
		       replacement_source_slot_id, provisional_token, config_version
		FROM openai_user_resident_slots s
		WHERE s.user_id = $1 AND s.scope_key = $2
		  AND s.status IN ('provisional', 'active', 'replacement_pending', 'draining')
		ORDER BY s.usage_score DESC, s.last_success_at DESC NULLS LAST, s.admitted_at, s.account_id`,
		userID, normalizeOpenAIUserAffinityScopeKey(scopeKey))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	slots := make([]service.OpenAIUserResidentSlot, 0)
	for rows.Next() {
		var slot service.OpenAIUserResidentSlot
		var lastSuccess sql.NullTime
		var replacementSource sql.NullInt64
		var provisionalToken sql.NullString
		if err := rows.Scan(
			&slot.ID, &slot.UserID, &slot.ScopeKey, &slot.SlotIndex, &slot.AccountID,
			&slot.Generation, &slot.Status, &slot.AdmittedAt, &lastSuccess, &slot.ExpiresAt,
			&slot.UsageScore, &slot.ScoreUpdatedAt, &replacementSource, &provisionalToken,
			&slot.ConfigVersion,
		); err != nil {
			return nil, err
		}
		if lastSuccess.Valid {
			value := lastSuccess.Time.UTC()
			slot.LastSuccessAt = &value
		}
		if replacementSource.Valid {
			value := replacementSource.Int64
			slot.ReplacementSourceSlotID = &value
		}
		if provisionalToken.Valid {
			slot.ProvisionalToken = provisionalToken.String
		}
		slots = append(slots, slot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	accountIDs := make([]int64, 0, len(slots))
	for _, slot := range slots {
		accountIDs = append(accountIDs, slot.AccountID)
	}
	occupancies, err := r.ListOpenAIAccountSoftOccupancies(ctx, accountIDs)
	if err != nil {
		return nil, err
	}
	for i := range slots {
		occupancy := occupancies[slots[i].AccountID]
		slots[i].ActiveRouteUserCount = occupancy.ActiveUserCount
		slots[i].SoftOwnerUserID = occupancy.OwnerUserID
	}
	return slots, nil
}

// GetOpenAIUserConversationBinding 按作用域化会话哈希读取长期绑定。
func (r *accountRepository) GetOpenAIUserConversationBinding(ctx context.Context, userID, apiKeyID int64, scopeKey, conversationHash string) (*service.OpenAIUserConversationBinding, error) {
	return r.getOpenAIUserConversationBinding(ctx, `
		SELECT id, user_id, api_key_id, scope_key, conversation_hash, resident_slot_id,
		       account_id, slot_generation, status, context_rebuildable,
		       first_output_committed, active_until, expires_at, last_success_at, provisional_token,
		       account_persona_id, persona_session_epoch, credential_chain_id,
		       root_client_session_hash, user_group_client_session_lease_id, profile_id, profile_version
		FROM openai_user_conversation_bindings
		WHERE user_id = $1 AND api_key_id = $2 AND scope_key = $3 AND conversation_hash = $4
		  AND status IN ('provisional', 'active', 'draining') AND expires_at > NOW()`,
		userID, apiKeyID, normalizeOpenAIUserAffinityScopeKey(scopeKey), strings.ToLower(strings.TrimSpace(conversationHash)))
}

// GetOpenAIUserConversationBindingByAlias 通过 response/session 等别名回源长期绑定。
func (r *accountRepository) GetOpenAIUserConversationBindingByAlias(ctx context.Context, userID, apiKeyID int64, scopeKey, aliasType, aliasHash string) (*service.OpenAIUserConversationBinding, error) {
	return r.getOpenAIUserConversationBinding(ctx, `
		SELECT b.id, b.user_id, b.api_key_id, b.scope_key, b.conversation_hash, b.resident_slot_id,
		       b.account_id, b.slot_generation, b.status, b.context_rebuildable,
		       b.first_output_committed, b.active_until, b.expires_at, b.last_success_at, b.provisional_token,
		       b.account_persona_id, b.persona_session_epoch, b.credential_chain_id,
		       b.root_client_session_hash, b.user_group_client_session_lease_id, b.profile_id, b.profile_version
		FROM openai_user_conversation_aliases a
		JOIN openai_user_conversation_bindings b ON b.id = a.binding_id
		WHERE a.user_id = $1 AND a.api_key_id = $2 AND a.scope_key = $3
		  AND a.alias_type = $4 AND a.alias_hash = $5 AND a.expires_at > NOW()
		  AND b.status IN ('provisional', 'active', 'draining') AND b.expires_at > NOW()`,
		userID, apiKeyID, normalizeOpenAIUserAffinityScopeKey(scopeKey),
		strings.ToLower(strings.TrimSpace(aliasType)), strings.ToLower(strings.TrimSpace(aliasHash)))
}

// ValidateOpenAIUserConversationBinding 在每次复用前复核 binding 与 resident slot 的版本和生命周期。
func (r *accountRepository) ValidateOpenAIUserConversationBinding(ctx context.Context, binding service.OpenAIUserConversationBinding) (bool, error) {
	if r == nil || r.sql == nil || binding.ID <= 0 || binding.UserID <= 0 || binding.APIKeyID <= 0 ||
		binding.ResidentSlotID <= 0 || binding.AccountID <= 0 || binding.SlotGeneration <= 0 {
		return false, nil
	}
	return validateOpenAIUserConversationBinding(ctx, r.sql, binding)
}

func validateOpenAIUserConversationBinding(
	ctx context.Context,
	exec sqlQueryExecutor,
	binding service.OpenAIUserConversationBinding,
) (bool, error) {
	var valid bool
	err := scanSingleRow(ctx, exec, `
		SELECT EXISTS (
			SELECT 1
			FROM openai_user_conversation_bindings b
			JOIN openai_user_resident_slots s ON s.id = b.resident_slot_id
			WHERE b.id = $1 AND b.user_id = $2 AND b.api_key_id = $3 AND b.scope_key = $4
			  AND b.conversation_hash = $5::char(64) AND b.account_id = $6 AND b.slot_generation = $7
			  AND b.status IN ('provisional', 'active', 'draining') AND b.expires_at > NOW()
			  AND s.user_id = b.user_id AND s.scope_key = b.scope_key
			  AND s.account_id = b.account_id AND s.generation = b.slot_generation
			  AND s.status IN ('provisional', 'active', 'replacement_pending', 'draining')
			  AND s.expires_at > NOW()
		)`, []any{binding.ID, binding.UserID, binding.APIKeyID,
		normalizeOpenAIUserAffinityScopeKey(binding.ScopeKey), strings.ToLower(strings.TrimSpace(binding.ConversationHash)),
		binding.AccountID, binding.SlotGeneration}, &valid)
	return valid, err
}

func (r *accountRepository) getOpenAIUserConversationBinding(ctx context.Context, query string, args ...any) (*service.OpenAIUserConversationBinding, error) {
	if r == nil || r.sql == nil {
		return nil, errors.New("openai user affinity storage unavailable")
	}
	return scanOpenAIUserConversationBindingRow(ctx, r.sql, query, args)
}

// BindOpenAIUserConversationExecutionTarget 在 Persona 席位预留后把完整身份固化到 provisional binding。
func (r *accountRepository) BindOpenAIUserConversationExecutionTarget(
	ctx context.Context,
	transition service.OpenAIUserConversationTransition,
	target service.OpenAIExecutionTarget,
) error {
	if r == nil || r.sql == nil || transition.BindingID <= 0 || transition.AccountID <= 0 ||
		strings.TrimSpace(transition.ProvisionalToken) == "" || !target.Valid() ||
		target.AccountID != transition.AccountID || len(strings.TrimSpace(transition.RootClientSessionHash)) != 64 ||
		target.UserGroupLeaseID <= 0 {
		return errors.New("invalid openai conversation execution target binding")
	}
	result, err := r.sql.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			account_persona_id = $1, persona_session_epoch = $2, credential_chain_id = $3,
			root_client_session_hash = $4::char(64), user_group_client_session_lease_id = $5,
			profile_id = $6, profile_version = $7, updated_at = $8
		WHERE id = $9 AND account_id = $10 AND status = 'provisional'
		  AND first_output_committed = FALSE AND provisional_token = $11
		  AND (account_persona_id IS NULL OR (
			account_persona_id = $1 AND persona_session_epoch = $2 AND credential_chain_id = $3 AND
			root_client_session_hash = $4::char(64) AND user_group_client_session_lease_id = $5 AND
			profile_id = $6 AND profile_version = $7))`,
		target.AccountPersonaID, target.SessionEpoch, target.CredentialChainID,
		strings.ToLower(strings.TrimSpace(transition.RootClientSessionHash)), target.UserGroupLeaseID,
		string(target.ProfileID), target.ProfileVersion, time.Now().UTC(), transition.BindingID,
		transition.AccountID, transition.ProvisionalToken)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return service.ErrOpenAIUserAffinityReservationConflict
	}
	return nil
}

func scanOpenAIUserConversationBindingRow(ctx context.Context, exec sqlQueryer, query string, args []any) (*service.OpenAIUserConversationBinding, error) {
	var binding service.OpenAIUserConversationBinding
	var activeUntil, lastSuccess sql.NullTime
	var provisionalToken, credentialChainID, rootClientSessionHash, profileID, profileVersion sql.NullString
	var accountPersonaID, personaSessionEpoch, userGroupLeaseID sql.NullInt64
	err := scanSingleRow(ctx, exec, query, args,
		&binding.ID, &binding.UserID, &binding.APIKeyID, &binding.ScopeKey,
		&binding.ConversationHash, &binding.ResidentSlotID, &binding.AccountID,
		&binding.SlotGeneration, &binding.Status, &binding.ContextRebuildable,
		&binding.FirstOutputCommitted, &activeUntil, &binding.ExpiresAt, &lastSuccess,
		&provisionalToken, &accountPersonaID, &personaSessionEpoch, &credentialChainID,
		&rootClientSessionHash, &userGroupLeaseID, &profileID, &profileVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if activeUntil.Valid {
		value := activeUntil.Time.UTC()
		binding.ActiveUntil = &value
	}
	if lastSuccess.Valid {
		value := lastSuccess.Time.UTC()
		binding.LastSuccessAt = &value
	}
	if provisionalToken.Valid {
		binding.ProvisionalToken = provisionalToken.String
	}
	if accountPersonaID.Valid {
		binding.AccountPersonaID = accountPersonaID.Int64
	}
	if personaSessionEpoch.Valid {
		binding.PersonaSessionEpoch = personaSessionEpoch.Int64
	}
	if credentialChainID.Valid {
		binding.CredentialChainID = credentialChainID.String
	}
	if rootClientSessionHash.Valid {
		binding.RootClientSessionHash = rootClientSessionHash.String
	}
	if userGroupLeaseID.Valid {
		binding.UserGroupLeaseID = userGroupLeaseID.Int64
	}
	if profileID.Valid {
		binding.ProfileID = service.SessionPersonaID(profileID.String)
	}
	if profileVersion.Valid {
		binding.ProfileVersion = profileVersion.String
	}
	return &binding, nil
}

func normalizeOpenAIUserConversationReservationAliases(reservation service.OpenAIUserConversationReservation) ([]service.OpenAIUserConversationAlias, error) {
	aliases := append([]service.OpenAIUserConversationAlias(nil), reservation.Aliases...)
	if strings.TrimSpace(reservation.AliasType) != "" || strings.TrimSpace(reservation.AliasHash) != "" {
		aliases = append(aliases, service.OpenAIUserConversationAlias{
			ScopeKey: reservation.ScopeKey, Type: reservation.AliasType, Hash: reservation.AliasHash,
		})
	}
	allowedTypes := map[string]struct{}{
		"previous_response_id": {}, "response_id": {}, "session_id": {},
		"prompt_cache_key": {}, "websocket": {}, "codex_thread": {},
	}
	normalized := make([]service.OpenAIUserConversationAlias, 0, len(aliases))
	seen := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		alias.ScopeKey = normalizeOpenAIUserAffinityScopeKey(alias.ScopeKey)
		alias.Type = strings.ToLower(strings.TrimSpace(alias.Type))
		alias.Hash = strings.ToLower(strings.TrimSpace(alias.Hash))
		decodedHash, decodeErr := hex.DecodeString(alias.Hash)
		if _, allowed := allowedTypes[alias.Type]; !allowed || decodeErr != nil || len(decodedHash) != 32 {
			return nil, errors.New("invalid openai conversation alias")
		}
		key := alias.ScopeKey + "\x00" + alias.Type + "\x00" + alias.Hash
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, alias)
	}
	sort.Slice(normalized, func(i, j int) bool {
		left := normalized[i].ScopeKey + "\x00" + normalized[i].Type + "\x00" + normalized[i].Hash
		right := normalized[j].ScopeKey + "\x00" + normalized[j].Type + "\x00" + normalized[j].Hash
		return left < right
	})
	return normalized, nil
}

func lockOpenAIUserConversationAliases(ctx context.Context, exec sqlQueryExecutor, userID, apiKeyID int64, aliases []service.OpenAIUserConversationAlias) error {
	for _, alias := range aliases {
		lockKey := fmt.Sprintf("%d:%d:%s:%s:%s", userID, apiKeyID, alias.ScopeKey, alias.Type, alias.Hash)
		if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
			return err
		}
	}
	return nil
}

func validateOpenAIUserConversationAliasOwnership(
	ctx context.Context,
	exec sqlQueryExecutor,
	userID, apiKeyID, bindingID int64,
	aliases []service.OpenAIUserConversationAlias,
) (bool, error) {
	for _, alias := range aliases {
		var currentBindingID int64
		err := scanSingleRow(ctx, exec, `
			SELECT binding_id FROM openai_user_conversation_aliases
			WHERE user_id = $1 AND api_key_id = $2 AND scope_key = $3
			  AND alias_type = $4 AND alias_hash = $5 AND expires_at > NOW()
			FOR UPDATE`, []any{userID, apiKeyID, alias.ScopeKey, alias.Type, alias.Hash}, &currentBindingID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if currentBindingID != bindingID {
			return false, nil
		}
	}
	return true, nil
}

func getOpenAIUserConversationBindingByAliasesForUpdate(
	ctx context.Context,
	exec sqlQueryExecutor,
	userID, apiKeyID int64,
	bindingScopeKey string,
	aliases []service.OpenAIUserConversationAlias,
) (*service.OpenAIUserConversationBinding, error) {
	var winner *service.OpenAIUserConversationBinding
	for _, alias := range aliases {
		candidate, err := scanOpenAIUserConversationBindingRow(ctx, exec, `
			SELECT b.id, b.user_id, b.api_key_id, b.scope_key, b.conversation_hash, b.resident_slot_id,
			       b.account_id, b.slot_generation, b.status, b.context_rebuildable,
			       b.first_output_committed, b.active_until, b.expires_at, b.last_success_at, b.provisional_token,
			       b.account_persona_id, b.persona_session_epoch, b.credential_chain_id,
			       b.root_client_session_hash, b.user_group_client_session_lease_id, b.profile_id, b.profile_version
			FROM openai_user_conversation_aliases a
			JOIN openai_user_conversation_bindings b ON b.id = a.binding_id
			WHERE a.user_id = $1 AND a.api_key_id = $2 AND a.scope_key = $3
			  AND a.alias_type = $4 AND a.alias_hash = $5 AND a.expires_at > NOW()
			  AND b.status IN ('provisional', 'active', 'draining') AND b.expires_at > NOW()
			FOR UPDATE OF a, b`, []any{userID, apiKeyID, alias.ScopeKey, alias.Type, alias.Hash})
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			continue
		}
		if normalizeOpenAIUserAffinityScopeKey(candidate.ScopeKey) != bindingScopeKey {
			continue
		}
		if winner != nil && winner.ID != candidate.ID {
			return nil, errors.New("openai conversation aliases belong to different bindings")
		}
		winner = candidate
	}
	return winner, nil
}

func upsertOpenAIUserConversationAliases(
	ctx context.Context,
	exec sqlQueryExecutor,
	binding *service.OpenAIUserConversationBinding,
	userID, apiKeyID int64,
	bindingScopeKey string,
	aliases []service.OpenAIUserConversationAlias,
	expiresAt time.Time,
) error {
	if binding == nil || binding.UserID != userID || binding.APIKeyID != apiKeyID ||
		normalizeOpenAIUserAffinityScopeKey(binding.ScopeKey) != normalizeOpenAIUserAffinityScopeKey(bindingScopeKey) {
		return errors.New("openai conversation alias requires binding")
	}
	for _, alias := range aliases {
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO openai_user_conversation_aliases
				(binding_id, user_id, api_key_id, scope_key, alias_type, alias_hash, account_id, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (user_id, api_key_id, scope_key, alias_type, alias_hash) DO UPDATE SET
				binding_id = EXCLUDED.binding_id, account_id = EXCLUDED.account_id,
				expires_at = EXCLUDED.expires_at`, binding.ID, binding.UserID, binding.APIKeyID,
			alias.ScopeKey, alias.Type, alias.Hash, binding.AccountID, expiresAt); err != nil {
			return err
		}
	}
	return nil
}

// ReserveOpenAIUserConversationBinding 在上游首输出前原子预留会话和槽位占用。
func (r *accountRepository) ReserveOpenAIUserConversationBinding(ctx context.Context, reservation service.OpenAIUserConversationReservation) (*service.OpenAIUserConversationBinding, bool, error) {
	if r == nil || r.client == nil || r.sql == nil {
		return nil, false, errors.New("openai user affinity storage unavailable")
	}
	if reservation.UserID <= 0 || reservation.APIKeyID <= 0 || reservation.AccountID <= 0 ||
		reservation.PlacementGeneration <= 0 || strings.TrimSpace(reservation.ProvisionalToken) == "" ||
		len(strings.TrimSpace(reservation.ConversationHash)) != 64 {
		return nil, false, errors.New("invalid openai conversation reservation")
	}
	reservation.ScopeKey = normalizeOpenAIUserAffinityScopeKey(reservation.ScopeKey)
	reservation.ConversationHash = strings.ToLower(strings.TrimSpace(reservation.ConversationHash))
	aliases, err := normalizeOpenAIUserConversationReservationAliases(reservation)
	if err != nil {
		return nil, false, err
	}

	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return nil, false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	lockKey := fmt.Sprintf("%d:%s", reservation.UserID, reservation.ScopeKey)
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return nil, false, err
	}
	if err := lockOpenAIUserConversationAliases(ctx, exec, reservation.UserID, reservation.APIKeyID, aliases); err != nil {
		return nil, false, err
	}
	aliasBinding, err := getOpenAIUserConversationBindingByAliasesForUpdate(
		ctx, exec, reservation.UserID, reservation.APIKeyID, reservation.ScopeKey, aliases,
	)
	if err != nil {
		return nil, false, err
	}
	var maxResidentUsers, cooldownSeconds sql.NullInt64
	var cooldownUntil sql.NullTime
	var accountStatus, accountPlatform string
	var accountSchedulable bool
	if err := scanSingleRow(ctx, exec, `
		SELECT max_resident_users, new_resident_cooldown_seconds, new_resident_cooldown_until,
		       status, schedulable, platform
		FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`,
		[]any{reservation.AccountID}, &maxResidentUsers, &cooldownSeconds,
		&cooldownUntil, &accountStatus, &accountSchedulable, &accountPlatform); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, service.ErrOpenAIUserAffinityAccountUnavailable
		}
		return nil, false, err
	}
	if accountStatus != service.StatusActive || !accountSchedulable || accountPlatform != service.PlatformOpenAI {
		return nil, false, service.ErrOpenAIUserAffinityAccountUnavailable
	}

	existing := aliasBinding
	if existing == nil {
		existing, err = scanOpenAIUserConversationBindingRow(ctx, exec, `
			SELECT id, user_id, api_key_id, scope_key, conversation_hash, resident_slot_id,
			       account_id, slot_generation, status, context_rebuildable,
			       first_output_committed, active_until, expires_at, last_success_at, provisional_token,
			       account_persona_id, persona_session_epoch, credential_chain_id,
			       root_client_session_hash, user_group_client_session_lease_id, profile_id, profile_version
			FROM openai_user_conversation_bindings
			WHERE user_id = $1 AND api_key_id = $2 AND scope_key = $3 AND conversation_hash = $4
			FOR UPDATE`, []any{reservation.UserID, reservation.APIKeyID, reservation.ScopeKey, reservation.ConversationHash})
		if err != nil {
			return nil, false, err
		}
	}
	if existing != nil && existing.ExpiresAt.After(now) &&
		(existing.Status == "provisional" || existing.Status == "active" || existing.Status == "draining") {
		existingValid, validateErr := validateOpenAIUserConversationBinding(ctx, exec, *existing)
		if validateErr != nil {
			return nil, false, validateErr
		}
		if existingValid {
			if err := upsertOpenAIUserConversationAliases(ctx, exec, existing, reservation.UserID,
				reservation.APIKeyID, reservation.ScopeKey, aliases, existing.ExpiresAt); err != nil {
				return nil, false, err
			}
			if tx != nil {
				if err := tx.Commit(); err != nil {
					return nil, false, err
				}
			}
			return existing, false, nil
		}
	}

	if existing != nil && (existing.UserID != reservation.UserID || existing.APIKeyID != reservation.APIKeyID ||
		normalizeOpenAIUserAffinityScopeKey(existing.ScopeKey) != reservation.ScopeKey ||
		existing.ConversationHash != reservation.ConversationHash) {
		return nil, false, service.ErrOpenAIUserAffinityReservationConflict
	}

	effectiveMax := reservation.Config.DefaultMaxResidentUsers
	if maxResidentUsers.Valid && maxResidentUsers.Int64 > 0 {
		effectiveMax = int(maxResidentUsers.Int64)
	}
	residentUsers, userAlreadyResident, err := getOpenAIAccountResidentCapacity(
		ctx, exec, reservation.AccountID, reservation.UserID, now,
	)
	if err != nil {
		return nil, false, err
	}
	if residentUsers >= effectiveMax && !userAlreadyResident {
		return nil, false, service.ErrOpenAIUserAffinityAccountUnavailable
	}
	if cooldownUntil.Valid && now.Before(cooldownUntil.Time) && !userAlreadyResident {
		return nil, false, service.ErrOpenAIUserAffinityAccountUnavailable
	}
	reservationKind := "new_resident"
	if userAlreadyResident {
		reservationKind = "resident_scope"
	}
	reservationUntil := now.Add(openAIUserAffinityProvisionalTTL(reservation.Config))
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO account_user_contacts
			(account_id, user_id, reservation_kind, reservation_token, reservation_until, reservation_generation,
			 reentry_config_version, follower_jitter_min_ms, follower_jitter_max_ms, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (account_id, user_id) DO UPDATE SET
			reservation_kind = EXCLUDED.reservation_kind,
			reservation_token = EXCLUDED.reservation_token,
			reservation_until = EXCLUDED.reservation_until,
			reservation_generation = EXCLUDED.reservation_generation,
			reentry_config_version = EXCLUDED.reentry_config_version,
			follower_jitter_min_ms = EXCLUDED.follower_jitter_min_ms,
			follower_jitter_max_ms = EXCLUDED.follower_jitter_max_ms,
			updated_at = EXCLUDED.updated_at`, reservation.AccountID, reservation.UserID,
		reservationKind, reservation.ProvisionalToken, reservationUntil, reservation.PlacementGeneration,
		reservation.Config.ConfigVersion, reservation.Config.FollowerJitterMinMS,
		reservation.Config.FollowerJitterMaxMS, now); err != nil {
		return nil, false, err
	}
	if !userAlreadyResident {
		effectiveCooldown := reservation.Config.DefaultNewResidentCooldownSeconds
		if cooldownSeconds.Valid && cooldownSeconds.Int64 > 0 {
			effectiveCooldown = int(cooldownSeconds.Int64)
		}
		if _, err := exec.ExecContext(ctx, `
			UPDATE accounts SET new_resident_cooldown_until = $2,
				affinity_config_version = $3, updated_at = $1 WHERE id = $4`,
			now, now.Add(time.Duration(effectiveCooldown)*time.Second),
			reservation.Config.ConfigVersion, reservation.AccountID); err != nil {
			return nil, false, err
		}
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_resident_slots SET status = 'expired', provisional_token = NULL, updated_at = $3
		WHERE user_id = $1 AND scope_key = $2
		  AND status IN ('provisional', 'active', 'replacement_pending') AND expires_at <= $3`,
		reservation.UserID, reservation.ScopeKey, now); err != nil {
		return nil, false, err
	}

	maxSlots := reservation.MaxResidentSlots
	if maxSlots < 1 {
		maxSlots = 1
	}
	if maxSlots > 5 {
		maxSlots = 5
	}
	rows, err := exec.QueryContext(ctx, `
		SELECT id, slot_index, account_id, generation, status
		FROM openai_user_resident_slots
		WHERE user_id = $1 AND scope_key = $2
		  AND status IN ('provisional', 'active', 'replacement_pending', 'draining')
		ORDER BY account_id, generation FOR UPDATE`, reservation.UserID, reservation.ScopeKey)
	if err != nil {
		return nil, false, err
	}
	var slotID, slotGeneration, maxGeneration int64
	preferredSlotRequested := reservation.PreferredResidentSlotID > 0 && reservation.PreferredSlotGeneration > 0
	preferredSlotFound := false
	drainingAccountConflict := false
	usedIndexes := make(map[int]struct{}, maxSlots)
	activeSlotCount := 0
	for rows.Next() {
		var id, accountID, generation int64
		var slotIndex int
		var status string
		if err := rows.Scan(&id, &slotIndex, &accountID, &generation, &status); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		if generation > maxGeneration {
			maxGeneration = generation
		}
		if status != service.OpenAIUserResidentSlotStatusDraining {
			activeSlotCount++
			usedIndexes[slotIndex] = struct{}{}
		}
		if accountID == reservation.AccountID && status == service.OpenAIUserResidentSlotStatusDraining {
			drainingAccountConflict = true
		}
		if preferredSlotRequested && id == reservation.PreferredResidentSlotID &&
			accountID == reservation.AccountID && generation == reservation.PreferredSlotGeneration &&
			status != service.OpenAIUserResidentSlotStatusDraining {
			slotID = id
			slotGeneration = generation
			preferredSlotFound = true
		} else if !preferredSlotRequested && accountID == reservation.AccountID && status != service.OpenAIUserResidentSlotStatusDraining {
			slotID = id
			slotGeneration = generation
		}
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	var historicalMaxGeneration int64
	if err := scanSingleRow(ctx, exec, `
		SELECT COALESCE(MAX(generation), 0) FROM openai_user_resident_slots
		WHERE user_id = $1 AND scope_key = $2`,
		[]any{reservation.UserID, reservation.ScopeKey}, &historicalMaxGeneration); err != nil {
		return nil, false, err
	}
	if historicalMaxGeneration > maxGeneration {
		maxGeneration = historicalMaxGeneration
	}
	if preferredSlotRequested && !preferredSlotFound {
		if drainingAccountConflict {
			return nil, false, service.ErrOpenAIUserAffinityDrainingSlotConflict
		}
		return nil, false, service.ErrOpenAIUserAffinityReservationConflict
	}
	if slotID == 0 {
		if drainingAccountConflict {
			return nil, false, service.ErrOpenAIUserAffinityDrainingSlotConflict
		}
		if activeSlotCount >= maxSlots {
			return nil, false, service.ErrOpenAIUserAffinityResidentSlotsFull
		}
		slotIndex := 1
		for ; slotIndex <= maxSlots; slotIndex++ {
			if _, used := usedIndexes[slotIndex]; !used {
				break
			}
		}
		slotGeneration = maxGeneration + 1
		if reservation.PlacementGeneration > slotGeneration {
			slotGeneration = reservation.PlacementGeneration
		}
		provisionalExpiresAt := now.Add(openAIUserAffinityProvisionalTTL(reservation.Config))
		slotErr := scanSingleRow(ctx, exec, `
			INSERT INTO openai_user_resident_slots
				(user_id, scope_key, slot_index, account_id, generation, status, admitted_at,
				 expires_at, score_updated_at, provisional_token, config_version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, 'provisional', $6, $7, $6, $8, $9, $6, $6)
			ON CONFLICT DO NOTHING RETURNING id`,
			[]any{reservation.UserID, reservation.ScopeKey, slotIndex, reservation.AccountID,
				slotGeneration, now, provisionalExpiresAt, reservation.ProvisionalToken, reservation.Config.ConfigVersion}, &slotID)
		if errors.Is(slotErr, sql.ErrNoRows) {
			var conflictingStatus string
			slotErr = scanSingleRow(ctx, exec, `
				SELECT id, generation, status FROM openai_user_resident_slots
				WHERE user_id = $1 AND scope_key = $2 AND account_id = $3
				  AND status IN ('provisional', 'active', 'replacement_pending', 'draining')
				ORDER BY generation DESC LIMIT 1 FOR UPDATE`,
				[]any{reservation.UserID, reservation.ScopeKey, reservation.AccountID}, &slotID, &slotGeneration, &conflictingStatus)
			if slotErr == nil && conflictingStatus == service.OpenAIUserResidentSlotStatusDraining {
				return nil, false, service.ErrOpenAIUserAffinityDrainingSlotConflict
			}
			if errors.Is(slotErr, sql.ErrNoRows) {
				return nil, false, service.ErrOpenAIUserAffinityReservationConflict
			}
		}
		if slotErr != nil {
			return nil, false, slotErr
		}
	}
	activeRouteAccepted, activeRoutePending, err := reserveOpenAIUserActiveRoute(
		ctx, exec, reservation, slotID, slotGeneration, now,
	)
	if err != nil {
		return nil, false, err
	}
	if !activeRouteAccepted {
		return nil, false, service.ErrOpenAIUserAffinityReservationConflict
	}

	bindingExpiresAt := now.Add(openAIUserAffinityProvisionalTTL(reservation.Config))
	activeUntil := now.Add(reservation.Config.ConversationActiveTTL())
	var binding *service.OpenAIUserConversationBinding
	if existing != nil {
		binding, err = scanOpenAIUserConversationBindingRow(ctx, exec, `
			UPDATE openai_user_conversation_bindings SET resident_slot_id = $2, account_id = $3,
				slot_generation = $4, status = 'provisional', context_rebuildable = $5,
				first_output_committed = FALSE, active_until = $6, expires_at = $7,
				last_success_at = NULL, provisional_token = $8,
				account_persona_id = NULL, persona_session_epoch = NULL, credential_chain_id = NULL,
				root_client_session_hash = NULL, user_group_client_session_lease_id = NULL,
				profile_id = NULL, profile_version = NULL, updated_at = $9
			WHERE id = $1
			RETURNING id, user_id, api_key_id, scope_key, conversation_hash, resident_slot_id,
				account_id, slot_generation, status, context_rebuildable,
				first_output_committed, active_until, expires_at, last_success_at, provisional_token,
				account_persona_id, persona_session_epoch, credential_chain_id,
				root_client_session_hash, user_group_client_session_lease_id, profile_id, profile_version`,
			[]any{existing.ID, slotID, reservation.AccountID, slotGeneration, reservation.ContextRebuildable,
				activeUntil, bindingExpiresAt, reservation.ProvisionalToken, now})
	} else {
		binding, err = scanOpenAIUserConversationBindingRow(ctx, exec, `
			INSERT INTO openai_user_conversation_bindings
				(user_id, api_key_id, scope_key, conversation_hash, resident_slot_id, account_id,
				 slot_generation, status, context_rebuildable, first_output_committed,
				 active_until, expires_at, provisional_token, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'provisional', $8, FALSE, $9, $10, $11, $12, $12)
			RETURNING id, user_id, api_key_id, scope_key, conversation_hash, resident_slot_id,
				account_id, slot_generation, status, context_rebuildable,
				first_output_committed, active_until, expires_at, last_success_at, provisional_token,
				account_persona_id, persona_session_epoch, credential_chain_id,
				root_client_session_hash, user_group_client_session_lease_id, profile_id, profile_version`,
			[]any{reservation.UserID, reservation.APIKeyID, reservation.ScopeKey, reservation.ConversationHash,
				slotID, reservation.AccountID, slotGeneration, reservation.ContextRebuildable,
				activeUntil, bindingExpiresAt, reservation.ProvisionalToken, now})
	}
	if err != nil {
		return nil, false, err
	}
	if binding == nil {
		return nil, false, service.ErrOpenAIUserAffinityReservationConflict
	}
	if err := upsertOpenAIUserConversationAliases(ctx, exec, binding, reservation.UserID,
		reservation.APIKeyID, reservation.ScopeKey, aliases, bindingExpiresAt); err != nil {
		return nil, false, err
	}
	binding.ManageActiveRoute = reservation.ManageActiveRoute
	binding.ActiveRoutePending = activeRoutePending
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
	}
	return binding, true, nil
}

// ReserveOpenAIUserConversationFailover 只写 pending 目标，首输出前保留原会话绑定。
func (r *accountRepository) ReserveOpenAIUserConversationFailover(ctx context.Context, reservation service.OpenAIUserConversationFailoverReservation) (*service.OpenAIUserConversationTransition, bool, error) {
	if r == nil || r.client == nil || r.sql == nil || reservation.BindingID <= 0 || reservation.UserID <= 0 || reservation.APIKeyID <= 0 ||
		reservation.SourceAccountID <= 0 || reservation.TargetAccountID <= 0 ||
		reservation.SourceResidentSlotID <= 0 || reservation.TargetResidentSlotID <= 0 ||
		reservation.SourceSlotGeneration <= 0 || reservation.TargetSlotGeneration <= 0 ||
		reservation.SourceAccountID == reservation.TargetAccountID || len(strings.TrimSpace(reservation.ConversationHash)) != 64 ||
		strings.TrimSpace(reservation.ProvisionalToken) == "" {
		return nil, false, errors.New("invalid openai conversation failover reservation")
	}
	reservation.ScopeKey = normalizeOpenAIUserAffinityScopeKey(reservation.ScopeKey)
	reservation.ConversationHash = strings.ToLower(strings.TrimSpace(reservation.ConversationHash))
	aliases, err := normalizeOpenAIUserConversationReservationAliases(service.OpenAIUserConversationReservation{
		ScopeKey: reservation.ScopeKey, Aliases: reservation.Aliases,
	})
	if err != nil {
		return nil, false, err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return nil, false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	lockKey := fmt.Sprintf("%d:%s", reservation.UserID, reservation.ScopeKey)
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return nil, false, err
	}
	accountRows, err := exec.QueryContext(ctx, `
		SELECT id, status, schedulable, platform FROM accounts
		WHERE id IN ($1, $2) AND deleted_at IS NULL ORDER BY id FOR UPDATE`,
		reservation.SourceAccountID, reservation.TargetAccountID)
	if err != nil {
		return nil, false, err
	}
	accountCount := 0
	targetAllowed := false
	for accountRows.Next() {
		var accountID int64
		var status, platform string
		var schedulable bool
		if err := accountRows.Scan(&accountID, &status, &schedulable, &platform); err != nil {
			_ = accountRows.Close()
			return nil, false, err
		}
		accountCount++
		if accountID == reservation.TargetAccountID {
			targetAllowed = status == service.StatusActive && schedulable && platform == service.PlatformOpenAI
		}
	}
	if err := accountRows.Close(); err != nil {
		return nil, false, err
	}
	if accountCount != 2 || !targetAllowed {
		return nil, false, nil
	}
	slotRows, err := exec.QueryContext(ctx, `
		SELECT id, account_id, generation, status, expires_at FROM openai_user_resident_slots
		WHERE id IN ($1, $2) ORDER BY account_id, id FOR UPDATE`,
		reservation.SourceResidentSlotID, reservation.TargetResidentSlotID)
	if err != nil {
		return nil, false, err
	}
	validSource, validTarget := false, false
	for slotRows.Next() {
		var slotID, accountID, generation int64
		var status string
		var expiresAt time.Time
		if err := slotRows.Scan(&slotID, &accountID, &generation, &status, &expiresAt); err != nil {
			_ = slotRows.Close()
			return nil, false, err
		}
		if slotID == reservation.SourceResidentSlotID {
			validSource = accountID == reservation.SourceAccountID && generation == reservation.SourceSlotGeneration &&
				(status == service.OpenAIUserResidentSlotStatusActive || status == service.OpenAIUserResidentSlotStatusDraining || status == service.OpenAIUserResidentSlotStatusReplacementPending)
		}
		if slotID == reservation.TargetResidentSlotID {
			validTarget = accountID == reservation.TargetAccountID && generation == reservation.TargetSlotGeneration &&
				status == service.OpenAIUserResidentSlotStatusActive && now.Before(expiresAt)
		}
	}
	if err := slotRows.Close(); err != nil {
		return nil, false, err
	}
	if !validSource || !validTarget {
		return nil, false, nil
	}
	var bindingAccountID, bindingSlotID, bindingGeneration int64
	var bindingStatus string
	var firstOutputCommitted bool
	var bindingExpiresAt time.Time
	var pendingAccountID sql.NullInt64
	var pendingToken sql.NullString
	var pendingExpiresAt sql.NullTime
	if err := scanSingleRow(ctx, exec, `
		SELECT account_id, resident_slot_id, slot_generation, status, first_output_committed, expires_at,
		       pending_account_id, pending_token, pending_expires_at
		FROM openai_user_conversation_bindings
		WHERE id = $1 AND user_id = $2 AND api_key_id = $3 AND scope_key = $4 AND conversation_hash = $5::char(64)
		FOR UPDATE`, []any{reservation.BindingID, reservation.UserID, reservation.APIKeyID, reservation.ScopeKey, reservation.ConversationHash},
		&bindingAccountID, &bindingSlotID, &bindingGeneration, &bindingStatus, &firstOutputCommitted,
		&bindingExpiresAt, &pendingAccountID, &pendingToken, &pendingExpiresAt); err != nil {
		return nil, false, err
	}
	if bindingAccountID != reservation.SourceAccountID || bindingSlotID != reservation.SourceResidentSlotID ||
		bindingGeneration != reservation.SourceSlotGeneration || !firstOutputCommitted || !now.Before(bindingExpiresAt) ||
		(bindingStatus != "active" && bindingStatus != "draining") {
		return nil, false, nil
	}
	if pendingToken.Valid && pendingExpiresAt.Valid && now.Before(pendingExpiresAt.Time) {
		return nil, false, nil
	}
	pendingUntil := now.Add(openAIUserAffinityProvisionalTTL(reservation.Config))
	result, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			pending_resident_slot_id = $2, pending_account_id = $3, pending_slot_generation = $4,
			pending_token = $5, pending_expires_at = $6, updated_at = $7
		WHERE id = $1 AND account_id = $8 AND resident_slot_id = $9 AND slot_generation = $10`,
		reservation.BindingID, reservation.TargetResidentSlotID, reservation.TargetAccountID,
		reservation.TargetSlotGeneration, reservation.ProvisionalToken, pendingUntil, now,
		reservation.SourceAccountID, reservation.SourceResidentSlotID, reservation.SourceSlotGeneration)
	if err != nil {
		return nil, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return nil, false, err
	}
	if reservation.DetachSource {
		if _, err := exec.ExecContext(ctx, `
			UPDATE openai_user_resident_slots SET status = 'draining', provisional_token = NULL, updated_at = $2
			WHERE id = $1 AND account_id = $3 AND generation = $4
			  AND status IN ('provisional', 'active', 'replacement_pending')`,
			reservation.SourceResidentSlotID, now, reservation.SourceAccountID, reservation.SourceSlotGeneration); err != nil {
			return nil, false, err
		}
		if _, err := exec.ExecContext(ctx, `
			UPDATE account_user_contacts SET reservation_kind = NULL, reservation_token = NULL,
				reservation_until = NULL, updated_at = $3
			WHERE account_id = $1 AND user_id = $2`, reservation.SourceAccountID, reservation.UserID, now); err != nil {
			return nil, false, err
		}
		if err := cleanupOpenAIUserActiveRoute(ctx, exec, reservation.UserID, reservation.ScopeKey, now); err != nil {
			return nil, false, err
		}
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO user_account_placement_events
				(user_id, scope_key, placement_generation, source_account_id, event_type,
				 reason, effective_source, resident_slot_id)
			VALUES ($1, $2, $3, $4, 'slot_evicted', 'permanent_source_unavailable', 'global', $5)`,
			reservation.UserID, reservation.ScopeKey, reservation.SourceSlotGeneration,
			reservation.SourceAccountID, reservation.SourceResidentSlotID); err != nil {
			return nil, false, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
	}
	return &service.OpenAIUserConversationTransition{
		BindingID: reservation.BindingID, UserID: reservation.UserID, APIKeyID: reservation.APIKeyID, ScopeKey: reservation.ScopeKey,
		ConversationHash: reservation.ConversationHash, ResidentSlotID: reservation.TargetResidentSlotID,
		AccountID: reservation.TargetAccountID, SlotGeneration: reservation.TargetSlotGeneration,
		ProvisionalToken: reservation.ProvisionalToken, Failover: true,
		SourceAccountID: reservation.SourceAccountID, SourceSlotID: reservation.SourceResidentSlotID,
		SourceGeneration: reservation.SourceSlotGeneration, DetachSource: reservation.DetachSource,
		Aliases: aliases, Config: reservation.Config,
	}, true, nil
}

// ReserveOpenAIUserResidentSlotReplacement 原子冻结 victim，并为当前会话创建 provisional target。
func (r *accountRepository) ReserveOpenAIUserResidentSlotReplacement(ctx context.Context, reservation service.OpenAIUserResidentSlotReplacementReservation) (*service.OpenAIUserConversationTransition, bool, error) {
	if r == nil || r.client == nil || r.sql == nil || reservation.BindingID <= 0 || reservation.UserID <= 0 || reservation.APIKeyID <= 0 ||
		reservation.SourceAccountID <= 0 || reservation.SourceResidentSlotID <= 0 || reservation.SourceSlotGeneration <= 0 ||
		reservation.VictimSlotID <= 0 || reservation.TargetAccountID <= 0 ||
		len(reservation.CheckedSlots) == 0 || len(strings.TrimSpace(reservation.ConversationHash)) != 64 ||
		strings.TrimSpace(reservation.ProvisionalToken) == "" {
		return nil, false, errors.New("invalid openai resident slot replacement reservation")
	}
	reservation.ScopeKey = normalizeOpenAIUserAffinityScopeKey(reservation.ScopeKey)
	reservation.ConversationHash = strings.ToLower(strings.TrimSpace(reservation.ConversationHash))
	aliases, err := normalizeOpenAIUserConversationReservationAliases(service.OpenAIUserConversationReservation{
		ScopeKey: reservation.ScopeKey, Aliases: reservation.Aliases,
	})
	if err != nil {
		return nil, false, err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return nil, false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	lockKey := fmt.Sprintf("%d:%s", reservation.UserID, reservation.ScopeKey)
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return nil, false, err
	}
	var maxResidentUsers, cooldownSeconds sql.NullInt64
	var cooldownUntil sql.NullTime
	var accountStatus, accountPlatform string
	var accountSchedulable bool
	if err := scanSingleRow(ctx, exec, `
		SELECT max_resident_users, new_resident_cooldown_seconds, new_resident_cooldown_until,
		       status, schedulable, platform
		FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`,
		[]any{reservation.TargetAccountID}, &maxResidentUsers, &cooldownSeconds,
		&cooldownUntil, &accountStatus, &accountSchedulable, &accountPlatform); err != nil {
		return nil, false, err
	}
	if accountStatus != service.StatusActive || !accountSchedulable || accountPlatform != service.PlatformOpenAI {
		return nil, false, nil
	}
	checked := make(map[int64]service.OpenAIUserResidentSlotVersion, len(reservation.CheckedSlots))
	for _, slot := range reservation.CheckedSlots {
		checked[slot.ID] = slot
	}
	rows, err := exec.QueryContext(ctx, `
		SELECT id, slot_index, account_id, generation, status, expires_at
		FROM openai_user_resident_slots
		WHERE user_id = $1 AND scope_key = $2
		  AND status IN ('provisional', 'active', 'replacement_pending', 'draining')
		ORDER BY account_id, id FOR UPDATE`, reservation.UserID, reservation.ScopeKey)
	if err != nil {
		return nil, false, err
	}
	var victimSlotIndex int
	var victimAccountID, victimGeneration, maxGeneration int64
	activeCount := 0
	for rows.Next() {
		var slotID, accountID, generation int64
		var slotIndex int
		var status string
		var expiresAt time.Time
		if err := rows.Scan(&slotID, &slotIndex, &accountID, &generation, &status, &expiresAt); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		if generation > maxGeneration {
			maxGeneration = generation
		}
		if status == service.OpenAIUserResidentSlotStatusDraining || !now.Before(expiresAt) {
			continue
		}
		if status != service.OpenAIUserResidentSlotStatusActive {
			_ = rows.Close()
			return nil, false, nil
		}
		activeCount++
		version, ok := checked[slotID]
		if !ok || version.AccountID != accountID || version.Generation != generation {
			_ = rows.Close()
			return nil, false, nil
		}
		if slotID == reservation.VictimSlotID {
			victimSlotIndex, victimAccountID, victimGeneration = slotIndex, accountID, generation
		}
	}
	if err := rows.Close(); err != nil {
		return nil, false, err
	}
	if activeCount != len(checked) || victimAccountID <= 0 {
		return nil, false, nil
	}
	effectiveMax := reservation.Config.DefaultMaxResidentUsers
	if maxResidentUsers.Valid && maxResidentUsers.Int64 > 0 {
		effectiveMax = int(maxResidentUsers.Int64)
	}
	residentUsers, userAlreadyResident, err := getOpenAIAccountResidentCapacity(
		ctx, exec, reservation.TargetAccountID, reservation.UserID, now,
	)
	if err != nil {
		return nil, false, err
	}
	if residentUsers >= effectiveMax && !userAlreadyResident ||
		cooldownUntil.Valid && now.Before(cooldownUntil.Time) && !userAlreadyResident {
		return nil, false, nil
	}
	reservationKind := "new_resident"
	if userAlreadyResident {
		reservationKind = "resident_scope"
	}
	reservationUntil := now.Add(openAIUserAffinityProvisionalTTL(reservation.Config))
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO account_user_contacts
			(account_id, user_id, reservation_kind, reservation_token, reservation_until, reservation_generation,
			 reentry_config_version, follower_jitter_min_ms, follower_jitter_max_ms, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (account_id, user_id) DO UPDATE SET
			reservation_kind = EXCLUDED.reservation_kind, reservation_token = EXCLUDED.reservation_token,
			reservation_until = EXCLUDED.reservation_until, reservation_generation = EXCLUDED.reservation_generation,
			reentry_config_version = EXCLUDED.reentry_config_version,
			follower_jitter_min_ms = EXCLUDED.follower_jitter_min_ms,
			follower_jitter_max_ms = EXCLUDED.follower_jitter_max_ms, updated_at = EXCLUDED.updated_at`,
		reservation.TargetAccountID, reservation.UserID, reservationKind, reservation.ProvisionalToken,
		reservationUntil, maxGeneration+1, reservation.Config.ConfigVersion,
		reservation.Config.FollowerJitterMinMS, reservation.Config.FollowerJitterMaxMS, now); err != nil {
		return nil, false, err
	}
	if !userAlreadyResident {
		effectiveCooldown := reservation.Config.DefaultNewResidentCooldownSeconds
		if cooldownSeconds.Valid && cooldownSeconds.Int64 > 0 {
			effectiveCooldown = int(cooldownSeconds.Int64)
		}
		if _, err := exec.ExecContext(ctx, `
			UPDATE accounts SET new_resident_cooldown_until = $2,
				affinity_config_version = $3, updated_at = $1 WHERE id = $4`,
			now, now.Add(time.Duration(effectiveCooldown)*time.Second),
			reservation.Config.ConfigVersion, reservation.TargetAccountID); err != nil {
			return nil, false, err
		}
	}
	targetGeneration := maxGeneration + 1
	var targetSlotID int64
	if err := scanSingleRow(ctx, exec, `
		INSERT INTO openai_user_resident_slots
			(user_id, scope_key, slot_index, account_id, generation, status, admitted_at,
			 expires_at, score_updated_at, replacement_source_slot_id, provisional_token,
			 config_version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'provisional', $6, $7, $6, $8, $9, $10, $6, $6)
		RETURNING id`, []any{reservation.UserID, reservation.ScopeKey, victimSlotIndex,
		reservation.TargetAccountID, targetGeneration, now, reservationUntil,
		reservation.VictimSlotID, reservation.ProvisionalToken, reservation.Config.ConfigVersion}, &targetSlotID); err != nil {
		return nil, false, err
	}
	var bindingAccountID, bindingSlotID, bindingGeneration int64
	var bindingStatus string
	var firstOutputCommitted bool
	var pendingToken sql.NullString
	if err := scanSingleRow(ctx, exec, `
		SELECT account_id, resident_slot_id, slot_generation, status, first_output_committed, pending_token
		FROM openai_user_conversation_bindings
		WHERE id = $1 AND user_id = $2 AND api_key_id = $3 AND scope_key = $4 AND conversation_hash = $5::char(64)
		FOR UPDATE`, []any{reservation.BindingID, reservation.UserID, reservation.APIKeyID, reservation.ScopeKey, reservation.ConversationHash},
		&bindingAccountID, &bindingSlotID, &bindingGeneration, &bindingStatus, &firstOutputCommitted, &pendingToken); err != nil {
		return nil, false, err
	}
	if bindingAccountID != reservation.SourceAccountID || bindingSlotID != reservation.SourceResidentSlotID ||
		bindingGeneration != reservation.SourceSlotGeneration || !firstOutputCommitted || pendingToken.Valid ||
		(bindingStatus != "active" && bindingStatus != "draining") {
		return nil, false, nil
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_resident_slots SET status = 'replacement_pending', updated_at = $2
		WHERE id = $1 AND account_id = $3 AND generation = $4 AND status = 'active'`,
		reservation.VictimSlotID, now, victimAccountID, victimGeneration); err != nil {
		return nil, false, err
	}
	result, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			pending_resident_slot_id = $2, pending_account_id = $3, pending_slot_generation = $4,
			pending_token = $5, pending_expires_at = $6, updated_at = $7
		WHERE id = $1 AND account_id = $8 AND resident_slot_id = $9 AND slot_generation = $10`,
		reservation.BindingID, targetSlotID, reservation.TargetAccountID, targetGeneration,
		reservation.ProvisionalToken, reservationUntil, now, reservation.SourceAccountID,
		reservation.SourceResidentSlotID, reservation.SourceSlotGeneration)
	if err != nil {
		return nil, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return nil, false, err
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, source_account_id, target_account_id,
			 event_type, reason, config_version, effective_source, resident_slot_id)
		VALUES ($1, $2, $3, $4, $5, 'slot_replacement_started', 'all_slots_unavailable', $6, 'global', $7)`,
		reservation.UserID, reservation.ScopeKey, targetGeneration, victimAccountID,
		reservation.TargetAccountID, reservation.Config.ConfigVersion, targetSlotID); err != nil {
		return nil, false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
	}
	return &service.OpenAIUserConversationTransition{
		BindingID: reservation.BindingID, UserID: reservation.UserID, APIKeyID: reservation.APIKeyID, ScopeKey: reservation.ScopeKey,
		ConversationHash: reservation.ConversationHash, ResidentSlotID: targetSlotID,
		AccountID: reservation.TargetAccountID, SlotGeneration: targetGeneration,
		ProvisionalToken: reservation.ProvisionalToken, Failover: true, Replacement: true,
		SourceAccountID: reservation.SourceAccountID, SourceSlotID: reservation.SourceResidentSlotID,
		SourceGeneration: reservation.SourceSlotGeneration, ReplacementSlotID: reservation.VictimSlotID,
		Aliases: aliases, Config: reservation.Config,
	}, true, nil
}

// CommitOpenAIUserConversationBinding 在首个有效输出后提交绑定；后续成功只刷新双 TTL。
func (r *accountRepository) CommitOpenAIUserConversationBinding(ctx context.Context, transition service.OpenAIUserConversationTransition) (bool, error) {
	if transition.Failover {
		return r.commitOpenAIUserConversationFailover(ctx, transition)
	}
	if r == nil || r.client == nil || r.sql == nil || transition.BindingID <= 0 || transition.UserID <= 0 || transition.APIKeyID <= 0 ||
		transition.AccountID <= 0 || strings.TrimSpace(transition.ScopeKey) == "" {
		return false, errors.New("openai user affinity storage unavailable")
	}
	aliases, err := normalizeOpenAIUserConversationReservationAliases(service.OpenAIUserConversationReservation{
		ScopeKey: transition.ScopeKey, Aliases: transition.Aliases,
	})
	if err != nil {
		return false, err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	lockKey := fmt.Sprintf("%d:%s", transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey))
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return false, err
	}
	if err := lockOpenAIUserConversationAliases(ctx, exec, transition.UserID, transition.APIKeyID, aliases); err != nil {
		return false, err
	}
	aliasesOwned, err := validateOpenAIUserConversationAliasOwnership(
		ctx, exec, transition.UserID, transition.APIKeyID, transition.BindingID, aliases,
	)
	if err != nil || !aliasesOwned {
		return false, err
	}
	var status string
	var committed bool
	var token sql.NullString
	var slotID, accountID int64
	var bindingExpiresAt time.Time
	if err := scanSingleRow(ctx, exec, `
		SELECT status, first_output_committed, provisional_token, resident_slot_id, account_id, expires_at
		FROM openai_user_conversation_bindings
		WHERE id = $1 AND user_id = $2 AND api_key_id = $3 AND scope_key = $4 AND conversation_hash = $5::char(64)
		FOR UPDATE`, []any{transition.BindingID, transition.UserID, transition.APIKeyID,
		normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), strings.ToLower(strings.TrimSpace(transition.ConversationHash))},
		&status, &committed, &token, &slotID, &accountID, &bindingExpiresAt); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if accountID != transition.AccountID || status == "provisional" && (!token.Valid || token.String != transition.ProvisionalToken) ||
		status != "provisional" && status != "active" && status != "draining" || !now.Before(bindingExpiresAt) {
		return false, nil
	}
	var slotStatus string
	var slotGeneration int64
	var slotToken sql.NullString
	var slotExpiresAt time.Time
	if err := scanSingleRow(ctx, exec, `
		SELECT status, generation, provisional_token, expires_at
		FROM openai_user_resident_slots WHERE id = $1 AND account_id = $2 FOR UPDATE`,
		[]any{slotID, transition.AccountID}, &slotStatus, &slotGeneration, &slotToken, &slotExpiresAt); err != nil {
		return false, err
	}
	if slotGeneration != transition.SlotGeneration ||
		(slotStatus != service.OpenAIUserResidentSlotStatusActive && slotStatus != service.OpenAIUserResidentSlotStatusProvisional && slotStatus != service.OpenAIUserResidentSlotStatusDraining && slotStatus != service.OpenAIUserResidentSlotStatusReset) ||
		(slotStatus == service.OpenAIUserResidentSlotStatusProvisional && (!slotToken.Valid || slotToken.String != transition.ProvisionalToken || !now.Before(slotExpiresAt))) {
		return false, nil
	}
	firstCommit := !committed
	result, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			status = CASE WHEN status = 'draining' THEN 'draining' ELSE 'active' END,
			first_output_committed = TRUE,
			active_until = $2, expires_at = $3, last_success_at = $1,
			provisional_token = NULL, updated_at = $1
		WHERE id = $4 AND account_id = $5 AND expires_at > $1
		  AND (status IN ('active', 'draining') OR (status = 'provisional' AND provisional_token = $6))`,
		now, now.Add(transition.Config.ConversationActiveTTL()), now.Add(transition.Config.ResidentTTL()),
		transition.BindingID, transition.AccountID, transition.ProvisionalToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	increment := 0.0
	if firstCommit {
		increment = 1
	}
	residentTTLSeconds := transition.Config.ResidentTTL().Seconds()
	slotResult, err := exec.ExecContext(ctx, `
		UPDATE openai_user_resident_slots SET
			status = CASE WHEN status IN ('draining', 'reset') THEN status ELSE 'active' END,
			last_success_at = $1,
			expires_at = $2,
			usage_score = usage_score * POWER(0.5, GREATEST(EXTRACT(EPOCH FROM ($1 - score_updated_at)), 0) / $3::double precision) + $4,
			score_updated_at = $1, provisional_token = NULL, updated_at = $1
		WHERE id = $5 AND account_id = $6 AND generation = $7
		  AND status IN ('provisional', 'active', 'draining', 'reset')`, now, now.Add(transition.Config.ResidentTTL()),
		residentTTLSeconds, increment, slotID, transition.AccountID, transition.SlotGeneration)
	if err != nil {
		return false, err
	}
	slotAffected, err := slotResult.RowsAffected()
	if err != nil || slotAffected == 0 {
		return false, err
	}
	if responseAliasHash := strings.ToLower(strings.TrimSpace(transition.ResponseAliasHash)); len(responseAliasHash) == 64 {
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO openai_user_conversation_aliases
				(binding_id, user_id, api_key_id, scope_key, alias_type, alias_hash, account_id, expires_at)
			SELECT id, user_id, api_key_id, scope_key, 'response_id', $2, account_id, $3
			FROM openai_user_conversation_bindings WHERE id = $1 AND account_id = $4
			ON CONFLICT (user_id, api_key_id, scope_key, alias_type, alias_hash) DO UPDATE SET
				binding_id = EXCLUDED.binding_id, account_id = EXCLUDED.account_id,
				expires_at = EXCLUDED.expires_at`, transition.BindingID, responseAliasHash,
			now.Add(transition.Config.ResidentTTL()), transition.AccountID); err != nil {
			return false, err
		}
	}
	if err := upsertOpenAIUserConversationAliases(ctx, exec, &service.OpenAIUserConversationBinding{
		ID: transition.BindingID, UserID: transition.UserID, APIKeyID: transition.APIKeyID,
		ScopeKey: normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), AccountID: transition.AccountID,
	}, transition.UserID, transition.APIKeyID, transition.ScopeKey, aliases, now.Add(transition.Config.ResidentTTL())); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_aliases SET expires_at = $2
		WHERE binding_id = $1`, transition.BindingID, now.Add(transition.Config.ResidentTTL())); err != nil {
		return false, err
	}
	if firstCommit {
		if _, err := exec.ExecContext(ctx, `
			UPDATE openai_user_affinity_reset_exclusions SET consumed_at = $3
			WHERE user_id = $1 AND scope_key = $2 AND consumed_at IS NULL`,
			transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), now); err != nil {
			return false, err
		}
	}
	if firstCommit && slotStatus == service.OpenAIUserResidentSlotStatusProvisional {
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO user_account_placement_events
				(user_id, scope_key, placement_generation, target_account_id, event_type,
				 reason, config_version, effective_source, resident_slot_id)
			VALUES ($1, $2, $3, $4, 'slot_admitted', 'new_conversation_first_output', $5, 'global', $6)`,
			transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey),
			transition.SlotGeneration, transition.AccountID, transition.Config.ConfigVersion, slotID); err != nil {
			return false, err
		}
	}
	activeRouteCommitted, err := commitOpenAIUserActiveRoute(ctx, exec, transition, now)
	if err != nil {
		return false, err
	}
	if !activeRouteCommitted {
		return false, nil
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return firstCommit, nil
}

func (r *accountRepository) commitOpenAIUserConversationFailover(ctx context.Context, transition service.OpenAIUserConversationTransition) (bool, error) {
	if r == nil || r.client == nil || r.sql == nil || transition.BindingID <= 0 || transition.UserID <= 0 || transition.APIKeyID <= 0 ||
		transition.AccountID <= 0 || transition.ResidentSlotID <= 0 || transition.SlotGeneration <= 0 ||
		transition.SourceAccountID <= 0 || transition.SourceSlotID <= 0 || transition.SourceGeneration <= 0 ||
		strings.TrimSpace(transition.ProvisionalToken) == "" {
		return false, errors.New("invalid openai conversation failover transition")
	}
	aliases, err := normalizeOpenAIUserConversationReservationAliases(service.OpenAIUserConversationReservation{
		ScopeKey: transition.ScopeKey, Aliases: transition.Aliases,
	})
	if err != nil {
		return false, err
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	lockKey := fmt.Sprintf("%d:%s", transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey))
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return false, err
	}
	if err := lockOpenAIUserConversationAliases(ctx, exec, transition.UserID, transition.APIKeyID, aliases); err != nil {
		return false, err
	}
	aliasesOwned, err := validateOpenAIUserConversationAliasOwnership(
		ctx, exec, transition.UserID, transition.APIKeyID, transition.BindingID, aliases,
	)
	if err != nil || !aliasesOwned {
		return false, err
	}
	slotRows, err := exec.QueryContext(ctx, `
		SELECT id, account_id, generation, status, provisional_token, expires_at FROM openai_user_resident_slots
		WHERE id IN ($1, $2, $3) ORDER BY account_id, id FOR UPDATE`,
		transition.SourceSlotID, transition.ResidentSlotID, transition.ReplacementSlotID)
	if err != nil {
		return false, err
	}
	validSource, validTarget, validVictim := false, false, !transition.Replacement
	for slotRows.Next() {
		var slotID, accountID, generation int64
		var status string
		var provisionalToken sql.NullString
		var expiresAt time.Time
		if err := slotRows.Scan(&slotID, &accountID, &generation, &status, &provisionalToken, &expiresAt); err != nil {
			_ = slotRows.Close()
			return false, err
		}
		if slotID == transition.SourceSlotID {
			validSource = accountID == transition.SourceAccountID && generation == transition.SourceGeneration &&
				now.Before(expiresAt) && (status == service.OpenAIUserResidentSlotStatusActive || status == service.OpenAIUserResidentSlotStatusDraining || status == service.OpenAIUserResidentSlotStatusReplacementPending)
		}
		if slotID == transition.ResidentSlotID {
			validTarget = accountID == transition.AccountID && generation == transition.SlotGeneration &&
				now.Before(expiresAt) && (status == service.OpenAIUserResidentSlotStatusActive || transition.Replacement &&
				status == service.OpenAIUserResidentSlotStatusProvisional && provisionalToken.Valid && provisionalToken.String == transition.ProvisionalToken)
		}
		if transition.Replacement && slotID == transition.ReplacementSlotID {
			validVictim = status == service.OpenAIUserResidentSlotStatusReplacementPending
		}
	}
	if err := slotRows.Close(); err != nil {
		return false, err
	}
	if !validSource || !validTarget || !validVictim {
		return false, nil
	}
	var sourceAccountID, sourceSlotID, sourceGeneration int64
	var pendingAccountID, pendingSlotID, pendingGeneration int64
	var pendingToken string
	var pendingExpiresAt time.Time
	if err := scanSingleRow(ctx, exec, `
		SELECT account_id, resident_slot_id, slot_generation,
		       pending_account_id, pending_resident_slot_id, pending_slot_generation,
		       pending_token, pending_expires_at
		FROM openai_user_conversation_bindings
		WHERE id = $1 AND user_id = $2 AND api_key_id = $3 AND scope_key = $4 AND conversation_hash = $5::char(64)
		FOR UPDATE`, []any{transition.BindingID, transition.UserID, transition.APIKeyID,
		normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), strings.ToLower(strings.TrimSpace(transition.ConversationHash))},
		&sourceAccountID, &sourceSlotID, &sourceGeneration,
		&pendingAccountID, &pendingSlotID, &pendingGeneration, &pendingToken, &pendingExpiresAt); err != nil {
		return false, err
	}
	if sourceAccountID != transition.SourceAccountID || sourceSlotID != transition.SourceSlotID || sourceGeneration != transition.SourceGeneration ||
		pendingAccountID != transition.AccountID || pendingSlotID != transition.ResidentSlotID || pendingGeneration != transition.SlotGeneration ||
		pendingToken != transition.ProvisionalToken || !now.Before(pendingExpiresAt) {
		return false, nil
	}
	result, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			resident_slot_id = $2, account_id = $3, slot_generation = $4, status = 'active',
			active_until = $5, expires_at = $6, last_success_at = $1,
			pending_resident_slot_id = NULL, pending_account_id = NULL, pending_slot_generation = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $1
		WHERE id = $7 AND pending_token = $8`, now, transition.ResidentSlotID, transition.AccountID,
		transition.SlotGeneration, now.Add(transition.Config.ConversationActiveTTL()),
		now.Add(transition.Config.ResidentTTL()), transition.BindingID, transition.ProvisionalToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_resident_slots SET status = 'active', last_success_at = $1, expires_at = $2,
			provisional_token = NULL, updated_at = $1
		WHERE id = $3 AND account_id = $4 AND generation = $5 AND status IN ('provisional', 'active')`,
		now, now.Add(transition.Config.ResidentTTL()), transition.ResidentSlotID,
		transition.AccountID, transition.SlotGeneration); err != nil {
		return false, err
	}
	if transition.Replacement {
		if _, err := exec.ExecContext(ctx, `
			UPDATE openai_user_resident_slots SET status = 'draining', updated_at = $2
			WHERE id = $1 AND status = 'replacement_pending'`, transition.ReplacementSlotID, now); err != nil {
			return false, err
		}
		if _, err := exec.ExecContext(ctx, `
			UPDATE openai_user_conversation_bindings SET status = 'draining', updated_at = $2
			WHERE resident_slot_id = $1 AND status = 'active'`, transition.ReplacementSlotID, now); err != nil {
			return false, err
		}
	}
	if responseAliasHash := strings.ToLower(strings.TrimSpace(transition.ResponseAliasHash)); len(responseAliasHash) == 64 {
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO openai_user_conversation_aliases
				(binding_id, user_id, api_key_id, scope_key, alias_type, alias_hash, account_id, expires_at)
			SELECT id, user_id, api_key_id, scope_key, 'response_id', $2, account_id, $3
			FROM openai_user_conversation_bindings WHERE id = $1 AND account_id = $4
			ON CONFLICT (user_id, api_key_id, scope_key, alias_type, alias_hash) DO UPDATE SET
				binding_id = EXCLUDED.binding_id, account_id = EXCLUDED.account_id,
				expires_at = EXCLUDED.expires_at`, transition.BindingID, responseAliasHash,
			now.Add(transition.Config.ResidentTTL()), transition.AccountID); err != nil {
			return false, err
		}
	}
	if err := upsertOpenAIUserConversationAliases(ctx, exec, &service.OpenAIUserConversationBinding{
		ID: transition.BindingID, UserID: transition.UserID, APIKeyID: transition.APIKeyID,
		ScopeKey: normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), AccountID: transition.AccountID,
	}, transition.UserID, transition.APIKeyID, transition.ScopeKey, aliases, now.Add(transition.Config.ResidentTTL())); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_aliases SET account_id = $2, expires_at = $3
		WHERE binding_id = $1`, transition.BindingID, transition.AccountID, now.Add(transition.Config.ResidentTTL())); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE user_account_capacity_incidents SET status = 'closed', close_reason = 'conversation_failover',
			closed_at = $8, updated_at = $8
		WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3
		  AND placement_generation = $4 AND conversation_hash = $5::char(64)
		  AND resident_slot_id = $6 AND slot_generation = $7 AND closed_at IS NULL`,
		transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), transition.SourceAccountID,
		transition.SourceGeneration, strings.ToLower(strings.TrimSpace(transition.ConversationHash)),
		transition.SourceSlotID, transition.SourceGeneration, now); err != nil {
		return false, err
	}
	failoverReason := "capacity_retry_threshold"
	if transition.DetachSource {
		failoverReason = "permanent_source_unavailable"
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, source_account_id, target_account_id,
			 event_type, reason, config_version, effective_source, resident_slot_id)
		VALUES ($1, $2, $3, $4, $5, 'conversation_migrated', $6, $7, 'global', $8)`,
		transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), transition.SlotGeneration,
		transition.SourceAccountID, transition.AccountID, failoverReason, transition.Config.ConfigVersion, transition.ResidentSlotID); err != nil {
		return false, err
	}
	if transition.Replacement {
		if _, err := exec.ExecContext(ctx, `
			INSERT INTO user_account_placement_events
				(user_id, scope_key, placement_generation, source_account_id, target_account_id,
				 event_type, reason, config_version, effective_source, resident_slot_id)
			SELECT $1, $2, $3, account_id, $4, 'slot_replaced', 'all_slots_unavailable', $5, 'global', $6
			FROM openai_user_resident_slots WHERE id = $7`, transition.UserID,
			normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), transition.SlotGeneration,
			transition.AccountID, transition.Config.ConfigVersion, transition.ResidentSlotID,
			transition.ReplacementSlotID); err != nil {
			return false, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}

// RollbackOpenAIUserConversationBinding 仅删除 token 匹配且尚未提交首输出的 provisional 绑定。
func (r *accountRepository) RollbackOpenAIUserConversationBinding(ctx context.Context, transition service.OpenAIUserConversationTransition) (bool, error) {
	if transition.Replacement {
		return r.rollbackOpenAIUserResidentSlotReplacement(ctx, transition)
	}
	if transition.Failover {
		return r.rollbackOpenAIUserConversationFailover(ctx, transition)
	}
	if r == nil || r.client == nil || r.sql == nil || transition.BindingID <= 0 || transition.UserID <= 0 ||
		transition.APIKeyID <= 0 || transition.ResidentSlotID <= 0 || transition.AccountID <= 0 || transition.SlotGeneration <= 0 ||
		strings.TrimSpace(transition.ScopeKey) == "" || len(strings.TrimSpace(transition.ConversationHash)) != 64 ||
		strings.TrimSpace(transition.ProvisionalToken) == "" {
		return false, nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	lockKey := fmt.Sprintf("%d:%s", transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey))
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return false, err
	}
	result, err := exec.ExecContext(ctx, `
		DELETE FROM openai_user_conversation_bindings
		WHERE id = $1 AND user_id = $2 AND api_key_id = $3 AND scope_key = $4
		  AND conversation_hash = $5::char(64) AND resident_slot_id = $6
		  AND account_id = $7 AND slot_generation = $8 AND status = 'provisional'
		  AND first_output_committed = FALSE AND provisional_token = $9`,
		transition.BindingID, transition.UserID, transition.APIKeyID,
		normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), strings.ToLower(strings.TrimSpace(transition.ConversationHash)),
		transition.ResidentSlotID, transition.AccountID, transition.SlotGeneration, transition.ProvisionalToken)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	now := time.Now().UTC()
	if err := rollbackOpenAIUserActiveRoute(ctx, exec, transition, now); err != nil {
		return false, err
	}
	if transition.ResidentSlotID > 0 {
		if _, err := exec.ExecContext(ctx, `
			DELETE FROM openai_user_resident_slots s
			WHERE s.id = $1 AND s.user_id = $2 AND s.scope_key = $3 AND s.account_id = $4
			  AND s.generation = $5 AND s.status = 'provisional' AND s.provisional_token = $6
			  AND NOT EXISTS (SELECT 1 FROM openai_user_conversation_bindings b WHERE b.resident_slot_id = s.id)`,
			transition.ResidentSlotID, transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey),
			transition.AccountID, transition.SlotGeneration, transition.ProvisionalToken); err != nil {
			return false, err
		}
	}
	var reservationKind sql.NullString
	reservationErr := scanSingleRow(ctx, exec, `
		SELECT reservation_kind FROM account_user_contacts
		WHERE account_id = $1 AND user_id = $2 AND reservation_token = $3 FOR UPDATE`,
		[]any{transition.AccountID, transition.UserID, transition.ProvisionalToken}, &reservationKind)
	if reservationErr != nil && !errors.Is(reservationErr, sql.ErrNoRows) {
		return false, reservationErr
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE account_user_contacts SET reservation_kind = NULL, reservation_token = NULL,
			reservation_until = NULL, updated_at = $4
		WHERE account_id = $1 AND user_id = $2 AND reservation_token = $3`,
		transition.AccountID, transition.UserID, transition.ProvisionalToken, now); err != nil {
		return false, err
	}
	startedCooldown := reservationKind.Valid && reservationKind.String == "new_resident"
	if startedCooldown {
		if _, err := exec.ExecContext(ctx, `
			UPDATE accounts SET new_resident_cooldown_until = NULL, updated_at = $2
			WHERE id = $1 AND NOT EXISTS (
				SELECT 1 FROM account_user_contacts c
				WHERE c.account_id = $1 AND c.reservation_until > $2
			)`, transition.AccountID, now); err != nil {
			return false, err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (r *accountRepository) rollbackOpenAIUserResidentSlotReplacement(ctx context.Context, transition service.OpenAIUserConversationTransition) (bool, error) {
	if r == nil || r.client == nil || r.sql == nil || transition.BindingID <= 0 || transition.UserID <= 0 ||
		transition.ResidentSlotID <= 0 || transition.ReplacementSlotID <= 0 || strings.TrimSpace(transition.ProvisionalToken) == "" {
		return false, nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	now := time.Now().UTC()
	lockKey := fmt.Sprintf("%d:%s", transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey))
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return false, err
	}
	result, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			pending_resident_slot_id = NULL, pending_account_id = NULL, pending_slot_generation = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $3
		WHERE id = $1 AND pending_token = $2`, transition.BindingID, transition.ProvisionalToken, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		DELETE FROM openai_user_resident_slots
		WHERE id = $1 AND account_id = $2 AND status = 'provisional' AND provisional_token = $3`,
		transition.ResidentSlotID, transition.AccountID, transition.ProvisionalToken); err != nil {
		return false, err
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE openai_user_resident_slots SET status = 'active', updated_at = $2
		WHERE id = $1 AND status = 'replacement_pending'`, transition.ReplacementSlotID, now); err != nil {
		return false, err
	}
	var reservationKind sql.NullString
	reservationErr := scanSingleRow(ctx, exec, `
		SELECT reservation_kind FROM account_user_contacts
		WHERE account_id = $1 AND user_id = $2 AND reservation_token = $3 FOR UPDATE`,
		[]any{transition.AccountID, transition.UserID, transition.ProvisionalToken}, &reservationKind)
	if reservationErr != nil && !errors.Is(reservationErr, sql.ErrNoRows) {
		return false, reservationErr
	}
	if _, err := exec.ExecContext(ctx, `
		UPDATE account_user_contacts SET reservation_kind = NULL, reservation_token = NULL,
			reservation_until = NULL, updated_at = $4
		WHERE account_id = $1 AND user_id = $2 AND reservation_token = $3`,
		transition.AccountID, transition.UserID, transition.ProvisionalToken, now); err != nil {
		return false, err
	}
	if reservationKind.Valid && reservationKind.String == "new_resident" {
		if _, err := exec.ExecContext(ctx, `
			UPDATE accounts SET new_resident_cooldown_until = NULL, updated_at = $2
			WHERE id = $1 AND NOT EXISTS (
				SELECT 1 FROM account_user_contacts c
				WHERE c.account_id = $1 AND c.reservation_until > $2
			)`, transition.AccountID, now); err != nil {
			return false, err
		}
	}
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO user_account_placement_events
			(user_id, scope_key, placement_generation, source_account_id, target_account_id,
			 event_type, reason, config_version, effective_source, resident_slot_id)
		SELECT $1, $2, generation, account_id, $3, 'slot_replacement_failed',
		       'target_first_output_failed', $4, 'global', id
		FROM openai_user_resident_slots WHERE id = $5`, transition.UserID,
		normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey), transition.AccountID,
		transition.Config.ConfigVersion, transition.ReplacementSlotID); err != nil {
		return false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (r *accountRepository) rollbackOpenAIUserConversationFailover(ctx context.Context, transition service.OpenAIUserConversationTransition) (bool, error) {
	if r == nil || r.client == nil || r.sql == nil || transition.BindingID <= 0 || transition.UserID <= 0 ||
		strings.TrimSpace(transition.ScopeKey) == "" || strings.TrimSpace(transition.ProvisionalToken) == "" {
		return false, nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return false, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	lockKey := fmt.Sprintf("%d:%s", transition.UserID, normalizeOpenAIUserAffinityScopeKey(transition.ScopeKey))
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	result, err := exec.ExecContext(ctx, `
		UPDATE openai_user_conversation_bindings SET
			pending_resident_slot_id = NULL, pending_account_id = NULL, pending_slot_generation = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $3
		WHERE id = $1 AND pending_token = $2`, transition.BindingID, transition.ProvisionalToken, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return false, err
		}
	}
	return true, nil
}
