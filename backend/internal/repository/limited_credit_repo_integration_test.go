//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	dbledger "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditledger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestLimitedCreditRepository_CreateGrants_PersistsGrantAndLedgerRows(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	t.Cleanup(func() { _ = client.User.DeleteOneID(user.ID).Exec(ctx) })
	repo := NewLimitedCreditRepository(client, nil)
	expiresAt := time.Now().UTC().Add(24 * time.Hour)

	created, err := repo.CreateGrantsIndependent(ctx, []*service.LimitedCreditGrant{
		{UserID: user.ID, SourceType: service.LimitedCreditSourceDefaultUserSetting, InitialAmount: 2.5, ExpiresAt: expiresAt},
		{UserID: user.ID, SourceType: service.LimitedCreditSourceDefaultUserSetting, InitialAmount: 4, ExpiresAt: expiresAt},
	})

	require.NoError(t, err)
	require.Len(t, created, 2)
	grantCount, err := client.UserLimitedCreditGrant.Query().Where(dbgrant.UserIDEQ(user.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, grantCount)
	ledgerCount, err := client.UserLimitedCreditLedger.Query().Where(dbledger.UserIDEQ(user.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, ledgerCount)
}

func TestLimitedCreditRepository_CreateGrants_RollsBackWholeBatch(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	t.Cleanup(func() { _ = client.User.DeleteOneID(user.ID).Exec(ctx) })
	repo := NewLimitedCreditRepository(client, nil)
	expiresAt := time.Now().UTC().Add(24 * time.Hour)

	_, err := repo.CreateGrantsIndependent(ctx, []*service.LimitedCreditGrant{
		{UserID: user.ID, SourceType: service.LimitedCreditSourceDefaultUserSetting, InitialAmount: 2.5, ExpiresAt: expiresAt},
		nil,
	})

	require.Error(t, err)
	grantCount, countErr := client.UserLimitedCreditGrant.Query().Where(dbgrant.UserIDEQ(user.ID)).Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, grantCount)
	ledgerCount, countErr := client.UserLimitedCreditLedger.Query().Where(dbledger.UserIDEQ(user.ID)).Count(ctx)
	require.NoError(t, countErr)
	require.Zero(t, ledgerCount)
}

func TestLimitedCreditRepository_CreateGrantsIndependent_DoesNotJoinOuterTransaction(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{})
	t.Cleanup(func() { _ = client.User.DeleteOneID(user.ID).Exec(ctx) })
	repo := NewLimitedCreditRepository(client, nil)

	outerTx, err := client.Tx(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = outerTx.Rollback() })
	outerCtx := dbent.NewTxContext(ctx, outerTx)

	created, err := repo.CreateGrantsIndependent(outerCtx, []*service.LimitedCreditGrant{{
		UserID:        user.ID,
		SourceType:    service.LimitedCreditSourceDefaultUserSetting,
		InitialAmount: 3,
		ExpiresAt:     time.Now().UTC().Add(24 * time.Hour),
	}})
	require.NoError(t, err)
	require.Len(t, created, 1)
	require.NoError(t, outerTx.Rollback())

	grantCount, err := client.UserLimitedCreditGrant.Query().Where(dbgrant.UserIDEQ(user.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, grantCount)
	ledgerCount, err := client.UserLimitedCreditLedger.Query().Where(dbledger.UserIDEQ(user.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, ledgerCount)
}
