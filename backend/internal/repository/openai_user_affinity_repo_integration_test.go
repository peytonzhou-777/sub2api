//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountResidentCapacityAndPoolStatsShareUniqueUserSemantics(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	account := mustCreateAccount(t, client, &service.Account{
		Name: "openai-resident-capacity-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	users := make([]*service.User, 6)
	for i := range users {
		users[i] = mustCreateUser(t, client, &service.User{
			Email: "resident-capacity-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
		})
	}
	now := time.Now().UTC()
	type slotSeed struct {
		user        *service.User
		scope       string
		status      string
		expiresAt   time.Time
		lastSuccess any
	}
	seeds := []slotSeed{
		{users[0], "openai:v1:group:1:lane:general", "active", now.Add(time.Hour), now},
		{users[0], "openai:v1:group:2:lane:general", "active", now.Add(time.Hour), nil},
		{users[1], "openai:v1:group:1:lane:general", "provisional", now.Add(time.Hour), nil},
		{users[2], "openai:v1:group:1:lane:general", "replacement_pending", now.Add(time.Hour), nil},
		{users[3], "openai:v1:group:1:lane:general", "draining", now.Add(-time.Hour), nil},
		{users[5], "openai:v1:group:1:lane:general", "expired", now.Add(-time.Hour), nil},
	}
	for i, seed := range seeds {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO openai_user_resident_slots
				(user_id, scope_key, slot_index, account_id, generation, status, expires_at, last_success_at)
			VALUES ($1, $2, 1, $3, $4, $5, $6, $7)`,
			seed.user.ID, seed.scope, account.ID, int64(i+1), seed.status, seed.expiresAt, seed.lastSuccess)
		require.NoError(t, err)
	}
	for _, user := range []*service.User{users[0], users[4]} {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO user_account_placements
				(user_id, scope_key, account_id, generation, status, expires_at)
			VALUES ($1, $2, $3, 1, 'active', $4)`,
			user.ID, "openai:v1:group:9:lane:general", account.ID, now.Add(time.Hour))
		require.NoError(t, err)
	}

	residentUsers, alreadyResident, err := getOpenAIAccountResidentCapacity(ctx, tx, account.ID, users[0].ID, now)
	require.NoError(t, err)
	require.Equal(t, 5, residentUsers)
	require.True(t, alreadyResident)

	residentUsers, alreadyResident, err = getOpenAIAccountResidentCapacity(ctx, tx, account.ID, users[5].ID, now)
	require.NoError(t, err)
	require.Equal(t, 5, residentUsers)
	require.False(t, alreadyResident)

	stats, err := repo.ListAccountPoolResidentStats(ctx, []int64{account.ID}, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(5), stats[account.ID].Total)
	require.Equal(t, int64(1), stats[account.ID].Active)
	require.Equal(t, int64(1), stats[account.ID].DrainingSlots)
}

// 该回归测试必须使用真实 PostgreSQL，sqlmock 无法触发无类型 NULL 的参数推断错误。
func TestRecordOpenAIUserAffinityCapacityFailureBeforeMigrationThreshold(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{})
	account := mustCreateAccount(t, client, &service.Account{
		Name:     "openai-affinity-capacity-failure-" + time.Now().Format(time.RFC3339Nano),
		Platform: service.PlatformOpenAI,
	})
	scopeKey := "openai:v1:group:1:lane:general"
	generation := int64(3)

	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_account_placements
			(user_id, scope_key, account_id, generation, status, expires_at)
		VALUES ($1, $2, $3, $4, 'active', $5)`,
		user.ID, scopeKey, account.ID, generation, time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)

	config := service.DefaultOpenAIUserAffinityConfig()
	config.CapacityFailureMigrationThreshold = 2
	authorizedAt, err := repo.RecordOpenAIUserAffinityCapacityFailure(
		ctx, service.OpenAIUserAffinityIncidentIdentity{
			UserID: user.ID, AccountID: account.ID, ScopeKey: scopeKey, PlacementGeneration: generation,
		}, strings.Repeat("a", 64), "resident_account_excluded", config,
	)
	require.NoError(t, err)
	require.Nil(t, authorizedAt)

	var failureCount, failureThreshold int
	var migrationAuthorizedAt sql.NullTime
	var status string
	err = scanSingleRow(ctx, tx, `
		SELECT failure_count, failure_threshold, migration_authorized_at, status
		FROM user_account_capacity_incidents
		WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3
		  AND placement_generation = $4`,
		[]any{user.ID, scopeKey, account.ID, generation},
		&failureCount, &failureThreshold, &migrationAuthorizedAt, &status)
	require.NoError(t, err)
	require.Equal(t, 1, failureCount)
	require.Equal(t, 2, failureThreshold)
	require.False(t, migrationAuthorizedAt.Valid)
	require.Equal(t, "collecting", status)
}

func TestOpenAIUserConversationBindingProvisionalLifecycle(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-conversation-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-conversation-" + uuid.NewString(), Name: "affinity",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-conversation-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	now := time.Now().UTC()
	scopeKey := "openai:v1:group:simple:lane:general"
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_account_placements
			(user_id, scope_key, account_id, generation, status, assigned_at, expires_at)
		VALUES ($1, $2, $3, 2, 'active', $4, $5)`,
		user.ID, scopeKey, account.ID, now, now.Add(14*24*time.Hour))
	require.NoError(t, err)

	config := service.DefaultOpenAIUserAffinityConfig()
	conversationHash := strings.Repeat("b", 64)
	aliasHash := strings.Repeat("c", 64)
	threadAliasHash := strings.Repeat("a", 64)
	threadAliasScope := "openai:v1:group:simple:lineage:codex-thread"
	responseAliasHash := strings.Repeat("e", 64)
	token := uuid.NewString()
	binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
		ConversationHash: conversationHash, AliasType: "response_id", AliasHash: aliasHash,
		Aliases:   []service.OpenAIUserConversationAlias{{ScopeKey: threadAliasScope, Type: "codex_thread", Hash: threadAliasHash}},
		AccountID: account.ID, PlacementGeneration: 2, ContextRebuildable: true,
		ProvisionalToken: token, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "provisional", binding.Status)
	require.False(t, binding.FirstOutputCommitted)

	transition := service.OpenAIUserConversationTransition{
		BindingID: binding.ID, UserID: user.ID, APIKeyID: apiKey.ID,
		ScopeKey: scopeKey, ConversationHash: conversationHash, ResidentSlotID: binding.ResidentSlotID,
		AccountID: account.ID, SlotGeneration: binding.SlotGeneration,
		ProvisionalToken: token, ResponseAliasHash: responseAliasHash, Config: config,
	}
	firstCommit, err := repo.CommitOpenAIUserConversationBinding(ctx, transition)
	require.NoError(t, err)
	require.True(t, firstCommit)
	loaded, err := repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, conversationHash)
	require.NoError(t, err)
	require.NotNil(t, loaded)
	require.Equal(t, "active", loaded.Status)
	require.True(t, loaded.FirstOutputCommitted)
	require.NotNil(t, loaded.ActiveUntil)
	require.WithinDuration(t, *loaded.ActiveUntil, loaded.ExpiresAt, time.Millisecond)
	require.WithinDuration(t, time.Now().UTC().Add(config.ConversationActiveTTL()), loaded.ExpiresAt, 5*time.Second)
	byAlias, err := repo.GetOpenAIUserConversationBindingByAlias(ctx, user.ID, apiKey.ID, scopeKey, "response_id", aliasHash)
	require.NoError(t, err)
	require.NotNil(t, byAlias)
	require.Equal(t, loaded.ID, byAlias.ID)
	byThreadAlias, err := repo.GetOpenAIUserConversationBindingByAlias(ctx, user.ID, apiKey.ID, threadAliasScope, "codex_thread", threadAliasHash)
	require.NoError(t, err)
	require.NotNil(t, byThreadAlias)
	require.Equal(t, loaded.ID, byThreadAlias.ID)
	secondThreadAliasHash := strings.Repeat("f", 64)
	existingBinding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: conversationHash,
		AccountID: account.ID, PlacementGeneration: binding.SlotGeneration, ContextRebuildable: true,
		ProvisionalToken: uuid.NewString(), Config: config,
		Aliases: []service.OpenAIUserConversationAlias{{ScopeKey: threadAliasScope, Type: "codex_thread", Hash: secondThreadAliasHash}},
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, loaded.ID, existingBinding.ID)
	bySecondThreadAlias, err := repo.GetOpenAIUserConversationBindingByAlias(
		ctx, user.ID, apiKey.ID, threadAliasScope, "codex_thread", secondThreadAliasHash,
	)
	require.NoError(t, err)
	require.NotNil(t, bySecondThreadAlias)
	require.Equal(t, loaded.ID, bySecondThreadAlias.ID, "既有 binding 必须可原子补齐新线程别名")
	otherAPIKeyForSameUser := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-conversation-same-user-" + uuid.NewString(), Name: "affinity",
	})
	crossAPIKeyThreadAlias, err := repo.GetOpenAIUserConversationBindingByAlias(
		ctx, user.ID, otherAPIKeyForSameUser.ID, threadAliasScope, "codex_thread", threadAliasHash,
	)
	require.NoError(t, err)
	require.Nil(t, crossAPIKeyThreadAlias, "Codex thread alias 保持现有 API Key 隔离")
	byResponseAlias, err := repo.GetOpenAIUserConversationBindingByAlias(ctx, user.ID, apiKey.ID, scopeKey, "response_id", responseAliasHash)
	require.NoError(t, err)
	require.NotNil(t, byResponseAlias)
	require.Equal(t, loaded.ID, byResponseAlias.ID)
	otherUser := mustCreateUser(t, client, &service.User{
		Email: "affinity-conversation-other-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	otherAPIKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: otherUser.ID, Key: "sk-affinity-conversation-other-" + uuid.NewString(), Name: "affinity",
	})
	crossUserAlias, err := repo.GetOpenAIUserConversationBindingByAlias(ctx, otherUser.ID, otherAPIKey.ID, scopeKey, "response_id", responseAliasHash)
	require.NoError(t, err)
	require.Nil(t, crossUserAlias, "相同 response alias 不得跨认证用户命中")

	rollbackHash := strings.Repeat("d", 64)
	rollbackToken := uuid.NewString()
	rollbackBinding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
		ConversationHash: rollbackHash, AccountID: account.ID, PlacementGeneration: 2,
		ContextRebuildable: true, ProvisionalToken: rollbackToken, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	rolledBack, err := repo.RollbackOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: rollbackBinding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
		ConversationHash: rollbackBinding.ConversationHash, ResidentSlotID: rollbackBinding.ResidentSlotID,
		AccountID: account.ID, SlotGeneration: rollbackBinding.SlotGeneration, ProvisionalToken: rollbackToken,
	})
	require.NoError(t, err)
	require.True(t, rolledBack)
	missing, err := repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, rollbackHash)
	require.NoError(t, err)
	require.Nil(t, missing)
}

// 新会话在已有 active 槽位失败时，事故必须先落库，再由 token CAS 回滚 provisional binding。
func TestOpenAIUserConversationProvisionalCapacityFailureIsRecorded(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-provisional-incident-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-provisional-incident-" + uuid.NewString(), Name: "affinity",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-provisional-incident-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	config := service.DefaultOpenAIUserAffinityConfig()
	config.CapacityFailureMigrationThreshold = 2
	scopeKey := "openai:v1:group:simple:lane:provisional-incident"

	reserve := func(hash, token string) *service.OpenAIUserConversationBinding {
		binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
			UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: hash,
			AccountID: account.ID, PlacementGeneration: 1, MaxResidentSlots: 1,
			ContextRebuildable: true, ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		require.True(t, created)
		return binding
	}
	firstToken := uuid.NewString()
	first := reserve(strings.Repeat("1", 64), firstToken)
	committed, err := repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: first.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: first.ConversationHash,
		ResidentSlotID: first.ResidentSlotID, AccountID: account.ID, SlotGeneration: first.SlotGeneration,
		ProvisionalToken: firstToken, Config: config,
	})
	require.NoError(t, err)
	require.True(t, committed)

	secondToken := uuid.NewString()
	second := reserve(strings.Repeat("2", 64), secondToken)
	authorizedAt, err := repo.RecordOpenAIUserAffinityCapacityFailure(ctx, service.OpenAIUserAffinityIncidentIdentity{
		UserID: user.ID, AccountID: account.ID, ScopeKey: scopeKey,
		PlacementGeneration: second.SlotGeneration, ConversationHash: second.ConversationHash,
		ResidentSlotID: second.ResidentSlotID, SlotGeneration: second.SlotGeneration,
	}, strings.Repeat("f", 64), "concurrency_unavailable", config)
	require.NoError(t, err)
	require.Nil(t, authorizedAt)

	var incidentCount, failureCount int
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT COUNT(*), COALESCE(SUM(failure_count), 0) FROM user_account_capacity_incidents
		WHERE user_id = $1 AND scope_key = $2 AND conversation_hash = $3::char(64)`,
		[]any{user.ID, scopeKey, second.ConversationHash}, &incidentCount, &failureCount))
	require.Equal(t, 1, incidentCount)
	require.Equal(t, 1, failureCount)

	rolledBack, err := repo.RollbackOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: second.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: second.ConversationHash,
		ResidentSlotID: second.ResidentSlotID, AccountID: account.ID, SlotGeneration: second.SlotGeneration,
		ProvisionalToken: secondToken, Config: config,
	})
	require.NoError(t, err)
	require.True(t, rolledBack)
}

func TestOpenAIUserResidentSlotIgnoresConversationCountForSoftOccupancy(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{Email: "affinity-stale-binding-" + uuid.NewString() + "@example.com", PasswordHash: "hash"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-affinity-stale-binding-" + uuid.NewString(), Name: "affinity"})
	account := mustCreateAccount(t, client, &service.Account{Name: "openai-affinity-stale-binding-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	config := service.DefaultOpenAIUserAffinityConfig()
	scopeKey := "openai:v1:group:simple:lane:stale-binding"
	token := uuid.NewString()
	binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: strings.Repeat("3", 64),
		AccountID: account.ID, PlacementGeneration: 1, MaxResidentSlots: 2,
		ContextRebuildable: true, ProvisionalToken: token, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	_, err = tx.ExecContext(ctx, `UPDATE openai_user_conversation_bindings
		SET expires_at = $2, active_until = $3 WHERE id = $1`,
		binding.ID, time.Now().UTC().Add(-time.Minute), time.Now().UTC().Add(time.Hour))
	require.NoError(t, err)

	slots, err := repo.ListOpenAIUserResidentSlots(ctx, user.ID, scopeKey)
	require.NoError(t, err)
	require.Len(t, slots, 1)
	require.Zero(t, slots[0].ActiveRouteUserCount)
}

func TestOpenAIUserActiveRouteSoftOwnerAndPendingLifecycle(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	firstUser := mustCreateUser(t, client, &service.User{Email: "affinity-route-first-" + uuid.NewString() + "@example.com", PasswordHash: "hash"})
	secondUser := mustCreateUser(t, client, &service.User{Email: "affinity-route-second-" + uuid.NewString() + "@example.com", PasswordHash: "hash"})
	firstKey := mustCreateApiKey(t, client, &service.APIKey{UserID: firstUser.ID, Key: "sk-affinity-route-first-" + uuid.NewString(), Name: "affinity"})
	secondKey := mustCreateApiKey(t, client, &service.APIKey{UserID: secondUser.ID, Key: "sk-affinity-route-second-" + uuid.NewString(), Name: "affinity"})
	firstAccount := mustCreateAccount(t, client, &service.Account{Name: "openai-affinity-route-first-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	secondAccount := mustCreateAccount(t, client, &service.Account{Name: "openai-affinity-route-second-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	config.DefaultNewResidentCooldownSeconds = 0
	scopeKey := "openai:v1:group:simple:lane:active-route"

	reserveAndCommit := func(userID, apiKeyID, accountID int64, hashByte string) *service.OpenAIUserConversationBinding {
		t.Helper()
		token := uuid.NewString()
		binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
			UserID: userID, APIKeyID: apiKeyID, ScopeKey: scopeKey, ConversationHash: strings.Repeat(hashByte, 64),
			AccountID: accountID, PlacementGeneration: 1, MaxResidentSlots: 2,
			ContextRebuildable: true, ProvisionalToken: token, ManageActiveRoute: true, Config: config,
		})
		require.NoError(t, err)
		require.True(t, created)
		require.NotNil(t, binding)
		committed, err := repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
			BindingID: binding.ID, UserID: userID, APIKeyID: apiKeyID, ScopeKey: scopeKey,
			ConversationHash: binding.ConversationHash, ResidentSlotID: binding.ResidentSlotID,
			AccountID: accountID, SlotGeneration: binding.SlotGeneration, ProvisionalToken: token,
			ManageActiveRoute: binding.ManageActiveRoute, ActiveRoutePending: binding.ActiveRoutePending, Config: config,
		})
		require.NoError(t, err)
		require.True(t, committed)
		return binding
	}

	firstBinding := reserveAndCommit(firstUser.ID, firstKey.ID, firstAccount.ID, "a")
	_ = reserveAndCommit(secondUser.ID, secondKey.ID, firstAccount.ID, "b")
	occupancies, err := repo.ListOpenAIAccountSoftOccupancies(ctx, []int64{firstAccount.ID, secondAccount.ID})
	require.NoError(t, err)
	require.Equal(t, 2, occupancies[firstAccount.ID].ActiveUserCount)
	require.Equal(t, firstUser.ID, occupancies[firstAccount.ID].OwnerUserID)

	token := uuid.NewString()
	pendingBinding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: secondUser.ID, APIKeyID: secondKey.ID, ScopeKey: scopeKey, ConversationHash: strings.Repeat("c", 64),
		AccountID: secondAccount.ID, PlacementGeneration: 2, MaxResidentSlots: 2,
		ContextRebuildable: true, ProvisionalToken: token, ManageActiveRoute: true, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.True(t, pendingBinding.ActiveRoutePending)
	route, err := repo.GetOpenAIUserActiveRoute(ctx, secondUser.ID, scopeKey)
	require.NoError(t, err)
	require.Equal(t, firstAccount.ID, route.AccountID)
	require.Equal(t, secondAccount.ID, route.PendingAccountID)
	occupancies, err = repo.ListOpenAIAccountSoftOccupancies(ctx, []int64{firstAccount.ID, secondAccount.ID})
	require.NoError(t, err)
	require.Equal(t, secondUser.ID, occupancies[secondAccount.ID].OwnerUserID)

	rolledBack, err := repo.RollbackOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: pendingBinding.ID, UserID: secondUser.ID, APIKeyID: secondKey.ID, ScopeKey: scopeKey,
		ConversationHash: pendingBinding.ConversationHash,
		ResidentSlotID:   pendingBinding.ResidentSlotID, AccountID: secondAccount.ID,
		SlotGeneration: pendingBinding.SlotGeneration, ProvisionalToken: token,
		ManageActiveRoute: true, ActiveRoutePending: true, Config: config,
	})
	require.NoError(t, err)
	require.True(t, rolledBack)
	route, err = repo.GetOpenAIUserActiveRoute(ctx, secondUser.ID, scopeKey)
	require.NoError(t, err)
	require.Equal(t, firstAccount.ID, route.AccountID)
	require.Zero(t, route.PendingAccountID)

	finalBinding := reserveAndCommit(secondUser.ID, secondKey.ID, secondAccount.ID, "d")
	require.NotEqual(t, firstBinding.ResidentSlotID, finalBinding.ResidentSlotID)
	route, err = repo.GetOpenAIUserActiveRoute(ctx, secondUser.ID, scopeKey)
	require.NoError(t, err)
	require.Equal(t, secondAccount.ID, route.AccountID)
	require.Zero(t, route.PendingAccountID)
	occupancies, err = repo.ListOpenAIAccountSoftOccupancies(ctx, []int64{firstAccount.ID, secondAccount.ID})
	require.NoError(t, err)
	require.Equal(t, 1, occupancies[firstAccount.ID].ActiveUserCount)
	require.Equal(t, firstUser.ID, occupancies[firstAccount.ID].OwnerUserID)
	require.Equal(t, 1, occupancies[secondAccount.ID].ActiveUserCount)
	require.Equal(t, secondUser.ID, occupancies[secondAccount.ID].OwnerUserID)
}

func TestOpenAIUserAffinityConvergenceShortensExistingTTL(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{Email: "affinity-ttl-convergence-" + uuid.NewString() + "@example.com", PasswordHash: "hash"})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-affinity-ttl-convergence-" + uuid.NewString(), Name: "affinity"})
	account := mustCreateAccount(t, client, &service.Account{Name: "openai-affinity-ttl-convergence-" + uuid.NewString(), Platform: service.PlatformOpenAI})
	config := service.DefaultOpenAIUserAffinityConfig()
	scopeKey := "openai:v1:group:simple:lane:ttl-convergence"
	token := uuid.NewString()
	responseAliasHash := strings.Repeat("4", 64)
	binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: strings.Repeat("5", 64),
		AccountID: account.ID, PlacementGeneration: 1, MaxResidentSlots: 1,
		ContextRebuildable: true, ProvisionalToken: token, ManageActiveRoute: true, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	committed, err := repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: binding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: binding.ConversationHash,
		ResidentSlotID: binding.ResidentSlotID, AccountID: account.ID, SlotGeneration: binding.SlotGeneration,
		ProvisionalToken: token, ResponseAliasHash: responseAliasHash,
		ManageActiveRoute: binding.ManageActiveRoute, ActiveRoutePending: binding.ActiveRoutePending, Config: config,
	})
	require.NoError(t, err)
	require.True(t, committed)
	oldSuccess := time.Now().UTC().Add(-48 * time.Hour)
	farFuture := time.Now().UTC().Add(7 * 24 * time.Hour)
	_, err = tx.ExecContext(ctx, `UPDATE openai_user_resident_slots SET last_success_at = $2, expires_at = $3 WHERE id = $1`,
		binding.ResidentSlotID, oldSuccess, farFuture)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE openai_user_conversation_bindings
		SET last_success_at = $2, expires_at = $3, active_until = $3 WHERE id = $1`, binding.ID, oldSuccess, farFuture)
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, `UPDATE openai_user_conversation_aliases SET expires_at = $2 WHERE binding_id = $1`, binding.ID, farFuture)
	require.NoError(t, err)

	reduced := config
	reduced.ResidentTTLSeconds = 24 * 60 * 60
	reduced.ConversationActiveTTLSeconds = 30 * 60
	now := time.Now().UTC()
	require.NoError(t, repo.ConvergeOpenAIUserResidentSlots(ctx, user.ID, scopeKey, reduced, now))

	var slotStatus, bindingStatus string
	var activeRouteCount int
	var slotExpiresAt, bindingExpiresAt, aliasExpiresAt time.Time
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT status, expires_at FROM openai_user_resident_slots WHERE id = $1`,
		[]any{binding.ResidentSlotID}, &slotStatus, &slotExpiresAt))
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT status, expires_at FROM openai_user_conversation_bindings WHERE id = $1`,
		[]any{binding.ID}, &bindingStatus, &bindingExpiresAt))
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT expires_at FROM openai_user_conversation_aliases
		WHERE binding_id = $1 AND alias_type = 'response_id' AND alias_hash = $2::char(64)`,
		[]any{binding.ID, responseAliasHash}, &aliasExpiresAt))
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT COUNT(*) FROM openai_user_active_routes
		WHERE user_id = $1 AND scope_key = $2`, []any{user.ID, scopeKey}, &activeRouteCount))
	require.Equal(t, service.OpenAIUserResidentSlotStatusExpired, slotStatus)
	require.Equal(t, "expired", bindingStatus)
	require.False(t, slotExpiresAt.After(now))
	require.False(t, bindingExpiresAt.After(now))
	require.False(t, aliasExpiresAt.After(now))
	require.Zero(t, activeRouteCount)
}

// 多槽位上限、触达预留和回滚必须在同一事务中生效。
func TestOpenAIUserConversationBindingEnforcesResidentSlotLimit(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-slot-limit-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-slot-limit-" + uuid.NewString(), Name: "affinity",
	})
	accounts := make([]*service.Account, 0, 3)
	for i := 0; i < 3; i++ {
		accounts = append(accounts, mustCreateAccount(t, client, &service.Account{
			Name: "openai-affinity-slot-limit-" + uuid.NewString(), Platform: service.PlatformOpenAI,
		}))
	}
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	scopeKey := "openai:v1:group:simple:lane:multi-slot-limit"

	reserve := func(account *service.Account, hashByte string) (*service.OpenAIUserConversationBinding, string, bool, error) {
		token := uuid.NewString()
		binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
			UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
			ConversationHash: strings.Repeat(hashByte, 64), AccountID: account.ID,
			PlacementGeneration: 1, MaxResidentSlots: 2, ContextRebuildable: true,
			ProvisionalToken: token, Config: config,
		})
		return binding, token, created, err
	}

	first, _, created, err := reserve(accounts[0], "1")
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, first)
	second, secondToken, created, err := reserve(accounts[1], "2")
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, second)
	blocked, _, created, err := reserve(accounts[2], "3")
	require.ErrorIs(t, err, service.ErrOpenAIUserAffinityResidentSlotsFull)
	require.False(t, created)
	require.Nil(t, blocked)

	var activeSlots, blockedContacts int
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT COUNT(*) FROM openai_user_resident_slots
		WHERE user_id = $1 AND scope_key = $2
		  AND status IN ('provisional', 'active', 'replacement_pending')`,
		[]any{user.ID, scopeKey}, &activeSlots))
	require.Equal(t, 2, activeSlots)
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT COUNT(*) FROM account_user_contacts WHERE account_id = $1 AND user_id = $2`,
		[]any{accounts[2].ID, user.ID}, &blockedContacts))
	require.Zero(t, blockedContacts, "槽位上限拒绝时不得泄漏触达预留")

	rolledBack, err := repo.RollbackOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: second.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
		ConversationHash: second.ConversationHash, ResidentSlotID: second.ResidentSlotID,
		AccountID: accounts[1].ID, SlotGeneration: second.SlotGeneration, ProvisionalToken: secondToken,
	})
	require.NoError(t, err)
	require.True(t, rolledBack)
	replacement, _, created, err := reserve(accounts[2], "3")
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, replacement)
}

func TestReserveOpenAIUserConversationBindingDrainingConflictReturnsDomainErrorAndAllowsReselection(t *testing.T) {
	ctx := context.Background()
	client := integrationEntClient
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-draining-conflict-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-draining-conflict-" + uuid.NewString(), Name: "affinity",
	})
	drainingAccount := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-draining-conflict-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	activeAccount := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-draining-reselect-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id IN ($1, $2)`, drainingAccount.ID, activeAccount.ID)
	})
	scopeKey := "openai:v1:group:simple:lane:draining-conflict"
	now := time.Now().UTC()
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO openai_user_resident_slots
			(user_id, scope_key, slot_index, account_id, generation, status, admitted_at, expires_at, score_updated_at)
		VALUES ($1, $2, 1, $3, 4, 'draining', $4, $5, $4)`,
		user.ID, scopeKey, drainingAccount.ID, now, now.Add(time.Hour))
	require.NoError(t, err)
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2

	binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("d", 64), AccountID: drainingAccount.ID,
		PlacementGeneration: 5, MaxResidentSlots: 2, ContextRebuildable: true,
		ProvisionalToken: uuid.NewString(), Config: config,
	})
	require.ErrorIs(t, err, service.ErrOpenAIUserAffinityDrainingSlotConflict)
	require.False(t, errors.Is(err, sql.ErrNoRows))
	require.False(t, created)
	require.Nil(t, binding)

	binding, created, err = repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("d", 64), AccountID: activeAccount.ID,
		PlacementGeneration: 5, MaxResidentSlots: 2, ContextRebuildable: true,
		ProvisionalToken: uuid.NewString(), Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, binding)
	require.Equal(t, activeAccount.ID, binding.AccountID)
}

func TestReserveOpenAIUserConversationBindingUsesGenerationAfterExpiredHistory(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-expired-generation-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-expired-generation-" + uuid.NewString(), Name: "affinity",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-expired-generation-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	scopeKey := "openai:v1:group:simple:lane:expired-generation"
	now := time.Now().UTC()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO openai_user_resident_slots
			(user_id, scope_key, slot_index, account_id, generation, status, admitted_at, expires_at, score_updated_at)
		VALUES ($1, $2, 1, $3, 1, 'expired', $4, $4, $4)`,
		user.ID, scopeKey, account.ID, now)
	require.NoError(t, err)

	binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
		ConversationHash: strings.Repeat("f", 64), AccountID: account.ID,
		PlacementGeneration: 1, MaxResidentSlots: 2, ContextRebuildable: true,
		ProvisionalToken: uuid.NewString(), Config: service.DefaultOpenAIUserAffinityConfig(),
	})
	require.NoError(t, err)
	require.True(t, created)
	require.NotNil(t, binding)
	require.Equal(t, int64(2), binding.SlotGeneration)
}

func TestReserveOpenAIUserConversationBindingMissingAccountReturnsDomainError(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-missing-account-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-missing-account-" + uuid.NewString(), Name: "affinity",
	})

	binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID,
		ScopeKey:         "openai:v1:group:simple:lane:missing-account",
		ConversationHash: strings.Repeat("a", 64), AccountID: 1<<62 - 1,
		PlacementGeneration: 1, MaxResidentSlots: 2, ContextRebuildable: true,
		ProvisionalToken: uuid.NewString(), Config: service.DefaultOpenAIUserAffinityConfig(),
	})
	require.ErrorIs(t, err, service.ErrOpenAIUserAffinityAccountUnavailable)
	require.False(t, errors.Is(err, sql.ErrNoRows))
	require.False(t, created)
	require.Nil(t, binding)
}

func TestReserveOpenAIUserConversationBindingConcurrentSameConversationIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := integrationEntClient
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-concurrent-binding-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-concurrent-binding-" + uuid.NewString(), Name: "affinity",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-concurrent-binding-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
	})
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	scopeKey := "openai:v1:group:simple:lane:concurrent-binding"
	conversationHash := strings.Repeat("e", 64)
	type reserveResult struct {
		binding *service.OpenAIUserConversationBinding
		created bool
		err     error
	}
	results := make(chan reserveResult, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
				UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
				ConversationHash: conversationHash, AccountID: account.ID,
				PlacementGeneration: 1, MaxResidentSlots: 2, ContextRebuildable: true,
				ProvisionalToken: uuid.NewString(), Config: config,
			})
			results <- reserveResult{binding: binding, created: created, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	createdCount := 0
	bindingID := int64(0)
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.binding)
		if result.created {
			createdCount++
		}
		if bindingID == 0 {
			bindingID = result.binding.ID
		}
		require.Equal(t, bindingID, result.binding.ID)
	}
	require.Equal(t, 1, createdCount)
	var bindingCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM openai_user_conversation_bindings
		WHERE user_id = $1 AND api_key_id = $2 AND scope_key = $3 AND conversation_hash = $4`,
		user.ID, apiKey.ID, scopeKey, conversationHash).Scan(&bindingCount))
	require.Equal(t, 1, bindingCount)
}

func TestReserveOpenAIUserConversationBindingRebuildsStaleSlotGeneration(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-stale-generation-rebuild-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-stale-generation-rebuild-" + uuid.NewString(), Name: "affinity",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-stale-generation-rebuild-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	scopeKey := "openai:v1:group:simple:lane:stale-generation-rebuild"
	conversationHash := strings.Repeat("7", 64)
	originalToken := uuid.NewString()
	binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: conversationHash,
		AccountID: account.ID, PlacementGeneration: 1, MaxResidentSlots: 2,
		ContextRebuildable: true, ProvisionalToken: originalToken, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	committed, err := repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: binding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
		ConversationHash: conversationHash, ResidentSlotID: binding.ResidentSlotID,
		AccountID: account.ID, SlotGeneration: binding.SlotGeneration,
		ProvisionalToken: originalToken, Config: config,
	})
	require.NoError(t, err)
	require.True(t, committed)

	_, err = tx.ExecContext(ctx, `UPDATE openai_user_resident_slots SET generation = 2 WHERE id = $1`, binding.ResidentSlotID)
	require.NoError(t, err)
	valid, err := repo.ValidateOpenAIUserConversationBinding(ctx, *binding)
	require.NoError(t, err)
	require.False(t, valid)

	rebuildToken := uuid.NewString()
	rebuilt, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: conversationHash,
		AccountID: account.ID, PlacementGeneration: 2, MaxResidentSlots: 2,
		PreferredResidentSlotID: binding.ResidentSlotID, PreferredSlotGeneration: 2,
		ContextRebuildable: true, ProvisionalToken: rebuildToken, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, binding.ID, rebuilt.ID)
	require.Equal(t, int64(2), rebuilt.SlotGeneration)
	require.Equal(t, "provisional", rebuilt.Status)
	require.False(t, rebuilt.FirstOutputCommitted)
	require.Equal(t, rebuildToken, rebuilt.ProvisionalToken)
}

func TestCrossScopeLineageConcurrentRebuildLifecycle(t *testing.T) {
	ctx := context.Background()
	client := integrationEntClient
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-cross-scope-rebuild-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-cross-scope-rebuild-" + uuid.NewString(), Name: "affinity",
	})
	sourceAccount := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-cross-scope-source-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	targetAccount := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-cross-scope-target-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id IN ($1, $2)`, sourceAccount.ID, targetAccount.ID)
	})
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	chatScope := "openai:v1:group:simple:lane:endpoint:chat_completions"
	responsesScope := "openai:v1:group:simple:lane:endpoint:responses"
	lineageScope := "openai:v1:group:simple:lineage:codex-thread"
	lineageHash := strings.Repeat("b", 64)
	alias := service.OpenAIUserConversationAlias{ScopeKey: lineageScope, Type: "codex_thread", Hash: lineageHash}
	sourceToken := uuid.NewString()
	sourceBinding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: chatScope, ConversationHash: strings.Repeat("c", 64),
		AccountID: sourceAccount.ID, PlacementGeneration: 1, MaxResidentSlots: 2,
		ContextRebuildable: true, ProvisionalToken: sourceToken, Aliases: []service.OpenAIUserConversationAlias{alias}, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	_, err = repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: sourceBinding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: chatScope,
		ConversationHash: sourceBinding.ConversationHash, ResidentSlotID: sourceBinding.ResidentSlotID,
		AccountID: sourceAccount.ID, SlotGeneration: sourceBinding.SlotGeneration,
		ProvisionalToken: sourceToken, Aliases: []service.OpenAIUserConversationAlias{alias}, Config: config,
	})
	require.NoError(t, err)
	valid, err := repo.ValidateOpenAIUserConversationBinding(ctx, *sourceBinding)
	require.NoError(t, err)
	require.True(t, valid)

	type rebuildResult struct {
		binding *service.OpenAIUserConversationBinding
		created bool
		token   string
		err     error
	}
	results := make(chan rebuildResult, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			token := uuid.NewString()
			binding, wasCreated, reserveErr := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
				UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: responsesScope, ConversationHash: lineageHash,
				AccountID: targetAccount.ID, PlacementGeneration: 2, MaxResidentSlots: 2,
				ContextRebuildable: true, ProvisionalToken: token, Aliases: []service.OpenAIUserConversationAlias{alias}, Config: config,
			})
			results <- rebuildResult{binding: binding, created: wasCreated, token: token, err: reserveErr}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	createdCount := 0
	var leader rebuildResult
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.binding)
		require.Equal(t, responsesScope, result.binding.ScopeKey)
		require.Equal(t, targetAccount.ID, result.binding.AccountID)
		if result.created {
			createdCount++
			leader = result
		}
	}
	require.Equal(t, 1, createdCount)
	byLineage, err := repo.GetOpenAIUserConversationBindingByAlias(ctx, user.ID, apiKey.ID, lineageScope, "codex_thread", lineageHash)
	require.NoError(t, err)
	require.NotNil(t, byLineage)
	require.Equal(t, leader.binding.ID, byLineage.ID)

	rolledBack, err := repo.RollbackOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: leader.binding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: responsesScope,
		ConversationHash: lineageHash, ResidentSlotID: leader.binding.ResidentSlotID,
		AccountID: targetAccount.ID, SlotGeneration: leader.binding.SlotGeneration,
		ProvisionalToken: leader.token, Aliases: []service.OpenAIUserConversationAlias{alias}, Config: config,
	})
	require.NoError(t, err)
	require.True(t, rolledBack)
	byLineage, err = repo.GetOpenAIUserConversationBindingByAlias(ctx, user.ID, apiKey.ID, lineageScope, "codex_thread", lineageHash)
	require.NoError(t, err)
	require.Nil(t, byLineage, "首输出前失败不得留下指向 provisional binding 的 lineage alias")

	commitToken := uuid.NewString()
	committedBinding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: responsesScope, ConversationHash: lineageHash,
		AccountID: targetAccount.ID, PlacementGeneration: 3, MaxResidentSlots: 2,
		ContextRebuildable: true, ProvisionalToken: commitToken, Aliases: []service.OpenAIUserConversationAlias{alias}, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	committed, err := repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: committedBinding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: responsesScope,
		ConversationHash: lineageHash, ResidentSlotID: committedBinding.ResidentSlotID,
		AccountID: targetAccount.ID, SlotGeneration: committedBinding.SlotGeneration,
		ProvisionalToken: commitToken, Aliases: []service.OpenAIUserConversationAlias{alias}, Config: config,
	})
	require.NoError(t, err)
	require.True(t, committed)
	byLineage, err = repo.GetOpenAIUserConversationBindingByAlias(ctx, user.ID, apiKey.ID, lineageScope, "codex_thread", lineageHash)
	require.NoError(t, err)
	require.NotNil(t, byLineage)
	require.Equal(t, responsesScope, byLineage.ScopeKey)
	require.Equal(t, targetAccount.ID, byLineage.AccountID)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_user_resident_slots SET status = 'expired' WHERE id = $1`, sourceBinding.ResidentSlotID)
	require.NoError(t, err)
	valid, err = repo.ValidateOpenAIUserConversationBinding(ctx, *sourceBinding)
	require.NoError(t, err)
	require.True(t, valid, "常驻偏好过期不应使仍活跃的原绑定失效")
}

func TestCrossScopeLineageLateCommitCannotOverwriteNewOwner(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-lineage-late-commit-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-lineage-late-commit-" + uuid.NewString(), Name: "affinity",
	})
	sourceAccount := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-lineage-late-source-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	targetAccount := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-lineage-late-target-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	chatScope := "openai:v1:group:simple:lane:endpoint:chat_completions"
	responsesScope := "openai:v1:group:simple:lane:endpoint:responses"
	lineageScope := "openai:v1:group:simple:lineage:codex-thread"
	lineageHash := strings.Repeat("8", 64)
	alias := service.OpenAIUserConversationAlias{ScopeKey: lineageScope, Type: "codex_thread", Hash: lineageHash}

	reserve := func(scopeKey, conversationHash string, accountID, generation int64) (*service.OpenAIUserConversationBinding, string) {
		t.Helper()
		token := uuid.NewString()
		binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
			UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: conversationHash,
			AccountID: accountID, PlacementGeneration: generation, MaxResidentSlots: 2,
			ContextRebuildable: true, ProvisionalToken: token,
			Aliases: []service.OpenAIUserConversationAlias{alias}, Config: config,
		})
		require.NoError(t, err)
		require.True(t, created)
		return binding, token
	}
	transition := func(binding *service.OpenAIUserConversationBinding, token string) service.OpenAIUserConversationTransition {
		return service.OpenAIUserConversationTransition{
			BindingID: binding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: binding.ScopeKey,
			ConversationHash: binding.ConversationHash, ResidentSlotID: binding.ResidentSlotID,
			AccountID: binding.AccountID, SlotGeneration: binding.SlotGeneration,
			ProvisionalToken: token, Aliases: []service.OpenAIUserConversationAlias{alias}, Config: config,
		}
	}

	oldBinding, oldToken := reserve(chatScope, strings.Repeat("9", 64), sourceAccount.ID, 1)
	newBinding, newToken := reserve(responsesScope, lineageHash, targetAccount.ID, 2)
	committed, err := repo.CommitOpenAIUserConversationBinding(ctx, transition(newBinding, newToken))
	require.NoError(t, err)
	require.True(t, committed)

	committed, err = repo.CommitOpenAIUserConversationBinding(ctx, transition(oldBinding, oldToken))
	require.NoError(t, err)
	require.False(t, committed, "旧 scope 的晚到首输出不得覆盖已提交的新 lineage owner")
	byLineage, err := repo.GetOpenAIUserConversationBindingByAlias(ctx, user.ID, apiKey.ID, lineageScope, "codex_thread", lineageHash)
	require.NoError(t, err)
	require.NotNil(t, byLineage)
	require.Equal(t, newBinding.ID, byLineage.ID)
	oldLoaded, err := repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, chatScope, oldBinding.ConversationHash)
	require.NoError(t, err)
	require.NotNil(t, oldLoaded)
	require.Equal(t, "provisional", oldLoaded.Status)
	require.False(t, oldLoaded.FirstOutputCommitted)
}

// 会话级切号提交前必须保留源绑定，失败回滚和成功提交都不能搬迁其他会话。
func TestOpenAIUserConversationFailoverLifecycle(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-failover-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-failover-" + uuid.NewString(), Name: "affinity",
	})
	sourceAccount := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-failover-source-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	targetAccount := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-failover-target-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	scopeKey := "openai:v1:group:simple:lane:conversation-failover"
	reserveAndCommit := func(account *service.Account, conversationHash string, generation int64) *service.OpenAIUserConversationBinding {
		token := uuid.NewString()
		binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
			UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
			ConversationHash: conversationHash, AccountID: account.ID,
			PlacementGeneration: generation, MaxResidentSlots: 2, ContextRebuildable: true,
			ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		require.True(t, created)
		committed, err := repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
			BindingID: binding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
			ConversationHash: conversationHash, ResidentSlotID: binding.ResidentSlotID,
			AccountID: account.ID, SlotGeneration: binding.SlotGeneration,
			ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		require.True(t, committed)
		return binding
	}
	sourceHash := strings.Repeat("4", 64)
	targetHash := strings.Repeat("5", 64)
	sourceBinding := reserveAndCommit(sourceAccount, sourceHash, 1)
	targetBinding := reserveAndCommit(targetAccount, targetHash, 2)

	reserveFailover := func(token string) *service.OpenAIUserConversationTransition {
		transition, reserved, err := repo.ReserveOpenAIUserConversationFailover(ctx, service.OpenAIUserConversationFailoverReservation{
			BindingID: sourceBinding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: sourceHash,
			SourceAccountID: sourceAccount.ID, SourceResidentSlotID: sourceBinding.ResidentSlotID,
			SourceSlotGeneration: sourceBinding.SlotGeneration,
			TargetAccountID:      targetAccount.ID, TargetResidentSlotID: targetBinding.ResidentSlotID,
			TargetSlotGeneration: targetBinding.SlotGeneration, ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		require.True(t, reserved)
		require.NotNil(t, transition)
		return transition
	}
	rollbackToken := uuid.NewString()
	rollbackTransition := reserveFailover(rollbackToken)
	loaded, err := repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, sourceHash)
	require.NoError(t, err)
	require.Equal(t, sourceAccount.ID, loaded.AccountID, "pending 期间不得覆盖源绑定")
	rolledBack, err := repo.RollbackOpenAIUserConversationBinding(ctx, *rollbackTransition)
	require.NoError(t, err)
	require.True(t, rolledBack)
	loaded, err = repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, sourceHash)
	require.NoError(t, err)
	require.Equal(t, sourceAccount.ID, loaded.AccountID)

	commitTransition := reserveFailover(uuid.NewString())
	commitTransition.ResponseAliasHash = strings.Repeat("f", 64)
	committed, err := repo.CommitOpenAIUserConversationBinding(ctx, *commitTransition)
	require.NoError(t, err)
	require.True(t, committed)
	loaded, err = repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, sourceHash)
	require.NoError(t, err)
	require.Equal(t, targetAccount.ID, loaded.AccountID)
	require.Equal(t, targetBinding.ResidentSlotID, loaded.ResidentSlotID)
	byResponseAlias, err := repo.GetOpenAIUserConversationBindingByAlias(
		ctx, user.ID, apiKey.ID, scopeKey, "response_id", commitTransition.ResponseAliasHash,
	)
	require.NoError(t, err)
	require.NotNil(t, byResponseAlias)
	require.Equal(t, targetAccount.ID, byResponseAlias.AccountID)
	other, err := repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, targetHash)
	require.NoError(t, err)
	require.Equal(t, targetAccount.ID, other.AccountID, "切号不得修改目标槽位中的其他会话")

	var sourceSlotStatus string
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT status FROM openai_user_resident_slots WHERE id = $1`,
		[]any{sourceBinding.ResidentSlotID}, &sourceSlotStatus))
	require.Equal(t, service.OpenAIUserResidentSlotStatusActive, sourceSlotStatus)
}

// 槽位替换失败恢复 victim，成功后只把 victim 转为 draining。
func TestOpenAIUserResidentSlotReplacementLifecycle(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-replacement-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-replacement-" + uuid.NewString(), Name: "affinity",
	})
	accounts := make([]*service.Account, 0, 3)
	for i := 0; i < 3; i++ {
		accounts = append(accounts, mustCreateAccount(t, client, &service.Account{
			Name: "openai-affinity-replacement-" + uuid.NewString(), Platform: service.PlatformOpenAI,
		}))
	}
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	scopeKey := "openai:v1:group:simple:lane:slot-replacement"
	createBinding := func(account *service.Account, hash string, generation int64) *service.OpenAIUserConversationBinding {
		token := uuid.NewString()
		binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
			UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: hash,
			AccountID: account.ID, PlacementGeneration: generation, MaxResidentSlots: 2,
			ContextRebuildable: true, ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		require.True(t, created)
		_, err = repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
			BindingID: binding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: hash,
			ResidentSlotID: binding.ResidentSlotID, AccountID: account.ID,
			SlotGeneration: binding.SlotGeneration, ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		return binding
	}
	sourceHash, victimHash := strings.Repeat("8", 64), strings.Repeat("9", 64)
	sourceBinding := createBinding(accounts[0], sourceHash, 1)
	victimBinding := createBinding(accounts[1], victimHash, 2)
	// target 已在其他 scope 居住时应复用，而不是被误判为不可迁入。
	crossScopeToken := uuid.NewString()
	crossScopeBinding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey + ":other", ConversationHash: strings.Repeat("a", 64),
		AccountID: accounts[2].ID, PlacementGeneration: 1, MaxResidentSlots: 1,
		ContextRebuildable: true, ProvisionalToken: crossScopeToken, Config: config,
	})
	require.NoError(t, err)
	require.True(t, created)
	_, err = repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
		BindingID: crossScopeBinding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: crossScopeBinding.ScopeKey,
		ConversationHash: crossScopeBinding.ConversationHash, ResidentSlotID: crossScopeBinding.ResidentSlotID,
		AccountID: accounts[2].ID, SlotGeneration: crossScopeBinding.SlotGeneration,
		ProvisionalToken: crossScopeToken, Config: config,
	})
	require.NoError(t, err)
	checked := []service.OpenAIUserResidentSlotVersion{
		{ID: sourceBinding.ResidentSlotID, AccountID: accounts[0].ID, Generation: sourceBinding.SlotGeneration},
		{ID: victimBinding.ResidentSlotID, AccountID: accounts[1].ID, Generation: victimBinding.SlotGeneration},
	}
	reserveReplacement := func(token string) *service.OpenAIUserConversationTransition {
		transition, reserved, err := repo.ReserveOpenAIUserResidentSlotReplacement(ctx, service.OpenAIUserResidentSlotReplacementReservation{
			BindingID: sourceBinding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: sourceHash,
			SourceAccountID: accounts[0].ID, SourceResidentSlotID: sourceBinding.ResidentSlotID,
			SourceSlotGeneration: sourceBinding.SlotGeneration, VictimSlotID: victimBinding.ResidentSlotID,
			TargetAccountID: accounts[2].ID, CheckedSlots: checked, ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		require.True(t, reserved)
		require.NotNil(t, transition)
		return transition
	}

	rollbackTransition := reserveReplacement(uuid.NewString())
	var victimStatus, targetStatus string
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT status FROM openai_user_resident_slots WHERE id = $1`,
		[]any{victimBinding.ResidentSlotID}, &victimStatus))
	require.Equal(t, service.OpenAIUserResidentSlotStatusReplacementPending, victimStatus)
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT status FROM openai_user_resident_slots WHERE id = $1`,
		[]any{rollbackTransition.ResidentSlotID}, &targetStatus))
	require.Equal(t, service.OpenAIUserResidentSlotStatusProvisional, targetStatus)
	rolledBack, err := repo.RollbackOpenAIUserConversationBinding(ctx, *rollbackTransition)
	require.NoError(t, err)
	require.True(t, rolledBack)
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT status FROM openai_user_resident_slots WHERE id = $1`,
		[]any{victimBinding.ResidentSlotID}, &victimStatus))
	require.Equal(t, service.OpenAIUserResidentSlotStatusActive, victimStatus)
	var targetCount int
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT COUNT(*) FROM openai_user_resident_slots WHERE id = $1`,
		[]any{rollbackTransition.ResidentSlotID}, &targetCount))
	require.Zero(t, targetCount)

	commitTransition := reserveReplacement(uuid.NewString())
	committed, err := repo.CommitOpenAIUserConversationBinding(ctx, *commitTransition)
	require.NoError(t, err)
	require.True(t, committed)
	loaded, err := repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, sourceHash)
	require.NoError(t, err)
	require.Equal(t, accounts[2].ID, loaded.AccountID)
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT status FROM openai_user_resident_slots WHERE id = $1`,
		[]any{victimBinding.ResidentSlotID}, &victimStatus))
	require.Equal(t, service.OpenAIUserResidentSlotStatusDraining, victimStatus)
	other, err := repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, victimHash)
	require.NoError(t, err)
	require.Equal(t, "draining", other.Status)

	reducedConfig := config
	reducedConfig.ResidentAccountSlotCount = 1
	require.NoError(t, repo.ConvergeOpenAIUserResidentSlots(ctx, user.ID, scopeKey, reducedConfig, time.Now().UTC()))
	var activeCount, drainingCount int
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT COUNT(*) FILTER (WHERE status = 'active'), COUNT(*) FILTER (WHERE status = 'draining')
		FROM openai_user_resident_slots WHERE user_id = $1 AND scope_key = $2`,
		[]any{user.ID, scopeKey}, &activeCount, &drainingCount))
	require.Equal(t, 1, activeCount)
	require.Equal(t, 2, drainingCount)
	loaded, err = repo.GetOpenAIUserConversationBinding(ctx, user.ID, apiKey.ID, scopeKey, sourceHash)
	require.NoError(t, err)
	require.Equal(t, "draining", loaded.Status, "减槽只排空低热度槽，不批量搬迁会话")
}

// 同一槽位的不同会话必须分别累计容量事故。
func TestOpenAIUserCapacityIncidentsAreIsolatedByConversation(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-incident-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-incident-" + uuid.NewString(), Name: "affinity",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "openai-affinity-incident-" + uuid.NewString(), Platform: service.PlatformOpenAI,
	})
	config := service.DefaultOpenAIUserAffinityConfig()
	config.CapacityFailureMigrationThreshold = 2
	scopeKey := "openai:v1:group:simple:lane:incident-isolation"
	createBinding := func(hash string) *service.OpenAIUserConversationBinding {
		token := uuid.NewString()
		binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
			UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: hash,
			AccountID: account.ID, PlacementGeneration: 1, MaxResidentSlots: 1,
			ContextRebuildable: true, ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		require.True(t, created)
		_, err = repo.CommitOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationTransition{
			BindingID: binding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey, ConversationHash: hash,
			ResidentSlotID: binding.ResidentSlotID, AccountID: account.ID,
			SlotGeneration: binding.SlotGeneration, ProvisionalToken: token, Config: config,
		})
		require.NoError(t, err)
		return binding
	}
	hashA, hashB := strings.Repeat("6", 64), strings.Repeat("7", 64)
	bindingA := createBinding(hashA)
	bindingB := createBinding(hashB)
	for index, current := range []struct {
		hash    string
		binding *service.OpenAIUserConversationBinding
	}{{hashA, bindingA}, {hashB, bindingB}} {
		_, err := repo.RecordOpenAIUserAffinityCapacityFailure(ctx, service.OpenAIUserAffinityIncidentIdentity{
			UserID: user.ID, AccountID: account.ID, ScopeKey: scopeKey,
			PlacementGeneration: current.binding.SlotGeneration, ConversationHash: current.hash,
			ResidentSlotID: current.binding.ResidentSlotID, SlotGeneration: current.binding.SlotGeneration,
		}, strings.Repeat(string(rune('a'+index)), 64), "concurrency_unavailable", config)
		require.NoError(t, err)
	}
	var incidentCount, totalFailures int
	require.NoError(t, scanSingleRow(ctx, tx, `
		SELECT COUNT(*), COALESCE(SUM(failure_count), 0) FROM user_account_capacity_incidents
		WHERE user_id = $1 AND scope_key = $2 AND source_account_id = $3`,
		[]any{user.ID, scopeKey, account.ID}, &incidentCount, &totalFailures))
	require.Equal(t, 2, incidentCount)
	require.Equal(t, 2, totalFailures)
}

// 回填必须可重复执行，且只能把仍有效的单归属投影成槽位 1。
func TestOpenAIUserAffinityMultiSlotMigrationBackfillIsIdempotent(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	user := mustCreateUser(t, client, &service.User{})
	account := mustCreateAccount(t, client, &service.Account{
		Name:     "openai-affinity-slot-backfill-" + time.Now().Format(time.RFC3339Nano),
		Platform: service.PlatformOpenAI,
	})
	scopeKey := "openai:v1:group:1:lane:general"
	now := time.Now().UTC()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_account_placements
			(user_id, scope_key, account_id, generation, status, assigned_at, last_active_at, expires_at)
		VALUES ($1, $2, $3, 4, 'active', $4, NULL, $5)`,
		user.ID, scopeKey, account.ID, now, now.Add(time.Hour))
	require.NoError(t, err)

	migrationSQL, err := migrations.FS.ReadFile("235_openai_user_affinity_multi_slot_foundation.sql")
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(migrationSQL))
	require.NoError(t, err)

	var count, slotIndex int
	var generation int64
	var lastSuccess sql.NullTime
	err = scanSingleRow(ctx, tx, `
		SELECT COUNT(*), MIN(slot_index), MIN(generation), MAX(last_success_at)
		FROM openai_user_resident_slots
		WHERE user_id = $1 AND scope_key = $2 AND account_id = $3`,
		[]any{user.ID, scopeKey, account.ID}, &count, &slotIndex, &generation, &lastSuccess)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	require.Equal(t, 1, slotIndex)
	require.Equal(t, int64(4), generation)
	require.False(t, lastSuccess.Valid)
}

// 管理员整组重置必须排除全部原槽位，同时保留已提交会话的严格续链。
func TestResetOpenAIUserAffinityScopePreservesBindingsAndConsumesExclusions(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{
		Email: "affinity-reset-" + uuid.NewString() + "@example.com", PasswordHash: "hash",
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-affinity-reset-" + uuid.NewString(), Name: "affinity-reset",
	})
	accounts := make([]*service.Account, 0, 3)
	for i := 0; i < 3; i++ {
		accounts = append(accounts, mustCreateAccount(t, client, &service.Account{
			Name: "openai-affinity-reset-" + uuid.NewString(), Platform: service.PlatformOpenAI,
		}))
	}
	config := service.DefaultOpenAIUserAffinityConfig()
	config.ResidentAccountSlotCount = 2
	scopeKey := "openai:v1:group:reset:lane:general"

	reserveAndCommit := func(account *service.Account, hashChar string, generation int64) service.OpenAIUserConversationTransition {
		token := uuid.NewString()
		binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
			UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
			ConversationHash: strings.Repeat(hashChar, 64), AccountID: account.ID,
			PlacementGeneration: generation, MaxResidentSlots: 2, ContextRebuildable: true,
			ProvisionalToken: token, ManageActiveRoute: true, Config: config,
		})
		require.NoError(t, err)
		require.True(t, created)
		transition := service.OpenAIUserConversationTransition{
			BindingID: binding.ID, UserID: user.ID, APIKeyID: apiKey.ID, ScopeKey: scopeKey,
			ConversationHash: binding.ConversationHash, ResidentSlotID: binding.ResidentSlotID,
			AccountID: binding.AccountID, SlotGeneration: binding.SlotGeneration,
			ProvisionalToken: token, ManageActiveRoute: binding.ManageActiveRoute,
			ActiveRoutePending: binding.ActiveRoutePending, Config: config,
		}
		firstCommit, err := repo.CommitOpenAIUserConversationBinding(ctx, transition)
		require.NoError(t, err)
		require.True(t, firstCommit)
		return transition
	}
	first := reserveAndCommit(accounts[0], "8", 1)
	_ = reserveAndCommit(accounts[1], "9", 2)

	require.NoError(t, repo.ResetOpenAIUserAffinityPlacement(ctx, user.ID, user.ID, scopeKey, true))
	var resetSlots, drainingBindings, pendingExclusions, activeRoutes int
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT COUNT(*) FROM openai_user_resident_slots
		WHERE user_id = $1 AND scope_key = $2 AND status = 'reset'`, []any{user.ID, scopeKey}, &resetSlots))
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT COUNT(*) FROM openai_user_conversation_bindings
		WHERE user_id = $1 AND scope_key = $2 AND status = 'draining'`, []any{user.ID, scopeKey}, &drainingBindings))
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT COUNT(*) FROM openai_user_affinity_reset_exclusions
		WHERE user_id = $1 AND scope_key = $2 AND consumed_at IS NULL`, []any{user.ID, scopeKey}, &pendingExclusions))
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT COUNT(*) FROM openai_user_active_routes
		WHERE user_id = $1 AND scope_key = $2`, []any{user.ID, scopeKey}, &activeRoutes))
	require.Equal(t, 2, resetSlots)
	require.Equal(t, 2, drainingBindings)
	require.Equal(t, 2, pendingExclusions)
	require.Zero(t, activeRoutes)
	excluded, err := repo.ListOpenAIUserAffinityResetExcludedAccountIDs(ctx, user.ID, scopeKey)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{accounts[0].ID, accounts[1].ID}, excluded)

	first.ProvisionalToken = ""
	firstCommit, err := repo.CommitOpenAIUserConversationBinding(ctx, first)
	require.NoError(t, err)
	require.False(t, firstCommit)

	_ = reserveAndCommit(accounts[2], "a", 3)
	require.NoError(t, scanSingleRow(ctx, tx, `SELECT COUNT(*) FROM openai_user_affinity_reset_exclusions
		WHERE user_id = $1 AND scope_key = $2 AND consumed_at IS NULL`, []any{user.ID, scopeKey}, &pendingExclusions))
	require.Zero(t, pendingExclusions)
}
