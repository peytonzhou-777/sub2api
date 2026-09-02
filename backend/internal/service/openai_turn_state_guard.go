package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

const openAITurnStateSessionHashContextKey = "openai_turn_state_session_hash"

// openAITurnStateLifecycle 模拟 Codex 每 turn 的 OnceLock 语义。一个物理 WS
// 可以承载多个 turn，但不透明路由状态不得跨 turn_id 变化继续存活。
type openAITurnStateLifecycle struct {
	mu     sync.Mutex
	turnID string
	state  string
}

// BeginTurn 切换逻辑 turn，并在当前 turn 尚未锁定状态时接受首个候选值。
func (l *openAITurnStateLifecycle) BeginTurn(turnID, candidate string) string {
	if l == nil {
		return ""
	}
	turnID = strings.TrimSpace(turnID)
	candidate = strings.TrimSpace(candidate)
	l.mu.Lock()
	defer l.mu.Unlock()
	if turnID == "" {
		l.turnID = ""
		l.state = ""
		return ""
	}
	if l.turnID != turnID {
		l.turnID = turnID
		l.state = ""
	}
	if l.state == "" && candidate != "" {
		l.state = candidate
	}
	return l.state
}

// Commit 以 first-write-wins 方式提交当前 turn 的首个有效上游状态。
func (l *openAITurnStateLifecycle) Commit(turnID, candidate string) (string, bool) {
	if l == nil {
		return "", false
	}
	turnID = strings.TrimSpace(turnID)
	candidate = strings.TrimSpace(candidate)
	if turnID == "" || candidate == "" {
		return "", false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.turnID != turnID {
		return "", false
	}
	if l.state != "" {
		return l.state, false
	}
	l.state = candidate
	return candidate, true
}

// OpenAITurnStateScope 是 x-codex-turn-state 唯一允许复用的完整身份边界。
// 它不绑定 HTTP/WS 物理传输，以便同一 turn 在相同 CPA scope 内安全回退。
type OpenAITurnStateScope struct {
	Version              int              `json:"version"`
	AccountID            int64            `json:"account_id"`
	Persona              SessionPersonaID `json:"persona"`
	PersonaVersion       string           `json:"persona_version,omitempty"`
	MappingVersion       int              `json:"mapping_version"`
	SlotID               int              `json:"slot_id"`
	SessionEpoch         int64            `json:"session_epoch"`
	SlotGeneration       int64            `json:"slot_generation"`
	SlotSetGeneration    int64            `json:"slot_set_generation"`
	CredentialChainID    string           `json:"credential_chain_id,omitempty"`
	InstallationID       string           `json:"installation_id"`
	SessionScopeHash     string           `json:"session_scope_hash"`
	UpstreamSessionID    string           `json:"upstream_session_id"`
	UpstreamThreadID     string           `json:"upstream_thread_id"`
	UpstreamTurnID       string           `json:"upstream_turn_id"`
	OutboundProfile      string           `json:"outbound_profile"`
	TransportScopeDigest string           `json:"transport_scope_digest"`
}

// Normalize 清理 scope 中的文本标识，确保持久化和比较使用同一规范形态。
func (s OpenAITurnStateScope) Normalize() OpenAITurnStateScope {
	s.Persona = SessionPersonaID(strings.ToLower(strings.TrimSpace(string(s.Persona))))
	s.PersonaVersion = strings.TrimSpace(s.PersonaVersion)
	s.CredentialChainID = strings.TrimSpace(s.CredentialChainID)
	s.InstallationID = strings.TrimSpace(s.InstallationID)
	s.SessionScopeHash = strings.TrimSpace(s.SessionScopeHash)
	s.UpstreamSessionID = strings.TrimSpace(s.UpstreamSessionID)
	s.UpstreamThreadID = strings.TrimSpace(s.UpstreamThreadID)
	s.UpstreamTurnID = strings.TrimSpace(s.UpstreamTurnID)
	s.OutboundProfile = strings.TrimSpace(s.OutboundProfile)
	s.TransportScopeDigest = strings.TrimSpace(s.TransportScopeDigest)
	return s
}

// Valid 判断 scope 是否具备安全复用 turn-state 所需的全部身份字段。
func (s OpenAITurnStateScope) Valid() bool {
	s = s.Normalize()
	if s.Version != 2 || s.AccountID <= 0 || s.Persona != SessionPersonaCodexCLIStrict ||
		s.SlotID < 0 || s.SessionEpoch <= 0 || s.InstallationID == "" ||
		s.SessionScopeHash == "" || s.UpstreamSessionID == "" || s.UpstreamThreadID == "" ||
		s.UpstreamTurnID == "" || s.OutboundProfile == "" || s.TransportScopeDigest == "" {
		return false
	}
	if s.MappingVersion >= SessionPersonaScopeVersionV3 {
		return s.SlotGeneration > 0 && s.SlotSetGeneration > 0 && s.CredentialChainID != ""
	}
	return true
}

// Equal 对两个规范化后的完整身份 scope 做严格等值比较。
func (s OpenAITurnStateScope) Equal(other OpenAITurnStateScope) bool {
	return s.Normalize() == other.Normalize()
}

// isolateOpenAITurnStateAttempt 为单次账号尝试克隆请求，并按完整 turn scope
// 剥离来源未知、跨槽位、跨代际或跨 turn 的状态。返回体可能已删除 WS 载体。
func (s *OpenAIGatewayService) isolateOpenAITurnStateAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	requestBody []byte,
) ([]byte, func(), error) {
	if c == nil || c.Request == nil {
		return requestBody, func() {}, nil
	}
	originalRequest := c.Request
	attemptCtx := ctx
	if attemptCtx == nil {
		attemptCtx = originalRequest.Context()
	}
	if _, ok := SessionPersonaBindingFromContext(attemptCtx); !ok {
		if binding, bound := SessionPersonaBindingFromContext(originalRequest.Context()); bound {
			attemptCtx = ContextWithSessionPersonaBinding(attemptCtx, binding)
		}
	}
	// 每次转发都使用本轮 ctx，确保账号级指纹、turn-state 与 429 延后处分
	// 只作用于当前尝试，不泄漏到原始客户端请求或后续账号。
	attemptRequest := originalRequest.Clone(attemptCtx)
	attemptRequest.Header = originalRequest.Header.Clone()
	c.Request = attemptRequest
	restore := func() { c.Request = originalRequest }

	sessionHash := s.GenerateSessionHash(c, requestBody)
	c.Set(openAITurnStateSessionHashContextKey, sessionHash)
	turnState, conflictingCarrier := extractOpenAITurnStateFromRequest(attemptRequest.Header, requestBody)
	if turnState == "" {
		return requestBody, restore, nil
	}
	if account != nil && account.IsOpenAIOAuth() && !codexFingerprintAdmissionPreparedForAccount(c, account) {
		if err := s.PrepareCodexFingerprintForAdmission(ctx, c, account, requestBody, false); err != nil {
			restore()
			return requestBody, func() {}, err
		}
	}
	allowed, reason, sourceScope := s.openAITurnStateAllowedForAccount(ctx, c, account, sessionHash, turnState)
	if conflictingCarrier {
		allowed = false
		reason = "conflicting_carriers"
	}
	if !allowed {
		requestBody = stripOpenAITurnStateFromRequest(attemptRequest.Header, requestBody)
		s.recordOpenAITurnStateStripped()
		accountID := int64(0)
		if account != nil {
			accountID = account.ID
		}
		logger.L().Warn("openai.turn_state_stripped",
			zap.String("reason", reason),
			zap.Int64("account_id", accountID),
			zap.Int64("source_account_id", sourceScope.AccountID),
			zap.Int64("api_key_id", getAPIKeyIDFromContext(c)),
			zap.Bool("has_session_hash", sessionHash != ""),
		)
	}
	return requestBody, restore, nil
}

func openAITurnStateSessionHash(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(openAITurnStateSessionHashContextKey)
	if !ok {
		return ""
	}
	sessionHash, _ := value.(string)
	return strings.TrimSpace(sessionHash)
}

func (s *OpenAIGatewayService) openAITurnStateAllowedForAccount(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	sessionHash string,
	turnState string,
) (bool, string, OpenAITurnStateScope) {
	if account == nil || account.ID <= 0 || account.Platform != PlatformOpenAI {
		return false, "non_openai_account", OpenAITurnStateScope{}
	}
	store := s.getOpenAIWSStateStore()
	apiKeyID := getAPIKeyIDFromContext(c)
	if store == nil || apiKeyID <= 0 || strings.TrimSpace(sessionHash) == "" {
		return false, "missing_provenance_scope", OpenAITurnStateScope{}
	}
	currentScope, ok := openAITurnStateScopeForAttempt(ctx, c, account)
	if !ok {
		return false, "incomplete_current_scope", OpenAITurnStateScope{}
	}
	sourceScope, err := store.GetTurnStateScope(ctx, getOpenAIGroupIDFromContext(c), apiKeyID, sessionHash, turnState)
	if err != nil {
		if errors.Is(err, ErrOpenAITurnStateScopeNotFound) {
			return false, "unknown_source", OpenAITurnStateScope{}
		}
		return false, "provenance_lookup_failed", OpenAITurnStateScope{}
	}
	if !sourceScope.Equal(currentScope) {
		return false, openAITurnStateScopeMismatchReason(sourceScope, currentScope), sourceScope
	}
	return true, "matched", sourceScope
}

// bindOpenAITurnStateProvenance 在有效输出确认后提交状态来源，不持久化原始 state blob。
func (s *OpenAIGatewayService) bindOpenAITurnStateProvenance(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	sessionHash string,
	turnState string,
	ttl time.Duration,
) {
	store := s.getOpenAIWSStateStore()
	apiKeyID := getAPIKeyIDFromContext(c)
	scope, ok := openAITurnStateScopeForAttempt(ctx, c, account)
	if store == nil || !ok || apiKeyID <= 0 || strings.TrimSpace(sessionHash) == "" || strings.TrimSpace(turnState) == "" {
		return
	}
	if err := store.BindTurnStateScope(ctx, getOpenAIGroupIDFromContext(c), apiKeyID, sessionHash, turnState, scope, ttl); err != nil {
		logger.L().Warn("openai.turn_state_provenance_bind_failed",
			zap.Int64("account_id", scope.AccountID),
			zap.Int64("api_key_id", apiKeyID),
			zap.Error(err),
		)
	}
}

func openAITurnStateScopeForAttempt(ctx context.Context, c *gin.Context, account *Account) (OpenAITurnStateScope, bool) {
	ids := stagedCodexFingerprintIDsForAccount(c, account)
	if account == nil || ids == nil {
		return OpenAITurnStateScope{}, false
	}
	persona := SessionPersonaCodexCLIStrict
	personaVersion := ResolveCodexOutboundProfile(account)
	mappingVersion := ids.sessionScopeVersion
	slotID := ids.sessionSlot
	var slotGeneration, slotSetGeneration int64
	credentialChainID := ""
	transportDigest := ""
	if binding, ok := SessionPersonaBindingFromContextOrGin(ctx, c); ok {
		if binding.AccountID != account.ID || IsOpenCodePersona(binding) || binding.SlotID != ids.sessionSlot ||
			binding.SessionEpoch != ids.sessionEpoch ||
			(strings.TrimSpace(binding.InstallationID) != "" && strings.TrimSpace(binding.InstallationID) != strings.TrimSpace(ids.installationID)) {
			return OpenAITurnStateScope{}, false
		}
		persona = binding.PersonaID
		personaVersion = binding.PersonaVersion
		mappingVersion = binding.EffectiveMappingVersion()
		slotID = binding.SlotID
		slotGeneration = binding.SlotGeneration
		slotSetGeneration = binding.SlotSetGeneration
		credentialChainID = binding.CredentialChainID
		proxyURL := ""
		if account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		transportScope := openAITransportScopeFromBinding(binding)
		if !transportScope.ReadyForCPA(account.ID) {
			return OpenAITurnStateScope{}, false
		}
		transportDigest = transportScope.OpenAICPAScopeFingerprint(proxyURL)
	}
	profile := ResolveCodexOutboundProfile(account)
	if transportDigest == "" {
		proxyURL := ""
		if account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		transportSeed := strings.Join([]string{profile, proxyURL, ids.installationID, credentialChainID}, "\x00")
		digest := sha256.Sum256([]byte(transportSeed))
		transportDigest = hex.EncodeToString(digest[:])
	}
	scope := OpenAITurnStateScope{
		Version:              2,
		AccountID:            account.ID,
		Persona:              persona,
		PersonaVersion:       personaVersion,
		MappingVersion:       mappingVersion,
		SlotID:               slotID,
		SessionEpoch:         ids.sessionEpoch,
		SlotGeneration:       slotGeneration,
		SlotSetGeneration:    slotSetGeneration,
		CredentialChainID:    credentialChainID,
		InstallationID:       ids.installationID,
		SessionScopeHash:     ids.sessionScopeHash,
		UpstreamSessionID:    ids.sessionID,
		UpstreamThreadID:     ids.threadID,
		UpstreamTurnID:       ids.turnID,
		OutboundProfile:      profile,
		TransportScopeDigest: transportDigest,
	}.Normalize()
	return scope, scope.Valid()
}

func openAITurnStateScopeMismatchReason(source, current OpenAITurnStateScope) string {
	if source.AccountID != current.AccountID {
		return "cross_account"
	}
	if source.Persona != current.Persona {
		return "cross_persona"
	}
	if source.SlotID != current.SlotID {
		return "cross_slot"
	}
	if source.UpstreamTurnID != current.UpstreamTurnID {
		return "cross_turn"
	}
	if source.SessionEpoch != current.SessionEpoch || source.SlotGeneration != current.SlotGeneration || source.SlotSetGeneration != current.SlotSetGeneration {
		return "cross_generation"
	}
	if source.CredentialChainID != current.CredentialChainID {
		return "cross_credential"
	}
	return "scope_mismatch"
}

func extractOpenAITurnStateFromRequest(headers http.Header, body []byte) (string, bool) {
	values := []string{strings.TrimSpace(headers.Get(openAIWSTurnStateHeader))}
	if len(body) > 0 && gjson.ValidBytes(body) {
		for _, path := range []string{"client_metadata.x-codex-turn-state", "client_metadata.x_codex_turn_state", "x-codex-turn-state", "x_codex_turn_state"} {
			values = append(values, strings.TrimSpace(gjson.GetBytes(body, path).String()))
		}
	}
	state := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		if state != "" && state != value {
			return state, true
		}
		state = value
	}
	return state, false
}

func stripOpenAITurnStateFromRequest(headers http.Header, body []byte) []byte {
	deleteOpenAIHeaderEqualFold(headers, openAIWSTurnStateHeader)
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	stripped := body
	for _, path := range []string{"client_metadata.x-codex-turn-state", "client_metadata.x_codex_turn_state", "x-codex-turn-state", "x_codex_turn_state"} {
		if next, err := sjson.DeleteBytes(stripped, path); err == nil {
			stripped = next
		}
	}
	return stripped
}

// validateOpenAITurnStatePayload 仅校验 WS 帧内 client_metadata，不读取连接
// 握手头；握手头中的状态只属于下游连接的首个逻辑 turn。
func (s *OpenAIGatewayService) validateOpenAITurnStatePayload(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	sessionHash string,
	body []byte,
) ([]byte, string) {
	state, conflicting := extractOpenAITurnStateFromRequest(nil, body)
	if state == "" {
		return body, ""
	}
	allowed, reason, sourceScope := s.openAITurnStateAllowedForAccount(ctx, c, account, sessionHash, state)
	if conflicting {
		allowed = false
		reason = "conflicting_carriers"
	}
	if allowed {
		return body, state
	}
	stripped := stripOpenAITurnStateFromRequest(nil, body)
	s.recordOpenAITurnStateStripped()
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	logger.L().Warn("openai.turn_state_payload_stripped",
		zap.String("reason", reason),
		zap.Int64("account_id", accountID),
		zap.Int64("source_account_id", sourceScope.AccountID),
		zap.Int64("api_key_id", getAPIKeyIDFromContext(c)),
	)
	return stripped, ""
}

func applyOpenAITurnStateToPayload(body []byte, state string) []byte {
	body = stripOpenAITurnStateFromRequest(nil, body)
	state = strings.TrimSpace(state)
	if state == "" || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}
	updated, err := sjson.SetBytes(body, "client_metadata.x-codex-turn-state", state)
	if err != nil {
		return body
	}
	return updated
}

func extractOpenAITurnStateFromMetadataEvent(eventType string, payload []byte) string {
	switch strings.TrimSpace(eventType) {
	case "response.metadata", "codex.response.metadata":
	default:
		return ""
	}
	for _, path := range []string{
		"headers.x-codex-turn-state",
		"headers.x_codex_turn_state",
		"response.headers.x-codex-turn-state",
		"response.headers.x_codex_turn_state",
	} {
		if state := strings.TrimSpace(gjson.GetBytes(payload, path).String()); state != "" {
			return state
		}
	}
	return ""
}

func cloneOpenAIAttemptHeaderWithTurnState(base http.Header, turnState string) http.Header {
	headers := cloneHeader(base)
	if headers == nil {
		headers = make(http.Header)
	}
	if state := strings.TrimSpace(turnState); state != "" {
		headers.Set(openAIWSTurnStateHeader, state)
	} else {
		headers.Del(openAIWSTurnStateHeader)
	}
	return headers
}
