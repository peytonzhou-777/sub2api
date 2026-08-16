//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestSecurityDepositSettings_RoundTripStoredValues(t *testing.T) {
	repo := newMockSettingRepo()
	repo.data[SettingKeySecurityDepositEnforcementEnabled] = "true"
	repo.data[SettingKeySecurityDepositSelfRefundEnabled] = "true"
	repo.data[SettingKeySecurityDepositPenaltyMode] = SecurityDepositPenaltyModeShadow
	repo.data[SettingKeySecurityDepositFreezeHours] = "48"
	repo.data[SettingKeySecurityDepositMaxRiskMultiplier] = "12"
	repo.data[SettingKeySecurityDepositPolicyVersion] = "policy-v2"
	repo.data[SettingKeySecurityDepositAgreementContentZH] = "中文约定"
	repo.data[SettingKeySecurityDepositAgreementContentEN] = "English terms"
	svc := NewSettingService(repo, &config.Config{})

	settings, err := svc.GetAllSettings(context.Background())
	require.NoError(t, err)
	require.True(t, settings.SecurityDepositEnforcementEnabled)
	require.True(t, settings.SecurityDepositSelfRefundEnabled)
	require.Equal(t, SecurityDepositPenaltyModeShadow, settings.SecurityDepositPenaltyMode)
	require.Equal(t, 48, settings.SecurityDepositFreezeHours)
	require.EqualValues(t, 12, settings.SecurityDepositMaxRiskMultiplier)
	require.Equal(t, "policy-v2", settings.SecurityDepositPolicyVersion)
	require.Equal(t, "中文约定", settings.SecurityDepositAgreementContentZH)
	require.Equal(t, "English terms", settings.SecurityDepositAgreementContentEN)
}

func TestSecurityDepositSettings_UpdatePersistsAllFields(t *testing.T) {
	repo := &settingUpdateRepoStub{}
	svc := NewSettingService(repo, &config.Config{})

	err := svc.UpdateSettings(context.Background(), &SystemSettings{
		SecurityDepositEnforcementEnabled: true,
		SecurityDepositSelfRefundEnabled:  true,
		SecurityDepositPenaltyMode:        SecurityDepositPenaltyModeEnforce,
		SecurityDepositFreezeHours:        72,
		SecurityDepositMaxRiskMultiplier:  9,
		SecurityDepositPolicyVersion:      " policy-v3 ",
		SecurityDepositAgreementContentZH: " 中文约定 v3 ",
		SecurityDepositAgreementContentEN: " English terms v3 ",
	})
	require.NoError(t, err)
	require.Equal(t, "true", repo.updates[SettingKeySecurityDepositEnforcementEnabled])
	require.Equal(t, "true", repo.updates[SettingKeySecurityDepositSelfRefundEnabled])
	require.Equal(t, SecurityDepositPenaltyModeEnforce, repo.updates[SettingKeySecurityDepositPenaltyMode])
	require.Equal(t, "72", repo.updates[SettingKeySecurityDepositFreezeHours])
	require.Equal(t, "9", repo.updates[SettingKeySecurityDepositMaxRiskMultiplier])
	require.Equal(t, "policy-v3", repo.updates[SettingKeySecurityDepositPolicyVersion])
	require.Equal(t, "中文约定 v3", repo.updates[SettingKeySecurityDepositAgreementContentZH])
	require.Equal(t, "English terms v3", repo.updates[SettingKeySecurityDepositAgreementContentEN])
}

func TestSecurityDepositSettings_DefaultAgreementStatesPenaltyAndRefundRules(t *testing.T) {
	policy := buildSecurityDepositPolicyConfig(nil)

	require.Contains(t, policy.ContentZH, "首次触发按 1 倍门槛")
	require.Contains(t, policy.ContentZH, "第二次按 2 倍")
	require.Contains(t, policy.ContentZH, "第三次按 3 倍")
	require.Contains(t, policy.ContentZH, "管理员配置的倍率上限")
	require.Contains(t, policy.ContentZH, "平台保证金自助退款")
	require.Contains(t, policy.ContentZH, "对应支付实例")
	require.Contains(t, policy.ContentZH, "管理员发放保证金永久冻结且不可退款")

	require.Contains(t, policy.ContentEN, "first violation uses 1x")
	require.Contains(t, policy.ContentEN, "second uses 2x")
	require.Contains(t, policy.ContentEN, "third uses 3x")
	require.Contains(t, policy.ContentEN, "administrator-configured multiplier cap")
	require.Contains(t, policy.ContentEN, "platform self-refund")
	require.Contains(t, policy.ContentEN, "payment-provider instance")
	require.Contains(t, policy.ContentEN, "permanently frozen and non-refundable")
}

func TestSecurityDepositSettings_RejectsInvalidBounds(t *testing.T) {
	tests := []struct {
		name       string
		settings   *SystemSettings
		wantReason string
	}{
		{
			name:       "冻结时长小于零",
			settings:   &SystemSettings{SecurityDepositFreezeHours: -1},
			wantReason: "INVALID_SECURITY_DEPOSIT_FREEZE_HOURS",
		},
		{
			name:       "冻结时长超过上限",
			settings:   &SystemSettings{SecurityDepositFreezeHours: maxSecurityDepositFreezeHours + 1},
			wantReason: "INVALID_SECURITY_DEPOSIT_FREEZE_HOURS",
		},
		{
			name:       "倍率小于下限",
			settings:   &SystemSettings{SecurityDepositMaxRiskMultiplier: -1},
			wantReason: "INVALID_SECURITY_DEPOSIT_MAX_RISK_MULTIPLIER",
		},
		{
			name:       "倍率超过上限",
			settings:   &SystemSettings{SecurityDepositMaxRiskMultiplier: 101},
			wantReason: "INVALID_SECURITY_DEPOSIT_MAX_RISK_MULTIPLIER",
		},
		{
			name:       "处罚模式非法",
			settings:   &SystemSettings{SecurityDepositPenaltyMode: "invalid"},
			wantReason: "INVALID_SECURITY_DEPOSIT_PENALTY_MODE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &settingUpdateRepoStub{}
			svc := NewSettingService(repo, &config.Config{})

			err := svc.UpdateSettings(context.Background(), tt.settings)

			require.Error(t, err)
			require.Equal(t, tt.wantReason, infraerrors.Reason(err))
			require.Nil(t, repo.updates)
		})
	}
}
