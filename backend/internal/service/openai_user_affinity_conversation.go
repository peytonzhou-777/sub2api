package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
)

const openAIAccountScheduleLayerConversationBinding = "conversation_binding"

type openAIUserConversationIdentity struct {
	userID                 int64
	apiKeyID               int64
	scopeKey               string
	conversationHash       string
	legacyConversationHash string
	aliasType              string
	aliasHash              string
	codexThreadHash        string
	contextRebuildable     bool
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
	if topology := openAICodexThreadAffinityFromContext(ctx); topology != nil {
		identity.codexThreadHash = strings.ToLower(strings.TrimSpace(topology.selfAliasHash))
	}
	if previousResponseID := strings.TrimSpace(req.PreviousResponseID); previousResponseID != "" {
		identity.aliasType = "response_id"
		identity.aliasHash = openAIUserAffinityScopedStateHash(userID, apiKeyID, scopeKey, identity.aliasType, previousResponseID)
	}
	if identity.codexThreadHash != "" {
		// Codex 同一 session 下可派生多个线程，线程 HMAC 必须高于 session hash。
		identity.conversationHash = identity.codexThreadHash
		if sessionHash := strings.TrimSpace(req.SessionHash); sessionHash != "" {
			// 升级期仍回读旧的 session 会话绑定，命中后原子补齐线程别名。
			identity.legacyConversationHash = openAIUserAffinityScopedStateHash(userID, apiKeyID, scopeKey, "session_hash", sessionHash)
		}
	} else if sessionHash := strings.TrimSpace(req.SessionHash); sessionHash != "" {
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
func (s *OpenAIGatewayService) selectOpenAIUserAffinityConversation(ctx context.Context, req OpenAIAccountScheduleRequest) (selection *AccountSelectionResult, handled bool, resultErr error) {
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
	topology := openAICodexThreadAffinityFromContext(ctx)
	if topology != nil {
		topology.resetAuthorization()
	}
	selfBinding, parentBinding, topologyErr := resolveOpenAICodexThreadBindings(ctx, store, req, identity, topology)
	if topologyErr != nil {
		return nil, true, topologyErr
	}
	selfBinding = s.acceptOpenAICodexLineageBinding(ctx, req, identity, selfBinding)
	parentBinding = s.acceptOpenAICodexLineageBinding(ctx, req, identity, parentBinding)
	// 过期的自身 Thread 不能借用仍活跃的父系或旧 Session 索引重新激活。
	if selfBinding == nil && identity.codexThreadHash != "" {
		if expiryStore, ok := s.accountRepo.(OpenAIConversationActivityStore); ok {
			aliases := []OpenAIUserConversationAlias{openAICodexThreadReservationAlias(req.GroupID, identity.codexThreadHash)}
			if topology != nil {
				for _, hash := range topology.selfLookupHashes {
					aliases = append(aliases, openAICodexThreadReservationAlias(req.GroupID, hash))
				}
			}
			expired, err := expiryStore.HasExpiredOpenAIConversation(ctx, identity.userID, identity.apiKeyID, identity.scopeKey, identity.conversationHash, aliases)
			if err != nil {
				return nil, true, err
			}
			if expired {
				return nil, true, ErrOpenAIConversationResetRequired
			}
		}
	}
	var binding *OpenAIUserConversationBinding
	bindingSource := ""
	if selfBinding != nil {
		binding = selfBinding
		bindingSource = "codex_self"
	} else if parentBinding != nil && identity.codexThreadHash != "" {

		binding = parentBinding
		bindingSource = "codex_parent"
	} else if identity.aliasHash != "" {
		binding, err = store.GetOpenAIUserConversationBindingByAlias(ctx, identity.userID, identity.apiKeyID,
			identity.scopeKey, identity.aliasType, identity.aliasHash)
		if err != nil {
			return nil, true, err
		}
		if binding != nil {
			bindingSource = "protocol_alias"
		}
	}
	if binding == nil && identity.conversationHash != "" {
		binding, err = store.GetOpenAIUserConversationBinding(ctx, identity.userID, identity.apiKeyID,
			identity.scopeKey, identity.conversationHash)
		if err != nil {
			return nil, true, err
		}
		if binding != nil {
			bindingSource = "conversation"
		}
	}
	if binding == nil && identity.legacyConversationHash != "" {
		binding, err = store.GetOpenAIUserConversationBinding(ctx, identity.userID, identity.apiKeyID,
			identity.scopeKey, identity.legacyConversationHash)
		if err != nil {
			return nil, true, err
		}
		if binding != nil {
			bindingSource = "legacy_conversation"
		}
	}
	if binding == nil {
		if topology != nil && len(topology.parentAliasHashes) > 0 {
			return nil, true, ErrOpenAIConversationResetRequired
		}
		// 不可重建续链在作用域化 alias 未命中时必须失败关闭，禁止回落到 group 级旧缓存。
		if identity.aliasHash != "" {
			return nil, true, ErrOpenAIConversationResetRequired
		}
		if opaque, _ := ctx.Value(openAIContinuationStateKey{}).(bool); opaque {
			return nil, true, ErrOpenAIConversationResetRequired
		}
		if expiryStore, ok := s.accountRepo.(OpenAIConversationActivityStore); ok {
			aliases := []OpenAIUserConversationAlias{}
			if identity.codexThreadHash != "" {
				aliases = append(aliases, openAICodexThreadReservationAlias(req.GroupID, identity.codexThreadHash))
			}
			if topology != nil {
				for _, hash := range topology.parentAliasHashes {
					aliases = append(aliases, openAICodexThreadReservationAlias(req.GroupID, hash))
				}
			}
			expired, err := expiryStore.HasExpiredOpenAIConversation(ctx, identity.userID, identity.apiKeyID, identity.scopeKey, identity.conversationHash, aliases)
			if err != nil {
				return nil, true, err
			}
			if expired {
				return nil, true, ErrOpenAIConversationResetRequired
			}
		}
		return nil, false, nil
	}
	defer func() {
		if resultErr != nil {
			resultErr = &OpenAIContinuationSelectionError{Cause: resultErr, BindingID: binding.ID, AccountID: binding.AccountID, AccountPersonaID: binding.AccountPersonaID, SessionEpoch: binding.PersonaSessionEpoch, Profile: string(binding.ProfileID), Source: bindingSource}
		}
	}()
	if binding.AccountPersonaID > 0 {
		if _, excluded := OpenAIAttemptExclusionsFromContext(ctx).AccountPersonaIDs[binding.AccountPersonaID]; excluded {
			return nil, true, ErrOpenAIPreviousResponseAccountUnavailable
		}
	}
	validBinding, validateErr := store.ValidateOpenAIUserConversationBinding(ctx, *binding)
	if validateErr != nil {
		return nil, true, validateErr
	}
	if !validBinding {
		slog.Warn("openai_user_affinity.stale_binding_slot_invalid",
			"user_id", identity.userID, "api_key_id", identity.apiKeyID, "scope_key", identity.scopeKey,
			"binding_id", binding.ID, "account_id", binding.AccountID, "slot_id", binding.ResidentSlotID,
			"slot_generation", binding.SlotGeneration)
		if !binding.ContextRebuildable || !identity.contextRebuildable {
			return nil, true, ErrOpenAIConversationResetRequired
		}
		// reservation 会在 scope 锁内再次校验并把 stale binding 转回 provisional，
		// 这里先交还当前 scope 的居民槽位选择流程。
		return nil, false, nil
	}
	if bindingSource == "codex_parent" && (binding.Status == "provisional" || !binding.FirstOutputCommitted) {
		return nil, true, ErrOpenAICodexParentThreadPending
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
	if isOpenAIUserAffinityResidentHardUnavailable(admission) {
		s.observeOpenAIUserAffinityRecovery("stale_binding_account_unavailable",
			"user_id", identity.userID, "api_key_id", identity.apiKeyID, "scope_key", identity.scopeKey,
			"binding_id", binding.ID, "account_id", binding.AccountID, "binding_source", bindingSource,
			"admission", admission)
		if bindingSource == "codex_self" || bindingSource == "codex_parent" {
			s.observeOpenAIUserAffinityRecovery("stale_lineage_account_unavailable",
				"user_id", identity.userID, "api_key_id", identity.apiKeyID, "scope_key", identity.scopeKey,
				"binding_id", binding.ID, "account_id", binding.AccountID, "binding_source", bindingSource)
		}
	}
	if binding.Status == "provisional" || !binding.FirstOutputCommitted {
		// 首输出前若账号已确认不可恢复，先回滚旧预留，避免 provisional 绑定永久占住会话。
		if bindingSource == "codex_parent" {
			if excluded || isOpenAIUserAffinityResidentHardUnavailable(admission) {
				return nil, true, ErrOpenAICodexParentThreadUnavailable
			}
			return nil, true, ErrOpenAICodexParentThreadPending
		}
		if isOpenAIUserAffinityResidentHardUnavailable(admission) {
			rolledBack, rollbackErr := store.RollbackOpenAIUserConversationBinding(ctx, OpenAIUserConversationTransition{
				BindingID: binding.ID, UserID: binding.UserID, APIKeyID: binding.APIKeyID,
				ScopeKey: binding.ScopeKey, ConversationHash: binding.ConversationHash,
				ResidentSlotID: binding.ResidentSlotID, AccountID: binding.AccountID,
				SlotGeneration: binding.SlotGeneration, ProvisionalToken: binding.ProvisionalToken,
				Config: config,
			})
			if rollbackErr != nil {
				return nil, true, rollbackErr
			}
			if rolledBack {
				s.observeOpenAIUserAffinityRecovery("provisional_rollback",
					"user_id", binding.UserID, "api_key_id", binding.APIKeyID, "scope_key", binding.ScopeKey,
					"binding_id", binding.ID, "account_id", binding.AccountID)
				if maintenance, maintenanceOK := s.accountRepo.(OpenAIUserAffinityResidentSlotMaintenanceStore); maintenanceOK {
					if _, evictErr := maintenance.EvictOpenAIUserResidentSlot(ctx, binding.UserID, binding.ScopeKey,
						binding.ResidentSlotID, binding.AccountID, binding.SlotGeneration, "account_unavailable", time.Now().UTC()); evictErr != nil {
						return nil, true, evictErr
					}
				}
				if selection, found, selectErr := s.selectOpenAIUserAffinityResidentSlots(ctx, req); found || selectErr != nil {
					return selection, found, selectErr
				}
				return nil, false, nil
			}
		}
		// leader 首输出前不允许同会话 follower 向上游发送。
		s.observeOpenAIUserAffinityRecovery("concurrent_rebuild_conflict",
			"user_id", identity.userID, "api_key_id", identity.apiKeyID, "scope_key", identity.scopeKey,
			"binding_id", binding.ID, "account_id", binding.AccountID, "binding_source", bindingSource)
		return nil, true, ErrNoAvailableAccounts
	}
	if excluded || admission != openAIUserAffinityResidentAllowed {
		if bindingSource == "codex_parent" {
			// 父线程命中后属于硬锁；父账号不可用时不得把派生请求投递到其他账号。
			return nil, true, ErrOpenAICodexParentThreadUnavailable
		}
		if !binding.ContextRebuildable || !identity.contextRebuildable {
			return nil, true, ErrOpenAIPreviousResponseAccountUnavailable
		}
		if binding.AccountPersonaID <= 0 {
			return s.selectOpenAIUserAffinityConversationFailover(ctx, req, config, identity, binding, admission)
		}
		if excluded || isOpenAIUserAffinityResidentHardUnavailable(admission) {
			return nil, true, ErrOpenAIPreviousResponseAccountUnavailable
		}
		// 动态 Persona binding 已锁定完整 lineage；临时容量只在原目标排队，
		// 不得沿用旧账号级 failover 把 continuation 搬到另一个 Persona。
	}
	var executionTarget OpenAIExecutionTarget
	if account.IsOpenAIOAuth() {
		var targetErr error
		executionTarget, targetErr = s.restoreOpenAIUserConversationExecutionTarget(ctx, binding)
		if targetErr != nil {
			// OAuth 续链缺少 Persona/epoch 身份时禁止重新选号，避免 Thread 跨出站设备。
			if errors.Is(targetErr, ErrOpenAIConversationResetRequired) {
				return nil, true, ErrOpenAIConversationResetRequired
			}
			return nil, true, ErrOpenAIPreviousResponseAccountUnavailable
		}
	}
	s.rememberOpenAIUserAffinityConversationAttempt(ctx, binding, config, "")
	if bindingSource == "codex_parent" {
		// 为派生线程建立自己的 provisional binding，父 binding 只作为选号依据。
		childIdentity := identity
		// 派生线程沿用父 binding 的居民槽位 scope，避免跨 HTTP/WS lane 重复占槽。
		childIdentity.scopeKey = binding.ScopeKey
		if err := s.reserveOpenAIUserAffinityConversationWithAliases(ctx, req, account.ID, childIdentity, []OpenAIUserConversationAlias{
			openAICodexThreadReservationAlias(req.GroupID, identity.codexThreadHash),
		}, binding, false); err != nil {
			return nil, true, err
		}
	} else if bindingSource != "codex_parent" && identity.codexThreadHash != "" && (bindingSource != "codex_self" || binding.ConversationHash != identity.codexThreadHash) {
		// 为升级前或 v1 Session+Thread 索引命中的会话补齐稳定的 v2 Thread 别名。
		legacyIdentity := identity
		legacyIdentity.conversationHash = binding.ConversationHash
		if err := s.reserveOpenAIUserAffinityConversationWithAliases(ctx, req, account.ID, legacyIdentity, []OpenAIUserConversationAlias{
			openAICodexThreadReservationAlias(req.GroupID, identity.codexThreadHash),
		}, nil, false); err != nil {
			return nil, true, err
		}
	}
	if topology != nil && parentBinding != nil && parentBinding.AccountID == account.ID {
		topology.authorize(account.ID)
	}
	acquired, acquireErr := s.tryAcquireAccountSlot(ctx, account.ID, account.Concurrency)
	if acquireErr == nil && acquired != nil && acquired.Acquired {
		selection, selectionErr := s.newAcquiredSelectionResult(ctx, account, acquired.ReleaseFunc)
		if selection != nil && executionTarget.Valid() {
			selection.ExecutionTarget = &executionTarget
		}
		return selection, true, selectionErr
	}
	selection, selectionErr := s.newSelectionResult(ctx, account, false, nil, &AccountWaitPlan{
		AccountID: account.ID, MaxConcurrency: account.Concurrency,
		Timeout: s.schedulingConfig().StickySessionWaitTimeout, MaxWaiting: s.schedulingConfig().StickySessionMaxWaiting,
	})
	if selection != nil && executionTarget.Valid() {
		selection.ExecutionTarget = &executionTarget
	}
	return selection, true, selectionErr
}

func (s *OpenAIGatewayService) restoreOpenAIUserConversationExecutionTarget(
	ctx context.Context,
	binding *OpenAIUserConversationBinding,
) (OpenAIExecutionTarget, error) {
	if binding == nil || binding.AccountPersonaID <= 0 || binding.PersonaSessionEpoch <= 0 ||
		binding.CredentialChainID == "" || binding.ProfileID == "" || binding.ProfileVersion == "" ||
		binding.BindingEpoch != OpenAIConversationBindingEpoch {
		return OpenAIExecutionTarget{}, ErrOpenAIConversationResetRequired
	}
	if s == nil || s.accountPersonaRepo == nil {
		return OpenAIExecutionTarget{}, ErrOpenAIPreviousResponseAccountUnavailable
	}
	persona, err := s.accountPersonaRepo.GetAccountPersona(ctx, binding.AccountID, binding.AccountPersonaID)
	if errors.Is(err, ErrOpenAIAccountPersonaNotFound) || (err == nil && persona == nil) {
		return OpenAIExecutionTarget{}, ErrOpenAIConversationResetRequired
	}
	if err != nil {
		return OpenAIExecutionTarget{}, ErrOpenAIPreviousResponseAccountUnavailable
	}
	session, err := s.accountPersonaRepo.GetAccountPersonaSession(ctx, binding.AccountID, binding.AccountPersonaID, binding.PersonaSessionEpoch, time.Now().UTC())
	if errors.Is(err, ErrOpenAIAccountPersonaSessionNotFound) || errors.Is(err, ErrOpenAIAccountPersonaSessionExpired) ||
		(err == nil && session == nil) {
		return OpenAIExecutionTarget{}, ErrOpenAIConversationResetRequired
	}
	if err != nil {
		return OpenAIExecutionTarget{}, ErrOpenAIPreviousResponseAccountUnavailable
	}
	target, err := OpenAIExecutionTargetFromPersonaSession(*persona, *session)
	if err != nil || target.CredentialChainID != binding.CredentialChainID ||
		target.ProfileID != binding.ProfileID || target.ProfileVersion != binding.ProfileVersion {
		return OpenAIExecutionTarget{}, ErrOpenAIConversationResetRequired
	}
	return target, nil
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
	aliases := make([]OpenAIUserConversationAlias, 0, 2)
	if identity.aliasType != "" && identity.aliasHash != "" {
		aliases = append(aliases, OpenAIUserConversationAlias{
			ScopeKey: identity.scopeKey, Type: identity.aliasType, Hash: identity.aliasHash,
		})
	}
	if identity.codexThreadHash != "" {
		aliases = append(aliases, openAICodexThreadReservationAlias(req.GroupID, identity.codexThreadHash))
	}
	return s.reserveOpenAIUserAffinityConversationWithAliases(ctx, req, accountID, identity, aliases, nil, true)
}

// reserveOpenAIUserAffinityConversationWithAliases 允许父线程继承仅写入当前线程别名，避免夺取父响应别名。
func (s *OpenAIGatewayService) reserveOpenAIUserAffinityConversationWithAliases(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	accountID int64,
	identity openAIUserConversationIdentity,
	aliases []OpenAIUserConversationAlias,
	preferredBinding *OpenAIUserConversationBinding,
	manageActiveRoute bool,
) error {
	if identity.conversationHash == "" {
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
	preferredSlotID, preferredSlotGeneration := int64(0), int64(0)
	if preferredBinding != nil && preferredBinding.AccountID == accountID {
		preferredSlotID = preferredBinding.ResidentSlotID
		preferredSlotGeneration = preferredBinding.SlotGeneration
	}
	binding, created, err := store.ReserveOpenAIUserConversationBinding(ctx, OpenAIUserConversationReservation{
		UserID: identity.userID, APIKeyID: identity.apiKeyID, ScopeKey: identity.scopeKey,
		ConversationHash: identity.conversationHash, AccountID: accountID,
		Aliases:             aliases,
		PlacementGeneration: attempt.generation, ContextRebuildable: identity.contextRebuildable,
		PreferredResidentSlotID: preferredSlotID, PreferredSlotGeneration: preferredSlotGeneration,
		MaxResidentSlots: config.RuntimeResidentAccountSlotCount(),
		ProvisionalToken: token, ManageActiveRoute: manageActiveRoute, Config: config,
	})
	if err != nil {
		slog.Warn("openai_user_affinity.conversation_reservation_rejected",
			"user_id", identity.userID, "scope_key", identity.scopeKey, "account_id", accountID,
			"reason", openAIUserAffinityReservationErrorReason(err), "error", err)
		return err
	}
	if binding == nil && preferredBinding != nil {
		return ErrOpenAICodexParentThreadUnavailable
	}
	if binding == nil {
		return ErrOpenAIUserAffinityNoCandidateSlot
	}
	if binding.AccountID != accountID {
		return ErrOpenAIUserAffinityReservationConflict
	}
	if !created && (binding.Status == "provisional" || !binding.FirstOutputCommitted) {
		s.observeOpenAIUserAffinityRecovery("concurrent_rebuild_conflict",
			"user_id", identity.userID, "api_key_id", identity.apiKeyID, "scope_key", identity.scopeKey,
			"binding_id", binding.ID, "account_id", binding.AccountID)
		return ErrNoAvailableAccounts
	}
	if !created {
		token = ""
	}
	s.rememberOpenAIUserAffinityConversationAttempt(ctx, binding, config, token)
	if attempt, found := s.openAIUserAffinityAttempt(ctx, binding.AccountID); found && attempt.conversation != nil {
		attempt.conversation.Aliases = append([]OpenAIUserConversationAlias(nil), aliases...)
	}
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
		BindingEpoch:     binding.BindingEpoch,
		AccountPersonaID: binding.AccountPersonaID, PersonaSessionEpoch: binding.PersonaSessionEpoch,
		CredentialChainID: binding.CredentialChainID, RootClientSessionHash: binding.RootClientSessionHash,
		ProfileID: binding.ProfileID, ProfileVersion: binding.ProfileVersion,
		ManageActiveRoute: binding.ManageActiveRoute, ActiveRoutePending: binding.ActiveRoutePending,
	}
	if binding.FirstOutputCommitted {
		state.conversationCommitted.Store(true)
	}
}

func (s *OpenAIGatewayService) bindOpenAIUserAffinityConversationExecutionTarget(
	ctx context.Context,
	accountID int64,
	target OpenAIExecutionTarget,
	rootClientSessionHash string,
) error {
	attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
	if !found || attempt.conversation == nil || strings.TrimSpace(attempt.conversation.ProvisionalToken) == "" {
		return nil
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityConversationStore)
	if !ok {
		return errors.New("openai conversation binding store unavailable")
	}
	transition := *attempt.conversation
	transition.RootClientSessionHash = strings.ToLower(strings.TrimSpace(rootClientSessionHash))
	if err := store.BindOpenAIUserConversationExecutionTarget(ctx, transition, target); err != nil {
		return err
	}
	attempt.conversation.AccountPersonaID = target.AccountPersonaID
	attempt.conversation.PersonaSessionEpoch = target.SessionEpoch
	attempt.conversation.CredentialChainID = target.CredentialChainID
	attempt.conversation.RootClientSessionHash = transition.RootClientSessionHash
	attempt.conversation.ProfileID = target.ProfileID
	attempt.conversation.ProfileVersion = target.ProfileVersion
	return nil
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
	if err != nil {
		return false
	}
	if strings.TrimSpace(attempt.conversation.ProvisionalToken) != "" && !firstCommit {
		s.observeOpenAIUserAffinityRecovery("concurrent_rebuild_conflict",
			"user_id", transition.UserID, "api_key_id", transition.APIKeyID, "scope_key", transition.ScopeKey,
			"binding_id", transition.BindingID, "account_id", transition.AccountID, "phase", "first_output_commit")
		return false
	}
	if strings.TrimSpace(attempt.conversation.ProvisionalToken) != "" && firstCommit {
		s.observeOpenAIUserAffinityRecovery("provisional_commit_success",
			"user_id", transition.UserID, "api_key_id", transition.APIKeyID, "scope_key", transition.ScopeKey,
			"binding_id", transition.BindingID, "account_id", transition.AccountID)
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
	rolledBack, err := store.RollbackOpenAIUserConversationBinding(ctx, *attempt.conversation)
	if err == nil && rolledBack {
		s.observeOpenAIUserAffinityRecovery("provisional_rollback",
			"user_id", attempt.conversation.UserID, "api_key_id", attempt.conversation.APIKeyID,
			"scope_key", attempt.conversation.ScopeKey, "binding_id", attempt.conversation.BindingID,
			"account_id", attempt.conversation.AccountID)
	}
}

// acceptOpenAICodexLineageBinding 只允许 lineage 恢复当前 endpoint scope 的权威 binding。
func (s *OpenAIGatewayService) acceptOpenAICodexLineageBinding(
	ctx context.Context,
	req OpenAIAccountScheduleRequest,
	identity openAIUserConversationIdentity,
	binding *OpenAIUserConversationBinding,
) *OpenAIUserConversationBinding {
	if binding == nil || binding.ScopeKey == identity.scopeKey {
		return binding
	}
	s.observeOpenAIUserAffinityRecovery("stale_lineage_scope_mismatch",
		"user_id", identity.userID, "api_key_id", identity.apiKeyID, "scope_key", identity.scopeKey,
		"binding_id", binding.ID, "binding_scope_key", binding.ScopeKey, "account_id", binding.AccountID)
	account, err := s.getOpenAIUserAffinityResidentAccount(ctx, binding.AccountID)
	if err == nil {
		admission := s.classifyOpenAIUserAffinityResidentAdmission(ctx, account, req.GroupID, req.RequestedModel,
			req.RequireCompact, req.RequiredCapability, req.RequiredImageCapability, req.RequiredTransport)
		if isOpenAIUserAffinityResidentHardUnavailable(admission) {
			s.observeOpenAIUserAffinityRecovery("stale_lineage_account_unavailable",
				"user_id", identity.userID, "api_key_id", identity.apiKeyID, "scope_key", identity.scopeKey,
				"binding_id", binding.ID, "binding_scope_key", binding.ScopeKey, "account_id", binding.AccountID,
				"admission", admission)
		}
	}
	return nil
}
