package service

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// 阶段时间只保留首次发生的事件，避免后续 SSE 事件覆盖真正的首字时间点。
func TestOpenAITimingStagesAreFirstWins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	base := time.Unix(100, 0)
	beginOpenAITiming(c, base)
	markOpenAITimingUpstreamAttempt(c, base.Add(10*time.Millisecond))
	markOpenAITimingUpstreamAttempt(c, base.Add(20*time.Millisecond))
	markOpenAITimingUpstreamResponse(c, base.Add(30*time.Millisecond), 200)
	markOpenAITimingUpstreamFirstEvent(c, base.Add(40*time.Millisecond))
	markOpenAITimingUpstreamFirstEvent(c, base.Add(50*time.Millisecond))
	markOpenAITimingFirstClientOutput(c, base.Add(55*time.Millisecond))
	markOpenAITimingFirstClientOutput(c, base.Add(65*time.Millisecond))
	markOpenAITimingFirstSemanticOutput(c, base.Add(60*time.Millisecond))
	markOpenAITimingFirstSemanticOutput(c, base.Add(70*time.Millisecond))
	markOpenAITimingFirstClientWrite(c, base.Add(80*time.Millisecond))
	markOpenAITimingFirstClientWrite(c, base.Add(90*time.Millisecond))
	markOpenAITimingUpstreamBodyDone(c, base.Add(100*time.Millisecond))

	trace := openAITiming(c)
	if trace == nil {
		t.Fatal("timing trace was not stored")
	}
	if trace.upstreamAttempts != 2 {
		t.Fatalf("upstream attempts = %d, want 2", trace.upstreamAttempts)
	}
	if !trace.upstreamAttemptStarted.Equal(base.Add(20 * time.Millisecond)) {
		t.Fatalf("upstream attempt start = %v, want final attempt start", trace.upstreamAttemptStarted)
	}
	if !trace.upstreamFirstEventAt.Equal(base.Add(40 * time.Millisecond)) {
		t.Fatalf("first event = %v, want first event timestamp", trace.upstreamFirstEventAt)
	}
	if !trace.firstClientOutputAt.Equal(base.Add(55 * time.Millisecond)) {
		t.Fatalf("client output = %v, want first client output timestamp", trace.firstClientOutputAt)
	}
	if !trace.firstSemanticOutputAt.Equal(base.Add(60 * time.Millisecond)) {
		t.Fatalf("semantic output = %v, want first semantic output timestamp", trace.firstSemanticOutputAt)
	}
	if !trace.firstClientWriteAt.Equal(base.Add(80 * time.Millisecond)) {
		t.Fatalf("client write = %v, want first client write timestamp", trace.firstClientWriteAt)
	}
	if trace.upstreamStatusCode != 200 {
		t.Fatalf("upstream status = %d, want 200", trace.upstreamStatusCode)
	}
	if duration, ok := openAITimingDurationMS(base, base.Add(1234*time.Millisecond)); !ok || duration != 1234 {
		t.Fatalf("duration = %d, ok=%v, want 1234ms/true", duration, ok)
	}
}
