//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type codexRouteDetectionTestConn struct {
	messages [][]byte
}

func (c *codexRouteDetectionTestConn) WriteJSON(context.Context, any) error { return nil }

func (c *codexRouteDetectionTestConn) ReadMessage(context.Context) ([]byte, error) {
	if len(c.messages) == 0 {
		return nil, io.EOF
	}
	payload := c.messages[0]
	c.messages = c.messages[1:]
	return payload, nil
}

func (c *codexRouteDetectionTestConn) Ping(context.Context) error { return nil }
func (c *codexRouteDetectionTestConn) Close() error               { return nil }

func TestCodexTimingEngineIDs(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"gpt56sol-codex-main"}, codexTimingEngineIDs([]byte(
		`{"timing_metrics":{"engine_ids":["gpt56sol-codex-main"]}}`,
	)))
	require.Equal(t, []string{"gpt56lun-codex-main"}, codexTimingEngineIDs([]byte(
		`{"data":{"timing_metrics":{"engine_ids":["gpt56lun-codex-main"]}}}`,
	)))
}

func TestClassifyCodexRouteEngineIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		timingSeen bool
		engineIDs  []string
		status     string
		reason     string
	}{
		{name: "sol", timingSeen: true, engineIDs: []string{"gpt56sol-codex-main", "gpt56sol-codex-fast"}, status: "sol", reason: "sol_engine"},
		{name: "luna", timingSeen: true, engineIDs: []string{"gpt56lun-codex-main"}, status: "luna", reason: "luna_engine"},
		{name: "event missing", engineIDs: []string{"gpt56sol-codex-main"}, status: "inconclusive", reason: "timing_missing"},
		{name: "ids missing", timingSeen: true, status: "inconclusive", reason: "timing_missing"},
		{name: "unknown", timingSeen: true, engineIDs: []string{"unknown-engine"}, status: "inconclusive", reason: "unknown_engine"},
		{name: "mixed", timingSeen: true, engineIDs: []string{"gpt56sol-codex-main", "gpt56lun-codex-main"}, status: "inconclusive", reason: "mixed_engines"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason := classifyCodexRouteEngineIDs(tt.timingSeen, tt.engineIDs)
			require.Equal(t, tt.status, status)
			require.Equal(t, tt.reason, reason)
		})
	}
}

func TestReadCodexRouteDetectionEventsSupportsTerminalTimingOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		messages []string
		status   string
		reason   string
		model    string
	}{
		{
			name: "timing before completed",
			messages: []string{
				`{"type":"responsesapi.websocket_timing","timing_metrics":{"engine_ids":["gpt56sol-codex-main"]}}`,
				`{"type":"response.completed","response":{"model":"gpt-5.6-sol"}}`,
			},
			status: "sol", reason: "sol_engine", model: "gpt-5.6-sol",
		},
		{
			name: "timing after completed",
			messages: []string{
				`{"type":"response.completed","response":{"model":"gpt-5.6-sol"}}`,
				`{"type":"responsesapi.websocket_timing","data":{"timing_metrics":{"engine_ids":["gpt56lun-codex-main"]}}}`,
			},
			status: "luna", reason: "luna_engine", model: "gpt-5.6-sol",
		},
		{
			name:     "completed without timing",
			messages: []string{`{"type":"response.completed","response":{"model":"gpt-5.6-sol"}}`},
			status:   "inconclusive", reason: "timing_missing", model: "gpt-5.6-sol",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := make([][]byte, 0, len(tt.messages))
			for _, message := range tt.messages {
				messages = append(messages, []byte(message))
			}
			result := &CodexRouteDetectionResult{}
			service := &AccountTestService{}
			service.readCodexRouteDetectionEvents(context.Background(), &codexRouteDetectionTestConn{messages: messages}, result)

			require.Equal(t, tt.status, result.Status)
			require.Equal(t, tt.reason, result.ReasonCode)
			require.Equal(t, tt.model, result.ReportedModel)
		})
	}
}

func TestSelectCodexRouteDetectionHeadersUsesAllowlist(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Add("X-Codex-Primary-Used-Percent", "42")
	headers.Add("X-Codex-Primary-Used-Percent", "43")
	headers.Set("X-Codex-Active-Limit", "primary")
	headers.Set("Authorization", "secret")

	selected := selectCodexRouteDetectionHeaders(headers)
	require.Equal(t, map[string]string{
		"x-codex-primary-used-percent":          "42, 43",
		"x-codex-primary-window-minutes":        "",
		"x-codex-active-limit":                  "primary",
		"x-codex-safety-buffering-faster-model": "",
	}, selected)
	require.NotContains(t, selected, "Authorization")
}

func TestAcquireCodexRouteDetectionLimitsConcurrencyAndDeduplicatesCredentials(t *testing.T) {
	t.Parallel()

	service := &AccountTestService{}
	releaseFirst, err := service.acquireCodexRouteDetection(context.Background(), 101)
	require.NoError(t, err)
	defer releaseFirst()

	_, err = service.acquireCodexRouteDetection(context.Background(), 101)
	require.ErrorIs(t, err, ErrCodexRouteDetectionBusy)

	releaseSecond, err := service.acquireCodexRouteDetection(context.Background(), 102)
	require.NoError(t, err)
	defer releaseSecond()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.acquireCodexRouteDetection(canceled, 103)
	require.ErrorIs(t, err, context.Canceled)
	_, active := service.codexRouteDetectionActive.Load(int64(103))
	require.False(t, active)
}

func TestAcquireCodexRouteDetectionLeaseDeduplicatesAcrossInstances(t *testing.T) {
	t.Parallel()

	cache := &fakeLeaderLockCache{}
	first := &AccountTestService{codexRouteDetectionLock: cache}
	second := &AccountTestService{codexRouteDetectionLock: cache}

	releaseFirst, err := first.acquireCodexRouteDetectionLease(context.Background(), 201)
	require.NoError(t, err)
	_, err = second.acquireCodexRouteDetectionLease(context.Background(), 201)
	require.ErrorIs(t, err, ErrCodexRouteDetectionBusy)

	releaseFirst()
	releaseSecond, err := second.acquireCodexRouteDetectionLease(context.Background(), 201)
	require.NoError(t, err)
	releaseSecond()
}

func TestAcquireCodexRouteDetectionLeaseFailsClosedOnCacheError(t *testing.T) {
	t.Parallel()

	service := &AccountTestService{
		codexRouteDetectionLock: &fakeLeaderLockCache{acquireErr: context.DeadlineExceeded},
	}
	_, err := service.acquireCodexRouteDetectionLease(context.Background(), 301)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrCodexRouteDetectionBusy)
}
