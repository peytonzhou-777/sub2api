package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type OpenAIAccountPersonaState string

const (
	OpenAIAccountPersonaStateDraft    OpenAIAccountPersonaState = "draft"
	OpenAIAccountPersonaStateActive   OpenAIAccountPersonaState = "active"
	OpenAIAccountPersonaStateDraining OpenAIAccountPersonaState = "draining"
	OpenAIAccountPersonaStateDisabled OpenAIAccountPersonaState = "disabled"
	OpenAIAccountPersonaStateRetired  OpenAIAccountPersonaState = "retired"
)

type OpenAICredentialOwner string

const (
	OpenAICredentialOwnerAccountPrimary     OpenAICredentialOwner = "account_primary"
	OpenAICredentialOwnerPersonaIndependent OpenAICredentialOwner = "persona_independent"
)

type OpenAIPersonaSessionState string

const (
	OpenAIPersonaSessionCurrent  OpenAIPersonaSessionState = "current"
	OpenAIPersonaSessionDraining OpenAIPersonaSessionState = "draining"
	OpenAIPersonaSessionExpired  OpenAIPersonaSessionState = "expired"
	OpenAIPersonaSessionRevoked  OpenAIPersonaSessionState = "revoked"
)

var (
	ErrOpenAIAccountPersonaNotFound         = errors.New("OpenAI AccountPersona not found")
	ErrOpenAIAccountPersonaCASConflict      = errors.New("OpenAI AccountPersona version changed")
	ErrOpenAIDefaultPersonaProtected        = errors.New("DEFAULT_PERSONA_PROTECTED")
	ErrOpenAIAccountPersonaIdentityMismatch = errors.New("OpenAI AccountPersona identity mismatch")
	ErrOpenAIAccountPersonaSessionNotFound  = errors.New("OpenAI AccountPersona Session not found")
	ErrOpenAIAccountPersonaSessionExpired   = errors.New("OpenAI AccountPersona Session expired")
	ErrOpenAIAccountPersonaSessionOccupied  = errors.New("OpenAI AccountPersona Session is occupied")
)

// OpenAIAccountPersona 是账号下稳定的应用/设备实例，Position 仅用于管理排序。
type OpenAIAccountPersona struct {
	ID                              int64
	AccountID                       int64
	Position                        int
	ProfileID                       SessionPersonaID
	ProfileVersion                  string
	CredentialOwner                 OpenAICredentialOwner
	State                           OpenAIAccountPersonaState
	Enabled                         bool
	PersonaGeneration               int64
	CurrentCredentialChainID        string
	CurrentSessionEpoch             int64
	DeviceSeed                      []byte
	InstallationID                  string
	ProxyID                         *int64
	MaxActiveClientSessionsOverride *int
	RowVersion                      int64
	CreatedAt                       time.Time
	UpdatedAt                       time.Time
	DrainingStartedAt               *time.Time
	DisabledAt                      *time.Time
	RetiredAt                       *time.Time
}

func (p OpenAIAccountPersona) IsDefaultProtected() bool {
	return p.Position == 0 && p.ProfileID == SessionPersonaCodexCLIStrict && p.CredentialOwner == OpenAICredentialOwnerAccountPrimary
}

func (p OpenAIAccountPersona) AcceptsNewRoot() bool {
	return p.ID > 0 && p.AccountID > 0 && p.Enabled && p.State == OpenAIAccountPersonaStateActive &&
		p.CurrentCredentialChainID != "" && p.CurrentSessionEpoch > 0
}

type OpenAIAccountPersonaSession struct {
	AccountPersonaID  int64
	SessionEpoch      int64
	UpstreamSessionID string
	State             OpenAIPersonaSessionState
	PersonaGeneration int64
	CredentialChainID string
	ProfileID         SessionPersonaID
	ProfileVersion    string
	EffectiveProxyID  *int64
	ProxyRevision     int64
	EffectiveProxyURL string `json:"-"`
	InstallationID    string
	ProxySnapshotSet  bool
	StartedAt         time.Time
	LastActiveAt      *time.Time
	DrainingStartedAt *time.Time
	ExpiresAt         *time.Time
}

// OpenAIAccountPersonaLeaseStats 是管理端可展示的脱敏占用摘要。
type OpenAIAccountPersonaLeaseStats struct {
	ActiveClientSessions int
	EarliestReleaseAt    *time.Time
}

// OpenAIAccountPersonaAdminView 聚合 Persona、OAuth、Session 与准入容量，
// 不包含 Token、原始 Session ID、device seed 或客户端 Session 标识。
type OpenAIAccountPersonaAdminView struct {
	Persona                        OpenAIAccountPersona
	CredentialState                string
	CredentialUpdatedAt            *time.Time
	CredentialExpiresAt            *time.Time
	SessionState                   OpenAIPersonaSessionState
	SessionStartedAt               *time.Time
	SessionLastActiveAt            *time.Time
	EffectiveProxyID               *int64
	ProxyInherited                 bool
	ActiveClientSessions           int
	EarliestClientSessionReleaseAt *time.Time
	EffectiveMaxClientSessions     int
	EffectiveMaxConcurrency        int
	EffectiveMaxWebSockets         int
}

type OpenAIAccountPersonaCreate struct {
	AccountID                       int64
	ProfileID                       SessionPersonaID
	ProfileVersion                  string
	ProxyID                         *int64
	MaxActiveClientSessionsOverride *int
	DeviceSeed                      []byte
	InstallationID                  string
}

type OpenAIAccountPersonaUpdate struct {
	AccountID                       int64
	AccountPersonaID                int64
	ExpectedRowVersion              int64
	Enabled                         *bool
	State                           *OpenAIAccountPersonaState
	ProxyConfigured                 bool
	ProxyID                         *int64
	MaxActiveSessionsConfigured     bool
	MaxActiveClientSessionsOverride *int
	NewUpstreamSessionID            string
	OldSessionExpiresAt             time.Time
}

type OpenAIAccountPersonaAuthorization struct {
	AccountID           int64
	AccountPersonaID    int64
	ExpectedRowVersion  int64
	PersonaGeneration   int64
	CredentialChainID   string
	EncryptedPayload    json.RawMessage
	ChatGPTAccountID    string
	OAuthClientID       string
	InstallationID      string
	UpstreamSessionID   string
	OldSessionExpiresAt time.Time
}

type OpenAIAccountPersonaCredentialUpdate struct {
	AccountPersonaID  int64
	CredentialChainID string
	EncryptedPayload  json.RawMessage
	ChatGPTAccountID  string
	InstallationID    string
}

// OpenAIAccountPersonaSessionPrepareInput 描述新根请求或管理员操作对 current epoch 的原子准备。
type OpenAIAccountPersonaSessionPrepareInput struct {
	AccountID          int64
	AccountPersonaID   int64
	ExpectedRowVersion int64
	Now                time.Time
	Policy             CodexFingerprintEpochPolicy
	NewUpstreamSession string
	Manual             bool
	Force              bool
}

type OpenAIAccountPersonaSessionPrepareResult struct {
	Persona OpenAIAccountPersona
	Session OpenAIAccountPersonaSession
	Rotated bool
}

// OpenAIPrimaryPersonaCreate 由账号首次 Codex OAuth 回调生成，并与账号同事务落库。
type OpenAIPrimaryPersonaCreate struct {
	ProfileVersion    string
	CredentialChainID string
	EncryptedPayload  json.RawMessage
	ChatGPTAccountID  string
	OAuthClientID     string
	DeviceSeed        []byte
	InstallationID    string
	UpstreamSessionID string
}

type OpenAIAccountPersonaRepository interface {
	ListAccountPersonas(ctx context.Context, accountID int64) ([]OpenAIAccountPersona, error)
	GetAccountPersona(ctx context.Context, accountID, accountPersonaID int64) (*OpenAIAccountPersona, error)
	CreateAccountPersona(ctx context.Context, input OpenAIAccountPersonaCreate) (*OpenAIAccountPersona, error)
	UpdateAccountPersona(ctx context.Context, input OpenAIAccountPersonaUpdate) (*OpenAIAccountPersona, error)
	RetireAccountPersona(ctx context.Context, accountID, accountPersonaID, expectedRowVersion int64) error
	AuthorizeAccountPersona(ctx context.Context, input OpenAIAccountPersonaAuthorization) (*OpenAIAccountPersona, error)
	RevokeAccountPersonaAuthorization(ctx context.Context, accountID, accountPersonaID, expectedRowVersion int64) ([]string, error)
	GetAccountPersonaCredential(ctx context.Context, accountPersonaID int64, credentialChainID string) (*OpenAIPersonaCredentialRecord, error)
	ClaimAccountPersonaCredentialRefresh(ctx context.Context, accountPersonaID int64, credentialChainID string, expectedVersion int64) (bool, error)
	CompareAndSwapAccountPersonaToken(ctx context.Context, input OpenAIAccountPersonaCredentialUpdate, expectedVersion int64) (bool, error)
	MarkAccountPersonaCredentialInvalid(ctx context.Context, accountPersonaID int64, credentialChainID string, expectedVersion int64, reason string) error
	PrepareAccountPersonaSession(ctx context.Context, input OpenAIAccountPersonaSessionPrepareInput) (*OpenAIAccountPersonaSessionPrepareResult, error)
	GetAccountPersonaSession(ctx context.Context, accountID, accountPersonaID, sessionEpoch int64, now time.Time) (*OpenAIAccountPersonaSession, error)
	TouchAccountPersonaSession(ctx context.Context, accountPersonaID, sessionEpoch int64, now time.Time) error
}

// OpenAIAccountPersonaBatchReader 为管理列表和号池快照提供批量读取，避免逐账号查询。
type OpenAIAccountPersonaBatchReader interface {
	ListAccountPersonasByAccountIDs(ctx context.Context, accountIDs []int64) (map[int64][]OpenAIAccountPersona, error)
}

// OpenAIAccountPersonaLeaseStatsReader 为管理端和容量聚合提供数据库权威占用摘要。
type OpenAIAccountPersonaLeaseStatsReader interface {
	ListAccountPersonaLeaseStats(ctx context.Context, accountID int64, now time.Time) (map[int64]OpenAIAccountPersonaLeaseStats, error)
}

// OpenAIExecutionTarget 是选定账号后贯穿 HTTP、WS、compact 与 OAuth 的完整身份边界。
type OpenAIExecutionTarget struct {
	AccountID         int64
	AccountPersonaID  int64
	PersonaGeneration int64
	SessionEpoch      int64
	SessionStartedAt  time.Time
	CredentialChainID string
	ProfileID         SessionPersonaID
	ProfileVersion    string
	DeviceSeed        []byte `json:"-"`
	InstallationID    string
	UpstreamSessionID string
	EffectiveProxyID  *int64
	ProxyRevision     int64
	EffectiveProxyURL string `json:"-"`
	UserGroupLeaseID  int64
	PersonaLeaseID    int64
	ReservationToken  string
}

func (t OpenAIExecutionTarget) Valid() bool {
	return t.AccountID > 0 && t.AccountPersonaID > 0 && t.PersonaGeneration > 0 &&
		t.SessionEpoch > 0 && !t.SessionStartedAt.IsZero() && len(t.DeviceSeed) >= 16 &&
		t.CredentialChainID != "" && t.ProfileID != "" &&
		t.ProfileVersion != "" && t.InstallationID != "" && t.UpstreamSessionID != ""
}

// OpenAIExecutionTargetFromPersonaSession 构造贯穿 HTTP/WS/compact 的不可变执行目标。
func OpenAIExecutionTargetFromPersonaSession(persona OpenAIAccountPersona, session OpenAIAccountPersonaSession) (OpenAIExecutionTarget, error) {
	if persona.ID <= 0 || persona.AccountID <= 0 || session.AccountPersonaID != persona.ID ||
		session.SessionEpoch <= 0 || session.PersonaGeneration <= 0 ||
		session.CredentialChainID == "" || session.ProfileID != persona.ProfileID ||
		session.ProfileVersion != persona.ProfileVersion || session.InstallationID == "" {
		return OpenAIExecutionTarget{}, ErrOpenAIAccountPersonaIdentityMismatch
	}
	target := OpenAIExecutionTarget{
		AccountID: persona.AccountID, AccountPersonaID: persona.ID,
		PersonaGeneration: session.PersonaGeneration, SessionEpoch: session.SessionEpoch,
		SessionStartedAt: session.StartedAt, DeviceSeed: append([]byte(nil), persona.DeviceSeed...),
		CredentialChainID: session.CredentialChainID, ProfileID: session.ProfileID,
		ProfileVersion: session.ProfileVersion, InstallationID: session.InstallationID,
		UpstreamSessionID: session.UpstreamSessionID, EffectiveProxyID: session.EffectiveProxyID,
		ProxyRevision: session.ProxyRevision, EffectiveProxyURL: session.EffectiveProxyURL,
	}
	if !target.Valid() {
		return OpenAIExecutionTarget{}, ErrOpenAIAccountPersonaIdentityMismatch
	}
	return target, nil
}

type openAIExecutionTargetContextKey struct{}

func ContextWithOpenAIExecutionTarget(ctx context.Context, target OpenAIExecutionTarget) context.Context {
	if ctx == nil || !target.Valid() {
		return ctx
	}
	return context.WithValue(ctx, openAIExecutionTargetContextKey{}, target)
}

func OpenAIExecutionTargetFromContext(ctx context.Context) (OpenAIExecutionTarget, bool) {
	if ctx == nil {
		return OpenAIExecutionTarget{}, false
	}
	target, ok := ctx.Value(openAIExecutionTargetContextKey{}).(OpenAIExecutionTarget)
	return target, ok && target.Valid()
}
