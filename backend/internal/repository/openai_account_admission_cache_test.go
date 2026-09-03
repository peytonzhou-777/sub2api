package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newOpenAIAdmissionTestCache(t *testing.T) service.OpenAIAccountAdmissionQueue {
	t.Helper()
	mr := miniredis.RunT(t)
	return NewOpenAIAccountAdmissionQueue(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
}

func TestOpenAIAccountAdmissionKeysKeepLegacyLayoutWithoutPersona(t *testing.T) {
	ticket := service.OpenAIAccountAdmissionTicket{
		AccountID:        17,
		SessionScopeHash: strings.Repeat("a", 64),
		SessionEpoch:     3,
	}
	keys := openAIAccountAdmissionKeys(ticket)
	if got, want := keys[0], "openai:admission:{17}:session:"+strings.Repeat("a", 64)+":3:interactive"; got != want {
		t.Fatalf("legacy interactive key = %q, want %q", got, want)
	}
	if got, want := keys[10], "openai:admission:{17}:rate_state"; got != want {
		t.Fatalf("legacy rate key = %q, want %q", got, want)
	}
}

func TestOpenAIAccountAdmissionKeysIsolatePersonaSlotGenerations(t *testing.T) {
	base := service.OpenAIAccountAdmissionTicket{
		AccountID:         17,
		Persona:           " OpenCode ",
		SlotID:            1,
		SlotGeneration:    2,
		SlotSetGeneration: 5,
		CredentialChainID: "chain-a",
		SessionScopeHash:  strings.Repeat("a", 64),
		SessionEpoch:      3,
	}
	baseKeys := openAIAccountAdmissionKeys(base)
	if !strings.Contains(baseKeys[0], "persona:opencode:slot:1:generation:2:set_generation:5:session:") {
		t.Fatalf("persona queue key missing binding metadata: %q", baseKeys[0])
	}
	if got, want := baseKeys[10], "openai:admission:{17}:persona:opencode:slot:1:rate_state"; got != want {
		t.Fatalf("persona rate key = %q, want %q", got, want)
	}

	variants := []service.OpenAIAccountAdmissionTicket{
		func() service.OpenAIAccountAdmissionTicket { v := base; v.Persona = "codex_cli_strict"; return v }(),
		func() service.OpenAIAccountAdmissionTicket { v := base; v.SlotID = 0; return v }(),
		func() service.OpenAIAccountAdmissionTicket { v := base; v.SlotGeneration = 3; return v }(),
		func() service.OpenAIAccountAdmissionTicket { v := base; v.SlotSetGeneration = 6; return v }(),
	}
	for i, variant := range variants {
		variantKeys := openAIAccountAdmissionKeys(variant)
		if variantKeys[0] == baseKeys[0] {
			t.Fatalf("variant %d was not isolated in queue: %q", i, variantKeys[0])
		}
		if i < 2 && variantKeys[10] == baseKeys[10] {
			t.Fatalf("variant %d was not isolated in rate bucket: %q", i, variantKeys[10])
		}
		if i >= 2 && variantKeys[10] != baseKeys[10] {
			t.Fatalf("variant %d unexpectedly reset rate bucket: %q", i, variantKeys[10])
		}
	}

	// Credential chains are intentionally metadata only here: RPM and queue
	// semantics are partitioned by Persona/slot generation, not token refresh
	// chain identity.
	otherChain := base
	otherChain.CredentialChainID = "chain-b"
	otherChainKeys := openAIAccountAdmissionKeys(otherChain)
	if otherChainKeys[0] != baseKeys[0] || otherChainKeys[10] != baseKeys[10] {
		t.Fatalf("credential chain unexpectedly changed admission partition: queue=%q rate=%q", otherChainKeys[0], otherChainKeys[10])
	}
}

func TestOpenAIAccountAdmissionKeysUseStableAccountPersonaRateScope(t *testing.T) {
	base := service.OpenAIAccountAdmissionTicket{
		AccountID: 17, AccountPersonaID: 101, PersonaGeneration: 4,
		CredentialChainID: "chain-a", SessionEpoch: 7,
		SessionScopeHash: strings.Repeat("b", 64),
	}
	keys := openAIAccountAdmissionKeys(base)
	if got, want := keys[10], "openai:admission:{17}:account_persona:101:rate_state"; got != want {
		t.Fatalf("dynamic Persona rate key = %q, want %q", got, want)
	}
	if !strings.Contains(keys[0], "account_persona:101:generation:4:epoch:7:chain:chain-a:session:") {
		t.Fatalf("dynamic Persona queue key missing scope: %q", keys[0])
	}
	otherGeneration := base
	otherGeneration.PersonaGeneration++
	otherKeys := openAIAccountAdmissionKeys(otherGeneration)
	if otherKeys[0] == keys[0] || otherKeys[10] != keys[10] {
		t.Fatalf("generation must split queue but retain rate debt: queue=%q rate=%q", otherKeys[0], otherKeys[10])
	}
	otherPersona := base
	otherPersona.AccountPersonaID++
	otherPersonaKeys := openAIAccountAdmissionKeys(otherPersona)
	if otherPersonaKeys[0] == keys[0] || otherPersonaKeys[10] == keys[10] {
		t.Fatalf("AccountPersona must isolate queue and rate debt: queue=%q rate=%q", otherPersonaKeys[0], otherPersonaKeys[10])
	}
}

func TestOpenAIAccountAdmissionQueuePrioritizesInteractiveAndAgesBackground(t *testing.T) {
	ctx := context.Background()
	queue := newOpenAIAdmissionTestCache(t)
	now := time.Now()
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.InteractiveBurst = 2
	cfg.BackgroundAgingSeconds = 5
	background := service.OpenAIAccountAdmissionTicket{ID: "b", AccountID: 7, Class: service.OpenAIAdmissionBackground, EnqueuedAt: now.Add(-6 * time.Second), Deadline: now.Add(time.Minute), EstimatedTokens: 1}
	interactive := service.OpenAIAccountAdmissionTicket{ID: "i", AccountID: 7, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1}
	if err := queue.Enqueue(ctx, background, cfg); err != nil {
		t.Fatalf("enqueue background: %v", err)
	}
	queueImpl, ok := queue.(*openAIAccountAdmissionQueue)
	if !ok {
		t.Fatal("unexpected admission queue implementation")
	}
	if err := queueImpl.rdb.HSet(ctx, openAIAccountAdmissionKeys(background)[3], background.ID, now.Add(-6*time.Second).UnixMilli()).Err(); err != nil {
		t.Fatalf("age background ticket: %v", err)
	}
	if err := queue.Enqueue(ctx, interactive, cfg); err != nil {
		t.Fatalf("enqueue interactive: %v", err)
	}
	poll, err := queue.Poll(ctx, interactive, cfg)
	if err != nil {
		t.Fatalf("poll interactive: %v", err)
	}
	if poll.Selected {
		t.Fatal("aged background request must get the next grant")
	}
	poll, err = queue.Poll(ctx, background, cfg)
	if err != nil || !poll.Selected || poll.Delay > 0 {
		t.Fatalf("aged background was not selected: %+v, %v", poll, err)
	}
}

func TestOpenAIAccountAdmissionQueueServesBackgroundAfterInteractiveBurst(t *testing.T) {
	ctx := context.Background()
	queue := newOpenAIAdmissionTestCache(t)
	now := time.Now()
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.InteractiveBurst = 1
	cfg.BackgroundAgingSeconds = 30
	background := service.OpenAIAccountAdmissionTicket{ID: "background", AccountID: 8, Class: service.OpenAIAdmissionBackground, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1}
	interactive1 := service.OpenAIAccountAdmissionTicket{ID: "interactive-1", AccountID: 8, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1}
	interactive2 := service.OpenAIAccountAdmissionTicket{ID: "interactive-2", AccountID: 8, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1}
	for _, ticket := range []service.OpenAIAccountAdmissionTicket{background, interactive1, interactive2} {
		if err := queue.Enqueue(ctx, ticket, cfg); err != nil {
			t.Fatalf("enqueue %s: %v", ticket.ID, err)
		}
	}
	poll, err := queue.Poll(ctx, interactive1, cfg)
	if err != nil || !poll.Selected {
		t.Fatalf("first interactive request was not selected: %+v, %v", poll, err)
	}
	grant, err := queue.Grant(ctx, interactive1, cfg, 0)
	if err != nil || !grant.Granted {
		t.Fatalf("grant first interactive request: %+v, %v", grant, err)
	}
	poll, err = queue.Poll(ctx, interactive2, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if poll.Selected {
		t.Fatal("interactive burst must yield to an already waiting background request")
	}
	poll, err = queue.Poll(ctx, background, cfg)
	if err != nil || !poll.Selected {
		t.Fatalf("background request was not selected after burst: %+v, %v", poll, err)
	}
}

func TestOpenAIAccountAdmissionQueueSmoothsRPMAndTPM(t *testing.T) {
	ctx := context.Background()
	queue := newOpenAIAdmissionTestCache(t)
	now := time.Now()
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.RequestsPerMinute = 60
	cfg.TokensPerMinute = 600
	first := service.OpenAIAccountAdmissionTicket{ID: "first", AccountID: 9, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 10}
	second := service.OpenAIAccountAdmissionTicket{ID: "second", AccountID: 9, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 10}
	if err := queue.Enqueue(ctx, first, cfg); err != nil {
		t.Fatal(err)
	}
	if err := queue.Enqueue(ctx, second, cfg); err != nil {
		t.Fatal(err)
	}
	grant, err := queue.Grant(ctx, first, cfg, 0)
	if err != nil || !grant.Granted {
		t.Fatalf("first grant: %+v, %v", grant, err)
	}
	poll, err := queue.Poll(ctx, second, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !poll.Selected || poll.Delay < 900*time.Millisecond {
		t.Fatalf("second request was not smoothed: %+v", poll)
	}
}

func TestOpenAIAccountAdmissionQueueSeparatesSessionOrderButSharesAccountRate(t *testing.T) {
	ctx := context.Background()
	queue := newOpenAIAdmissionTestCache(t)
	now := time.Now()
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.RequestsPerMinute = 60
	first := service.OpenAIAccountAdmissionTicket{
		ID: "slot-0", AccountID: 19, SessionScopeHash: strings.Repeat("a", 64), SessionEpoch: 3,
		Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1,
	}
	second := service.OpenAIAccountAdmissionTicket{
		ID: "slot-1", AccountID: 19, SessionScopeHash: strings.Repeat("b", 64), SessionEpoch: 3,
		Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1,
	}
	for _, ticket := range []service.OpenAIAccountAdmissionTicket{first, second} {
		if err := queue.Enqueue(ctx, ticket, cfg); err != nil {
			t.Fatal(err)
		}
		poll, err := queue.Poll(ctx, ticket, cfg)
		if err != nil || !poll.Selected {
			t.Fatalf("session queue head not selected: %+v, %v", poll, err)
		}
	}
	grant, err := queue.Grant(ctx, first, cfg, 0)
	if err != nil || !grant.Granted {
		t.Fatalf("first session grant: %+v, %v", grant, err)
	}
	poll, err := queue.Poll(ctx, second, cfg)
	if err != nil || !poll.Selected || poll.Delay < 900*time.Millisecond {
		t.Fatalf("second session did not share account RPM debt: %+v, %v", poll, err)
	}
}

func TestOpenAIAccountAdmissionQueueKeepsPersonaRateDebtAcrossSlotGenerations(t *testing.T) {
	ctx := context.Background()
	queue := newOpenAIAdmissionTestCache(t)
	now := time.Now()
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.RequestsPerMinute = 60
	first := service.OpenAIAccountAdmissionTicket{
		ID: "generation-1", AccountID: 20, Persona: "opencode", SlotID: 1,
		SlotGeneration: 1, SlotSetGeneration: 1,
		Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1,
	}
	second := first
	second.ID = "generation-2"
	second.SlotGeneration = 2
	second.SlotSetGeneration = 2
	for _, ticket := range []service.OpenAIAccountAdmissionTicket{first, second} {
		if err := queue.Enqueue(ctx, ticket, cfg); err != nil {
			t.Fatalf("enqueue %s: %v", ticket.ID, err)
		}
	}
	poll, err := queue.Poll(ctx, first, cfg)
	if err != nil || !poll.Selected {
		t.Fatalf("first generation was not selected: %+v, %v", poll, err)
	}
	grant, err := queue.Grant(ctx, first, cfg, 0)
	if err != nil || !grant.Granted {
		t.Fatalf("first generation grant: %+v, %v", grant, err)
	}
	poll, err = queue.Poll(ctx, second, cfg)
	if err != nil || !poll.Selected || poll.Delay < 900*time.Millisecond {
		t.Fatalf("slot generation unexpectedly reset Persona rate debt: %+v, %v", poll, err)
	}
}

func TestOpenAIAccountAdmissionQueueGrantsOversizedTicketAndDefersFollowingRequests(t *testing.T) {
	ctx := context.Background()
	queue := newOpenAIAdmissionTestCache(t)
	now := time.Now()
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.TokensPerMinute = 100
	oversized := service.OpenAIAccountAdmissionTicket{ID: "oversized", AccountID: 12, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(2 * time.Minute), EstimatedTokens: 150}
	following := service.OpenAIAccountAdmissionTicket{ID: "following", AccountID: 12, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(2 * time.Minute), EstimatedTokens: 1}
	for _, ticket := range []service.OpenAIAccountAdmissionTicket{oversized, following} {
		if err := queue.Enqueue(ctx, ticket, cfg); err != nil {
			t.Fatalf("enqueue %s: %v", ticket.ID, err)
		}
	}

	poll, err := queue.Poll(ctx, oversized, cfg)
	if err != nil || !poll.Selected || poll.Delay > 0 {
		t.Fatalf("oversized head ticket was not immediately selectable: %+v, %v", poll, err)
	}
	grant, err := queue.Grant(ctx, oversized, cfg, 0)
	if err != nil || !grant.Granted {
		t.Fatalf("oversized head ticket was not granted: %+v, %v", grant, err)
	}

	poll, err = queue.Poll(ctx, following, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !poll.Selected || poll.Delay < 89*time.Second {
		t.Fatalf("following request did not inherit oversized TPM debt: %+v", poll)
	}
}

func TestOpenAIAccountAdmissionQueueCleansExpiredTicketsBeforeDepthCheck(t *testing.T) {
	ctx := context.Background()
	queue := newOpenAIAdmissionTestCache(t)
	now := time.Now()
	cfg := service.DefaultOpenAIAccountAdmissionConfig()
	cfg.MaxQueueDepthPerAccount = 1
	fresh := service.OpenAIAccountAdmissionTicket{ID: "fresh", AccountID: 10, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1}
	queueImpl, ok := queue.(*openAIAccountAdmissionQueue)
	if !ok {
		t.Fatal("unexpected admission queue implementation")
	}
	keys := openAIAccountAdmissionKeys(fresh)
	pipe := queueImpl.rdb.TxPipeline()
	pipe.ZAdd(ctx, keys[0], redis.Z{Score: 1, Member: "expired"})
	pipe.ZAdd(ctx, keys[2], redis.Z{Score: float64(now.Add(-time.Second).UnixMilli()), Member: "expired"})
	pipe.HSet(ctx, keys[3], "expired", now.Add(-time.Minute).UnixMilli())
	pipe.HSet(ctx, keys[4], "expired", 1)
	pipe.HSet(ctx, keys[5], "expired", string(service.OpenAIAdmissionInteractive))
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("seed expired ticket: %v", err)
	}
	if err := queue.Enqueue(ctx, fresh, cfg); err != nil {
		t.Fatalf("expired ticket blocked queue depth: %v", err)
	}
	poll, err := queue.Poll(ctx, fresh, cfg)
	if err != nil || !poll.Selected {
		t.Fatalf("fresh ticket was not selected: %+v, %v", poll, err)
	}
}

func TestOpenAIAccountAdmissionQueueDisabledRateDimensionIgnoresOldTAT(t *testing.T) {
	ctx := context.Background()
	queue := newOpenAIAdmissionTestCache(t)
	now := time.Now()
	limited := service.DefaultOpenAIAccountAdmissionConfig()
	limited.RequestsPerMinute = 1
	first := service.OpenAIAccountAdmissionTicket{ID: "limited", AccountID: 11, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1}
	if err := queue.Enqueue(ctx, first, limited); err != nil {
		t.Fatal(err)
	}
	grant, err := queue.Grant(ctx, first, limited, 0)
	if err != nil || !grant.Granted {
		t.Fatalf("grant limited ticket: %+v, %v", grant, err)
	}

	unlimited := service.DefaultOpenAIAccountAdmissionConfig()
	second := service.OpenAIAccountAdmissionTicket{ID: "unlimited", AccountID: 11, Class: service.OpenAIAdmissionInteractive, EnqueuedAt: now, Deadline: now.Add(time.Minute), EstimatedTokens: 1}
	if err := queue.Enqueue(ctx, second, unlimited); err != nil {
		t.Fatal(err)
	}
	poll, err := queue.Poll(ctx, second, unlimited)
	if err != nil || !poll.Selected || poll.Delay > 0 {
		t.Fatalf("disabled RPM dimension retained stale delay: %+v, %v", poll, err)
	}
}
