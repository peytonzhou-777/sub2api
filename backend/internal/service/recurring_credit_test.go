package service

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func intPointer(value int) *int { return &value }

func TestNextRecurringOccurrenceMonthlyIsStrictlyFuture(t *testing.T) {
	input := RecurringCreditTaskInput{ScheduleType: RecurringCreditMonthly, DayOfMonth: intPointer(5), LocalTime: "08:00", Timezone: "Asia/Shanghai"}
	after := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	next, err := nextRecurringOccurrence(input, after)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), next)
}

func TestNextRecurringOccurrenceWeeklyUsesISOWeekday(t *testing.T) {
	input := RecurringCreditTaskInput{ScheduleType: RecurringCreditWeekly, DayOfWeek: intPointer(1), LocalTime: "08:00", Timezone: "Asia/Shanghai"}
	after := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC) // 周日 18:00。
	next, err := nextRecurringOccurrence(input, after)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC), next)
}

func TestRecurringOccurrenceResolvesDSTGapAndOverlap(t *testing.T) {
	gap := RecurringCreditTaskInput{ScheduleType: RecurringCreditWeekly, DayOfWeek: intPointer(7), LocalTime: "02:30", Timezone: "America/New_York"}
	next, err := nextRecurringOccurrence(gap, time.Date(2026, 3, 7, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC), next, "不存在的 02:30 应解析为跳时后的 03:00")

	overlap := RecurringCreditTaskInput{ScheduleType: RecurringCreditWeekly, DayOfWeek: intPointer(7), LocalTime: "01:30", Timezone: "America/New_York"}
	next, err = nextRecurringOccurrence(overlap, time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC), next, "重复的 01:30 应选择第一次出现")
}

func TestQualificationWindowUsesCompleteCalendarPeriod(t *testing.T) {
	start, end, err := qualificationWindow(RecurringCreditMonthly, "Asia/Shanghai", time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 6, 30, 16, 0, 0, 0, time.UTC), start)
	require.Equal(t, time.Date(2026, 7, 31, 16, 0, 0, 0, time.UTC), end)

	start, end, err = qualificationWindow(RecurringCreditWeekly, "Asia/Shanghai", time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 5, 16, 0, 0, 0, time.UTC), start)
	require.Equal(t, time.Date(2026, 7, 12, 16, 0, 0, 0, time.UTC), end)
}

func TestValidateImmediateCreditNormalizesOneTimeExecution(t *testing.T) {
	service := &RecurringCreditService{defaultTimezone: "Asia/Shanghai"}
	validityDays := 7
	input := RecurringCreditTaskInput{
		Name:            "全站即时赠额",
		ScheduleType:    RecurringCreditImmediate,
		DayOfMonth:      intPointer(5),
		DayOfWeek:       intPointer(1),
		LocalTime:       "08:00",
		Amount:          10,
		ExecutionMode:   RecurringCreditModePermanent,
		RemainingRuns:   intPointer(9),
		ValidityDays:    &validityDays,
		InitiallyActive: false,
	}

	require.NoError(t, service.validateInput(&input))
	require.Nil(t, input.DayOfMonth)
	require.Nil(t, input.DayOfWeek)
	require.Empty(t, input.LocalTime)
	require.Equal(t, RecurringCreditModeFinite, input.ExecutionMode)
	require.Equal(t, 1, *input.RemainingRuns)
	require.True(t, input.InitiallyActive)
	require.Equal(t, "Asia/Shanghai", input.Timezone)
}

func TestImmediateCreditValidityDaysMustBeInRange(t *testing.T) {
	service := &RecurringCreditService{defaultTimezone: "Asia/Shanghai"}
	for _, days := range []int{0, 36501} {
		input := RecurringCreditTaskInput{Name: "即时赠额", ScheduleType: RecurringCreditImmediate, Amount: 1, ValidityDays: &days}
		require.Error(t, service.validateInput(&input))
	}
}

func TestImmediateCreditScheduleDescriptionShowsValidity(t *testing.T) {
	validityDays := 7
	description := scheduleDescription(&RecurringCreditTaskView{ScheduleType: RecurringCreditImmediate, ValidityDays: &validityDays})
	require.Equal(t, "立即执行（有效期7天）", description)
}

func TestRecurringCreditActivityWindowUsesRollingThirtyDays(t *testing.T) {
	cutoff := time.Date(2026, 7, 17, 12, 30, 0, 0, time.UTC)
	start, end := recurringCreditActivityWindow(cutoff)
	require.Equal(t, cutoff.Add(-30*24*time.Hour), start)
	require.Equal(t, cutoff, end)
}

func TestRecurringCreditActivityReasonCombinesSources(t *testing.T) {
	require.Equal(t, "api_activity", recurringCreditActivityReason(true, false))
	require.Equal(t, "site_activity", recurringCreditActivityReason(false, true))
	require.Equal(t, "api_and_site_activity", recurringCreditActivityReason(true, true))
	require.Empty(t, recurringCreditActivityReason(false, false))
}

func TestRecurringCreditActivityQueriesDoNotScanUsageOrRecharge(t *testing.T) {
	queries := strings.ToLower(rollingActivityStatsSQL + rollingActivitySnapshotSQL)
	require.Contains(t, queries, "api_keys")
	require.Contains(t, queries, "last_active_at")
	require.NotContains(t, queries, "usage_logs")
	require.NotContains(t, queries, "payment_orders")
	require.Contains(t, queries, "30 days 1 minute")
}

func TestRecurringCreditActivitySnapshotFreezesPendingUsers(t *testing.T) {
	query := strings.ToLower(rollingActivitySnapshotSQL)
	require.Contains(t, query, "'pending'")
	require.Contains(t, query, "u.status='active'")
	require.Contains(t, query, "u.deleted_at is null")
	require.Contains(t, query, "on conflict(batch_id,user_id) do nothing")
	require.Contains(t, query, "api_last_used_at")
	require.Contains(t, query, "site_last_active_at")
}

func TestRecurringCreditActivityCandidateQueryKeepsDeletedAPIKeyHistory(t *testing.T) {
	apiSection := strings.Split(strings.ToLower(rollingActivityCandidatesSQL), "), site_activity")[0]
	require.Contains(t, apiSection, "from api_keys")
	require.NotContains(t, apiSection, "deleted_at")
	require.NotContains(t, apiSection, "status=")
	require.True(t, strings.HasPrefix(rollingActivityStatsSQL, rollingActivityCandidatesSQL))
	require.True(t, strings.HasPrefix(rollingActivitySnapshotSQL, rollingActivityCandidatesSQL))
}
