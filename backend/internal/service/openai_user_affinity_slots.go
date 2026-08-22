package service

import (
	"context"
	"math"
	"sort"
	"time"
)

// selectOpenAIUserAffinityResidentSlots 为新会话执行空闲槽、按需填槽和满槽回首选。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityResidentSlots(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, bool, error) {
	if s == nil || s.settingService == nil || s.accountRepo == nil || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		return nil, false, nil
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeEnforce {
		return nil, false, err
	}
	identity, hasConversation := resolveOpenAIUserConversationIdentity(ctx, req)
	if !hasConversation || identity.conversationHash == "" {
		return nil, false, nil
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityConversationStore)
	if !ok {
		return nil, false, nil
	}
	if maintenance, maintenanceOK := s.accountRepo.(OpenAIUserAffinityResidentSlotMaintenanceStore); maintenanceOK {
		if err := maintenance.ConvergeOpenAIUserResidentSlots(ctx, identity.userID, identity.scopeKey, config, time.Now().UTC()); err != nil {
			return nil, true, err
		}
	}
	if config.RuntimeResidentAccountSlotCount() <= 1 {
		return nil, false, nil
	}
	slots, err := store.ListOpenAIUserResidentSlots(ctx, identity.userID, identity.scopeKey)
	if err != nil {
		return nil, true, err
	}
	resetExcluded, resetPending, err := s.applyOpenAIUserAffinityResetExclusions(ctx, identity.userID, identity.scopeKey, req.ExcludedIDs)
	if err != nil {
		return nil, true, err
	}
	now := time.Now().UTC()
	activeSlots := make([]OpenAIUserResidentSlot, 0, len(slots))
	occupiedSlots := make([]OpenAIUserResidentSlot, 0, len(slots))
	for _, slot := range slots {
		if slot.Status == OpenAIUserResidentSlotStatusDraining {
			occupiedSlots = append(occupiedSlots, slot)
			continue
		}
		if !now.Before(slot.ExpiresAt) {
			continue
		}
		if slot.Status == OpenAIUserResidentSlotStatusActive || slot.Status == OpenAIUserResidentSlotStatusProvisional ||
			slot.Status == OpenAIUserResidentSlotStatusReplacementPending {
			activeSlots = append(activeSlots, slot)
			occupiedSlots = append(occupiedSlots, slot)
		}
	}
	if len(slots) == 0 && !resetPending {
		return nil, false, nil
	}
	sortOpenAIUserResidentSlots(activeSlots, config.ResidentTTL(), now)

	// 新会话先复用按当前有效热度排序的空闲 active 槽。
	for i := range activeSlots {
		slot := &activeSlots[i]
		if slot.Status == OpenAIUserResidentSlotStatusActive && slot.ActiveConversationCount == 0 {
			return s.selectOpenAIUserAffinityResidentSlot(ctx, req, config, identity, slot)
		}
	}

	if len(activeSlots) < config.RuntimeResidentAccountSlotCount() {
		return s.fillOpenAIUserAffinityResidentSlot(ctx, req, config, identity, occupiedSlots, activeSlots, resetExcluded, !resetPending)
	}

	// 槽位已满时不因短期拥挤继续扩散，仍按热度选择最常用的可准入槽位。
	for i := range activeSlots {
		slot := &activeSlots[i]
		if slot.Status != OpenAIUserResidentSlotStatusActive {
			continue
		}
		selection, handled, selectErr := s.selectOpenAIUserAffinityResidentSlot(ctx, req, config, identity, slot)
		if selectErr == nil && selection != nil {
			return selection, true, nil
		}
		if handled && selectErr != nil {
			continue
		}
	}
	return nil, true, ErrNoAvailableAccounts
}

func (s *OpenAIGatewayService) selectOpenAIUserAffinityResidentSlot(ctx context.Context, req OpenAIAccountScheduleRequest, config OpenAIUserAffinityConfig, identity openAIUserConversationIdentity, slot *OpenAIUserResidentSlot) (*AccountSelectionResult, bool, error) {
	if slot == nil || slot.AccountID <= 0 {
		return nil, false, nil
	}
	if req.ExcludedIDs != nil {
		if _, excluded := req.ExcludedIDs[slot.AccountID]; excluded {
			return nil, true, ErrNoAvailableAccounts
		}
	}
	account, err := s.getOpenAIUserAffinityResidentAccount(ctx, slot.AccountID)
	if err != nil {
		return nil, true, err
	}
	if s.classifyOpenAIUserAffinityResidentAdmission(ctx, account, req.GroupID, req.RequestedModel,
		req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport) != openAIUserAffinityResidentAllowed {
		return nil, true, ErrNoAvailableAccounts
	}
	placement := &OpenAIUserPlacement{
		UserID: identity.userID, ScopeKey: identity.scopeKey, AccountID: &slot.AccountID,
		Generation: slot.Generation, Status: "active", AssignedAt: slot.AdmittedAt, ExpiresAt: slot.ExpiresAt,
	}
	s.rememberOpenAIUserAffinityAttempt(ctx, placement)
	acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
	if acquireErr == nil && acquired != nil && acquired.Acquired {
		if err := s.reserveOpenAIUserAffinityConversation(ctx, req, account.ID); err != nil {
			if acquired.ReleaseFunc != nil {
				acquired.ReleaseFunc()
			}
			return nil, true, err
		}
		selection, selectionErr := s.newAcquiredSelectionResult(ctx, account, acquired.ReleaseFunc)
		return selection, true, selectionErr
	}
	if err := s.reserveOpenAIUserAffinityConversation(ctx, req, account.ID); err != nil {
		return nil, true, err
	}
	selection, selectionErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
		AccountID: account.ID, MaxConcurrency: account.Concurrency,
		Timeout: s.schedulingConfig().StickySessionWaitTimeout, MaxWaiting: s.schedulingConfig().StickySessionMaxWaiting,
	})
	return selection, true, selectionErr
}

func (s *OpenAIGatewayService) fillOpenAIUserAffinityResidentSlot(ctx context.Context, req OpenAIAccountScheduleRequest, config OpenAIUserAffinityConfig, identity openAIUserConversationIdentity, occupiedSlots, activeSlots []OpenAIUserResidentSlot, baseExcluded map[int64]struct{}, preferExistingAffinity bool) (*AccountSelectionResult, bool, error) {
	excluded := cloneExcludedAccountIDs(baseExcluded)
	if excluded == nil {
		excluded = make(map[int64]struct{}, len(occupiedSlots))
	}
	maxGeneration := int64(0)
	for _, slot := range occupiedSlots {
		excluded[slot.AccountID] = struct{}{}
	}
	for _, slot := range activeSlots {
		if slot.Generation > maxGeneration {
			maxGeneration = slot.Generation
		}
	}
	accounts, candidates, err := s.openAIUserAffinityCandidates(ctx, identity.userID, req.GroupID, req.RequestedModel,
		excluded, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	if err != nil {
		return nil, true, err
	}
	demand := s.predictOpenAIUserAffinityDemand(ctx, identity.userID, config)
	now := time.Now().UTC()
	unknownQuota := false
	for _, candidate := range candidates {
		if !candidate.Quota5HKnown || !candidate.Quota7DKnown {
			unknownQuota = true
			break
		}
	}
	for len(candidates) > 0 {
		candidate, found := selectOpenAIUserAffinityCandidate(config, candidates, demand.Demand5H, demand.Demand7D, now, preferExistingAffinity)
		if !found {
			break
		}
		account := accounts[candidate.AccountID]
		acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if acquireErr == nil && acquired != nil && acquired.Acquired {
			placement := &OpenAIUserPlacement{
				UserID: identity.userID, ScopeKey: identity.scopeKey, AccountID: &account.ID,
				Generation: maxGeneration + 1, Status: "active", AssignedAt: now,
				ExpiresAt: now.Add(config.ResidentTTL()), AssignmentReason: "resident_slot_fill",
			}
			s.rememberOpenAIUserAffinityAttempt(ctx, placement)
			if err := s.reserveOpenAIUserAffinityConversation(ctx, req, account.ID); err != nil {
				if acquired.ReleaseFunc != nil {
					acquired.ReleaseFunc()
				}
				return nil, true, err
			}
			selection, selectionErr := s.newAcquiredSelectionResult(ctx, account, acquired.ReleaseFunc)
			s.openaiAffinity.metrics.residentSlotFillAttempts.Add(1)
			return selection, true, selectionErr
		}
		candidates = removeOpenAIUserAffinityCandidate(candidates, candidate.AccountID)
	}
	if unknownQuota {
		// 未知快照交给普通调度发起一次真实请求自愈，不把未知容量误判为不可用。
		return nil, false, nil
	}
	return nil, true, ErrNoAvailableAccounts
}

// sortOpenAIUserResidentSlots 按实时衰减热度及稳定兜底字段确定首选顺序。
func sortOpenAIUserResidentSlots(slots []OpenAIUserResidentSlot, halfLife time.Duration, now time.Time) {
	if halfLife <= 0 {
		halfLife = 7 * 24 * time.Hour
	}
	score := func(slot OpenAIUserResidentSlot) float64 {
		elapsed := now.Sub(slot.ScoreUpdatedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		return slot.UsageScore * math.Pow(0.5, elapsed.Seconds()/halfLife.Seconds())
	}
	sort.SliceStable(slots, func(i, j int) bool {
		leftScore, rightScore := score(slots[i]), score(slots[j])
		if math.Abs(leftScore-rightScore) > 1e-12 {
			return leftScore > rightScore
		}
		leftSuccess, rightSuccess := slots[i].LastSuccessAt, slots[j].LastSuccessAt
		if leftSuccess != nil || rightSuccess != nil {
			if leftSuccess == nil {
				return false
			}
			if rightSuccess == nil {
				return true
			}
			if !leftSuccess.Equal(*rightSuccess) {
				return leftSuccess.After(*rightSuccess)
			}
		}
		if !slots[i].AdmittedAt.Equal(slots[j].AdmittedAt) {
			return slots[i].AdmittedAt.Before(slots[j].AdmittedAt)
		}
		return slots[i].AccountID < slots[j].AccountID
	})
}
