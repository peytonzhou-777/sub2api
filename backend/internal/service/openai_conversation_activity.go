package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

// OpenAIConversationActivityStore 将在途保护和会话活跃截止时间与常驻偏好隔离。
type OpenAIConversationActivityStore interface {
	AcquireOpenAIConversationActivity(context.Context, OpenAIUserConversationTransition, string, time.Time, ...string) (bool, error)
	RenewOpenAIConversationActivity(context.Context, string, time.Time) (bool, error)
	ReleaseOpenAIConversationActivity(context.Context, string) error
	HasExpiredOpenAIConversation(context.Context, int64, int64, string, string, []OpenAIUserConversationAlias) (bool, error)
	TouchOpenAIConversationActivity(context.Context, OpenAIUserConversationTransition, time.Time) (bool, error)
}

// OpenAIContinuationSelectionError 保留选中但被拒绝的目标，仅用于内部审计。
type OpenAIContinuationSelectionError struct {
	Cause                                                error
	BindingID, AccountID, AccountPersonaID, SessionEpoch int64
	Profile, Source                                      string
	Reason, RequestKind, ScopeKey                        string
}

func (e *OpenAIContinuationSelectionError) Error() string { return e.Cause.Error() }
func (e *OpenAIContinuationSelectionError) Unwrap() error { return e.Cause }

func (s *OpenAIGatewayService) continuationSelectionError(ctx context.Context, accountID int64, cause error) error {
	attempt, ok := s.openAIUserAffinityAttempt(ctx, accountID)
	if !ok || attempt.conversation == nil {
		return cause
	}
	tr := attempt.conversation
	reason := "persona_reservation_failed"
	if errors.Is(cause, ErrOpenAIPersonaUserCapacity) {
		reason = "persona_capacity_limited"
	}
	if errors.Is(cause, ErrOpenAIConversationResetRequired) {
		reason = "bound_identity_invalid"
	}
	return &OpenAIContinuationSelectionError{Cause: cause, BindingID: tr.BindingID, AccountID: accountID, AccountPersonaID: tr.AccountPersonaID, SessionEpoch: tr.PersonaSessionEpoch, Profile: string(tr.ProfileID), Source: "persona_reservation", Reason: reason, RequestKind: OpenAIConversationRequestKind(ctx), ScopeKey: tr.ScopeKey}
}

type openAIConversationActivity struct {
	once                  sync.Once
	done                  chan struct{}
	bindingID             int64
	accountID, generation int64
	stop                  func()
	personaReservation    *openAIPersonaUserReservationState
	requestKind           string
}

type openAIConversationReferenceKey struct{}

// ContextWithOpenAIConversationReference 在 WS 连接中保存不可变引用，不依赖可清理的请求缓存。
func (s *OpenAIGatewayService) ContextWithOpenAIConversationReference(ctx context.Context, accountID int64) context.Context {
	if attempt, ok := s.openAIUserAffinityAttempt(ctx, accountID); ok && attempt.conversation != nil {
		ref := *attempt.conversation
		return context.WithValue(ctx, openAIConversationReferenceKey{}, ref)
	}
	return ctx
}

const openAIConversationHoldTTL = 2 * time.Minute

type openAIContinuationStateKey struct{}
type openAIConversationRequestKindKey struct{}

// OpenAIConversationRequestKind 只记录协议类别，不记录用户正文或原始标识。
func OpenAIConversationRequestKind(ctx context.Context) string {
	if ctx == nil {
		return "response"
	}
	kind, _ := ctx.Value(openAIConversationRequestKindKey{}).(string)
	if kind == "" {
		return "response"
	}
	return kind
}

func openAIConversationRejection(ctx context.Context, cause error, reason, scope string) error {
	return &OpenAIContinuationSelectionError{Cause: cause, Reason: reason, RequestKind: OpenAIConversationRequestKind(ctx), ScopeKey: scope, Source: "identity_lookup"}
}

// ContextWithOpenAIContinuationState 保留不透明状态的存在性，禁止无绑定时作为新根选号。
func ContextWithOpenAIContinuationState(ctx context.Context, headers http.Header, body []byte, paths ...string) context.Context {
	state, conflict := extractOpenAITurnStateFromRequest(headers, body)
	kind := "response"
	if len(paths) > 0 && strings.HasSuffix(strings.TrimRight(paths[0], "/"), "/compact") {
		kind = "legacy_compact"
	}
	if HasCompactionTriggerInInput(body) {
		kind = "native_compact"
	}
	if generate := gjson.GetBytes(body, "generate"); generate.Exists() && generate.Type == gjson.False {
		kind = "prewarm"
	}
	opaque := state != "" || conflict
	gjson.GetBytes(body, "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("encrypted_content").String() != "" || item.Get("type").String() == "item_reference" {
			opaque = true
		}
		return true
	})
	ctx = context.WithValue(ctx, openAIConversationRequestKindKey{}, kind)
	return context.WithValue(ctx, openAIContinuationStateKey{}, opaque)
}

// BeginOpenAIConversationActivity 每个实际请求或 WS turn 单独获取在途保护，空闲连接不续租。
func (s *OpenAIGatewayService) BeginOpenAIConversationActivity(ctx context.Context, accountID int64, personaToken ...string) error {
	return s.beginOpenAIConversationActivity(ctx, accountID, nil, personaToken...)
}

func (s *OpenAIGatewayService) beginOpenAIConversationActivity(ctx context.Context, accountID int64, reservation *openAIPersonaUserReservationState, personaToken ...string) error {
	if s == nil {
		return nil
	}
	store, ok := s.accountRepo.(OpenAIConversationActivityStore)
	if !ok {
		return nil
	}
	attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
	if !found || attempt.conversation == nil {
		return nil
	}
	key := openAIUserAffinityRequestKey(ctx)
	if prior, ok := s.openaiAffinity.activities.Load(key); ok {
		activity := prior.(*openAIConversationActivity)
		if activity.bindingID == attempt.conversation.BindingID && activity.accountID == accountID && activity.generation == attempt.conversation.SlotGeneration {
			if reservation != nil {
				return ErrOpenAIUserAffinityReservationConflict
			}
			return nil
		}
		activity.stop()
	}
	token := uuid.NewString()
	acquired, err := store.AcquireOpenAIConversationActivity(ctx, *attempt.conversation, token, time.Now().UTC().Add(openAIConversationHoldTTL), personaToken...)
	if err != nil {
		return err
	}
	if !acquired {
		return ErrOpenAIConversationResetRequired
	}
	activity := &openAIConversationActivity{done: make(chan struct{}), bindingID: attempt.conversation.BindingID, accountID: accountID, generation: attempt.conversation.SlotGeneration, personaReservation: reservation, requestKind: OpenAIConversationRequestKind(ctx)}
	activity.stop = func() {
		activity.once.Do(func() {
			close(activity.done)
			s.openaiAffinity.activities.CompareAndDelete(key, activity)
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := store.ReleaseOpenAIConversationActivity(cleanup, token); err != nil {
				slog.Warn("openai.conversation_activity_release_failed", "binding_id", activity.bindingID, "error", err)
			}
			if reservation != nil {
				reservation.rollback()
			}
		})
	}
	if _, loaded := s.openaiAffinity.activities.LoadOrStore(key, activity); loaded {
		activity.stop()
		return ErrOpenAIUserAffinityReservationConflict
	}
	go func() {
		ticker := time.NewTicker(openAIConversationHoldTTL / 3)
		defer ticker.Stop()
		defer activity.stop()
		for {
			select {
			case <-activity.done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				renew, cancel := context.WithTimeout(ctx, 5*time.Second)
				alive, err := store.RenewOpenAIConversationActivity(renew, token, time.Now().UTC().Add(openAIConversationHoldTTL))
				cancel()
				if err != nil || !alive {
					slog.Warn("openai.conversation_activity_lost", "binding_id", activity.bindingID, "error", err)
					return
				}
			}
		}
	}()
	return nil
}

// ResumeOpenAIConversationActivity 每个 WS 回合回源验证原身份，并重新申请原 Persona 的容量。
func (s *OpenAIGatewayService) ResumeOpenAIConversationActivity(ctx context.Context, accountID int64) error {
	if s == nil {
		return nil
	}
	if _, guarded := s.accountRepo.(OpenAIConversationActivityStore); !guarded {
		return nil
	}
	if _, active := s.openaiAffinity.activities.Load(openAIUserAffinityRequestKey(ctx)); active {
		return ErrOpenAIUserAffinityReservationConflict
	}
	ref, hasRef := ctx.Value(openAIConversationReferenceKey{}).(OpenAIUserConversationTransition)
	if !hasRef {
		// 非 WS 的兼容调用仍使用本次请求态；生产 WS 在创建 hooks 前固定引用。
		attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
		if !found || attempt.conversation == nil {
			return openAIConversationRejection(ctx, ErrOpenAIConversationResetRequired, "ws_reference_missing", "")
		}
		return s.BeginOpenAIConversationActivity(ctx, accountID)
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	if ref.AccountID != accountID || ref.UserID != userID || ref.APIKeyID != apiKeyID {
		return openAIConversationRejection(ctx, ErrOpenAIConversationResetRequired, "ws_reference_mismatch", ref.ScopeKey)
	}
	store, ok := s.accountRepo.(OpenAIUserAffinityConversationStore)
	if !ok {
		return errors.New("conversation binding store unavailable")
	}
	binding, err := store.GetOpenAIUserConversationBinding(ctx, userID, apiKeyID, ref.ScopeKey, ref.ConversationHash)
	if err != nil {
		return err
	}
	if binding == nil || binding.ID != ref.BindingID || binding.AccountID != ref.AccountID ||
		binding.SlotGeneration != ref.SlotGeneration || binding.BindingEpoch != ref.BindingEpoch ||
		binding.AccountPersonaID != ref.AccountPersonaID || binding.PersonaSessionEpoch != ref.PersonaSessionEpoch ||
		binding.CredentialChainID != ref.CredentialChainID || binding.ProfileID != ref.ProfileID || binding.ProfileVersion != ref.ProfileVersion {
		return openAIConversationRejection(ctx, ErrOpenAIConversationResetRequired, "ws_identity_invalid", ref.ScopeKey)
	}
	valid, err := store.ValidateOpenAIUserConversationBinding(ctx, *binding)
	if err != nil {
		return err
	}
	if !valid {
		return openAIConversationRejection(ctx, ErrOpenAIConversationResetRequired, "ws_binding_invalid", ref.ScopeKey)
	}
	cfg, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
	if err != nil {
		return err
	}
	s.rememberOpenAIUserAffinityConversationAttempt(ctx, binding, cfg, "")
	if binding.AccountPersonaID <= 0 {
		return s.BeginOpenAIConversationActivity(ctx, accountID)
	}
	target, err := s.restoreOpenAIUserConversationExecutionTarget(ctx, binding)
	if err != nil {
		return s.continuationSelectionError(ctx, accountID, err)
	}
	account, err := s.getOpenAIUserAffinityResidentAccount(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil || account.Status != StatusActive || !account.Schedulable {
		return ErrOpenAIPreviousResponseAccountUnavailable
	}
	reservation, err := s.beginOpenAIPersonaUserReservation(ctx)
	if err != nil {
		return err
	}
	if reservation == nil {
		return errors.New("Persona reservation store unavailable")
	}
	selection := &AccountSelectionResult{Account: account, ExecutionTarget: &target}
	if err := s.attachBoundOpenAIPersonaReservation(ctx, selection, reservation, binding.RootClientSessionHash); err != nil {
		reservation.rollback()
		return s.continuationSelectionError(ctx, accountID, err)
	}
	if err := s.beginOpenAIConversationActivity(ctx, accountID, reservation, reservation.token); err != nil {
		reservation.rollback()
		return err
	}
	return nil
}

// commitOpenAIConversationPersonaReservation 提交后续回合独立获取的容量租约。
func (s *OpenAIGatewayService) commitOpenAIConversationPersonaReservation(ctx context.Context) {
	if s == nil {
		return
	}
	if value, ok := s.openaiAffinity.activities.Load(openAIUserAffinityRequestKey(ctx)); ok {
		if reservation := value.(*openAIConversationActivity).personaReservation; reservation != nil {
			reservation.commit()
		}
	}
}

func (s *OpenAIGatewayService) isOpenAIConversationPrewarm(ctx context.Context) bool {
	if s != nil {
		if value, ok := s.openaiAffinity.activities.Load(openAIUserAffinityRequestKey(ctx)); ok {
			return value.(*openAIConversationActivity).requestKind == "prewarm"
		}
	}
	return OpenAIConversationRequestKind(ctx) == "prewarm"
}

func (s *OpenAIGatewayService) endOpenAIConversationActivity(ctx context.Context) {
	if s == nil {
		return
	}
	if value, ok := s.openaiAffinity.activities.Load(openAIUserAffinityRequestKey(ctx)); ok {
		value.(*openAIConversationActivity).stop()
	}
}

func (s *OpenAIGatewayService) touchOpenAIConversationActivity(ctx context.Context, accountID int64) {
	store, ok := s.accountRepo.(OpenAIConversationActivityStore)
	if !ok {
		return
	}
	attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
	if !found || attempt.conversation == nil {
		return
	}
	if _, err := store.TouchOpenAIConversationActivity(ctx, *attempt.conversation, time.Now().UTC()); err != nil {
		slog.Warn("openai.conversation_activity_touch_failed", "binding_id", attempt.conversation.BindingID, "error", err)
	}
}
