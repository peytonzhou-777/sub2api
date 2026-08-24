package service

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"
)

// selectOpenAIUserAffinityResidentSlots 让新会话优先收敛到用户活动路由，并在共享时软退让。
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
		// 全新居民仍交给权威 placement 流程完成原子归属预留；其 BestFit 同样应用软占用分层。
		return nil, false, nil
	}
	sortOpenAIUserResidentSlots(activeSlots, config.ResidentTTL(), now)
	routingStore, routingOK := s.accountRepo.(OpenAIUserAffinityActiveRoutingStore)
	var route *OpenAIUserActiveRoute
	if routingOK {
		route, err = routingStore.GetOpenAIUserActiveRoute(ctx, identity.userID, identity.scopeKey)
		if err != nil {
			return nil, true, err
		}
		if route != nil && route.PendingAccountID > 0 && route.PendingExpiresAt != nil && now.Before(*route.PendingExpiresAt) {
			// 同一用户的活动路由切换尚未得到首输出，其他新会话不得并发扩散。
			return nil, true, ErrNoAvailableAccounts
		}
	}

	var routeSlot *OpenAIUserResidentSlot
	if route != nil && route.AccountID > 0 && route.ActiveUntil != nil && now.Before(*route.ActiveUntil) {
		for i := range activeSlots {
			slot := &activeSlots[i]
			if slot.Status == OpenAIUserResidentSlotStatusActive && slot.ID == route.ResidentSlotID &&
				slot.AccountID == route.AccountID && slot.Generation == route.SlotGeneration {
				routeSlot = slot
				break
			}
		}
	}
	if routeSlot != nil && (routeSlot.SoftOwnerUserID == 0 || routeSlot.SoftOwnerUserID == identity.userID) {
		admission, admissionErr := s.openAIUserAffinityResidentSlotAdmission(ctx, req, routeSlot)
		if admissionErr != nil {
			return nil, true, admissionErr
		}
		if admission == openAIUserAffinityResidentAllowed || admission == openAIUserAffinityResidentTemporaryCapacity {
			// 自己拥有或尚无人拥有的活动账号保持稳定；临时容量失败也不主动扩散。
			return s.selectOpenAIUserAffinityResidentSlot(ctx, req, config, identity, routeSlot)
		}
	}

	// 非主用户的新会话先退让到自己拥有的其他槽位，再尝试无人占用槽位。
	for _, tier := range []int{0, 1} {
		for i := range activeSlots {
			slot := &activeSlots[i]
			if slot.Status != OpenAIUserResidentSlotStatusActive || routeSlot != nil && slot.ID == routeSlot.ID ||
				openAIUserAffinityResidentSlotOwnershipTier(*slot, identity.userID) != tier {
				continue
			}
			admission, admissionErr := s.openAIUserAffinityResidentSlotAdmission(ctx, req, slot)
			if admissionErr != nil {
				return nil, true, admissionErr
			}
			if admission != openAIUserAffinityResidentAllowed {
				continue
			}
			return s.selectOpenAIUserAffinityResidentSlot(ctx, req, config, identity, slot)
		}
	}

	if len(activeSlots) < config.RuntimeResidentAccountSlotCount() {
		selection, handled, fillErr := s.fillOpenAIUserAffinityResidentSlot(ctx, req, config, identity, occupiedSlots, activeSlots, resetExcluded, !resetPending)
		if selection != nil || !handled || fillErr != nil && !errors.Is(fillErr, ErrNoAvailableAccounts) {
			return selection, handled, fillErr
		}
	}

	// 无空闲槽可退让时继续允许共享；活动路由优先，其他共享槽按活跃用户数升序。
	if routeSlot != nil {
		selection, handled, selectErr := s.selectOpenAIUserAffinityResidentSlot(ctx, req, config, identity, routeSlot)
		if selectErr == nil && selection != nil {
			return selection, true, nil
		}
		if !handled {
			return nil, false, selectErr
		}
	}
	sort.SliceStable(activeSlots, func(i, j int) bool {
		if activeSlots[i].ActiveRouteUserCount != activeSlots[j].ActiveRouteUserCount {
			return activeSlots[i].ActiveRouteUserCount < activeSlots[j].ActiveRouteUserCount
		}
		return false
	})
	for i := range activeSlots {
		slot := &activeSlots[i]
		if slot.Status != OpenAIUserResidentSlotStatusActive || routeSlot != nil && slot.ID == routeSlot.ID {
			continue
		}
		admission, admissionErr := s.openAIUserAffinityResidentSlotAdmission(ctx, req, slot)
		if admissionErr != nil {
			return nil, true, admissionErr
		}
		if admission != openAIUserAffinityResidentAllowed {
			continue
		}
		return s.selectOpenAIUserAffinityResidentSlot(ctx, req, config, identity, slot)
	}
	return nil, true, ErrNoAvailableAccounts
}

func (s *OpenAIGatewayService) openAIUserAffinityResidentSlotAdmission(ctx context.Context, req OpenAIAccountScheduleRequest, slot *OpenAIUserResidentSlot) (openAIUserAffinityResidentAdmission, error) {
	if slot == nil || slot.AccountID <= 0 {
		return openAIUserAffinityResidentPermanentUnavailable, nil
	}
	account, err := s.getOpenAIUserAffinityResidentAccount(ctx, slot.AccountID)
	if err != nil {
		return openAIUserAffinityResidentPermanentUnavailable, err
	}
	return s.classifyOpenAIUserAffinityResidentAdmission(ctx, account, req.GroupID, req.RequestedModel,
		req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport), nil
}

func openAIUserAffinityResidentSlotOwnershipTier(slot OpenAIUserResidentSlot, userID int64) int {
	switch {
	case slot.SoftOwnerUserID == userID && userID > 0:
		return 0
	case slot.SoftOwnerUserID == 0:
		return 1
	default:
		return 2
	}
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
	occupancies := make(map[int64]OpenAIAccountSoftOccupancy)
	if routingStore, ok := s.accountRepo.(OpenAIUserAffinityActiveRoutingStore); ok {
		accountIDs := make([]int64, 0, len(candidates))
		for _, candidate := range candidates {
			accountIDs = append(accountIDs, candidate.AccountID)
		}
		occupancies, err = routingStore.ListOpenAIAccountSoftOccupancies(ctx, accountIDs)
		if err != nil {
			return nil, true, err
		}
	}
	for _, tierCandidates := range openAIUserAffinitySoftOccupancyCandidateTiers(
		candidates, occupancies, identity.userID, len(activeSlots) == 0,
	) {
		for len(tierCandidates) > 0 {
			candidate, found := selectOpenAIUserAffinityCandidate(config, tierCandidates, demand.Demand5H, demand.Demand7D, now, preferExistingAffinity)
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
			tierCandidates = removeOpenAIUserAffinityCandidate(tierCandidates, candidate.AccountID)
		}
	}
	if unknownQuota {
		// 未知快照交给普通调度发起一次真实请求自愈，不把未知容量误判为不可用。
		return nil, false, nil
	}
	return nil, true, ErrNoAvailableAccounts
}

// openAIUserAffinitySoftOccupancyCandidateTiers 先复用本人软主账号，再选无人账号，最后才软共享最少活动用户的账号。
func openAIUserAffinitySoftOccupancyCandidateTiers(candidates []OpenAIUserAffinityCandidate, occupancies map[int64]OpenAIAccountSoftOccupancy, userID int64, allowShared bool) [][]OpenAIUserAffinityCandidate {
	tiers := make([][]OpenAIUserAffinityCandidate, 2)
	minimumSharedUsers := -1
	var shared []OpenAIUserAffinityCandidate
	for _, candidate := range candidates {
		occupancy := occupancies[candidate.AccountID]
		switch {
		case occupancy.OwnerUserID == userID && userID > 0:
			tiers[0] = append(tiers[0], candidate)
		case occupancy.OwnerUserID == 0:
			tiers[1] = append(tiers[1], candidate)
		case allowShared:
			if minimumSharedUsers < 0 || occupancy.ActiveUserCount < minimumSharedUsers {
				minimumSharedUsers = occupancy.ActiveUserCount
				shared = shared[:0]
			}
			if occupancy.ActiveUserCount == minimumSharedUsers {
				shared = append(shared, candidate)
			}
		}
	}
	if allowShared {
		tiers = append(tiers, shared)
	}
	return tiers
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
