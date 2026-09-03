package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type openAITransportScopeContextKey struct{}

// OpenAITransportScope 是 OpenAI OAuth 专用 Transport 的不可变作用域快照。
//
// 动态路径只从 OpenAIExecutionTarget 派生，不包含 token 内容。Transport 层必须
// 把 AccountPersona、代次、Session epoch、credential、Profile 和代理快照作为
// 最小隔离边界，不能退化为账号级或 Profile 级复用。
type OpenAITransportScope struct {
	AccountID         int64
	AccountPersonaID  int64
	ProfileID         SessionPersonaID
	ProfileVersion    string
	PersonaGeneration int64
	SessionEpoch      int64
	CredentialChainID string
	InstallationID    string
	EffectiveProxyID  *int64
	ProxyRevision     int64

	// 以下字段只供 P8 删除前的固定双槽请求兼容，不参与动态 Persona scope。
	Persona           SessionPersonaID
	PersonaVersion    string
	SlotID            int
	SlotGeneration    int64
	SlotSetGeneration int64
}

type OpenAIPersonaTransportInvalidator interface {
	InvalidateOpenAIPersonaTransport(accountID int64, persona SessionPersonaID, slotID int, credentialChainID string)
	InvalidateOpenAIAccountPersonaCredentialTransport(accountID, accountPersonaID int64, credentialChainID string)
	InvalidateOpenAIAccountPersonaSessionTransport(accountID, accountPersonaID, sessionEpoch int64)
}

func (s OpenAITransportScope) MatchesCredential(accountID int64, persona SessionPersonaID, slotID int, credentialChainID string) bool {
	return s.AccountID == accountID && s.Persona == persona && s.SlotID == slotID &&
		strings.TrimSpace(s.CredentialChainID) == strings.TrimSpace(credentialChainID)
}

func (s OpenAITransportScope) MatchesAccountPersonaCredential(accountPersonaID int64, credentialChainID string) bool {
	return s.AccountPersonaID > 0 && s.AccountPersonaID == accountPersonaID &&
		strings.TrimSpace(s.CredentialChainID) == strings.TrimSpace(credentialChainID)
}

func (s OpenAITransportScope) MatchesAccountPersonaSession(accountPersonaID, sessionEpoch int64) bool {
	return s.AccountPersonaID > 0 && s.AccountPersonaID == accountPersonaID && s.SessionEpoch == sessionEpoch
}

// Fingerprint 返回不包含凭据内容的作用域摘要，用于 WS 连接池的严格复用判定。
// 摘要把代际字段纳入键，确保 Session/slot/credential 链轮换后不会命中旧连接。
func (s OpenAITransportScope) Fingerprint(profileVersion, proxyURL string) string {
	raw := ""
	if s.AccountPersonaID > 0 {
		proxyID := int64(0)
		if s.EffectiveProxyID != nil {
			proxyID = *s.EffectiveProxyID
		}
		raw = fmt.Sprintf("dynamic|%d|%d|%s|%s|%d|%d|%s|%s|%d|%d|%s|%s",
			s.AccountID, s.AccountPersonaID, strings.TrimSpace(string(s.ProfileID)),
			strings.TrimSpace(s.ProfileVersion), s.PersonaGeneration, s.SessionEpoch,
			strings.TrimSpace(s.CredentialChainID), strings.TrimSpace(s.InstallationID),
			proxyID, s.ProxyRevision, strings.TrimSpace(profileVersion), strings.TrimSpace(proxyURL))
	} else {
		raw = fmt.Sprintf("legacy|%d|%s|%s|%d|%d|%d|%d|%s|%s|%s",
			s.AccountID, strings.TrimSpace(string(s.Persona)), strings.TrimSpace(s.PersonaVersion),
			s.SlotID, s.SessionEpoch, s.SlotGeneration, s.SlotSetGeneration,
			strings.TrimSpace(s.CredentialChainID), strings.TrimSpace(s.InstallationID),
			strings.TrimSpace(profileVersion)+"|"+strings.TrimSpace(proxyURL))
	}
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// OpenAICPAScopeFingerprint uses the locked OpenAI CPA profile version so
// callers cannot accidentally derive different WS/HTTP scope keys.
func (s OpenAITransportScope) OpenAICPAScopeFingerprint(proxyURL string) string {
	return s.Fingerprint("openai_chrome_cpa_v1", proxyURL)
}

// ContextWithOpenAITransportScope attaches an immutable transport scope to a
// derived dial context. WS pool callers use this when the original request
// context is wrapped by an acquire timeout.
func ContextWithOpenAITransportScope(ctx context.Context, scope OpenAITransportScope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAITransportScopeContextKey{}, scope)
}

func openAITransportScopeFromContextAny(ctx context.Context) (OpenAITransportScope, bool) {
	if ctx == nil {
		return OpenAITransportScope{}, false
	}
	if value := ctx.Value(openAITransportScopeContextKey{}); value != nil {
		if scope, ok := value.(OpenAITransportScope); ok && scope.ReadyForCPA(scope.AccountID) {
			return scope, true
		}
	}
	if target, ok := OpenAIExecutionTargetFromContext(ctx); ok {
		scope := openAITransportScopeFromExecutionTarget(target)
		if scope.ReadyForCPA(target.AccountID) {
			return scope, true
		}
	}
	binding, ok := SessionPersonaBindingFromContext(ctx)
	if !ok || binding.EffectiveMappingVersion() < SessionPersonaScopeVersionV3 {
		return OpenAITransportScope{}, false
	}
	scope := openAITransportScopeFromBinding(binding)
	if !scope.ReadyForCPA(scope.AccountID) {
		return OpenAITransportScope{}, false
	}
	return scope, true
}

func openAITransportScopeFromExecutionTarget(target OpenAIExecutionTarget) OpenAITransportScope {
	return OpenAITransportScope{
		AccountID: target.AccountID, AccountPersonaID: target.AccountPersonaID,
		ProfileID: target.ProfileID, ProfileVersion: strings.TrimSpace(target.ProfileVersion),
		PersonaGeneration: target.PersonaGeneration, SessionEpoch: target.SessionEpoch,
		CredentialChainID: strings.TrimSpace(target.CredentialChainID),
		InstallationID:    strings.TrimSpace(target.InstallationID),
		EffectiveProxyID:  target.EffectiveProxyID, ProxyRevision: target.ProxyRevision,
	}
}

func openAITransportScopeFromBinding(binding SessionPersonaSlotBinding) OpenAITransportScope {
	return OpenAITransportScope{
		AccountID:         binding.AccountID,
		Persona:           binding.PersonaID,
		PersonaVersion:    strings.TrimSpace(binding.PersonaVersion),
		SlotID:            binding.SlotID,
		SessionEpoch:      binding.SessionEpoch,
		SlotGeneration:    binding.SlotGeneration,
		SlotSetGeneration: binding.SlotSetGeneration,
		CredentialChainID: strings.TrimSpace(binding.CredentialChainID),
		InstallationID:    strings.TrimSpace(binding.InstallationID),
	}
}

// ReadyForCPA 表示当前请求具备进入 CPA Transport 的完整身份快照。
// 缺少任一关键代际字段时返回 false，由调用方保留旧兼容 Transport。
func (s OpenAITransportScope) ReadyForCPA(accountID int64) bool {
	if s.AccountID <= 0 || s.AccountID != accountID || s.SessionEpoch <= 0 ||
		strings.TrimSpace(s.CredentialChainID) == "" || strings.TrimSpace(s.InstallationID) == "" {
		return false
	}
	if s.AccountPersonaID > 0 {
		if strings.TrimSpace(string(s.ProfileID)) == "" || strings.TrimSpace(s.ProfileVersion) == "" ||
			s.PersonaGeneration <= 0 || s.ProxyRevision < 0 {
			return false
		}
		return s.EffectiveProxyID == nil || s.ProxyRevision > 0
	}
	return strings.TrimSpace(string(s.Persona)) != "" &&
		s.SlotID >= 0 &&
		s.SlotGeneration > 0 &&
		s.SlotSetGeneration > 0
}

// OpenAITransportScopeFromContext 从请求上下文读取 Persona/slot 绑定。
// 只有完整 v3 绑定才会返回 true；旧 v1/v2 或不完整兼容请求继续走旧路径。
func OpenAITransportScopeFromContext(ctx context.Context, accountID int64) (OpenAITransportScope, bool) {
	scope, ok := openAITransportScopeFromContextAny(ctx)
	if !ok || !scope.ReadyForCPA(accountID) {
		return OpenAITransportScope{}, false
	}
	return scope, true
}
