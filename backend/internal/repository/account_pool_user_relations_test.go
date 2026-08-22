package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestListAccountPoolUserRelationsUsesSuccessfulTouchFacts(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	mock.ExpectQuery(`(?s)WITH affinity_config AS.*ranked_slots AS.*FROM openai_user_resident_slots.*status = 'active'.*SELECT account_id,.*BOOL_OR\(is_current_residence\).*BOOL_OR\(is_primary_residence\).*UNION ALL.*account_user_contacts.*last_touched_at IS NOT NULL`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{
			"account_id", "is_current_residence", "is_primary_residence", "is_seven_day_contact", "is_historical_contact",
		}).AddRow(11, true, true, false, false).AddRow(12, false, false, true, true))

	relations, err := repo.ListAccountPoolUserRelations(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, relations, 2)
	require.True(t, relations[0].IsCurrentResidence)
	require.True(t, relations[0].IsPrimaryResidence)
	require.False(t, relations[0].IsHistoricalContact)
	require.True(t, relations[1].IsSevenDayContact)
	require.True(t, relations[1].IsHistoricalContact)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListAccountPoolResidentStatsCountsDistinctActiveResidents(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	activeSince := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`(?s)WITH requested AS.*resident_stats AS.*FROM openai_user_resident_slots.*conversation_stats AS.*openai_user_conversation_bindings.*contact_stats AS.*account_user_contacts.*SELECT requested.account_id`).
		WithArgs("{11,12}", activeSince).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "active", "total", "draining_slots", "active_conversations", "contacted_users"}).
			AddRow(11, int64(3), int64(8), int64(1), int64(4), int64(9)).
			AddRow(12, int64(0), int64(2), int64(0), int64(1), int64(2)))

	stats, err := repo.ListAccountPoolResidentStats(context.Background(), []int64{11, 12}, activeSince)
	require.NoError(t, err)
	require.Equal(t, int64(3), stats[11].Active)
	require.Equal(t, int64(8), stats[11].Total)
	require.Equal(t, int64(1), stats[11].DrainingSlots)
	require.Equal(t, int64(4), stats[11].ActiveConversations)
	require.Equal(t, int64(9), stats[11].ContactedUsers)
	require.Equal(t, int64(0), stats[12].Active)
	require.Equal(t, int64(2), stats[12].Total)
	require.NoError(t, mock.ExpectationsWereMet())
}
