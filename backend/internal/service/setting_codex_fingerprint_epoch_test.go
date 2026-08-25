package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexFingerprintEpochSettingRepo struct {
	values map[string]string
	err    error
}

func (r *codexFingerprintEpochSettingRepo) Get(context.Context, string) (*Setting, error) {
	panic("unexpected Get call")
}

func (r *codexFingerprintEpochSettingRepo) GetValue(context.Context, string) (string, error) {
	panic("unexpected GetValue call")
}

func (r *codexFingerprintEpochSettingRepo) Set(context.Context, string, string) error {
	panic("unexpected Set call")
}

func (r *codexFingerprintEpochSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *codexFingerprintEpochSettingRepo) SetMultiple(context.Context, map[string]string) error {
	panic("unexpected SetMultiple call")
}

func (r *codexFingerprintEpochSettingRepo) GetAll(context.Context) (map[string]string, error) {
	panic("unexpected GetAll call")
}

func (r *codexFingerprintEpochSettingRepo) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func testCodexFingerprintEpochConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Gateway.CodexFingerprintMinSessionAgeHours = 80
	cfg.Gateway.CodexFingerprintMaxSessionAgeHours = 180
	cfg.Gateway.CodexFingerprintRotationJitterHours = 12
	cfg.Gateway.CodexFingerprintIdleGateMinutes = 90
	cfg.Gateway.CodexFingerprintOldEpochGraceHours = 36
	return cfg
}

func TestGetCodexFingerprintEpochPolicyMergesDatabaseAndStaticFallback(t *testing.T) {
	repo := &codexFingerprintEpochSettingRepo{values: map[string]string{
		SettingKeyCodexFingerprintMaxSessionAgeHours: "240",
		SettingKeyCodexFingerprintIdleGateMinutes:    "30",
	}}
	svc := NewSettingService(repo, testCodexFingerprintEpochConfig())

	policy := svc.GetCodexFingerprintEpochPolicy(context.Background())
	require.Equal(t, CodexFingerprintEpochPolicy{
		MinSessionAgeHours:  80,
		MaxSessionAgeHours:  240,
		RotationJitterHours: 12,
		IdleGateMinutes:     30,
		OldEpochGraceHours:  36,
	}, policy)
}

func TestGetCodexFingerprintEpochPolicyRejectsPartialInvalidPolicyAsAWhole(t *testing.T) {
	repo := &codexFingerprintEpochSettingRepo{values: map[string]string{
		SettingKeyCodexFingerprintMinSessionAgeHours: "300",
		SettingKeyCodexFingerprintMaxSessionAgeHours: "200",
	}}
	svc := NewSettingService(repo, testCodexFingerprintEpochConfig())

	require.Equal(t, svc.codexFingerprintEpochPolicyFallback(), svc.GetCodexFingerprintEpochPolicy(context.Background()))
}

func TestGetCodexFingerprintEpochPolicyKeepsLastKnownGoodOnReadFailure(t *testing.T) {
	repo := &codexFingerprintEpochSettingRepo{values: map[string]string{
		SettingKeyCodexFingerprintMinSessionAgeHours: "96",
	}}
	svc := NewSettingService(repo, testCodexFingerprintEpochConfig())
	want := svc.GetCodexFingerprintEpochPolicy(context.Background())
	require.Equal(t, 96, want.MinSessionAgeHours)

	repo.err = errors.New("database unavailable")
	svc.codexFingerprintEpochCache.Store(&cachedCodexFingerprintEpochPolicy{
		policy:    want,
		expiresAt: time.Now().Add(-time.Second).UnixNano(),
	})
	svc.codexFingerprintEpochSF.Forget(codexFingerprintEpochPolicyCacheKey)
	require.Equal(t, want, svc.GetCodexFingerprintEpochPolicy(context.Background()))
}

func TestValidateCodexFingerprintEpochPolicyBounds(t *testing.T) {
	valid := defaultCodexFingerprintEpochPolicy()
	require.NoError(t, ValidateCodexFingerprintEpochPolicy(valid))

	tests := []struct {
		name   string
		mutate func(*CodexFingerprintEpochPolicy)
	}{
		{"最短寿命下界", func(p *CodexFingerprintEpochPolicy) { p.MinSessionAgeHours = 0 }},
		{"最长寿命不得小于最短寿命", func(p *CodexFingerprintEpochPolicy) { p.MaxSessionAgeHours = p.MinSessionAgeHours - 1 }},
		{"抖动上界", func(p *CodexFingerprintEpochPolicy) {
			p.RotationJitterHours = config.CodexFingerprintRotationJitterMax + 1
		}},
		{"空闲门槛上界", func(p *CodexFingerprintEpochPolicy) {
			p.IdleGateMinutes = config.CodexFingerprintIdleGateMinutesMax + 1
		}},
		{"旧epoch宽限下界", func(p *CodexFingerprintEpochPolicy) { p.OldEpochGraceHours = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := valid
			test.mutate(&policy)
			require.Error(t, ValidateCodexFingerprintEpochPolicy(policy))
		})
	}
}

func TestBuildSystemSettingsUpdatesPersistsCodexFingerprintEpochPolicy(t *testing.T) {
	svc := NewSettingService(&codexFingerprintEpochSettingRepo{}, testCodexFingerprintEpochConfig())
	settings := &SystemSettings{
		CodexFingerprintMinSessionAgeHours:  100,
		CodexFingerprintMaxSessionAgeHours:  200,
		CodexFingerprintRotationJitterHours: 20,
		CodexFingerprintIdleGateMinutes:     60,
		CodexFingerprintOldEpochGraceHours:  72,
		SecurityDepositPenaltyMode:          SecurityDepositPenaltyModeOff,
	}

	updates, err := svc.buildSystemSettingsUpdates(context.Background(), settings)
	require.NoError(t, err)
	require.Equal(t, "100", updates[SettingKeyCodexFingerprintMinSessionAgeHours])
	require.Equal(t, "200", updates[SettingKeyCodexFingerprintMaxSessionAgeHours])
	require.Equal(t, "20", updates[SettingKeyCodexFingerprintRotationJitterHours])
	require.Equal(t, "60", updates[SettingKeyCodexFingerprintIdleGateMinutes])
	require.Equal(t, "72", updates[SettingKeyCodexFingerprintOldEpochGraceHours])
}
