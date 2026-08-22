package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
)

const openAIAccountScheduleLayerConversationBinding = "conversation_binding"

type openAIUserConversationIdentity struct {
	userID             int64
	apiKeyID           int64
	scopeKey           string
	conversationHash   string
	aliasType          string
	aliasHash          string
	contextRebuildable bool
}

// resolveOpenAIUserConversationIdentity 只使用认证上下文和网关生成的会话信号，不信任请求体用户标识。
func resolveOpenAIUserConversationIdentity(ctx context.Context, req OpenAIAccountScheduleRequest) (openAIUserConversationIdentity, bool) {
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	if userID <= 0 || apiKeyID <= 0 {
		return openAIUserConversationIdentity{}, false
	}
	scopeKey := openAIUserAffinityScopeKey(req.GroupID, req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	identity := openAIUserConversationIdentity{
		userID: userID, apiKeyID: apiKeyID, scopeKey: scopeKey,
		contextRebuildable: req.PreviousResponseCanMove || strings.TrimSpace(req.PreviousResponseID) == "",
	}
	if previousResponseID := strings.TrimSpace(req.PreviousResponseID); previousResponseID != "" {
		identity.aliasType = "response_id"
		identity.aliasHash = openAIUserAffinityScopedStateHash(userID, apiKeyID, scopeKey, identity.aliasType, previousResponseID)
	}
	if sessionHash := strings.TrimSpace(req.SessionHash); sessionHash != "" {
		identity.conversationHash = openAIUserAffinityScopedStateHash(userID, apiKeyID, scopeKey, "session_hash", sessionHash)
	} else if identity.aliasHash != "" {
		// 没有稳定 session 信号时，同一个 previous_response_id 的客户端重试仍归入同一会话。
		identity.conversationHash = identity.aliasHash
	}
	return identity, identity.conversationHash != "" || identity.aliasHash != ""
}

func openAIUserAffinityScopedStateHash(userID, apiKeyID int64, scopeKey, kind, value string) string {
	payload := fmt.Sprintf("v1\x00%d\x00%d\x00%s\x00%s\x00%s", userID, apiKeyID,
		strings.TrimSpace(scopeKey), strings.ToLower(strings.TrimSpace(kind)), strings.TrimSpace(value))
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

// usesOpenAIUserAffinityScopedAliases 表示续链来源必须按认证用户、API Key 和 scope 隔离。
func (s *OpenAIGatewayService) usesOpenAIUserAffinityScopedAliases(ctx context.Context, req OpenAIAccountScheduleRequest) bool {
	if s == nil || s.settingService == nil || s.accountRepo == nil || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		return false
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	if userID <= 0 || apiKeyID <= 0 {
		return false
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeEnforce {
		return false
	}
	_, ok := s.accountRepo.(OpenAIUserAffinityConversationStore)
	return ok
}

// selectOpenAIUserAffinityConversation 在居民槽位前恢复已提交的长期会话绑定。
func (s *OpenAIGatewayService) selectOpenAIUserAffinityConversation(ctx context.Context, req OpenAIAccountScheduleRequest) (*AccountSelectionResult, bool, error) {
	if s == nil || s.settingService == nil || s.accountRepo == nil || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI {
		return nil, false, nil
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil || !config.Enabled || config.Mode != OpenAIUserAffinityModeEnforce {
		return nil, false, err
	}
	identity, ok := resolveOpenAIUserConversationIdentity(ctx, req)
	if !ok {
		return nil, false, nil
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityConversationStore)
	if !ok {
		return nil, false, nil
	}
	var binding *OpenAIUserConversationBinding
	if identity.aliasHash != "" {
		binding, err = store.GetOpenAIUserConversationBindingByAlias(ctx, identity.userID, identity.apiKeyID,
			identity.scopeKey, identity.aliasType, identity.aliasHash)
		if err != nil {
			return nil, true, err
		}
	}
	if binding == nil && identity.conversationHash != "" {
		binding, err = store.GetOpenAIUserConversationBinding(ctx, identity.userID, identity.apiKeyID,
			identity.scopeKey, identity.conversationHash)
		if err != nil {
			return nil, true, err
		}
	}
	if binding == nil {
		// 不可重建续链在作用域化 alias 未命中时必须失败关闭，禁止回落到 group 级旧缓存。
		if identity.aliasHash != "" && !identity.contextRebuildable {
			return nil, true, ErrOpenAIPreviousResponseAccountUnavailable
		}
		return nil, false, nil
	}
	if binding.Status == "provisional" || !binding.FirstOutputCommitted {
		// leader 首输出前不允许同会话 follower 向上游发送。
		return nil, true, ErrNoAvailableAccounts
	}
	excluded := false
	if req.ExcludedIDs != nil {
		_, excluded = req.ExcludedIDs[binding.AccountID]
	}
	account, err := s.getOpenAIUserAffinityResidentAccount(ctx, binding.AccountID)
	if err != nil {
		return nil, true, err
	}
	admission := s.classifyOpenAIUserAffinityResidentAdmission(ctx, account, req.GroupID, req.RequestedModel,
		req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
	if excluded || admission != openAIUserAffinityResidentAllowed {
		if !binding.ContextRebuildable || !identity.contextRebuildable {
			return nil, true, ErrOpenAIPreviousResponseAccountUnavailable
		}
		return s.selectOpenAIUserAffinityConversationFailover(ctx, req, config, identity, binding, admission)
	}
	s.rememberOpenAIUserAffinityConversationAttempt(ctx, binding, config, "")
	acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
	if acquireErr == nil && acquired != nil && acquired.Acquired {
		selection, selectionErr := s.newAcquiredSelectionResult(ctx, account, acquired.ReleaseFunc)
		return selection, true, selectionErr
	}
	selection, selectionErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
		AccountID: account.ID, MaxConcurrency: account.Concurrency,
		Timeout: s.schedulingConfig().StickySessionWaitTimeout, MaxWaiting: s.schedulingConfig().StickySessionMaxWaiting,
	})
	return selection, true, selectionErr
}

func (s *OpenAIGatewayService) rememberOpenAIUserAffinityConversationTransition(ctx context.Context, transition *OpenAIUserConversationTransition) {
	if s == nil || transition == nil {
		return
	}
	accountID := transition.AccountID
	placement := &OpenAIUserPlacement{
		UserID: transition.UserID, ScopeKey: transition.ScopeKey, AccountID: &accountID,
		Generation: transition.SlotGeneration, Status: "active", AssignedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(transition.Config.ResidentTTL()),
	}
	s.rememberOpenAIUserAffinityAttempt(ctx, placement)
	if attempt, found := s.openAIUserAffinityAttempt(ctx, transition.AccountID); found {
		attempt.conversation = transition
	}
}

// reserveOpenAIUserAffinityConversation 为本次新会话创建首输出前 provisional 绑定。
func (s *OpenAIGatewayService) reserveOpenAIUserAffinityConversation(ctx context.Context, req OpenAIAccountScheduleRequest, accountID int64) error {
	identity, ok := resolveOpenAIUserConversationIdentity(ctx, req)
	if !ok || identity.conversationHash == "" {
		return nil
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityConversationStore)
	if !ok {
		return nil
	}
	attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
	if !found {
		return errors.New("openai user affinity conversation requires placement attempt")
	}
	config, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil {
		return err
	}
	token := uuid.NewString()
	binding, created, err := store.ReserveOpenAIUserConversationBinding(ctx, OpenAIUserConversationReservation{
		UserID: identity.userID, APIKeyID: identity.apiKeyID, ScopeKey: identity.scopeKey,
		ConversationHash: identity.conversationHash, AccountID: accountID,
		AliasType: identity.aliasType, AliasHash: identity.aliasHash,
		PlacementGeneration: attempt.generation, ContextRebuildable: identity.contextRebuildable,
		MaxResidentSlots: config.RuntimeResidentAccountSlotCount(),
		ProvisionalToken: token, Config: config,
	})
	if err != nil {
		return err
	}
	if binding == nil || binding.AccountID != accountID {
		return errors.New("openai conversation was concurrently bound to another account")
	}
	if !created && (binding.Status == "provisional" || !binding.FirstOutputCommitted) {
		return ErrNoAvailableAccounts
	}
	if !created {
		token = ""
	}
	s.rememberOpenAIUserAffinityConversationAttempt(ctx, binding, config, token)
	return nil
}

func (s *OpenAIGatewayService) rememberOpenAIUserAffinityConversationAttempt(ctx context.Context, binding *OpenAIUserConversationBinding, config OpenAIUserAffinityConfig, token string) {
	if s == nil || binding == nil {
		return
	}
	key := openAIUserAffinityRequestKey(ctx)
	value, ok := s.openaiAffinity.requests.Load(key)
	state, valid := value.(*openAIUserAffinityRequestState)
	if !ok || !valid || state == nil {
		state = &openAIUserAffinityRequestState{
			scopeKey: binding.ScopeKey, generation: binding.SlotGeneration,
			accountID: binding.AccountID, createdAt: time.Now().UTC(),
		}
		s.openaiAffinity.requests.Store(key, state)
	}
	state.scopeKey = binding.ScopeKey
	state.generation = binding.SlotGeneration
	state.accountID = binding.AccountID
	state.conversation = &OpenAIUserConversationTransition{
		BindingID: binding.ID, UserID: binding.UserID, APIKeyID: binding.APIKeyID,
		ScopeKey: binding.ScopeKey, ConversationHash: binding.ConversationHash,
		ResidentSlotID: binding.ResidentSlotID, AccountID: binding.AccountID,
		SlotGeneration: binding.SlotGeneration, ProvisionalToken: token, Config: config,
	}
	if binding.FirstOutputCommitted {
		state.conversationCommitted.Store(true)
	}
}

func (s *OpenAIGatewayService) commitOpenAIUserAffinityConversation(ctx context.Context, accountID int64) bool {
	attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
	if !found || attempt.conversation == nil {
		return true
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityConversationStore)
	if !ok {
		return false
	}
	transition := *attempt.conversation
	if value := attempt.responseAliasHash.Load(); value != nil {
		transition.ResponseAliasHash, _ = value.(string)
	}
	firstCommit, err := store.CommitOpenAIUserConversationBinding(ctx, transition)
	if err != nil || strings.TrimSpace(attempt.conversation.ProvisionalToken) != "" && !firstCommit {
		return false
	}
	attempt.conversationCommitted.Store(true)
	attempt.conversation.ProvisionalToken = ""
	return true
}

// stageOpenAIUserAffinityResponseAlias 仅暂存作用域化响应 ID 哈希，最终成功时随 binding 原子提交。
func (s *OpenAIGatewayService) stageOpenAIUserAffinityResponseAlias(ctx context.Context, accountID int64, responseID string) {
	responseID = strings.TrimSpace(responseID)
	if s == nil || accountID <= 0 || responseID == "" {
		return
	}
	attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
	if !found || attempt.conversation == nil {
		return
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	if userID <= 0 || apiKeyID <= 0 {
		return
	}
	hash := openAIUserAffinityScopedStateHash(userID, apiKeyID, attempt.conversation.ScopeKey, "response_id", responseID)
	attempt.responseAliasHash.Store(hash)
}

func (s *OpenAIGatewayService) rollbackOpenAIUserAffinityConversation(ctx context.Context, attempt *openAIUserAffinityRequestState) {
	if s == nil || attempt == nil || attempt.conversation == nil || attempt.conversationCommitted.Load() ||
		strings.TrimSpace(attempt.conversation.ProvisionalToken) == "" {
		return
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityConversationStore)
	if !ok {
		return
	}
	_, _ = store.RollbackOpenAIUserConversationBinding(ctx, *attempt.conversation)
}
