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
