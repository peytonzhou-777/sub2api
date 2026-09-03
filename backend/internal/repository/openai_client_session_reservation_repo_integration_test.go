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
