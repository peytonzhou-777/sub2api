package service

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// codexFingerprintIDsContextKey 是暂存在 gin context 的收敛 ID 集合键。
// 由 Forward（非透传）或 forwardOpenAIPassthrough（透传）解析后写入，请求
// 构造器读取用于出站头改写——请求体与出站头必须共享同一份 IDs，保证
// turn_id 等随机字段一致。
const codexFingerprintIDsContextKey = "codex_fingerprint_ids"

// stageCodexFingerprintIDs 将本 attempt 解析出的收敛 ID 暂存到 gin context。
// 必须无条件覆写（含 nil）：failover 从收敛账号切到 off 账号时，上一账号的
// IDs 不得残留并被误应用到新账号的出站头（typed-nil 由应用侧 nil 守卫吸收）。
func stageCodexFingerprintIDs(c *gin.Context, ids *codexFingerprintIDs) {
	if c != nil {
		c.Set(codexFingerprintIDsContextKey, ids)
	}
}

func stagedCodexFingerprintIDsForAccount(c *gin.Context, account *Account) *codexFingerprintIDs {
	if c == nil || account == nil || account.Type != AccountTypeOAuth {
		return nil
	}
	value, ok := c.Get(codexFingerprintIDsContextKey)
	if !ok {
		return nil
	}
	ids, ok := value.(*codexFingerprintIDs)
	if !ok || ids == nil || ids.accountID != account.ID {
		return nil
	}
	return ids
}

// applyStagedCodexFingerprintHeaders 读取 context 暂存的收敛 ID 并改写出站头。
// 非透传与透传两个请求构造器共用本函数，防止应用语义漂移。仅解析该
// snapshot 的 OAuth 账号可读取，避免 stale context 跨账号 failover 泄漏。
func applyStagedCodexFingerprintHeaders(c *gin.Context, account *Account, h http.Header) {
	applyCodexFingerprintHeaders(h, stagedCodexFingerprintIDsForAccount(c, account))
	stripOpenAICodexLineageHeaders(c, account, h)
}

func applyStagedCodexFingerprintClientMetadata(c *gin.Context, account *Account, reqBody map[string]any) bool {
	modified := applyCodexFingerprintClientMetadata(reqBody, stagedCodexFingerprintIDsForAccount(c, account))
	return stripOpenAICodexLineageClientMetadata(c, account, reqBody) || modified
}

// codexFingerprintMode 控制 OAuth 账号出站请求的设备指纹收敛强度。
// 多人共享同一 OAuth 账号时，每个用户的 Codex 客户端会携带各自不同的
// installation_id / session_id / thread_id，上游据此判定设备数和会话数。
// 收敛模式将这些标识改写为账号级设备和有限客户端槽位，减少上游可见的设备/会话指纹。
type codexFingerprintMode string

const (
	// codexFingerprintOff 不做任何收敛，原样透传客户端标识。
	// 仅用于管理员显式回滚/停用，不作为线上默认值。
	codexFingerprintOff codexFingerprintMode = "off"
	// codexFingerprintDevice 仅收敛 installation_id 为账号级恒定值。
	// 上游看到 1 台设备 + 多会话（每用户各自的 session）。
	// 仅保留兼容存量配置，线上维护主线不再扩展。
	codexFingerprintDevice codexFingerprintMode = "device"
	// codexFingerprintSession 收敛 installation_id + session_id，
	// v3 的 session_id 按稳定客户端槽位隔离，thread_id 再按客户端原始
	// session-id 确定性派生（每个真实客户端会话一个独立线程）。
	// 上游看到 1 台设备 + 少量客户端会话 + N 线程。
	// 线上默认模式：设备+Session。
	codexFingerprintSession codexFingerprintMode = "session"
	// codexFingerprintFull 收敛所有标识：installation_id + session_id + thread_id。
	// v3 的 Session 仍按稳定客户端槽位共享，Thread 则继续按下游 API Key 隔离。
	// 仅保留兼容存量配置，线上维护主线不再扩展。
	codexFingerprintFull codexFingerprintMode = "full"
)

const (
	// codexFingerprintDefaultMode 是 OAuth 账号缺失或非法配置时的线上默认值。
	codexFingerprintDefaultMode  = codexFingerprintSession
	codexFingerprintModeExtraKey = "codex_fingerprint_mode"
	// codexFingerprintSeedExtraKey 仅用于拒绝旧版 extra 输入；真实 seed 只存专用列。
	codexFingerprintSeedExtraKey          = "codex_fingerprint_seed"
	codexSessionSlotCountExtraKey         = "codex_session_slot_count"
	codexSessionSlotCountUpperBoundary    = 4
	codexSubagentMaxInflightExtraKey      = "codex_subagent_max_inflight_per_session"
	codexSubagentMaxInflightUpperBoundary = 64
)

func codexFingerprintModeFromExtra(extra map[string]any) codexFingerprintMode {
	if extra == nil {
		return codexFingerprintDefaultMode
	}
	raw, _ := extra[codexFingerprintModeExtraKey].(string)
	switch codexFingerprintMode(strings.TrimSpace(raw)) {
	case codexFingerprintOff, codexFingerprintDevice, codexFingerprintSession, codexFingerprintFull:
		return codexFingerprintMode(strings.TrimSpace(raw))
	default:
		return codexFingerprintDefaultMode
	}
}

// stripDeprecatedCodexFingerprintExtraSeed 阻止旧 UUID seed 进入普通 Extra。
func stripDeprecatedCodexFingerprintExtraSeed(extra map[string]any) map[string]any {
	if extra == nil {
		return nil
	}
	if _, exists := extra[codexFingerprintSeedExtraKey]; !exists {
		return extra
	}
	stripped := make(map[string]any, len(extra)-1)
	for key, value := range extra {
		if key != codexFingerprintSeedExtraKey {
			stripped[key] = value
		}
	}
	return stripped
}

func prepareCodexFingerprintExtraForCreate(_, _ string, extra map[string]any) map[string]any {
	return stripDeprecatedCodexFingerprintExtraSeed(extra)
}

func prepareCodexFingerprintExtraForUpdate(_ *Account, extra map[string]any) map[string]any {
	return stripDeprecatedCodexFingerprintExtraSeed(extra)
}

func sanitizedCodexFingerprintExtraUpdates(updates map[string]any) map[string]any {
	return stripDeprecatedCodexFingerprintExtraSeed(updates)
}

// GetCodexFingerprintMode 从账号 extra JSON 读取指纹收敛模式。
//
// 缺失、空值或非法值统一使用线上默认的 session（设备+Session）模式。
// 显式 off 仍可作为紧急回滚/停用开关；device/full 仅兼容存量配置。
// 线上账号统一为 OpenAI OAuth，因此该默认值会覆盖未写入 extra 的存量 OAuth 账号。
func (a *Account) GetCodexFingerprintMode() codexFingerprintMode {
	if a == nil || !a.IsOpenAIOAuthLike() {
		return codexFingerprintOff
	}
	return codexFingerprintModeFromExtra(a.Extra)
}

// GetCodexSubagentMaxInflightPerSession 返回账号单个收敛 Session 允许的子代理并发数。
// 0 表示关闭；仅 session/full 模式生效，避免配置误伤未收敛或仅设备收敛的账号。
func (a *Account) GetCodexSubagentMaxInflightPerSession() int {
	if a == nil || !a.IsOpenAIOAuth() {
		return 0
	}
	mode := a.GetCodexFingerprintMode()
	if mode != codexFingerprintSession && mode != codexFingerprintFull {
		return 0
	}
	raw, ok := a.Extra[codexSubagentMaxInflightExtraKey]
	if !ok || raw == nil {
		return 0
	}
	value := 0
	switch typed := raw.(type) {
	case int:
		value = typed
	case int64:
		value = int(typed)
	case float64:
		if typed == float64(int(typed)) {
			value = int(typed)
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			value = int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			value = parsed
		}
	}
	if value < 1 || value > codexSubagentMaxInflightUpperBoundary {
		return 0
	}
	return value
}

// GetCodexSessionSlotCount 返回账号启用的收敛 Session 槽位数。
// 线上 OAuth/session 默认使用两个槽位；显式非法值仍回落到单槽位，
// 以兼容历史配置和紧急回滚。仅 session/full 模式生效。
func (a *Account) GetCodexSessionSlotCount() int {
	if a == nil || !a.IsOpenAIOAuth() {
		return 1
	}
	mode := a.GetCodexFingerprintMode()
	if mode != codexFingerprintSession && mode != codexFingerprintFull {
		return 1
	}
	raw, ok := a.Extra[codexSessionSlotCountExtraKey]
	if !ok || raw == nil {
		return DefaultSessionPersonaSlotCount
	}
	value := 0
	switch typed := raw.(type) {
	case int:
		value = typed
	case int64:
		value = int(typed)
	case float64:
		if typed == float64(int(typed)) {
			value = int(typed)
		}
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			value = int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			value = parsed
		}
	}
	if value < 1 || value > codexSessionSlotCountUpperBoundary {
		return 1
	}
	return value
}

// deriveStableUUIDv4 从种子确定性派生一个 UUIDv4 格式的字符串。
// 同一种子永远返回同一值。
func deriveStableUUIDv4(seed string) string {
	h := sha256.Sum256([]byte(seed))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 1
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

// codexFingerprintIDs 收敛后的完整 ID 集合。
// 由 v3 指纹上下文一次性生成，同一个实例在头改写和体改写之间共享，
// 确保所有载体中的 turn_id 等随机字段一致。体改写时还会补记原始
// client_metadata.session_id，用于识别 root prompt_cache_key 的默认值。
type codexFingerprintIDs struct {
	accountID           int64
	mode                codexFingerprintMode
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
	requestID           string
	subagentHeader      string
	subagentKind        string
	threadSource        string
	isSubagent          bool
	turnStartedAtUnixMs int64
}

func stagedCodexFingerprintSessionScopeHash(c *gin.Context) string {
	ids := stagedCodexFingerprintIDs(c)
	if ids == nil {
		return ""
	}
	return strings.TrimSpace(ids.sessionScopeHash)
}

func stagedCodexFingerprintIDs(c *gin.Context) *codexFingerprintIDs {
	if c == nil {
		return nil
	}
	value, ok := c.Get(codexFingerprintIDsContextKey)
	if !ok {
		return nil
	}
	ids, ok := value.(*codexFingerprintIDs)
	if !ok || ids == nil {
		return nil
	}
	return ids
}

// stagedCodexFingerprintControlsSession 表示本 attempt 已由指纹层接管 Session。
// device 模式只接管设备标识，仍保留兼容层原有的 Session 逻辑。
func stagedCodexFingerprintControlsSession(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, ok := c.Get(codexFingerprintIDsContextKey)
	if !ok {
		return false
	}
	ids, ok := value.(*codexFingerprintIDs)
	return ok && ids != nil && ids.mode != codexFingerprintDevice
}

// extractClientSessionID 从请求头中提取客户端原始的会话标识。
// 优先取 session-id（连字符形式，Codex CLI 标准），回退到 session_id（下划线形式）。
// 返回的值尚未被 isolateOpenAISessionID 改写，是客户端的真实标识。
func extractClientSessionID(h http.Header) string {
	if v := strings.TrimSpace(h.Get("session-id")); v != "" {
		return v
	}
	return strings.TrimSpace(h.Get("session_id"))
}

// applyCodexFingerprintHeaders 按预计算的收敛 ID 改写出站 HTTP 头中的设备指纹。
// 在 buildUpstreamRequest 的白名单透传之后、enforceCodexIdentityHeaders 之前调用。
func applyCodexFingerprintHeaders(h http.Header, ids *codexFingerprintIDs) {
	if h == nil || ids == nil {
		return
	}

	// 所有非 off 模式都收敛 installation_id
	h.Set("x-codex-installation-id", ids.installationID)

	if ids.mode == codexFingerprintDevice {
		rewriteCodexTurnMetadataFields(h, map[string]any{
			"installation_id": ids.installationID,
		})
		return
	}

	// session / full 模式：改写所有相关头
	h.Set("x-codex-window-id", ids.windowID)
	// CodexCLI 0.149.0 的请求头直接复用 Thread ID，避免产生额外的可见身份。
	h.Set("x-client-request-id", ids.threadID)
	// 连字符形式和下划线形式都改写，保证一致
	h.Set("session-id", ids.sessionID)
	h.Set("session_id", ids.sessionID)
	h.Set("thread-id", ids.threadID)
	if ids.parentThreadID != "" {
		h.Set("x-codex-parent-thread-id", ids.parentThreadID)
	}
	if ids.subagentHeader != "" {
		h.Set("x-openai-subagent", ids.subagentHeader)
	}

	turnMetadataFields := map[string]any{
		"installation_id":         ids.installationID,
		"session_id":              ids.sessionID,
		"thread_id":               ids.threadID,
		"turn_id":                 ids.turnID,
		"window_id":               ids.windowID,
		"turn_started_at_unix_ms": ids.turnStartedAtUnixMs,
	}
	if ids.parentThreadID != "" {
		turnMetadataFields["parent_thread_id"] = ids.parentThreadID
	}
	if ids.forkedThreadID != "" {
		turnMetadataFields["forked_from_thread_id"] = ids.forkedThreadID
	}
	if ids.parentTurnID != "" {
		turnMetadataFields["parent_turn_id"] = ids.parentTurnID
	}
	if ids.rootTurnID != "" {
		turnMetadataFields["root_turn_id"] = ids.rootTurnID
	}
	if ids.subagentKind != "" {
		turnMetadataFields["subagent_kind"] = ids.subagentKind
	}
	if ids.threadSource != "" {
		turnMetadataFields["thread_source"] = ids.threadSource
	}
	rewriteCodexTurnMetadataFields(h, turnMetadataFields)
}

// rewriteCodexTurnMetadataFields 解析 x-codex-turn-metadata 头中的 JSON，
// 替换指定字段后回写。合法对象保留未指定字段（如 sandbox、thread_source）；
// 非法/非对象值重建为最小合法 metadata，避免 flat 与 embedded identity 分裂。
func rewriteCodexTurnMetadataFields(h http.Header, fields map[string]any) {
	raw := strings.TrimSpace(h.Get("x-codex-turn-metadata"))
	if raw == "" {
		return
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any, len(fields))
	}
	for k, v := range fields {
		metadata[k] = v
	}
	rebuilt, err := json.Marshal(metadata)
	if err != nil {
		return
	}
	h.Set("x-codex-turn-metadata", string(rebuilt))
}

// applyCodexFingerprintClientMetadata 按预计算的收敛 ID 改写请求体中的 client_metadata。
// v3 提供权威缓存标识时直接覆盖或补齐。
func applyCodexFingerprintClientMetadata(reqBody map[string]any, ids *codexFingerprintIDs) bool {
	if reqBody == nil || ids == nil {
		return false
	}

	existing, _ := reqBody["client_metadata"].(map[string]any)
	if existing == nil {
		existing = make(map[string]any)
	}

	modified := applyCodexFingerprintToClientMetadataMap(existing, ids)
	if modified {
		reqBody["client_metadata"] = existing
	}
	if ids.promptCacheKey != "" {
		if current, _ := reqBody["prompt_cache_key"].(string); current != ids.promptCacheKey {
			reqBody["prompt_cache_key"] = ids.promptCacheKey
			modified = true
		}
	}
	return modified
}

// applyCodexFingerprintToClientMetadataMap 是 client_metadata 改写的共享核心，
// map 版（非透传，body 已解码）与 raw 字节版（透传热路径）都经由它，保证两条
// 路径的收敛语义永不漂移。
func applyCodexFingerprintToClientMetadataMap(existing map[string]any, ids *codexFingerprintIDs) bool {
	if existing == nil || ids == nil {
		return false
	}

	modified := false

	if ids.installationID != "" {
		existing["x-codex-installation-id"] = ids.installationID
		modified = true
	}

	if ids.mode == codexFingerprintDevice {
		rewriteClientMetadataEmbeddedTurnMetadata(existing, map[string]any{
			"installation_id": ids.installationID,
		})
		return modified
	}

	// session / full 模式
	existing["session_id"] = ids.sessionID
	existing["thread_id"] = ids.threadID
	if ids.parentThreadID != "" {
		existing["parent_thread_id"] = ids.parentThreadID
	}
	if ids.forkedThreadID != "" {
		existing["forked_from_thread_id"] = ids.forkedThreadID
	}
	existing["turn_id"] = ids.turnID
	if ids.parentTurnID != "" {
		existing["parent_turn_id"] = ids.parentTurnID
	}
	if ids.rootTurnID != "" {
		existing["root_turn_id"] = ids.rootTurnID
	}
	existing["x-codex-window-id"] = ids.windowID
	if ids.subagentHeader != "" {
		existing["x-openai-subagent"] = ids.subagentHeader
	}

	turnMetadataFields := map[string]any{
		"installation_id":         ids.installationID,
		"session_id":              ids.sessionID,
		"thread_id":               ids.threadID,
		"turn_id":                 ids.turnID,
		"window_id":               ids.windowID,
		"turn_started_at_unix_ms": ids.turnStartedAtUnixMs,
	}
	if ids.parentThreadID != "" {
		turnMetadataFields["parent_thread_id"] = ids.parentThreadID
	}
	if ids.forkedThreadID != "" {
		turnMetadataFields["forked_from_thread_id"] = ids.forkedThreadID
	}
	if ids.parentTurnID != "" {
		turnMetadataFields["parent_turn_id"] = ids.parentTurnID
	}
	if ids.rootTurnID != "" {
		turnMetadataFields["root_turn_id"] = ids.rootTurnID
	}
	if ids.subagentKind != "" {
		turnMetadataFields["subagent_kind"] = ids.subagentKind
	}
	if ids.threadSource != "" {
		turnMetadataFields["thread_source"] = ids.threadSource
	}
	rewriteClientMetadataEmbeddedTurnMetadata(existing, turnMetadataFields)
	return true
}

// applyCodexFingerprintClientMetadataRaw 在原始 JSON 字节上改写 client_metadata，
// 供透传路径使用——透传是热路径，禁止对可能高达数十 MB 的 body 做全量
// Unmarshal（见 forwardOpenAIPassthrough 的轻量提取注释）。实现为：gjson 提取
// client_metadata 小对象单独解码，经共享核心改写后 sjson 一次性拼回，body
// 其余字节原样保留；v3 的权威 prompt_cache_key 会覆盖或补齐。
// 语义与 map 版逐点一致（含
// "非对象值整体替换为收敛集合"的行为）。
func applyCodexFingerprintClientMetadataRaw(body []byte, ids *codexFingerprintIDs) ([]byte, bool, error) {
	if len(body) == 0 || ids == nil {
		return body, false, nil
	}
	// 非 JSON 对象的 body（数组/标量/畸形）没有 client_metadata 语义，
	// sjson 在这类根上写字段会改写整体结构，直接放行保持原样。
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body, false, nil
	}

	existing := map[string]any{}
	if cm := gjson.GetBytes(body, "client_metadata"); cm.IsObject() {
		if err := json.Unmarshal([]byte(cm.Raw), &existing); err != nil {
			return body, false, fmt.Errorf("decode client_metadata for fingerprint: %w", err)
		}
	}

	next := body
	modified := false
	if applyCodexFingerprintToClientMetadataMap(existing, ids) {
		raw, err := json.Marshal(existing)
		if err != nil {
			return body, false, fmt.Errorf("encode converged client_metadata: %w", err)
		}
		var setErr error
		next, setErr = sjson.SetRawBytes(body, "client_metadata", raw)
		if setErr != nil {
			return body, false, fmt.Errorf("splice converged client_metadata: %w", setErr)
		}
		modified = true
	}
	if ids.promptCacheKey != "" {
		rewritten, err := sjson.SetBytes(next, "prompt_cache_key", ids.promptCacheKey)
		if err != nil {
			return body, false, fmt.Errorf("splice converged prompt_cache_key: %w", err)
		}
		if string(rewritten) != string(next) {
			next = rewritten
			modified = true
		}
	}
	return next, modified, nil
}

// rewriteClientMetadataEmbeddedTurnMetadata 改写 client_metadata 中内嵌的
// x-codex-turn-metadata JSON 字符串里的指定字段。非法/非对象值会重建，
// 避免 flat client_metadata 与 embedded metadata 暴露两套身份。
func rewriteClientMetadataEmbeddedTurnMetadata(clientMetadata map[string]any, fields map[string]any) {
	raw, ok := clientMetadata["x-codex-turn-metadata"].(string)
	if !ok || raw == "" {
		return
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil || metadata == nil {
		metadata = make(map[string]any, len(fields))
	}
	for k, v := range fields {
		metadata[k] = v
	}
	if rebuilt, err := json.Marshal(metadata); err == nil {
		clientMetadata["x-codex-turn-metadata"] = string(rebuilt)
	}
}
