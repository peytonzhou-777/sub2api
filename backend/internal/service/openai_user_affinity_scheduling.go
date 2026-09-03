package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
)

// selectLegacyOpenAIAccountWithUserAffinity 为 legacy 公共入口统一处理居民恢复和新居民归属。
func (s *OpenAIGatewayService) selectLegacyOpenAIAccountWithUserAffinity(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*AccountSelectionResult, error) {
	req := newOpenAIUserAffinityScheduleRequest(groupID, PlatformOpenAI, "", requestedModel,
		OpenAIUpstreamTransportHTTPSSE, "", "", false, excludedIDs)
	req.SessionHash = sessionHash
	if selection, found, err := s.selectOpenAIUserAffinityConversation(ctx, req); err != nil || found {
		return selection, err
	}
	if selection, found, err := s.selectOpenAIUserAffinityResidentSlots(ctx, req); err != nil || found {
		return selection, err
	}
	if selection, found, err := s.selectOpenAIUserAffinityPlacementForRequest(ctx, req); err != nil || found {
		if err == nil && selection != nil && selection.Account != nil {
			if reserveErr := s.reserveOpenAIUserAffinityConversation(ctx, req, selection.Account.ID); reserveErr != nil {
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				if errors.Is(reserveErr, ErrOpenAIUserAffinityReservationRetry) {
					reselected, _, retryErr := s.reselectOpenAIUserAffinityAfterReservationConflict(ctx, req)
					return reselected, retryErr
				}
				return nil, reserveErr
			}
		}
		return selection, err
	}
	result, err := s.selectAccountWithLoadAwareness(ctx, groupID, PlatformOpenAI, sessionHash, requestedModel, excludedIDs, false, "", true)
	if err == nil && result != nil && result.Account != nil {
		scopeKey := openAIUserAffinityScopeKey(groupID, false, "", "", OpenAIUpstreamTransportHTTPSSE)
		_ = s.reserveOpenAIUserAffinityPlacementForRequest(ctx, req, result.Account.ID, scopeKey)
		if reserveErr := s.reserveOpenAIUserAffinityConversation(ctx, req, result.Account.ID); reserveErr != nil {
			if result.ReleaseFunc != nil {
				result.ReleaseFunc()
			}
			if errors.Is(reserveErr, ErrOpenAIUserAffinityReservationRetry) {
				reselected, _, retryErr := s.reselectOpenAIUserAffinityAfterReservationConflict(ctx, req)
				return reselected, retryErr
			}
			return nil, reserveErr
		}
	}
	return result, err
}

// isOpenAIUserAffinityResidentAccountEligible 保留居民的协议、能力和运行时门控，
// 但不套用面向新流量的额度自动暂停阈值。
func isOpenAIUserAffinityResidentAccountEligible(ctx context.Context, account *Account, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability) bool {
	if account == nil || account.Platform != PlatformOpenAI || !account.IsOpenAICompatible() || !account.IsSchedulableForModelWithContext(ctx, requestedModel) {
		return false
	}
	if requestedModel != "" && !account.IsModelSupported(requestedModel) {
		return false
	}
	if !account.SupportsOpenAIEndpointCapability(requiredCapability) {
		return false
	}
	if requireCompact && openAICompactSupportTier(account) == 0 {
		return false
	}
	if vetoed, _ := openAIProfitControlVetoReason(ctx, account); vetoed {
		return false
	}
	return true
}

type openAIUserAffinityResidentAdmission string

const (
	openAIUserAffinityResidentAllowed              openAIUserAffinityResidentAdmission = "allowed"
	openAIUserAffinityResidentTemporaryCapacity    openAIUserAffinityResidentAdmission = "temporary_capacity"
	openAIUserAffinityResidentQuota7DExhausted     openAIUserAffinityResidentAdmission = "quota_7d_exhausted"
	openAIUserAffinityResidentPermanentUnavailable openAIUserAffinityResidentAdmission = "permanent_unavailable"
)

func isOpenAIUserAffinityResidentHardUnavailable(admission openAIUserAffinityResidentAdmission) bool {
	return admission == openAIUserAffinityResidentPermanentUnavailable || admission == openAIUserAffinityResidentQuota7DExhausted
}

// classifyOpenAIUserAffinityResidentAdmission 区分需累计客户端重试的临时失败与可直接搬迁的永久失败。
func (s *OpenAIGatewayService) classifyOpenAIUserAffinityResidentAdmission(ctx context.Context, account *Account, groupID *int64, requestedModel string, requireCompact bool, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requiredTransport OpenAIUpstreamTransport) openAIUserAffinityResidentAdmission {
	if account == nil || account.Platform != PlatformOpenAI || !account.IsOpenAICompatible() || account.Status != StatusActive || !account.Schedulable {
		return openAIUserAffinityResidentPermanentUnavailable
	}
	if (requestedModel != "" && !account.IsModelSupported(requestedModel)) || !account.SupportsOpenAIEndpointCapability(requiredCapability) ||
		!account.SupportsOpenAIImageCapability(requiredImageCapability) ||
		!s.isOpenAIAccountTransportCompatible(account, requiredTransport) || !s.openAIAccountMatchesSchedulingGroup(account, groupID) ||
		(requireCompact && openAICompactSupportTier(account) == 0) {
		return openAIUserAffinityResidentPermanentUnavailable
	}
	if !parentHealthyForShadow(account, s.parentAccountLookup(ctx)) ||
		s.isOpenAIAccountRequestRuntimeBlocked(account, requestedModel) ||
		s.isOpenAIProxyStreamQuarantined(ctx, account) ||
		!s.openAIUserAffinityPrivacyAllowed(ctx, groupID, account) ||
		s.openAIUserAffinityChannelRestricted(ctx, groupID, account, requestedModel, requireCompact) {
		return openAIUserAffinityResidentTemporaryCapacity
	}
	now := time.Now()
	if available, known := readOpenAIUserAffinityQuotaAvailableRatio(account.Extra, "7d", now); known && available <= 0 {
		return openAIUserAffinityResidentQuota7DExhausted
	}
	if available, known := readOpenAIUserAffinityQuotaAvailableRatio(account.Extra, "5h", now); known && available <= 0 {
		return openAIUserAffinityResidentTemporaryCapacity
	}
	if !isOpenAIUserAffinityResidentAccountEligible(ctx, account, requestedModel, requireCompact, requiredCapability) {
		return openAIUserAffinityResidentTemporaryCapacity
	}
	return openAIUserAffinityResidentAllowed
}

// readOpenAIUserAffinityQuotaAvailableRatio 只接受尚未 reset 且带更新时间的额度快照。
// 新鲜快照中缺失或非正窗口代表已知无限制；格式损坏才是未知容量。
func readOpenAIUserAffinityQuotaAvailableRatio(extra map[string]any, window string, now time.Time) (float64, bool) {
	if len(extra) == 0 || openAICodexSnapshotStaleForPause(extra, now) {
		return 0, false
	}
	if _, ok := extra["codex_usage_updated_at"]; !ok {
		return 0, false
	}
	switch resolveOpenAIQuotaWindowLimitState(extra, window) {
	case openAIQuotaWindowUnlimited:
		return 1, true
	case openAIQuotaWindowLimitUnknown:
		return 0, false
	}
	if openAIQuotaWindowReset(extra, window, now) {
		return 0, false
	}
	usedPercent, ok := resolveOpenAILimitedQuotaUsedPercent(extra, window)
	if !ok {
		return 0, false
	}
	return 1 - usedPercent/100, true
}

// selectOpenAIUserAffinityPlacement 在普通负载调度前恢复用户居住账号。
// 账号明确硬不可用时立即脱离常驻槽位，临时容量问题仍交给重试窗口处理。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityPlacement(ctx context.Context, groupID *int64, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requiredTransport OpenAIUpstreamTransport) (*AccountSelectionResult, bool, error) {
	req := newOpenAIUserAffinityScheduleRequest(groupID, PlatformOpenAI, "", requestedModel,
		requiredTransport, requiredCapability, requiredImageCapability, requireCompact, excludedIDs)
	return s.selectOpenAIUserAffinityPlacementForRequest(ctx, req)
}

// selectOpenAIUserAffinityPlacementForRequest 在完整请求上下文下恢复用户居住账号。
// 完整请求用于校验多槽位 projection 与 live slot 的 generation 一致性。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityPlacementForRequest(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, bool, error) {
	if s == nil || s.settingService == nil || s.accountRepo == nil {
		return nil, false, nil
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeEnforce {
		return nil, false, err
	}
	s.maybeReconcileOpenAIUserAffinity()
	userID, ok := ctx.Value(ctxkey.UserID).(int64)
	if !ok || userID <= 0 {
		slog.Error("openai_user_affinity.missing_user_identity")
		return nil, true, ErrNoAvailableAccounts
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityStore)
	if !ok {
		return nil, false, nil
	}
	now := time.Now().UTC()
	scopeKey := openAIUserAffinityScopeKey(req.GroupID, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	placement, err := store.GetOpenAIUserPlacement(ctx, userID, scopeKey)
	if err != nil {
		return nil, false, err
	}
	if placement != nil && placement.Status == "active" && placement.AccountID != nil && now.Before(placement.ExpiresAt) {
		// 多槽位模式下 placement 只是兼容投影，必须和当前 live slot 的账号及 generation 同时成立。
		// Converge 会先过期 slot；若继续信任旧 projection，容量失败会卡在旧账号并持续返回 503。
		if live, liveErr := s.openAIUserAffinityPlacementHasLiveSlot(ctx, req, placement, config, now); liveErr != nil {
			return nil, true, liveErr
		} else if !live {
			slog.Warn("openai_user_affinity.stale_placement", "user_id", userID, "scope_key", scopeKey,
				"account_id", *placement.AccountID, "placement_generation", placement.Generation,
				"reason", "no_live_resident_slot")
			ctx = context.WithValue(ctx, openAIUserAffinityStalePlacementContextKey{}, true)
			placement = nil
		}
	}

	if placement != nil && placement.Status == "active" && placement.AccountID != nil && now.Before(placement.ExpiresAt) {
		incident := OpenAIUserAffinityIncidentIdentity{
			UserID: userID, AccountID: *placement.AccountID, ScopeKey: placement.ScopeKey,
			PlacementGeneration: placement.Generation,
		}
		runtimeStore, runtimeOK := s.accountRepo.(OpenAIUserAffinityRuntimeStore)
		if runtimeOK {
			authorizedAt, authErr := runtimeStore.GetOpenAIUserAffinityMigrationAuthorizedAt(ctx, incident)
			if authErr != nil {
				return nil, true, authErr
			}
			if openAIUserAffinityMigrationStable(config, authorizedAt, now) {
				return s.selectOpenAIUserAffinityMigrationTarget(ctx, req.GroupID, req.RequestedModel, req.ExcludedIDs, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport, "capacity_retry_threshold", config, now, placement, runtimeStore)
			}
		}
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[*placement.AccountID]; excluded {
				if s.failOpenAIUserAffinityReentryLeader(ctx) {
					return s.selectOpenAIUserAffinityPlacementForRequest(ctx, req)
				}
				if runtimeOK {
					authorizedAt, failureErr := recordOpenAIUserAffinityCapacityFailure(ctx, runtimeStore, incident, "resident_account_excluded", config)
					if errors.Is(failureErr, ErrOpenAIUserAffinityPlacementStale) {
						// placement 在记录容量失败的并发窗口内消失，按 miss 继续新居民选号。
						ctx = context.WithValue(ctx, openAIUserAffinityStalePlacementContextKey{}, true)
						placement = nil
						goto placementMiss
					}
					if failureErr != nil {
						return nil, true, failureErr
					}
					if openAIUserAffinityMigrationStable(config, authorizedAt, now) {
						return s.selectOpenAIUserAffinityMigrationTarget(ctx, req.GroupID, req.RequestedModel, req.ExcludedIDs, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport, "capacity_retry_threshold", config, now, placement, runtimeStore)
					}
				}
				return nil, true, ErrNoAvailableAccounts
			}
		}
		account, accountErr := s.getOpenAIUserAffinityResidentAccount(ctx, *placement.AccountID)
		if accountErr != nil {
			return nil, true, accountErr
		}
		admission := s.classifyOpenAIUserAffinityResidentAdmission(ctx, account, req.GroupID, req.RequestedModel, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
		if isOpenAIUserAffinityResidentHardUnavailable(admission) {
			// 单槽兼容投影也不能继续锁住硬错误账号；先标记旧 projection 陈旧，再走新居民选号。
			if req.ExcludedIDs == nil {
				req.ExcludedIDs = make(map[int64]struct{})
			} else {
				req.ExcludedIDs = cloneExcludedAccountIDs(req.ExcludedIDs)
			}
			req.ExcludedIDs[*placement.AccountID] = struct{}{}
			if maintenance, maintenanceOK := s.accountRepo.(OpenAIUserAffinityResidentSlotMaintenanceStore); maintenanceOK {
				if slotStore, slotStoreOK := s.accountRepo.(OpenAIUserAffinityMultiSlotStore); slotStoreOK {
					slots, slotErr := slotStore.ListOpenAIUserResidentSlots(ctx, placement.UserID, placement.ScopeKey)
					if slotErr != nil {
						return nil, true, slotErr
					}
					for _, slot := range slots {
						if slot.AccountID == *placement.AccountID && slot.Generation == placement.Generation &&
							slot.Status != OpenAIUserResidentSlotStatusDraining && now.Before(slot.ExpiresAt) {
							if _, evictErr := maintenance.EvictOpenAIUserResidentSlot(ctx, placement.UserID, placement.ScopeKey,
								slot.ID, slot.AccountID, slot.Generation, "account_unavailable", now); evictErr != nil {
								return nil, true, evictErr
							}
							break
						}
					}
				}
			}
			ctx = context.WithValue(ctx, openAIUserAffinityStalePlacementContextKey{}, true)
			placement = nil
			goto placementMiss
		}
		if admission != openAIUserAffinityResidentAllowed {
			// 5h、RPM 和临时运行时门控只累计不同客户端请求，未达阈值不改变归属。
			if runtimeOK {
				authorizedAt, failureErr := recordOpenAIUserAffinityCapacityFailure(ctx, runtimeStore, incident, "resident_account_unavailable", config)
				if errors.Is(failureErr, ErrOpenAIUserAffinityPlacementStale) {
					// 同上：不要把可恢复的 placement 竞态暴露为客户端 503。
					ctx = context.WithValue(ctx, openAIUserAffinityStalePlacementContextKey{}, true)
					placement = nil
					goto placementMiss
				}
				if failureErr != nil {
					return nil, true, failureErr
				}
				if openAIUserAffinityMigrationStable(config, authorizedAt, now) {
					return s.selectOpenAIUserAffinityMigrationTarget(ctx, req.GroupID, req.RequestedModel, req.ExcludedIDs, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport, "capacity_retry_threshold", config, now, placement, runtimeStore)
				}
			}
			return nil, true, ErrNoAvailableAccounts
		}
		s.rememberOpenAIUserAffinityAttempt(ctx, placement)
		if err := s.coordinateOpenAIUserAffinityReentry(ctx, placement, config); err != nil {
			return nil, true, err
		}
		acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if acquireErr == nil && acquired != nil && acquired.Acquired {
			result, resultErr := s.newAcquiredSelectionResult(ctx, account, acquired.ReleaseFunc)
			return result, true, resultErr
		}
		result, resultErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
			AccountID: account.ID, MaxConcurrency: account.Concurrency,
			Timeout:    s.schedulingConfig().StickySessionWaitTimeout,
			MaxWaiting: s.schedulingConfig().StickySessionMaxWaiting,
		})
		return result, true, resultErr
	}

placementMiss:
	newResidentExcluded, preferExistingAffinity := resolveOpenAIUserAffinityNewResidentPolicy(placement, req.ExcludedIDs)
	newResidentExcluded, resetPending, err := s.applyOpenAIUserAffinityResetExclusions(ctx, userID, scopeKey, newResidentExcluded)
	if err != nil {
		return nil, true, err
	}
	if resetPending {
		preferExistingAffinity = false
	}
	return s.selectOpenAIUserAffinityNewResident(ctx, req.GroupID, req.RequestedModel, newResidentExcluded, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport, scopeKey, config, now, preferExistingAffinity)
}

// openAIUserAffinityPlacementHasLiveSlot 校验 placement 是否仍由 live resident slot 支撑。
// 单槽兼容模式或旧仓储替身没有 slot 读能力时保留原有 placement 语义。
func (s *OpenAIGatewayService) openAIUserAffinityPlacementHasLiveSlot(ctx context.Context, req OpenAIAccountScheduleRequest, placement *OpenAIUserPlacement, config OpenAIUserAffinityConfig, now time.Time) (bool, error) {
	if placement == nil || placement.AccountID == nil || config.RuntimeResidentAccountSlotCount() <= 1 {
		return true, nil
	}
	// 只要请求已经完成 API Key 认证，就应校验多槽位 projection；即使本次没有
	// 稳定 SessionHash，也不能让陈旧 placement 把请求锁死在已过期账号上。
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	if apiKeyID <= 0 {
		return true, nil
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityMultiSlotStore)
	if !ok {
		return true, nil
	}
	slots, err := store.ListOpenAIUserResidentSlots(ctx, placement.UserID, placement.ScopeKey)
	if err != nil {
		return false, err
	}
	for _, slot := range slots {
		if slot.AccountID != *placement.AccountID || slot.Generation != placement.Generation ||
			(slot.Status != OpenAIUserResidentSlotStatusActive && slot.Status != OpenAIUserResidentSlotStatusProvisional &&
				slot.Status != OpenAIUserResidentSlotStatusReplacementPending) || !now.Before(slot.ExpiresAt) {
			continue
		}
		return true, nil
	}
	return false, nil
}

// applyOpenAIUserAffinityResetExclusions 合并整组重置的全部原账号，并强制绕过触达优先直达 BestFit。
func (s *OpenAIGatewayService) applyOpenAIUserAffinityResetExclusions(ctx context.Context, userID int64, scopeKey string, excludedIDs map[int64]struct{}) (map[int64]struct{}, bool, error) {
	store, ok := s.accountRepo.(OpenAIUserAffinityResetExclusionStore)
	if !ok {
		return excludedIDs, false, nil
	}
	accountIDs, err := store.ListOpenAIUserAffinityResetExcludedAccountIDs(ctx, userID, scopeKey)
	if err != nil {
		return nil, false, err
	}
	if len(accountIDs) == 0 {
		return excludedIDs, false, nil
	}
	merged := cloneExcludedAccountIDs(excludedIDs)
	if merged == nil {
		merged = make(map[int64]struct{}, len(accountIDs))
	}
	for _, accountID := range accountIDs {
		merged[accountID] = struct{}{}
	}
	return merged, true, nil
}

// resolveOpenAIUserAffinityNewResidentPolicy 将手动重置排除语义统一为严格的新居民重选：
// 原账号不可再次入选，七日触达和跨 scope 居住记录也不再提供前置优先。
func resolveOpenAIUserAffinityNewResidentPolicy(placement *OpenAIUserPlacement, excludedIDs map[int64]struct{}) (map[int64]struct{}, bool) {
	if placement == nil || placement.Status != "reset" || placement.ResetExcludeSourceAccount == nil || !*placement.ResetExcludeSourceAccount {
		return excludedIDs, true
	}
	if placement.ResetSourceAccountID == nil || *placement.ResetSourceAccountID <= 0 {
		return excludedIDs, false
	}
	resetExcluded := cloneExcludedAccountIDs(excludedIDs)
	if resetExcluded == nil {
		resetExcluded = make(map[int64]struct{})
	}
	resetExcluded[*placement.ResetSourceAccountID] = struct{}{}
	return resetExcluded, false
}

func openAIUserAffinityMigrationStable(config OpenAIUserAffinityConfig, authorizedAt *time.Time, now time.Time) bool {
	return authorizedAt != nil && !now.Before(authorizedAt.Add(time.Duration(config.MigrationStabilitySeconds)*time.Second))
}

// selectOpenAIUserAffinityMigrationTarget 在授权后选择目标，并以 generation CAS 原子搬迁。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityMigrationTarget(ctx context.Context, groupID *int64, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requiredTransport OpenAIUpstreamTransport, migrationReason string, config OpenAIUserAffinityConfig, now time.Time, placement *OpenAIUserPlacement, runtimeStore OpenAIUserAffinityRuntimeStore) (*AccountSelectionResult, bool, error) {
	if placement == nil || placement.AccountID == nil {
		return nil, true, ErrNoAvailableAccounts
	}
	migrationExcluded := cloneExcludedAccountIDs(excludedIDs)
	if migrationExcluded == nil {
		migrationExcluded = make(map[int64]struct{})
	}
	migrationExcluded[*placement.AccountID] = struct{}{}
	accounts, candidates, err := s.openAIUserAffinityCandidates(ctx, placement.UserID, groupID, requestedModel, migrationExcluded, requireCompact, requiredCapability, requiredImageCapability, requiredTransport)
	if err != nil {
		return nil, true, err
	}
	demand := s.predictOpenAIUserAffinityDemand(ctx, placement.UserID, config)
	for len(candidates) > 0 {
		candidate, found := SelectOpenAIUserAffinityCandidate(config, candidates, demand.Demand5H, demand.Demand7D, now)
		if !found {
			break
		}
		account := accounts[candidate.AccountID]
		acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if acquireErr == nil && acquired != nil && acquired.Acquired {
			provisionalToken := uuid.NewString()
			migrated, migrateErr := runtimeStore.MigrateOpenAIUserAffinityPlacement(ctx, placement.UserID, *placement.AccountID, account.ID, placement.Generation, placement.ScopeKey, provisionalToken, migrationReason, config)
			if migrateErr != nil {
				if acquired.ReleaseFunc != nil {
					acquired.ReleaseFunc()
				}
				return nil, true, migrateErr
			}
			if migrated {
				moved := *placement
				moved.AccountID = &account.ID
				moved.Generation++
				moved.ProvisionalToken = provisionalToken
				previous := *placement
				s.rememberOpenAIUserAffinityAttempt(ctx, &moved, &OpenAIUserAffinityProvisionalTransition{
					Kind: "migration", Token: provisionalToken, TargetPlacement: moved, PreviousPlacement: &previous, Config: config,
				})
				result, resultErr := s.newAcquiredSelectionResult(ctx, account, acquired.ReleaseFunc)
				return result, true, resultErr
			}
			if acquired.ReleaseFunc != nil {
				acquired.ReleaseFunc()
			}
		}
		candidates = removeOpenAIUserAffinityCandidate(candidates, candidate.AccountID)
	}
	return nil, true, ErrNoAvailableAccounts
}

// selectOpenAIUserAffinityNewResident 以 7d/5h 容量 BestFit 为新居民选号，隔离由 Persona lease 保证。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityNewResident(ctx context.Context, groupID *int64, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requiredTransport OpenAIUpstreamTransport, scopeKey string, config OpenAIUserAffinityConfig, now time.Time, preferExistingAffinity bool) (*AccountSelectionResult, bool, error) {
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	demand := s.predictOpenAIUserAffinityDemand(ctx, userID, config)
	accountByID, candidates, err := s.openAIUserAffinityCandidates(ctx, userID, groupID, requestedModel, excludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport)
	if err != nil {
		return nil, true, err
	}
	unknownQuota := false
	for _, candidate := range candidates {
		if !candidate.Quota5HKnown || !candidate.Quota7DKnown {
			unknownQuota = true
		}
	}
	for len(candidates) > 0 {
		candidate, found := selectOpenAIUserAffinityCandidate(config, candidates, demand.Demand5H, demand.Demand7D, now, preferExistingAffinity)
		if !found {
			break
		}
		account := accountByID[candidate.AccountID]
		acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if acquireErr == nil && acquired != nil && acquired.Acquired {
			reservationReq := newOpenAIUserAffinityScheduleRequest(groupID, PlatformOpenAI, "", requestedModel,
				requiredTransport, requiredCapability, requiredImageCapability, requireCompact, excludedIDs)
			if !s.reserveOpenAIUserAffinityPlacementForRequest(ctx, reservationReq, account.ID, scopeKey) {
				if acquired.ReleaseFunc != nil {
					acquired.ReleaseFunc()
				}
				candidates = removeOpenAIUserAffinityCandidate(candidates, candidate.AccountID)
				continue
			}
			result, resultErr := s.newAcquiredSelectionResult(ctx, account, acquired.ReleaseFunc)
			return result, true, resultErr
		}
		candidates = removeOpenAIUserAffinityCandidate(candidates, candidate.AccountID)
	}
	if unknownQuota {
		// 已知候选均未通过时，仍允许未知快照回退普通调度并用真实请求自愈。
		return nil, false, nil
	}
	return nil, true, ErrNoAvailableAccounts
}

func (s *OpenAIGatewayService) openAIUserAffinityCandidates(ctx context.Context, userID int64, groupID *int64, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requiredTransport OpenAIUpstreamTransport) (map[int64]*Account, []OpenAIUserAffinityCandidate, error) {
	statsStore, ok := s.accountRepo.(OpenAIUserAffinityCandidateStore)
	if !ok {
		return nil, nil, nil
	}
	listed, err := s.listSchedulableAccounts(ctx, groupID, PlatformOpenAI)
	if err != nil {
		return nil, nil, err
	}
	accountByID := make(map[int64]*Account, len(listed))
	accountIDs := make([]int64, 0, len(listed))
	for i := range listed {
		account := &listed[i]
		if excludedIDs != nil {
			if _, excluded := excludedIDs[account.ID]; excluded {
				continue
			}
		}
		fresh := s.resolveFreshSchedulableOpenAIAccount(ctx, account, PlatformOpenAI, requestedModel, requireCompact, requiredCapability)
		fresh = s.recheckSelectedOpenAIAccountFromDB(ctx, fresh, groupID, PlatformOpenAI, requestedModel, requireCompact, requiredCapability)
		if fresh == nil || !fresh.SupportsOpenAIImageCapability(requiredImageCapability) ||
			!s.isOpenAIAccountTransportCompatible(fresh, requiredTransport) ||
			!s.openAIUserAffinityPrivacyAllowed(ctx, groupID, fresh) ||
			s.openAIUserAffinityChannelRestricted(ctx, groupID, fresh, requestedModel, requireCompact) {
			continue
		}
		accountByID[fresh.ID] = fresh
		accountIDs = append(accountIDs, fresh.ID)
	}
	stats, err := statsStore.GetOpenAIUserAffinityCandidateStats(ctx, userID, accountIDs)
	if err != nil {
		return nil, nil, err
	}
	candidates := make([]OpenAIUserAffinityCandidate, 0, len(accountIDs))
	for _, accountID := range accountIDs {
		account := accountByID[accountID]
		candidate := stats[accountID]
		candidate.AccountID = accountID
		candidate.Available5HRatio, candidate.Quota5HKnown = readOpenAIUserAffinityQuotaAvailableRatio(account.Extra, "5h", time.Now())
		candidate.Available7DRatio, candidate.Quota7DKnown = readOpenAIUserAffinityQuotaAvailableRatio(account.Extra, "7d", time.Now())
		candidate.Quota5HWindowMinutes, candidate.Quota5HResetAt = readOpenAIUserAffinityQuotaWindowHorizon(account.Extra, "5h")
		candidate.Quota7DWindowMinutes, candidate.Quota7DResetAt = readOpenAIUserAffinityQuotaWindowHorizon(account.Extra, "7d")
		candidates = append(candidates, candidate)
	}
	return accountByID, candidates, nil
}

func readOpenAIUserAffinityQuotaWindowHorizon(extra map[string]any, window string) (int, *time.Time) {
	windowMinutes := ParseExtraInt(extra["codex_"+window+"_window_minutes"])
	if windowMinutes <= 0 {
		return 0, nil
	}
	resetRaw, ok := extra["codex_"+window+"_reset_at"]
	if !ok || resetRaw == nil {
		return windowMinutes, nil
	}
	resetAt, err := parseTime(strings.TrimSpace(fmt.Sprint(resetRaw)))
	if err != nil {
		return windowMinutes, nil
	}
	value := resetAt.UTC()
	return windowMinutes, &value
}

func removeOpenAIUserAffinityCandidate(candidates []OpenAIUserAffinityCandidate, accountID int64) []OpenAIUserAffinityCandidate {
	remaining := candidates[:0]
	for _, current := range candidates {
		if current.AccountID != accountID {
			remaining = append(remaining, current)
		}
	}
	return remaining
}

// openAIUserAffinityStalePlacementContextKey 标记本次请求已确认旧 projection 失效。
type openAIUserAffinityStalePlacementContextKey struct{}

// reserveOpenAIUserAffinityPlacementForRequest 在写入新归属前复核旧 projection。
// 请求带有完整认证上下文时，缺失 live slot 的 active projection 视为陈旧并允许自愈覆盖。
func (s *OpenAIGatewayService) reserveOpenAIUserAffinityPlacementForRequest(ctx context.Context, req OpenAIAccountScheduleRequest, accountID int64, scopeKey string) bool {
	if s == nil || s.settingService == nil || s.accountRepo == nil || accountID <= 0 {
		return false
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeEnforce {
		return false
	}
	userID, ok := ctx.Value(ctxkey.UserID).(int64)
	if !ok || userID <= 0 {
		return false
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityStore)
	if !ok {
		return false
	}
	current, err := store.GetOpenAIUserPlacement(ctx, userID, scopeKey)
	now := time.Now().UTC()
	stalePlacement, _ := ctx.Value(openAIUserAffinityStalePlacementContextKey{}).(bool)
	if err != nil {
		return false
	}
	if !stalePlacement && current != nil && current.Status == "active" && current.AccountID != nil && now.Before(current.ExpiresAt) {
		if err != nil || current == nil || current.AccountID == nil {
			return false
		}
		if *current.AccountID == accountID {
			return true
		}
		// 完整请求下由 live slot 复核旧 projection；无 live slot 才允许覆盖。
		if strings.TrimSpace(req.Platform) != "" {
			live, liveErr := s.openAIUserAffinityPlacementHasLiveSlot(ctx, req, current, config, now)
			if liveErr != nil || live {
				return false
			}
			// 陈旧 projection 继续进入下方权威 assignment 流程。
		} else {
			return false
		}
	}
	generation := int64(1)
	assignmentReason := "new_resident"
	if current != nil && current.Generation >= generation {
		generation = current.Generation + 1
		if current.Status == "reset" {
			assignmentReason = "manual_reset_reassignment"
		}
	}
	placement := OpenAIUserPlacement{
		UserID: userID, ScopeKey: scopeKey, AccountID: &accountID, Generation: generation,
		Status: "active", AssignedAt: now, ExpiresAt: now.Add(config.ResidentTTL()),
		AssignmentReason: assignmentReason,
	}
	demand := s.predictOpenAIUserAffinityDemand(ctx, userID, config)
	placement.Predicted5HDemand = &demand.Demand5H
	placement.Predicted7DDemand = &demand.Demand7D
	placement.PredictionVersion = demand.Version
	if runtimeStore, ok := s.accountRepo.(OpenAIUserAffinityRuntimeStore); ok {
		placement.ProvisionalToken = uuid.NewString()
		assigned, assignErr := runtimeStore.AssignOpenAIUserAffinityPlacement(ctx, placement, config)
		if assignErr != nil {
			slog.Warn("openai_user_affinity.placement_reservation_failed", "user_id", userID, "account_id", accountID, "error", assignErr)
			return false
		}
		if assigned {
			latest, latestErr := store.GetOpenAIUserPlacement(ctx, userID, scopeKey)
			if latestErr != nil || latest == nil || latest.AccountID == nil || *latest.AccountID != accountID {
				return false
			}
			if latest.ProvisionalToken == placement.ProvisionalToken {
				s.rememberOpenAIUserAffinityAttempt(ctx, latest, &OpenAIUserAffinityProvisionalTransition{
					Kind: "assignment", Token: placement.ProvisionalToken, TargetPlacement: *latest, PreviousPlacement: current, Config: config,
				})
			} else {
				s.rememberOpenAIUserAffinityAttempt(ctx, latest)
			}
		}
		return assigned
	}
	if err := store.UpsertOpenAIUserPlacement(ctx, placement); err != nil {
		slog.Warn("openai_user_affinity.placement_reservation_failed", "user_id", userID, "account_id", accountID, "error", err)
		return false
	}
	s.rememberOpenAIUserAffinityAttempt(ctx, &placement)
	return true
}

func (s *OpenAIGatewayService) openAIUserAffinityPrivacyAllowed(ctx context.Context, groupID *int64, account *Account) bool {
	if groupID == nil || s.schedulerSnapshot == nil {
		return true
	}
	group, err := s.schedulerSnapshot.GetGroupByID(ctx, *groupID)
	return err == nil && group != nil && SchedulerPrivacyAllowsSelection(*account, group.RequirePrivacySet)
}

func (s *OpenAIGatewayService) openAIUserAffinityChannelRestricted(ctx context.Context, groupID *int64, account *Account, requestedModel string, requireCompact bool) bool {
	return s.needsUpstreamChannelRestrictionCheck(ctx, groupID) &&
		groupID != nil && s.isUpstreamModelRestrictedByChannel(ctx, *groupID, account, requestedModel, requireCompact)
}
