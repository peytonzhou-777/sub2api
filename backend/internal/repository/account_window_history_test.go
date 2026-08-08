package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAppendCodex7DWindowHistoryDerivesStartAndUpserts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	resetAt := time.Date(2026, 8, 8, 12, 34, 56, 0, time.UTC)
	observedAt := time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)
	wantStart := resetAt.Add(-7 * 24 * time.Hour).Truncate(time.Minute)
	mock.ExpectExec("INSERT INTO account_usage_window_histories").
		WithArgs(int64(42), wantStart, observedAt).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = appendCodex7DWindowHistory(context.Background(), db, 42, map[string]any{
		"codex_7d_reset_at":       resetAt.Format(time.RFC3339),
		"codex_7d_window_minutes": float64(7 * 24 * 60),
		"codex_usage_updated_at":  observedAt.Format(time.RFC3339),
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAppendCodex7DWindowHistoryIgnoresInvalidSnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	require.NoError(t, appendCodex7DWindowHistory(context.Background(), db, 42, map[string]any{
		"codex_7d_reset_at": "invalid",
	}))
	require.NoError(t, mock.ExpectationsWereMet())
}
