package service

import (
	"context"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

const openAIAccountScheduleLayerUserAffinity = "user_affinity"

func newOpenAIUserAffinityScheduleRequest(
	groupID *int64,
	platform, previousResponseID, requestedModel string,
	requiredTransport OpenAIUpstreamTransport,
	requiredCapability OpenAIEndpointCapability,
	requiredImageCapability OpenAIImagesCapability,
	requireCompact bool,
	excludedIDs map[int64]struct{},
) OpenAIAccountScheduleRequest {
	return OpenAIAccountScheduleRequest{
		GroupID: groupID, Platform: platform, PreviousResponseID: previousResponseID,
		RequestedModel: requestedModel, RequiredTransport: requiredTransport,
		RequiredCapability: requiredCapability, RequiredImageCapability: requiredImageCapability,
		RequireCompact: requireCompact, ExcludedIDs: excludedIDs,
	}
}

// observeOpenAIUserAffinityShadow 在统一调度入口固定期望账号，并在退出时记录实际选择。
func (s *OpenAIGatewayService) observeOpenAIUserAffinityShadow(ctx context.Context, req OpenAIAccountScheduleRequest, decision *OpenAIAccountScheduleDecision) func() {
	expectedAccountID := s.evaluateOpenAIUserAffinityShadow(ctx, req)
	return func() {
		selectedAccountID := int64(0)
		if decision != nil {
			selectedAccountID = decision.SelectedAccountID
		}
		s.recordOpenAIUserAffinityShadowDecision(ctx, expectedAccountID, selectedAccountID)
	}
}

// selectOpenAIUserAffinity 为高级调度在会话粘性前恢复用户归属。
func (s *defaultOpenAIAccountScheduler) selectOpenAIUserAffinity(ctx context.Context, req OpenAIAccountScheduleRequest, decision *OpenAIAccountScheduleDecision) (*AccountSelectionResult, bool, error) {
	if s == nil || s.service == nil || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		return nil, false, nil
	}
	selection, found, err := s.service.selectOpenAIUserAffinityPlacement(ctx, req.GroupID, req.RequestedModel, req.ExcludedIDs, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	if err != nil || !found || selection == nil || selection.Account == nil {
		return selection, false, err
	}
	decision.Layer = openAIAccountScheduleLayerUserAffinity
	decision.SelectedAccountID = selection.Account.ID
	decision.SelectedAccountType = selection.Account.Type
	return selection, true, nil
}

// reserveOpenAIUserAffinitySelection 为高级调度成功选中的新居民建立归属。
func (s *defaultOpenAIAccountScheduler) reserveOpenAIUserAffinitySelection(ctx context.Context, req OpenAIAccountScheduleRequest, selection *AccountSelectionResult, selectionErr error) {
	if s == nil || s.service == nil || selectionErr != nil || selection == nil || selection.Account == nil || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		return
	}
	scopeKey := openAIUserAffinityScopeKey(req.GroupID, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	_ = s.service.reserveOpenAIUserAffinityPlacement(ctx, selection.Account.ID, scopeKey)
}

// selectLegacyOpenAIUserAffinityPreflight 保证 legacy 调度同样先处理协议续链，再处理用户归属。
func (s *OpenAIGatewayService) selectLegacyOpenAIUserAffinityPreflight(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	decision *OpenAIAccountScheduleDecision,
) (*AccountSelectionResult, bool, error) {
	if strings.TrimSpace(req.PreviousResponseID) != "" && req.Platform == PlatformOpenAI {
		selection, err := s.selectAccountByPreviousResponseIDForCapability(ctx, req.GroupID, req.PreviousResponseID, req.RequestedModel, req.ExcludedIDs, req.RequiredCapability, req.RequireCompact)
		if err != nil {
			return nil, true, err
		}
		if selection != nil && selection.Account != nil &&
			s.isOpenAIAccountTransportCompatible(selection.Account, req.RequiredTransport) &&
			accountSupportsOpenAICapabilities(selection.Account, req.RequiredCapability, req.RequiredImageCapability) {
			s.rememberOpenAIUserAffinityPreviousResponseAttempt(ctx, req, selection.Account.ID)
			decision.Layer = openAIAccountScheduleLayerPreviousResponse
			decision.StickyPreviousHit = true
			decision.SelectedAccountID = selection.Account.ID
			decision.SelectedAccountType = selection.Account.Type
			return selection, true, nil
		}
		if selection != nil && selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		if !req.PreviousResponseCanMove {
			return nil, true, ErrOpenAIPreviousResponseAccountUnavailable
		}
	}
	if req.Platform != PlatformOpenAI {
		return nil, false, nil
	}
	selection, found, err := s.selectOpenAIUserAffinityPlacement(
		ctx, req.GroupID, req.RequestedModel, req.ExcludedIDs, req.RequireCompact,
		req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport,
	)
	if err != nil || !found {
		return selection, found, err
	}
	decision.Layer = openAIAccountScheduleLayerUserAffinity
	if selection != nil && selection.Account != nil {
		decision.SelectedAccountID = selection.Account.ID
		decision.SelectedAccountType = selection.Account.Type
	}
	return selection, true, nil
}

// rememberOpenAIUserAffinityPreviousResponseAttempt 让严格续链成功同样刷新既有居住 TTL。
func (s *OpenAIGatewayService) rememberOpenAIUserAffinityPreviousResponseAttempt(ctx context.Context, req OpenAIAccountScheduleRequest, accountID int64) {
	if s == nil || s.settingService == nil || s.accountRepo == nil || accountID <= 0 {
		return
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeEnforce {
		return
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	if userID <= 0 {
		return
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityStore)
	if !ok {
		return
	}
	scopeKey := openAIUserAffinityScopeKey(req.GroupID, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	placement, err := store.GetOpenAIUserPlacement(ctx, userID, scopeKey)
	if err != nil || placement == nil || placement.AccountID == nil || placement.Status != "active" ||
		*placement.AccountID != accountID || !time.Now().UTC().Before(placement.ExpiresAt) {
		return
	}
	s.rememberOpenAIUserAffinityAttempt(ctx, placement)
}
