//go:build unit

package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newRechargeBonusTestService(t *testing.T) (*RechargeBonusService, *dbent.Client) {
	t.Helper()
	dsn := fmt.Sprintf("file:recharge_bonus_%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, func() error {
		_, execErr := db.Exec("PRAGMA foreign_keys = ON")
		return execErr
	}())

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return NewRechargeBonusService(client, nil, nil, nil), client
}

func validRechargeBonusCampaignInput(startAt, endAt time.Time) RechargeBonusCampaignInput {
	return RechargeBonusCampaignInput{
		Name:               "暑期充值活动",
		Description:        "充值越多，赠送越多\n限时额度有效 30 天",
		StartAt:            startAt.UTC(),
		EndAt:              endAt.UTC(),
		ParticipationLimit: 2,
		Tiers: []RechargeBonusTier{
			{MinAmount: 10, MaxAmount: 100, MinRate: 5, MaxRate: 5},
			{MinAmount: 100, MaxAmount: 500, MinRate: 5, MaxRate: 10},
		},
	}
}

func TestRechargeBonusService_CreateAndListCampaigns(t *testing.T) {
	svc, _ := newRechargeBonusTestService(t)
	ctx := context.Background()
	startAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	input := validRechargeBonusCampaignInput(startAt, startAt.Add(7*24*time.Hour))

	created, err := svc.CreateCampaign(ctx, input)
	require.NoError(t, err)
	require.Equal(t, input.Name, created.Name)
	require.Equal(t, input.Description, created.Description)
	require.Equal(t, input.StartAt, created.StartAt)
	require.Equal(t, input.EndAt, created.EndAt)
	require.Len(t, created.Tiers, 2)

	items, err := svc.ListCampaigns(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, RechargeBonusCampaignStatusScheduled, items[0].Status)
}

func TestRechargeBonusService_RejectsOverlappingCampaignButAllowsAdjacency(t *testing.T) {
	svc, _ := newRechargeBonusTestService(t)
	ctx := context.Background()
	startAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	first := validRechargeBonusCampaignInput(startAt, startAt.Add(24*time.Hour))
	_, err := svc.CreateCampaign(ctx, first)
	require.NoError(t, err)

	overlap := validRechargeBonusCampaignInput(startAt.Add(23*time.Hour), startAt.Add(48*time.Hour))
	_, err = svc.CreateCampaign(ctx, overlap)
	require.Error(t, err)

	adjacent := validRechargeBonusCampaignInput(first.EndAt, first.EndAt.Add(24*time.Hour))
	adjacent.Name = "下一期活动"
	_, err = svc.CreateCampaign(ctx, adjacent)
	require.NoError(t, err)
}

func TestRechargeBonusService_StartedCampaignOnlyAllowsEarlyEnd(t *testing.T) {
	svc, _ := newRechargeBonusTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	input := validRechargeBonusCampaignInput(now.Add(-time.Hour), now.Add(time.Hour))
	created, err := svc.CreateCampaign(ctx, input)
	require.NoError(t, err)

	earlyEnd := input
	earlyEnd.EndAt = now.Add(30 * time.Minute)
	updated, err := svc.UpdateCampaign(ctx, created.ID, earlyEnd)
	require.NoError(t, err)
	require.Equal(t, earlyEnd.EndAt, updated.EndAt)

	changed := earlyEnd
	changed.Description = "活动已经开始后不可修改描述"
	_, err = svc.UpdateCampaign(ctx, created.ID, changed)
	require.Error(t, err)

	extended := earlyEnd
	extended.EndAt = input.EndAt.Add(time.Hour)
	_, err = svc.UpdateCampaign(ctx, created.ID, extended)
	require.Error(t, err)
}

func TestRechargeBonusService_DeleteOnlyScheduledCampaign(t *testing.T) {
	svc, client := newRechargeBonusTestService(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	scheduled, err := svc.CreateCampaign(ctx, validRechargeBonusCampaignInput(now.Add(time.Hour), now.Add(2*time.Hour)))
	require.NoError(t, err)
	require.NoError(t, svc.DeleteCampaign(ctx, scheduled.ID))
	_, err = client.RechargeBonusCampaign.Get(ctx, scheduled.ID)
	require.True(t, dbent.IsNotFound(err))

	active, err := svc.CreateCampaign(ctx, validRechargeBonusCampaignInput(now.Add(-time.Hour), now.Add(time.Hour)))
	require.NoError(t, err)
	require.Error(t, svc.DeleteCampaign(ctx, active.ID))
}

func TestRechargeBonusOverlapConstraintErrorDetection(t *testing.T) {
	err := errors.New("pq: conflicting key value violates exclusion constraint recharge_bonus_campaigns_no_overlap")

	require.True(t, isRechargeBonusOverlapConstraintError(err))
	require.False(t, isRechargeBonusOverlapConstraintError(errors.New("other database error")))
}
