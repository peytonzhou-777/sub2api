package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

type openAIUserAffinityAcceptedState struct {
	config    OpenAIUserAffinityConfig
	createdAt time.Time
}

// RecordOpenAIUserAffinityAccepted 在收到首个非错误上游响应时冻结成功判定配置。
func (s *OpenAIGatewayService) RecordOpenAIUserAffinityAccepted(ctx context.Context, accountID int64, eventKeys ...string) {
	if s == nil || s.settingService == nil || s.accountRepo == nil || accountID <= 0 {
		return
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeEnforce {
		return
	}
	key := openAIUserAffinitySuccessKey(ctx, accountID, eventKeys...)
	if config.TouchSuccessMode == OpenAIUserAffinityTouchCompleted {
		if key != "" {
			s.openaiAffinity.accepted.Store(key, openAIUserAffinityAcceptedState{config: config, createdAt: time.Now().UTC()})
			s.pruneOpenAIUserAffinityAcceptedStates()
		}
		return
	}
	if config.TouchSuccessMode == OpenAIUserAffinityTouchAccepted {
		s.touchOpenAIUserAffinity(ctx, accountID, config)
	}
}

// RecordOpenAIUserAffinitySuccess 在响应或 WebSocket turn 成功完成后消费 accepted 事实。
func (s *OpenAIGatewayService) RecordOpenAIUserAffinitySuccess(ctx context.Context, accountID int64, eventKeys ...string) {
	if s == nil || accountID <= 0 {
		return
	}
	key := openAIUserAffinitySuccessKey(ctx, accountID, eventKeys...)
	if key == "" {
		return
	}
	value, ok := s.openaiAffinity.accepted.LoadAndDelete(key)
	if !ok {
		return
	}
	state, ok := value.(openAIUserAffinityAcceptedState)
	if !ok || state.config.TouchSuccessMode != OpenAIUserAffinityTouchCompleted {
		return
	}
	latest, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !latest.Enabled || latest.Mode != OpenAIUserAffinityModeEnforce {
		return
	}
	s.touchOpenAIUserAffinity(ctx, accountID, state.config)
}

func (s *OpenAIGatewayService) touchOpenAIUserAffinity(ctx context.Context, accountID int64, config OpenAIUserAffinityConfig) {
	userID, ok := ctx.Value(ctxkey.UserID).(int64)
	if !ok || userID <= 0 {
		return
	}
	touchStore, ok := s.accountRepo.(OpenAIUserAffinityTouchStore)
	if !ok {
		return
	}
	generation := int64(0)
	scopeKey := ""
	if attempt, found := s.openAIUserAffinityAttempt(ctx, accountID); found {
		generation = attempt.generation
		scopeKey = attempt.scopeKey
	}
	if err := touchStore.TouchOpenAIUserAffinity(ctx, userID, accountID, generation, scopeKey, config); err != nil {
		slog.Warn("openai_user_affinity.touch_refresh_failed", "user_id", userID, "account_id", accountID, "error", err)
		return
	}
	s.activateOpenAIUserAffinityReentry(ctx)
}

func openAIUserAffinitySuccessKey(ctx context.Context, accountID int64, eventKeys ...string) string {
	base := openAIUserAffinityRequestKey(ctx)
	if base == "" {
		return ""
	}
	eventKey := "http"
	if len(eventKeys) > 0 && strings.TrimSpace(eventKeys[0]) != "" {
		eventKey = strings.TrimSpace(eventKeys[0])
	}
	return fmt.Sprintf("%s:%d:%s", base, accountID, eventKey)
}

// pruneOpenAIUserAffinityAcceptedStates 清理流式失败等未进入 completed 钩子的旧请求快照。
func (s *OpenAIGatewayService) pruneOpenAIUserAffinityAcceptedStates() {
	now := time.Now().UTC()
	previous := s.openaiAffinity.acceptedPruneUnix.Load()
	if previous > 0 && now.Sub(time.Unix(previous, 0)) < time.Minute {
		return
	}
	if !s.openaiAffinity.acceptedPruneUnix.CompareAndSwap(previous, now.Unix()) {
		return
	}
	cutoff := now.Add(-10 * time.Minute)
	s.openaiAffinity.accepted.Range(func(key, value any) bool {
		state, ok := value.(openAIUserAffinityAcceptedState)
		if !ok || state.createdAt.Before(cutoff) {
			s.openaiAffinity.accepted.Delete(key)
		}
		return true
	})
}

// RecordOpenAIUserAffinityCapacityFailure 记录一次客户端可见的居民账号准入失败。
func (s *OpenAIGatewayService) RecordOpenAIUserAffinityCapacityFailure(ctx context.Context, accountID int64, reasons ...string) {
	if s == nil || s.settingService == nil || s.accountRepo == nil || accountID <= 0 {
		return
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeEnforce {
		return
	}
	userID, ok := ctx.Value(ctxkey.UserID).(int64)
	if !ok || userID <= 0 {
		return
	}
	runtimeStore, runtimeOK := s.accountRepo.(OpenAIUserAffinityRuntimeStore)
	if !runtimeOK {
		return
	}
	attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
	if !found {
		return
	}
	s.failOpenAIUserAffinityReentryLeader(ctx)
	reason := "concurrency_unavailable"
	if len(reasons) > 0 && strings.TrimSpace(reasons[0]) != "" {
		reason = strings.TrimSpace(reasons[0])
	}
	requestIDHash := openAIUserAffinityRequestIDHash(ctx)
	if requestIDHash == "" {
		slog.Warn("openai_user_affinity.capacity_failure_missing_request_id", "user_id", userID, "account_id", accountID)
		return
	}
	if _, err := runtimeStore.RecordOpenAIUserAffinityCapacityFailure(ctx, userID, accountID, attempt.generation, attempt.scopeKey, requestIDHash, reason, config); err != nil {
		slog.Warn("openai_user_affinity.capacity_failure_record_failed", "user_id", userID, "account_id", accountID, "error", err)
	}
}

func (s *OpenAIGatewayService) predictOpenAIUserAffinityDemand(ctx context.Context, userID int64, config OpenAIUserAffinityConfig) OpenAIUserAffinityDemand {
	fallback := OpenAIUserAffinityDemand{Demand5H: 0.05, Demand7D: 0.05, Version: "cold_start_v1"}
	if userID <= 0 {
		return fallback
	}
	if cached, ok := s.openaiAffinity.demandCache.Load(userID); ok {
		if value, valid := cached.(openAIUserAffinityDemandCacheEntry); valid && time.Now().Before(value.expiresAt) {
			return value.demand
		}
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityRuntimeStore)
	if !ok {
		return fallback
	}
	demand, err := store.PredictOpenAIUserAffinityDemand(ctx, userID, config.ColdStartDemandQuantile)
	if err != nil {
		slog.Warn("openai_user_affinity.demand_prediction_failed", "user_id", userID, "error", err)
		return fallback
	}
	s.openaiAffinity.demandCache.Store(userID, openAIUserAffinityDemandCacheEntry{demand: demand, expiresAt: time.Now().Add(5 * time.Minute)})
	return demand
}

type openAIUserAffinityDemandCacheEntry struct {
	demand    OpenAIUserAffinityDemand
	expiresAt time.Time
}

func recordOpenAIUserAffinityCapacityFailure(ctx context.Context, store OpenAIUserAffinityRuntimeStore, userID, accountID, generation int64, scopeKey, reason string, config OpenAIUserAffinityConfig) (*time.Time, error) {
	requestIDHash := openAIUserAffinityRequestIDHash(ctx)
	if requestIDHash == "" {
		return nil, nil
	}
	return store.RecordOpenAIUserAffinityCapacityFailure(ctx, userID, accountID, generation, scopeKey, requestIDHash, reason, config)
}

// getOpenAIUserAffinityResidentAccount 读取居民账号原始状态，避免新流量调度阈值提前抹掉失败类别。
func (s *OpenAIGatewayService) getOpenAIUserAffinityResidentAccount(ctx context.Context, accountID int64) (*Account, error) {
	if s.schedulerSnapshot != nil {
		return s.schedulerSnapshot.GetAccount(ctx, accountID)
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if errors.Is(err, ErrAccountNotFound) {
		return nil, nil
	}
	return account, err
}
