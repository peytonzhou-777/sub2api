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
	ErrOpenAIPersonaCapacityExhausted = errors.New("OPENAI_PERSONA_CAPACITY_EXHAUSTED")
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
	ExistingThread    bool
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
	CurrentClientLease   bool
}

// OpenAIClientSessionReservationRepository 管理串联总门与 Persona 的两段短事务。
type OpenAIClientSessionReservationRepository interface {
	ReserveUserGroupSession(context.Context, OpenAIUserGroupSessionReserveInput) (*OpenAIClientSessionLeaseReservation, error)
	ReservePersonaSession(context.Context, OpenAIPersonaSessionReserveInput) (*OpenAIClientSessionLeaseReservation, error)
	CommitClientSessionReservation(context.Context, OpenAIClientSessionReservationCommit) (OpenAIExecutionTarget, error)
	RollbackClientSessionReservation(context.Context, string, time.Time) error
	ListOpenAIPersonaCapacityCandidates(context.Context, []int64, int64, int64, string, time.Time) ([]OpenAIPersonaCapacityCandidate, error)
}

type openAIClientSessionReservationContextKey struct{}
type openAIInboundPersonaPreferenceContextKey struct{}
type openAIAttemptExclusionsContextKey struct{}

// OpenAIAttemptExclusions 区分账号级与具体 Persona 实例级重试排除。
type OpenAIAttemptExclusions struct {
	AccountIDs        map[int64]struct{}
	AccountPersonaIDs map[int64]struct{}
}

func OpenAIAttemptExclusionsFromContext(ctx context.Context) OpenAIAttemptExclusions {
	if ctx == nil {
		return OpenAIAttemptExclusions{}
	}
	value, _ := ctx.Value(openAIAttemptExclusionsContextKey{}).(OpenAIAttemptExclusions)
	return value
}

// ContextWithOpenAIExcludedAccountPersona 返回不可变的 Persona 排除快照。
func ContextWithOpenAIExcludedAccountPersona(ctx context.Context, accountPersonaID int64) context.Context {
	if ctx == nil || accountPersonaID <= 0 {
		return ctx
	}
	current := OpenAIAttemptExclusionsFromContext(ctx)
	next := OpenAIAttemptExclusions{
		AccountIDs:        cloneOpenAIExclusionIDs(current.AccountIDs),
		AccountPersonaIDs: cloneOpenAIExclusionIDs(current.AccountPersonaIDs),
	}
	if next.AccountPersonaIDs == nil {
		next.AccountPersonaIDs = make(map[int64]struct{})
	}
	next.AccountPersonaIDs[accountPersonaID] = struct{}{}
	return context.WithValue(ctx, openAIAttemptExclusionsContextKey{}, next)
}

func cloneOpenAIExclusionIDs(source map[int64]struct{}) map[int64]struct{} {
	if len(source) == 0 {
		return nil
	}
	result := make(map[int64]struct{}, len(source))
	for id := range source {
		result[id] = struct{}{}
	}
	return result
}

// ScopeOpenAIFailoverToPersona 将独立 OAuth credential 故障限定到当前 Persona。
func ScopeOpenAIFailoverToPersona(ctx context.Context, err *UpstreamFailoverError) (context.Context, bool) {
	if err == nil || !err.IsCredentialFailure() || err.Scope == GatewayFailureScopeProvider || err.LocalRequestFailure {
		return ctx, false
	}
	target, ok := OpenAIExecutionTargetFromContext(ctx)
	if !ok {
		return ctx, false
	}
	return ContextWithOpenAIExcludedAccountPersona(ctx, target.AccountPersonaID), true
}

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
	mu                sync.Mutex
	repo              OpenAIClientSessionReservationRepository
	token             string
	userLeaseID       int64
	personaLeaseID    int64
	clientSessionHash string
	activeTTL         time.Duration
	committed         bool
	rolledBack        bool
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
	state.clientSessionHash = clientHash
	if selection.ExecutionTarget != nil && selection.ExecutionTarget.Valid() {
		return s.attachBoundOpenAIPersonaReservation(ctx, selection, state, clientHash)
	}
	candidates, err := state.repo.ListOpenAIPersonaCapacityCandidates(ctx, []int64{selection.Account.ID}, userID, apiKeyID, clientHash, time.Now().UTC())
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
		if _, excluded := OpenAIAttemptExclusionsFromContext(ctx).AccountPersonaIDs[candidate.Persona.ID]; excluded {
			continue
		}
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
		selection.attachOpenAIClientSessionReservation(state)
		if bindErr := s.bindOpenAIUserAffinityConversationExecutionTarget(ctx, selection.Account.ID, target, clientHash); bindErr != nil {
			state.rollback()
			return bindErr
		}
		return nil
	}
	return ErrOpenAIPersonaSessionCapacity
}

// attachBoundOpenAIPersonaReservation 只为 continuation 已固化的 Persona 建立客户端占用，不重新选择身份。
func (s *OpenAIGatewayService) attachBoundOpenAIPersonaReservation(
	ctx context.Context,
	selection *AccountSelectionResult,
	state *openAIClientSessionReservationState,
	clientHash string,
) error {
	target := *selection.ExecutionTarget
	if target.AccountID != selection.Account.ID {
		return ErrOpenAIPreviousResponseAccountUnavailable
	}
	if s.accountPersonaRepo == nil {
		return ErrOpenAIPreviousResponseAccountUnavailable
	}
	persona, err := s.accountPersonaRepo.GetAccountPersona(ctx, target.AccountID, target.AccountPersonaID)
	if err != nil || persona == nil || persona.ProfileID != target.ProfileID || persona.ProfileVersion != target.ProfileVersion {
		return ErrOpenAIPreviousResponseAccountUnavailable
	}
	cfg, err := s.settingService.GetOpenAIAccountAdmissionConfig(ctx)
	if err != nil {
		return err
	}
	maxSessions := cfg.ForPersona(persona.ProfileID).MaxActiveClientSessions
	if persona.MaxActiveClientSessionsOverride != nil {
		maxSessions = *persona.MaxActiveClientSessionsOverride
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	now := time.Now().UTC()
	holdUntil := now.Add(time.Duration(cfg.MaxWaitSeconds+60) * time.Second)
	reserved, err := state.repo.ReservePersonaSession(ctx, OpenAIPersonaSessionReserveInput{
		ReservationToken: state.token, AccountID: target.AccountID, AccountPersonaID: target.AccountPersonaID,
		UserID: userID, APIKeyID: apiKeyID, ClientSessionHash: clientHash, MaxSessions: maxSessions,
		Now: now, HoldUntil: holdUntil, ExistingThread: true,
	})
	if err != nil {
		return err
	}
	target.UserGroupLeaseID = state.userLeaseID
	target.PersonaLeaseID = reserved.LeaseID
	target.ReservationToken = state.token
	state.personaLeaseID = reserved.LeaseID
	selection.ExecutionTarget = &target
	selection.attachOpenAIClientSessionReservation(state)
	// 父 Thread 派生的新子 Thread 会继承已固化的执行目标，但仍拥有自己的
	// provisional binding。必须在首次上游发送前把完整目标写入子 binding；
	// helper 的 provisional token 门禁保证普通 continuation 不会被改写。
	if bindErr := s.bindOpenAIUserAffinityConversationExecutionTarget(ctx, selection.Account.ID, target, clientHash); bindErr != nil {
		state.rollback()
		return bindErr
	}
	return nil
}

// attachOpenAIClientSessionReservation 将“放弃选号”保留为完整释放，同时冻结一个
// 仅用于统一准入交接的账号 permit 释放入口，避免排队前误回滚客户端 Session lease。
func (r *AccountSelectionResult) attachOpenAIClientSessionReservation(state *openAIClientSessionReservationState) {
	if r == nil || state == nil {
		return
	}
	if !r.accountPermitReleaseCaptured {
		originalRelease := r.ReleaseFunc
		if originalRelease != nil {
			var releaseOnce sync.Once
			r.accountPermitReleaseFunc = func() {
				releaseOnce.Do(originalRelease)
			}
		}
		r.accountPermitReleaseCaptured = true
	}
	r.clientSessionReservation = state
	r.ReleaseFunc = func() {
		r.ReleaseAccountPermit()
		state.rollback()
	}
}

// openAIAccountsWithPersonaCapacity 在常驻/BestFit 写状态前构造数据库权威的账号可用集合。
func (s *OpenAIGatewayService) openAIAccountsWithPersonaCapacity(ctx context.Context, groupID *int64, excluded map[int64]struct{}, sessionHash string) (map[int64]struct{}, error) {
	accounts, err := s.listSchedulableAccounts(ctx, groupID, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	accountIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if _, skip := excluded[account.ID]; !skip {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	apiKeyID, _ := ctx.Value(ctxkey.APIKeyID).(int64)
	clientHash, err := OpenAIPersonaClientSessionHash(ctx, sessionHash)
	if err != nil {
		return nil, err
	}
	candidates, err := s.clientSessionReservationRepo.ListOpenAIPersonaCapacityCandidates(ctx, accountIDs, userID, apiKeyID, clientHash, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	cfg, err := s.settingService.GetOpenAIAccountAdmissionConfig(ctx)
	if err != nil {
		return nil, err
	}
	available := make(map[int64]struct{})
	for _, candidate := range candidates {
		if _, excluded := OpenAIAttemptExclusionsFromContext(ctx).AccountPersonaIDs[candidate.Persona.ID]; excluded {
			continue
		}
		limit := cfg.ForPersona(candidate.Persona.ProfileID).MaxActiveClientSessions
		if candidate.Persona.MaxActiveClientSessionsOverride != nil {
			limit = *candidate.Persona.MaxActiveClientSessionsOverride
		}
		if candidate.CurrentClientLease || candidate.ActiveClientSessions < limit {
			available[candidate.Persona.AccountID] = struct{}{}
		}
	}
	result := cloneExcludedAccountIDs(excluded)
	if result == nil {
		result = make(map[int64]struct{})
	}
	for _, accountID := range accountIDs {
		if _, ok := available[accountID]; !ok {
			result[accountID] = struct{}{}
		}
	}
	if len(available) == 0 {
		return result, ErrOpenAIPersonaCapacityExhausted
	}
	return result, nil
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
