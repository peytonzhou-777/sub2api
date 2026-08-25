package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

const (
	CodexOutboundProfileLegacy         = "legacy"
	CodexOutboundProfileCLI0149        = "codex_cli_0_149_0"
	codexOutboundProfileExtraKey       = "codex_outbound_profile"
	codexOutboundSnapshotContextKey    = "codex_outbound_snapshot"
	codexOutboundFallbackContextKey    = "codex_outbound_fallback"
	codexOutboundTurnStartedContextKey = "codex_outbound_turn_started_at_unix_ms"

	// 严格 profile 使用同一低熵客户端类别模板；它不表示账号来自同一设备或安装。
	codexCLI0149WindowsUserAgent = "codex_cli_rs/0.149.0 (Windows 10.0.26100; x86_64) WindowsTerminal"
	codexCLI0149Version          = "0.149.0"
)

var (
	codexOutboundDefaultProfile = func() *atomic.Value {
		value := &atomic.Value{}
		// 进程尚未装配运行时配置时保留 legacy，避免无配置测试桩改变协议语义。
		value.Store(CodexOutboundProfileLegacy)
		return value
	}()
	codexOutboundForceLegacy atomic.Bool
	codexOutboundZstdPool    = sync.Pool{New: func() any {
		encoder, err := zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)),
			zstd.WithEncoderConcurrency(1),
		)
		if err != nil {
			return err
		}
		return encoder
	}}
	defaultCodexOutboundMetrics codexOutboundMetrics
)

// CodexOutboundMetricsSnapshot 提供严格 profile 的进程内低基数统计。
type CodexOutboundMetricsSnapshot struct {
	StrictRequestsTotal           uint64 `json:"strict_requests_total"`
	LegacyRequestsTotal           uint64 `json:"legacy_requests_total"`
	HTTPRequestsTotal             uint64 `json:"http_requests_total"`
	WSRequestsTotal               uint64 `json:"ws_requests_total"`
	CompactRequestsTotal          uint64 `json:"compact_requests_total"`
	ModelsRequestsTotal           uint64 `json:"models_requests_total"`
	MetadataSynthesizedTotal      uint64 `json:"metadata_synthesized_total"`
	ForbiddenHeadersStrippedTotal uint64 `json:"forbidden_headers_stripped_total"`
	UnknownFieldsPreservedTotal   uint64 `json:"unknown_fields_preserved_total"`
	ProfileFallbackTotal          uint64 `json:"profile_fallback_total"`
	ZstdRequestsTotal             uint64 `json:"zstd_requests_total"`
	ZstdFallbackTotal             uint64 `json:"zstd_fallback_total"`
	ZstdInputBytesTotal           uint64 `json:"zstd_input_bytes_total"`
	ZstdOutputBytesTotal          uint64 `json:"zstd_output_bytes_total"`
	ZstdDurationMicrosTotal       uint64 `json:"zstd_duration_micros_total"`
	SerializeDurationMicrosTotal  uint64 `json:"serialize_duration_micros_total"`
	WSPrewarmTotal                uint64 `json:"ws_prewarm_total"`
}

type codexOutboundMetrics struct {
	strictRequests           atomic.Uint64
	legacyRequests           atomic.Uint64
	httpRequests             atomic.Uint64
	wsRequests               atomic.Uint64
	compactRequests          atomic.Uint64
	modelsRequests           atomic.Uint64
	metadataSynthesized      atomic.Uint64
	forbiddenHeadersStripped atomic.Uint64
	unknownFieldsPreserved   atomic.Uint64
	profileFallback          atomic.Uint64
	zstdRequests             atomic.Uint64
	zstdFallback             atomic.Uint64
	zstdInputBytes           atomic.Uint64
	zstdOutputBytes          atomic.Uint64
	zstdDurationMicros       atomic.Uint64
	serializeDurationMicros  atomic.Uint64
	wsPrewarm                atomic.Uint64
}

// SnapshotCodexOutboundMetrics 返回进程启动以来的累计统计，不包含账号或请求标识。
func SnapshotCodexOutboundMetrics() CodexOutboundMetricsSnapshot {
	return CodexOutboundMetricsSnapshot{
		StrictRequestsTotal:           defaultCodexOutboundMetrics.strictRequests.Load(),
		LegacyRequestsTotal:           defaultCodexOutboundMetrics.legacyRequests.Load(),
		HTTPRequestsTotal:             defaultCodexOutboundMetrics.httpRequests.Load(),
		WSRequestsTotal:               defaultCodexOutboundMetrics.wsRequests.Load(),
		CompactRequestsTotal:          defaultCodexOutboundMetrics.compactRequests.Load(),
		ModelsRequestsTotal:           defaultCodexOutboundMetrics.modelsRequests.Load(),
		MetadataSynthesizedTotal:      defaultCodexOutboundMetrics.metadataSynthesized.Load(),
		ForbiddenHeadersStrippedTotal: defaultCodexOutboundMetrics.forbiddenHeadersStripped.Load(),
		UnknownFieldsPreservedTotal:   defaultCodexOutboundMetrics.unknownFieldsPreserved.Load(),
		ProfileFallbackTotal:          defaultCodexOutboundMetrics.profileFallback.Load(),
		ZstdRequestsTotal:             defaultCodexOutboundMetrics.zstdRequests.Load(),
		ZstdFallbackTotal:             defaultCodexOutboundMetrics.zstdFallback.Load(),
		ZstdInputBytesTotal:           defaultCodexOutboundMetrics.zstdInputBytes.Load(),
		ZstdOutputBytesTotal:          defaultCodexOutboundMetrics.zstdOutputBytes.Load(),
		ZstdDurationMicrosTotal:       defaultCodexOutboundMetrics.zstdDurationMicros.Load(),
		SerializeDurationMicrosTotal:  defaultCodexOutboundMetrics.serializeDurationMicros.Load(),
		WSPrewarmTotal:                defaultCodexOutboundMetrics.wsPrewarm.Load(),
	}
}

// SetCodexOutboundProfileConfig 发布无触库的全局 profile 快照，供凭据面和管理端复用。
func SetCodexOutboundProfileConfig(defaultProfile string, forceLegacy bool) {
	profile := normalizeCodexOutboundProfile(defaultProfile)
	if profile == "" {
		profile = CodexOutboundProfileCLI0149
	}
	codexOutboundDefaultProfile.Store(profile)
	codexOutboundForceLegacy.Store(forceLegacy)
}

func normalizeCodexOutboundProfile(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexOutboundProfileLegacy:
		return CodexOutboundProfileLegacy
	case CodexOutboundProfileCLI0149:
		return CodexOutboundProfileCLI0149
	default:
		return ""
	}
}

// GetCodexOutboundProfileOverride 返回账号级故障隔离覆写；非法值按未配置处理。
func (a *Account) GetCodexOutboundProfileOverride() string {
	if a == nil || !a.IsOpenAIOAuth() || a.Extra == nil {
		return ""
	}
	raw, _ := a.Extra[codexOutboundProfileExtraKey].(string)
	return normalizeCodexOutboundProfile(raw)
}

// ResolveCodexOutboundProfile 按全局回滚、账号覆写、全局默认的固定优先级解析生效 profile。
func ResolveCodexOutboundProfile(account *Account) string {
	if account == nil || !account.IsOpenAIOAuth() {
		return CodexOutboundProfileLegacy
	}
	if codexOutboundForceLegacy.Load() {
		return CodexOutboundProfileLegacy
	}
	if override := account.GetCodexOutboundProfileOverride(); override != "" {
		return override
	}
	if profile, ok := codexOutboundDefaultProfile.Load().(string); ok {
		if normalized := normalizeCodexOutboundProfile(profile); normalized != "" {
			return normalized
		}
	}
	return CodexOutboundProfileCLI0149
}

func (s *OpenAIGatewayService) resolveCodexOutboundProfile(account *Account) string {
	if s == nil || s.cfg == nil {
		return CodexOutboundProfileLegacy
	}
	if account == nil || !account.IsOpenAIOAuth() || s.cfg.Gateway.CodexOutboundForceLegacy {
		return CodexOutboundProfileLegacy
	}
	if override := account.GetCodexOutboundProfileOverride(); override != "" {
		return override
	}
	if profile := normalizeCodexOutboundProfile(s.cfg.Gateway.CodexOutboundProfileDefault); profile != "" {
		return profile
	}
	// 生产配置加载器会显式写入严格默认值；手工构造的零值 Config 保持 legacy，
	// 避免测试桩和未经过标准加载器的嵌入式调用意外改变协议语义。
	return CodexOutboundProfileLegacy
}

func isCodexOutboundGlobalStrict() bool {
	if codexOutboundForceLegacy.Load() {
		return false
	}
	profile, _ := codexOutboundDefaultProfile.Load().(string)
	return normalizeCodexOutboundProfile(profile) == CodexOutboundProfileCLI0149
}

// CodexOutboundProfileStatus 是管理端可公开的安全 profile 视图。
type CodexOutboundProfileStatus struct {
	Profile   string `json:"profile"`
	UserAgent string `json:"user_agent"`
	Zstd      bool   `json:"zstd"`
}

// CodexOutboundProfileStatusForAccount 不返回 seed 或任何请求级标识。
func CodexOutboundProfileStatusForAccount(account *Account) *CodexOutboundProfileStatus {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	profile := ResolveCodexOutboundProfile(account)
	status := &CodexOutboundProfileStatus{Profile: profile}
	if profile == CodexOutboundProfileCLI0149 {
		status.UserAgent = codexCLI0149WindowsUserAgent
		status.Zstd = true
	}
	return status
}

// CodexOutboundSnapshot 是最终账号选定后生成的只读应用层出站快照。
// HTTP、WS、compact 的请求头和 body 只从该快照投影，不再次读取下游身份头。
type CodexOutboundSnapshot struct {
	profile             string
	accountID           int64
	transport           string
	requestKind         string
	fingerprintMode     string
	fingerprintVersion  string
	sessionScopeHash    string
	sessionEpoch        int64
	sessionScopeVersion int
	sessionSlot         int
	sessionSlotCount    int
	installationID      string
	sessionID           string
	threadID            string
	parentThreadID      string
	forkedThreadID      string
	turnID              string
	parentTurnID        string
	rootTurnID          string
	windowID            string
	promptCacheKey      string
	turnStartedAtUnixMs int64
	model               string
	serviceTier         string
	subagentHeader      string
	subagentKind        string
	threadSource        string
	turnMetadata        string
	zstd                bool
	orderedBody         []byte
}

type codexOutboundFallback struct {
	accountID int64
	reason    string
}

func stageCodexOutboundSnapshot(c *gin.Context, snapshot *CodexOutboundSnapshot) {
	if c != nil {
		c.Set(codexOutboundSnapshotContextKey, snapshot)
	}
}

func stageCodexOutboundFallback(c *gin.Context, fallback *codexOutboundFallback) {
	if c != nil {
		c.Set(codexOutboundFallbackContextKey, fallback)
	}
}

func stagedCodexOutboundFallback(c *gin.Context, account *Account) *codexOutboundFallback {
	if c == nil || account == nil {
		return nil
	}
	value, exists := c.Get(codexOutboundFallbackContextKey)
	fallback, ok := value.(*codexOutboundFallback)
	if !exists || !ok || fallback == nil || fallback.accountID != account.ID {
		return nil
	}
	return fallback
}

func (s *OpenAIGatewayService) resolveCodexOutboundProfileForRequest(c *gin.Context, account *Account) string {
	if stagedCodexOutboundFallback(c, account) != nil {
		return CodexOutboundProfileLegacy
	}
	return s.resolveCodexOutboundProfile(account)
}

func stagedCodexOutboundSnapshot(c *gin.Context, account *Account) *CodexOutboundSnapshot {
	if c == nil || account == nil {
		return nil
	}
	value, exists := c.Get(codexOutboundSnapshotContextKey)
	snapshot, ok := value.(*CodexOutboundSnapshot)
	if !exists || !ok || snapshot == nil || snapshot.accountID != account.ID {
		return nil
	}
	return snapshot
}

func stagedCodexOutboundSnapshotAnyAccount(c *gin.Context) *CodexOutboundSnapshot {
	if c == nil {
		return nil
	}
	value, exists := c.Get(codexOutboundSnapshotContextKey)
	snapshot, ok := value.(*CodexOutboundSnapshot)
	if !exists || !ok {
		return nil
	}
	return snapshot
}

func stagedCodexOutboundTopologyScope(c *gin.Context, account *Account) string {
	snapshot := stagedCodexOutboundSnapshot(c, account)
	if snapshot == nil {
		return ""
	}
	return normalizeOpenAIWSTopologyScopeValues(
		snapshot.installationID,
		snapshot.threadID,
		snapshot.parentThreadID,
		snapshot.subagentHeader,
	)
}

func stableCodexOutboundTurnStartedAt(c *gin.Context, fallback int64) int64 {
	if c != nil {
		if value, exists := c.Get(codexOutboundTurnStartedContextKey); exists {
			switch typed := value.(type) {
			case int64:
				if typed > 0 {
					return typed
				}
			case int:
				if typed > 0 {
					return int64(typed)
				}
			}
		}
	}
	if fallback <= 0 {
		fallback = time.Now().UnixMilli()
	}
	if c != nil {
		c.Set(codexOutboundTurnStartedContextKey, fallback)
	}
	return fallback
}

func buildCodexOutboundSnapshot(c *gin.Context, account *Account, body []byte, transport string, compact bool) *CodexOutboundSnapshot {
	if account == nil {
		return nil
	}
	snapshot := &CodexOutboundSnapshot{
		profile:     ResolveCodexOutboundProfile(account),
		accountID:   account.ID,
		transport:   strings.TrimSpace(transport),
		requestKind: "turn",
		model:       strings.TrimSpace(gjson.GetBytes(body, "model").String()),
		serviceTier: strings.TrimSpace(gjson.GetBytes(body, "service_tier").String()),
	}
	if compact {
		snapshot.requestKind = "compaction"
	} else if generate := gjson.GetBytes(body, "generate"); generate.Exists() && generate.Type == gjson.False {
		snapshot.requestKind = "prewarm"
	}

	ids := stagedCodexFingerprintIDsForAccount(c, account)
	if ids != nil {
		snapshot.fingerprintMode = string(ids.mode)
		snapshot.fingerprintVersion = codexFingerprintAlgorithmV3
		snapshot.sessionScopeHash = ids.sessionScopeHash
		snapshot.sessionEpoch = ids.sessionEpoch
		snapshot.sessionScopeVersion = ids.sessionScopeVersion
		snapshot.sessionSlot = ids.sessionSlot
		snapshot.sessionSlotCount = ids.sessionSlotCount
		snapshot.installationID = ids.installationID
		snapshot.sessionID = ids.sessionID
		snapshot.threadID = ids.threadID
		snapshot.parentThreadID = ids.parentThreadID
		snapshot.forkedThreadID = ids.forkedThreadID
		snapshot.turnID = ids.turnID
		snapshot.parentTurnID = ids.parentTurnID
		snapshot.rootTurnID = ids.rootTurnID
		snapshot.windowID = ids.windowID
		snapshot.promptCacheKey = ids.promptCacheKey
		snapshot.subagentHeader = ids.subagentHeader
		snapshot.subagentKind = ids.subagentKind
		snapshot.threadSource = ids.threadSource
		snapshot.turnStartedAtUnixMs = ids.turnStartedAtUnixMs
	}

	if snapshot.promptCacheKey == "" {
		// 共享特征优先：Codex OAuth 缺省缓存键与当前 Session 收口为同一生命周期。
		snapshot.promptCacheKey = snapshot.sessionID
	}
	snapshot.turnStartedAtUnixMs = stableCodexOutboundTurnStartedAt(c, snapshot.turnStartedAtUnixMs)
	snapshot.turnMetadata = marshalCodexOutboundTurnMetadata(snapshot)
	return snapshot
}

func marshalCodexOutboundTurnMetadata(snapshot *CodexOutboundSnapshot) string {
	if snapshot == nil {
		return ""
	}
	metadata := struct {
		InstallationID      string `json:"installation_id,omitempty"`
		SessionID           string `json:"session_id,omitempty"`
		ThreadID            string `json:"thread_id,omitempty"`
		TurnID              string `json:"turn_id,omitempty"`
		WindowID            string `json:"window_id,omitempty"`
		RequestKind         string `json:"request_kind"`
		ForkedFromThreadID  string `json:"forked_from_thread_id,omitempty"`
		ParentThreadID      string `json:"parent_thread_id,omitempty"`
		ParentTurnID        string `json:"parent_turn_id,omitempty"`
		RootTurnID          string `json:"root_turn_id,omitempty"`
		SubagentKind        string `json:"subagent_kind,omitempty"`
		ThreadSource        string `json:"thread_source,omitempty"`
		TurnStartedAtUnixMs int64  `json:"turn_started_at_unix_ms"`
	}{
		InstallationID:      snapshot.installationID,
		SessionID:           snapshot.sessionID,
		ThreadID:            snapshot.threadID,
		TurnID:              snapshot.turnID,
		WindowID:            snapshot.windowID,
		RequestKind:         snapshot.requestKind,
		ForkedFromThreadID:  snapshot.forkedThreadID,
		ParentThreadID:      snapshot.parentThreadID,
		ParentTurnID:        snapshot.parentTurnID,
		RootTurnID:          snapshot.rootTurnID,
		SubagentKind:        snapshot.subagentKind,
		ThreadSource:        snapshot.threadSource,
		TurnStartedAtUnixMs: snapshot.turnStartedAtUnixMs,
	}
	raw, err := marshalOpenAIUpstreamJSON(metadata)
	if err != nil {
		return ""
	}
	return string(raw)
}

func buildCodexOutboundClientMetadata(snapshot *CodexOutboundSnapshot) []byte {
	if snapshot == nil {
		return nil
	}
	metadata := map[string]any{"x-codex-turn-metadata": snapshot.turnMetadata}
	for key, value := range map[string]string{
		"x-codex-installation-id": snapshot.installationID,
		"session_id":              snapshot.sessionID,
		"thread_id":               snapshot.threadID,
		"parent_thread_id":        snapshot.parentThreadID,
		"forked_from_thread_id":   snapshot.forkedThreadID,
		"turn_id":                 snapshot.turnID,
		"parent_turn_id":          snapshot.parentTurnID,
		"root_turn_id":            snapshot.rootTurnID,
		"x-codex-window-id":       snapshot.windowID,
		"x-openai-subagent":       snapshot.subagentHeader,
	} {
		if value != "" {
			metadata[key] = value
		}
	}
	raw, err := marshalOpenAIUpstreamJSON(metadata)
	if err != nil {
		return nil
	}
	return raw
}

var codexHTTPResponseFieldOrder = []string{
	"model", "instructions", "input", "tools", "tool_choice", "parallel_tool_calls",
	"reasoning", "store", "stream", "stream_options", "include", "service_tier",
	"prompt_cache_key", "text", "client_metadata",
}

var codexWSResponseFieldOrder = []string{
	"type", "model", "instructions", "previous_response_id", "input", "tools", "tool_choice",
	"parallel_tool_calls", "reasoning", "store", "stream", "stream_options", "include",
	"service_tier", "prompt_cache_key", "text", "generate", "client_metadata",
}

var codexCompactFieldOrder = []string{
	"model", "input", "instructions", "tools", "parallel_tool_calls", "reasoning",
	"service_tier", "prompt_cache_key", "text", "previous_response_id",
}

// prepareCodexOutboundBody 在所有业务改写完成后执行纯本地严格投影和有序编码。
func (s *OpenAIGatewayService) prepareCodexOutboundBody(c *gin.Context, account *Account, body []byte, transport string, compact bool) ([]byte, *CodexOutboundSnapshot, error) {
	stageCodexOutboundFallback(c, nil)
	if s.resolveCodexOutboundProfile(account) != CodexOutboundProfileCLI0149 {
		if account != nil && account.IsOpenAIOAuth() {
			defaultCodexOutboundMetrics.legacyRequests.Add(1)
		}
		stageCodexOutboundSnapshot(c, nil)
		SetOpsOpenAIPromptCacheObservation(c, account, nil, body, "legacy_profile")
		return body, nil, nil
	}
	order := codexHTTPResponseFieldOrder
	if compact {
		order = codexCompactFieldOrder
	} else if strings.HasPrefix(strings.ToLower(strings.TrimSpace(transport)), "ws") || gjson.GetBytes(body, "type").String() == "response.create" {
		order = codexWSResponseFieldOrder
	}
	unknown, unknownErr := unknownCodexOutboundTopLevelFields(body, order)
	if unknownErr == nil && len(unknown) > 0 {
		requestKind := "turn"
		if compact {
			requestKind = "compaction"
		} else if generate := gjson.GetBytes(body, "generate"); generate.Exists() && generate.Type == gjson.False {
			requestKind = "prewarm"
		}
		stageCodexOutboundSnapshot(c, nil)
		stageCodexOutboundFallback(c, &codexOutboundFallback{accountID: account.ID, reason: "unknown_top_level_field"})
		defaultCodexOutboundMetrics.profileFallback.Add(1)
		defaultCodexOutboundMetrics.legacyRequests.Add(1)
		SetOpsOpenAIPromptCacheObservation(c, account, nil, body, "unknown_top_level_field")
		logger.L().Warn("Codex 严格 profile 遇到未识别顶层字段，整请求回退 legacy",
			zap.Int64("account_id", account.ID),
			zap.String("profile", CodexOutboundProfileCLI0149),
			zap.String("transport", strings.TrimSpace(transport)),
			zap.String("request_kind", requestKind),
			zap.String("reason", "unknown_top_level_field"),
			zap.Int("unknown_field_count", len(unknown)),
		)
		return body, nil, nil
	}
	serializeStartedAt := time.Now()
	defer func() {
		defaultCodexOutboundMetrics.serializeDurationMicros.Add(uint64(time.Since(serializeStartedAt).Microseconds()))
	}()
	snapshot := buildCodexOutboundSnapshot(c, account, body, transport, compact)
	if snapshot != nil {
		snapshot.profile = s.resolveCodexOutboundProfile(account)
	}
	if snapshot == nil {
		stageCodexOutboundSnapshot(c, nil)
		return body, nil, nil
	}
	defaultCodexOutboundMetrics.strictRequests.Add(1)
	if compact {
		defaultCodexOutboundMetrics.compactRequests.Add(1)
	} else if strings.HasPrefix(strings.ToLower(strings.TrimSpace(transport)), "ws") {
		defaultCodexOutboundMetrics.wsRequests.Add(1)
		if snapshot.requestKind == "prewarm" {
			defaultCodexOutboundMetrics.wsPrewarm.Add(1)
		}
	} else {
		defaultCodexOutboundMetrics.httpRequests.Add(1)
	}

	ordered, err := projectCodexOutboundBody(body, snapshot, transport, compact, nil)
	if err != nil {
		stageCodexOutboundSnapshot(c, nil)
		return body, nil, err
	}
	copied := *snapshot
	copied.orderedBody = append([]byte(nil), ordered...)
	snapshot = &copied
	stageCodexOutboundSnapshot(c, snapshot)
	SetOpsOpenAIPromptCacheObservation(c, account, snapshot, ordered, "")
	return ordered, snapshot, nil
}

// codexOutboundPrefixConfigHash 返回影响缓存前缀、但不包含动态 input 和 client_metadata 的配置摘要哈希。
// input 会随多轮上下文增长，单独排除后可用于判断模型/工具/reasoning 等静态前缀是否漂移。
func codexOutboundPrefixConfigHash(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	paths := []string{
		"type", "model", "instructions", "previous_response_id", "tools", "tool_choice",
		"parallel_tool_calls", "reasoning", "store", "stream", "stream_options", "include",
		"service_tier", "prompt_cache_key", "text",
	}
	material := make(map[string]json.RawMessage, len(paths))
	for _, path := range paths {
		value := gjson.GetBytes(body, path)
		if !value.Exists() {
			continue
		}
		raw := strings.TrimSpace(value.Raw)
		if raw == "" || !json.Valid([]byte(raw)) {
			encodedValue, err := json.Marshal(value.Value())
			if err != nil {
				continue
			}
			raw = string(encodedValue)
		}
		material[path] = json.RawMessage(raw)
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return ""
	}
	return hashSensitiveValueForLog(string(encoded))
}

// projectCodexOutboundBody 将同一份快照投影到 HTTP、WS 或 compact 请求体。
func projectCodexOutboundBody(body []byte, snapshot *CodexOutboundSnapshot, transport string, compact bool, reference []byte) ([]byte, error) {
	if snapshot == nil {
		return body, nil
	}
	next := body
	if compact {
		if snapshot.promptCacheKey != "" {
			var err error
			next, err = sjson.SetBytes(next, "prompt_cache_key", snapshot.promptCacheKey)
			if err != nil {
				return body, err
			}
		} else if gjson.GetBytes(next, "prompt_cache_key").Exists() {
			var err error
			next, err = sjson.DeleteBytes(next, "prompt_cache_key")
			if err != nil {
				return body, err
			}
		}
		return marshalOrderedCodexJSONWithReference(next, reference, codexCompactFieldOrder)
	}

	clientMetadata := buildCodexOutboundClientMetadata(snapshot)
	if len(clientMetadata) > 0 {
		var err error
		next, err = sjson.SetRawBytes(next, "client_metadata", clientMetadata)
		if err != nil {
			return body, err
		}
		defaultCodexOutboundMetrics.metadataSynthesized.Add(1)
	}
	if snapshot.promptCacheKey != "" {
		var err error
		next, err = sjson.SetBytes(next, "prompt_cache_key", snapshot.promptCacheKey)
		if err != nil {
			return body, err
		}
	} else if gjson.GetBytes(next, "prompt_cache_key").Exists() {
		var err error
		next, err = sjson.DeleteBytes(next, "prompt_cache_key")
		if err != nil {
			return body, err
		}
	}
	if !gjson.GetBytes(next, "reasoning").IsObject() {
		var err error
		next, err = sjson.SetRawBytes(next, "reasoning", []byte(`{}`))
		if err != nil {
			return body, err
		}
	}
	var err error
	if next, err = sjson.SetBytes(next, "store", false); err != nil {
		return body, err
	}
	if next, err = sjson.SetBytes(next, "stream", true); err != nil {
		return body, err
	}
	if next, err = sjson.SetRawBytes(next, "include", []byte(`["reasoning.encrypted_content"]`)); err != nil {
		return body, err
	}
	if gjson.GetBytes(next, "stream_options").Exists() {
		if next, err = sjson.DeleteBytes(next, "stream_options"); err != nil {
			return body, err
		}
	}
	order := codexHTTPResponseFieldOrder
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(transport)), "ws") || gjson.GetBytes(next, "type").String() == "response.create" {
		order = codexWSResponseFieldOrder
	}
	return marshalOrderedCodexJSONWithReference(next, reference, order)
}

// marshalOrderedCodexJSON 保留每个值的原始 JSON 子树，仅重排顶层字段。
// 未识别字段按稳定字典序追加，避免协议升级时静默丢失业务语义。
func marshalOrderedCodexJSON(body []byte, order []string) ([]byte, error) {
	return marshalOrderedCodexJSONWithReference(body, nil, order)
}

func unknownCodexOutboundTopLevelFields(body []byte, order []string) ([]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	fields := make(map[string]json.RawMessage)
	if err := decoder.Decode(&fields); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	known := make(map[string]struct{}, len(order))
	for _, key := range order {
		known[key] = struct{}{}
	}
	unknown := make([]string, 0)
	for key := range fields {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown, nil
}

// marshalOrderedCodexJSONWithReference 在值语义未变化时借用参考体的原始子树。
// 这使 WS 中间 map 转换不会重新编码 tools/input 等大字段。
func marshalOrderedCodexJSONWithReference(body []byte, reference []byte, order []string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	fields := make(map[string]json.RawMessage)
	if err := decoder.Decode(&fields); err != nil {
		return body, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return body, errors.New("multiple JSON values")
		}
		return body, err
	}
	if len(reference) > 0 {
		var referenceFields map[string]json.RawMessage
		if err := json.Unmarshal(reference, &referenceFields); err == nil {
			for key, current := range fields {
				previous, exists := referenceFields[key]
				if !exists {
					continue
				}
				currentNormalized, currentErr := normalizeOpenAIWSJSONForCompare(current)
				previousNormalized, previousErr := normalizeOpenAIWSJSONForCompare(previous)
				if currentErr == nil && previousErr == nil && bytes.Equal(currentNormalized, previousNormalized) {
					fields[key] = previous
				}
			}
		}
	}

	orderedSet := make(map[string]struct{}, len(order))
	keys := make([]string, 0, len(fields))
	for _, key := range order {
		orderedSet[key] = struct{}{}
		if _, exists := fields[key]; exists {
			keys = append(keys, key)
		}
	}
	unknown := make([]string, 0)
	for key := range fields {
		if _, known := orderedSet[key]; !known {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	defaultCodexOutboundMetrics.unknownFieldsPreserved.Add(uint64(len(unknown)))
	keys = append(keys, unknown...)

	var out bytes.Buffer
	out.Grow(len(body))
	out.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			out.WriteByte(',')
		}
		encodedKey, _ := json.Marshal(key)
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(bytes.TrimSpace(fields[key]))
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// marshalCodexOutboundWSPayload 在真正写帧前完成最后一次严格投影和有序编码。
func (s *OpenAIGatewayService) marshalCodexOutboundWSPayload(
	c *gin.Context,
	account *Account,
	payload map[string]any,
	transport string,
	requestKind string,
) ([]byte, error) {
	raw, err := marshalOpenAIUpstreamJSON(payload)
	if err != nil || s.resolveCodexOutboundProfileForRequest(c, account) != CodexOutboundProfileCLI0149 {
		return raw, err
	}
	snapshot := stagedCodexOutboundSnapshot(c, account)
	if snapshot == nil {
		snapshot = buildCodexOutboundSnapshot(c, account, raw, transport, false)
	}
	if snapshot == nil {
		return raw, nil
	}
	copied := *snapshot
	if kind := strings.TrimSpace(requestKind); kind != "" && kind != copied.requestKind {
		copied.requestKind = kind
		copied.turnMetadata = marshalCodexOutboundTurnMetadata(&copied)
	}
	projected, err := projectCodexOutboundBody(raw, &copied, transport, false, snapshot.orderedBody)
	if err != nil {
		return nil, err
	}
	// 官方 0.149.0 在每次真正写 WS 帧前记录传输开始时间；该值不参与身份或缓存派生。
	return sjson.SetBytes(projected, "client_metadata.x-codex-ws-stream-request-start-ms", strconv.FormatInt(time.Now().UnixMilli(), 10))
}

func compressCodexOutboundBody(body []byte) ([]byte, error) {
	value := codexOutboundZstdPool.Get()
	if err, ok := value.(error); ok {
		return body, err
	}
	encoder, ok := value.(*zstd.Encoder)
	if !ok || encoder == nil {
		return body, errors.New("codex zstd encoder unavailable")
	}
	compressed := encoder.EncodeAll(body, make([]byte, 0, len(body)))
	codexOutboundZstdPool.Put(encoder)
	return compressed, nil
}

// compressCodexOutboundHTTPRequest 必须在所有读取 body 的语义判定之后调用。
// 压缩初始化失败时只回退未压缩 body，不触发换号或调度惩罚。
func (s *OpenAIGatewayService) compressCodexOutboundHTTPRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	req *http.Request,
	body []byte,
	compact bool,
) {
	snapshot := stagedCodexOutboundSnapshot(c, account)
	if req == nil || snapshot == nil || compact || snapshot.profile != CodexOutboundProfileCLI0149 {
		return
	}
	compressStartedAt := time.Now()
	compressed, compressErr := compressCodexOutboundBody(body)
	compressDuration := time.Since(compressStartedAt)
	if compressErr != nil {
		defaultCodexOutboundMetrics.zstdFallback.Add(1)
		logger.FromContext(ctx).Warn("Codex 严格 profile zstd 压缩失败，回退未压缩请求",
			zap.String("component", "service.openai_codex_outbound"),
			zap.Int64("account_id", account.ID),
			zap.String("profile", snapshot.profile),
			zap.String("reason", compressErr.Error()),
		)
		return
	}
	defaultCodexOutboundMetrics.zstdRequests.Add(1)
	defaultCodexOutboundMetrics.zstdInputBytes.Add(uint64(len(body)))
	defaultCodexOutboundMetrics.zstdOutputBytes.Add(uint64(len(compressed)))
	defaultCodexOutboundMetrics.zstdDurationMicros.Add(uint64(compressDuration.Microseconds()))
	copied := *snapshot
	copied.zstd = true
	stageCodexOutboundSnapshot(c, &copied)
	req.Body = io.NopCloser(bytes.NewReader(compressed))
	req.ContentLength = int64(len(compressed))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(compressed)), nil
	}
}

// applyCodexOutboundProbeProfile 让账号测试与后台用量探测复用业务请求的严格出站语义。
func (s *OpenAIGatewayService) applyCodexOutboundProbeProfile(ctx context.Context, account *Account, req *http.Request, body []byte) error {
	if s == nil || account == nil || req == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	probeContext, _ := gin.CreateTestContext(nil)
	probeContext.Request = req
	SetOpenAIClientTransport(probeContext, OpenAIClientTransportHTTP)
	ids, err := s.prepareCodexFingerprintForAttempt(ctx, probeContext, account, body, true)
	if err != nil {
		return fmt.Errorf("prepare Codex fingerprint: %w", err)
	}
	if ids != nil {
		applyCodexFingerprintHeaders(req.Header, ids)
	}
	projected, _, err := s.prepareCodexOutboundBody(probeContext, account, body, "http", false)
	if err != nil {
		return fmt.Errorf("prepare Codex outbound body: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(projected))
	req.ContentLength = int64(len(projected))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(projected)), nil
	}
	s.compressCodexOutboundHTTPRequest(ctx, probeContext, account, req, projected, false)
	s.finalizeCodexOutboundHeaders(probeContext, account, req.Header, false, "http", "", "")
	return nil
}

// finalizeCodexOutboundHeaders 在所有旧逻辑与账号覆写之后执行严格白名单收口。
func (s *OpenAIGatewayService) finalizeCodexOutboundHeaders(
	c *gin.Context,
	account *Account,
	headers http.Header,
	compact bool,
	transport string,
	model string,
	serviceTier string,
) {
	if headers == nil {
		return
	}
	// 无论 strict/legacy/fallback 如何重建 Header，父系授权都在函数返回前最终收口。
	defer stripOpenAICodexLineageHeaders(c, account, headers)
	if fallback := stagedCodexOutboundFallback(c, account); fallback != nil {
		// 请求级兼容回退必须同时恢复 identity/body/compression，不能留下严格 profile 混合形态。
		identity := resolveCodexOutboundIdentity(account.GetOpenAIUserAgent())
		headers.Set("User-Agent", identity.userAgent)
		headers.Set("originator", identity.originator)
		headers.Set("version", identity.version)
		deleteOpenAIHeaderEqualFold(headers, "content-encoding")
		return
	}
	if s.resolveCodexOutboundProfileForRequest(c, account) != CodexOutboundProfileCLI0149 {
		explicitLegacy := account != nil && account.GetCodexOutboundProfileOverride() == CodexOutboundProfileLegacy
		forcedLegacy := s != nil && s.cfg != nil && s.cfg.Gateway.CodexOutboundForceLegacy
		if (explicitLegacy || forcedLegacy) && strings.TrimSpace(headers.Get("originator")) != "" {
			overrideUA := ""
			if account != nil {
				overrideUA = account.GetOpenAIUserAgent()
			}
			identity := resolveCodexOutboundIdentity(overrideUA)
			headers.Set("User-Agent", identity.userAgent)
			headers.Set("originator", identity.originator)
			headers.Set("version", identity.version)
			deleteOpenAIHeaderEqualFold(headers, "content-encoding")
		}
		return
	}
	snapshot := stagedCodexOutboundSnapshot(c, account)
	if snapshot == nil {
		snapshot = buildCodexOutboundSnapshot(c, account, nil, transport, compact)
		if snapshot != nil {
			snapshot.profile = s.resolveCodexOutboundProfile(account)
		}
		stageCodexOutboundSnapshot(c, snapshot)
	}
	if snapshot == nil {
		return
	}
	normalizedTransport := strings.TrimSpace(transport)
	refineTransport := normalizedTransport != "" && (snapshot.transport == "" ||
		(strings.EqualFold(strings.TrimSpace(snapshot.transport), "ws") && !strings.EqualFold(snapshot.transport, normalizedTransport)))
	if refineTransport || (snapshot.model == "" && model != "") || (snapshot.serviceTier == "" && serviceTier != "") {
		// 快照生成后不原地修改；低频预连接缺少 body 时复制并补齐握手投影输入。
		copied := *snapshot
		if refineTransport {
			copied.transport = normalizedTransport
		}
		if copied.model == "" {
			copied.model = strings.TrimSpace(model)
		}
		if copied.serviceTier == "" {
			copied.serviceTier = strings.TrimSpace(serviceTier)
		}
		snapshot = &copied
		stageCodexOutboundSnapshot(c, snapshot)
	}
	for _, name := range []string{
		"user-agent", "originator", "version", "accept-language", "session-id", "session_id",
		"conversation_id", "thread-id", "x-client-request-id", "x-codex-window-id",
		"x-codex-installation-id", "x-codex-parent-thread-id", "x-codex-turn-metadata",
		"x-openai-subagent", "x-codex-beta-features", "x-responsesapi-include-timing-metrics",
		"traceparent", "tracestate", openAICodexRoutingHintHeader,
	} {
		for key := range headers {
			if strings.EqualFold(strings.TrimSpace(key), name) {
				defaultCodexOutboundMetrics.forbiddenHeadersStripped.Add(1)
				break
			}
		}
		deleteOpenAIHeaderEqualFold(headers, name)
	}
	headers.Set("User-Agent", codexCLI0149WindowsUserAgent)
	headers.Set("originator", "codex_cli_rs")
	headers.Set("x-codex-beta-features", openAIRemoteCompactionV2Feature)
	if snapshot.sessionID != "" {
		headers.Set("session-id", snapshot.sessionID)
	}
	if snapshot.threadID != "" {
		headers.Set("thread-id", snapshot.threadID)
	}
	if snapshot.windowID != "" {
		headers.Set("x-codex-window-id", snapshot.windowID)
	}
	if snapshot.parentThreadID != "" {
		headers.Set("x-codex-parent-thread-id", snapshot.parentThreadID)
	}
	if snapshot.subagentHeader != "" {
		headers.Set("x-openai-subagent", snapshot.subagentHeader)
	}
	if snapshot.turnMetadata != "" {
		headers.Set("x-codex-turn-metadata", snapshot.turnMetadata)
	}

	if compact {
		deleteOpenAIHeaderEqualFold(headers, "accept")
		deleteOpenAIHeaderEqualFold(headers, "x-client-request-id")
		if snapshot.installationID != "" {
			headers.Set("x-codex-installation-id", snapshot.installationID)
		}
	} else if snapshot.threadID != "" {
		headers.Set("x-client-request-id", snapshot.threadID)
	}

	if strings.HasPrefix(strings.ToLower(snapshot.transport), "ws") {
		deleteOpenAIHeaderEqualFold(headers, "accept")
		deleteOpenAIHeaderEqualFold(headers, "content-encoding")
		deleteOpenAIHeaderEqualFold(headers, "content-type")
		headers.Set("OpenAI-Beta", openAIWSBetaV2Value)
	} else {
		deleteOpenAIHeaderEqualFold(headers, "OpenAI-Beta")
		headers.Set("Content-Type", "application/json")
		if compact {
			deleteOpenAIHeaderEqualFold(headers, "content-encoding")
		} else {
			headers.Set("Accept", "text/event-stream")
			if snapshot.zstd {
				headers.Set("Content-Encoding", "zstd")
			}
		}
	}
	setOpenAICodexRoutingHint(headers, account, snapshot.model, snapshot.serviceTier)
}
