package repository

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGetOrInitializeCodexFingerprintStateAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	seed := strings.Repeat("ab", 32)
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE accounts")).
		WithArgs(int64(27), sqlmock.AnyArg(), now).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
		}).AddRow(seed, "v2", int64(1), now))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	state, err := repo.GetOrInitializeCodexFingerprintState(context.Background(), 27, now)

	require.NoError(t, err)
	require.Equal(t, seed, state.Seed)
	require.Equal(t, "v2", state.Version)
	require.Equal(t, int64(1), state.Epoch)
	require.Equal(t, now, state.EpochStartedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetOrInitializeCodexFingerprintStateRejectsIneligibleOrPartialState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE accounts")).
		WithArgs(int64(27), sqlmock.AnyArg(), now).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
		}))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	state, err := repo.GetOrInitializeCodexFingerprintState(context.Background(), 27, now)

	require.Nil(t, state)
	require.ErrorIs(t, err, service.ErrAccountNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCodexFingerprintSessionStateReturnsBoundEpoch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	cutoff := now.Add(-48 * time.Hour)
	startedAt := now.Add(-15 * 24 * time.Hour)
	lastUsedAt := now.Add(-3 * time.Hour)
	idleBefore := now.Add(-2 * time.Hour)
	seed := strings.Repeat("ab", 32)
	threadHash := strings.Repeat("cd", 32)
	mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
		WithArgs(int64(27), threadHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
			"session_epoch",
			"last_seen_at",
		}))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT codex_fingerprint_seed.*FROM accounts.*FOR UPDATE`).
		WithArgs(int64(27)).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
			"last_used_at",
		}).AddRow(seed, "v2", int64(3), startedAt, lastUsedAt))
	mock.ExpectQuery(`(?s)SELECT session_epoch.*codex_fingerprint_thread_epochs`).
		WithArgs(int64(27), threadHash).
		WillReturnRows(sqlmock.NewRows([]string{"session_epoch"}))
	mock.ExpectQuery(`(?s)UPDATE accounts.*codex_fingerprint_epoch = codex_fingerprint_epoch \+ 1`).
		WithArgs(int64(27), now).
		WillReturnRows(sqlmock.NewRows([]string{"codex_fingerprint_epoch", "codex_fingerprint_epoch_started_at"}).AddRow(int64(4), now))
	mock.ExpectQuery(`(?s)INSERT INTO codex_fingerprint_thread_epochs`).
		WithArgs(int64(27), threadHash, int64(4), now).
		WillReturnRows(sqlmock.NewRows([]string{"session_epoch"}).AddRow(int64(4)))
	mock.ExpectExec(`(?s)DELETE FROM codex_fingerprint_thread_epochs`).
		WithArgs(int64(27), int64(4), cutoff).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	state, err := repo.ResolveCodexFingerprintSessionState(
		context.Background(), 27, threadHash, now, true, startedAt, idleBefore, cutoff,
	)

	require.NoError(t, err)
	require.Equal(t, int64(4), state.State.Epoch)
	require.Equal(t, int64(4), state.BoundEpoch)
	require.True(t, state.Rotated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCodexFingerprintSessionStateUsesBoundThreadFastPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	startedAt := now.Add(-24 * time.Hour)
	lastSeenAt := now.Add(-time.Minute)
	seed := strings.Repeat("ab", 32)
	threadHash := strings.Repeat("cd", 32)
	mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
		WithArgs(int64(27), threadHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
			"session_epoch",
			"last_seen_at",
		}).AddRow(seed, "v2", int64(5), startedAt, int64(3), lastSeenAt))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	resolved, err := repo.ResolveCodexFingerprintSessionState(
		context.Background(), 27, threadHash, now, true, startedAt, now.Add(-2*time.Hour), now.Add(-48*time.Hour),
	)

	require.NoError(t, err)
	require.Equal(t, int64(5), resolved.State.Epoch)
	require.Equal(t, int64(3), resolved.BoundEpoch)
	require.False(t, resolved.Rotated)
	require.NoError(t, mock.ExpectationsWereMet())
}
