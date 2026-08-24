package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

func newOpenAIUserAffinityRepositoryTest(t *testing.T) (*accountRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	return newAccountRepositoryWithSQL(client, db, nil), mock
}

func TestListOpenAIUserResidentSlotsReadsAuthoritativeProjection(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	now := time.Now().UTC()
	mock.ExpectQuery(`(?s)SELECT id, user_id, scope_key, slot_index, account_id, generation, status.*FROM openai_user_resident_slots.*ORDER BY s\.usage_score DESC`).
		WithArgs(int64(42), "openai").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "scope_key", "slot_index", "account_id", "generation", "status",
			"admitted_at", "last_success_at", "expires_at", "usage_score", "score_updated_at",
			"replacement_source_slot_id", "provisional_token", "config_version",
		}).AddRow(7, 42, "openai", 1, 11, 3, service.OpenAIUserResidentSlotStatusActive,
			now.Add(-time.Hour), now, now.Add(time.Hour), 2.5, now, nil, nil, 8))
	mock.ExpectQuery(`(?s)WITH claims AS.*FROM deduplicated GROUP BY account_id`).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "active_user_count", "owner_user_id"}).
			AddRow(11, 2, 42))

	slots, err := repo.ListOpenAIUserResidentSlots(context.Background(), 42, "")
	require.NoError(t, err)
	require.Len(t, slots, 1)
	require.Equal(t, int64(11), slots[0].AccountID)
	require.NotNil(t, slots[0].LastSuccessAt)
	require.Equal(t, 2.5, slots[0].UsageScore)
	require.Equal(t, 2, slots[0].ActiveRouteUserCount)
	require.Equal(t, int64(42), slots[0].SoftOwnerUserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetOpenAIUserConversationBindingByAliasScopesLookup(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	now := time.Now().UTC()
	hash := strings.Repeat("a", 64)
	mock.ExpectQuery(`(?s)FROM openai_user_conversation_aliases a.*JOIN openai_user_conversation_bindings b`).
		WithArgs(int64(42), int64(9), "openai", "response_id", hash).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "api_key_id", "scope_key", "conversation_hash", "resident_slot_id",
			"account_id", "slot_generation", "status", "context_rebuildable", "first_output_committed",
			"active_until", "expires_at", "last_success_at", "provisional_token",
		}).AddRow(5, 42, 9, "openai", hash, 7, 11, 3, "active", true, true,
			now.Add(time.Hour), now.Add(7*24*time.Hour), now, nil))

	binding, err := repo.GetOpenAIUserConversationBindingByAlias(
		context.Background(), 42, 9, "", " RESPONSE_ID ", strings.ToUpper(hash),
	)
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.Equal(t, int64(11), binding.AccountID)
	require.True(t, binding.FirstOutputCommitted)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestNormalizeOpenAIUserConversationReservationAliasesSupportsCodexThreadScope(t *testing.T) {
	responseHash := strings.Repeat("a", 64)
	threadHash := strings.Repeat("b", 64)
	aliases, err := normalizeOpenAIUserConversationReservationAliases(service.OpenAIUserConversationReservation{
		ScopeKey:  "openai:v1:group:1:lane:general",
		AliasType: " RESPONSE_ID ", AliasHash: strings.ToUpper(responseHash),
		Aliases: []service.OpenAIUserConversationAlias{
			{ScopeKey: "openai:v1:group:1:lineage:codex-thread", Type: "CODEX_THREAD", Hash: strings.ToUpper(threadHash)},
			{ScopeKey: "openai:v1:group:1:lineage:codex-thread", Type: "codex_thread", Hash: threadHash},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []service.OpenAIUserConversationAlias{
		{ScopeKey: "openai:v1:group:1:lane:general", Type: "response_id", Hash: responseHash},
		{ScopeKey: "openai:v1:group:1:lineage:codex-thread", Type: "codex_thread", Hash: threadHash},
	}, aliases)
}

func TestNormalizeOpenAIUserConversationReservationAliasesRejectsUnknownType(t *testing.T) {
	_, err := normalizeOpenAIUserConversationReservationAliases(service.OpenAIUserConversationReservation{
		Aliases: []service.OpenAIUserConversationAlias{{Type: "raw_thread", Hash: strings.Repeat("c", 64)}},
	})
	require.ErrorContains(t, err, "invalid openai conversation alias")
}

func TestGetOpenAIUserAffinityCandidateStatsMarksAlreadyActiveUser(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	mock.ExpectQuery(`(?s)SELECT a.id, a.max_contact_users.*BOOL_OR\(c.user_id = \$3.*WHERE a.id IN \(\$1, \$2\)`).
		WithArgs(int64(11), int64(12), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "max_contact_users", "new_resident_cooldown_seconds",
			"new_resident_cooldown_until", "active_contact_users", "user_already_active", "user_already_resident",
		}).AddRow(11, 10, 300, nil, 10, true, true).AddRow(12, 10, 300, nil, 4, false, false))

	stats, err := repo.GetOpenAIUserAffinityCandidateStats(context.Background(), 42, []int64{11, 12})
	require.NoError(t, err)
	require.True(t, stats[11].UserAlreadyActive)
	require.True(t, stats[11].UserAlreadyResident)
	require.False(t, stats[12].UserAlreadyActive)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAssignOpenAIUserAffinityPlacementAllowsAlreadyCountedUserAtCapacity(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	now := time.Now().UTC()
	accountID := int64(11)
	placement := service.OpenAIUserPlacement{
		UserID: 42, ScopeKey: "openai:v1:group:1:lane:general", AccountID: &accountID,
		Generation: 1, ExpiresAt: now.Add(14 * 24 * time.Hour), AssignmentReason: "new_resident",
		ProvisionalToken: "assignment-token",
	}
	config := service.DefaultOpenAIUserAffinityConfig()

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT max_contact_users.*FROM accounts.*FOR UPDATE`).
		WithArgs(accountID, placement.UserID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"max_contact_users", "new_resident_cooldown_seconds", "new_resident_cooldown_until", "status", "schedulable", "platform", "user_already_resident"}).AddRow(10, 300, nil, service.StatusActive, true, service.PlatformOpenAI, false))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\), COALESCE\(BOOL_OR\(user_id = \$3\), FALSE\).*account_user_contacts`).
		WithArgs(accountID, sqlmock.AnyArg(), placement.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"count", "user_already_active"}).AddRow(10, true))
	mock.ExpectQuery(`(?s)SELECT account_id, status, expires_at FROM user_account_placements.*FOR UPDATE`).
		WithArgs(placement.UserID, placement.ScopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "status", "expires_at"}))
	mock.ExpectExec(`(?s)INSERT INTO user_account_placements`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)INSERT INTO account_user_contacts`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)INSERT INTO user_account_placement_events`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	assigned, err := repo.AssignOpenAIUserAffinityPlacement(context.Background(), placement, config)
	require.NoError(t, err)
	require.True(t, assigned)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMigrateOpenAIUserAffinityPlacementUsesStableLockOrderAndAllowsExistingContact(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	config := service.DefaultOpenAIUserAffinityConfig()
	scopeKey := "openai:v1:group:1:lane:general"

	mock.ExpectQuery(`(?s)WITH per_user AS.*percentile_cont\(\$2\).*FROM population`).
		WithArgs(int64(42), config.ColdStartDemandQuantile).
		WillReturnRows(sqlmock.NewRows([]string{"tokens_5h", "tokens_7d", "p5h", "p7d"}).AddRow(100, 200, 1000, 2000))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id FROM accounts WHERE id IN \(\$1, \$2\) ORDER BY id FOR UPDATE`).
		WithArgs(int64(11), int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11).AddRow(12))
	mock.ExpectQuery(`(?s)SELECT account_id, generation, status, provisional_token FROM user_account_placements.*FOR UPDATE`).
		WithArgs(int64(42), scopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "generation", "status", "provisional_token"}).AddRow(11, 3, "active", nil))
	mock.ExpectQuery(`(?s)SELECT max_contact_users.*FROM accounts WHERE id = \$1`).
		WithArgs(int64(12), int64(42), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"max_contact_users", "new_resident_cooldown_seconds", "new_resident_cooldown_until", "status", "schedulable", "platform", "user_already_resident"}).AddRow(10, 300, nil, service.StatusActive, true, service.PlatformOpenAI, false))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\), COALESCE\(BOOL_OR\(user_id = \$3\), FALSE\).*account_user_contacts`).
		WithArgs(int64(12), sqlmock.AnyArg(), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"count", "user_already_active"}).AddRow(10, true))
	mock.ExpectExec(`(?s)UPDATE user_account_placements SET account_id`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO account_user_contacts`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)UPDATE user_account_capacity_incidents SET migration_target_account_id`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO user_account_placement_events`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	migrated, err := repo.MigrateOpenAIUserAffinityPlacement(
		context.Background(), 42, 11, 12, 3, scopeKey, "provisional-token", "capacity_retry_threshold", config,
	)
	require.NoError(t, err)
	require.True(t, migrated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMigrateOpenAIUserAffinityPlacementRejectsDisabledTargetAccount(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	config := service.DefaultOpenAIUserAffinityConfig()
	scopeKey := "openai:v1:group:1:lane:general"

	mock.ExpectQuery(`(?s)WITH per_user AS.*percentile_cont\(\$2\).*FROM population`).
		WithArgs(int64(42), config.ColdStartDemandQuantile).
		WillReturnRows(sqlmock.NewRows([]string{"tokens_5h", "tokens_7d", "p5h", "p7d"}).AddRow(100, 200, 1000, 2000))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id FROM accounts WHERE id IN \(\$1, \$2\) ORDER BY id FOR UPDATE`).
		WithArgs(int64(11), int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11).AddRow(12))
	mock.ExpectQuery(`(?s)SELECT account_id, generation, status, provisional_token FROM user_account_placements.*FOR UPDATE`).
		WithArgs(int64(42), scopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "generation", "status", "provisional_token"}).AddRow(11, 3, "active", nil))
	mock.ExpectQuery(`(?s)SELECT max_contact_users.*FROM accounts WHERE id = \$1`).
		WithArgs(int64(12), int64(42), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"max_contact_users", "new_resident_cooldown_seconds", "new_resident_cooldown_until", "status", "schedulable", "platform", "user_already_resident"}).
			AddRow(10, 300, nil, service.StatusDisabled, false, service.PlatformOpenAI, false))
	mock.ExpectRollback()

	migrated, err := repo.MigrateOpenAIUserAffinityPlacement(
		context.Background(), 42, 11, 12, 3, scopeKey, "provisional-token", "capacity_retry_threshold", config,
	)
	require.NoError(t, err)
	require.False(t, migrated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMigrateOpenAIUserAffinityPlacementRejectsProvisionalSourcePlacement(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	config := service.DefaultOpenAIUserAffinityConfig()
	scopeKey := "openai:v1:group:1:lane:general"

	mock.ExpectQuery(`(?s)WITH per_user AS.*percentile_cont\(\$2\).*FROM population`).
		WithArgs(int64(42), config.ColdStartDemandQuantile).
		WillReturnRows(sqlmock.NewRows([]string{"tokens_5h", "tokens_7d", "p5h", "p7d"}).AddRow(100, 200, 1000, 2000))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id FROM accounts WHERE id IN \(\$1, \$2\) ORDER BY id FOR UPDATE`).
		WithArgs(int64(11), int64(12)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11).AddRow(12))
	mock.ExpectQuery(`(?s)SELECT account_id, generation, status, provisional_token FROM user_account_placements.*FOR UPDATE`).
		WithArgs(int64(42), scopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "generation", "status", "provisional_token"}).AddRow(11, 3, "active", "pending-request"))
	mock.ExpectRollback()

	migrated, err := repo.MigrateOpenAIUserAffinityPlacement(
		context.Background(), 42, 11, 12, 3, scopeKey, "migration-token", "capacity_retry_threshold", config,
	)
	require.NoError(t, err)
	require.False(t, migrated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAssignOpenAIUserAffinityPlacementRejectsDisabledAuthoritativeAccount(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	accountID := int64(11)
	placement := service.OpenAIUserPlacement{
		UserID: 42, ScopeKey: "openai:v1:group:1:lane:general", AccountID: &accountID,
		Generation: 1, ExpiresAt: time.Now().UTC().Add(14 * 24 * time.Hour),
		ProvisionalToken: "assignment-token",
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT max_contact_users.*FROM accounts.*FOR UPDATE`).
		WithArgs(accountID, placement.UserID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"max_contact_users", "new_resident_cooldown_seconds", "new_resident_cooldown_until", "status", "schedulable", "platform", "user_already_resident"}).
			AddRow(10, 300, nil, service.StatusDisabled, false, service.PlatformOpenAI, false))
	mock.ExpectRollback()

	assigned, err := repo.AssignOpenAIUserAffinityPlacement(context.Background(), placement, service.DefaultOpenAIUserAffinityConfig())
	require.NoError(t, err)
	require.False(t, assigned)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRollbackOpenAIUserAffinityPlacementUsesProvisionalTokenCAS(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	accountID := int64(11)
	transition := service.OpenAIUserAffinityProvisionalTransition{
		Kind:  "assignment",
		Token: "request-token",
		TargetPlacement: service.OpenAIUserPlacement{
			UserID: 42, ScopeKey: "openai:v1:group:1:lane:general", AccountID: &accountID, Generation: 4,
		},
	}

	mock.ExpectBegin()
	mock.ExpectExec(`(?s)UPDATE user_account_placements SET status = 'expired'.*provisional_token = \$6`).
		WithArgs(int64(42), transition.TargetPlacement.ScopeKey, &accountID, sqlmock.AnyArg(), int64(4), transition.Token).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT reservation_kind FROM account_user_contacts.*FOR UPDATE`).
		WithArgs(accountID, int64(42), transition.Token).
		WillReturnRows(sqlmock.NewRows([]string{"reservation_kind"}).AddRow("new_resident"))
	mock.ExpectExec(`(?s)UPDATE account_user_contacts SET reservation_kind = NULL`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE accounts SET new_resident_cooldown_until = NULL`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO user_account_placement_events`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	rolledBack, err := repo.RollbackOpenAIUserAffinityPlacement(context.Background(), transition, service.DefaultOpenAIUserAffinityConfig())
	require.NoError(t, err)
	require.True(t, rolledBack)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTouchAndConfirmOpenAIUserAffinitySeparateAcceptedFromRecovery(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	config := service.DefaultOpenAIUserAffinityConfig()
	scopeKey := "openai:v1:group:1:lane:general"
	future := time.Now().UTC().Add(time.Hour)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT account_id, generation, status FROM user_account_placements.*FOR UPDATE`).
		WithArgs(int64(42), scopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "generation", "status"}).AddRow(11, 3, "active"))
	mock.ExpectQuery(`(?s)SELECT active_period_id, touch_expires_at FROM account_user_contacts.*FOR UPDATE`).
		WithArgs(int64(11), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"active_period_id", "touch_expires_at"}).AddRow(91, future))
	mock.ExpectExec(`(?s)UPDATE account_user_contact_periods SET last_touched_at`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)INSERT INTO account_user_contacts`).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`(?s)UPDATE user_account_placements SET last_active_at`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE openai_user_affinity_reset_exclusions SET consumed_at`).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	mock.ExpectExec(`(?s)UPDATE user_account_capacity_incidents SET status = 'closed'.*resident_recovered`).WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.TouchOpenAIUserAffinity(context.Background(), 42, 11, 3, scopeKey, config)
	require.NoError(t, err)
	err = repo.ConfirmOpenAIUserAffinitySuccess(context.Background(), service.OpenAIUserAffinityIncidentIdentity{
		UserID: 42, AccountID: 11, ScopeKey: scopeKey, PlacementGeneration: 3,
	})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRecordOpenAIUserAffinityCapacityFailureExtendsAuthorizedWindow(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	config := service.DefaultOpenAIUserAffinityConfig()
	config.CapacityFailureMigrationThreshold = 2
	scopeKey := "openai:v1:group:1:lane:general"

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT capacity_failure_migration_threshold.*FROM accounts WHERE id = \$1`).
		WithArgs(int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"capacity_failure_migration_threshold", "capacity_failure_window_seconds"}).AddRow(nil, nil))
	mock.ExpectQuery(`(?s)SELECT account_id, generation, status FROM user_account_placements.*FOR UPDATE`).
		WithArgs(int64(42), scopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "generation", "status"}).AddRow(11, 3, "active"))
	mock.ExpectQuery(`(?s)SELECT id, failure_count, migration_authorized_at.*window_expires_at > \$5`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "failure_count", "migration_authorized_at"}).AddRow(91, 1, nil))
	mock.ExpectQuery(`(?s)INSERT INTO user_account_capacity_failures.*RETURNING id`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(101))
	mock.ExpectExec(`(?s)UPDATE user_account_capacity_incidents SET failure_count.*migration_authorized_at = \$5::timestamptz.*CASE WHEN \$5::timestamptz IS NOT NULL.*GREATEST\(window_expires_at, \$7\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	authorizedAt, err := repo.RecordOpenAIUserAffinityCapacityFailure(
		context.Background(), service.OpenAIUserAffinityIncidentIdentity{
			UserID: 42, AccountID: 11, ScopeKey: scopeKey, PlacementGeneration: 3,
		}, strings.Repeat("a", 64), "concurrency_unavailable", config,
	)
	require.NoError(t, err)
	require.NotNil(t, authorizedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetOpenAIUserAffinityMigrationAuthorizedAtIgnoresExpiredWindow(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	scopeKey := "openai:v1:group:1:lane:general"
	mock.ExpectQuery(`(?s)SELECT migration_authorized_at.*window_expires_at > NOW\(\)`).
		WithArgs(int64(42), scopeKey, int64(11), int64(3), nil, nil, nil).
		WillReturnRows(sqlmock.NewRows([]string{"migration_authorized_at"}))

	authorizedAt, err := repo.GetOpenAIUserAffinityMigrationAuthorizedAt(context.Background(), service.OpenAIUserAffinityIncidentIdentity{
		UserID: 42, AccountID: 11, ScopeKey: scopeKey, PlacementGeneration: 3,
	})
	require.NoError(t, err)
	require.Nil(t, authorizedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBeginOpenAIUserAffinityReentrySeparatesPlacementAndCoordinationGenerations(t *testing.T) {
	repo, mock := newOpenAIUserAffinityRepositoryTest(t)
	config := service.DefaultOpenAIUserAffinityConfig()
	scopeKey := "openai:v1:group:2:lane:general"
	future := time.Now().UTC().Add(time.Minute)

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT max_contact_users FROM accounts.*FOR UPDATE`).
		WithArgs(int64(11)).
		WillReturnRows(sqlmock.NewRows([]string{"max_contact_users"}).AddRow(10))
	mock.ExpectQuery(`(?s)SELECT account_id, generation, status, expires_at FROM user_account_placements.*FOR UPDATE`).
		WithArgs(int64(42), scopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "generation", "status", "expires_at"}).AddRow(11, 9, "active", future))
	mock.ExpectQuery(`(?s)SELECT touch_expires_at, reservation_until, reservation_generation, reentry_batch_token.*account_user_contacts.*FOR UPDATE`).
		WithArgs(int64(11), int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{
			"touch_expires_at", "reservation_until", "reservation_generation", "reentry_batch_token",
			"reentry_state", "leader_token", "leader_version", "leader_lease_until",
			"follower_jitter_min_ms", "follower_jitter_max_ms",
		}).AddRow(nil, future, 3, "shared-batch", "leader_pending", "leader-3", 7, future, 100, 500))
	mock.ExpectRollback()

	admission, err := repo.BeginOpenAIUserAffinityReentry(context.Background(), service.OpenAIUserAffinityReentryBegin{
		UserID: 42, AccountID: 11, Generation: 9, ScopeKey: scopeKey, Config: config,
	})
	require.NoError(t, err)
	require.NotNil(t, admission)
	require.False(t, admission.Leader)
	require.Equal(t, int64(9), admission.Generation)
	require.Equal(t, int64(3), admission.CoordinationGeneration)
	require.NoError(t, mock.ExpectationsWereMet())
}
