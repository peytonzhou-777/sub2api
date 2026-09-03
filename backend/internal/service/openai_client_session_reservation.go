package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	openaiidentity "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

var (
	// ErrOpenAIUserGroupSessionCapacity 表示用户在有效分组内已占满客户端 Session 总席位。
	ErrOpenAIUserGroupSessionCapacity = errors.New("拒绝下游分发")
	ErrOpenAIPersonaSessionCapacity   = errors.New("OpenAI AccountPersona client Session capacity exhausted")
	ErrOpenAIAccountPersonaClaim      = errors.New("OpenAI account is already claimed by another Persona for this user")
	ErrOpenAIClientSessionReservation = errors.New("OpenAI client Session reservation is missing or expired")
)

// OpenAIUserGroupSessionReserveInput 是选号前的全局 Session 总门预留。
type OpenAIUserGroupSessionReserveInput struct {
	ReservationToken  string
	UserID            int64
	EffectiveGroupID  int64
	ClientSessionHash string
	MaxSessions       int
	Now               time.Time
	HoldUntil         time.Time
}

// OpenAIPersonaSessionReserveInput 是候选 Persona 的客户端 Session 席位预留。
type OpenAIPersonaSessionReserveInput struct {
	ReservationToken  string
	AccountID         int64
	AccountPersonaID  int64
	UserID            int64
	APIKeyID          int64
	ClientSessionHash string
	MaxSessions       int
	Now               time.Time
	HoldUntil         time.Time
}

type OpenAIClientSessionLeaseReservation struct {
	ReservationToken string
	LeaseID          int64
	Created          bool
	AlreadyActive    bool
}

type OpenAIClientSessionReservationCommit struct {
	ReservationToken string
	Now              time.Time
	ActiveUntil      time.Time
}

// OpenAIPersonaCapacityCandidate 是不含 OAuth Token 的数据库权威候选快照。
type OpenAIPersonaCapacityCandidate struct {
	Persona              OpenAIAccountPersona
	Session              OpenAIAccountPersonaSession
	ActiveClientSessions int
	EarliestReleaseAt    *time.Time
	ClaimedByUser        bool
}

// OpenAIClientSessionReservationRepository 管理串联总门与 Persona 的两段短事务。
type OpenAIClientSessionReservationRepository interface {
	ReserveUserGroupSession(context.Context, OpenAIUserGroupSessionReserveInput) (*OpenAIClientSessionLeaseReservation, error)
	ReservePersonaSession(context.Context, OpenAIPersonaSessionReserveInput) (*OpenAIClientSessionLeaseReservation, error)
	CommitClientSessionReservation(context.Context, OpenAIClientSessionReservationCommit) (OpenAIExecutionTarget, error)
	RollbackClientSessionReservation(context.Context, string, time.Time) error
	ListOpenAIPersonaCapacityCandidates(context.Context, []int64, int64, time.Time) ([]OpenAIPersonaCapacityCandidate, error)
}

type openAIClientSessionReservationContextKey struct{}
type openAIInboundPersonaPreferenceContextKey struct{}

// ContextWithOpenAIInboundPersonaPreference 记录入站客户端族，只影响未绑定新根的候选排序。
func ContextWithOpenAIInboundPersonaPreference(ctx context.Context, profile SessionPersonaID) context.Context {
	if ctx == nil || (profile != SessionPersonaCodexCLIStrict && profile != SessionPersonaOpenCode) {
		return ctx
	}
	return context.WithValue(ctx, openAIInboundPersonaPreferenceContextKey{}, profile)
}

// ContextWithOpenAIInboundPersonaPreferenceFromHeaders 将官方 Codex 与其他客户端映射为新根 Persona 偏好。
func ContextWithOpenAIInboundPersonaPreferenceFromHeaders(ctx context.Context, userAgent, originator string) context.Context {
	profile := SessionPersonaOpenCode
	if openaiidentity.IsCodexOfficialClientByHeaders(userAgent, originator) {
		profile = SessionPersonaCodexCLIStrict
	}
	return ContextWithOpenAIInboundPersonaPreference(ctx, profile)
}

type openAIClientSessionReservationState struct {
	mu             sync.Mutex
	repo           OpenAIClientSessionReservationRepository
	token          string
	userLeaseID    int64
	personaLeaseID int64
	activeTTL      time.Duration
	committed      bool
	rolledBack     bool
}

func openAIUserGroupClientSessionHash(ctx context.Context, sessionHash string) (string, error) {
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	sessionHash = strings.TrimSpace(sessionHash)
	if userID <= 0 || sessionHash == "" {
		return "", errors.New("OpenAI User x Group client Session identity is unavailable")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("v1\x00%d\x00openai:user-group-session\x00%s", userID, sessionHash)))
	return hex.EncodeToString(sum[:]), nil
}

func (s *OpenAIGatewayService) beginOpenAIClientSessionReservation(ctx context.Context, groupID *int64, sessionHash string) (*openAIClientSessionReservationState, error) {
	if s == nil || s.clientSessionReservationRepo == nil {
		return nil, nil
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	if userID <= 0 || apiKeyID <= 0 || groupID == nil || *groupID <= 0 {
		return nil, errors.New("OpenAI client Session reservation scope is unavailable")
	}
	clientHash, err := openAIUserGroupClientSessionHash(ctx, sessionHash)
	if err != nil {
		return nil, err
	}
	cfg, err := s.settingService.GetOpenAIAccountAdmissionConfig(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	holdTTL := time.Duration(cfg.MaxWaitSeconds+60) * time.Second
	if holdTTL < 2*time.Minute {
		holdTTL = 2 * time.Minute
	}
	token := uuid.NewString()
	reserved, err := s.clientSessionReservationRepo.ReserveUserGroupSession(ctx, OpenAIUserGroupSessionReserveInput{
		ReservationToken: token, UserID: userID, EffectiveGroupID: *groupID,
		ClientSessionHash: clientHash, MaxSessions: cfg.MaxActiveClientSessionsPerUserGroup,
		Now: now, HoldUntil: now.Add(holdTTL),
	})
	if err != nil {
		return nil, err
	}
	activeTTL := time.Hour
	if s.settingService != nil {
		if affinityCfg, loadErr := s.settingService.GetOpenAIUserAffinityConfig(ctx); loadErr != nil {
			_ = s.clientSessionReservationRepo.RollbackClientSessionReservation(context.Background(), token, time.Now().UTC())
			return nil, loadErr
		} else {
			activeTTL = affinityCfg.ConversationActiveTTL()
		}
	}
	return &openAIClientSessionReservationState{repo: s.clientSessionReservationRepo, token: token, userLeaseID: reserved.LeaseID, activeTTL: activeTTL}, nil
}

func (s *OpenAIGatewayService) attachOpenAIPersonaReservation(ctx context.Context, selection *AccountSelectionResult, state *openAIClientSessionReservationState, sessionHash string) error {
	if selection == nil || selection.Account == nil || state == nil || state.repo == nil {
		return ErrOpenAIPersonaSessionCapacity
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	clientHash, err := OpenAIPersonaClientSessionHash(ctx, sessionHash)
	if err != nil {
		return err
	}
	candidates, err := state.repo.ListOpenAIPersonaCapacityCandidates(ctx, []int64{selection.Account.ID}, userID, time.Now().UTC())
	if err != nil {
		return err
	}
	preferred, _ := ctx.Value(openAIInboundPersonaPreferenceContextKey{}).(SessionPersonaID)
	if preferred == "" {
		preferred = SessionPersonaCodexCLIStrict
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftPreferred := candidates[i].Persona.ProfileID == preferred
		rightPreferred := candidates[j].Persona.ProfileID == preferred
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		if candidates[i].Persona.Position != candidates[j].Persona.Position {
			return candidates[i].Persona.Position < candidates[j].Persona.Position
		}
		return candidates[i].Persona.ID < candidates[j].Persona.ID
	})
	cfg, err := s.settingService.GetOpenAIAccountAdmissionConfig(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		maxSessions := cfg.ForPersona(candidate.Persona.ProfileID).MaxActiveClientSessions
		if candidate.Persona.MaxActiveClientSessionsOverride != nil {
			maxSessions = *candidate.Persona.MaxActiveClientSessionsOverride
		}
		now := time.Now().UTC()
		holdUntil := now.Add(time.Duration(cfg.MaxWaitSeconds+60) * time.Second)
		reserved, reserveErr := state.repo.ReservePersonaSession(ctx, OpenAIPersonaSessionReserveInput{
			ReservationToken: state.token, AccountID: selection.Account.ID, AccountPersonaID: candidate.Persona.ID,
			UserID: userID, APIKeyID: apiKeyID, ClientSessionHash: clientHash, MaxSessions: maxSessions,
			Now: now, HoldUntil: holdUntil,
		})
		if errors.Is(reserveErr, ErrOpenAIPersonaSessionCapacity) || errors.Is(reserveErr, ErrOpenAIAccountPersonaClaim) {
			continue
		}
		if reserveErr != nil {
			return reserveErr
		}
		target, buildErr := OpenAIExecutionTargetFromPersonaSession(candidate.Persona, candidate.Session)
		if buildErr != nil {
			return buildErr
		}
		target.UserGroupLeaseID = state.userLeaseID
		target.PersonaLeaseID = reserved.LeaseID
		target.ReservationToken = state.token
		state.personaLeaseID = reserved.LeaseID
		selection.ExecutionTarget = &target
		selection.clientSessionReservation = state
		originalRelease := selection.ReleaseFunc
		selection.ReleaseFunc = func() {
			if originalRelease != nil {
				originalRelease()
			}
			state.rollback()
		}
		return nil
	}
	return ErrOpenAIPersonaSessionCapacity
}

func (s *openAIClientSessionReservationState) commit() {
	if s == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.committed || s.rolledBack {
		return
	}
	now := time.Now().UTC()
	if _, err := s.repo.CommitClientSessionReservation(context.Background(), OpenAIClientSessionReservationCommit{
		ReservationToken: s.token, Now: now, ActiveUntil: now.Add(s.activeTTL),
	}); err != nil {
		slog.Error("openai.client_session_reservation_commit_failed", "error", err)
		return
	}
	s.committed = true
}

func (s *openAIClientSessionReservationState) rollback() {
	if s == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.committed || s.rolledBack {
		return
	}
	if err := s.repo.RollbackClientSessionReservation(context.Background(), s.token, time.Now().UTC()); err != nil {
		slog.Warn("openai.client_session_reservation_rollback_failed", "error", err)
		return
	}
	s.rolledBack = true
}

func dynamicOpenAIClientSessionReservationFromContext(ctx context.Context) *openAIClientSessionReservationState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(openAIClientSessionReservationContextKey{}).(*openAIClientSessionReservationState)
	return state
}
