package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSelectOpenAIUserAffinityCandidatePrefersMostRemaining7D(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 1, Available7DRatio: 0.70, Available5HRatio: 0.80, Quota5HKnown: true, Quota7DKnown: true},
		{AccountID: 2, Available7DRatio: 0.60, Available5HRatio: 0.95, Quota5HKnown: true, Quota7DKnown: true},
	}, 0, 0, time.Now())
	if !ok || selected.AccountID != 1 {
		t.Fatalf("selected=%+v ok=%v, want account 1", selected, ok)
	}
}

func TestSelectOpenAIUserAffinityCandidatePrefersFewerContactsWithinPrimaryTolerance(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 1, Available7DRatio: 0.70, Available5HRatio: 0.95, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 8},
		{AccountID: 2, Available7DRatio: 0.695, Available5HRatio: 0.60, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 1},
	}, 0, 0, time.Now())
	require.True(t, ok)
	require.Equal(t, int64(2), selected.AccountID)
}

func TestSelectOpenAIUserAffinityCandidateKeepsQuotaPriorityOutsideTolerance(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 1, Available7DRatio: 0.70, Available5HRatio: 0.80, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 8},
		{AccountID: 2, Available7DRatio: 0.68, Available5HRatio: 0.95, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 1},
	}, 0, 0, time.Now())
	require.True(t, ok)
	require.Equal(t, int64(1), selected.AccountID)
}

func TestSelectOpenAIUserAffinityCandidateUsesSecondaryQuotaAfterContactCount(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 2, Available7DRatio: 0.695, Available5HRatio: 0.90, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 2},
		{AccountID: 1, Available7DRatio: 0.70, Available5HRatio: 0.80, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 2},
	}, 0, 0, time.Now())
	require.True(t, ok)
	require.Equal(t, int64(2), selected.AccountID)
}

func TestSelectOpenAIUserAffinityCandidateSupports5HPrimary(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	cfg.BestFitStrategy = OpenAIUserAffinityBestFit5HThen7D
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 1, Available7DRatio: 0.40, Available5HRatio: 0.80, Quota5HKnown: true, Quota7DKnown: true},
		{AccountID: 2, Available7DRatio: 0.90, Available5HRatio: 0.70, Quota5HKnown: true, Quota7DKnown: true},
	}, 0, 0, time.Now())
	require.True(t, ok)
	require.Equal(t, int64(1), selected.AccountID)
}

func TestSelectOpenAIUserAffinityCandidateCreditsQuotaWindowNearReset(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	now := time.Now().UTC()
	nearReset := now.Add(time.Hour)
	farReset := now.Add(6 * 24 * time.Hour)
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{
			AccountID: 1, Available7DRatio: 0.70, Available5HRatio: 0.8,
			Quota5HKnown: true, Quota7DKnown: true, Quota7DWindowMinutes: 10080, Quota7DResetAt: &farReset,
		},
		{
			AccountID: 2, Available7DRatio: 0.60, Available5HRatio: 0.8,
			Quota5HKnown: true, Quota7DKnown: true, Quota7DWindowMinutes: 10080, Quota7DResetAt: &nearReset,
		},
	}, 0.05, 0.05, now)
	require.True(t, ok)
	require.Equal(t, int64(2), selected.AccountID, "更早重置的 7d 窗口应获得 renewal credit")
}

func TestSelectOpenAIUserAffinityCandidateIsDeterministicAcrossInputOrder(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	account1 := OpenAIUserAffinityCandidate{AccountID: 1, Available7DRatio: 0.70, Available5HRatio: 0.80, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 2}
	account2 := OpenAIUserAffinityCandidate{AccountID: 2, Available7DRatio: 0.70, Available5HRatio: 0.80, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 2}

	for _, candidates := range [][]OpenAIUserAffinityCandidate{{account1, account2}, {account2, account1}} {
		selected, ok := SelectOpenAIUserAffinityCandidate(cfg, candidates, 0, 0, time.Now())
		require.True(t, ok)
		require.Equal(t, int64(1), selected.AccountID)
	}
}

func TestSortOpenAIUserResidentSlotsUsesDecayedScore(t *testing.T) {
	now := time.Now().UTC()
	halfLife := 7 * 24 * time.Hour
	recentSuccess := now.Add(-time.Hour)
	slots := []OpenAIUserResidentSlot{
		{AccountID: 1, UsageScore: 8, ScoreUpdatedAt: now.Add(-4 * halfLife), AdmittedAt: now.Add(-30 * 24 * time.Hour)},
		{AccountID: 2, UsageScore: 1, ScoreUpdatedAt: now, LastSuccessAt: &recentSuccess, AdmittedAt: now.Add(-24 * time.Hour)},
	}

	sortOpenAIUserResidentSlots(slots, halfLife, now)

	require.Equal(t, int64(2), slots[0].AccountID, "旧高分应按半衰期衰减后再参与排序")
}

func TestClassifyOpenAIUserAffinityResidentAdmission(t *testing.T) {
	now := time.Now().UTC()
	baseExtra := map[string]any{
		"codex_usage_updated_at":  now.Format(time.RFC3339Nano),
		"codex_5h_reset_at":       now.Add(time.Hour).Format(time.RFC3339Nano),
		"codex_7d_reset_at":       now.Add(24 * time.Hour).Format(time.RFC3339Nano),
		"codex_5h_used_percent":   95.0,
		"codex_7d_used_percent":   95.0,
		"codex_5h_window_minutes": 300,
		"codex_7d_window_minutes": 10080,
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{RunMode: config.RunModeSimple}}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: baseExtra}
	if got := svc.classifyOpenAIUserAffinityResidentAdmission(context.Background(), account, nil, "", false, "", "", OpenAIUpstreamTransportHTTPSSE); got != openAIUserAffinityResidentAllowed {
		t.Fatalf("reserve-zone resident admission=%s, want allowed", got)
	}

	account.Extra = cloneMapAny(baseExtra)
	account.Extra["codex_5h_used_percent"] = 100.0
	if got := svc.classifyOpenAIUserAffinityResidentAdmission(context.Background(), account, nil, "", false, "", "", OpenAIUpstreamTransportHTTPSSE); got != openAIUserAffinityResidentTemporaryCapacity {
		t.Fatalf("5h exhausted admission=%s, want temporary", got)
	}

	account.Extra = cloneMapAny(baseExtra)
	account.Extra["codex_7d_used_percent"] = 100.0
	if got := svc.classifyOpenAIUserAffinityResidentAdmission(context.Background(), account, nil, "", false, "", "", OpenAIUpstreamTransportHTTPSSE); got != openAIUserAffinityResidentQuota7DExhausted {
		t.Fatalf("7d exhausted admission=%s, want direct migration", got)
	}

	account.Extra = cloneMapAny(baseExtra)
	svc.openaiAccountRuntimeBlockUntil.Store(account.ID, time.Now().Add(time.Minute))
	if got := svc.classifyOpenAIUserAffinityResidentAdmission(context.Background(), account, nil, "", false, "", "", OpenAIUpstreamTransportHTTPSSE); got != openAIUserAffinityResidentTemporaryCapacity {
		t.Fatalf("runtime-blocked resident admission=%s, want temporary capacity", got)
	}
}

func cloneMapAny(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func TestSelectOpenAIUserAffinityCandidateUsesContactCountAndCooldown(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	cooldown := time.Now().Add(time.Minute)
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 1, Available7DRatio: 0.9, Available5HRatio: 0.9, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 10},
		{AccountID: 2, Available7DRatio: 0.8, Available5HRatio: 0.8, Quota5HKnown: true, Quota7DKnown: true, CooldownUntil: &cooldown},
		{AccountID: 3, Available7DRatio: 0.7, Available5HRatio: 0.7, Quota5HKnown: true, Quota7DKnown: true},
	}, 0, 0, time.Now())
	if !ok || selected.AccountID != 3 {
		t.Fatalf("selected=%+v ok=%v, want account 3", selected, ok)
	}
}

func TestSelectOpenAIUserAffinityCandidateCapacityAndOverride(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 1, Available7DRatio: 0.5, Available5HRatio: 0.5, Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 1, MaxContactUsers: 1},
		{AccountID: 2, Available7DRatio: 0.8, Available5HRatio: 0.8, Quota5HKnown: true, Quota7DKnown: true},
	}, 0.5, 0.5, time.Now())
	if !ok || selected.AccountID != 2 {
		t.Fatalf("selected=%+v ok=%v, want account 2", selected, ok)
	}
}

func TestSelectOpenAIUserAffinityCandidateAllowsAlreadyCountedUserAtCapacity(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{
			AccountID: 1, Available7DRatio: 0.5, Available5HRatio: 0.5,
			Quota5HKnown: true, Quota7DKnown: true, ActiveContactUsers: 10,
			MaxContactUsers: 10, UserAlreadyActive: true,
		},
	}, 0.1, 0.1, time.Now())
	if !ok || selected.AccountID != 1 {
		t.Fatalf("selected=%+v ok=%v, want already-counted account 1", selected, ok)
	}
}

func TestSelectOpenAIUserAffinityCandidatePrefersResidentAcrossScopesAndBypassesCooldown(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	now := time.Now().UTC()
	cooldown := now.Add(time.Minute)
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 11, Quota5HKnown: true, Quota7DKnown: true, Available5HRatio: 0.9, Available7DRatio: 0.9},
		{AccountID: 12, Quota5HKnown: true, Quota7DKnown: true, Available5HRatio: 0.8, Available7DRatio: 0.8,
			ActiveContactUsers: cfg.DefaultMaxContactUsers, UserAlreadyResident: true, CooldownUntil: &cooldown},
	}, 0.05, 0.05, now)
	require.True(t, ok)
	require.Equal(t, int64(12), selected.AccountID)
}

func TestSelectOpenAIUserAffinityCandidateManualResetUsesStrictNewResidentBestFit(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	now := time.Now().UTC()
	candidates := []OpenAIUserAffinityCandidate{
		{AccountID: 11, Quota5HKnown: true, Quota7DKnown: true, Available5HRatio: 0.5, Available7DRatio: 0.5, UserAlreadyActive: true},
		{AccountID: 12, Quota5HKnown: true, Quota7DKnown: true, Available5HRatio: 0.9, Available7DRatio: 0.9},
	}

	residentPreferred, ok := SelectOpenAIUserAffinityCandidate(cfg, candidates, 0.05, 0.05, now)
	require.True(t, ok)
	require.Equal(t, int64(11), residentPreferred.AccountID)

	bestFit, ok := selectOpenAIUserAffinityCandidate(cfg, candidates, 0.05, 0.05, now, false)
	require.True(t, ok)
	require.Equal(t, int64(12), bestFit.AccountID)
}

func TestSelectOpenAIUserAffinityCandidateManualResetKeepsAlreadyCountedCapacitySemantics(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	now := time.Now().UTC()
	cooldown := now.Add(time.Minute)
	candidates := []OpenAIUserAffinityCandidate{
		{
			AccountID: 11, Quota5HKnown: true, Quota7DKnown: true, Available5HRatio: 0.95, Available7DRatio: 0.95,
			ActiveContactUsers: cfg.DefaultMaxContactUsers, UserAlreadyActive: true, CooldownUntil: &cooldown,
		},
		{AccountID: 12, Quota5HKnown: true, Quota7DKnown: true, Available5HRatio: 0.8, Available7DRatio: 0.8},
	}

	selected, ok := selectOpenAIUserAffinityCandidate(cfg, candidates, 0.05, 0.05, now, false)
	require.True(t, ok)
	require.Equal(t, int64(11), selected.AccountID)
}

func TestSelectOpenAIUserAffinityCandidateRejectsUnknownQuotaWindow(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 1, Available7DRatio: 1, Available5HRatio: 1, Quota5HKnown: false, Quota7DKnown: true},
		{AccountID: 2, Available7DRatio: 0.8, Available5HRatio: 0.8, Quota5HKnown: true, Quota7DKnown: true},
	}, 0.05, 0.05, time.Now())
	if !ok || selected.AccountID != 2 {
		t.Fatalf("selected=%+v ok=%v, want account 2", selected, ok)
	}
}

func TestSelectOpenAIUserAffinityCandidateAllows7DOnlyAccount(t *testing.T) {
	now := time.Now().UTC()
	extra := map[string]any{
		"codex_usage_updated_at":  now.Format(time.RFC3339Nano),
		"codex_7d_window_minutes": 10080,
		"codex_7d_used_percent":   20.0,
		"codex_7d_reset_at":       now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	}
	available5H, known5H := readOpenAIUserAffinityQuotaAvailableRatio(extra, "5h", now)
	available7D, known7D := readOpenAIUserAffinityQuotaAvailableRatio(extra, "7d", now)

	selected, ok := SelectOpenAIUserAffinityCandidate(DefaultOpenAIUserAffinityConfig(), []OpenAIUserAffinityCandidate{
		{
			AccountID: 1, Available5HRatio: available5H, Available7DRatio: available7D,
			Quota5HKnown: known5H, Quota7DKnown: known7D,
		},
	}, 0.05, 0.05, now)
	require.True(t, ok)
	require.Equal(t, int64(1), selected.AccountID)
	require.True(t, known5H, "缺失 5h 窗口应视为已知无限制")
	require.Equal(t, 1.0, available5H)
}

func TestOpenAIUserAffinityScopeKeySeparatesGroupsAndCapabilityLanes(t *testing.T) {
	groupA, groupB := int64(10), int64(11)
	textA := openAIUserAffinityScopeKey(&groupA, false, OpenAIEndpointCapabilityChatCompletions, "", OpenAIUpstreamTransportHTTPSSE)
	textB := openAIUserAffinityScopeKey(&groupB, false, OpenAIEndpointCapabilityChatCompletions, "", OpenAIUpstreamTransportHTTPSSE)
	imagesA := openAIUserAffinityScopeKey(&groupA, false, "", OpenAIImagesCapabilityBasic, OpenAIUpstreamTransportHTTPSSE)
	if textA == textB || textA == imagesA {
		t.Fatalf("scope keys must isolate groups and capability lanes: %q %q %q", textA, textB, imagesA)
	}
}

func TestOpenAIUserAffinityMigrationStable(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	cfg.MigrationStabilitySeconds = 60
	now := time.Now().UTC()
	if openAIUserAffinityMigrationStable(cfg, nil, now) {
		t.Fatal("nil authorization must not permit migration")
	}
	authorizedAt := now.Add(-59 * time.Second)
	if openAIUserAffinityMigrationStable(cfg, &authorizedAt, now) {
		t.Fatal("migration must wait for the full stability period")
	}
	authorizedAt = now.Add(-60 * time.Second)
	if !openAIUserAffinityMigrationStable(cfg, &authorizedAt, now) {
		t.Fatal("migration should be permitted at the stability boundary")
	}
}
