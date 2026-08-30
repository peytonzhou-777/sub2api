package service

import (
	"context"
	"encoding/json"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type openAIUserAffinityAdminStoreStub struct {
	AccountRepository
	excludeSource bool
	scopeKey      string
}

func (s *openAIUserAffinityAdminStoreStub) ListOpenAIUserAffinityResidents(context.Context, int64, int, int) ([]OpenAIUserAffinityResident, int64, error) {
	return nil, 0, nil
}
func (s *openAIUserAffinityAdminStoreStub) GetOpenAIUserAffinityUserDetail(context.Context, int64, int) (*OpenAIUserAffinityUserDetail, error) {
	return nil, nil
}
func (s *openAIUserAffinityAdminStoreStub) ResetOpenAIUserAffinityPlacement(_ context.Context, _, _ int64, scopeKey string, excludeSource bool) error {
	s.scopeKey = scopeKey
	s.excludeSource = excludeSource
	return nil
}
func (s *openAIUserAffinityAdminStoreStub) GetOpenAIUserAffinityAccountPolicy(context.Context, int64) (*OpenAIUserAffinityAccountPolicy, error) {
	return nil, nil
}
func (s *openAIUserAffinityAdminStoreStub) UpdateOpenAIUserAffinityAccountPolicy(context.Context, OpenAIUserAffinityAccountPolicy) error {
	return nil
}

type openAIUserAffinitySettingRepoStub struct {
	values map[string]string
}

func (r *openAIUserAffinitySettingRepoStub) Get(ctx context.Context, key string) (*Setting, error) {
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *openAIUserAffinitySettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *openAIUserAffinitySettingRepoStub) Set(ctx context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *openAIUserAffinitySettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *openAIUserAffinitySettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *openAIUserAffinitySettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return r.values, nil
}
func (r *openAIUserAffinitySettingRepoStub) Delete(context.Context, string) error { return nil }

func TestDefaultOpenAIUserAffinityConfig(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	if cfg.Enabled || cfg.DefaultMaxContactUsers != 10 || cfg.DefaultNewResidentCooldownSeconds != 300 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.ResidentAccountSlotCount != 1 || cfg.ResidentTTLSeconds != 7*24*60*60 || cfg.ConversationActiveTTLSeconds != 60*60 {
		t.Fatalf("unexpected multi-slot defaults: %+v", cfg)
	}
	if cfg.RuntimeResidentAccountSlotCount() != 1 {
		t.Fatalf("P1 runtime slot count = %d, want 1", cfg.RuntimeResidentAccountSlotCount())
	}
	if cfg.ConfigVersion != 0 {
		t.Fatalf("default version = %d, want 0", cfg.ConfigVersion)
	}
}

func TestValidateAndNormalizeOpenAIUserAffinityConfig(t *testing.T) {
	cfg := DefaultOpenAIUserAffinityConfig()
	cfg.Mode = " SHADOW "
	cfg.BestFitStrategy = "5H_THEN_7D"
	cfg.TouchSuccessMode = "RESPONSE_COMPLETED"
	normalized, err := ValidateAndNormalizeOpenAIUserAffinityConfig(cfg)
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if normalized.Mode != OpenAIUserAffinityModeShadow || normalized.BestFitStrategy != OpenAIUserAffinityBestFit5HThen7D || normalized.TouchSuccessMode != OpenAIUserAffinityTouchCompleted {
		t.Fatalf("unexpected normalized config: %+v", normalized)
	}

	cases := []struct {
		name string
		edit func(*OpenAIUserAffinityConfig)
	}{
		{"max contact", func(c *OpenAIUserAffinityConfig) { c.DefaultMaxContactUsers = 0 }},
		{"jitter", func(c *OpenAIUserAffinityConfig) { c.FollowerJitterMaxMS = c.FollowerJitterMinMS - 1 }},
		{"strategy", func(c *OpenAIUserAffinityConfig) { c.BestFitStrategy = "other" }},
		{"resident slots", func(c *OpenAIUserAffinityConfig) { c.ResidentAccountSlotCount = 6 }},
		{"resident ttl", func(c *OpenAIUserAffinityConfig) { c.ResidentTTLSeconds = 60 }},
		{"conversation ttl", func(c *OpenAIUserAffinityConfig) { c.ConversationActiveTTLSeconds = 60 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			invalid := DefaultOpenAIUserAffinityConfig()
			tc.edit(&invalid)
			if _, err := ValidateAndNormalizeOpenAIUserAffinityConfig(invalid); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestOpenAIUserAffinityLegacyConfigFillsMultiSlotDefaults(t *testing.T) {
	repo := &openAIUserAffinitySettingRepoStub{values: map[string]string{
		SettingKeyOpenAIUserAffinityScheduling: `{
			"enabled":true,
			"mode":"enforce",
			"quota_reserve_ratio_5h":0.1,
			"quota_reserve_ratio_7d":0.1,
			"cold_start_demand_quantile":0.75,
			"best_fit_strategy":"7d_then_5h",
			"best_fit_close_tolerance_ratio":0.01,
			"default_max_contact_users":10,
			"default_new_resident_cooldown_seconds":300,
			"resident_reentry_overcommit_enabled":true,
			"capacity_failure_migration_threshold":3,
			"capacity_failure_window_seconds":60,
			"migration_stability_seconds":60,
			"follower_jitter_min_ms":100,
			"follower_jitter_max_ms":500,
			"touch_success_mode":"upstream_accepted",
			"config_version":9
		}`,
	}}
	svc := NewSettingService(repo, nil)
	cfg, err := svc.GetOpenAIUserAffinityConfig(context.Background())
	if err != nil {
		t.Fatalf("get legacy config: %v", err)
	}
	if cfg.ResidentAccountSlotCount != 1 || cfg.ResidentTTLSeconds != 604800 || cfg.ConversationActiveTTLSeconds != 3600 {
		t.Fatalf("legacy defaults were not filled: %+v", cfg)
	}
	if cfg.ConfigVersion != 9 || !cfg.Enabled {
		t.Fatalf("legacy fields changed unexpectedly: %+v", cfg)
	}
}

func TestOpenAIUserAffinityConfigVersionCAS(t *testing.T) {
	repo := &openAIUserAffinitySettingRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, nil)
	initial, err := svc.GetOpenAIUserAffinityConfig(context.Background())
	if err != nil {
		t.Fatalf("get default config: %v", err)
	}
	next := initial
	next.Enabled = true
	updated, err := svc.UpdateOpenAIUserAffinityConfig(context.Background(), next, initial.ConfigVersion)
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	if updated.ConfigVersion != 1 || !updated.Enabled {
		t.Fatalf("unexpected update: %+v", updated)
	}
	_, err = svc.UpdateOpenAIUserAffinityConfig(context.Background(), next, initial.ConfigVersion)
	if err == nil || !infraerrors.IsConflict(err) {
		t.Fatal("expected stale version conflict")
	}
	loaded, err := svc.GetOpenAIUserAffinityConfig(context.Background())
	if err != nil || loaded.ConfigVersion != 1 || !loaded.Enabled {
		t.Fatalf("stored config changed unexpectedly: %+v, %v", loaded, err)
	}
}

func TestOpenAIUserAffinityManualResetUsesConfiguredSourceExclusion(t *testing.T) {
	config := DefaultOpenAIUserAffinityConfig()
	config.ManualResetExcludeSourceAccount = true
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	settingRepo := &openAIUserAffinitySettingRepoStub{values: map[string]string{
		SettingKeyOpenAIUserAffinityScheduling: string(raw),
	}}
	store := &openAIUserAffinityAdminStoreStub{}
	adminService := &adminServiceImpl{
		accountRepo: store, settingService: NewSettingService(settingRepo, nil),
	}

	if err := adminService.ResetOpenAIUserAffinityPlacement(context.Background(), 42, 7, "openai", false); err != nil {
		t.Fatalf("reset placement: %v", err)
	}
	if !store.excludeSource {
		t.Fatal("configured source exclusion was not applied")
	}
}

func TestOpenAIUserAffinityManualResetAllowsAllScopes(t *testing.T) {
	config := DefaultOpenAIUserAffinityConfig()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	settingRepo := &openAIUserAffinitySettingRepoStub{values: map[string]string{
		SettingKeyOpenAIUserAffinityScheduling: string(raw),
	}}
	store := &openAIUserAffinityAdminStoreStub{}
	adminService := &adminServiceImpl{
		accountRepo: store, settingService: NewSettingService(settingRepo, nil),
	}

	if err := adminService.ResetOpenAIUserAffinityPlacement(context.Background(), 42, 7, "", false); err != nil {
		t.Fatalf("reset all scopes: %v", err)
	}
	if store.scopeKey != "" {
		t.Fatalf("all-scope reset must pass an empty scope key, got %q", store.scopeKey)
	}
}
