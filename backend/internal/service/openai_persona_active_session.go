package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/google/uuid"
)

// OpenAIPersonaActiveSessionReservationState 描述一次原子 Session 占位的结果。
type OpenAIPersonaActiveSessionReservationState int

const (
	OpenAIPersonaActiveSessionRejected OpenAIPersonaActiveSessionReservationState = iota
	OpenAIPersonaActiveSessionPendingCreated
	OpenAIPersonaActiveSessionAlreadyActive
	OpenAIPersonaActiveSessionPendingJoined
)

// OpenAIPersonaActiveSessionCache 管理 Account×Persona 下的客户端 Session 活跃窗口。
// clientSessionHash 已按认证用户与 API Key 做安全摘要，缓存不得保存原始客户端 Session ID。
type OpenAIPersonaActiveSessionCache interface {
	ReserveOpenAIPersonaActiveSession(ctx context.Context, accountID int64, persona, clientSessionHash, reservationID string, maxSessions int, pendingTTL time.Duration) (OpenAIPersonaActiveSessionReservationState, error)
	CommitOpenAIPersonaActiveSession(ctx context.Context, accountID int64, persona, clientSessionHash string, activeTTL time.Duration) (bool, error)
	ReleaseOpenAIPersonaActiveSessionReservation(ctx context.Context, accountID int64, persona, clientSessionHash, reservationID string) error
}

type openAIPersonaActiveSessionReservation struct {
	mu                sync.Mutex
	cache             OpenAIPersonaActiveSessionCache
	accountID         int64
	persona           string
	clientSessionHash string
	reservationID     string
	activeTTL         time.Duration
	pending           bool
	accepted          bool
}

type openAIPersonaActiveSessionReservationContextKey struct{}

func openAIPersonaActiveSessionReservationFromContext(ctx context.Context) *openAIPersonaActiveSessionReservation {
	if ctx == nil {
		return nil
	}
	reservation, _ := ctx.Value(openAIPersonaActiveSessionReservationContextKey{}).(*openAIPersonaActiveSessionReservation)
	return reservation
}

// OpenAIPersonaClientSessionHash 将选号使用的客户端 Session 信号绑定到认证主体。
// 不能复用 Codex 出站 fingerprint scope：后者表示账号侧 Session 槽位，可能合并多个客户端。
func OpenAIPersonaClientSessionHash(ctx context.Context, sessionHash string) (string, error) {
	if ctx == nil {
		return "", errors.New("OpenAI Persona client Session identity is unavailable")
	}
	sessionHash = strings.TrimSpace(sessionHash)
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	if userID <= 0 || apiKeyID <= 0 || sessionHash == "" {
		return "", errors.New("OpenAI Persona client Session identity is unavailable")
	}
	return openAIUserAffinityScopedStateHash(
		userID, apiKeyID, "openai:persona-active-session:v1", "session_hash", sessionHash,
	), nil
}

// ReserveOpenAIPersonaActiveSession 在进入账号排队和上游连接前原子占用客户端 Session 名额。
func (s *OpenAIGatewayService) ReserveOpenAIPersonaActiveSession(
	ctx context.Context,
	account *Account,
	binding SessionPersonaSlotBinding,
	clientSessionHash string,
) (context.Context, func(), bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || account == nil || account.ID <= 0 || !binding.Valid() {
		return ctx, nil, false, errors.New("invalid OpenAI Persona active Session reservation")
	}
	if binding.AccountID != 0 && binding.AccountID != account.ID {
		return ctx, nil, false, errors.New("OpenAI Persona active Session account scope mismatch")
	}
	clientSessionHash = strings.ToLower(strings.TrimSpace(clientSessionHash))
	if len(clientSessionHash) != 64 {
		return ctx, nil, false, errors.New("OpenAI Persona active Session scope is unavailable")
	}
	cache, ok := s.cache.(OpenAIPersonaActiveSessionCache)
	if !ok || cache == nil {
		return ctx, nil, false, errors.New("OpenAI Persona active Session cache is unavailable")
	}
	policy := s.EffectiveOpenAIPersonaAdmissionPolicy(ctx, account, binding)
	if policy.MaxActiveClientSessions <= 0 {
		return ctx, nil, false, errors.New("OpenAI Persona active Session limit is invalid")
	}

	activeTTL := time.Hour
	if s.settingService != nil {
		cfg, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
		if err != nil {
			return ctx, nil, false, err
		}
		activeTTL = cfg.ConversationActiveTTL()
	}
	reservationID := uuid.NewString()
	state, err := cache.ReserveOpenAIPersonaActiveSession(
		ctx, account.ID, string(binding.PersonaID), clientSessionHash, reservationID,
		policy.MaxActiveClientSessions, activeTTL,
	)
	if err != nil {
		return ctx, nil, false, err
	}
	if state == OpenAIPersonaActiveSessionRejected {
		return ctx, nil, false, nil
	}
	reservation := &openAIPersonaActiveSessionReservation{
		cache: cache, accountID: account.ID, persona: string(binding.PersonaID),
		clientSessionHash: clientSessionHash, reservationID: reservationID, activeTTL: activeTTL,
		pending: state == OpenAIPersonaActiveSessionPendingCreated || state == OpenAIPersonaActiveSessionPendingJoined,
	}
	boundCtx := context.WithValue(ctx, openAIPersonaActiveSessionReservationContextKey{}, reservation)
	release := func() { reservation.releasePending() }
	return boundCtx, release, true, nil
}

func (r *openAIPersonaActiveSessionReservation) operationContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func (r *openAIPersonaActiveSessionReservation) commit() {
	if r == nil || r.cache == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accepted = true
	ctx, cancel := r.operationContext()
	defer cancel()
	committed, err := r.cache.CommitOpenAIPersonaActiveSession(ctx, r.accountID, r.persona, r.clientSessionHash, r.activeTTL)
	if err != nil {
		slog.Warn("openai.persona_active_session_commit_failed", "account_id", r.accountID, "persona", r.persona, "error", err)
		return
	}
	if committed {
		r.pending = false
	}
}

func (r *openAIPersonaActiveSessionReservation) releasePending() {
	if r == nil || r.cache == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.pending || r.accepted {
		return
	}
	ctx, cancel := r.operationContext()
	defer cancel()
	if err := r.cache.ReleaseOpenAIPersonaActiveSessionReservation(ctx, r.accountID, r.persona, r.clientSessionHash, r.reservationID); err != nil {
		slog.Warn("openai.persona_active_session_release_failed", "account_id", r.accountID, "persona", r.persona, "error", err)
		return
	}
	r.pending = false
}

func commitOpenAIPersonaActiveSession(ctx context.Context, accountID int64) {
	if dynamic := dynamicOpenAIClientSessionReservationFromContext(ctx); dynamic != nil {
		dynamic.commit()
	}
	reservation := openAIPersonaActiveSessionReservationFromContext(ctx)
	if reservation == nil || reservation.accountID != accountID {
		return
	}
	reservation.commit()
}

func releaseOpenAIPersonaActiveSessionReservation(ctx context.Context, accountID int64) {
	if dynamic := dynamicOpenAIClientSessionReservationFromContext(ctx); dynamic != nil {
		dynamic.rollback()
	}
	reservation := openAIPersonaActiveSessionReservationFromContext(ctx)
	if reservation == nil || reservation.accountID != accountID {
		return
	}
	reservation.releasePending()
}
