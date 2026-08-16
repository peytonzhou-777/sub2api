package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestCalculateResetRebateAutoRatio(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{name: "四天", duration: 4 * 24 * time.Hour, want: "42.85714285"},
		{name: "两天", duration: 2 * 24 * time.Hour, want: "71.42857142"},
		{name: "七天", duration: 7 * 24 * time.Hour, want: "0.00000000"},
		{name: "超过七天", duration: 9 * 24 * time.Hour, want: "0.00000000"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ratio, err := CalculateResetRebateAutoRatio(start, start.Add(test.duration))
			require.NoError(t, err)
			require.Equal(t, test.want, decimalString(ratio, 8))
		})
	}
}

func TestResetRebateCalculationAggregatesBeforeFinalTruncation(t *testing.T) {
	accountA := decimal.RequireFromString("100").Mul(decimal.RequireFromString("80")).Div(resetRebateHundred)
	accountB := decimal.RequireFromString("50").Mul(decimal.RequireFromString("60")).Div(resetRebateHundred)
	require.Equal(t, "99.00000000", decimalString(truncateResetRebateAmount(accountA.Add(accountB), 90), 8))

	// 两个账号贡献分别不足最小金额，但跨账号求和后应产生 0.00000001。
	tiny := decimal.RequireFromString("0.000000006")
	require.Equal(t, "0.00000001", decimalString(truncateResetRebateAmount(tiny.Add(tiny), 100), 8))
	require.True(t, tiny.Truncate(8).IsZero())
}

func TestResetRebateRatioValidation(t *testing.T) {
	for _, value := range []string{"0", "80", "100.00000000"} {
		_, err := parseResetRebateRatio(value, "ratio")
		require.NoError(t, err)
	}
	for _, value := range []string{"-1", "100.00000001", "80.123456789", "not-number"} {
		_, err := parseResetRebateRatio(value, "ratio")
		require.Error(t, err)
	}
}

func TestNormalizeResetRebateReasonUsesConfirmedDefault(t *testing.T) {
	reason, err := normalizeResetRebateReason("  ")
	require.NoError(t, err)
	require.Equal(t, ResetRebateDefaultReason, reason)
}

func TestResetRebateSkipCountExclusionSurvivesPreview(t *testing.T) {
	statsUser := &resetRebateStatsUser{SkipCount: 2, Weighted: decimal.NewFromInt(10)}
	result, reason := classifyResetRebateStatsUser(statsUser)
	require.Equal(t, "excluded", result)
	require.Equal(t, ResetRebateExclusionSkipCount, reason)

	result, reason = classifyResetRebatePreviewUser(false, result, reason, decimal.NewFromInt(9))
	require.Equal(t, "excluded", result)
	require.Equal(t, ResetRebateExclusionSkipCount, reason)
}

func TestResetRebateUserFailureDoesNotStopRemainingUsers(t *testing.T) {
	svc := &ResetRebateService{}
	issued := make([]int64, 0, 3)
	failed := make([]int64, 0, 1)
	wantCause := errors.New("grant insert failed")
	err := svc.processResetRebateUsers(context.Background(), 9, []int64{1, 2, 3}, ResetRebateActor{AdminID: 7}, "initial", time.Time{},
		func(_ context.Context, _ int64, userID int64, _ ResetRebateActor, _ string, _ time.Time) error {
			issued = append(issued, userID)
			if userID == 2 {
				return wantCause
			}
			return nil
		},
		func(_ context.Context, _ int64, userID int64, _ ResetRebateActor, _ string, cause error) error {
			failed = append(failed, userID)
			require.ErrorIs(t, cause, wantCause)
			return nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3}, issued)
	require.Equal(t, []int64{2}, failed)
}

func TestResetRebateServiceHasNoUpstreamQuotaDependency(t *testing.T) {
	constructor := reflect.TypeOf(NewResetRebateService)
	require.Equal(t, 3, constructor.NumIn())
	require.Equal(t, "*sql.DB", constructor.In(0).String())
	for index := 0; index < constructor.NumIn(); index++ {
		require.NotContains(t, constructor.In(index).String(), "Quota")
	}
}
