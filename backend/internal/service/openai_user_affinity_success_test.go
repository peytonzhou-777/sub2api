package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type openAIUserAffinitySuccessSettingRepo struct {
	SettingRepository
	value string
}

func (r *openAIUserAffinitySuccessSettingRepo) GetValue(context.Context, string) (string, error) {
	return r.value, nil
}

type openAIUserAffinitySuccessTouchRepo struct {
	AccountRepository
	mu        sync.Mutex
	touches   []OpenAIUserAffinityConfig
	confirms  int
	rollbacks int
	touchCh   chan struct{}
}

func (r *openAIUserAffinitySuccessTouchRepo) ConfirmOpenAIUserAffinitySuccess(context.Context, int64, int64, int64, string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirms++
	return nil
}

func (r *openAIUserAffinitySuccessTouchRepo) RollbackOpenAIUserAffinityPlacement(context.Context, OpenAIUserAffinityProvisionalTransition, OpenAIUserAffinityConfig) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollbacks++
	return true, nil
}

func (r *openAIUserAffinitySuccessTouchRepo) TouchOpenAIUserAffinity(_ context.Context, _, _, _ int64, _ string, config OpenAIUserAffinityConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.touches = append(r.touches, config)
	if r.touchCh != nil {
		select {
		case r.touchCh <- struct{}{}:
		default:
		}
	}
	return nil
}

func (r *openAIUserAffinitySuccessTouchRepo) touchCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.touches)
}

func (r *openAIUserAffinitySuccessTouchRepo) confirmCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.confirms
}

func newOpenAIUserAffinitySuccessTestService(t *testing.T, mode string) (*OpenAIGatewayService, *openAIUserAffinitySuccessTouchRepo) {
	t.Helper()
	config := DefaultOpenAIUserAffinityConfig()
	config.Enabled = true
	config.Mode = OpenAIUserAffinityModeEnforce
	config.TouchSuccessMode = mode
	config.ConfigVersion = 7
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	touchRepo := &openAIUserAffinitySuccessTouchRepo{}
	return &OpenAIGatewayService{
		accountRepo:    touchRepo,
		settingService: NewSettingService(&openAIUserAffinitySuccessSettingRepo{value: string(raw)}, nil),
	}, touchRepo
}

func openAIUserAffinitySuccessTestContext(requestID string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(42))
	return context.WithValue(ctx, ctxkey.RequestID, requestID)
}

func TestOpenAIUserAffinityAcceptedModeTouchesImmediately(t *testing.T) {
	service, repo := newOpenAIUserAffinitySuccessTestService(t, OpenAIUserAffinityTouchAccepted)
	ctx := openAIUserAffinitySuccessTestContext("req-accepted")

	service.RecordOpenAIUserAffinityAccepted(ctx, 9)
	require.Equal(t, 1, repo.touchCount())
	service.RecordOpenAIUserAffinitySuccess(ctx, 9)
	require.Equal(t, 1, repo.touchCount(), "完成钩子不得重复刷新 accepted 请求")
}

func TestOpenAIUserAffinityAcceptedDoesNotConfirmIncidentUntilTerminalSuccess(t *testing.T) {
	service, repo := newOpenAIUserAffinitySuccessTestService(t, OpenAIUserAffinityTouchAccepted)
	ctx := openAIUserAffinitySuccessTestContext("req-accepted-terminal")
	accountID := int64(9)
	service.rememberOpenAIUserAffinityAttempt(ctx, &OpenAIUserPlacement{
		UserID: 42, ScopeKey: "openai:v1:group:1:lane:general", AccountID: &accountID, Generation: 3,
	})

	service.RecordOpenAIUserAffinityAccepted(ctx, accountID)
	require.Equal(t, 1, repo.touchCount())
	require.Zero(t, repo.confirmCount())

	service.RecordOpenAIUserAffinitySuccess(ctx, accountID)
	require.Equal(t, 1, repo.confirmCount())
}

func TestOpenAIUserAffinityCompletedModeRequiresAcceptedFact(t *testing.T) {
	service, repo := newOpenAIUserAffinitySuccessTestService(t, OpenAIUserAffinityTouchCompleted)
	ctx := openAIUserAffinitySuccessTestContext("req-completed")

	service.RecordOpenAIUserAffinitySuccess(ctx, 9)
	require.Zero(t, repo.touchCount(), "没有非错误上游响应时不得刷新")
	service.RecordOpenAIUserAffinityAccepted(ctx, 9)
	require.Zero(t, repo.touchCount())
	service.RecordOpenAIUserAffinitySuccess(ctx, 9)
	require.Equal(t, 1, repo.touchCount())
	service.RecordOpenAIUserAffinitySuccess(ctx, 9)
	require.Equal(t, 1, repo.touchCount(), "完成事实只能消费一次")
}

func TestOpenAIUserAffinityWebSocketTurnsUseIndependentSuccessKeys(t *testing.T) {
	service, repo := newOpenAIUserAffinitySuccessTestService(t, OpenAIUserAffinityTouchCompleted)
	ctx := openAIUserAffinitySuccessTestContext("req-websocket")

	service.RecordOpenAIUserAffinityAccepted(ctx, 9, "ws-turn-1")
	service.RecordOpenAIUserAffinityAccepted(ctx, 9, "ws-turn-2")
	service.RecordOpenAIUserAffinitySuccess(ctx, 9, "ws-turn-1")
	service.RecordOpenAIUserAffinitySuccess(ctx, 9, "ws-turn-2")
	require.Equal(t, 2, repo.touchCount())
}

func TestOpenAIUserAffinityCanceledOldAttemptDoesNotDeleteReplacement(t *testing.T) {
	service := &OpenAIGatewayService{}
	ctx, cancel := context.WithCancel(openAIUserAffinitySuccessTestContext("req-reused"))
	accountID := int64(9)
	placement := &OpenAIUserPlacement{
		UserID: 42, ScopeKey: "openai:v1:group=1", AccountID: &accountID, Generation: 3,
	}
	service.rememberOpenAIUserAffinityAttempt(ctx, placement)
	key := openAIUserAffinityRequestKey(ctx)
	replacement := &openAIUserAffinityRequestState{
		scopeKey: placement.ScopeKey, generation: placement.Generation,
		accountID: accountID, createdAt: time.Now().UTC(),
	}
	service.openaiAffinity.requests.Store(key, replacement)

	cancel()
	require.Never(t, func() bool {
		_, ok := service.openaiAffinity.requests.Load(key)
		return !ok
	}, 250*time.Millisecond, 10*time.Millisecond)
	value, ok := service.openaiAffinity.requests.Load(key)
	require.True(t, ok)
	require.Same(t, replacement, value)
}

func TestOpenAIUserAffinityAttemptFailureClearsHTTPAcceptedState(t *testing.T) {
	service := &OpenAIGatewayService{}
	ctx := openAIUserAffinitySuccessTestContext("req-failed")
	requestKey := openAIUserAffinityRequestKey(ctx)
	service.openaiAffinity.requests.Store(requestKey, &openAIUserAffinityRequestState{accountID: 9})
	acceptedKey := openAIUserAffinitySuccessKey(ctx, 9)
	service.openaiAffinity.accepted.Store(acceptedKey, openAIUserAffinityAcceptedState{createdAt: time.Now().UTC()})

	service.failOpenAIUserAffinityReentryLeader(ctx)
	_, exists := service.openaiAffinity.accepted.Load(acceptedKey)
	require.False(t, exists)
}

func TestOpenAIUserAffinityAttemptFailureRollsBackProvisionalPlacement(t *testing.T) {
	service, repo := newOpenAIUserAffinitySuccessTestService(t, OpenAIUserAffinityTouchCompleted)
	ctx := openAIUserAffinitySuccessTestContext("req-provisional-failed")
	accountID := int64(9)
	placement := OpenAIUserPlacement{
		UserID: 42, ScopeKey: "openai:v1:group:1:lane:general", AccountID: &accountID, Generation: 3,
	}
	service.openaiAffinity.requests.Store(openAIUserAffinityRequestKey(ctx), &openAIUserAffinityRequestState{
		accountID: accountID, scopeKey: placement.ScopeKey, generation: placement.Generation,
		provisional: &OpenAIUserAffinityProvisionalTransition{
			Kind: "assignment", Token: "request-token", TargetPlacement: placement,
		},
	})

	require.True(t, service.failOpenAIUserAffinityReentryLeader(ctx))
	repo.mu.Lock()
	defer repo.mu.Unlock()
	require.Equal(t, 1, repo.rollbacks)
}
