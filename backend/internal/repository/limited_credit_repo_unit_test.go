//go:build unit

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	dbgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	dbledger "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditledger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newLimitedCreditRepoSQLite(t *testing.T) (*limitedCreditRepository, *dbent.Client) {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(10)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })

	return &limitedCreditRepository{client: client, db: db}, client
}

func mustCreateLimitedCreditRepoUser(t *testing.T, ctx context.Context, client *dbent.Client) int64 {
	t.Helper()

	user, err := client.User.Create().
		SetEmail(fmt.Sprintf("%s@example.com", t.Name())).
		SetPasswordHash("test-password-hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	return user.ID
}

func TestLimitedCreditRepositoryCreateGrantsIndependentPersistsGrantAndLedgerRows(t *testing.T) {
	ctx := context.Background()
	repo, client := newLimitedCreditRepoSQLite(t)
	userID := mustCreateLimitedCreditRepoUser(t, ctx, client)
	expiresAt := time.Now().UTC().Add(24 * time.Hour)

	created, err := repo.CreateGrantsIndependent(ctx, []*service.LimitedCreditGrant{
		{UserID: userID, SourceType: service.LimitedCreditSourceDefaultUserSetting, InitialAmount: 2.5, ExpiresAt: expiresAt},
		{UserID: userID, SourceType: service.LimitedCreditSourceDefaultUserSetting, InitialAmount: 4, ExpiresAt: expiresAt},
	})

	require.NoError(t, err)
	require.Len(t, created, 2)
	grants, err := client.UserLimitedCreditGrant.Query().
		Where(dbgrant.UserIDEQ(userID)).
		All(ctx)
	require.NoError(t, err)
	require.Len(t, grants, 2)
	require.Equal(t, service.LimitedCreditSourceDefaultUserSetting, grants[0].SourceType)
	require.Nil(t, grants[0].SourceID)
	ledgerCount, err := client.UserLimitedCreditLedger.Query().
		Where(dbledger.UserIDEQ(userID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, ledgerCount)
}

func TestLimitedCreditRepositoryCreateGrantsIndependentRollsBackWholeBatch(t *testing.T) {
	ctx := context.Background()
	repo, client := newLimitedCreditRepoSQLite(t)
	userID := mustCreateLimitedCreditRepoUser(t, ctx, client)

	_, err := repo.CreateGrantsIndependent(ctx, []*service.LimitedCreditGrant{
		{
			UserID:        userID,
			SourceType:    service.LimitedCreditSourceDefaultUserSetting,
			InitialAmount: 2.5,
			ExpiresAt:     time.Now().UTC().Add(24 * time.Hour),
		},
		nil,
	})

	require.Error(t, err)
	grantCount, countErr := client.UserLimitedCreditGrant.Query().
		Where(dbgrant.UserIDEQ(userID)).
		Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, grantCount)
	ledgerCount, countErr := client.UserLimitedCreditLedger.Query().
		Where(dbledger.UserIDEQ(userID)).
		Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, ledgerCount)
}

func TestLimitedCreditRepositoryCreateGrantsIndependentDoesNotJoinOuterTransaction(t *testing.T) {
	ctx := context.Background()
	repo, client := newLimitedCreditRepoSQLite(t)
	userID := mustCreateLimitedCreditRepoUser(t, ctx, client)

	outerTx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = outerTx.Rollback() })
	outerCtx := dbent.NewTxContext(ctx, outerTx)

	created, err := repo.CreateGrantsIndependent(outerCtx, []*service.LimitedCreditGrant{{
		UserID:        userID,
		SourceType:    service.LimitedCreditSourceDefaultUserSetting,
		InitialAmount: 3,
		ExpiresAt:     time.Now().UTC().Add(24 * time.Hour),
	}})
	require.NoError(t, err)
	require.Len(t, created, 1)
	require.NoError(t, outerTx.Rollback())

	grantCount, err := client.UserLimitedCreditGrant.Query().
		Where(dbgrant.UserIDEQ(userID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, grantCount)
	ledgerCount, err := client.UserLimitedCreditLedger.Query().
		Where(dbledger.UserIDEQ(userID)).
		Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, ledgerCount)
}

func TestLimitedCreditRepositoryListActiveByUserAttachesSourceReasons(t *testing.T) {
	ctx := context.Background()
	repo, client := newLimitedCreditRepoSQLite(t)
	userID := mustCreateLimitedCreditRepoUser(t, ctx, client)
	now := time.Now().UTC()

	resetBatch, err := client.ResetRebateBatch.Create().
		SetGroupID(1).
		SetGroupName("测试分组").
		SetAdminID(1).
		SetPeriodStart(now.Add(-time.Hour)).
		SetPeriodEnd(now).
		SetRebateReason("官方重置！本站返利！").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(userID).
		SetUserEmail("source@example.com").
		SetUserName("source-user").
		SetAmount(10).
		SetPayAmount(10).
		SetRechargeCode("SOURCE-ORDER").
		SetOutTradeNo("SOURCE-ORDER").
		SetPaymentType("test").
		SetPaymentTradeNo("SOURCE-TRADE").
		SetOrderType("balance").
		SetStatus("COMPLETED").
		SetExpiresAt(now.Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("example.com").
		SetRechargeBonusCampaignID(2).
		SetRechargeBonusCampaignName("暑期充值活动").
		Save(ctx)
	require.NoError(t, err)

	recurringBatch, err := client.RecurringCreditBatch.Create().
		SetTaskID(3).
		SetTaskName("每周赠额").
		SetScheduledAt(now).
		SetExpiresAt(now.AddDate(0, 0, 7)).
		SetQualificationStart(now.Add(-24 * time.Hour)).
		SetQualificationEnd(now).
		SetConfigVersion(1).
		SetScheduleType("weekly").
		SetLocalTime("09:00").
		SetTimezone("UTC").
		SetAmount(2).
		SetExecutionMode("finite").
		SetStatus("succeeded").
		Save(ctx)
	require.NoError(t, err)

	creditEvent, err := client.CreditGrantEvent.Create().
		SetName("新人专项赠额").
		SetCreditType(service.CreditGrantEventTypeLimited).
		SetAmount(4).
		SetValidityDays(7).
		Save(ctx)
	require.NoError(t, err)

	for _, grant := range []*service.LimitedCreditGrant{
		{UserID: userID, SourceType: service.LimitedCreditSourceResetRebate, SourceID: &resetBatch.ID, InitialAmount: 1, ExpiresAt: now.Add(time.Hour)},
		{UserID: userID, SourceType: service.LimitedCreditSourceRechargeBonus, SourceID: &order.ID, InitialAmount: 2, ExpiresAt: now.Add(2 * time.Hour)},
		{UserID: userID, SourceType: service.LimitedCreditSourceRecurring, SourceID: &recurringBatch.ID, InitialAmount: 3, ExpiresAt: now.Add(3 * time.Hour)},
		{UserID: userID, SourceType: service.LimitedCreditSourceCreditGrantEvent, SourceID: &creditEvent.ID, InitialAmount: 4, ExpiresAt: now.Add(4 * time.Hour), Notes: "赠额事件：新人专项赠额"},
	} {
		_, err = repo.CreateGrant(ctx, grant)
		require.NoError(t, err)
	}
	require.NoError(t, client.CreditGrantEvent.DeleteOne(creditEvent).Exec(ctx))

	grants, err := repo.ListActiveByUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, grants, 4)
	reasons := make(map[string]string, len(grants))
	for _, grant := range grants {
		reasons[grant.SourceType] = grant.SourceReason
	}
	require.Equal(t, "官方重置！本站返利！", reasons[service.LimitedCreditSourceResetRebate])
	require.Equal(t, "暑期充值活动", reasons[service.LimitedCreditSourceRechargeBonus])
	require.Equal(t, "每周赠额", reasons[service.LimitedCreditSourceRecurring])
	require.Equal(t, "新人专项赠额", reasons[service.LimitedCreditSourceCreditGrantEvent])
}

func TestLimitedCreditRepositoryListDepletedByUserFiltersByExhaustedWindow(t *testing.T) {
	ctx := context.Background()
	repo, client := newLimitedCreditRepoSQLite(t)
	userID := mustCreateLimitedCreditRepoUser(t, ctx, client)
	otherUser, err := client.User.Create().
		SetEmail("other-" + t.Name() + "@example.com").
		SetPasswordHash("test-password-hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		Save(ctx)
	require.NoError(t, err)
	otherUserID := otherUser.ID
	now := time.Now().UTC().Truncate(time.Second)

	createGrant := func(userID int64, status string, updatedAt time.Time) int64 {
		t.Helper()
		usedAmount := 5.0
		if status == service.LimitedCreditStatusActive {
			usedAmount = 1
		}
		grant, err := client.UserLimitedCreditGrant.Create().
			SetUserID(userID).
			SetSourceType(service.LimitedCreditSourceDefaultUserSetting).
			SetInitialAmount(5).
			SetUsedAmount(usedAmount).
			SetExpiresAt(now.AddDate(0, 1, 0)).
			SetStatus(status).
			SetUpdatedAt(updatedAt).
			Save(ctx)
		require.NoError(t, err)
		return grant.ID
	}

	olderID := createGrant(userID, service.LimitedCreditStatusDepleted, now.Add(-4*time.Hour))
	newerID := createGrant(userID, service.LimitedCreditStatusDepleted, now.Add(-2*time.Hour))
	createGrant(userID, service.LimitedCreditStatusDepleted, now.Add(-48*time.Hour))
	createGrant(userID, service.LimitedCreditStatusActive, now.Add(-time.Hour))
	createGrant(otherUserID, service.LimitedCreditStatusDepleted, now.Add(-time.Hour))

	grants, err := repo.ListDepletedByUser(ctx, userID, now.Add(-24*time.Hour), now)

	require.NoError(t, err)
	require.Len(t, grants, 2)
	require.Equal(t, []int64{newerID, olderID}, []int64{grants[0].ID, grants[1].ID})
	require.Equal(t, service.LimitedCreditStatusDepleted, grants[0].Status)
	require.WithinDuration(t, now.Add(-2*time.Hour), grants[0].UpdatedAt, time.Second)
}
