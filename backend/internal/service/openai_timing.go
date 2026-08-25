package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// OpenAI 请求阶段观测只保存在 Gin 请求上下文，不写入业务表，避免改变
// 调度、指纹、计费和前端 first_token_ms 的既有语义。
const openAITimingContextKey = "openai_timing_trace"

type openAITimingTrace struct {
	forwardStartedAt       time.Time
	upstreamAttemptStarted time.Time
	upstreamHeadersAt      time.Time
	upstreamFirstEventAt   time.Time
	firstClientOutputAt    time.Time
	firstSemanticOutputAt  time.Time
	firstClientWriteAt     time.Time
	upstreamBodyDoneAt     time.Time
	upstreamAttempts       int
	upstreamStatusCode     int
	wsQueueWaitMs          int64
	wsConnPickMs           int64
	wsConnReused           bool
	wsConnID               string
	wsLeaseObserved        bool
}

func beginOpenAITiming(c *gin.Context, startedAt time.Time) {
	if c == nil || startedAt.IsZero() {
		return
	}
	c.Set(openAITimingContextKey, &openAITimingTrace{forwardStartedAt: startedAt})
}

func openAITiming(c *gin.Context) *openAITimingTrace {
	if c == nil {
		return nil
	}
	value, ok := c.Get(openAITimingContextKey)
	if !ok {
		return nil
	}
	trace, ok := value.(*openAITimingTrace)
	if !ok {
		return nil
	}
	return trace
}

func markOpenAITimingUpstreamAttempt(c *gin.Context, startedAt time.Time) {
	trace := openAITiming(c)
	if trace == nil || startedAt.IsZero() {
		return
	}
	trace.upstreamAttempts++
	trace.upstreamAttemptStarted = startedAt
}

func markOpenAITimingUpstreamResponse(c *gin.Context, responseAt time.Time, statusCode int) {
	trace := openAITiming(c)
	if trace == nil || responseAt.IsZero() {
		return
	}
	trace.upstreamHeadersAt = responseAt
	trace.upstreamStatusCode = statusCode
}

func markOpenAITimingUpstreamFirstEvent(c *gin.Context, eventAt time.Time) {
	trace := openAITiming(c)
	if trace == nil || !trace.upstreamFirstEventAt.IsZero() || eventAt.IsZero() {
		return
	}
	trace.upstreamFirstEventAt = eventAt
}

func markOpenAITimingFirstSemanticOutput(c *gin.Context, outputAt time.Time) {
	trace := openAITiming(c)
	if trace == nil || !trace.firstSemanticOutputAt.IsZero() || outputAt.IsZero() {
		return
	}
	trace.firstSemanticOutputAt = outputAt
}

// markOpenAITimingFirstClientOutput 记录首次满足 startsClientOutput 的结构性输出事件。
func markOpenAITimingFirstClientOutput(c *gin.Context, outputAt time.Time) {
	trace := openAITiming(c)
	if trace == nil || !trace.firstClientOutputAt.IsZero() || outputAt.IsZero() {
		return
	}
	trace.firstClientOutputAt = outputAt
}

func markOpenAITimingFirstClientWrite(c *gin.Context, writeAt time.Time) {
	trace := openAITiming(c)
	if trace == nil || !trace.firstClientWriteAt.IsZero() || writeAt.IsZero() {
		return
	}
	trace.firstClientWriteAt = writeAt
}

func markOpenAITimingUpstreamBodyDone(c *gin.Context, doneAt time.Time) {
	trace := openAITiming(c)
	if trace == nil || doneAt.IsZero() {
		return
	}
	trace.upstreamBodyDoneAt = doneAt
}

// markOpenAITimingWSLease 保存 WS 连接池租约信息，便于与 SSE 的上游阶段耗时对照。
func markOpenAITimingWSLease(c *gin.Context, queueWaitMs, connPickMs int64, reused bool, connID string) {
	trace := openAITiming(c)
	if trace == nil {
		return
	}
	trace.wsQueueWaitMs = queueWaitMs
	trace.wsConnPickMs = connPickMs
	trace.wsConnReused = reused
	trace.wsConnID = strings.TrimSpace(connID)
	trace.wsLeaseObserved = true
}

func openAITimingDurationMS(startAt, endAt time.Time) (int64, bool) {
	if startAt.IsZero() || endAt.IsZero() || endAt.Before(startAt) {
		return 0, false
	}
	return endAt.Sub(startAt).Milliseconds(), true
}

// logOpenAITiming 输出单条请求级阶段日志，不记录请求体、Prompt、Token 或凭据。
// 阶段均以最终上游 attempt 为基准，便于直接定位 HTTP/SSE 首字长尾。
func logOpenAITiming(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	model string,
	stream bool,
	result *OpenAIForwardResult,
	finishedAt time.Time,
) {
	trace := openAITiming(c)
	if trace == nil || trace.forwardStartedAt.IsZero() {
		return
	}
	if finishedAt.IsZero() {
		finishedAt = time.Now()
	}

	transport := "http_json"
	if stream {
		transport = "http_sse"
	}
	if result != nil && result.OpenAIWSMode {
		transport = "ws"
	}
	fields := []zap.Field{
		zap.String("transport", transport),
		zap.Bool("stream", stream),
		zap.String("model", strings.TrimSpace(model)),
		zap.Int("upstream_attempts", trace.upstreamAttempts),
		zap.Int("upstream_status_code", trace.upstreamStatusCode),
	}
	if account != nil {
		fields = append(fields,
			zap.Int64("account_id", account.ID),
			zap.String("account_type", string(account.Type)),
			zap.String("account_platform", account.Platform),
		)
	}
	if result != nil {
		fields = append(fields,
			zap.Bool("openai_ws_mode", result.OpenAIWSMode),
			zap.Int64("request_duration_ms", result.Duration.Milliseconds()),
		)
		if result.FirstTokenMs != nil {
			fields = append(fields, zap.Int("first_token_ms", *result.FirstTokenMs))
		}
	}
	appendDuration := func(name string, startAt, endAt time.Time) {
		if value, ok := openAITimingDurationMS(startAt, endAt); ok {
			fields = append(fields, zap.Int64(name, value))
		}
	}
	appendDuration("forward_pre_upstream_ms", trace.forwardStartedAt, trace.upstreamAttemptStarted)
	appendDuration("upstream_response_headers_ms", trace.upstreamAttemptStarted, trace.upstreamHeadersAt)
	appendDuration("upstream_first_event_ms", trace.upstreamAttemptStarted, trace.upstreamFirstEventAt)
	appendDuration("first_client_output_ms", trace.upstreamAttemptStarted, trace.firstClientOutputAt)
	appendDuration("first_semantic_output_ms", trace.upstreamAttemptStarted, trace.firstSemanticOutputAt)
	appendDuration("first_client_write_ms", trace.upstreamAttemptStarted, trace.firstClientWriteAt)
	appendDuration("upstream_body_ms", trace.upstreamAttemptStarted, trace.upstreamBodyDoneAt)
	appendDuration("forward_total_ms", trace.forwardStartedAt, finishedAt)
	if trace.wsLeaseObserved {
		fields = append(fields,
			zap.Int64("ws_queue_wait_ms", trace.wsQueueWaitMs),
			zap.Int64("ws_conn_pick_ms", trace.wsConnPickMs),
			zap.Bool("ws_conn_reused", trace.wsConnReused),
		)
		if trace.wsConnID != "" {
			fields = append(fields, zap.String("ws_conn_id", trace.wsConnID))
		}
	}

	logger.FromContext(ctx).With(fields...).Info("openai.ttft_stage_timing")
}
