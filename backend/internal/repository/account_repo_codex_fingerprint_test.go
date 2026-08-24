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
		}).AddRow(seed, "v3", int64(1), now))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	state, err := repo.GetOrInitializeCodexFingerprintState(context.Background(), 27, now)

	require.NoError(t, err)
	require.Equal(t, seed, state.Seed)
	require.Equal(t, "v3", state.Version)
	require.Equal(t, int64(1), state.Epoch)
	require.Equal(t, now, state.EpochStartedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetOrInitializeCodexFingerprintStateAcceptsV3(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	seed := strings.Repeat("ab", 32)
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE accounts")).
		WithArgs(int64(27), sqlmock.AnyArg(), now).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
		}).AddRow(seed, "v3", int64(4), now))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	state, err := repo.GetOrInitializeCodexFingerprintState(context.Background(), 27, now)

	require.NoError(t, err)
	require.Equal(t, "v3", state.Version)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetOrInitializeCodexFingerprintStateRejectsV2(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	seed := strings.Repeat("ab", 32)
	mock.ExpectQuery(regexp.QuoteMeta("UPDATE accounts")).
		WithArgs(int64(27), sqlmock.AnyArg(), now).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
		}).AddRow(seed, "v2", int64(4), now))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	state, err := repo.GetOrInitializeCodexFingerprintState(context.Background(), 27, now)

	require.Nil(t, state)
	require.ErrorContains(t, err, "invalid codex fingerprint state")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateCodexFingerprintSecretRejectsClusterMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	secretHash := strings.Repeat("ab", 32)
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO codex_fingerprint_cluster_secrets")).
		WithArgs(secretHash, now).
		WillReturnRows(sqlmock.NewRows([]string{"secret_hash"}).AddRow(secretHash))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO codex_fingerprint_cluster_secrets")).
		WithArgs(secretHash, now).
		WillReturnRows(sqlmock.NewRows([]string{"secret_hash"}))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	require.NoError(t, repo.ValidateCodexFingerprintSecret(context.Background(), secretHash, now))
	require.ErrorContains(t, repo.ValidateCodexFingerprintSecret(context.Background(), secretHash, now), "does not match")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRotateCodexFingerprintSessionsOnlyUpdatesFingerprintState(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)UPDATE accounts.*codex_fingerprint_version = 'v3'`).
		WithArgs(int64(27), now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(27)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE codex_fingerprint_session_scopes")).
		WithArgs(int64(27), now).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	require.NoError(t, repo.RotateCodexFingerprintSessions(context.Background(), 27, now))
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
	scopeHash := strings.Repeat("ef", 32)
	mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
		WithArgs(int64(27), threadHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
			"session_epoch",
			"session_epoch_started_at",
			"last_seen_at",
			"session_scope_hash",
		}))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
		WithArgs(int64(27), threadHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
			"session_epoch",
			"session_epoch_started_at",
			"last_seen_at",
			"session_scope_hash",
		}))
	mock.ExpectQuery(`(?s)SELECT codex_fingerprint_seed.*FROM accounts`).
		WithArgs(int64(27)).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
		}).AddRow(seed, "v3", int64(3), startedAt))
	mock.ExpectExec(`(?s)INSERT INTO codex_fingerprint_session_scopes`).
		WithArgs(int64(27), scopeHash, int64(3), now, 1, 0, 1).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`(?s)SELECT session_epoch, epoch_started_at, last_active_at.*FOR UPDATE`).
		WithArgs(int64(27), scopeHash).
		WillReturnRows(sqlmock.NewRows([]string{"session_epoch", "epoch_started_at", "last_active_at"}).AddRow(int64(3), startedAt, lastUsedAt))
	mock.ExpectQuery(`(?s)UPDATE codex_fingerprint_session_scopes.*session_epoch = session_epoch \+ 1`).
		WithArgs(int64(27), scopeHash, now).
		WillReturnRows(sqlmock.NewRows([]string{"session_epoch", "epoch_started_at"}).AddRow(int64(4), now))
	mock.ExpectQuery(`(?s)INSERT INTO codex_fingerprint_thread_epochs`).
		WithArgs(int64(27), threadHash, int64(4), now, now, scopeHash).
		WillReturnRows(sqlmock.NewRows([]string{"session_epoch", "session_epoch_started_at", "session_scope_hash"}).AddRow(int64(4), now, scopeHash))
	mock.ExpectExec(`(?s)UPDATE codex_fingerprint_session_scopes.*last_active_at`).
		WithArgs(int64(27), scopeHash, now, int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)DELETE FROM codex_fingerprint_thread_epochs`).
		WithArgs(int64(27), scopeHash, int64(4), cutoff).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	state, err := repo.ResolveCodexFingerprintSessionState(
		context.Background(), service.CodexFingerprintSessionRequest{
			AccountID: 27, SessionScopeHash: scopeHash, ThreadSourceHashes: []string{threadHash},
			Now: now, RotationAllowed: true, MinAgeBefore: now.Add(-72 * time.Hour),
			IdleBefore: idleBefore, MaxAgeBefore: now.Add(-7 * 24 * time.Hour), OldEpochCutoff: cutoff,
		},
	)

	require.NoError(t, err)
	require.Equal(t, int64(4), state.State.Epoch)
	require.Equal(t, int64(4), state.BoundEpoch)
	require.Equal(t, now, state.BoundEpochStartedAt)
	require.Equal(t, scopeHash, state.BoundSessionScopeHash)
	require.Equal(t, threadHash, state.MatchedThreadSourceHash)
	require.True(t, state.Rotated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCodexFingerprintSessionStateUsesBoundThreadFastPath(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)
	startedAt := now.Add(-24 * time.Hour)
	boundStartedAt := now.Add(-7 * 24 * time.Hour)
	lastSeenAt := now.Add(-time.Minute)
	seed := strings.Repeat("ab", 32)
	threadHash := strings.Repeat("cd", 32)
	scopeHash := strings.Repeat("ef", 32)
	mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
		WithArgs(int64(27), threadHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"codex_fingerprint_seed",
			"codex_fingerprint_version",
			"codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at",
			"session_epoch",
			"session_epoch_started_at",
			"last_seen_at",
			"session_scope_hash",
		}).AddRow(seed, "v3", int64(5), startedAt, int64(3), boundStartedAt, lastSeenAt, scopeHash))

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	resolved, err := repo.ResolveCodexFingerprintSessionState(
		context.Background(), service.CodexFingerprintSessionRequest{
			AccountID: 27, SessionScopeHash: scopeHash, ThreadSourceHashes: []string{threadHash}, Now: now,
			RotationAllowed: true, MinAgeBefore: now.Add(-72 * time.Hour), IdleBefore: now.Add(-2 * time.Hour),
			MaxAgeBefore: now.Add(-7 * 24 * time.Hour), OldEpochCutoff: now.Add(-48 * time.Hour),
		},
	)

	require.NoError(t, err)
	require.Equal(t, int64(5), resolved.State.Epoch)
	require.Equal(t, "v3", resolved.State.Version)
	require.Equal(t, int64(3), resolved.BoundEpoch)
	require.Equal(t, boundStartedAt, resolved.BoundEpochStartedAt)
	require.Equal(t, scopeHash, resolved.BoundSessionScopeHash)
	require.False(t, resolved.Rotated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCodexFingerprintSessionStateBindsChildToParentEpoch(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	startedAt := now.Add(-24 * time.Hour)
	boundStartedAt := now.Add(-7 * 24 * time.Hour)
	lastSeenAt := now.Add(-time.Minute)
	seed := strings.Repeat("ab", 32)
	childHash := strings.Repeat("cd", 32)
	parentHash := strings.Repeat("de", 32)
	scopeHash := strings.Repeat("ef", 32)
	stateRows := func(withRow bool) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{
			"codex_fingerprint_seed", "codex_fingerprint_version", "codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at", "session_epoch", "session_epoch_started_at", "last_seen_at", "session_scope_hash",
		})
		if withRow {
			rows.AddRow(seed, "v3", int64(5), startedAt, int64(3), boundStartedAt, lastSeenAt, scopeHash)
		}
		return rows
	}
	for _, withRow := range []bool{false, true} {
		hash := childHash
		if withRow {
			hash = parentHash
		}
		mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
			WithArgs(int64(27), hash).
			WillReturnRows(stateRows(withRow))
	}
	mock.ExpectBegin()
	for _, withRow := range []bool{false, true} {
		hash := childHash
		if withRow {
			hash = parentHash
		}
		mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
			WithArgs(int64(27), hash).
			WillReturnRows(stateRows(withRow))
	}
	mock.ExpectQuery(`(?s)INSERT INTO codex_fingerprint_thread_epochs`).
		WithArgs(int64(27), childHash, int64(3), boundStartedAt, now, scopeHash).
		WillReturnRows(sqlmock.NewRows([]string{"session_epoch", "session_epoch_started_at", "session_scope_hash"}).AddRow(int64(3), boundStartedAt, scopeHash))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	resolved, err := repo.ResolveCodexFingerprintSessionState(context.Background(), service.CodexFingerprintSessionRequest{
		AccountID: 27, SessionScopeHash: scopeHash,
		ThreadSourceHashes: []string{childHash, parentHash}, BindSourceHashes: []string{childHash},
		Now: now, RotationAllowed: true, MinAgeBefore: now.Add(-72 * time.Hour),
		IdleBefore: now.Add(-2 * time.Hour), MaxAgeBefore: now.Add(-7 * 24 * time.Hour), OldEpochCutoff: now.Add(-48 * time.Hour),
	})

	require.NoError(t, err)
	require.Equal(t, int64(3), resolved.BoundEpoch)
	require.Equal(t, boundStartedAt, resolved.BoundEpochStartedAt)
	require.Equal(t, childHash, resolved.MatchedThreadSourceHash)
	require.Equal(t, scopeHash, resolved.BoundSessionScopeHash)
	require.True(t, resolved.Created)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolveCodexFingerprintSessionStateUsesConcurrentChildBinding(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	startedAt := now.Add(-24 * time.Hour)
	boundStartedAt := now.Add(-7 * 24 * time.Hour)
	lastSeenAt := now.Add(-time.Minute)
	seed := strings.Repeat("ab", 32)
	childHash := strings.Repeat("cd", 32)
	parentHash := strings.Repeat("de", 32)
	parentScopeHash := strings.Repeat("ef", 32)
	winnerScopeHash := strings.Repeat("12", 32)
	stateRows := func(epoch int64, scopeHash string) *sqlmock.Rows {
		rows := sqlmock.NewRows([]string{
			"codex_fingerprint_seed", "codex_fingerprint_version", "codex_fingerprint_epoch",
			"codex_fingerprint_epoch_started_at", "session_epoch", "session_epoch_started_at", "last_seen_at", "session_scope_hash",
		})
		if epoch > 0 {
			rows.AddRow(seed, "v3", int64(9), startedAt, epoch, boundStartedAt, lastSeenAt, scopeHash)
		}
		return rows
	}
	for _, candidate := range []struct {
		hash  string
		epoch int64
		scope string
	}{{childHash, 0, ""}, {parentHash, 3, parentScopeHash}} {
		mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
			WithArgs(int64(27), candidate.hash).
			WillReturnRows(stateRows(candidate.epoch, candidate.scope))
	}
	mock.ExpectBegin()
	for _, candidate := range []struct {
		hash  string
		epoch int64
		scope string
	}{{childHash, 0, ""}, {parentHash, 3, parentScopeHash}} {
		mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
			WithArgs(int64(27), candidate.hash).
			WillReturnRows(stateRows(candidate.epoch, candidate.scope))
	}
	mock.ExpectQuery(`(?s)INSERT INTO codex_fingerprint_thread_epochs`).
		WithArgs(int64(27), childHash, int64(3), boundStartedAt, now, parentScopeHash).
		WillReturnRows(sqlmock.NewRows([]string{"session_epoch", "session_epoch_started_at", "session_scope_hash"}))
	mock.ExpectQuery(`(?s)SELECT a.codex_fingerprint_seed.*FROM codex_fingerprint_thread_epochs t`).
		WithArgs(int64(27), childHash).
		WillReturnRows(stateRows(7, winnerScopeHash))
	mock.ExpectCommit()

	repo := newAccountRepositoryWithSQL(nil, db, nil)
	resolved, err := repo.ResolveCodexFingerprintSessionState(context.Background(), service.CodexFingerprintSessionRequest{
		AccountID: 27, SessionScopeHash: parentScopeHash,
		ThreadSourceHashes: []string{childHash, parentHash}, BindSourceHashes: []string{childHash},
		Now: now, RotationAllowed: true, MinAgeBefore: now.Add(-72 * time.Hour),
		IdleBefore: now.Add(-2 * time.Hour), MaxAgeBefore: now.Add(-7 * 24 * time.Hour), OldEpochCutoff: now.Add(-48 * time.Hour),
	})

	require.NoError(t, err)
	require.Equal(t, int64(7), resolved.BoundEpoch)
	require.Equal(t, boundStartedAt, resolved.BoundEpochStartedAt)
	require.Equal(t, childHash, resolved.MatchedThreadSourceHash)
	require.Equal(t, winnerScopeHash, resolved.BoundSessionScopeHash)
	require.False(t, resolved.Created)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestShouldRotateCodexFingerprintScopeUsesIdleAndMaxAgeGates(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	request := service.CodexFingerprintSessionRequest{
		RotationAllowed: true,
		MinAgeBefore:    now.Add(-72 * time.Hour),
		IdleBefore:      now.Add(-2 * time.Hour),
		MaxAgeBefore:    now.Add(-7 * 24 * time.Hour),
	}

	rotated, reason := shouldRotateCodexFingerprintScope(
		request,
		service.CodexFingerprintState{EpochStartedAt: now.Add(-80 * time.Hour)},
		now.Add(-3*time.Hour),
	)
	require.True(t, rotated, "达到最短年龄且空闲时应轮换")
	require.Equal(t, "idle_after_min_age", reason)
	rotated, _ = shouldRotateCodexFingerprintScope(
		request,
		service.CodexFingerprintState{EpochStartedAt: now.Add(-80 * time.Hour)},
		now.Add(-time.Hour),
	)
	require.False(t, rotated, "未达到最长年龄时不得跳过空闲门")
	rotated, reason = shouldRotateCodexFingerprintScope(
		request,
		service.CodexFingerprintState{EpochStartedAt: now.Add(-8 * 24 * time.Hour)},
		now.Add(-time.Hour),
	)
	require.True(t, rotated, "达到最长年龄后应允许新 Thread 兜底轮换")
	require.Equal(t, "max_age", reason)
	request.RotationAllowed = false
	rotated, reason = shouldRotateCodexFingerprintScope(
		request,
		service.CodexFingerprintState{EpochStartedAt: now.Add(-8 * 24 * time.Hour)},
		now.Add(-3*time.Hour),
	)
	require.True(t, rotated, "达到最长寿命后不得被活跃 WS 阻止新根切换")
	require.Equal(t, "max_age", reason)
}
