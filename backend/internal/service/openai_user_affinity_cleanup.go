package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// maintainOpenAIUserAffinity 仅在策略启用时执行数据库权威状态清理。
func (s *OpsCleanupService) maintainOpenAIUserAffinity(ctx context.Context, now time.Time) {
	if !s.openAIUserAffinityEnabled(ctx) {
		return
	}
	if err := s.reconcileOpenAIUserAffinity(ctx, now); err != nil {
		logger.LegacyPrintf("service.ops_cleanup", "[OpsCleanup] openai user affinity reconciliation failed: %v", err)
	}
}

func (s *OpsCleanupService) openAIUserAffinityEnabled(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIUserAffinityScheduling)
	if err != nil {
		return false
	}
	var cfg OpenAIUserAffinityConfig
	return json.Unmarshal([]byte(raw), &cfg) == nil && cfg.Enabled
}

// reconcileOpenAIUserAffinity 按数据库时间关闭过期暂态，Redis 丢失时也能恢复权威计数。
func (s *OpsCleanupService) reconcileOpenAIUserAffinity(ctx context.Context, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	statements := []struct {
		name string
		sql  string
	}{
		{"placements", `UPDATE user_account_placements SET status = 'expired', updated_at = $1
			WHERE status = 'active' AND expires_at <= $1`},
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
	for _, statement := range statements {
		result, execErr := tx.ExecContext(ctx, statement.sql, now)
		if execErr != nil {
			return fmt.Errorf("reconcile openai user affinity %s: %w", statement.name, execErr)
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr == nil && affected > 0 {
			logger.LegacyPrintf("service.ops_cleanup", "[OpsCleanup] reconciled openai user affinity %s=%d", statement.name, affected)
		}
	}
	return tx.Commit()
}
