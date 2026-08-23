//go:build integration

package service

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestSecurityDepositBonusSnapshotRunsInPostgres 验证资格快照的真实 PostgreSQL 类型和分组权限口径。
func TestSecurityDepositBonusSnapshotRunsInPostgres(t *testing.T) {
	ctx, db := openRecurringCreditPostgres(t)
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback()) })

	_, err = tx.ExecContext(ctx, `
CREATE TEMP TABLE security_deposit_accounts (
    user_id BIGINT NOT NULL,
    balance_cents BIGINT NOT NULL,
    refund_reserved_cents BIGINT NOT NULL
);
CREATE TEMP TABLE security_deposit_risk_profiles (
    user_id BIGINT PRIMARY KEY,
    risk_multiplier BIGINT NOT NULL
);
CREATE TEMP TABLE groups (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL,
    security_deposit_base_required_cents BIGINT NOT NULL,
    deleted_at TIMESTAMPTZ,
    status TEXT NOT NULL,
    subscription_type TEXT NOT NULL,
    is_exclusive BOOLEAN NOT NULL
);
CREATE TEMP TABLE user_subscriptions (
    user_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    status TEXT NOT NULL,
    deleted_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE TEMP TABLE user_allowed_groups (
    user_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL
);
CREATE TEMP TABLE security_deposit_bonus_batch_items (
    batch_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    effective_balance_cents BIGINT NOT NULL,
    risk_multiplier BIGINT NOT NULL,
    qualifying_group_id BIGINT NOT NULL,
    qualifying_group_name TEXT NOT NULL,
    required_cents BIGINT NOT NULL,
    cap_amount NUMERIC(20,8) NOT NULL,
    UNIQUE(batch_id,user_id)
);`)
	require.NoError(t, err)

	startedAt := time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)
	_, err = tx.ExecContext(ctx, `
INSERT INTO security_deposit_accounts(user_id,balance_cents,refund_reserved_cents)
VALUES (7,12000,0),(7,9000,1000),(8,10000,0);
INSERT INTO security_deposit_risk_profiles(user_id,risk_multiplier) VALUES(7,2);
INSERT INTO groups(id,name,security_deposit_base_required_cents,status,subscription_type,is_exclusive)
VALUES (3,'公开安全分组',10000,'active','standard',FALSE),
       (4,'订阅安全分组',10000,'active','subscription',FALSE);
UPDATE groups SET is_exclusive=TRUE WHERE id=3;
INSERT INTO user_allowed_groups(user_id,group_id) VALUES(7,3);
INSERT INTO user_subscriptions(user_id,group_id,status,expires_at)
VALUES(8,4,'active',$1);`, startedAt.Add(24*time.Hour))
	require.NoError(t, err)

	err = snapshotSecurityDepositBonusEligibility(ctx, tx, &securityDepositBonusBatch{
		ID:          11,
		StartedAt:   startedAt,
		CapRatio:    decimal.NewFromInt(100),
		DailyAmount: decimal.NewFromInt(5),
	})
	require.NoError(t, err)

	rows, err := tx.QueryContext(ctx, `
SELECT user_id,risk_multiplier,qualifying_group_id,required_cents,cap_amount
FROM security_deposit_bonus_batch_items
ORDER BY user_id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()

	type item struct {
		userID, multiplier, groupID, required int64
		cap                                   decimal.Decimal
	}
	var items []item
	for rows.Next() {
		var got item
		require.NoError(t, rows.Scan(&got.userID, &got.multiplier, &got.groupID, &got.required, &got.cap))
		items = append(items, got)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []item{
		{userID: 7, multiplier: 2, groupID: 3, required: 20000, cap: decimal.NewFromInt(200)},
		{userID: 8, multiplier: 1, groupID: 4, required: 10000, cap: decimal.NewFromInt(100)},
	}, items)
}
