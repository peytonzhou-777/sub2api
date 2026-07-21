package service

import (
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestTruncateResetRebate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		actual float64
		ratio  int
		want   string
	}{
		{name: "普通金额", actual: 12.34567891, ratio: 33, want: "4.07407404"},
		{name: "向下截断八位", actual: 1, ratio: 1, want: "0.01"},
		{name: "小于最小精度", actual: 0.00000001, ratio: 1, want: "0"},
		{name: "零金额", actual: 0, ratio: 80, want: "0"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, truncateResetRebate(tt.actual, tt.ratio).String())
		})
	}
}

func TestOrdinarySevenDayWindow(t *testing.T) {
	t.Parallel()
	fiveHours := &OpenAIRateLimitWindow{UsedPercent: 10, LimitWindowSeconds: 18000}
	sevenDays := &OpenAIRateLimitWindow{UsedPercent: 42.25, LimitWindowSeconds: 604800}
	require.Same(t, sevenDays, ordinarySevenDayWindow(&OpenAIRateLimit{PrimaryWindow: fiveHours, SecondaryWindow: sevenDays}))
	require.Nil(t, ordinarySevenDayWindow(&OpenAIRateLimit{PrimaryWindow: fiveHours}))
	require.Nil(t, ordinarySevenDayWindow(nil))
}

func TestCalculateResetRebateSuggestedRatio(t *testing.T) {
	t.Parallel()
	require.Equal(t, 25, calculateResetRebateSuggestedRatio(50.99, 50.99))
	require.Equal(t, 0, calculateResetRebateSuggestedRatio(0, 80))
	require.Equal(t, 96, calculateResetRebateSuggestedRatio(120, 80))
}

func TestResetRebateFinalStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		actual       float64
		failed       int
		participants int
		wantStatus   string
		wantCode     string
	}{
		{name: "零消费始终不可执行", actual: 0, failed: 2, participants: 0, wantStatus: ResetRebateStatusNotEligible, wantCode: "NO_ACTUAL_CONSUMPTION"},
		{name: "统计不完整但有消费可执行", actual: 10, failed: 2, participants: 0, wantStatus: ResetRebateStatusIncomplete, wantCode: "UPSTREAM_STATS_INCOMPLETE"},
		{name: "完整但无参与账号可强制执行", actual: 10, failed: 0, participants: 0, wantStatus: ResetRebateStatusReady, wantCode: "NO_PARTICIPATING_ACCOUNTS"},
		{name: "完整且有参与账号正常执行", actual: 10, failed: 0, participants: 1, wantStatus: ResetRebateStatusReady, wantCode: ""},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			status, code, _ := resetRebateFinalStatus(tt.actual, tt.failed, tt.participants)
			require.Equal(t, tt.wantStatus, status)
			require.Equal(t, tt.wantCode, code)
		})
	}
}

func TestResetRebateExecutableStatus(t *testing.T) {
	t.Parallel()
	require.True(t, resetRebateExecutableStatus(ResetRebateStatusReady))
	require.True(t, resetRebateExecutableStatus(ResetRebateStatusIncomplete))
	require.False(t, resetRebateExecutableStatus(ResetRebateStatusNotEligible))
	require.False(t, resetRebateExecutableStatus(ResetRebateStatusExpired))
}

func TestNormalizeResetRebateReason(t *testing.T) {
	t.Parallel()
	emptyReason, err := normalizeResetRebateReason("  ")
	require.NoError(t, err)
	require.Empty(t, emptyReason)

	reason, err := normalizeResetRebateReason("  本周活动返利  ")
	require.NoError(t, err)
	require.Equal(t, "本周活动返利", reason)

	_, err = normalizeResetRebateReason(strings.Repeat("字", 101))
	require.Error(t, err)
}

func TestResetRebateInitialExclusion(t *testing.T) {
	t.Parallel()
	base := resetRebateAccountSnapshot{platform: PlatformOpenAI, accountType: AccountTypeOAuth, inGroup: true, schedulable: true}
	require.Equal(t, "等待上游统计", resetRebateInitialExclusion(base))
	shadow := base
	shadow.isShadow = true
	require.Equal(t, "影子账号已排除", resetRebateInitialExclusion(shadow))
	moved := base
	moved.inGroup = false
	require.Equal(t, "当前不在该分组", resetRebateInitialExclusion(moved))
	paused := base
	paused.schedulable = false
	require.Equal(t, "当前不可调度", resetRebateInitialExclusion(paused))
}

func TestResetRebateUserViewExclusions(t *testing.T) {
	t.Parallel()
	expiry := time.Now().Add(7 * 24 * time.Hour)
	deleted := resetRebateUserView(&dbent.ResetRebateUserItem{ID: 1, UserID: 7, ActualAmount: 10, UserDeleted: true}, 20, expiry)
	require.Equal(t, 2.0, deleted.TheoreticalAmount)
	require.Zero(t, deleted.RebateAmount)
	require.Equal(t, "用户已删除，未发放", deleted.ExclusionReason)
	require.Nil(t, deleted.ExpiresAt)

	tiny := resetRebateUserView(&dbent.ResetRebateUserItem{ID: 2, UserID: 8, ActualAmount: 0.00000001}, 1, expiry)
	require.Zero(t, tiny.RebateAmount)
	require.Equal(t, "金额过小，未发放", tiny.ExclusionReason)
}
