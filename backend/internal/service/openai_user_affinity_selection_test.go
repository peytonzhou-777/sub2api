package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestSelectOpenAIUserAffinityCandidateBestFit7DThen5D(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	selected, ok := SelectOpenAIUserAffinityCandidate(cfg, []OpenAIUserAffinityCandidate{
		{AccountID: 1, Available7DRatio: 0.70, Available5HRatio: 0.80, Quota5HKnown: true, Quota7DKnown: true},
		{AccountID: 2, Available7DRatio: 0.60, Available5HRatio: 0.95, Quota5HKnown: true, Quota7DKnown: true},
	}, 0, 0, time.Now())
	if !ok || selected.AccountID != 2 {
		t.Fatalf("selected=%+v ok=%v, want account 2", selected, ok)
	}
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
