package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

// ReconcileOpenAIUserAffinity 关闭过期暂态并返回修复计数；调用方负责低频触发。
func (r *accountRepository) ReconcileOpenAIUserAffinity(ctx context.Context, now time.Time) (map[string]int64, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("openai user affinity storage unavailable")
	}
	tx, err := r.client.Tx(ctx)
	if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
		return nil, err
	}
	exec := r.sql
	if tx != nil {
		defer func() { _ = tx.Rollback() }()
		exec = tx.Client()
	}
	statements := []struct {
		name string
		sql  string
	}{
		{"resident_slots", `UPDATE openai_user_resident_slots SET status = 'expired',
			provisional_token = NULL, updated_at = $1
			WHERE status IN ('provisional', 'active', 'replacement_pending') AND expires_at <= $1`},
		{"conversation_bindings", `UPDATE openai_user_conversation_bindings SET status = 'expired',
			pending_resident_slot_id = NULL, pending_account_id = NULL, pending_slot_generation = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $1
			WHERE status IN ('provisional', 'active', 'draining', 'reset') AND expires_at <= $1`},
		{"conversation_pending", `UPDATE openai_user_conversation_bindings SET
			pending_resident_slot_id = NULL, pending_account_id = NULL, pending_slot_generation = NULL,
			pending_token = NULL, pending_expires_at = NULL, updated_at = $1
			WHERE pending_expires_at <= $1`},
		{"replacement_victims", `UPDATE openai_user_resident_slots victim SET status = 'active', updated_at = $1
			WHERE victim.status = 'replacement_pending' AND EXISTS (
				SELECT 1 FROM openai_user_resident_slots target
				WHERE target.replacement_source_slot_id = victim.id
				  AND target.status IN ('provisional', 'expired') AND target.expires_at <= $1
			)`},
		{"draining_slots", `UPDATE openai_user_resident_slots s SET status = 'expired', updated_at = $1
			WHERE s.status = 'draining' AND NOT EXISTS (
				SELECT 1 FROM openai_user_conversation_bindings b
				WHERE b.resident_slot_id = s.id AND b.status IN ('provisional', 'active', 'draining')
				  AND b.expires_at > $1
			)`},
		{"reset_slots", `UPDATE openai_user_resident_slots s SET status = 'expired', updated_at = $1
			WHERE s.status = 'reset' AND NOT EXISTS (
				SELECT 1 FROM openai_user_conversation_bindings b
				WHERE b.resident_slot_id = s.id AND b.status = 'draining' AND b.expires_at > $1
			)`},
		// Placement 收敛放在 slot/binding 之后，与按 scope 的 Converge 保持一致锁顺序。
		{"placements", `UPDATE user_account_placements SET status = 'expired', updated_at = $1
			WHERE status = 'active' AND expires_at <= $1`},
		// 别名清理与按 scope 的 TTL 收敛可能并发触碰同一行；先有序加锁并跳过占用行，避免死锁。
		{"conversation_aliases", `WITH expired_aliases AS (
			SELECT id FROM openai_user_conversation_aliases
			WHERE expires_at <= $1
			ORDER BY id
			FOR UPDATE SKIP LOCKED
		)
		DELETE FROM openai_user_conversation_aliases a
		USING expired_aliases e
		WHERE a.id = e.id`},
		{"reset_exclusions", `DELETE FROM openai_user_affinity_reset_exclusions
			WHERE consumed_at IS NOT NULL AND consumed_at <= $1::timestamptz - INTERVAL '30 days'`},
		{"contacts", `UPDATE account_user_contacts SET reservation_kind = NULL,
			reservation_token = NULL, reservation_until = NULL, reentry_batch_token = NULL,
			reentry_state = CASE WHEN reentry_state IN ('leader_pending', 'stagger_releasing') THEN 'failed' ELSE reentry_state END,
			leader_token = NULL, leader_lease_until = NULL, updated_at = $1
			WHERE reservation_until <= $1 AND (touch_expires_at IS NULL OR touch_expires_at <= $1)`},
		{"contact_periods", `UPDATE account_user_contact_periods SET closed_at = $1, updated_at = $1
			WHERE closed_at IS NULL AND touch_expires_at <= $1`},
		{"capacity_incidents", `UPDATE user_account_capacity_incidents SET status = 'expired',
			closed_at = $1, close_reason = 'window_expired', updated_at = $1
			WHERE closed_at IS NULL AND window_expires_at <= $1`},
	}
	counts := make(map[string]int64, len(statements))
	for _, statement := range statements {
		result, execErr := exec.ExecContext(ctx, statement.sql, now.UTC())
		if execErr != nil {
			return nil, fmt.Errorf("reconcile openai user affinity %s: %w", statement.name, execErr)
		}
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return nil, rowsErr
		}
		counts[statement.name] = affected
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
	}
	return counts, nil
}
