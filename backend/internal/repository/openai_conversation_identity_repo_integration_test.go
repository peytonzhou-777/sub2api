//go:build integration

package repository

import (
	"context"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConversationIdentityIdleRestoresWithoutResidentPreference(t *testing.T) {
	repo, tr := conversationActivityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := repo.sql.ExecContext(ctx, "UPDATE openai_user_conversation_bindings SET active_until=NULL::timestamptz,last_success_at=$2::timestamptz,expires_at=$3::timestamptz WHERE id=$1", tr.BindingID, now.Add(-2*time.Hour), now.Add(5*24*time.Hour))
	require.NoError(t, err)
	_, err = repo.sql.ExecContext(ctx, "UPDATE openai_user_resident_slots SET status='expired',expires_at=$2::timestamptz WHERE id=$1", tr.ResidentSlotID, now.Add(-time.Hour))
	require.NoError(t, err)
	require.NoError(t, repo.ConvergeOpenAIUserResidentSlots(ctx, tr.UserID, tr.ScopeKey, tr.Config, now))
	b, err := repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
	require.NoError(t, err)
	require.NotNil(t, b)
	valid, err := repo.ValidateOpenAIUserConversationBinding(ctx, *b)
	require.NoError(t, err)
	require.True(t, valid)
	expired, err := repo.HasExpiredOpenAIConversation(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash, nil)
	require.NoError(t, err)
	require.False(t, expired)
	token := uuid.NewString()
	held, err := repo.AcquireOpenAIConversationActivity(ctx, tr, token, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, held)
	touched, err := repo.TouchOpenAIConversationActivity(ctx, tr, now)
	require.NoError(t, err)
	require.True(t, touched)
	require.NoError(t, repo.ReleaseOpenAIConversationActivity(ctx, token))
	b, err = repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
	require.NoError(t, err)
	require.Equal(t, tr.AccountID, b.AccountID)
	require.Equal(t, tr.SlotGeneration, b.SlotGeneration)
	require.WithinDuration(t, now.Add(tr.Config.ConversationActiveTTL()), *b.ActiveUntil, time.Second)
	require.WithinDuration(t, now.Add(tr.Config.ConversationIdentityTTL()), b.ExpiresAt, time.Second)
}

func TestConversationIdentityConcurrentCrossLaneReservation(t *testing.T) {
	f := newOpenAIReservationFixture(t)
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	ctx := context.Background()
	cfg := service.DefaultOpenAIUserAffinityConfig()
	alias := service.OpenAIUserConversationAlias{ScopeKey: fmt.Sprintf("openai:v1:group:%d:lineage:codex-thread", f.groupID), Type: "codex_thread", Hash: strings.Repeat("c", 64)}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]*service.OpenAIUserConversationBinding, 2)
	created := make([]bool, 2)
	errs := make([]error, 2)
	for i, lane := range []string{"general", "general:transport:responses_websockets_v2_ingress"} {
		wg.Add(1)
		go func(i int, lane string) {
			defer wg.Done()
			<-start
			results[i], created[i], errs[i] = repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.account.ID, ScopeKey: fmt.Sprintf("openai:v1:group:%d:lane:%s", f.groupID, lane), ConversationHash: strings.Repeat("c", 64), PlacementGeneration: 1, ProvisionalToken: uuid.NewString(), ContextRebuildable: true, Config: cfg, Aliases: []service.OpenAIUserConversationAlias{alias}})
		}(i, lane)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.NotNil(t, results[0])
	require.NotNil(t, results[1])
	require.Equal(t, results[0].ID, results[1].ID)
	require.NotEqual(t, created[0], created[1], "只能一个请求建立权威 binding")
	require.Equal(t, results[0].ScopeKey, results[1].ScopeKey)
}

func TestConversationIdentityMigrationKeepsExpiredRecordsClosed(t *testing.T) {
	repo, tr := conversationActivityFixture(t)
	ctx := context.Background()
	// 在事务内恢复旧版 live 定义，实跑迁移，测试结束自动回滚 DDL。
	_, err := repo.sql.ExecContext(ctx, "CREATE OR REPLACE FUNCTION openai_conversation_is_live(target_id BIGINT, active_deadline TIMESTAMPTZ, deadline TIMESTAMPTZ) RETURNS BOOLEAN LANGUAGE SQL STABLE AS $$ SELECT LEAST(COALESCE(active_deadline,deadline),deadline)>NOW() OR openai_conversation_has_activity(target_id) $$")
	require.NoError(t, err)
	_, err = repo.sql.ExecContext(ctx, "UPDATE openai_user_conversation_bindings SET active_until=NOW()+INTERVAL '30 minutes',expires_at=NOW()+INTERVAL '30 minutes' WHERE id=$1", tr.BindingID)
	require.NoError(t, err)
	// 同一事务创建一个显式过期身份，验证迁移不会靠延长时间复活它。
	var expiredID int64
	require.NoError(t, scanSingleRow(ctx, repo.sql, "INSERT INTO openai_user_conversation_bindings(user_id,api_key_id,scope_key,conversation_hash,resident_slot_id,account_id,slot_generation,binding_epoch,status,context_rebuildable,first_output_committed,active_until,expires_at) SELECT user_id,api_key_id,scope_key,$2::char(64),resident_slot_id,account_id,slot_generation,binding_epoch,'expired',context_rebuildable,TRUE,NOW()-INTERVAL '1 hour',NOW()-INTERVAL '1 hour' FROM openai_user_conversation_bindings WHERE id=$1 RETURNING id", []any{tr.BindingID, strings.Repeat("e", 64)}, &expiredID))
	body, err := migrations.FS.ReadFile("262_openai_conversation_identity_lifetime.sql")
	require.NoError(t, err)
	_, err = repo.sql.ExecContext(ctx, string(body))
	require.NoError(t, err)
	var preserved, extended bool
	require.NoError(t, scanSingleRow(ctx, repo.sql, "SELECT status='expired' AND expires_at<NOW() FROM openai_user_conversation_bindings WHERE id=$1", []any{expiredID}, &preserved))
	require.True(t, preserved)
	require.NoError(t, scanSingleRow(ctx, repo.sql, "SELECT expires_at>NOW()+INTERVAL '6 days' FROM openai_user_conversation_bindings WHERE id=$1", []any{tr.BindingID}, &extended))
	require.True(t, extended)
	_, err = repo.sql.ExecContext(ctx, "UPDATE openai_user_conversation_bindings SET active_until=NOW()-INTERVAL '1 hour' WHERE id=$1", tr.BindingID)
	require.NoError(t, err)
	b, err := repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
	require.NoError(t, err)
	require.NotNil(t, b)
}

// TestConversationIdentityDormantUserReacquiresCapacity 确认身份保留不等于永久占用 Persona。
func TestConversationIdentityDormantUserReacquiresCapacity(t *testing.T) {
	ctx := context.Background()
	f := newOpenAIReservationFixture(t)
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	capacity := NewOpenAIPersonaUserReservationRepository(integrationDB)
	cfg := service.DefaultOpenAIUserAffinityConfig()
	scope := fmt.Sprintf("openai:v1:group:%d:lane:general", f.groupID)
	token := uuid.NewString()
	b, _, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.account.ID, ScopeKey: scope, ConversationHash: strings.Repeat("d", 64), PlacementGeneration: 1, ProvisionalToken: token, ContextRebuildable: true, Config: cfg})
	require.NoError(t, err)
	tr := service.OpenAIUserConversationTransition{BindingID: b.ID, UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.account.ID, ScopeKey: scope, ConversationHash: b.ConversationHash, ResidentSlotID: b.ResidentSlotID, SlotGeneration: b.SlotGeneration, BindingEpoch: service.OpenAIConversationBindingEpoch, ProvisionalToken: token, Config: cfg}
	session, err := NewOpenAIAccountPersonaRepository(integrationDB).GetAccountPersonaSession(ctx, f.account.ID, f.persona.ID, f.persona.CurrentSessionEpoch, time.Now())
	require.NoError(t, err)
	target, err := service.OpenAIExecutionTargetFromPersonaSession(f.persona, *session)
	require.NoError(t, err)
	require.NoError(t, repo.BindOpenAIUserConversationExecutionTarget(ctx, tr, target))
	_, err = repo.CommitOpenAIUserConversationBinding(ctx, tr)
	require.NoError(t, err)
	now := time.Now().UTC()
	input := service.OpenAIPersonaUserReserveInput{ReservationToken: uuid.NewString(), AccountID: f.account.ID, AccountPersonaID: f.persona.ID, UserID: f.userID, MaxUsers: 1, Now: now, HoldUntil: now.Add(time.Minute), ExistingThread: true}
	lease, err := capacity.ReservePersonaUser(ctx, input)
	require.NoError(t, err)
	_, err = capacity.CommitPersonaUserReservation(ctx, service.OpenAIPersonaUserReservationCommit{ReservationToken: input.ReservationToken, Now: now, ActiveUntil: now.Add(time.Minute)})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE openai_persona_active_user_leases SET active_until=NOW()-INTERVAL '1 hour' WHERE id=$1", lease.LeaseID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, "UPDATE openai_user_conversation_bindings SET active_until=NOW()-INTERVAL '1 hour' WHERE id=$1", b.ID)
	require.NoError(t, err)
	other := createOpenAIReservationUser(t, "identity-capacity-other")
	otherInput := input
	otherInput.ReservationToken = uuid.NewString()
	otherInput.UserID = other
	otherInput.ExistingThread = false
	_, err = capacity.ReservePersonaUser(ctx, otherInput)
	require.NoError(t, err)
	input.ReservationToken = uuid.NewString()
	_, err = capacity.ReservePersonaUser(ctx, input)
	require.ErrorIs(t, err, service.ErrOpenAIPersonaUserCapacity)
	restored, err := repo.GetOpenAIUserConversationBinding(ctx, f.userID, f.apiKeyID, scope, b.ConversationHash)
	require.NoError(t, err)
	require.NotNil(t, restored)
	require.Equal(t, f.persona.ID, restored.AccountPersonaID)
	require.NoError(t, capacity.RollbackPersonaUserReservation(ctx, otherInput.ReservationToken, time.Now()))
	input.ReservationToken = uuid.NewString()
	_, err = capacity.ReservePersonaUser(ctx, input)
	require.NoError(t, err)
	require.NoError(t, capacity.RollbackPersonaUserReservation(ctx, input.ReservationToken, time.Now()))
}

// TestConversationIdentityChildUsesHistoricalParentSlot 不把父线程的历史偏好当作新居民名额。
func TestConversationIdentityChildUsesHistoricalParentSlot(t *testing.T) {
	repo, tr := conversationActivityFixture(t)
	ctx := context.Background()
	_, err := repo.sql.ExecContext(ctx, "UPDATE openai_user_resident_slots SET status='expired',expires_at=NOW()-INTERVAL '1 hour' WHERE id=$1", tr.ResidentSlotID)
	require.NoError(t, err)
	child, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{UserID: tr.UserID, APIKeyID: tr.APIKeyID, ScopeKey: tr.ScopeKey, ConversationHash: strings.Repeat("f", 64), AccountID: tr.AccountID, PlacementGeneration: tr.SlotGeneration, PreferredResidentSlotID: tr.ResidentSlotID, PreferredSlotGeneration: tr.SlotGeneration, ProvisionalToken: uuid.NewString(), Config: tr.Config})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, tr.ResidentSlotID, child.ResidentSlotID)
	require.Equal(t, tr.SlotGeneration, child.SlotGeneration)
	var state string
	require.NoError(t, scanSingleRow(ctx, repo.sql, "SELECT status FROM openai_user_resident_slots WHERE id=$1", []any{tr.ResidentSlotID}, &state))
	require.Equal(t, "expired", state)
}
