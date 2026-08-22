package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
)

var errOpenAIUserAffinityReentryUnavailable = errors.New("openai resident reentry coordination unavailable")
var errOpenAIUserAffinityPlacementChanged = errors.New("openai resident placement changed while waiting")

var (
	ErrOpenAIUserAffinityReentryBatchNotReady = errors.New("openai resident reentry batch not ready")
	ErrOpenAIUserAffinityReentryQueueFull     = errors.New("openai resident reentry follower queue full")
)

const openAIUserAffinityReconcileInterval = 10 * time.Minute

type openAIUserAffinityRequestState struct {
	admission             OpenAIUserAffinityReentryAdmission
	released              bool
	provisional           *OpenAIUserAffinityProvisionalTransition
	scopeKey              string
	generation            int64
	accountID             int64
	createdAt             time.Time
	conversation          *OpenAIUserConversationTransition
	conversationCommitted atomic.Bool
	responseAliasHash     atomic.Value
}

func (s *openAIUserAffinityRequestState) openAIUserAffinityIncidentIdentity(userID int64) OpenAIUserAffinityIncidentIdentity {
	if s == nil {
		return OpenAIUserAffinityIncidentIdentity{UserID: userID}
	}
	identity := OpenAIUserAffinityIncidentIdentity{
		UserID: userID, AccountID: s.accountID, ScopeKey: s.scopeKey, PlacementGeneration: s.generation,
	}
	if s.conversation != nil {
		identity.ConversationHash = s.conversation.ConversationHash
		identity.ResidentSlotID = s.conversation.ResidentSlotID
		identity.SlotGeneration = s.conversation.SlotGeneration
	}
	return identity
}

type openAIUserAffinityMetrics struct {
	reentryLeaders                  atomic.Uint64
	reentryFollowers                atomic.Uint64
	followersReleased               atomic.Uint64
	leaderTakeovers                 atomic.Uint64
	reentryLeaderFailures           atomic.Uint64
	shadowEvaluations               atomic.Uint64
	conversationHits                atomic.Uint64
	residentSlotHits                atomic.Uint64
	residentSlotFillAttempts        atomic.Uint64
	conversationFailoverAttempts    atomic.Uint64
	residentSlotReplacementAttempts atomic.Uint64
}

// openAIUserAffinityState 集中保存进程级亲和性协调状态，减少网关主结构的接线字段。
type openAIUserAffinityState struct {
	requests          sync.Map
	accepted          sync.Map
	demandCache       sync.Map
	metrics           openAIUserAffinityMetrics
	lastReconcileUnix atomic.Int64
	acceptedPruneUnix atomic.Int64
}

type OpenAIUserAffinityMetricsSnapshot struct {
	ReentryLeaders                  uint64
	ReentryFollowers                uint64
	FollowersReleased               uint64
	LeaderTakeovers                 uint64
	ReentryLeaderFailures           uint64
	ShadowEvaluations               uint64
	ConversationHits                uint64
	ResidentSlotHits                uint64
	ResidentSlotFillAttempts        uint64
	ConversationFailoverAttempts    uint64
	ResidentSlotReplacementAttempts uint64
}

// SnapshotOpenAIUserAffinityMetrics 返回进程级协调指标，供运维采集器和诊断使用。
func (s *OpenAIGatewayService) SnapshotOpenAIUserAffinityMetrics() OpenAIUserAffinityMetricsSnapshot {
	if s == nil {
		return OpenAIUserAffinityMetricsSnapshot{}
	}
	return OpenAIUserAffinityMetricsSnapshot{
		ReentryLeaders:                  s.openaiAffinity.metrics.reentryLeaders.Load(),
		ReentryFollowers:                s.openaiAffinity.metrics.reentryFollowers.Load(),
		FollowersReleased:               s.openaiAffinity.metrics.followersReleased.Load(),
		LeaderTakeovers:                 s.openaiAffinity.metrics.leaderTakeovers.Load(),
		ReentryLeaderFailures:           s.openaiAffinity.metrics.reentryLeaderFailures.Load(),
		ShadowEvaluations:               s.openaiAffinity.metrics.shadowEvaluations.Load(),
		ConversationHits:                s.openaiAffinity.metrics.conversationHits.Load(),
		ResidentSlotHits:                s.openaiAffinity.metrics.residentSlotHits.Load(),
		ResidentSlotFillAttempts:        s.openaiAffinity.metrics.residentSlotFillAttempts.Load(),
		ConversationFailoverAttempts:    s.openaiAffinity.metrics.conversationFailoverAttempts.Load(),
		ResidentSlotReplacementAttempts: s.openaiAffinity.metrics.residentSlotReplacementAttempts.Load(),
	}
}

// coordinateOpenAIUserAffinityReentry 在账号抢槽前合并同用户回流，并仅按 FIFO 放行独立请求。
func (s *OpenAIGatewayService) coordinateOpenAIUserAffinityReentry(ctx context.Context, placement *OpenAIUserPlacement, config OpenAIUserAffinityConfig) error {
	if s == nil || placement == nil || placement.AccountID == nil {
		return nil
	}
	runtimeStore, ok := s.accountRepo.(OpenAIUserAffinityRuntimeStore)
	if !ok {
		return errOpenAIUserAffinityReentryUnavailable
	}
	queue, ok := s.cache.(OpenAIUserAffinityReentryQueue)
	if !ok {
		return errOpenAIUserAffinityReentryUnavailable
	}
	requestKey := openAIUserAffinityRequestKey(ctx)
	if requestKey == "" {
		return errOpenAIUserAffinityReentryUnavailable
	}
	if current, found := s.openaiAffinity.requests.Load(requestKey); found {
		state, valid := current.(*openAIUserAffinityRequestState)
		if valid && state != nil && state.admission.AccountID == *placement.AccountID && state.admission.Generation == placement.Generation {
			return nil
		}
	}
	deadline := time.Now().Add(2 * time.Minute)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	batchToken := uuid.NewString()
	leaderToken := uuid.NewString()
	admission, err := runtimeStore.BeginOpenAIUserAffinityReentry(ctx, OpenAIUserAffinityReentryBegin{
		UserID: placement.UserID, AccountID: *placement.AccountID, Generation: placement.Generation,
		ScopeKey:   placement.ScopeKey,
		BatchToken: batchToken, LeaderToken: leaderToken,
		LeaderLeaseUntil: minOpenAIUserAffinityTime(deadline, time.Now().Add(30*time.Second)), Config: config,
	})
	if err != nil || admission == nil || !admission.Required {
		return err
	}
	admission.Deadline = deadline
	admission.MaxFollowers = s.schedulingConfig().StickySessionMaxWaiting
	if admission.MaxFollowers <= 0 {
		admission.MaxFollowers = 100
	}
	if admission.Leader {
		if err := queue.InitializeOpenAIUserAffinityReentry(ctx, *admission); err != nil {
			_, _ = runtimeStore.FailOpenAIUserAffinityReentryLeader(ctx, openAIUserAffinityReentryTransition(*admission))
			return errOpenAIUserAffinityReentryUnavailable
		}
		s.openaiAffinity.requests.Store(requestKey, &openAIUserAffinityRequestState{
			admission: *admission, scopeKey: placement.ScopeKey, generation: placement.Generation,
			accountID: *placement.AccountID, createdAt: time.Now().UTC(),
		})
		s.openaiAffinity.metrics.reentryLeaders.Add(1)
		slog.Info("openai_user_affinity.reentry_leader", "user_id", admission.UserID, "account_id", admission.AccountID, "generation", admission.Generation)
		return nil
	}

	admission.WaiterToken = uuid.NewString()
	if err := enqueueOpenAIUserAffinityFollower(ctx, queue, *admission); err != nil {
		return errOpenAIUserAffinityReentryUnavailable
	}
	s.openaiAffinity.metrics.reentryFollowers.Add(1)
	defer func() {
		if ctx.Err() != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = queue.RemoveOpenAIUserAffinityFollower(cleanupCtx, *admission)
		}
	}()
	pollTimer := time.NewTicker(25 * time.Millisecond)
	defer pollTimer.Stop()
	placementTimer := time.NewTicker(250 * time.Millisecond)
	defer placementTimer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-placementTimer.C:
			placementStore, placementOK := s.accountRepo.(OpenAIUserAffinityStore)
			if !placementOK {
				return errOpenAIUserAffinityReentryUnavailable
			}
			latest, latestErr := placementStore.GetOpenAIUserPlacement(ctx, admission.UserID, admission.ScopeKey)
			if latestErr != nil {
				return latestErr
			}
			if latest == nil || latest.AccountID == nil || latest.Status != "active" ||
				*latest.AccountID != admission.AccountID || latest.Generation != admission.Generation {
				_ = queue.RemoveOpenAIUserAffinityFollower(ctx, *admission)
				return errOpenAIUserAffinityPlacementChanged
			}
		case now := <-pollTimer.C:
			poll, pollErr := queue.PollOpenAIUserAffinityFollower(ctx, *admission, now)
			if pollErr != nil {
				_ = queue.RemoveOpenAIUserAffinityFollower(context.Background(), *admission)
				return errOpenAIUserAffinityReentryUnavailable
			}
			if poll.Released {
				empty, ackErr := queue.AcknowledgeOpenAIUserAffinityFollower(ctx, *admission, now)
				if ackErr != nil {
					return errOpenAIUserAffinityReentryUnavailable
				}
				if empty {
					_ = runtimeStore.CompleteOpenAIUserAffinityReentry(ctx, admission.AccountID, admission.UserID, openAIUserAffinityAdmissionCoordinationGeneration(*admission), admission.BatchToken)
				}
				s.openaiAffinity.requests.Store(requestKey, &openAIUserAffinityRequestState{
					admission: *admission, released: true, scopeKey: placement.ScopeKey,
					generation: placement.Generation, accountID: *placement.AccountID, createdAt: time.Now().UTC(),
				})
				s.openaiAffinity.metrics.followersReleased.Add(1)
				return nil
			}
			if poll.MayTakeover {
				takeover, takeoverErr := runtimeStore.TakeoverOpenAIUserAffinityReentry(ctx, OpenAIUserAffinityReentryTakeover{
					UserID: admission.UserID, AccountID: admission.AccountID, Generation: admission.Generation,
					CoordinationGeneration: admission.CoordinationGeneration,
					ScopeKey:               admission.ScopeKey,
					BatchToken:             admission.BatchToken, WaiterToken: admission.WaiterToken,
					ExpectedLeaderVersion: poll.ExpectedLeaderVersion,
					LeaderLeaseUntil:      minOpenAIUserAffinityTime(deadline, now.Add(30*time.Second)),
				})
				if takeoverErr != nil {
					return takeoverErr
				}
				if takeover == nil {
					continue
				}
				takeover.Deadline = deadline
				_ = queue.RemoveOpenAIUserAffinityFollower(ctx, *admission)
				if err := queue.InitializeOpenAIUserAffinityReentry(ctx, *takeover); err != nil {
					_, _ = runtimeStore.FailOpenAIUserAffinityReentryLeader(ctx, openAIUserAffinityReentryTransition(*takeover))
					return errOpenAIUserAffinityReentryUnavailable
				}
				s.openaiAffinity.requests.Store(requestKey, &openAIUserAffinityRequestState{
					admission: *takeover, scopeKey: placement.ScopeKey, generation: placement.Generation,
					accountID: *placement.AccountID, createdAt: time.Now().UTC(),
				})
				s.openaiAffinity.metrics.leaderTakeovers.Add(1)
				return nil
			}
		}
	}
}

// enqueueOpenAIUserAffinityFollower 处理数据库批次提交早于 Redis 发布的跨实例竞态。
func enqueueOpenAIUserAffinityFollower(ctx context.Context, queue OpenAIUserAffinityReentryQueue, admission OpenAIUserAffinityReentryAdmission) error {
	err := queue.EnqueueOpenAIUserAffinityFollower(ctx, admission)
	if !errors.Is(err, ErrOpenAIUserAffinityReentryBatchNotReady) {
		return err
	}
	// follower 只在 Redis 尚无批次时按数据库快照补建；不同批次不会被覆盖。
	if err := queue.InitializeOpenAIUserAffinityReentry(ctx, admission); err != nil {
		return err
	}
	return queue.EnqueueOpenAIUserAffinityFollower(ctx, admission)
}

func (s *OpenAIGatewayService) activateOpenAIUserAffinityReentry(ctx context.Context) {
	requestKey := openAIUserAffinityRequestKey(ctx)
	value, ok := s.openaiAffinity.requests.LoadAndDelete(requestKey)
	if !ok {
		return
	}
	state, ok := value.(*openAIUserAffinityRequestState)
	if !ok || state == nil {
		return
	}
	if !state.admission.Leader {
		return
	}
	runtimeStore, storeOK := s.accountRepo.(OpenAIUserAffinityRuntimeStore)
	queue, queueOK := s.cache.(OpenAIUserAffinityReentryQueue)
	if !storeOK || !queueOK {
		return
	}
	transition := openAIUserAffinityReentryTransition(state.admission)
	activated, err := runtimeStore.ActivateOpenAIUserAffinityReentry(ctx, transition)
	if err != nil || !activated {
		return
	}
	empty, err := queue.ActivateOpenAIUserAffinityFollowers(ctx, state.admission, time.Now())
	if err != nil {
		slog.Warn("openai_user_affinity.reentry_release_failed", "user_id", state.admission.UserID, "account_id", state.admission.AccountID, "error", err)
		return
	}
	if empty {
		_ = runtimeStore.CompleteOpenAIUserAffinityReentry(ctx, state.admission.AccountID, state.admission.UserID, openAIUserAffinityAdmissionCoordinationGeneration(state.admission), state.admission.BatchToken)
	}
}

func (s *OpenAIGatewayService) failOpenAIUserAffinityReentryLeader(ctx context.Context) bool {
	requestKey := openAIUserAffinityRequestKey(ctx)
	value, ok := s.openaiAffinity.requests.LoadAndDelete(requestKey)
	if !ok {
		return false
	}
	state, ok := value.(*openAIUserAffinityRequestState)
	if !ok || state == nil {
		return false
	}
	rolledBack := false
	if state.provisional != nil {
		if store, storeOK := s.accountRepo.(OpenAIUserAffinityProvisionalStore); storeOK {
			var rollbackErr error
			rolledBack, rollbackErr = store.RollbackOpenAIUserAffinityPlacement(ctx, *state.provisional, state.provisional.Config)
			if rollbackErr != nil {
				slog.Warn("openai_user_affinity.provisional_rollback_failed", "user_id", state.provisional.TargetPlacement.UserID, "account_id", state.accountID, "error", rollbackErr)
			} else if rolledBack {
				slog.Info("openai_user_affinity.provisional_rolled_back", "user_id", state.provisional.TargetPlacement.UserID, "account_id", state.accountID, "kind", state.provisional.Kind)
			}
		}
	}
	// HTTP 请求失败后立即丢弃 completed 模式的 accepted 事实，避免等待定期清理。
	s.openaiAffinity.accepted.Delete(openAIUserAffinitySuccessKey(ctx, state.accountID))
	if !state.admission.Leader {
		return rolledBack
	}
	runtimeStore, storeOK := s.accountRepo.(OpenAIUserAffinityRuntimeStore)
	queue, queueOK := s.cache.(OpenAIUserAffinityReentryQueue)
	if !storeOK || !queueOK {
		return rolledBack
	}
	failed, err := runtimeStore.FailOpenAIUserAffinityReentryLeader(ctx, openAIUserAffinityReentryTransition(state.admission))
	if err == nil && failed {
		_ = queue.MarkOpenAIUserAffinityLeaderFailed(ctx, state.admission)
		s.openaiAffinity.metrics.reentryLeaderFailures.Add(1)
	}
	return rolledBack
}

func openAIUserAffinityReentryTransition(admission OpenAIUserAffinityReentryAdmission) OpenAIUserAffinityReentryTransition {
	return OpenAIUserAffinityReentryTransition{
		UserID: admission.UserID, AccountID: admission.AccountID, Generation: admission.Generation,
		CoordinationGeneration: admission.CoordinationGeneration,
		ScopeKey:               admission.ScopeKey,
		BatchToken:             admission.BatchToken, LeaderToken: admission.LeaderToken, LeaderVersion: admission.LeaderVersion,
	}
}

func openAIUserAffinityAdmissionCoordinationGeneration(admission OpenAIUserAffinityReentryAdmission) int64 {
	if admission.CoordinationGeneration > 0 {
		return admission.CoordinationGeneration
	}
	return admission.Generation
}

func openAIUserAffinityRequestKey(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	userPrefix := ""
	if userID, _ := ctx.Value(ctxkey.UserID).(int64); userID > 0 {
		userPrefix = strconv.FormatInt(userID, 10) + ":"
	}
	if value, _ := ctx.Value(ctxkey.ClientRequestID).(string); strings.TrimSpace(value) != "" {
		return userPrefix + strings.TrimSpace(value)
	}
	if value, _ := ctx.Value(ctxkey.RequestID).(string); strings.TrimSpace(value) != "" {
		return userPrefix + strings.TrimSpace(value)
	}
	return ""
}

// rememberOpenAIUserAffinityAttempt 冻结本次选号对应的 scope/generation，供成功与失败钩子使用。
func (s *OpenAIGatewayService) rememberOpenAIUserAffinityAttempt(ctx context.Context, placement *OpenAIUserPlacement, provisional ...*OpenAIUserAffinityProvisionalTransition) {
	if s == nil || placement == nil || placement.AccountID == nil {
		return
	}
	key := openAIUserAffinityRequestKey(ctx)
	if key == "" {
		return
	}
	state := &openAIUserAffinityRequestState{
		scopeKey: placement.ScopeKey, generation: placement.Generation,
		accountID: *placement.AccountID, createdAt: time.Now().UTC(),
	}
	if len(provisional) > 0 {
		state.provisional = provisional[0]
	}
	if current, ok := s.openaiAffinity.requests.Load(key); ok {
		if existing, valid := current.(*openAIUserAffinityRequestState); valid && existing != nil {
			state.admission = existing.admission
			state.released = existing.released
			if state.provisional == nil {
				state.provisional = existing.provisional
			}
		}
	}
	s.openaiAffinity.requests.Store(key, state)
	// 请求未走成功钩子时，context 结束即让 leader 失效，队首 follower 可立即 CAS 接棒。
	context.AfterFunc(ctx, func() {
		current, ok := s.openAIUserAffinityAttempt(ctx, state.accountID)
		if !ok || current != state {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		s.failOpenAIUserAffinityReentryLeader(cleanupCtx)
		s.rollbackOpenAIUserAffinityConversation(cleanupCtx, current)
	})
	s.pruneOpenAIUserAffinityRequestStates()
}

func (s *OpenAIGatewayService) openAIUserAffinityAttempt(ctx context.Context, accountID int64) (*openAIUserAffinityRequestState, bool) {
	if s == nil {
		return nil, false
	}
	value, ok := s.openaiAffinity.requests.Load(openAIUserAffinityRequestKey(ctx))
	state, valid := value.(*openAIUserAffinityRequestState)
	return state, ok && valid && state != nil && state.accountID == accountID && state.scopeKey != "" && state.generation > 0
}

func (s *OpenAIGatewayService) pruneOpenAIUserAffinityRequestStates() {
	if s == nil {
		return
	}
	now := time.Now().UTC()
	s.openaiAffinity.requests.Range(func(key, value any) bool {
		state, ok := value.(*openAIUserAffinityRequestState)
		retention := 10 * time.Minute
		if ok && state != nil && state.conversation != nil && state.conversation.Config.ConversationActiveTTL() > retention {
			retention = state.conversation.Config.ConversationActiveTTL()
		}
		if !ok || state == nil || !state.createdAt.IsZero() && state.createdAt.Add(retention).Before(now) {
			s.openaiAffinity.requests.Delete(key)
		}
		return true
	})
}

func openAIUserAffinityRequestIDHash(ctx context.Context) string {
	requestID := openAIUserAffinityRequestKey(ctx)
	if requestID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(requestID))
	return hex.EncodeToString(sum[:])
}

func minOpenAIUserAffinityTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

// maybeReconcileOpenAIUserAffinity 由粘性流量低频触发，不依赖 Ops 清理开关。
func (s *OpenAIGatewayService) maybeReconcileOpenAIUserAffinity() {
	if s == nil {
		return
	}
	reconciler, ok := s.accountRepo.(OpenAIUserAffinityReconciler)
	if !ok {
		return
	}
	now := time.Now().UTC()
	previous := s.openaiAffinity.lastReconcileUnix.Load()
	if previous > 0 && now.Sub(time.Unix(previous, 0)) < openAIUserAffinityReconcileInterval {
		return
	}
	if !s.openaiAffinity.lastReconcileUnix.CompareAndSwap(previous, now.Unix()) {
		return
	}
	go func(runAt time.Time) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		counts, err := reconciler.ReconcileOpenAIUserAffinity(ctx, runAt)
		if err != nil {
			slog.Warn("openai_user_affinity.reconcile_failed", "error", err)
			return
		}
		slog.Info("openai_user_affinity.reconciled", "counts", counts)
	}(now)
}
