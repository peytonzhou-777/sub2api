package service

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexLifecyclePrefetcherStub struct {
	accountIDs chan int64
}

func (s *codexLifecyclePrefetcherStub) QueryUsage(_ context.Context, accountID int64) (*OpenAIQuotaUsage, error) {
	s.accountIDs <- accountID
	return &OpenAIQuotaUsage{}, nil
}

func TestCodexLifecyclePrefetchStateNewEpochAndIdle(t *testing.T) {
	var state codexLifecyclePrefetchState
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	const key = "account:scope:epoch"

	require.True(t, state.tryBegin(key, now, 2*time.Hour))
	require.False(t, state.tryBegin(key, now.Add(time.Minute), 2*time.Hour))
	state.finish(key)
	require.False(t, state.tryBegin(key, now.Add(119*time.Minute), 2*time.Hour))
	require.True(t, state.tryBegin(key, now.Add(4*time.Hour), 2*time.Hour))
	require.True(t, state.tryBegin(key+":2", now.Add(4*time.Hour), 2*time.Hour))
}

func TestMaybeStartCodexLifecyclePrefetchCoalescesShadowAndParent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set("api_key", &APIKey{ID: 7, UserID: 9})
	prefetcher := &codexLifecyclePrefetcherStub{accountIDs: make(chan int64, 4)}
	svc := &OpenAIGatewayService{}
	svc.SetCodexLifecycleQuotaPrefetcher(prefetcher)
	svc.setCodexLifecyclePrefetchDelayForTest(func(string) time.Duration { return 0 })
	defer svc.SetCodexLifecycleQuotaPrefetcher(nil)
	parentID := int64(10)
	shadow := &Account{ID: 20, ParentAccountID: &parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	parent := &Account{ID: parentID, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	ids := &codexFingerprintIDs{sessionScopeHash: "scope-a", sessionEpoch: 3, sessionSlot: 1}

	svc.maybeStartCodexLifecyclePrefetch(context.Background(), c, shadow, ids)
	select {
	case accountID := <-prefetcher.accountIDs:
		require.Equal(t, shadow.ID, accountID, "实际查询仍应由额度服务解析影子凭据")
	case <-time.After(time.Second):
		t.Fatal("生命周期预取未启动")
	}

	svc.maybeStartCodexLifecyclePrefetch(context.Background(), c, parent, ids)
	select {
	case <-prefetcher.accountIDs:
		t.Fatal("影子与母账号不得针对同一上游生命周期重复预取")
	case <-time.After(50 * time.Millisecond):
	}

	rotated := *ids
	rotated.sessionEpoch++
	svc.maybeStartCodexLifecyclePrefetch(context.Background(), c, parent, &rotated)
	select {
	case accountID := <-prefetcher.accountIDs:
		require.Equal(t, parent.ID, accountID)
	case <-time.After(time.Second):
		t.Fatal("新 epoch 应触发新的生命周期预取")
	}
}
