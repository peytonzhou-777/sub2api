package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func TestDefaultOpenAIAccountAdmissionConfig(t *testing.T) {
	cfg := DefaultOpenAIAccountAdmissionConfig()
	if cfg.Enabled || cfg.QueueEnabled {
		t.Fatalf("new admission control must default to disabled: %+v", cfg)
	}
	if cfg.MaxWaitSeconds != 45 || cfg.JitterMinMS != 100 || cfg.JitterMaxMS != 500 {
		t.Fatalf("unexpected queue defaults: %+v", cfg)
	}
	if cfg.InteractiveBurst != 4 || cfg.BackgroundAgingSeconds != 5 {
		t.Fatalf("unexpected priority defaults: %+v", cfg)
	}
}

func TestValidateOpenAIAccountAdmissionConfig(t *testing.T) {
	cases := []struct {
		name string
		edit func(*OpenAIAccountAdmissionConfig)
	}{
		{"max wait", func(c *OpenAIAccountAdmissionConfig) { c.MaxWaitSeconds = 121 }},
		{"rpm", func(c *OpenAIAccountAdmissionConfig) { c.RequestsPerMinute = -1 }},
		{"tpm", func(c *OpenAIAccountAdmissionConfig) { c.TokensPerMinute = 100000001 }},
		{"jitter order", func(c *OpenAIAccountAdmissionConfig) { c.JitterMaxMS = c.JitterMinMS - 1 }},
		{"queue depth", func(c *OpenAIAccountAdmissionConfig) { c.MaxQueueDepthPerAccount = 0 }},
		{"interactive burst", func(c *OpenAIAccountAdmissionConfig) { c.InteractiveBurst = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultOpenAIAccountAdmissionConfig()
			tc.edit(&cfg)
			if _, err := ValidateOpenAIAccountAdmissionConfig(cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestOpenAIAccountAdmissionConfigVersionCAS(t *testing.T) {
	repo := &openAIUserAffinitySettingRepoStub{values: map[string]string{}}
	svc := NewSettingService(repo, nil)
	initial, err := svc.GetOpenAIAccountAdmissionConfig(context.Background())
	if err != nil {
		t.Fatalf("get default config: %v", err)
	}
	next := initial
	next.Enabled = true
	next.QueueEnabled = true
	updated, err := svc.UpdateOpenAIAccountAdmissionConfig(context.Background(), next, initial.ConfigVersion)
	if err != nil {
		t.Fatalf("update config: %v", err)
	}
	if updated.ConfigVersion != 1 || !updated.Enabled || !updated.QueueEnabled {
		t.Fatalf("unexpected update: %+v", updated)
	}
	_, err = svc.UpdateOpenAIAccountAdmissionConfig(context.Background(), next, initial.ConfigVersion)
	if err == nil || !infraerrors.IsConflict(err) {
		t.Fatal("expected stale version conflict")
	}
}
