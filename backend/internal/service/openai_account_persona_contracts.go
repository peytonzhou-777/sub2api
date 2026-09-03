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
	StartedAt         time.Time
	LastActiveAt      *time.Time
	DrainingStartedAt *time.Time
	ExpiresAt         *time.Time
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
}

type OpenAIAccountPersonaAuthorization struct {
	AccountID          int64
	AccountPersonaID   int64
	ExpectedRowVersion int64
	PersonaGeneration  int64
	CredentialChainID  string
	EncryptedPayload   json.RawMessage
	ChatGPTAccountID   string
	OAuthClientID      string
	InstallationID     string
	UpstreamSessionID  string
}

type OpenAIAccountPersonaCredentialUpdate struct {
	AccountPersonaID  int64
	CredentialChainID string
	EncryptedPayload  json.RawMessage
	ChatGPTAccountID  string
	InstallationID    string
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
}

// OpenAIExecutionTarget 是选定账号后贯穿 HTTP、WS、compact 与 OAuth 的完整身份边界。
type OpenAIExecutionTarget struct {
	AccountID         int64
	AccountPersonaID  int64
	PersonaGeneration int64
	SessionEpoch      int64
	CredentialChainID string
	ProfileID         SessionPersonaID
	ProfileVersion    string
	InstallationID    string
	UpstreamSessionID string
	EffectiveProxyID  *int64
	ProxyRevision     int64
	UserGroupLeaseID  int64
	PersonaLeaseID    int64
	ReservationToken  string
}

func (t OpenAIExecutionTarget) Valid() bool {
	return t.AccountID > 0 && t.AccountPersonaID > 0 && t.PersonaGeneration > 0 &&
		t.SessionEpoch > 0 && t.CredentialChainID != "" && t.ProfileID != "" &&
		t.ProfileVersion != "" && t.InstallationID != "" && t.UpstreamSessionID != ""
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
