package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestGetAccountRequestWindowStatsScansFixedWindows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &opsRepository{db: db}
	accountID := int64(12)

	columns := []string{
		"account_id", "window_label", "window_minutes", "request_count", "peak_concurrency",
		"distinct_session_scopes", "distinct_session_sources", "distinct_prompt_cache_keys",
		"overload_count", "http_429_count", "http_5xx_count", "overload_error_rate",
		"http_429_error_rate", "http_5xx_error_rate",
	}
	rows := sqlmock.NewRows(columns).
		AddRow(accountID, "1m", 1, 10, 4, 3, 2, 2, 1, 1, 1, 0.1, 0.1, 0.1).
		AddRow(accountID, "5m", 5, 30, 7, 5, 4, 3, 2, 1, 2, 2.0/30.0, 1.0/30.0, 2.0/30.0).
		AddRow(accountID, "30m", 30, 100, 9, 8, 7, 6, 3, 2, 4, 0.03, 0.02, 0.04)
	mock.ExpectQuery("WITH params AS.*e.status_code >= 400 AS failed.*account_windows AS").
		WithArgs(sqlmock.AnyArg(), accountID).
		WillReturnRows(rows)

	stats, err := repo.GetAccountRequestWindowStats(context.Background(), &accountID)
	require.NoError(t, err)
	require.Len(t, stats, 3)
	require.Equal(t, "1m", stats[0].Window)
	require.Equal(t, int64(4), stats[0].PeakConcurrency)
	require.InDelta(t, 0.1, stats[0].OverloadErrorRate, 0.0001)
	require.Equal(t, int64(100), stats[2].RequestCount)
	require.NoError(t, mock.ExpectationsWereMet())
}
