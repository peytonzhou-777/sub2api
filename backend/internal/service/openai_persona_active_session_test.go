package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type openAIPersonaActiveSessionCacheStub struct {
	GatewayCache
	state       OpenAIPersonaActiveSessionReservationState
	commits     int
	releases    int
	activeTTL   time.Duration
	pendingTTL  time.Duration
	reservation string
}

func (s *openAIPersonaActiveSessionCacheStub) ReserveOpenAIPersonaActiveSession(_ context.Context, _ int64, _, _, reservationID string, _ int, pendingTTL time.Duration) (OpenAIPersonaActiveSessionReservationState, error) {
	s.reservation = reservationID
	s.pendingTTL = pendingTTL
	return s.state, nil
}

func (s *openAIPersonaActiveSessionCacheStub) CommitOpenAIPersonaActiveSession(_ context.Context, _ int64, _, _ string, activeTTL time.Duration) (bool, error) {
	s.commits++
	s.activeTTL = activeTTL
	return true, nil
}

func (s *openAIPersonaActiveSessionCacheStub) ReleaseOpenAIPersonaActiveSessionReservation(_ context.Context, _ int64, _, _, reservationID string) error {
	if reservationID == s.reservation {
		s.releases++
	}
	return nil
}

func openAIPersonaActiveSessionTestBinding(t *testing.T, accountID int64) SessionPersonaSlotBinding {
	t.Helper()
	binding, err := NewDefaultSessionPersonaRegistry().ResolveSlot(DefaultSessionPersonaScopeVersion, 0, DefaultSessionPersonaSlotCount)
	if err != nil {
		t.Fatalf("resolve Persona slot: %v", err)
	}
	binding.AccountID = accountID
	return binding
}

func TestOpenAIPersonaActiveSessionReservationCommitsOnAcceptedResponse(t *testing.T) {
	cache := &openAIPersonaActiveSessionCacheStub{state: OpenAIPersonaActiveSessionPendingCreated}
	service := &OpenAIGatewayService{cache: cache}
	account := &Account{ID: 41}
	binding := openAIPersonaActiveSessionTestBinding(t, account.ID)

	requestCtx := openAIUserAffinitySuccessTestContext("persona-active-accepted")
	clientSessionHash, err := OpenAIPersonaClientSessionHash(requestCtx, "client-session-a")
	if err != nil {
		t.Fatalf("hash client Session: %v", err)
	}
	ctx, release, allowed, err := service.ReserveOpenAIPersonaActiveSession(requestCtx, account, binding, clientSessionHash)
	if err != nil || !allowed {
		t.Fatalf("reserve active Session: allowed=%v err=%v", allowed, err)
	}
	service.RecordOpenAIUserAffinityAccepted(ctx, account.ID)
	release()

	if cache.commits != 1 || cache.releases != 0 {
		t.Fatalf("accepted reservation commits=%d releases=%d, want 1/0", cache.commits, cache.releases)
	}
	if cache.activeTTL != time.Hour {
		t.Fatalf("active TTL=%s, want %s", cache.activeTTL, time.Hour)
	}
	if cache.pendingTTL != time.Hour {
		t.Fatalf("pending TTL=%s, want %s", cache.pendingTTL, time.Hour)
	}
}

func TestOpenAIPersonaActiveSessionReservationReleasesOnFailedRequest(t *testing.T) {
	cache := &openAIPersonaActiveSessionCacheStub{state: OpenAIPersonaActiveSessionPendingCreated}
	service := &OpenAIGatewayService{cache: cache}
	account := &Account{ID: 42}
	binding := openAIPersonaActiveSessionTestBinding(t, account.ID)

	requestCtx := openAIUserAffinitySuccessTestContext("persona-active-failed")
	clientSessionHash, err := OpenAIPersonaClientSessionHash(requestCtx, "client-session-b")
	if err != nil {
		t.Fatalf("hash client Session: %v", err)
	}
	ctx, release, allowed, err := service.ReserveOpenAIPersonaActiveSession(requestCtx, account, binding, clientSessionHash)
	if err != nil || !allowed {
		t.Fatalf("reserve active Session: allowed=%v err=%v", allowed, err)
	}
	service.RecordOpenAIUserAffinityFailure(ctx, account.ID)
	release()

	if cache.commits != 0 || cache.releases != 1 {
		t.Fatalf("failed reservation commits=%d releases=%d, want 0/1", cache.commits, cache.releases)
	}
}

func TestOpenAIPersonaClientSessionHashScopesByAuthenticatedClient(t *testing.T) {
	base := context.WithValue(context.Background(), ctxkey.UserID, int64(7))
	firstCtx := context.WithValue(base, ctxkey.APIKeyID, int64(11))
	secondCtx := context.WithValue(base, ctxkey.APIKeyID, int64(12))

	first, err := OpenAIPersonaClientSessionHash(firstCtx, "same-session-id")
	if err != nil {
		t.Fatalf("hash first client Session: %v", err)
	}
	repeated, err := OpenAIPersonaClientSessionHash(firstCtx, "same-session-id")
	if err != nil {
		t.Fatalf("hash repeated client Session: %v", err)
	}
	second, err := OpenAIPersonaClientSessionHash(secondCtx, "same-session-id")
	if err != nil {
		t.Fatalf("hash second client Session: %v", err)
	}

	if len(first) != 64 || first != repeated || first == second {
		t.Fatalf("unexpected scoped hashes: first=%q repeated=%q second=%q", first, repeated, second)
	}
	if _, err := OpenAIPersonaClientSessionHash(nil, "same-session-id"); err == nil {
		t.Fatal("nil context must not produce an unscoped client Session hash")
	}
}
