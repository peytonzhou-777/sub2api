//go:build unit

package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestQualifyingSecurityDepositBonusGroup_UsesCurrentRiskThreshold(t *testing.T) {
	groups := []Group{
		{ID: 3, Name: "低门槛", SecurityDepositBaseRequiredCents: 10000},
		{ID: 5, Name: "高门槛", SecurityDepositBaseRequiredCents: 15000},
		{ID: 1, Name: "无门槛", SecurityDepositBaseRequiredCents: 0},
	}

	group, required := qualifyingSecurityDepositBonusGroup(groups, 2, 19999)
	require.Nil(t, group)
	require.Zero(t, required)

	group, required = qualifyingSecurityDepositBonusGroup(groups, 2, 30000)
	require.NotNil(t, group)
	require.EqualValues(t, 3, group.ID)
	require.EqualValues(t, 20000, required)
}

func TestSecurityDepositBonusCapAmount_UsesCNYFaceValueAsUSDCap(t *testing.T) {
	require.Equal(t, "100", securityDepositBonusCapAmount(10000, 100).String())
	require.Equal(t, "125", securityDepositBonusCapAmount(10000, 125).String())
	require.True(t, securityDepositBonusCapAmount(10000, 0).IsZero())
}

func TestNextSecurityDepositBonusMidnight_UsesAsiaShanghai(t *testing.T) {
	now := time.Date(2026, 8, 23, 15, 30, 0, 0, time.UTC)
	next := nextSecurityDepositBonusMidnight(now)

	require.Equal(t, "2026-08-24T00:00:00+08:00", next.Format(time.RFC3339))
}

func TestSecurityDepositBonusEstimate_ReportsActualHeadroom(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	settingRepo := &securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositEnforcementEnabled: "true",
		SettingKeySecurityDepositBonusDailyAmount:   "5",
		SettingKeySecurityDepositBonusCapRatio:      "100",
	}}
	settings := NewSettingService(settingRepo, &config.Config{})
	bonus := NewSecurityDepositBonusService(
		db,
		settings,
		&fakeSecurityDepositRepository{},
		fakeSecurityDepositGroupAccess{groups: []Group{{
			ID: 9, Name: "安全分组", SecurityDepositBaseRequiredCents: 10000,
		}}},
		nil,
		nil,
	)
	now := time.Date(2026, 8, 23, 15, 30, 0, 0, time.UTC)
	bonus.now = func() time.Time { return now }
	mock.ExpectQuery("SELECT initial_amount, used_amount, frozen_amount, expires_at, status").
		WithArgs(int64(7), LimitedCreditSourceSecurityDepositBonus).
		WillReturnRows(sqlmock.NewRows([]string{"initial_amount", "used_amount", "frozen_amount", "expires_at", "status"}).
			AddRow("149", "2", "0", now.Add(48*time.Hour), LimitedCreditStatusActive))

	estimate, err := bonus.GetEstimate(context.Background(), 7, &SecurityDepositAccountSummary{
		EffectiveBalanceCents: 15000,
		RiskMultiplier:        1,
	})

	require.NoError(t, err)
	require.Equal(t, "eligible", estimate.Reason)
	require.Equal(t, 150.0, estimate.CapAmount)
	require.Equal(t, 147.0, estimate.CurrentAmount)
	require.Equal(t, 3.0, estimate.EstimatedGrantAmount)
	require.Equal(t, "2026-08-24T00:00:00+08:00", estimate.NextGrantAt.Format(time.RFC3339))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSecurityDepositBonusEstimate_CountsExpiredFrozenAmountAgainstCap(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	bonus := &SecurityDepositBonusService{db: db}
	now := time.Date(2026, 8, 23, 15, 30, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT initial_amount, used_amount, frozen_amount, expires_at, status").
		WithArgs(int64(7), LimitedCreditSourceSecurityDepositBonus).
		WillReturnRows(sqlmock.NewRows([]string{"initial_amount", "used_amount", "frozen_amount", "expires_at", "status"}).
			AddRow("20", "10", "4", now.Add(-time.Hour), LimitedCreditStatusActive))

	current, expiresAt, err := bonus.currentBonusAmount(context.Background(), 7, now)

	require.NoError(t, err)
	require.Equal(t, "4", current.String())
	require.Nil(t, expiresAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplySecurityDepositBonusGrant_CapsNewGrantAndWritesDailyLedger(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id,initial_amount,used_amount,frozen_amount,security_deposit_bonus_pending_revoke_amount").
		WithArgs(int64(7), LimitedCreditSourceSecurityDepositBonus).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO user_limited_credit_grants").
		WithArgs(int64(7), LimitedCreditSourceSecurityDepositBonus, decimal.NewFromInt(4), sqlmock.AnyArg(), LimitedCreditStatusActive, "保证金每日赠额").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(15))
	mock.ExpectExec("INSERT INTO user_limited_credit_ledger").
		WithArgs(int64(7), int64(15), "security_deposit_bonus_grant", decimal.NewFromInt(4), "security-deposit-bonus:2026-08-23", "保证金赠额日批次").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	batch := &securityDepositBonusBatch{
		BusinessDate: time.Date(2026, 8, 23, 0, 0, 0, 0, securityDepositBonusLocation()),
		ExpiresAt:    time.Date(2026, 8, 30, 0, 0, 0, 0, securityDepositBonusLocation()),
		DailyAmount:  decimal.NewFromInt(5),
	}
	grantID, before, added, after, err := applySecurityDepositBonusGrant(
		context.Background(), tx, batch, &securityDepositBonusBatchItem{UserID: 7, CapAmount: decimal.NewFromInt(4)},
	)

	require.NoError(t, err)
	require.EqualValues(t, 15, *grantID)
	require.True(t, before.IsZero())
	require.Equal(t, "4", added.String())
	require.Equal(t, "4", after.String())
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestApplySecurityDepositBonusGrant_RenewsAtCapAndRebasesUsage(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)

	startedAt := time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)
	expiresAt := startedAt.Add(7 * 24 * time.Hour)
	mock.ExpectQuery("SELECT id,initial_amount,used_amount,frozen_amount,security_deposit_bonus_pending_revoke_amount").
		WithArgs(int64(7), LimitedCreditSourceSecurityDepositBonus).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "initial_amount", "used_amount", "frozen_amount", "security_deposit_bonus_pending_revoke_amount", "expires_at", "status",
		}).AddRow(15, "10", "2", "3", "1", startedAt.Add(time.Hour), LimitedCreditStatusActive))
	mock.ExpectExec(`(?s)UPDATE user_limited_credit_grants.*SET initial_amount=\$1,used_amount=0`).
		WithArgs(decimal.NewFromInt(8), expiresAt, LimitedCreditStatusActive, "保证金每日赠额", decimal.NewFromInt(1), int64(15)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO user_limited_credit_ledger").
		WithArgs(int64(7), int64(15), "security_deposit_bonus_renew", decimal.Zero, "security-deposit-bonus:2026-08-23", "保证金赠额日批次").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	grantID, before, added, after, err := applySecurityDepositBonusGrant(
		context.Background(),
		tx,
		&securityDepositBonusBatch{
			BusinessDate: time.Date(2026, 8, 23, 0, 0, 0, 0, securityDepositBonusLocation()),
			StartedAt:    startedAt,
			ExpiresAt:    expiresAt,
			DailyAmount:  decimal.NewFromInt(5),
		},
		&securityDepositBonusBatchItem{UserID: 7, CapAmount: decimal.NewFromInt(8)},
	)

	require.NoError(t, err)
	require.EqualValues(t, 15, *grantID)
	require.Equal(t, "8", before.String())
	require.True(t, added.IsZero())
	require.Equal(t, "8", after.String())
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRevokeSecurityDepositBonus_UsesUsedAmountForFullRevocation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	bonus := &SecurityDepositBonusService{db: db}

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(securityDepositBonusUserLockNS, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO security_deposit_bonus_reconciliations").
		WithArgs(int64(7), "refund", int64(12), decimal.Zero).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(21))
	mock.ExpectQuery("SELECT id,initial_amount,used_amount,frozen_amount").
		WithArgs(int64(7), LimitedCreditSourceSecurityDepositBonus).
		WillReturnRows(sqlmock.NewRows([]string{"id", "initial_amount", "used_amount", "frozen_amount"}).
			AddRow(15, "10", "0", "0"))
	mock.ExpectExec(`(?s)UPDATE user_limited_credit_grants.*SET used_amount=used_amount\+\$1`).
		WithArgs(decimal.NewFromInt(10), decimal.Zero, securityDepositBonusEpsilon, LimitedCreditStatusDepleted, int64(15)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO user_limited_credit_ledger").
		WithArgs(int64(7), int64(15), decimal.NewFromInt(10), "security-deposit:refund:12", "保证金退款或扣除后撤销赠额").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE security_deposit_bonus_reconciliations").
		WithArgs(int64(21), decimal.NewFromInt(10), decimal.Zero).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	err = bonus.revokeBonusToTarget(context.Background(), 7, "refund", 12, decimal.Zero)

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
