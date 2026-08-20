package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// evaluateOpenAIUserAffinityShadow 只计算推荐账号，不抢槽、不写归属和触达状态。
func (s *OpenAIGatewayService) evaluateOpenAIUserAffinityShadow(ctx context.Context, req OpenAIAccountScheduleRequest) int64 {
	if s == nil || s.settingService == nil || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		return 0
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeShadow {
		return 0
	}
	s.maybeReconcileOpenAIUserAffinity()
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	if userID <= 0 {
		return 0
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityStore)
	if !ok {
		return 0
	}
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(req.GroupID, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	placement, err := store.GetOpenAIUserPlacement(ctx, userID, scopeKey)
	if err != nil {
		return 0
	}
	if placement != nil && placement.Status == "active" && placement.AccountID != nil && now.Before(placement.ExpiresAt) {
		account, accountErr := s.getOpenAIUserAffinityResidentAccount(ctx, *placement.AccountID)
		if accountErr == nil && account != nil && s.classifyOpenAIUserAffinityResidentAdmission(ctx, account, req.GroupID, req.RequestedModel, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport) == openAIUserAffinityResidentAllowed &&
			account.SupportsOpenAIImageCapability(req.RequiredImageCapability) &&
			s.isOpenAIAccountTransportCompatible(account, req.RequiredTransport) &&
			s.openAIAccountMatchesSchedulingGroup(account, req.GroupID) {
			return account.ID
		}
	}
	excluded := cloneExcludedAccountIDs(req.ExcludedIDs)
	if placement != nil && placement.Status == "reset" && placement.ResetExcludeSourceAccount != nil &&
		*placement.ResetExcludeSourceAccount && placement.ResetSourceAccountID != nil {
		if excluded == nil {
			excluded = make(map[int64]struct{})
		}
		excluded[*placement.ResetSourceAccountID] = struct{}{}
	}
	_, candidates, err := s.openAIUserAffinityCandidates(ctx, userID, req.GroupID, req.RequestedModel, excluded, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	if err != nil {
		return 0
	}
	demand := s.predictOpenAIUserAffinityDemand(ctx, userID, config)
	candidate, found := SelectOpenAIUserAffinityCandidate(config, candidates, demand.Demand5H, demand.Demand7D, now)
	if !found {
		return 0
	}
	return candidate.AccountID
}

func (s *OpenAIGatewayService) recordOpenAIUserAffinityShadowDecision(ctx context.Context, expectedAccountID, actualAccountID int64) {
	if expectedAccountID <= 0 {
		return
	}
	s.openaiAffinity.metrics.shadowEvaluations.Add(1)
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	slog.Info("openai_user_affinity.shadow_decision",
		"user_id", userID,
		"expected_account_id", expectedAccountID,
		"actual_account_id", actualAccountID,
		"matched", expectedAccountID == actualAccountID,
	)
}
