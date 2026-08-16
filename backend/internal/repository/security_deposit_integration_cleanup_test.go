//go:build integration

package repository

import (
	"context"
	"testing"
)

// cleanupSecurityDepositIntegrationData 按外键逆序清理保证金集成测试数据，避免测试污染共享数据库。
func cleanupSecurityDepositIntegrationData(t *testing.T, userIDs []int64, groupIDs []int64) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		for _, userID := range userIDs {
			// 账本引用退款、违规和批次，必须最先删除。
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_ledger WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_admin_actions WHERE user_id = $1 OR operator_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_refunds WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_risk_events WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_agreements WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_risk_profiles WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_violations WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_lots WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM security_deposit_accounts WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM api_keys WHERE user_id = $1`, userID)
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM payment_orders WHERE user_id = $1`, userID)
		}
		for _, groupID := range groupIDs {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM groups WHERE id = $1`, groupID)
		}
		for _, userID := range userIDs {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, userID)
		}
	})
}
