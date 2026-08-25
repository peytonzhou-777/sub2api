package service

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const opsRequestObservationKey = "ops_request_observation"

const (
	opsMaxRetryAfterBytes                     = 128
	opsMaxRateLimitHeaderCount                = 64
	opsMaxRateLimitValueBytes                 = 1024
	openAIPromptCacheDiagnosticLargeBodyBytes = 64 * 1024
)

// OpenAIPromptCacheObservation 保存最终出站请求的缓存诊断摘要。
// 只保留不可逆哈希和低敏元数据，不保存 prompt、input 或完整请求体。
type OpenAIPromptCacheObservation struct {
	OutboundPromptCacheKeyHash string
	OutboundBodyHash           string
	OutboundPrefixConfigHash   string
	OutboundProfile            string
	OutboundFallbackReason     string
	OutboundAccountID          int64
	OutboundModel              string
	OutboundRequestKind        string
	OutboundServiceTier        string
	OutboundTransport          string
	OutboundBodyBytes          int
	AttemptNumber              int
	FingerprintMode            string
	FingerprintVersion         string
	SessionIDHash              string
	SessionScopeHash           string
	SessionEpoch               int64
	SessionScopeVersion        int
	SessionSlot                int
	SessionSlotCount           int
}

// OpsRequestObservation 保存请求级诊断信息；敏感标识只允许以截断哈希形式离开请求上下文。
type OpsRequestObservation struct {
	RequestStartedAt time.Time
	DurationMs       int64

	ServiceTier string

	ProxyID            *int64
	EgressIdentifier   string
	RetryCount         int
	AccountConcurrency *int

	ExplicitSessionIDPresent bool
	ExplicitSessionIDHash    string
	SessionScopeHash         string
	SessionSourceHash        string
	PromptCacheKeyPresent    bool
	PromptCacheKeyHash       string
	IsSubagent               bool
	SubagentKind             string
	InboundTransport         string
	UpstreamTransport        string
	PromptCache              OpenAIPromptCacheObservation

	UpstreamErrorCode    string
	UpstreamErrorType    string
	UpstreamRequestID    string
	RetryAfter           string
	RateLimitHeaders     map[string][]string
	RateLimitHeadersJSON *string
}

// SetOpsOpenAIPromptCacheObservation 记录最终出站 body 的缓存诊断摘要。
// 必须在 body 完成 profile 投影后调用，避免把入口 prompt_cache_key 误当作上游 key。
func SetOpsOpenAIPromptCacheObservation(c *gin.Context, account *Account, snapshot *CodexOutboundSnapshot, body []byte, fallbackReason string) {
	if c == nil {
		return
	}
	obs := getOrCreateOpsRequestObservation(c)
	diagnostic := OpenAIPromptCacheObservation{
		OutboundBodyHash:         hashSensitiveValueForLog(string(body)),
		OutboundPrefixConfigHash: codexOutboundPrefixConfigHash(body),
		OutboundBodyBytes:        len(body),
		AttemptNumber:            obs.RetryCount + 1,
		OutboundFallbackReason:   strings.TrimSpace(fallbackReason),
	}
	if account != nil {
		diagnostic.OutboundAccountID = account.ID
	}
	if snapshot != nil {
		diagnostic.OutboundAccountID = snapshot.accountID
		diagnostic.OutboundPromptCacheKeyHash = hashSensitiveValueForLog(snapshot.promptCacheKey)
		diagnostic.OutboundProfile = strings.TrimSpace(snapshot.profile)
		diagnostic.OutboundModel = truncateString(strings.TrimSpace(snapshot.model), 128)
		diagnostic.OutboundRequestKind = truncateString(strings.TrimSpace(snapshot.requestKind), 32)
		diagnostic.OutboundServiceTier = truncateString(strings.TrimSpace(snapshot.serviceTier), 64)
		diagnostic.OutboundTransport = normalizeOpsOpenAIUpstreamTransport(snapshot.transport)
		diagnostic.FingerprintMode = truncateString(strings.TrimSpace(snapshot.fingerprintMode), 32)
		diagnostic.FingerprintVersion = truncateString(strings.TrimSpace(snapshot.fingerprintVersion), 32)
		diagnostic.SessionIDHash = hashSensitiveValueForLog(snapshot.sessionID)
		diagnostic.SessionScopeHash = truncateString(strings.TrimSpace(snapshot.sessionScopeHash), 128)
		diagnostic.SessionEpoch = snapshot.sessionEpoch
		diagnostic.SessionScopeVersion = snapshot.sessionScopeVersion
		diagnostic.SessionSlot = snapshot.sessionSlot
		diagnostic.SessionSlotCount = snapshot.sessionSlotCount
	} else {
		if diagnostic.OutboundFallbackReason == "" {
			diagnostic.OutboundFallbackReason = "legacy_profile"
		}
		diagnostic.OutboundProfile = CodexOutboundProfileLegacy
	}
	obs.PromptCache = diagnostic
	logOpsOpenAIPromptCacheOutbound(c, diagnostic, obs.PromptCacheKeyHash)
}

// logOpsOpenAIPromptCacheOutbound 记录大上下文或 profile fallback 的最终出站摘要。
// 普通请求只打 Debug，避免临时诊断埋点放大生产日志量。
func logOpsOpenAIPromptCacheOutbound(c *gin.Context, diagnostic OpenAIPromptCacheObservation, inboundKeyHash string) {
	if c == nil {
		return
	}
	requestID := strings.TrimSpace(c.GetHeader("X-Request-ID"))
	if requestID == "" && c.Request != nil {
		if value, ok := c.Request.Context().Value(ctxkey.RequestID).(string); ok {
			requestID = strings.TrimSpace(value)
		}
	}
	fields := []zap.Field{
		zap.String("request_id", requestID),
		zap.Int64("account_id", diagnostic.OutboundAccountID),
		zap.String("inbound_prompt_cache_key_hash", inboundKeyHash),
		zap.String("outbound_prompt_cache_key_hash", diagnostic.OutboundPromptCacheKeyHash),
		zap.String("outbound_body_hash", diagnostic.OutboundBodyHash),
		zap.String("outbound_prefix_config_hash", diagnostic.OutboundPrefixConfigHash),
		zap.Int("outbound_body_bytes", diagnostic.OutboundBodyBytes),
		zap.String("outbound_profile", diagnostic.OutboundProfile),
		zap.String("outbound_fallback_reason", diagnostic.OutboundFallbackReason),
		zap.String("outbound_model", diagnostic.OutboundModel),
		zap.String("outbound_request_kind", diagnostic.OutboundRequestKind),
		zap.String("outbound_service_tier", diagnostic.OutboundServiceTier),
		zap.String("outbound_transport", diagnostic.OutboundTransport),
		zap.Int("attempt_number", diagnostic.AttemptNumber),
		zap.String("fingerprint_mode", diagnostic.FingerprintMode),
		zap.String("fingerprint_version", diagnostic.FingerprintVersion),
		zap.String("session_id_hash", diagnostic.SessionIDHash),
		zap.String("session_scope_hash", diagnostic.SessionScopeHash),
		zap.Int64("session_epoch", diagnostic.SessionEpoch),
		zap.Int("session_scope_version", diagnostic.SessionScopeVersion),
		zap.Int("session_slot", diagnostic.SessionSlot),
		zap.Int("session_slot_count", diagnostic.SessionSlotCount),
	}
	log := logger.L().With(zap.String("component", "service.openai_gateway"))
	if diagnostic.OutboundBodyBytes >= openAIPromptCacheDiagnosticLargeBodyBytes || diagnostic.OutboundFallbackReason == "unknown_top_level_field" {
		log.Info("openai.prompt_cache_outbound", fields...)
		return
	}
	log.Debug("openai.prompt_cache_outbound", fields...)
}

// BeginOpsRequestObservation 在最外层网关中间件进入时冻结请求开始时间。
func BeginOpsRequestObservation(c *gin.Context, startedAt time.Time) {
	if c == nil {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	obs := getOrCreateOpsRequestObservation(c)
	obs.RequestStartedAt = startedAt.UTC()
}

// SetOpsOpenAIRequestMetadata 提取可安全持久化的 OpenAI 请求标识元数据。
func SetOpsOpenAIRequestMetadata(c *gin.Context, body []byte) {
	if c == nil {
		return
	}
	obs := getOrCreateOpsRequestObservation(c)

	sessionID := ExtractClientSessionID(c)
	if sessionID == "" && len(body) > 0 {
		sessionID = sanitizeSessionID(gjson.GetBytes(body, "client_metadata.session_id").String())
	}
	obs.ExplicitSessionIDPresent = sessionID != ""
	obs.ExplicitSessionIDHash = hashSensitiveValueForLog(sessionID)

	promptCacheKey := ""
	if len(body) > 0 {
		value := gjson.GetBytes(body, "prompt_cache_key")
		if value.Exists() && value.Type == gjson.String {
			promptCacheKey = strings.TrimSpace(value.String())
		}
	}
	obs.PromptCacheKeyPresent = promptCacheKey != ""
	obs.PromptCacheKeyHash = hashSensitiveValueForLog(promptCacheKey)
	obs.ServiceTier = truncateString(strings.TrimSpace(gjson.GetBytes(body, "service_tier").String()), 64)
	obs.InboundTransport = normalizeOpsOpenAIInboundTransport(GetOpenAIClientTransport(c))
	var headers http.Header
	if c.Request != nil {
		headers = c.Request.Header
	}
	original := extractCodexFingerprintOriginalIDs(headers, body)
	obs.IsSubagent = original.isSubagent
	obs.SubagentKind = truncateString(original.subagentKind, 64)
}

// SetOpsOpenAIForwardAttempt 记录当前上游尝试的安全出口标识和累计重试次数。
func SetOpsOpenAIForwardAttempt(c *gin.Context, proxyID *int64, retryCount int) {
	if c == nil {
		return
	}
	obs := getOrCreateOpsRequestObservation(c)
	resetOpsUpstreamResponseObservation(obs)
	obs.PromptCache = OpenAIPromptCacheObservation{}
	obs.ProxyID = nil
	obs.EgressIdentifier = "direct"
	if proxyID != nil && *proxyID > 0 {
		value := *proxyID
		obs.ProxyID = &value
		obs.EgressIdentifier = "proxy:" + strconv.FormatInt(value, 10)
	}
	if retryCount < 0 {
		retryCount = 0
	}
	obs.RetryCount = retryCount
}

// SetOpsAccountConcurrency 记录 Forward 执行失败前、槽位仍被占用时的账号并发快照。
func SetOpsAccountConcurrency(c *gin.Context, concurrency int) {
	if c == nil || concurrency < 0 {
		return
	}
	obs := getOrCreateOpsRequestObservation(c)
	value := concurrency
	obs.AccountConcurrency = &value
}

// CaptureOpsUpstreamResponse 仅采集诊断白名单响应头和结构化错误分类。
func CaptureOpsUpstreamResponse(c *gin.Context, headers http.Header, body []byte) {
	if c == nil {
		return
	}
	obs := getOrCreateOpsRequestObservation(c)
	resetOpsUpstreamResponseObservation(obs)
	obs.UpstreamRequestID = truncateString(strings.TrimSpace(headers.Get("x-request-id")), 255)
	obs.RetryAfter = sanitizeOpsRetryAfter(headers.Get("Retry-After"))
	obs.RateLimitHeaders = sanitizeOpsRateLimitHeaders(headers)

	if len(body) == 0 {
		return
	}
	obs.UpstreamErrorCode = firstOpsJSONScalar(body,
		"error.code", "response.error.code", "code")
	obs.UpstreamErrorType = firstOpsJSONScalar(body,
		"error.type", "response.error.type", "type")
}

func resetOpsUpstreamResponseObservation(obs *OpsRequestObservation) {
	if obs == nil {
		return
	}
	obs.UpstreamErrorCode = ""
	obs.UpstreamErrorType = ""
	obs.UpstreamRequestID = ""
	obs.RetryAfter = ""
	obs.RateLimitHeaders = nil
	obs.RateLimitHeadersJSON = nil
}

// GetOpsRequestObservation 返回可直接落库的请求级快照。
func GetOpsRequestObservation(c *gin.Context, completedAt time.Time) OpsRequestObservation {
	if c == nil {
		return OpsRequestObservation{}
	}
	value, ok := c.Get(opsRequestObservationKey)
	if !ok {
		return OpsRequestObservation{}
	}
	stored, ok := value.(*OpsRequestObservation)
	if !ok || stored == nil {
		return OpsRequestObservation{}
	}
	out := *stored
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	if !out.RequestStartedAt.IsZero() {
		out.DurationMs = completedAt.Sub(out.RequestStartedAt).Milliseconds()
		if out.DurationMs < 0 {
			out.DurationMs = 0
		}
	}
	out.SessionScopeHash = truncateString(stagedCodexFingerprintSessionScopeHash(c), 128)
	if source := existingCodexFingerprintLogicalTurnSource(c); source != "" {
		out.SessionSourceHash = hashSensitiveValueForLog(source)
	}
	if out.InboundTransport == "" {
		out.InboundTransport = normalizeOpsOpenAIInboundTransport(GetOpenAIClientTransport(c))
	}
	if snapshot := stagedCodexOutboundSnapshotAnyAccount(c); snapshot != nil {
		out.UpstreamTransport = normalizeOpsOpenAIUpstreamTransport(snapshot.transport)
		if snapshot.subagentKind != "" {
			out.IsSubagent = true
			out.SubagentKind = truncateString(snapshot.subagentKind, 64)
		}
	}
	out.RateLimitHeaders = cloneOpsRateLimitHeaders(out.RateLimitHeaders)
	if len(out.RateLimitHeaders) > 0 {
		if raw, err := json.Marshal(out.RateLimitHeaders); err == nil {
			value := string(raw)
			out.RateLimitHeadersJSON = &value
		}
	}
	return out
}

func normalizeOpsOpenAIInboundTransport(transport OpenAIClientTransport) string {
	switch transport {
	case OpenAIClientTransportHTTP:
		return "http"
	case OpenAIClientTransportWS:
		return "ws"
	default:
		return ""
	}
}

func normalizeOpsOpenAIUpstreamTransport(transport string) string {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "http", string(OpenAIUpstreamTransportHTTPSSE):
		return string(OpenAIUpstreamTransportHTTPSSE)
	case "ws":
		return "websocket"
	case "ws_v2", string(OpenAIUpstreamTransportResponsesWebsocketV2):
		return string(OpenAIUpstreamTransportResponsesWebsocketV2)
	case string(OpenAIUpstreamTransportResponsesWebsocket):
		return string(OpenAIUpstreamTransportResponsesWebsocket)
	default:
		return ""
	}
}

func getOrCreateOpsRequestObservation(c *gin.Context) *OpsRequestObservation {
	if value, ok := c.Get(opsRequestObservationKey); ok {
		if obs, typeOK := value.(*OpsRequestObservation); typeOK && obs != nil {
			return obs
		}
	}
	obs := &OpsRequestObservation{}
	c.Set(opsRequestObservationKey, obs)
	return obs
}

func existingCodexFingerprintLogicalTurnSource(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(codexFingerprintLogicalTurnSourceContextKey)
	if !ok {
		return ""
	}
	source, _ := value.(string)
	return strings.TrimSpace(source)
}

func firstOpsJSONScalar(body []byte, paths ...string) string {
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if !value.Exists() {
			continue
		}
		text := strings.TrimSpace(value.String())
		if text != "" {
			return truncateString(text, 128)
		}
	}
	return ""
}

func sanitizeOpsRetryAfter(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return truncateString(value, opsMaxRetryAfterBytes)
}

func sanitizeOpsRateLimitHeaders(headers http.Header) map[string][]string {
	if len(headers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		lower := strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(lower, "x-ratelimit-") {
			keys = append(keys, lower)
		}
	}
	sort.Strings(keys)
	if len(keys) > opsMaxRateLimitHeaderCount {
		keys = keys[:opsMaxRateLimitHeaderCount]
	}
	out := make(map[string][]string, len(keys))
	for _, key := range keys {
		values := headers.Values(key)
		cleaned := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
			if value == "" || strings.ContainsAny(value, "\r\n") {
				continue
			}
			cleaned = append(cleaned, truncateString(value, opsMaxRateLimitValueBytes))
		}
		if len(cleaned) > 0 {
			out[key] = cleaned
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneOpsRateLimitHeaders(input map[string][]string) map[string][]string {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string][]string, len(input))
	for key, values := range input {
		out[key] = append([]string(nil), values...)
	}
	return out
}
