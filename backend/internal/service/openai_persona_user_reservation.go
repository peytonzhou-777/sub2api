package service

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	openaiidentity "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

var (
	ErrOpenAIPersonaUserCapacity    = errors.New("OpenAI AccountPersona active user capacity exhausted")
	ErrOpenAIPersonaUserReservation = errors.New("OpenAI Persona user reservation is missing or expired")
)

// OpenAIPersonaUserReserveInput 预留一个 Persona x User 名额，API Key 与客户端 Session 不参与容量身份。
type OpenAIPersonaUserReserveInput struct {
	ReservationToken string
	AccountID        int64
	AccountPersonaID int64
	UserID           int64
	MaxUsers         int
	Now              time.Time
	HoldUntil        time.Time
	ExistingThread   bool
}

type OpenAIPersonaUserLeaseReservation struct {
	ReservationToken string
	LeaseID          int64
	Created          bool
	AlreadyActive    bool
}

type OpenAIPersonaUserReservationCommit struct {
	ReservationToken string
	Now              time.Time
	ActiveUntil      time.Time
}

// OpenAIPersonaCapacityCandidate 是数据库权威的 Persona 用户容量快照。
type OpenAIPersonaCapacityCandidate struct {
	Persona           OpenAIAccountPersona
	Session           OpenAIAccountPersonaSession
	ActiveUsers       int
	EarliestReleaseAt *time.Time
	UserAlreadyActive bool
}

// OpenAIPersonaUserReservationRepository 管理 Persona x User 活跃占用。
type OpenAIPersonaUserReservationRepository interface {
	ReservePersonaUser(context.Context, OpenAIPersonaUserReserveInput) (*OpenAIPersonaUserLeaseReservation, error)
	CommitPersonaUserReservation(context.Context, OpenAIPersonaUserReservationCommit) (OpenAIExecutionTarget, error)
	RollbackPersonaUserReservation(context.Context, string, time.Time) error
	ListOpenAIPersonaCapacityCandidates(context.Context, []int64, int64, time.Time) ([]OpenAIPersonaCapacityCandidate, error)
}

type openAIPersonaUserReservationContextKey struct{}
type openAIInboundPersonaPreferenceContextKey struct{}
type openAIAttemptExclusionsContextKey struct{}
type openAIUserOccupiedPersonaAccountsContextKey struct{}

// OpenAIAttemptExclusions 区分账号级故障与 Persona 级故障。
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

// ScopeOpenAIFailoverToPersona 将独立 OAuth 凭据故障限定到单个 Persona。
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

// ContextWithOpenAIInboundPersonaPreference 记录客户端族，仅影响未绑定新根的候选排序。
func ContextWithOpenAIInboundPersonaPreference(ctx context.Context, profile SessionPersonaID) context.Context {
	if ctx == nil || (profile != SessionPersonaCodexCLIStrict && profile != SessionPersonaOpenCode) {
		return ctx
	}
	return context.WithValue(ctx, openAIInboundPersonaPreferenceContextKey{}, profile)
}

// ContextWithOpenAIInboundPersonaPreferenceFromHeaders 根据请求头记录新根 Persona 偏好。
func ContextWithOpenAIInboundPersonaPreferenceFromHeaders(ctx context.Context, userAgent, originator string) context.Context {
	profile := SessionPersonaOpenCode
	if openaiidentity.IsCodexOfficialClientByHeaders(userAgent, originator) {
		profile = SessionPersonaCodexCLIStrict
	}
	return ContextWithOpenAIInboundPersonaPreference(ctx, profile)
}

func contextWithOpenAIUserOccupiedPersonaAccounts(ctx context.Context, accounts map[int64]struct{}) context.Context {
	if ctx == nil || len(accounts) == 0 {
		return ctx
	}
	return context.WithValue(ctx, openAIUserOccupiedPersonaAccountsContextKey{}, cloneOpenAIExclusionIDs(accounts))
}

func openAIUserOccupiedPersonaAccounts(ctx context.Context) map[int64]struct{} {
	if ctx == nil {
		return nil
	}
	accounts, _ := ctx.Value(openAIUserOccupiedPersonaAccountsContextKey{}).(map[int64]struct{})
	return accounts
}

type openAIPersonaUserReservationState struct {
	mu             sync.Mutex
	repo           OpenAIPersonaUserReservationRepository
	token          string
	personaLeaseID int64
	activeTTL      time.Duration
	committed      bool
	rolledBack     bool
}

// beginOpenAIPersonaUserReservation 只创建请求令牌，选号后再预留 Persona 用户名额。
func (s *OpenAIGatewayService) beginOpenAIPersonaUserReservation(ctx context.Context) (*openAIPersonaUserReservationState, error) {
	if s == nil || s.personaUserReservationRepo == nil {
		return nil, nil
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	if userID <= 0 {
		return nil, errors.New("OpenAI Persona user reservation scope is unavailable")
	}
	activeTTL := time.Hour
	if s.settingService != nil {
		affinityCfg, err := s.settingService.GetOpenAIUserAffinityConfig(ctx)
		if err != nil {
			return nil, err
		}
		activeTTL = affinityCfg.ConversationActiveTTL()
	}
	return &openAIPersonaUserReservationState{
		repo: s.personaUserReservationRepo, token: uuid.NewString(), activeTTL: activeTTL,
	}, nil
}

func openAIDiagnosticClientSessionHash(ctx context.Context, sessionHash string) string {
	hash, err := OpenAIPersonaClientSessionHash(ctx, sessionHash)
	if err != nil {
		return ""
	}
	return hash
}

func (s *OpenAIGatewayService) attachOpenAIPersonaReservation(ctx context.Context, selection *AccountSelectionResult, state *openAIPersonaUserReservationState, sessionHash string) error {
	if selection == nil || selection.Account == nil || state == nil || state.repo == nil {
		return ErrOpenAIPersonaUserCapacity
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	rootClientSessionHash := openAIDiagnosticClientSessionHash(ctx, sessionHash)
	if selection.ExecutionTarget != nil && selection.ExecutionTarget.Valid() {
		return s.attachBoundOpenAIPersonaReservation(ctx, selection, state, rootClientSessionHash)
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
		if candidates[i].UserAlreadyActive != candidates[j].UserAlreadyActive {
			return candidates[i].UserAlreadyActive
		}
		leftPreferred := candidates[i].Persona.ProfileID == preferred
		rightPreferred := candidates[j].Persona.ProfileID == preferred
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		if !candidates[i].UserAlreadyActive && candidates[i].ActiveUsers != candidates[j].ActiveUsers {
			return candidates[i].ActiveUsers < candidates[j].ActiveUsers
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
		maxUsers := cfg.ForPersona(candidate.Persona.ProfileID).DefaultMaxActiveUsersPerPersona
		if candidate.Persona.MaxActiveUsersOverride != nil {
			maxUsers = *candidate.Persona.MaxActiveUsersOverride
		}
		now := time.Now().UTC()
		holdUntil := now.Add(time.Duration(cfg.MaxWaitSeconds+60) * time.Second)
		reserved, reserveErr := state.repo.ReservePersonaUser(ctx, OpenAIPersonaUserReserveInput{
			ReservationToken: state.token, AccountID: selection.Account.ID, AccountPersonaID: candidate.Persona.ID,
			UserID: userID, MaxUsers: maxUsers, Now: now, HoldUntil: holdUntil,
		})
		if errors.Is(reserveErr, ErrOpenAIPersonaUserCapacity) {
			continue
		}
		if reserveErr != nil {
			return reserveErr
		}
		target, buildErr := OpenAIExecutionTargetFromPersonaSession(candidate.Persona, candidate.Session)
		if buildErr != nil {
			return buildErr
		}
		target.PersonaUserLeaseID = reserved.LeaseID
		target.ReservationToken = state.token
		state.personaLeaseID = reserved.LeaseID
		selection.ExecutionTarget = &target
		selection.attachOpenAIPersonaUserReservation(state)
		if bindErr := s.bindOpenAIUserAffinityConversationExecutionTarget(ctx, selection.Account.ID, target, rootClientSessionHash); bindErr != nil {
			state.rollback()
			return bindErr
		}
		return nil
	}
	return ErrOpenAIPersonaUserCapacity
}

// attachBoundOpenAIPersonaReservation 为 continuation 续租已经固化的精确 Persona。
func (s *OpenAIGatewayService) attachBoundOpenAIPersonaReservation(
	ctx context.Context,
	selection *AccountSelectionResult,
	state *openAIPersonaUserReservationState,
	rootClientSessionHash string,
) error {
	target := *selection.ExecutionTarget
	if target.AccountID != selection.Account.ID || s.accountPersonaRepo == nil {
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
	maxUsers := cfg.ForPersona(persona.ProfileID).DefaultMaxActiveUsersPerPersona
	if persona.MaxActiveUsersOverride != nil {
		maxUsers = *persona.MaxActiveUsersOverride
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	now := time.Now().UTC()
	holdUntil := now.Add(time.Duration(cfg.MaxWaitSeconds+60) * time.Second)
	reserved, err := state.repo.ReservePersonaUser(ctx, OpenAIPersonaUserReserveInput{
		ReservationToken: state.token, AccountID: target.AccountID, AccountPersonaID: target.AccountPersonaID,
		UserID: userID, MaxUsers: maxUsers, Now: now, HoldUntil: holdUntil, ExistingThread: true,
	})
	if err != nil {
		return err
	}
	target.PersonaUserLeaseID = reserved.LeaseID
	target.ReservationToken = state.token
	state.personaLeaseID = reserved.LeaseID
	selection.ExecutionTarget = &target
	selection.attachOpenAIPersonaUserReservation(state)
	if bindErr := s.bindOpenAIUserAffinityConversationExecutionTarget(ctx, selection.Account.ID, target, rootClientSessionHash); bindErr != nil {
		state.rollback()
		return bindErr
	}
	return nil
}

// attachOpenAIPersonaUserReservation 独立交接 Persona 用户占用与账号并发 permit。
func (r *AccountSelectionResult) attachOpenAIPersonaUserReservation(state *openAIPersonaUserReservationState) {
	if r == nil || state == nil {
		return
	}
	if !r.accountPermitReleaseCaptured {
		originalRelease := r.ReleaseFunc
		if originalRelease != nil {
			var releaseOnce sync.Once
			r.accountPermitReleaseFunc = func() { releaseOnce.Do(originalRelease) }
		}
		r.accountPermitReleaseCaptured = true
	}
	r.personaUserReservation = state
	r.ReleaseFunc = func() {
		r.ReleaseAccountPermit()
		state.rollback()
	}
}

// openAIAccountsWithPersonaCapacity 返回可用账号，以及当前用户已有活跃 Persona 占用的账号。
func (s *OpenAIGatewayService) openAIAccountsWithPersonaCapacity(ctx context.Context, groupID *int64, excluded map[int64]struct{}) (map[int64]struct{}, map[int64]struct{}, error) {
	accounts, err := s.listSchedulableAccounts(ctx, groupID, PlatformOpenAI)
	if err != nil {
		return nil, nil, err
	}
	accountIDs := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if _, skip := excluded[account.ID]; !skip {
			accountIDs = append(accountIDs, account.ID)
		}
	}
	userID, _ := ctx.Value(ctxkey.UserID).(int64)
	candidates, err := s.personaUserReservationRepo.ListOpenAIPersonaCapacityCandidates(ctx, accountIDs, userID, time.Now().UTC())
	if err != nil {
		return nil, nil, err
	}
	cfg, err := s.settingService.GetOpenAIAccountAdmissionConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	available := make(map[int64]struct{})
	occupied := make(map[int64]struct{})
	for _, candidate := range candidates {
		if _, skip := OpenAIAttemptExclusionsFromContext(ctx).AccountPersonaIDs[candidate.Persona.ID]; skip {
			continue
		}
		limit := cfg.ForPersona(candidate.Persona.ProfileID).DefaultMaxActiveUsersPerPersona
		if candidate.Persona.MaxActiveUsersOverride != nil {
			limit = *candidate.Persona.MaxActiveUsersOverride
		}
		if candidate.UserAlreadyActive {
			occupied[candidate.Persona.AccountID] = struct{}{}
		}
		if candidate.UserAlreadyActive || candidate.ActiveUsers < limit {
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
		return result, occupied, ErrOpenAIPersonaUserCapacity
	}
	return result, occupied, nil
}

func (s *openAIPersonaUserReservationState) commit() {
	if s == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.committed || s.rolledBack {
		return
	}
	now := time.Now().UTC()
	if _, err := s.repo.CommitPersonaUserReservation(context.Background(), OpenAIPersonaUserReservationCommit{
		ReservationToken: s.token, Now: now, ActiveUntil: now.Add(s.activeTTL),
	}); err != nil {
		slog.Error("openai.persona_user_reservation_commit_failed", "error", err)
		return
	}
	s.committed = true
}

func (s *openAIPersonaUserReservationState) rollback() {
	if s == nil || s.repo == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.committed || s.rolledBack {
		return
	}
	if err := s.repo.RollbackPersonaUserReservation(context.Background(), s.token, time.Now().UTC()); err != nil {
		slog.Warn("openai.persona_user_reservation_rollback_failed", "error", err)
		return
	}
	s.rolledBack = true
}

func dynamicOpenAIPersonaUserReservationFromContext(ctx context.Context) *openAIPersonaUserReservationState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(openAIPersonaUserReservationContextKey{}).(*openAIPersonaUserReservationState)
	return state
}
