package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// SessionPersonaID identifies a client family at the upstream boundary.
// A persona is separate from an account, slot, and OAuth credential chain.
type SessionPersonaID string

const (
	// SessionPersonaCodexCLIStrict is the production CodexCLI 0.149.0 profile.
	SessionPersonaCodexCLIStrict SessionPersonaID = "codex_cli_strict"
	// SessionPersonaOpenCode is the OpenCode OpenAI OAuth profile.
	SessionPersonaOpenCode SessionPersonaID = "opencode"
)

// SessionPersonaTransport describes a concrete upstream transport.
type SessionPersonaTransport string

const (
	SessionPersonaTransportHTTP SessionPersonaTransport = "http"
	SessionPersonaTransportWS   SessionPersonaTransport = "websocket"

	// SessionPersonaTransportWebSocket is a readable alias for WS.
	SessionPersonaTransportWebSocket = SessionPersonaTransportWS
)

// SessionPersonaCompression describes the request-body compression policy
// owned by a persona. It prevents a later adapter from silently inheriting
// another persona's compression behavior.
type SessionPersonaCompression string

const (
	SessionPersonaCompressionNone SessionPersonaCompression = "none"
	SessionPersonaCompressionZstd SessionPersonaCompression = "zstd"
)

// SessionPersonaSlotState 是账号 Persona 槽位的内部生命周期。
// enabled 仍是对外兼容字段；内部状态用于区分排空和安全硬禁用。
type SessionPersonaSlotState string

const (
	SessionPersonaSlotStateActive   SessionPersonaSlotState = "active"
	SessionPersonaSlotStateDraining SessionPersonaSlotState = "draining"
	SessionPersonaSlotStateDisabled SessionPersonaSlotState = "disabled"
)

// SessionPersonaCredentialChain 描述一条独立 OAuth 授权链的非机密元数据。
// access/refresh token 不放入运行时 binding，也不应进入日志或审计事件。
type SessionPersonaCredentialChain struct {
	AccountID         int64            `json:"account_id"`
	PersonaID         SessionPersonaID `json:"persona_id"`
	SlotID            int              `json:"slot_id"`
	CredentialChainID string           `json:"credential_chain_id"`
	ChatGPTAccountID  string           `json:"chatgpt_account_id"`
	InstallationID    string           `json:"installation_id,omitempty"`
	Ready             bool             `json:"ready"`
	State             string           `json:"state,omitempty"`
	TokenVersion      int64            `json:"token_version,omitempty"`
}

// SessionPersonaScopeVersion identifies the scope mapping format. v1/v2 are
// legacy Codex-only scopes; v3 is the Persona-aware mapping for new roots.
const (
	SessionPersonaScopeVersionLegacyV1 = 1
	SessionPersonaScopeVersionLegacyV2 = 2
	SessionPersonaScopeVersionV3       = 3

	// DefaultSessionPersonaScopeVersion is used for new Persona-aware roots.
	DefaultSessionPersonaScopeVersion = SessionPersonaScopeVersionV3
	// DefaultSessionPersonaSlotCount is the production target: slot 0 Codex,
	// slot 1 OpenCode.
	DefaultSessionPersonaSlotCount = 2
	// LegacySessionPersonaSlotCount is the default for a missing v2 count.
	LegacySessionPersonaSlotCount = 2
	// LegacySingleSessionPersonaSlotCount preserves v1's historical one-slot
	// interpretation when the count is absent.
	LegacySingleSessionPersonaSlotCount = 1
	// MaxSessionPersonaSlotCount matches the existing compatibility boundary.
	MaxSessionPersonaSlotCount = 4
)

// SessionPersonaMappingKind describes why a slot resolved to a persona.
type SessionPersonaMappingKind string

const (
	SessionPersonaMappingLegacyCodexPair SessionPersonaMappingKind = "legacy_codex_pair"
	SessionPersonaMappingPersonaV3       SessionPersonaMappingKind = "persona_v3"
	SessionPersonaMappingCompatibility   SessionPersonaMappingKind = "compatibility_fallback"
)

var (
	ErrSessionPersonaInvalidDefinition = errors.New("invalid session persona definition")
	ErrSessionPersonaUnknown           = errors.New("unknown session persona")
	ErrSessionPersonaInvalidScope      = errors.New("invalid session persona scope version")
	ErrSessionPersonaInvalidSlot       = errors.New("invalid session persona slot")
	ErrSessionPersonaInvalidSlotCount  = errors.New("invalid session persona slot count")
)

// SessionPersona is the upstream identity contract used by adapters. The
// registry returns defensive copies, including the transport slice.
type SessionPersona struct {
	ID SessionPersonaID `json:"persona_id"`

	// PersonaVersion is canonical. Version remains a compatibility alias for
	// early callers that used the shorter field name.
	PersonaVersion string `json:"persona_version"`
	Version        string `json:"version,omitempty"`

	// UserAgent is the platform-neutral/default UA. UserAgentTemplate is used
	// by clients such as OpenCode that append OS details.
	UserAgent         string `json:"user_agent"`
	UserAgentTemplate string `json:"user_agent_template,omitempty"`
	Originator        string `json:"originator"`

	// Endpoint is the HTTPS Responses endpoint. WebSocketEndpoint is separate
	// because WS behavior is a Persona concern, not merely a transport toggle.
	Endpoint          string `json:"endpoint"`
	WebSocketEndpoint string `json:"websocket_endpoint,omitempty"`

	// Transport is the preferred transport. SupportedTransports is the full
	// capability set; rollout policy may still prefer HTTP first.
	Transport           SessionPersonaTransport   `json:"transport"`
	SupportedTransports []SessionPersonaTransport `json:"supported_transports"`

	Compression SessionPersonaCompression `json:"compression"`

	// Tokens do not belong here. OAuth metadata is part of the Persona
	// contract; credential state is stored by Account × Persona × chain ID.
	OAuthClientID string `json:"oauth_client_id,omitempty"`
	OAuthIssuer   string `json:"oauth_issuer,omitempty"`
}

// EffectiveVersion returns the canonical version while tolerating callers
// that only populated the compatibility Version field.
func (p SessionPersona) EffectiveVersion() string {
	if value := strings.TrimSpace(p.PersonaVersion); value != "" {
		return value
	}
	return strings.TrimSpace(p.Version)
}

// Valid reports whether the persona has the minimum fields required by an
// upstream adapter.
func (p SessionPersona) Valid() bool {
	return p.validate() == nil
}

func (p SessionPersona) validate() error {
	if _, ok := ParseSessionPersonaID(string(p.ID)); !ok {
		return fmt.Errorf("%w: persona_id is empty or unsupported", ErrSessionPersonaInvalidDefinition)
	}
	if p.EffectiveVersion() == "" {
		return fmt.Errorf("%w: persona version is empty", ErrSessionPersonaInvalidDefinition)
	}
	if strings.TrimSpace(p.UserAgent) == "" && strings.TrimSpace(p.UserAgentTemplate) == "" {
		return fmt.Errorf("%w: user agent is empty", ErrSessionPersonaInvalidDefinition)
	}
	if strings.TrimSpace(p.Originator) == "" {
		return fmt.Errorf("%w: originator is empty", ErrSessionPersonaInvalidDefinition)
	}
	if !validSessionPersonaEndpoint(p.Endpoint) {
		return fmt.Errorf("%w: endpoint is invalid", ErrSessionPersonaInvalidDefinition)
	}
	if p.WebSocketEndpoint != "" && !validSessionPersonaEndpoint(p.WebSocketEndpoint) {
		return fmt.Errorf("%w: websocket endpoint is invalid", ErrSessionPersonaInvalidDefinition)
	}
	if p.Transport != "" && !p.supportsTransport(p.Transport) {
		return fmt.Errorf("%w: preferred transport %q is not supported", ErrSessionPersonaInvalidDefinition, p.Transport)
	}
	if len(p.SupportedTransports) == 0 {
		return fmt.Errorf("%w: supported transports are empty", ErrSessionPersonaInvalidDefinition)
	}
	for _, transport := range p.SupportedTransports {
		if !isSessionPersonaTransport(transport) {
			return fmt.Errorf("%w: unsupported transport %q", ErrSessionPersonaInvalidDefinition, transport)
		}
	}
	return nil
}

func validSessionPersonaEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "ws" || parsed.Scheme == "wss"
}

func isSessionPersonaTransport(transport SessionPersonaTransport) bool {
	return transport == SessionPersonaTransportHTTP || transport == SessionPersonaTransportWS
}

// SupportsTransport reports whether the persona can use transport.
func (p SessionPersona) SupportsTransport(transport SessionPersonaTransport) bool {
	return p.supportsTransport(transport)
}

func (p SessionPersona) supportsTransport(transport SessionPersonaTransport) bool {
	if !isSessionPersonaTransport(transport) {
		return false
	}
	for _, supported := range p.SupportedTransports {
		if supported == transport {
			return true
		}
	}
	return false
}

// BuildUserAgent builds the UA shape owned by this Persona. OpenCode's source
// appends platform details; Codex strict keeps its pinned profile UA intact.
// Missing platform details intentionally produce the short deterministic UA.
func (p SessionPersona) BuildUserAgent(platform, release, arch string) string {
	version := p.EffectiveVersion()
	if strings.TrimSpace(p.UserAgentTemplate) != "" {
		platform = strings.TrimSpace(platform)
		release = strings.TrimSpace(release)
		arch = strings.TrimSpace(arch)
		if platform != "" && release != "" && arch != "" {
			return fmt.Sprintf(p.UserAgentTemplate, version, platform, release, arch)
		}
	}
	if value := strings.TrimSpace(p.UserAgent); value != "" {
		return value
	}
	if version != "" {
		return string(p.ID) + "/" + version
	}
	return string(p.ID)
}

// EndpointForTransport returns the endpoint appropriate for transport.
func (p SessionPersona) EndpointForTransport(transport SessionPersonaTransport) string {
	switch transport {
	case SessionPersonaTransportWS:
		if endpoint := strings.TrimSpace(p.WebSocketEndpoint); endpoint != "" {
			return endpoint
		}
		return websocketEndpointFromHTTP(p.Endpoint)
	case SessionPersonaTransportHTTP:
		fallthrough
	default:
		return strings.TrimSpace(p.Endpoint)
	}
}

func websocketEndpointFromHTTP(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	}
	return parsed.String()
}

// ParseSessionPersonaID normalizes a public persona identifier.
func ParseSessionPersonaID(raw string) (SessionPersonaID, bool) {
	switch SessionPersonaID(strings.ToLower(strings.TrimSpace(raw))) {
	case SessionPersonaCodexCLIStrict:
		return SessionPersonaCodexCLIStrict, true
	case SessionPersonaOpenCode:
		return SessionPersonaOpenCode, true
	default:
		return "", false
	}
}

// SessionPersonaSlotBinding is the auditable result of slot→Persona mapping.
// CompatibilityFallback is true only for v3 scopes carrying historical slots
// beyond the fixed slot 0/1 Persona mapping.
type SessionPersonaSlotBinding struct {
	AccountID        int64 `json:"account_id"`
	AccountPersonaID int64 `json:"account_persona_id,omitempty"`
	SlotID           int   `json:"slot_id"`
	SlotCount        int   `json:"slot_count"`
	// ScopeVersion is retained as a compatibility alias for the Persona
	// mapping version. It must not be confused with the underlying Codex
	// fingerprint storage scope version, which remains the legacy v1/v2 axis.
	ScopeVersion            int                     `json:"scope_version"`
	MappingVersion          int                     `json:"mapping_version"`
	FingerprintScopeVersion int                     `json:"fingerprint_scope_version,omitempty"`
	PersonaID               SessionPersonaID        `json:"persona_id"`
	PersonaVersion          string                  `json:"persona_version"`
	CredentialChainID       string                  `json:"credential_chain_id,omitempty"`
	InstallationID          string                  `json:"installation_id,omitempty"`
	State                   SessionPersonaSlotState `json:"state"`
	Enabled                 bool                    `json:"enabled"`
	// EnabledConfigured distinguishes an explicitly persisted false from the
	// zero value of historical bindings that had no enabled field.
	EnabledConfigured      bool   `json:"enabled_configured,omitempty"`
	Authorized             bool   `json:"authorized"`
	SessionEpoch           int64  `json:"session_epoch"`
	SlotGeneration         int64  `json:"slot_generation"`
	SlotSetGeneration      int64  `json:"slot_set_generation"`
	ClientThreadID         string `json:"client_thread_id,omitempty"`
	ClientResponseID       string `json:"client_response_id,omitempty"`
	UpstreamSessionID      string `json:"upstream_session_id,omitempty"`
	UpstreamMessageID      string `json:"upstream_message_id,omitempty"`
	CompactionCheckpointID string `json:"compaction_checkpoint_id,omitempty"`
	// MappingKey is an opaque, non-secret key for the Persona mapping layer.
	// It is intentionally separate from Codex fingerprint scope hashes.
	MappingKey            string                    `json:"mapping_key,omitempty"`
	Mapping               SessionPersonaMappingKind `json:"mapping"`
	Legacy                bool                      `json:"legacy"`
	CompatibilityFallback bool                      `json:"compatibility_fallback"`
	Persona               SessionPersona            `json:"-"`
}

// Valid reports whether a resolved binding is safe to use as an adapter key.
func (b SessionPersonaSlotBinding) Valid() bool {
	mappingVersion := b.EffectiveMappingVersion()
	if b.SlotID < 0 || b.SlotCount < 1 || b.SlotID >= b.SlotCount ||
		mappingVersion < 1 || mappingVersion > SessionPersonaScopeVersionV3 {
		return false
	}
	if b.FingerprintScopeVersion < 0 || b.FingerprintScopeVersion > SessionPersonaScopeVersionLegacyV2 {
		return false
	}
	if _, ok := ParseSessionPersonaID(string(b.PersonaID)); !ok {
		return false
	}
	// The pure slot resolver intentionally does not depend on a registry. If a
	// registry hydrated Persona is present, validate that snapshot as well.
	return b.Persona.ID == "" || b.Persona.Valid()
}

// EffectiveMappingVersion returns the independent Persona mapping version.
// ScopeVersion remains readable for older callers/tests that used that name.
func (b SessionPersonaSlotBinding) EffectiveMappingVersion() int {
	if b.MappingVersion > 0 {
		return b.MappingVersion
	}
	return b.ScopeVersion
}

// AcceptsNewRoot reports whether this slot may receive a new root request.
// Draining slots remain available only to existing Thread bindings.
func (b SessionPersonaSlotBinding) AcceptsNewRoot() bool {
	return b.State == SessionPersonaSlotStateActive && b.Enabled && b.Authorized
}

// KeepsExistingThread reports whether an existing Thread may naturally drain
// on this slot after new-root admission has been disabled.
func (b SessionPersonaSlotBinding) KeepsExistingThread() bool {
	return b.State == SessionPersonaSlotStateActive || b.State == SessionPersonaSlotStateDraining
}

// NormalizeLifecycle applies safe defaults for records created before the
// internal lifecycle fields existed. It never promotes an explicitly disabled
// record and keeps generation values monotonic/non-zero.
func (b SessionPersonaSlotBinding) NormalizeLifecycle() SessionPersonaSlotBinding {
	if b.State == "" {
		// Preserve old bindings that predate the enabled field as active. An
		// explicitly persisted enabled=false is a normal disable request and
		// therefore drains existing Threads; only an explicit internal disabled
		// state is a security hard-disable.
		if b.Enabled {
			b.State = SessionPersonaSlotStateActive
		} else if b.EnabledConfigured {
			b.State = SessionPersonaSlotStateDraining
		} else {
			b.State = SessionPersonaSlotStateActive
		}
	}
	if b.SlotGeneration < 1 {
		b.SlotGeneration = 1
	}
	if b.SlotSetGeneration < 1 {
		b.SlotSetGeneration = 1
	}
	return b
}

// Clone returns a request-safe copy and never aliases Persona transport data.
func (b SessionPersonaSlotBinding) Clone() SessionPersonaSlotBinding {
	b.Persona.SupportedTransports = cloneSessionPersonaTransports(b.Persona.SupportedTransports)
	return b
}

type sessionPersonaBindingContextKey struct{}

// ContextWithSessionPersonaBinding attaches an immutable Persona/slot binding
// to a request context. Empty bindings are ignored so legacy callers retain
// their Account-only behavior.
func ContextWithSessionPersonaBinding(ctx context.Context, binding SessionPersonaSlotBinding) context.Context {
	if ctx == nil || !binding.Valid() {
		return ctx
	}
	binding = binding.Clone().NormalizeLifecycle()
	return context.WithValue(ctx, sessionPersonaBindingContextKey{}, binding)
}

// SessionPersonaBindingFromContext reads the typed binding, if any.
func SessionPersonaBindingFromContext(ctx context.Context) (SessionPersonaSlotBinding, bool) {
	if ctx == nil {
		return SessionPersonaSlotBinding{}, false
	}
	if target, ok := OpenAIExecutionTargetFromContext(ctx); ok {
		return SessionPersonaBindingFromExecutionTarget(target)
	}
	binding, ok := ctx.Value(sessionPersonaBindingContextKey{}).(SessionPersonaSlotBinding)
	if ok && binding.Valid() {
		return binding.Clone().NormalizeLifecycle(), true
	}
	return SessionPersonaSlotBinding{}, false
}

// SessionPersonaBindingFromExecutionTarget 为现有协议适配器生成只读投影；
// 运行时身份、Token 与 Transport 仍只以 OpenAIExecutionTarget 为权威。
func SessionPersonaBindingFromExecutionTarget(target OpenAIExecutionTarget) (SessionPersonaSlotBinding, bool) {
	if !target.Valid() {
		return SessionPersonaSlotBinding{}, false
	}
	persona, ok := NewDefaultSessionPersonaRegistry().Get(string(target.ProfileID))
	if !ok {
		return SessionPersonaSlotBinding{}, false
	}
	persona.PersonaVersion = target.ProfileVersion
	persona.Version = target.ProfileVersion
	binding := SessionPersonaSlotBinding{
		AccountID: target.AccountID, AccountPersonaID: target.AccountPersonaID, SlotID: 0, SlotCount: 1,
		ScopeVersion: SessionPersonaScopeVersionV3, MappingVersion: SessionPersonaScopeVersionV3,
		PersonaID: target.ProfileID, PersonaVersion: target.ProfileVersion,
		CredentialChainID: target.CredentialChainID, InstallationID: target.InstallationID,
		State: SessionPersonaSlotStateActive, Enabled: true, EnabledConfigured: true, Authorized: true,
		SessionEpoch: target.SessionEpoch, SlotGeneration: target.PersonaGeneration,
		SlotSetGeneration: target.PersonaGeneration, UpstreamSessionID: target.UpstreamSessionID,
		MappingKey: fmt.Sprintf("account_persona:%d", target.AccountPersonaID),
		Mapping:    SessionPersonaMappingPersonaV3, Persona: persona,
	}
	return binding, binding.Valid()
}

// SessionPersonaSlotResolveRequest avoids relying on positional argument order
// at call sites that already carry scope metadata.
type SessionPersonaSlotResolveRequest struct {
	ScopeVersion int
	SlotID       int
	SlotCount    int
}

// ResolveSessionPersonaSlot resolves a slot under a scope version.
//
// v1/v2 (and a missing scope version represented by 0) are legacy Codex-only
// scopes. v3 maps slot 0 to strict Codex and slot 1 to OpenCode. Historical
// v3 slot counts 3/4 remain readable: extra slots resolve to strict Codex with
// CompatibilityFallback=true until the account converges to two slots. The
// resolver may also mark strict slot 0 as a compatibility fallback when an
// OpenCode-preferred new root needs the stable legacy path.
func ResolveSessionPersonaSlot(scopeVersion, slotID, slotCount int) (SessionPersonaSlotBinding, error) {
	if scopeVersion < 0 || scopeVersion > SessionPersonaScopeVersionV3 {
		return SessionPersonaSlotBinding{}, fmt.Errorf("%w: %d", ErrSessionPersonaInvalidScope, scopeVersion)
	}
	if scopeVersion == 0 {
		// A missing version is an old persisted scope. Treat it as v2 rather
		// than inventing an OpenCode binding for an existing Thread.
		scopeVersion = SessionPersonaScopeVersionLegacyV2
	}
	if slotCount == 0 {
		switch scopeVersion {
		case SessionPersonaScopeVersionLegacyV1:
			slotCount = LegacySingleSessionPersonaSlotCount
		case SessionPersonaScopeVersionLegacyV2:
			slotCount = LegacySessionPersonaSlotCount
		default:
			slotCount = DefaultSessionPersonaSlotCount
		}
	}
	if slotCount < 1 || slotCount > MaxSessionPersonaSlotCount {
		return SessionPersonaSlotBinding{}, fmt.Errorf("%w: %d", ErrSessionPersonaInvalidSlotCount, slotCount)
	}
	if slotID < 0 || slotID >= slotCount {
		return SessionPersonaSlotBinding{}, fmt.Errorf("%w: slot=%d count=%d", ErrSessionPersonaInvalidSlot, slotID, slotCount)
	}

	binding := SessionPersonaSlotBinding{
		SlotID:         slotID,
		SlotCount:      slotCount,
		ScopeVersion:   scopeVersion,
		MappingVersion: scopeVersion,
	}
	switch {
	case scopeVersion == SessionPersonaScopeVersionLegacyV1 || scopeVersion == SessionPersonaScopeVersionLegacyV2:
		binding.PersonaID = SessionPersonaCodexCLIStrict
		binding.Mapping = SessionPersonaMappingLegacyCodexPair
		binding.Legacy = true
	case scopeVersion == SessionPersonaScopeVersionV3 && slotID == 0:
		binding.PersonaID = SessionPersonaCodexCLIStrict
		binding.Mapping = SessionPersonaMappingPersonaV3
	case scopeVersion == SessionPersonaScopeVersionV3 && slotID == 1:
		binding.PersonaID = SessionPersonaOpenCode
		binding.Mapping = SessionPersonaMappingPersonaV3
	case scopeVersion == SessionPersonaScopeVersionV3:
		// Preserve old 3/4-slot data without expanding the new Persona
		// contract. The fallback is observable to migration code.
		binding.PersonaID = SessionPersonaCodexCLIStrict
		binding.Mapping = SessionPersonaMappingCompatibility
		binding.CompatibilityFallback = true
	default:
		return SessionPersonaSlotBinding{}, fmt.Errorf("%w: %d", ErrSessionPersonaInvalidScope, scopeVersion)
	}
	return binding, nil
}

// ResolveSessionPersonaSlotRequest is the struct-based equivalent of
// ResolveSessionPersonaSlot.
func ResolveSessionPersonaSlotRequest(request SessionPersonaSlotResolveRequest) (SessionPersonaSlotBinding, error) {
	return ResolveSessionPersonaSlot(request.ScopeVersion, request.SlotID, request.SlotCount)
}

// ResolveDefaultSessionPersonaSlot resolves a new-root slot under v3 defaults.
func ResolveDefaultSessionPersonaSlot(slotID int) (SessionPersonaSlotBinding, error) {
	return ResolveSessionPersonaSlot(
		DefaultSessionPersonaScopeVersion,
		slotID,
		DefaultSessionPersonaSlotCount,
	)
}

// ResolveLegacyCodexV2SessionPersonaSlot explicitly resolves the old double
// Codex scope, avoiding accidental use of the mixed v3 mapping for old Threads.
func ResolveLegacyCodexV2SessionPersonaSlot(slotID, slotCount int) (SessionPersonaSlotBinding, error) {
	return ResolveSessionPersonaSlot(SessionPersonaScopeVersionLegacyV2, slotID, slotCount)
}

// SessionPersonaRegistry stores validated persona definitions by ID.
type SessionPersonaRegistry struct {
	mu       sync.RWMutex
	personas map[SessionPersonaID]SessionPersona
}

// NewSessionPersonaRegistry creates a registry. Invalid definitions are
// skipped for convenient static construction; Register exposes validation
// errors for dynamic configuration paths.
func NewSessionPersonaRegistry(personas ...SessionPersona) *SessionPersonaRegistry {
	registry := &SessionPersonaRegistry{personas: make(map[SessionPersonaID]SessionPersona, len(personas))}
	for _, persona := range personas {
		if err := registry.Register(persona); err != nil {
			continue
		}
	}
	return registry
}

// Register validates and replaces a persona with the same ID.
func (r *SessionPersonaRegistry) Register(persona SessionPersona) error {
	if r == nil {
		return ErrSessionPersonaInvalidDefinition
	}
	canonicalID, ok := ParseSessionPersonaID(string(persona.ID))
	if !ok {
		return fmt.Errorf("%w: persona_id=%q", ErrSessionPersonaInvalidDefinition, persona.ID)
	}
	persona.ID = canonicalID
	version := persona.EffectiveVersion()
	persona.PersonaVersion = version
	persona.Version = version
	if err := persona.validate(); err != nil {
		return err
	}
	persona.SupportedTransports = cloneSessionPersonaTransports(persona.SupportedTransports)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.personas == nil {
		r.personas = make(map[SessionPersonaID]SessionPersona)
	}
	r.personas[persona.ID] = persona
	return nil
}

// Get returns a defensive copy of a registered persona.
func (r *SessionPersonaRegistry) Get(rawID string) (SessionPersona, bool) {
	if r == nil {
		return SessionPersona{}, false
	}
	id, ok := ParseSessionPersonaID(rawID)
	if !ok {
		return SessionPersona{}, false
	}
	r.mu.RLock()
	persona, ok := r.personas[id]
	r.mu.RUnlock()
	if !ok {
		return SessionPersona{}, false
	}
	persona.SupportedTransports = cloneSessionPersonaTransports(persona.SupportedTransports)
	return persona, true
}

// MustGet returns a registered persona or an explicit unknown-persona error.
// A missing definition should become a migration/fallback decision, not a
// process panic.
func (r *SessionPersonaRegistry) MustGet(rawID string) (SessionPersona, error) {
	persona, ok := r.Get(rawID)
	if !ok {
		return SessionPersona{}, fmt.Errorf("%w: %q", ErrSessionPersonaUnknown, strings.TrimSpace(rawID))
	}
	return persona, nil
}

// List returns registered definitions in deterministic ID order.
func (r *SessionPersonaRegistry) List() []SessionPersona {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	personas := make([]SessionPersona, 0, len(r.personas))
	for _, persona := range r.personas {
		persona.SupportedTransports = cloneSessionPersonaTransports(persona.SupportedTransports)
		personas = append(personas, persona)
	}
	r.mu.RUnlock()
	sort.Slice(personas, func(i, j int) bool { return personas[i].ID < personas[j].ID })
	return personas
}

// ResolveSlot resolves and hydrates a slot binding from this registry.
func (r *SessionPersonaRegistry) ResolveSlot(scopeVersion, slotID, slotCount int) (SessionPersonaSlotBinding, error) {
	binding, err := ResolveSessionPersonaSlot(scopeVersion, slotID, slotCount)
	if err != nil {
		return SessionPersonaSlotBinding{}, err
	}
	persona, err := r.MustGet(string(binding.PersonaID))
	if err != nil {
		return SessionPersonaSlotBinding{}, err
	}
	binding.Persona = persona
	binding.PersonaVersion = persona.EffectiveVersion()
	return binding, nil
}

// Clone returns an independent registry snapshot suitable for request-scoped
// or test configuration.
func (r *SessionPersonaRegistry) Clone() *SessionPersonaRegistry {
	clone := NewSessionPersonaRegistry()
	if r == nil {
		return clone
	}
	for _, persona := range r.List() {
		// A registry can only contain validated definitions, so this error is
		// impossible unless the implementation is changed concurrently.
		_ = clone.Register(persona)
	}
	return clone
}

func cloneSessionPersonaTransports(transports []SessionPersonaTransport) []SessionPersonaTransport {
	if len(transports) == 0 {
		return nil
	}
	return append([]SessionPersonaTransport(nil), transports...)
}

const (
	// SessionPersonaCodexCLIStrictVersion is the pinned strict profile version.
	SessionPersonaCodexCLIStrictVersion = codexCLI0149Version
	// SessionPersonaOpenCodeVersion, SessionPersonaOpenCodeTag and
	// SessionPersonaOpenCodeTagSHA pin the source-compatible OpenCode release
	// used by this registry. The SHA is the immutable commit resolved by the
	// formal tag, not a mutable branch or tag lookup at runtime.
	SessionPersonaOpenCodeVersion       = "1.18.23"
	SessionPersonaOpenCodeTag           = "v1.18.23"
	SessionPersonaOpenCodeTagSHA        = "ef2880f379129aa048be9e9353e30aa168d42c17"
	SessionPersonaOpenAICodexEndpoint   = "https://chatgpt.com/backend-api/codex/responses"
	SessionPersonaOpenAIOAuthIssuer     = "https://auth.openai.com"
	SessionPersonaOpenCodeOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
)

// NewDefaultSessionPersonaRegistry returns the two production Persona
// definitions. A fresh registry prevents policy reloads from mutating a
// request that already captured a snapshot.
func NewDefaultSessionPersonaRegistry() *SessionPersonaRegistry {
	return NewSessionPersonaRegistry(
		SessionPersona{
			ID:                SessionPersonaCodexCLIStrict,
			PersonaVersion:    SessionPersonaCodexCLIStrictVersion,
			UserAgent:         codexCLI0149WindowsUserAgent,
			Originator:        "codex_cli_rs",
			Endpoint:          SessionPersonaOpenAICodexEndpoint,
			WebSocketEndpoint: websocketEndpointFromHTTP(SessionPersonaOpenAICodexEndpoint),
			Transport:         SessionPersonaTransportHTTP,
			SupportedTransports: []SessionPersonaTransport{
				SessionPersonaTransportHTTP,
				SessionPersonaTransportWS,
			},
			Compression: SessionPersonaCompressionZstd,
			OAuthIssuer: SessionPersonaOpenAIOAuthIssuer,
		},
		SessionPersona{
			ID:                SessionPersonaOpenCode,
			PersonaVersion:    SessionPersonaOpenCodeVersion,
			UserAgent:         "opencode/" + SessionPersonaOpenCodeVersion,
			UserAgentTemplate: "opencode/%s (%s %s; %s)",
			Originator:        "opencode",
			Endpoint:          SessionPersonaOpenAICodexEndpoint,
			WebSocketEndpoint: websocketEndpointFromHTTP(SessionPersonaOpenAICodexEndpoint),
			Transport:         SessionPersonaTransportHTTP,
			SupportedTransports: []SessionPersonaTransport{
				SessionPersonaTransportHTTP,
			},
			Compression:   SessionPersonaCompressionNone,
			OAuthClientID: SessionPersonaOpenCodeOAuthClientID,
			OAuthIssuer:   SessionPersonaOpenAIOAuthIssuer,
		},
	)
}

// DefaultSessionPersonaRegistry returns a fresh default snapshot.
func DefaultSessionPersonaRegistry() *SessionPersonaRegistry {
	return NewDefaultSessionPersonaRegistry()
}

// ResolveDefaultSessionPersona is a convenience helper for callers that only
// need the hydrated default Persona and not the full binding metadata.
func ResolveDefaultSessionPersona(slotID int) (SessionPersona, error) {
	binding, err := NewDefaultSessionPersonaRegistry().ResolveSlot(
		DefaultSessionPersonaScopeVersion,
		slotID,
		DefaultSessionPersonaSlotCount,
	)
	if err != nil {
		return SessionPersona{}, err
	}
	return binding.Persona, nil
}
