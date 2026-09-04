//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type openAIReservationFixture struct {
	account                   *service.Account
	persona                   service.OpenAIAccountPersona
	userID, groupID, apiKeyID int64
}

func newOpenAIReservationFixture(t *testing.T) openAIReservationFixture {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	account := &service.Account{Name: "reservation-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "acct-" + suffix}, Extra: map[string]any{}, Concurrency: 1,
		Priority: 50, Status: service.StatusActive, Schedulable: true}
	primary := service.OpenAIPrimaryPersonaCreate{ProfileVersion: "0.149.0", CredentialChainID: "chain-" + suffix,
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"test"}`), ChatGPTAccountID: "acct-" + suffix,
		DeviceSeed: []byte("0123456789abcdef0123456789abcdef"), InstallationID: "install-" + suffix, UpstreamSessionID: "session-" + suffix}
	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	require.NoError(t, repo.CreateWithPrimaryOpenAIPersona(ctx, account, nil, primary))
	personas, err := NewOpenAIAccountPersonaRepository(integrationDB).ListAccountPersonas(ctx, account.ID)
	require.NoError(t, err)
	require.Len(t, personas, 1)
	var userID, groupID, apiKeyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, 'test') RETURNING id`, "reservation-"+suffix+"@example.com").Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO groups (name) VALUES ($1) RETURNING id`, "reservation-"+suffix).Scan(&groupID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys (user_id, key, name, group_id) VALUES ($1, $2, 'test', $3) RETURNING id`, userID, "sk-reservation-"+suffix, groupID).Scan(&apiKeyID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM accounts WHERE id = $1`, account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM groups WHERE id = $1`, groupID)
	})
	return openAIReservationFixture{account: account, persona: personas[0], userID: userID, groupID: groupID, apiKeyID: apiKeyID}
}

func TestOpenAIClientSessionReservationRepository_ConcurrentUserGroupLastSeat(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 2; i++ {
		_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO openai_user_group_client_session_leases
    (user_id, effective_group_id, client_session_hash, state, last_active_at, active_until)
VALUES ($1, $2, $3, 'active', $4::timestamptz, $5::timestamptz)`, fixture.userID, fixture.groupID,
			strings.Repeat(fmt.Sprintf("%x", i+1), 64), now, now.Add(time.Hour))
		require.NoError(t, err)
	}
	repo := NewOpenAIClientSessionReservationRepository(integrationDB)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := repo.ReserveUserGroupSession(context.Background(), service.OpenAIUserGroupSessionReserveInput{
				ReservationToken: uuid.NewString(), UserID: fixture.userID, EffectiveGroupID: fixture.groupID,
				ClientSessionHash: strings.Repeat(fmt.Sprintf("%x", index+10), 64), MaxSessions: 3,
				Now: now, HoldUntil: now.Add(time.Minute)})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	success, rejected := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if err == service.ErrOpenAIUserGroupSessionCapacity {
			rejected++
		} else {
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, success)
	require.Equal(t, 1, rejected)
}

func TestOpenAIClientSessionReservationRepository_SameSessionSharesSeatAndHolds(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := NewOpenAIClientSessionReservationRepository(integrationDB)
	hash := strings.Repeat("a", 64)
	start := make(chan struct{})
	ids := make(chan int64, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reservation, err := repo.ReserveUserGroupSession(context.Background(), service.OpenAIUserGroupSessionReserveInput{
				ReservationToken: uuid.NewString(), UserID: fixture.userID, EffectiveGroupID: fixture.groupID,
				ClientSessionHash: hash, MaxSessions: 1, Now: now, HoldUntil: now.Add(time.Minute)})
			require.NoError(t, err)
			ids <- reservation.LeaseID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	var first int64
	for id := range ids {
		if first == 0 {
			first = id
		}
		require.Equal(t, first, id)
	}
	var leases, holds int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM openai_user_group_client_session_leases WHERE user_id=$1 AND effective_group_id=$2`, fixture.userID, fixture.groupID).Scan(&leases))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM openai_user_group_session_request_holds WHERE lease_id=$1`, first).Scan(&holds))
	require.Equal(t, 1, leases)
	require.Equal(t, 2, holds)
}

func TestOpenAIConversationBindingPersistsExecutionTarget(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	token := uuid.NewString()
	rootHash := strings.Repeat("d", 64)
	reservationRepo := NewOpenAIClientSessionReservationRepository(integrationDB)
	userLease, err := reservationRepo.ReserveUserGroupSession(ctx, service.OpenAIUserGroupSessionReserveInput{
		ReservationToken: token, UserID: fixture.userID, EffectiveGroupID: fixture.groupID,
		ClientSessionHash: rootHash, MaxSessions: 3, Now: now, HoldUntil: now.Add(time.Minute),
	})
	require.NoError(t, err)
	candidates, err := reservationRepo.ListOpenAIPersonaCapacityCandidates(ctx, []int64{fixture.account.ID}, fixture.userID, fixture.apiKeyID, rootHash, now)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.False(t, candidates[0].ClaimedByUser, "用户没有 Persona claim 时应返回 false，而不是 SQL NULL 扫描错误")
	target, err := service.OpenAIExecutionTargetFromPersonaSession(candidates[0].Persona, candidates[0].Session)
	require.NoError(t, err)
	target.UserGroupLeaseID = userLease.LeaseID

	var residentSlotID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO openai_user_resident_slots
			(user_id, scope_key, slot_index, account_id, generation, status, admitted_at, expires_at,
			 score_updated_at, provisional_token, created_at, updated_at)
		VALUES ($1, 'openai', 1, $2, 1, 'provisional', $3::timestamptz, $4::timestamptz,
		        $3::timestamptz, $5, $3::timestamptz, $3::timestamptz)
		RETURNING id`, fixture.userID, fixture.account.ID, now, now.Add(time.Hour), token).Scan(&residentSlotID))
	conversationHash := strings.Repeat("e", 64)
	var bindingID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		INSERT INTO openai_user_conversation_bindings
			(user_id, api_key_id, scope_key, conversation_hash, resident_slot_id, account_id,
			 slot_generation, status, context_rebuildable, first_output_committed,
			 active_until, expires_at, provisional_token, created_at, updated_at)
		VALUES ($1, $2, 'openai', $3::char(64), $4, $5, 1, 'provisional', TRUE, FALSE,
		        $6::timestamptz, $7::timestamptz, $8, $9::timestamptz, $9::timestamptz)
		RETURNING id`, fixture.userID, fixture.apiKeyID, conversationHash, residentSlotID,
		fixture.account.ID, now.Add(time.Minute), now.Add(time.Hour), token, now).Scan(&bindingID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM openai_user_conversation_bindings WHERE id = $1`, bindingID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM openai_user_resident_slots WHERE id = $1`, residentSlotID)
		_ = reservationRepo.RollbackClientSessionReservation(context.Background(), token, time.Now().UTC())
	})

	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	transition := service.OpenAIUserConversationTransition{
		BindingID: bindingID, AccountID: fixture.account.ID, ProvisionalToken: token,
		RootClientSessionHash: rootHash,
	}
	require.NoError(t, repo.BindOpenAIUserConversationExecutionTarget(ctx, transition, target))
	binding, err := repo.GetOpenAIUserConversationBinding(ctx, fixture.userID, fixture.apiKeyID, "openai", conversationHash)
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.Equal(t, target.AccountPersonaID, binding.AccountPersonaID)
	require.Equal(t, target.SessionEpoch, binding.PersonaSessionEpoch)
	require.Equal(t, target.CredentialChainID, binding.CredentialChainID)
	require.Equal(t, rootHash, binding.RootClientSessionHash)
	require.Equal(t, userLease.LeaseID, binding.UserGroupLeaseID)
	require.Equal(t, target.ProfileID, binding.ProfileID)
	require.Equal(t, target.ProfileVersion, binding.ProfileVersion)
}

func TestOpenAIClientSessionReservationRollbackExpiresLeaseReferencedByBinding(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	token := uuid.NewString()
	clientHash := strings.Repeat("f", 64)
	repo := NewOpenAIClientSessionReservationRepository(integrationDB)
	userLease, err := repo.ReserveUserGroupSession(ctx, service.OpenAIUserGroupSessionReserveInput{
		ReservationToken: token, UserID: fixture.userID, EffectiveGroupID: fixture.groupID,
		ClientSessionHash: clientHash, MaxSessions: 3, Now: now, HoldUntil: now.Add(time.Minute),
	})
	require.NoError(t, err)
	personaLease, err := repo.ReservePersonaSession(ctx, service.OpenAIPersonaSessionReserveInput{
		ReservationToken: token, AccountID: fixture.account.ID, AccountPersonaID: fixture.persona.ID,
		UserID: fixture.userID, APIKeyID: fixture.apiKeyID, ClientSessionHash: clientHash,
		MaxSessions: 1, Now: now, HoldUntil: now.Add(time.Minute),
	})
	require.NoError(t, err)

	var residentSlotID, bindingID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO openai_user_resident_slots
        (user_id, scope_key, slot_index, account_id, generation, status, admitted_at, expires_at,
         score_updated_at, provisional_token, created_at, updated_at)
        VALUES ($1, 'openai', 1, $2, 1, 'provisional', $3::timestamptz, $4::timestamptz,
                $3::timestamptz, $5, $3::timestamptz, $3::timestamptz) RETURNING id`,
		fixture.userID, fixture.account.ID, now, now.Add(time.Hour), token).Scan(&residentSlotID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO openai_user_conversation_bindings
        (user_id, api_key_id, scope_key, conversation_hash, resident_slot_id, account_id,
         slot_generation, status, context_rebuildable, first_output_committed, active_until,
         expires_at, provisional_token, user_group_client_session_lease_id, created_at, updated_at)
        VALUES ($1, $2, 'openai', $3::char(64), $4, $5, 1, 'provisional', TRUE, FALSE,
                $6::timestamptz, $7::timestamptz, $8, $9, $10::timestamptz, $10::timestamptz)
        RETURNING id`, fixture.userID, fixture.apiKeyID, strings.Repeat("e", 64), residentSlotID,
		fixture.account.ID, now.Add(time.Minute), now.Add(time.Hour), token, userLease.LeaseID, now).Scan(&bindingID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM openai_user_conversation_bindings WHERE id = $1`, bindingID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM openai_user_resident_slots WHERE id = $1`, residentSlotID)
	})

	require.NoError(t, repo.RollbackClientSessionReservation(ctx, token, now.Add(time.Second)))
	var userState, personaState string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM openai_user_group_client_session_leases WHERE id = $1`, userLease.LeaseID).Scan(&userState))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT state FROM openai_persona_client_session_leases WHERE id = $1`, personaLease.LeaseID).Scan(&personaState))
	require.Equal(t, "expired", userState)
	require.Equal(t, "expired", personaState)
	var referencedLeaseID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT user_group_client_session_lease_id FROM openai_user_conversation_bindings WHERE id = $1`, bindingID).Scan(&referencedLeaseID))
	require.Equal(t, userLease.LeaseID, referencedLeaseID)
}

func TestOpenAIClientSessionReservationRepository_PersonaSeatClaimAndCommit(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := NewOpenAIClientSessionReservationRepository(integrationDB)
	token := uuid.NewString()
	hash := strings.Repeat("b", 64)
	global, err := repo.ReserveUserGroupSession(context.Background(), service.OpenAIUserGroupSessionReserveInput{
		ReservationToken: token, UserID: fixture.userID, EffectiveGroupID: fixture.groupID, ClientSessionHash: hash,
		MaxSessions: 3, Now: now, HoldUntil: now.Add(time.Minute)})
	require.NoError(t, err)
	persona, err := repo.ReservePersonaSession(context.Background(), service.OpenAIPersonaSessionReserveInput{
		ReservationToken: token, AccountID: fixture.account.ID, AccountPersonaID: fixture.persona.ID,
		UserID: fixture.userID, APIKeyID: fixture.apiKeyID, ClientSessionHash: hash, MaxSessions: 1,
		Now: now, HoldUntil: now.Add(time.Minute)})
	require.NoError(t, err)
	target, err := repo.CommitClientSessionReservation(context.Background(), service.OpenAIClientSessionReservationCommit{
		ReservationToken: token, Now: now.Add(time.Second), ActiveUntil: now.Add(time.Hour)})
	require.NoError(t, err)
	require.True(t, target.Valid())
	require.Equal(t, global.LeaseID, target.UserGroupLeaseID)
	require.Equal(t, persona.LeaseID, target.PersonaLeaseID)

	var otherUser, otherKey int64
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO users (email,password_hash) VALUES ($1,'test') RETURNING id`, "other-"+suffix+"@example.com").Scan(&otherUser))
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO api_keys (user_id,key,name,group_id) VALUES ($1,$2,'test',$3) RETURNING id`, otherUser, "sk-other-"+suffix, fixture.groupID).Scan(&otherKey))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id=$1`, otherUser)
	})
	_, err = repo.ReservePersonaSession(context.Background(), service.OpenAIPersonaSessionReserveInput{
		ReservationToken: uuid.NewString(), AccountID: fixture.account.ID, AccountPersonaID: fixture.persona.ID,
		UserID: otherUser, APIKeyID: otherKey, ClientSessionHash: strings.Repeat("c", 64), MaxSessions: 1,
		Now: now.Add(2 * time.Second), HoldUntil: now.Add(time.Minute)})
	require.ErrorIs(t, err, service.ErrOpenAIPersonaSessionCapacity)
}

func TestOpenAIClientSessionReservationRepository_DrainingPersonaOnlyAcceptsExistingThread(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, func() error {
		_, err := integrationDB.ExecContext(context.Background(), `UPDATE openai_account_personas
SET state = 'draining', enabled = FALSE, draining_started_at = $1::timestamptz, updated_at = $1::timestamptz
WHERE id = $2`, now, fixture.persona.ID)
		return err
	}())
	repo := NewOpenAIClientSessionReservationRepository(integrationDB)
	input := service.OpenAIPersonaSessionReserveInput{
		ReservationToken: uuid.NewString(), AccountID: fixture.account.ID, AccountPersonaID: fixture.persona.ID,
		UserID: fixture.userID, APIKeyID: fixture.apiKeyID, ClientSessionHash: strings.Repeat("a", 64),
		MaxSessions: 1, Now: now, HoldUntil: now.Add(time.Minute),
	}
	_, err := repo.ReservePersonaSession(context.Background(), input)
	require.ErrorIs(t, err, service.ErrOpenAIPersonaSessionCapacity)

	input.ReservationToken = uuid.NewString()
	input.ExistingThread = true
	reservation, err := repo.ReservePersonaSession(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.NoError(t, repo.RollbackClientSessionReservation(context.Background(), input.ReservationToken, now.Add(time.Second)))
}

func TestOpenAIClientSessionReservationRepository_HoldProtectsExpiredLease(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var leaseID int64
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO openai_user_group_client_session_leases
    (user_id,effective_group_id,client_session_hash,state,last_active_at,active_until)
VALUES ($1,$2,$3,'active',$4::timestamptz,$5::timestamptz) RETURNING id`, fixture.userID, fixture.groupID,
		strings.Repeat("d", 64), now.Add(-time.Hour), now.Add(-time.Minute)).Scan(&leaseID))
	require.NoError(t, func() error {
		_, err := integrationDB.ExecContext(context.Background(), `INSERT INTO openai_user_group_session_request_holds
    (reservation_token,lease_id,expires_at,created_at) VALUES ($1::uuid,$2,$3::timestamptz,$4::timestamptz)`, uuid.NewString(), leaseID, now.Add(time.Minute), now)
		return err
	}())
	_, err := NewOpenAIClientSessionReservationRepository(integrationDB).ReserveUserGroupSession(context.Background(), service.OpenAIUserGroupSessionReserveInput{
		ReservationToken: uuid.NewString(), UserID: fixture.userID, EffectiveGroupID: fixture.groupID,
		ClientSessionHash: strings.Repeat("e", 64), MaxSessions: 1, Now: now, HoldUntil: now.Add(time.Minute)})
	require.ErrorIs(t, err, service.ErrOpenAIUserGroupSessionCapacity)
}

func TestOpenAIClientSessionReservationRepository_UserCannotClaimTwoPersonasInOneAccount(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	ctx := context.Background()
	personaRepo := NewOpenAIAccountPersonaRepository(integrationDB)
	second, err := personaRepo.CreateAccountPersona(ctx, service.OpenAIAccountPersonaCreate{
		AccountID: fixture.account.ID, ProfileID: service.SessionPersonaOpenCode, ProfileVersion: "1.18.23",
		DeviceSeed: []byte("abcdef0123456789abcdef0123456789"), InstallationID: "second-" + uuid.NewString(),
	})
	require.NoError(t, err)
	second, err = personaRepo.AuthorizeAccountPersona(ctx, service.OpenAIAccountPersonaAuthorization{
		AccountID: fixture.account.ID, AccountPersonaID: second.ID, ExpectedRowVersion: second.RowVersion,
		PersonaGeneration: second.PersonaGeneration, CredentialChainID: "second-chain-" + uuid.NewString(),
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"second"}`),
		ChatGPTAccountID: fixture.account.GetCredential("chatgpt_account_id"), InstallationID: second.InstallationID,
		UpstreamSessionID: "second-session-" + uuid.NewString(), OldSessionExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	var secondKeyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys (user_id,key,name,group_id)
VALUES ($1,$2,'second',$3) RETURNING id`, fixture.userID, "sk-second-"+uuid.NewString(), fixture.groupID).Scan(&secondKeyID))
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := NewOpenAIClientSessionReservationRepository(integrationDB)
	_, err = repo.ReservePersonaSession(ctx, service.OpenAIPersonaSessionReserveInput{
		ReservationToken: uuid.NewString(), AccountID: fixture.account.ID, AccountPersonaID: fixture.persona.ID,
		UserID: fixture.userID, APIKeyID: fixture.apiKeyID, ClientSessionHash: strings.Repeat("f", 64),
		MaxSessions: 2, Now: now, HoldUntil: now.Add(time.Minute),
	})
	require.NoError(t, err)
	_, err = repo.ReservePersonaSession(ctx, service.OpenAIPersonaSessionReserveInput{
		ReservationToken: uuid.NewString(), AccountID: fixture.account.ID, AccountPersonaID: second.ID,
		UserID: fixture.userID, APIKeyID: secondKeyID, ClientSessionHash: strings.Repeat("1", 64),
		MaxSessions: 2, Now: now, HoldUntil: now.Add(time.Minute),
	})
	require.ErrorIs(t, err, service.ErrOpenAIAccountPersonaClaim)
}
