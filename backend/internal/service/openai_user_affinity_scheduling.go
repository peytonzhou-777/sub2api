package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
)

// selectLegacyOpenAIAccountWithUserAffinity 为 legacy 公共入口统一处理居民恢复和新居民归属。
func (s *OpenAIGatewayService) selectLegacyOpenAIAccountWithUserAffinity(ctx context.Context, groupID *int64, sessionHash string, requestedModel string, excludedIDs map[int64]struct{}) (*AccountSelectionResult, error) {
	if selection, found, err := s.selectOpenAIUserAffinityPlacement(
		ctx, groupID, requestedModel, excludedIDs, false, "", "", OpenAIUpstreamTransportHTTPSSE,
	); err != nil || found {
		return selection, err
	}
	result, err := s.selectAccountWithLoadAwareness(ctx, groupID, PlatformOpenAI, sessionHash, requestedModel, excludedIDs, false, "", true)
	if err == nil && result != nil && result.Account != nil {
		scopeKey := openAIUserAffinityScopeKey(groupID, false, "", "", OpenAIUpstreamTransportHTTPSSE)
		_ = s.reserveOpenAIUserAffinityPlacement(ctx, result.Account.ID, scopeKey)
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
// 账号已不可用时暂不清除归属，后续容量失败窗口负责决定是否搬迁。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityPlacement(ctx context.Context, groupID *int64, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requiredTransport OpenAIUpstreamTransport) (*AccountSelectionResult, bool, error) {
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
	scopeKey := openAIUserAffinityScopeKey(groupID, requireCompact, requiredCapability, requiredImageCapability, requiredTransport)
	placement, err := store.GetOpenAIUserPlacement(ctx, userID, scopeKey)
	if err != nil {
		return nil, false, err
	}
	if placement != nil && placement.Status == "active" && placement.AccountID != nil && now.Before(placement.ExpiresAt) {
		runtimeStore, runtimeOK := s.accountRepo.(OpenAIUserAffinityRuntimeStore)
		if runtimeOK {
			authorizedAt, authErr := runtimeStore.GetOpenAIUserAffinityMigrationAuthorizedAt(ctx, userID, *placement.AccountID, placement.Generation, placement.ScopeKey)
			if authErr != nil {
				return nil, true, authErr
			}
			if openAIUserAffinityMigrationStable(config, authorizedAt, now) {
				return s.selectOpenAIUserAffinityMigrationTarget(ctx, groupID, requestedModel, excludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport, "capacity_retry_threshold", config, now, placement, runtimeStore)
			}
		}
		if excludedIDs != nil {
			if _, excluded := excludedIDs[*placement.AccountID]; excluded {
				if s.failOpenAIUserAffinityReentryLeader(ctx) {
					return s.selectOpenAIUserAffinityPlacement(ctx, groupID, requestedModel, excludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport)
				}
				if runtimeOK {
					authorizedAt, failureErr := recordOpenAIUserAffinityCapacityFailure(ctx, runtimeStore, userID, *placement.AccountID, placement.Generation, placement.ScopeKey, "resident_account_excluded", config)
					if failureErr != nil {
						return nil, true, failureErr
					}
					if openAIUserAffinityMigrationStable(config, authorizedAt, now) {
						return s.selectOpenAIUserAffinityMigrationTarget(ctx, groupID, requestedModel, excludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport, "capacity_retry_threshold", config, now, placement, runtimeStore)
					}
				}
				return nil, true, ErrNoAvailableAccounts
			}
		}
		account, accountErr := s.getOpenAIUserAffinityResidentAccount(ctx, *placement.AccountID)
		if accountErr != nil {
			return nil, true, accountErr
		}
		admission := s.classifyOpenAIUserAffinityResidentAdmission(ctx, account, groupID, requestedModel, requireCompact, requiredCapability, requiredImageCapability, requiredTransport)
		if admission == openAIUserAffinityResidentQuota7DExhausted || admission == openAIUserAffinityResidentPermanentUnavailable {
			if !runtimeOK {
				return nil, true, ErrNoAvailableAccounts
			}
			return s.selectOpenAIUserAffinityMigrationTarget(ctx, groupID, requestedModel, excludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport, string(admission), config, now, placement, runtimeStore)
		}
		if admission != openAIUserAffinityResidentAllowed {
			// 5h、RPM 和临时运行时门控只累计不同客户端请求，未达阈值不改变归属。
			if runtimeOK {
				authorizedAt, failureErr := recordOpenAIUserAffinityCapacityFailure(ctx, runtimeStore, userID, *placement.AccountID, placement.Generation, placement.ScopeKey, "resident_account_unavailable", config)
				if failureErr != nil {
					return nil, true, failureErr
				}
				if openAIUserAffinityMigrationStable(config, authorizedAt, now) {
					return s.selectOpenAIUserAffinityMigrationTarget(ctx, groupID, requestedModel, excludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport, "capacity_retry_threshold", config, now, placement, runtimeStore)
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

	newResidentExcluded := excludedIDs
	if placement != nil && placement.Status == "reset" && placement.ResetExcludeSourceAccount != nil &&
		*placement.ResetExcludeSourceAccount && placement.ResetSourceAccountID != nil {
		newResidentExcluded = cloneExcludedAccountIDs(excludedIDs)
		if newResidentExcluded == nil {
			newResidentExcluded = make(map[int64]struct{})
		}
		newResidentExcluded[*placement.ResetSourceAccountID] = struct{}{}
	}
	return s.selectOpenAIUserAffinityNewResident(ctx, groupID, requestedModel, newResidentExcluded, requireCompact, requiredCapability, requiredImageCapability, requiredTransport, scopeKey, config, now)
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

// selectOpenAIUserAffinityNewResident 按 7d/5h 额度和当前触达容量为新居民装箱。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityNewResident(ctx context.Context, groupID *int64, requestedModel string, excludedIDs map[int64]struct{}, requireCompact bool, requiredCapability OpenAIEndpointCapability, requiredImageCapability OpenAIImagesCapability, requiredTransport OpenAIUpstreamTransport, scopeKey string, config OpenAIUserAffinityConfig, now time.Time) (*AccountSelectionResult, bool, error) {
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	demand := s.predictOpenAIUserAffinityDemand(ctx, userID, config)
	accountByID, candidates, err := s.openAIUserAffinityCandidates(ctx, userID, groupID, requestedModel, excludedIDs, requireCompact, requiredCapability, requiredImageCapability, requiredTransport)
	if err != nil {
		return nil, true, err
	}
	knownQuota := false
	unknownQuota := false
	for _, candidate := range candidates {
		if candidate.Quota5HKnown && candidate.Quota7DKnown {
			knownQuota = true
		} else {
			unknownQuota = true
		}
	}

	for len(candidates) > 0 {
		candidate, found := SelectOpenAIUserAffinityCandidate(config, candidates, demand.Demand5H, demand.Demand7D, now)
		if !found {
			break
		}
		account := accountByID[candidate.AccountID]
		acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
		if acquireErr == nil && acquired != nil && acquired.Acquired {
			if !s.reserveOpenAIUserAffinityPlacement(ctx, account.ID, scopeKey) {
				if acquired.ReleaseFunc != nil {
					acquired.ReleaseFunc()
				}
				remaining := candidates[:0]
				for _, current := range candidates {
					if current.AccountID != candidate.AccountID {
						remaining = append(remaining, current)
					}
				}
				candidates = remaining
				continue
			}
			result, resultErr := s.newAcquiredSelectionResult(ctx, account, acquired.ReleaseFunc)
			return result, true, resultErr
		}
		candidates = removeOpenAIUserAffinityCandidate(candidates, candidate.AccountID)
	}
	if !knownQuota && unknownQuota {
		// 仅额度快照未知时回退普通调度，用一次真实请求自愈响应头快照。
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
		candidates = append(candidates, candidate)
	}
	return accountByID, candidates, nil
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

// reserveOpenAIUserAffinityPlacement 原子建立新居民归属、触达预留和账号长冷却。
func (s *OpenAIGatewayService) reserveOpenAIUserAffinityPlacement(ctx context.Context, accountID int64, scopeKey string) bool {
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
	if err != nil || current != nil && current.Status == "active" && current.AccountID != nil && now.Before(current.ExpiresAt) {
		return err == nil && current != nil && current.AccountID != nil && *current.AccountID == accountID
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
		Status: "active", AssignedAt: now, ExpiresAt: now.Add(14 * 24 * time.Hour),
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
