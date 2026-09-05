//go:build integration

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	account := &service.Account{
		Name: "reservation-" + suffix, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"chatgpt_account_id": "acct-" + suffix}, Extra: map[string]any{}, Concurrency: 1,
		Priority: 50, Status: service.StatusActive, Schedulable: true,
	}
	primary := service.OpenAIPrimaryPersonaCreate{
		ProfileVersion: "0.149.0", CredentialChainID: "chain-" + suffix,
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"test"}`), ChatGPTAccountID: "acct-" + suffix,
		DeviceSeed: []byte("0123456789abcdef0123456789abcdef"), InstallationID: "install-" + suffix,
		UpstreamSessionID: "session-" + suffix,
	}
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

func createOpenAIReservationUser(t *testing.T, prefix string) int64 {
	t.Helper()
	var userID int64
	email := fmt.Sprintf("%s-%d@example.com", prefix, time.Now().UnixNano())
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `INSERT INTO users (email, password_hash) VALUES ($1, 'test') RETURNING id`, email).Scan(&userID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

func createAuthorizedReservationPersona(t *testing.T, fixture openAIReservationFixture) service.OpenAIAccountPersona {
	t.Helper()
	ctx := context.Background()
	repo := NewOpenAIAccountPersonaRepository(integrationDB)
	created, err := repo.CreateAccountPersona(ctx, service.OpenAIAccountPersonaCreate{
		AccountID: fixture.account.ID, ProfileID: service.SessionPersonaOpenCode, ProfileVersion: "1.18.23",
		DeviceSeed: []byte("abcdef0123456789abcdef0123456789"), InstallationID: "second-" + uuid.NewString(),
	})
	require.NoError(t, err)
	authorized, err := repo.AuthorizeAccountPersona(ctx, service.OpenAIAccountPersonaAuthorization{
		AccountID: fixture.account.ID, AccountPersonaID: created.ID, ExpectedRowVersion: created.RowVersion,
		PersonaGeneration: created.PersonaGeneration, CredentialChainID: "second-chain-" + uuid.NewString(),
		EncryptedPayload: json.RawMessage(`{"format_version":1,"ciphertext":"second"}`),
		ChatGPTAccountID: fixture.account.GetCredential("chatgpt_account_id"), InstallationID: created.InstallationID,
		UpstreamSessionID: "second-session-" + uuid.NewString(), OldSessionExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	return *authorized
}

func TestOpenAIPersonaUserReservation_SameUserRequestsShareOneSeat(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := NewOpenAIPersonaUserReservationRepository(integrationDB)
	tokens := []string{uuid.NewString(), uuid.NewString()}
	leaseIDs := make([]int64, 0, len(tokens))
	for _, token := range tokens {
		reservation, err := repo.ReservePersonaUser(ctx, service.OpenAIPersonaUserReserveInput{
			ReservationToken: token, AccountID: fixture.account.ID, AccountPersonaID: fixture.persona.ID,
			UserID: fixture.userID, MaxUsers: 1, Now: now, HoldUntil: now.Add(time.Minute),
		})
		require.NoError(t, err)
		leaseIDs = append(leaseIDs, reservation.LeaseID)
	}
	require.Equal(t, leaseIDs[0], leaseIDs[1])

	var leases, holds int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_persona_active_user_leases WHERE account_persona_id = $1 AND user_id = $2`, fixture.persona.ID, fixture.userID).Scan(&leases))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM openai_persona_user_request_holds WHERE lease_id = $1`, leaseIDs[0]).Scan(&holds))
	require.Equal(t, 1, leases)
	require.Equal(t, 2, holds)

	require.NoError(t, repo.RollbackPersonaUserReservation(ctx, tokens[0], now.Add(time.Second)))
	activeUntil := now.Add(40 * time.Minute)
	target, err := repo.CommitPersonaUserReservation(ctx, service.OpenAIPersonaUserReservationCommit{
		ReservationToken: tokens[1], Now: now.Add(2 * time.Second), ActiveUntil: activeUntil,
	})
	require.NoError(t, err)
	require.True(t, target.Valid())
	require.Equal(t, leaseIDs[0], target.PersonaUserLeaseID)

	candidates, err := repo.ListOpenAIPersonaCapacityCandidates(ctx, []int64{fixture.account.ID}, fixture.userID, now.Add(3*time.Second))
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, 1, candidates[0].ActiveUsers)
	require.True(t, candidates[0].UserAlreadyActive)
}

func TestOpenAIPersonaUserReservation_DifferentUsersSerializeLastSeat(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	otherUserID := createOpenAIReservationUser(t, "persona-other")
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := NewOpenAIPersonaUserReservationRepository(integrationDB)
	type result struct {
		token string
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, userID := range []int64{fixture.userID, otherUserID} {
		wg.Add(1)
		go func(userID int64) {
			defer wg.Done()
			<-start
			token := uuid.NewString()
			_, err := repo.ReservePersonaUser(context.Background(), service.OpenAIPersonaUserReserveInput{
				ReservationToken: token, AccountID: fixture.account.ID, AccountPersonaID: fixture.persona.ID,
				UserID: userID, MaxUsers: 1, Now: now, HoldUntil: now.Add(time.Minute),
			})
			results <- result{token: token, err: err}
		}(userID)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, rejected := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			require.NoError(t, repo.RollbackPersonaUserReservation(context.Background(), result.token, now.Add(time.Second)))
		case errors.Is(result.err, service.ErrOpenAIPersonaUserCapacity):
			rejected++
		default:
			require.NoError(t, result.err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, rejected)
}

func TestOpenAIPersonaUserReservation_UserCanOccupyMultiplePersonasInOneAccount(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	second := createAuthorizedReservationPersona(t, fixture)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo := NewOpenAIPersonaUserReservationRepository(integrationDB)
	leaseIDs := make([]int64, 0, 2)
	for _, personaID := range []int64{fixture.persona.ID, second.ID} {
		token := uuid.NewString()
		reservation, err := repo.ReservePersonaUser(ctx, service.OpenAIPersonaUserReserveInput{
			ReservationToken: token, AccountID: fixture.account.ID, AccountPersonaID: personaID,
			UserID: fixture.userID, MaxUsers: 1, Now: now, HoldUntil: now.Add(time.Minute),
		})
		require.NoError(t, err)
		leaseIDs = append(leaseIDs, reservation.LeaseID)
		require.NoError(t, repo.RollbackPersonaUserReservation(ctx, token, now.Add(time.Second)))
	}
	require.NotEqual(t, leaseIDs[0], leaseIDs[1])
}

func TestOpenAIPersonaUserReservation_DrainingAllowsExistingThreadOnly(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := integrationDB.ExecContext(ctx, `UPDATE openai_account_personas
SET state = 'draining', enabled = FALSE, draining_started_at = $1::timestamptz, updated_at = $1::timestamptz
WHERE id = $2`, now, fixture.persona.ID)
	require.NoError(t, err)
	repo := NewOpenAIPersonaUserReservationRepository(integrationDB)
	input := service.OpenAIPersonaUserReserveInput{
		ReservationToken: uuid.NewString(), AccountID: fixture.account.ID, AccountPersonaID: fixture.persona.ID,
		UserID: fixture.userID, MaxUsers: 1, Now: now, HoldUntil: now.Add(time.Minute),
	}
	_, err = repo.ReservePersonaUser(ctx, input)
	require.ErrorIs(t, err, service.ErrOpenAIPersonaUserCapacity)
	input.ReservationToken = uuid.NewString()
	input.ExistingThread = true
	_, err = repo.ReservePersonaUser(ctx, input)
	require.NoError(t, err)
	require.NoError(t, repo.RollbackPersonaUserReservation(ctx, input.ReservationToken, now.Add(time.Second)))
}

func TestOpenAIConversationBindingAllowsNullDiagnosticSessionHash(t *testing.T) {
	fixture := newOpenAIReservationFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	reservationRepo := NewOpenAIPersonaUserReservationRepository(integrationDB)
	candidates, err := reservationRepo.ListOpenAIPersonaCapacityCandidates(ctx, []int64{fixture.account.ID}, fixture.userID, now)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	target, err := service.OpenAIExecutionTargetFromPersonaSession(candidates[0].Persona, candidates[0].Session)
	require.NoError(t, err)

	token := uuid.NewString()
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
     expires_at, provisional_token, created_at, updated_at)
VALUES ($1, $2, 'openai', $3::char(64), $4, $5, 1, 'provisional', TRUE, FALSE,
        $6::timestamptz, $7::timestamptz, $8, $9::timestamptz, $9::timestamptz) RETURNING id`,
		fixture.userID, fixture.apiKeyID, strings.Repeat("e", 64), residentSlotID, fixture.account.ID,
		now.Add(time.Minute), now.Add(time.Hour), token, now).Scan(&bindingID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM openai_user_conversation_bindings WHERE id = $1`, bindingID)
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM openai_user_resident_slots WHERE id = $1`, residentSlotID)
	})

	repo := newAccountRepositoryWithSQL(integrationEntClient, integrationDB, nil)
	transition := service.OpenAIUserConversationTransition{
		BindingID: bindingID, AccountID: fixture.account.ID, ProvisionalToken: token,
		RootClientSessionHash: "",
	}
	require.NoError(t, repo.BindOpenAIUserConversationExecutionTarget(ctx, transition, target))
	require.NoError(t, repo.BindOpenAIUserConversationExecutionTarget(ctx, transition, target))
	var diagnosticHash sql.NullString
	var legacyLeaseID sql.NullInt64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT root_client_session_hash, user_group_client_session_lease_id
FROM openai_user_conversation_bindings WHERE id = $1`, bindingID).Scan(&diagnosticHash, &legacyLeaseID))
	require.False(t, diagnosticHash.Valid)
	require.False(t, legacyLeaseID.Valid)
}
