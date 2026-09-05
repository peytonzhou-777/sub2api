package service

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
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
}

func (e *OpenAIContinuationSelectionError) Error() string { return e.Cause.Error() }
func (e *OpenAIContinuationSelectionError) Unwrap() error { return e.Cause }

func (s *OpenAIGatewayService) continuationSelectionError(ctx context.Context, accountID int64, cause error) error {
	attempt, ok := s.openAIUserAffinityAttempt(ctx, accountID)
	if !ok || attempt.conversation == nil {
		return cause
	}
	tr := attempt.conversation
	return &OpenAIContinuationSelectionError{Cause: cause, BindingID: tr.BindingID, AccountID: accountID, AccountPersonaID: tr.AccountPersonaID, SessionEpoch: tr.PersonaSessionEpoch, Profile: string(tr.ProfileID), Source: "persona_reservation"}
}

type openAIConversationActivity struct {
	once                  sync.Once
	done                  chan struct{}
	bindingID             int64
	accountID, generation int64
	stop                  func()
}

const openAIConversationHoldTTL = 2 * time.Minute

type openAIContinuationStateKey struct{}

// ContextWithOpenAIContinuationState 保留不透明状态的存在性，禁止无绑定时作为新根选号。
func ContextWithOpenAIContinuationState(ctx context.Context, headers http.Header, body []byte) context.Context {
	state, conflict := extractOpenAITurnStateFromRequest(headers, body)
	return context.WithValue(ctx, openAIContinuationStateKey{}, state != "" || conflict)
}

// BeginOpenAIConversationActivity 每个实际请求或 WS turn 单独获取在途保护，空闲连接不续租。
func (s *OpenAIGatewayService) BeginOpenAIConversationActivity(ctx context.Context, accountID int64, personaToken ...string) error {
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
	activity := &openAIConversationActivity{done: make(chan struct{}), bindingID: attempt.conversation.BindingID, accountID: accountID, generation: attempt.conversation.SlotGeneration}
	activity.stop = func() {
		activity.once.Do(func() {
			close(activity.done)
			s.openaiAffinity.activities.CompareAndDelete(key, activity)
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := store.ReleaseOpenAIConversationActivity(cleanup, token); err != nil {
				slog.Warn("openai.conversation_activity_release_failed", "binding_id", activity.bindingID, "error", err)
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

// ResumeOpenAIConversationActivity 防止长连接空闲后因本地请求态被回收而跳过绑定检查。
func (s *OpenAIGatewayService) ResumeOpenAIConversationActivity(ctx context.Context, accountID int64) error {
	if s == nil {
		return nil
	}
	if _, guarded := s.accountRepo.(OpenAIConversationActivityStore); !guarded {
		return nil
	}
	attempt, found := s.openAIUserAffinityAttempt(ctx, accountID)
	if !found || attempt.conversation == nil {
		return ErrOpenAIConversationResetRequired
	}
	return s.BeginOpenAIConversationActivity(ctx, accountID)
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
