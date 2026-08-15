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
}

func (s *openAIUserAffinityAdminStoreStub) ListOpenAIUserAffinityResidents(context.Context, int64, int, int) ([]OpenAIUserAffinityResident, int64, error) {
	return nil, 0, nil
}
func (s *openAIUserAffinityAdminStoreStub) GetOpenAIUserAffinityUserDetail(context.Context, int64, int) (*OpenAIUserAffinityUserDetail, error) {
	return nil, nil
}
func (s *openAIUserAffinityAdminStoreStub) ResetOpenAIUserAffinityPlacement(_ context.Context, _, _ int64, _, _ string, excludeSource bool) error {
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

func TestOpenAIUserAffinityConfigVersionCAS(t *testing.T) {
	repo := &openAIUserAffinitySettingRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, nil)
	initial, err := svc.GetOpenAIUserAffinityConfig(context.Background())
	if err != nil {
		t.Fatalf("get default config: %v", err)
	}
	next := initial
	next.Enabled = true
	updated, err := svc.UpdateOpenAIUserAffinityConfig(context.Background(), next, initial.ConfigVersion, "enable affinity scheduling")
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	if updated.ConfigVersion != 1 || !updated.Enabled {
		t.Fatalf("unexpected update: %+v", updated)
	}
	_, err = svc.UpdateOpenAIUserAffinityConfig(context.Background(), next, initial.ConfigVersion, "stale update")
	if err == nil || !infraerrors.IsConflict(err) {
		t.Fatal("expected stale version conflict")
	}
	loaded, err := svc.GetOpenAIUserAffinityConfig(context.Background())
	if err != nil || loaded.ConfigVersion != 1 || !loaded.Enabled {
		t.Fatalf("stored config changed unexpectedly: %+v, %v", loaded, err)
	}
}

func TestOpenAIUserAffinityConfigRequiresReason(t *testing.T) {
	repo := &openAIUserAffinitySettingRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, nil)
	cfg := DefaultOpenAIUserAffinityConfig()
	if _, err := svc.UpdateOpenAIUserAffinityConfig(context.Background(), cfg, 0, " "); err == nil {
		t.Fatal("expected reason validation error")
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

	if err := adminService.ResetOpenAIUserAffinityPlacement(context.Background(), 42, 7, "openai", "manual", false); err != nil {
		t.Fatalf("reset placement: %v", err)
	}
	if !store.excludeSource {
		t.Fatal("configured source exclusion was not applied")
	}
}
