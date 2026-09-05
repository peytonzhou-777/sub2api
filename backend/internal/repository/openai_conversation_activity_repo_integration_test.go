//go:build integration

package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// conversationActivityFixture 使用真实 PostgreSQL 覆盖空 Persona 字段与 NULL 活跃时间。
func conversationActivityFixture(t *testing.T) (*accountRepository, service.OpenAIUserConversationTransition) {
	t.Helper()
	ctx := context.Background()
	tx := testEntTx(t)
	client := tx.Client()
	repo := newAccountRepositoryWithSQL(client, tx, nil)
	user := mustCreateUser(t, client, &service.User{Email: uuid.NewString() + "@example.com", PasswordHash: "hash"})
	key := mustCreateApiKey(t, client, &service.APIKey{UserID: user.ID, Key: "sk-" + uuid.NewString(), Name: "activity"})
	account := mustCreateAccount(t, client, &service.Account{Name: uuid.NewString(), Platform: service.PlatformOpenAI})
	cfg := service.DefaultOpenAIUserAffinityConfig()
	cfg.ConversationActiveTTLSeconds = 2400
	cfg.ResidentTTLSeconds = 259200
	scope := "openai:v1:group:simple:lane:general"
	_, err := tx.ExecContext(ctx, `INSERT INTO user_account_placements(user_id,scope_key,account_id,generation,status,expires_at)
 VALUES($1,$2,$3,1,'active',$4::timestamptz)`, user.ID, scope, account.ID, time.Now().Add(cfg.ResidentTTL()))
	require.NoError(t, err)
	token := uuid.NewString()
	binding, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: user.ID, APIKeyID: key.ID, ScopeKey: scope, ConversationHash: strings.Repeat("a", 64), AccountID: account.ID,
		PlacementGeneration: 1, ContextRebuildable: true, ProvisionalToken: token, Config: cfg,
		Aliases: []service.OpenAIUserConversationAlias{{ScopeKey: scope, Type: "codex_thread", Hash: strings.Repeat("b", 64)}},
	})
	require.NoError(t, err)
	require.True(t, created)
	tr := service.OpenAIUserConversationTransition{BindingID: binding.ID, UserID: user.ID, APIKeyID: key.ID, ScopeKey: scope,
		ConversationHash: binding.ConversationHash, ResidentSlotID: binding.ResidentSlotID, AccountID: account.ID,
		SlotGeneration: binding.SlotGeneration, BindingEpoch: service.OpenAIConversationBindingEpoch, ProvisionalToken: token, Config: cfg}
	first, err := repo.CommitOpenAIUserConversationBinding(ctx, tr)
	require.NoError(t, err)
	require.True(t, first)
	tr.ProvisionalToken = ""
	return repo, tr
}

func TestConversationActivityTTLAndInFlightProtection(t *testing.T) {
	repo, tr := conversationActivityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	load := func() *service.OpenAIUserConversationBinding {
		b, e := repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
		require.NoError(t, e)
		return b
	}
	binding := load()
	require.NotNil(t, binding)
	require.WithinDuration(t, now.Add(40*time.Minute), *binding.ActiveUntil, 5*time.Second)
	require.WithinDuration(t, now.Add(tr.Config.ConversationIdentityTTL()), binding.ExpiresAt, 5*time.Second)
	token := uuid.NewString()
	acquired, err := repo.AcquireOpenAIConversationActivity(ctx, tr, token, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = repo.sql.ExecContext(ctx, `UPDATE openai_user_conversation_bindings SET active_until=$2::timestamptz,expires_at=$2::timestamptz,last_success_at=$2::timestamptz WHERE id=$1`, tr.BindingID, now.Add(-time.Hour))
	require.NoError(t, err)
	_, err = repo.sql.ExecContext(ctx, `UPDATE openai_user_conversation_aliases SET expires_at=$2::timestamptz WHERE binding_id=$1`, tr.BindingID, now.Add(-time.Hour))
	require.NoError(t, err)
	require.NotNil(t, load(), "在途请求即使跨过活跃截止时间仍保留原绑定")
	byAlias, err := repo.GetOpenAIUserConversationBindingByAlias(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, "codex_thread", strings.Repeat("b", 64))
	require.NoError(t, err)
	require.NotNil(t, byAlias)
	reserved, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: tr.UserID, APIKeyID: tr.APIKeyID, ScopeKey: tr.ScopeKey, ConversationHash: tr.ConversationHash,
		AccountID: tr.AccountID, PlacementGeneration: tr.SlotGeneration, ProvisionalToken: uuid.NewString(), Config: tr.Config,
		Aliases: []service.OpenAIUserConversationAlias{{ScopeKey: tr.ScopeKey, Type: "codex_thread", Hash: strings.Repeat("b", 64)}},
	})
	require.NoError(t, err)
	require.False(t, created, "在途身份不能因原截止时间到期被重新预留")
	require.Equal(t, tr.BindingID, reserved.ID)
	require.NoError(t, repo.ConvergeOpenAIUserResidentSlots(ctx, tr.UserID, tr.ScopeKey, tr.Config, now))
	alive, err := repo.RenewOpenAIConversationActivity(ctx, token, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, alive)
	_, err = repo.sql.ExecContext(ctx, `UPDATE openai_user_resident_slots SET status='expired',expires_at=$2::timestamptz WHERE id=$1`, tr.ResidentSlotID, now.Add(-time.Hour))
	require.NoError(t, err)
	valid, err := repo.ValidateOpenAIUserConversationBinding(ctx, *load())
	require.NoError(t, err)
	require.True(t, valid, "常驻偏好过期不能破坏在途绑定")
	_, err = repo.CommitOpenAIUserConversationBinding(ctx, tr)
	require.NoError(t, err)
	require.NoError(t, repo.ReleaseOpenAIConversationActivity(ctx, token))
	binding = load()
	require.NotNil(t, binding)
	require.WithinDuration(t, time.Now().Add(tr.Config.ConversationActiveTTL()), *binding.ActiveUntil, 5*time.Second)
	require.WithinDuration(t, time.Now().Add(tr.Config.ConversationIdentityTTL()), binding.ExpiresAt, 5*time.Second)
}

func TestConversationActivityHoldCannotProtectChangedGeneration(t *testing.T) {
	repo, tr := conversationActivityFixture(t)
	ctx := context.Background()
	token := uuid.NewString()
	held, err := repo.AcquireOpenAIConversationActivity(ctx, tr, token, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.True(t, held)
	_, err = repo.sql.ExecContext(ctx, `UPDATE openai_user_conversation_bindings SET slot_generation=slot_generation+1,active_until=NOW()-INTERVAL '1 hour',expires_at=NOW()-INTERVAL '1 hour' WHERE id=$1`, tr.BindingID)
	require.NoError(t, err)
	b, err := repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
	require.NoError(t, err)
	require.Nil(t, b)
	renewed, err := repo.RenewOpenAIConversationActivity(ctx, token, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, renewed)
	require.NoError(t, repo.ReleaseOpenAIConversationActivity(ctx, token))
}

func TestConversationActivityExpiredIdentityCannotResurrect(t *testing.T) {
	repo, tr := conversationActivityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := repo.sql.ExecContext(ctx, `UPDATE openai_user_conversation_bindings SET active_until=$2::timestamptz,expires_at=$3::timestamptz WHERE id=$1`, tr.BindingID, now.Add(-time.Hour), now.Add(-time.Hour))
	require.NoError(t, err)
	b, err := repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
	require.NoError(t, err)
	require.Nil(t, b)
	expired, err := repo.HasExpiredOpenAIConversation(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash, nil)
	require.NoError(t, err)
	require.True(t, expired)
	expired, err = repo.HasExpiredOpenAIConversation(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, "", []service.OpenAIUserConversationAlias{{ScopeKey: tr.ScopeKey, Type: "codex_thread", Hash: strings.Repeat("b", 64)}})
	require.NoError(t, err)
	require.True(t, expired)
	expired, err = repo.HasExpiredOpenAIConversation(ctx, tr.UserID, tr.APIKeyID+10000, tr.ScopeKey, tr.ConversationHash, nil)
	require.NoError(t, err)
	require.False(t, expired)
	acquired, err := repo.AcquireOpenAIConversationActivity(ctx, tr, uuid.NewString(), now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, acquired)
	touched, err := repo.TouchOpenAIConversationActivity(ctx, tr, now)
	require.NoError(t, err)
	require.False(t, touched)
	_, _, err = repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{UserID: tr.UserID, APIKeyID: tr.APIKeyID, ScopeKey: tr.ScopeKey, ConversationHash: tr.ConversationHash, AccountID: tr.AccountID, PlacementGeneration: tr.SlotGeneration, ContextRebuildable: true, ProvisionalToken: uuid.NewString(), Config: tr.Config})
	require.ErrorIs(t, err, service.ErrOpenAIConversationResetRequired)
}

func TestConversationActivityResidentPreferenceReplacementKeepsLiveThread(t *testing.T) {
	repo, tr := conversationActivityFixture(t)
	ctx := context.Background()
	other := mustCreateAccount(t, repo.client, &service.Account{Name: uuid.NewString(), Platform: service.PlatformOpenAI})
	replacement, created, err := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{
		UserID: tr.UserID, APIKeyID: tr.APIKeyID, AccountID: other.ID, ScopeKey: tr.ScopeKey, ConversationHash: strings.Repeat("d", 64),
		PlacementGeneration: tr.SlotGeneration, MaxResidentSlots: 1, ProvisionalToken: uuid.NewString(), ContextRebuildable: true, Config: tr.Config})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, other.ID, replacement.AccountID)
	var status string
	require.NoError(t, scanSingleRow(ctx, repo.sql, `SELECT status FROM openai_user_resident_slots WHERE id=$1`, []any{tr.ResidentSlotID}, &status))
	require.Equal(t, "draining", status)
	original, err := repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
	require.NoError(t, err)
	require.NotNil(t, original)
	valid, err := repo.ValidateOpenAIUserConversationBinding(ctx, *original)
	require.NoError(t, err)
	require.True(t, valid)
}

func TestConversationActivityNullableDeadlineAndFailedRequest(t *testing.T) {
	repo, tr := conversationActivityFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := repo.sql.ExecContext(ctx, `UPDATE openai_user_conversation_bindings SET active_until=NULL::timestamptz,expires_at=$2::timestamptz WHERE id=$1`, tr.BindingID, now.Add(time.Minute))
	require.NoError(t, err)
	token := uuid.NewString()
	acquired, err := repo.AcquireOpenAIConversationActivity(ctx, tr, token, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, acquired)
	touched, err := repo.TouchOpenAIConversationActivity(ctx, tr, now)
	require.NoError(t, err)
	require.True(t, touched)
	b, err := repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
	require.NoError(t, err)
	require.NotNil(t, b.ActiveUntil)
	before := *b.ActiveUntil
	require.NoError(t, repo.ReleaseOpenAIConversationActivity(ctx, token))
	token = uuid.NewString()
	acquired, err = repo.AcquireOpenAIConversationActivity(ctx, tr, token, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, repo.ReleaseOpenAIConversationActivity(ctx, token))
	b, err = repo.GetOpenAIUserConversationBinding(ctx, tr.UserID, tr.APIKeyID, tr.ScopeKey, tr.ConversationHash)
	require.NoError(t, err)
	require.Equal(t, before, *b.ActiveUntil, "只获取并释放请求保护不得续期")
}

// 同一用户其他 Thread 不会被续期，在途请求仍计入真实 Persona 容量。
func TestConversationActivityProtectsPersonaLeaseAndIndependentThreads(t *testing.T) {
	ctx := context.Background()
	f := newOpenAIReservationFixture(t)
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	personaRepo := NewOpenAIPersonaUserReservationRepository(integrationDB)
	cfg := service.DefaultOpenAIUserAffinityConfig()
	cfg.ConversationActiveTTLSeconds = 2400
	scope := fmt.Sprintf("openai:v1:group:%d:lane:endpoint:responses", f.groupID)
	session, err := NewOpenAIAccountPersonaRepository(integrationDB).GetAccountPersonaSession(ctx, f.account.ID, f.persona.ID, f.persona.CurrentSessionEpoch, time.Now())
	require.NoError(t, err)
	target, err := service.OpenAIExecutionTargetFromPersonaSession(f.persona, *session)
	require.NoError(t, err)
	seed := func(hash string) service.OpenAIUserConversationTransition {
		token := uuid.NewString()
		b, _, e := repo.ReserveOpenAIUserConversationBinding(ctx, service.OpenAIUserConversationReservation{UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.account.ID, ScopeKey: scope, ConversationHash: strings.Repeat(hash, 64), PlacementGeneration: 1, ProvisionalToken: token, ContextRebuildable: true, Config: cfg})
		require.NoError(t, e)
		tr := service.OpenAIUserConversationTransition{BindingID: b.ID, UserID: f.userID, APIKeyID: f.apiKeyID, AccountID: f.account.ID, ScopeKey: scope, ConversationHash: b.ConversationHash, ResidentSlotID: b.ResidentSlotID, SlotGeneration: b.SlotGeneration, BindingEpoch: service.OpenAIConversationBindingEpoch, ProvisionalToken: token, Config: cfg}
		require.NoError(t, repo.BindOpenAIUserConversationExecutionTarget(ctx, tr, target))
		_, e = repo.CommitOpenAIUserConversationBinding(ctx, tr)
		require.NoError(t, e)
		tr.ProvisionalToken = ""
		return tr
	}
	first := seed("a")
	now := time.Now().UTC()
	personaToken := uuid.NewString()
	lease, err := personaRepo.ReservePersonaUser(ctx, service.OpenAIPersonaUserReserveInput{ReservationToken: personaToken, AccountID: f.account.ID, AccountPersonaID: f.persona.ID, UserID: f.userID, MaxUsers: 1, Now: now, HoldUntil: now.Add(time.Minute)})
	require.NoError(t, err)
	hold := uuid.NewString()
	held, err := repo.AcquireOpenAIConversationActivity(ctx, first, hold, now.Add(time.Minute), personaToken)
	require.NoError(t, err)
	require.True(t, held)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_persona_user_request_holds SET expires_at=NOW()-INTERVAL '1 minute' WHERE reservation_token=$1::uuid`, personaToken)
	require.NoError(t, err)
	alive, err := repo.RenewOpenAIConversationActivity(ctx, hold, time.Now().Add(2*time.Minute))
	require.NoError(t, err)
	require.True(t, alive)
	var holdLive bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT expires_at>NOW() FROM openai_persona_user_request_holds WHERE reservation_token=$1::uuid`, personaToken).Scan(&holdLive))
	require.True(t, holdLive)
	_, err = personaRepo.CommitPersonaUserReservation(ctx, service.OpenAIPersonaUserReservationCommit{ReservationToken: personaToken, Now: time.Now(), ActiveUntil: time.Now().Add(40 * time.Minute)})
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_persona_active_user_leases SET active_until=NOW()-INTERVAL '1 minute' WHERE id=$1`, lease.LeaseID)
	require.NoError(t, err)
	_, err = integrationDB.ExecContext(ctx, `UPDATE openai_user_conversation_bindings SET active_until=NOW()-INTERVAL '1 minute' WHERE id=$1`, first.BindingID)
	require.NoError(t, err)
	candidates, err := personaRepo.ListOpenAIPersonaCapacityCandidates(ctx, []int64{f.account.ID}, f.userID, time.Now())
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.True(t, candidates[0].UserAlreadyActive)
	require.Equal(t, 1, candidates[0].ActiveUsers)
	other := createOpenAIReservationUser(t, "activity-other")
	_, err = personaRepo.ReservePersonaUser(ctx, service.OpenAIPersonaUserReserveInput{ReservationToken: uuid.NewString(), AccountID: f.account.ID, AccountPersonaID: f.persona.ID, UserID: other, MaxUsers: 1, Now: time.Now(), HoldUntil: time.Now().Add(time.Minute)})
	require.ErrorIs(t, err, service.ErrOpenAIPersonaUserCapacity)
	touched, err := repo.TouchOpenAIConversationActivity(ctx, first, time.Now())
	require.NoError(t, err)
	require.True(t, touched)
	second := seed("b")
	var before, timeAfter, bindingUntil, leaseUntil time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT active_until FROM openai_user_conversation_bindings WHERE id=$1`, second.BindingID).Scan(&before))
	touched, err = repo.TouchOpenAIConversationActivity(ctx, first, time.Now().Add(time.Second))
	require.NoError(t, err)
	require.True(t, touched)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT active_until FROM openai_user_conversation_bindings WHERE id=$1`, second.BindingID).Scan(&timeAfter))
	require.Equal(t, before, timeAfter)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT b.active_until,l.active_until FROM openai_user_conversation_bindings b JOIN openai_persona_active_user_leases l ON l.account_persona_id=b.account_persona_id AND l.user_id=b.user_id WHERE b.id=$1`, first.BindingID).Scan(&bindingUntil, &leaseUntil))
	require.Equal(t, bindingUntil, leaseUntil)
	require.NoError(t, repo.ReleaseOpenAIConversationActivity(ctx, hold))
}
