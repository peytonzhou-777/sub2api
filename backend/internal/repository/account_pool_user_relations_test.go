package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestListAccountPoolUserRelationsUsesSuccessfulTouchFacts(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	mock.ExpectQuery(`(?s)SELECT account_id,.*BOOL_OR\(is_current_residence\).*FROM \(.*user_account_placements.*expires_at > NOW\(\).*UNION ALL.*account_user_contacts.*last_touched_at IS NOT NULL`).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{
			"account_id", "is_current_residence", "is_seven_day_contact", "is_historical_contact",
		}).AddRow(11, true, false, false).AddRow(12, false, true, true))

	relations, err := repo.ListAccountPoolUserRelations(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, relations, 2)
	require.True(t, relations[0].IsCurrentResidence)
	require.False(t, relations[0].IsHistoricalContact)
	require.True(t, relations[1].IsSevenDayContact)
	require.True(t, relations[1].IsHistoricalContact)
	require.NoError(t, mock.ExpectationsWereMet())
}
