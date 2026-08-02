package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbevent "github.com/Wei-Shaw/sub2api/ent/creditgrantevent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	dbredeem "github.com/Wei-Shaw/sub2api/ent/redeemcode"
	dbtrigger "github.com/Wei-Shaw/sub2api/ent/usercreditgranteventtrigger"
	dbgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	dbledger "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditledger"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newCreditGrantEventTestService(t *testing.T) (*adminServiceImpl, *dbent.Client) {
	t.Helper()
	dsn := fmt.Sprintf("file:credit_grant_event_%d?mode=memory&cache=shared&_fk=1", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(10)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return &adminServiceImpl{entClient: client}, client
}

func createCreditGrantEventTestUser(t *testing.T, ctx context.Context, client *dbent.Client, role, status string) *dbent.User {
	t.Helper()
	row, err := client.User.Create().
		SetEmail(fmt.Sprintf("%s-%s@example.com", t.Name(), role)).
		SetPasswordHash("test-password-hash").
		SetRole(role).
		SetStatus(status).
		SetBalance(1).
		Save(ctx)
	require.NoError(t, err)
	return row
}

func TestCreditGrantEventCRUDAllowsDuplicateNamesAndSoftDeletes(t *testing.T) {
	ctx := context.Background()
	svc, client := newCreditGrantEventTestService(t)
	input := CreditGrantEventInput{Name: "  周年赠额  ", CreditType: CreditGrantEventTypePermanent, Amount: 2.5}

	first, err := svc.CreateCreditGrantEvent(ctx, input)
	require.NoError(t, err)
	second, err := svc.CreateCreditGrantEvent(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID)
	require.Equal(t, "周年赠额", first.Name)

	days := 7
	updated, err := svc.UpdateCreditGrantEvent(ctx, first.ID, CreditGrantEventInput{
		Name:         "周年限时赠额",
		CreditType:   CreditGrantEventTypeLimited,
		Amount:       3,
		ValidityDays: &days,
	}, first.UpdatedAt)
	require.NoError(t, err)
	require.Equal(t, CreditGrantEventTypeLimited, updated.CreditType)
	require.Equal(t, &days, updated.ValidityDays)

	require.NoError(t, svc.DeleteCreditGrantEvent(ctx, second.ID, second.UpdatedAt))
	items, total, err := svc.ListCreditGrantEvents(ctx, 1, 20, "周年")
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, items, 1)
	count, err := client.CreditGrantEvent.Query().Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	_, err = client.CreditGrantEvent.Query().Where(dbevent.IDEQ(second.ID)).Only(ctx)
	require.True(t, dbent.IsNotFound(err))
}

func TestTriggerPermanentCreditGrantEventWritesBalanceHistoryOnce(t *testing.T) {
	ctx := context.Background()
	svc, client := newCreditGrantEventTestService(t)
	userRow := createCreditGrantEventTestUser(t, ctx, client, RoleAdmin, StatusDisabled)
	event, err := svc.CreateCreditGrantEvent(ctx, CreditGrantEventInput{Name: "人工补偿", CreditType: CreditGrantEventTypePermanent, Amount: 4.25})
	require.NoError(t, err)

	status, err := svc.TriggerCreditGrantEvent(ctx, userRow.ID, event.ID)
	require.NoError(t, err)
	require.True(t, status.Triggered)
	require.Equal(t, 4.25, *status.ActualAmount)
	refreshed, err := client.User.Get(ctx, userRow.ID)
	require.NoError(t, err)
	require.Equal(t, 5.25, refreshed.Balance)

	history, err := client.RedeemCode.Query().Where(dbredeem.UsedByEQ(userRow.ID), dbredeem.TypeEQ(AdjustmentTypeAdminBalance)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, 4.25, history.Value)
	require.Equal(t, "赠额事件：人工补偿", *history.Notes)
	trigger, err := client.UserCreditGrantEventTrigger.Query().Where(dbtrigger.EventIDEQ(event.ID), dbtrigger.UserIDEQ(userRow.ID)).Only(ctx)
	require.NoError(t, err)
	require.Equal(t, CreditGrantEventTypePermanent, trigger.CreditTypeSnapshot)
	require.NotNil(t, trigger.BalanceHistoryID)
	require.Nil(t, trigger.LimitedCreditGrantID)

	_, err = svc.TriggerCreditGrantEvent(ctx, userRow.ID, event.ID)
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, infraerrors.Code(err))
	require.Equal(t, "CREDIT_GRANT_EVENT_ALREADY_TRIGGERED", infraerrors.Reason(err))
	refreshed, err = client.User.Get(ctx, userRow.ID)
	require.NoError(t, err)
	require.Equal(t, 5.25, refreshed.Balance)
}

func TestTriggerLimitedCreditGrantEventPreservesSnapshotAfterUpdateAndDelete(t *testing.T) {
	ctx := context.Background()
	svc, client := newCreditGrantEventTestService(t)
	userRow := createCreditGrantEventTestUser(t, ctx, client, RoleUser, StatusActive)
	days := 10
	event, err := svc.CreateCreditGrantEvent(ctx, CreditGrantEventInput{Name: "限时体验", CreditType: CreditGrantEventTypeLimited, Amount: 6, ValidityDays: &days})
	require.NoError(t, err)

	status, err := svc.TriggerCreditGrantEvent(ctx, userRow.ID, event.ID)
	require.NoError(t, err)
	require.Equal(t, &days, status.ActualValidityDays)
	require.NotNil(t, status.ActualExpiresAt)
	grants, err := client.UserLimitedCreditGrant.Query().Where(dbgrant.UserIDEQ(userRow.ID), dbgrant.SourceIDEQ(event.ID)).All(ctx)
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.Equal(t, LimitedCreditSourceCreditGrantEvent, grants[0].SourceType)
	ledgerCount, err := client.UserLimitedCreditLedger.Query().Where(dbledger.GrantIDEQ(grants[0].ID), dbledger.EventTypeEQ("grant")).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, ledgerCount)

	updated, err := svc.UpdateCreditGrantEvent(ctx, event.ID, CreditGrantEventInput{Name: "改为永久", CreditType: CreditGrantEventTypePermanent, Amount: 9}, event.UpdatedAt)
	require.NoError(t, err)
	statuses, err := svc.listCreditGrantEventStatuses(ctx, userRow.ID)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, CreditGrantEventTypePermanent, statuses[0].CreditType)
	require.Equal(t, 9.0, statuses[0].Amount)
	require.Equal(t, CreditGrantEventTypeLimited, *statuses[0].ActualCreditType)
	require.Equal(t, 6.0, *statuses[0].ActualAmount)
	require.Equal(t, days, *statuses[0].ActualValidityDays)

	require.NoError(t, svc.DeleteCreditGrantEvent(ctx, event.ID, updated.UpdatedAt))
	statuses, err = svc.listCreditGrantEventStatuses(ctx, userRow.ID)
	require.NoError(t, err)
	require.Empty(t, statuses)
	triggerCount, err := client.UserCreditGrantEventTrigger.Query().Where(dbtrigger.EventIDEQ(event.ID), dbtrigger.UserIDEQ(userRow.ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, triggerCount)
	grantCount, err := client.UserLimitedCreditGrant.Query().Where(dbgrant.IDEQ(grants[0].ID)).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, grantCount)
}

func TestValidateCreditGrantEventInput(t *testing.T) {
	days := 7
	_, err := validateCreditGrantEventInput(CreditGrantEventInput{Name: "事件", CreditType: CreditGrantEventTypeLimited, Amount: 0.00000001, ValidityDays: &days})
	require.NoError(t, err)
	for _, input := range []CreditGrantEventInput{
		{Name: "", CreditType: CreditGrantEventTypePermanent, Amount: 1},
		{Name: "事件", CreditType: CreditGrantEventTypePermanent, Amount: 1, ValidityDays: &days},
		{Name: "事件", CreditType: CreditGrantEventTypeLimited, Amount: 1},
		{Name: "事件", CreditType: "other", Amount: 1},
		{Name: "事件", CreditType: CreditGrantEventTypePermanent, Amount: 1.000000001},
	} {
		_, err = validateCreditGrantEventInput(input)
		require.Error(t, err)
	}
}
