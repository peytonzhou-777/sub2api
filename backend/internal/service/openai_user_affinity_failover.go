package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// selectOpenAIUserAffinityConversationFailover 在事故授权后只把当前可重建会话重放到其他 active 槽位。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityConversationFailover(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	config OpenAIUserAffinityConfig,
	identity openAIUserConversationIdentity,
	binding *OpenAIUserConversationBinding,
	sourceAdmission openAIUserAffinityResidentAdmission,
) (*AccountSelectionResult, bool, error) {
	if binding == nil || binding.AccountID <= 0 || binding.ResidentSlotID <= 0 || binding.SlotGeneration <= 0 {
		return nil, true, ErrNoAvailableAccounts
	}
	runtimeStore, runtimeOK := s.accountRepo.(OpenAIUserAffinityRuntimeStore)
	failoverStore, failoverOK := s.accountRepo.(OpenAIUserAffinityConversationFailoverStore)
	slotStore, slotOK := s.accountRepo.(OpenAIUserAffinityMultiSlotStore)
	if !runtimeOK || !failoverOK || !slotOK {
		return nil, true, ErrNoAvailableAccounts
	}
	now := time.Now().UTC()
	incident := OpenAIUserAffinityIncidentIdentity{
		UserID: identity.userID, AccountID: binding.AccountID, ScopeKey: binding.ScopeKey,
		PlacementGeneration: binding.SlotGeneration, ConversationHash: binding.ConversationHash,
		ResidentSlotID: binding.ResidentSlotID, SlotGeneration: binding.SlotGeneration,
	}
	eligible := isOpenAIUserAffinityResidentHardUnavailable(sourceAdmission)
	if !eligible {
		authorizedAt, err := runtimeStore.RecordOpenAIUserAffinityCapacityFailure(
			ctx, incident, openAIUserAffinityRequestIDHash(ctx), "conversation_account_unavailable", config,
		)
		if err != nil {
			return nil, true, err
		}
		eligible = openAIUserAffinityMigrationStable(config, authorizedAt, now)
	}
	if !eligible {
		return nil, true, ErrNoAvailableAccounts
	}
	slots, err := slotStore.ListOpenAIUserResidentSlots(ctx, identity.userID, identity.scopeKey)
	if err != nil {
		return nil, true, err
	}
	activeSlots := make([]OpenAIUserResidentSlot, 0, len(slots))
	for _, slot := range slots {
		if slot.Status == OpenAIUserResidentSlotStatusActive && now.Before(slot.ExpiresAt) {
			activeSlots = append(activeSlots, slot)
		}
	}
	sortOpenAIUserResidentSlots(activeSlots, config.ResidentTTL(), now)
	checkedSlots := make([]OpenAIUserResidentSlotVersion, 0, len(activeSlots))
	for _, slot := range activeSlots {
		checkedSlots = append(checkedSlots, OpenAIUserResidentSlotVersion{ID: slot.ID, AccountID: slot.AccountID, Generation: slot.Generation})
	}
	orderedSlots := openAIUserAffinitySlotsAfterSource(activeSlots, binding.ResidentSlotID)
	allSlotsFailed := true
	for i := range orderedSlots {
		slot := &orderedSlots[i]
		if slot.AccountID == binding.AccountID {
			continue
		}
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[slot.AccountID]; excluded {
				targetAuthorizedAt, targetAuthErr := runtimeStore.GetOpenAIUserAffinityMigrationAuthorizedAt(ctx,
					openAIUserAffinityConversationIncident(identity, binding.ConversationHash, *slot))
				if targetAuthErr != nil {
					return nil, true, targetAuthErr
				}
				if !openAIUserAffinityMigrationStable(config, targetAuthorizedAt, now) {
					allSlotsFailed = false
				}
				continue
			}
		}
		targetIncident := openAIUserAffinityConversationIncident(identity, binding.ConversationHash, *slot)
		targetAuthorizedAt, targetAuthErr := runtimeStore.GetOpenAIUserAffinityMigrationAuthorizedAt(ctx, targetIncident)
		if targetAuthErr != nil {
			return nil, true, targetAuthErr
		}
		if openAIUserAffinityMigrationStable(config, targetAuthorizedAt, now) {
			continue
		}
		account, accountErr := s.getOpenAIUserAffinityResidentAccount(ctx, slot.AccountID)
		if accountErr != nil {
			return nil, true, accountErr
		}
		targetAdmission := s.classifyOpenAIUserAffinityResidentAdmission(ctx, account, req.GroupID, req.RequestedModel,
			req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
		if targetAdmission == openAIUserAffinityResidentTemporaryCapacity {
			authorizedAt, recordErr := runtimeStore.RecordOpenAIUserAffinityCapacityFailure(
				ctx, targetIncident, openAIUserAffinityRequestIDHash(ctx), "conversation_target_unavailable", config,
			)
			if recordErr != nil {
				return nil, true, recordErr
			}
			if !openAIUserAffinityMigrationStable(config, authorizedAt, now) {
				allSlotsFailed = false
			}
			continue
		}
		if targetAdmission != openAIUserAffinityResidentAllowed {
			continue
		}
		allSlotsFailed = false
		acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		var release func()
		if acquireErr == nil && acquired != nil && acquired.Acquired {
			release = acquired.ReleaseFunc
		}
		transition, reserved, reserveErr := failoverStore.ReserveOpenAIUserConversationFailover(ctx, OpenAIUserConversationFailoverReservation{
			BindingID: binding.ID, UserID: identity.userID, ScopeKey: identity.scopeKey,
			ConversationHash: binding.ConversationHash, SourceAccountID: binding.AccountID,
			SourceResidentSlotID: binding.ResidentSlotID, SourceSlotGeneration: binding.SlotGeneration,
			TargetAccountID: slot.AccountID, TargetResidentSlotID: slot.ID, TargetSlotGeneration: slot.Generation,
			ProvisionalToken: uuid.NewString(), DetachSource: isOpenAIUserAffinityResidentHardUnavailable(sourceAdmission), Config: config,
		})
		if reserveErr != nil || !reserved || transition == nil {
			if release != nil {
				release()
			}
			if reserveErr != nil {
				return nil, true, reserveErr
			}
			continue
		}
		s.rememberOpenAIUserAffinityConversationTransition(ctx, transition)
		s.openaiAffinity.metrics.conversationFailoverAttempts.Add(1)
		if release != nil {
			selection, selectionErr := s.newAcquiredSelectionResult(ctx, account, release)
			return selection, true, selectionErr
		}
		selection, selectionErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
			AccountID: account.ID, MaxConcurrency: account.Concurrency,
			Timeout: s.schedulingConfig().StickySessionWaitTimeout, MaxWaiting: s.schedulingConfig().StickySessionMaxWaiting,
		})
		return selection, true, selectionErr
	}
	if allSlotsFailed && len(activeSlots) >= config.RuntimeResidentAccountSlotCount() && len(activeSlots) > 0 {
		return s.selectOpenAIUserAffinitySlotReplacement(ctx, req, config, identity, binding, sourceAdmission, slots, activeSlots, checkedSlots)
	}
	return nil, true, ErrNoAvailableAccounts
}

func openAIUserAffinityConversationIncident(identity openAIUserConversationIdentity, conversationHash string, slot OpenAIUserResidentSlot) OpenAIUserAffinityIncidentIdentity {
	return OpenAIUserAffinityIncidentIdentity{
		UserID: identity.userID, AccountID: slot.AccountID, ScopeKey: identity.scopeKey,
		PlacementGeneration: slot.Generation, ConversationHash: conversationHash,
		ResidentSlotID: slot.ID, SlotGeneration: slot.Generation,
	}
}

func (s *OpenAIGatewayService) selectOpenAIUserAffinitySlotReplacement(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	config OpenAIUserAffinityConfig,
	identity openAIUserConversationIdentity,
	binding *OpenAIUserConversationBinding,
	sourceAdmission openAIUserAffinityResidentAdmission,
	allSlots, activeSlots []OpenAIUserResidentSlot,
	checkedSlots []OpenAIUserResidentSlotVersion,
) (*AccountSelectionResult, bool, error) {
	store, ok := s.accountRepo.(OpenAIUserAffinityConversationFailoverStore)
	if !ok || len(activeSlots) == 0 {
		return nil, true, ErrNoAvailableAccounts
	}
	victim := activeSlots[len(activeSlots)-1]
	if isOpenAIUserAffinityResidentHardUnavailable(sourceAdmission) {
		for _, slot := range activeSlots {
			if slot.ID == binding.ResidentSlotID {
				// 硬不可用源槽位优先被替换，避免把健康槽位错误地作为牺牲槽位。
				victim = slot
				break
			}
		}
	}
	excluded := cloneExcludedAccountIDs(req.ExcludedIDs)
	if excluded == nil {
		excluded = make(map[int64]struct{}, len(allSlots))
	}
	for _, slot := range allSlots {
		if slot.Status == OpenAIUserResidentSlotStatusProvisional || slot.Status == OpenAIUserResidentSlotStatusActive ||
			slot.Status == OpenAIUserResidentSlotStatusReplacementPending || slot.Status == OpenAIUserResidentSlotStatusDraining {
			excluded[slot.AccountID] = struct{}{}
		}
	}
	accounts, candidates, err := s.openAIUserAffinityCandidates(ctx, identity.userID, req.GroupID, req.RequestedModel,
		excluded, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	if err != nil {
		return nil, true, err
	}
	demand := s.predictOpenAIUserAffinityDemand(ctx, identity.userID, config)
	now := time.Now().UTC()
	for len(candidates) > 0 {
		candidate, found := SelectOpenAIUserAffinityCandidate(config, candidates, demand.Demand5H, demand.Demand7D, now)
		if !found {
			break
		}
		account := accounts[candidate.AccountID]
		acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		var release func()
		if acquireErr == nil && acquired != nil && acquired.Acquired {
			release = acquired.ReleaseFunc
		}
		transition, reserved, reserveErr := store.ReserveOpenAIUserResidentSlotReplacement(ctx, OpenAIUserResidentSlotReplacementReservation{
			BindingID: binding.ID, UserID: identity.userID, ScopeKey: identity.scopeKey,
			ConversationHash: binding.ConversationHash, SourceAccountID: binding.AccountID,
			SourceResidentSlotID: binding.ResidentSlotID, SourceSlotGeneration: binding.SlotGeneration,
			VictimSlotID: victim.ID, TargetAccountID: account.ID, CheckedSlots: checkedSlots,
			ProvisionalToken: uuid.NewString(), Config: config,
		})
		if reserveErr != nil || !reserved || transition == nil {
			if release != nil {
				release()
			}
			if reserveErr != nil {
				return nil, true, reserveErr
			}
			candidates = removeOpenAIUserAffinityCandidate(candidates, candidate.AccountID)
			continue
		}
		s.rememberOpenAIUserAffinityConversationTransition(ctx, transition)
		s.openaiAffinity.metrics.residentSlotReplacementAttempts.Add(1)
		if release != nil {
			selection, selectionErr := s.newAcquiredSelectionResult(ctx, account, release)
			return selection, true, selectionErr
		}
		selection, selectionErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
			AccountID: account.ID, MaxConcurrency: account.Concurrency,
			Timeout: s.schedulingConfig().StickySessionWaitTimeout, MaxWaiting: s.schedulingConfig().StickySessionMaxWaiting,
		})
		return selection, true, selectionErr
	}
	return nil, true, ErrNoAvailableAccounts
}

func openAIUserAffinitySlotsAfterSource(slots []OpenAIUserResidentSlot, sourceSlotID int64) []OpenAIUserResidentSlot {
	if len(slots) < 2 || sourceSlotID <= 0 {
		return slots
	}
	for i := range slots {
		if slots[i].ID != sourceSlotID {
			continue
		}
		ordered := make([]OpenAIUserResidentSlot, 0, len(slots)-1)
		ordered = append(ordered, slots[i+1:]...)
		ordered = append(ordered, slots[:i]...)
		return ordered
	}
	return slots
}
