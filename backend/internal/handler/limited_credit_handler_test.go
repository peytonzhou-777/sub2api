//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParseLimitedCreditHistoryRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	startTime := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	endTime := time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/?start_time="+startTime.Format(time.RFC3339)+"&end_time="+endTime.Format(time.RFC3339), nil)

	start, end, err := parseLimitedCreditHistoryRange(c)

	require.NoError(t, err)
	require.Equal(t, startTime, start)
	require.Equal(t, endTime, end)
}

func TestParseLimitedCreditHistoryRangeRejectsInvalidWindows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, rawQuery := range []string{
		"",
		"?start_time=bad&end_time=2026-08-31T00:00:00Z",
		"?start_time=2026-08-31T00:00:00Z&end_time=2026-08-01T00:00:00Z",
		"?start_time=2025-01-01T00:00:00Z&end_time=2026-08-31T00:00:00Z",
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/"+rawQuery, nil)
		_, _, err := parseLimitedCreditHistoryRange(c)
		require.Error(t, err, rawQuery)
	}
}

func TestLimitedCreditGrantResponsesIncludesExhaustedAtTimestamp(t *testing.T) {
	updatedAt := time.Date(2026, time.August, 25, 8, 30, 0, 0, time.UTC)
	responses := limitedCreditGrantResponses([]service.LimitedCreditGrant{{
		ID: 9, InitialAmount: 5, UsedAmount: 5, Status: service.LimitedCreditStatusDepleted, UpdatedAt: updatedAt,
	}})

	require.Len(t, responses, 1)
	require.Equal(t, updatedAt, responses[0].UpdatedAt)
	require.Zero(t, responses[0].RemainingAmount)
}
