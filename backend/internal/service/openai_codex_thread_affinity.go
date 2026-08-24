package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAICodexThreadAliasType = "codex_thread"

var (
	ErrOpenAICodexParentThreadPending     = fmt.Errorf("%w: Codex parent thread binding is provisional", ErrNoAvailableAccounts)
	ErrOpenAICodexParentThreadUnavailable = fmt.Errorf("%w: Codex parent thread account is unavailable", ErrNoAvailableAccounts)
	ErrOpenAICodexThreadLineageConflict   = fmt.Errorf("%w: Codex thread lineage resolves to different accounts", ErrNoAvailableAccounts)
)

type openAICodexThreadAffinityContextKey struct{}

// openAICodexThreadAffinityState 只保存 HMAC 索引和本次选号授权，不保存客户端原始 ID。
type openAICodexThreadAffinityState struct {
	selfAliasHash       string
	parentAliasHashes   []string
	internalSubagent    bool
	authorizedAccountID int64
}

func (s *openAICodexThreadAffinityState) authorize(accountID int64) {
	if s != nil {
		s.authorizedAccountID = accountID
	}
}

func (s *openAICodexThreadAffinityState) resetAuthorization() {
	if s != nil {
		s.authorizedAccountID = 0
	}
}

func (s *openAICodexThreadAffinityState) allows(accountID int64) bool {
	return s != nil && accountID > 0 && s.authorizedAccountID == accountID
}

func openAICodexThreadAffinityFromContext(ctx context.Context) *openAICodexThreadAffinityState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(openAICodexThreadAffinityContextKey{}).(*openAICodexThreadAffinityState)
	return state
}

func stagedOpenAICodexThreadAffinity(c *gin.Context) *openAICodexThreadAffinityState {
	if c == nil || c.Request == nil {
		return nil
	}
	return openAICodexThreadAffinityFromContext(c.Request.Context())
}

// stageOpenAICodexThreadAffinity 在选号前提取派生拓扑；同一请求的后续解析可补齐 body 信号。
func (s *OpenAIGatewayService) stageOpenAICodexThreadAffinity(c *gin.Context, body []byte) {
	if c == nil || c.Request == nil {
		return
	}
	original := extractCodexFingerprintOriginalIDs(c.Request.Header, body)
	bindConversation := isOpenAICodexConversationTurn(c, body)
	if !bindConversation && !original.isSubagent {
		return
	}
	if original.clientSessionID == "" || original.threadID == "" && original.parentThreadID == "" && original.forkedThreadID == "" {
		if !original.isSubagent {
			return
		}
	}
	secret := ""
	if s != nil && s.cfg != nil {
		secret = strings.TrimSpace(s.cfg.Gateway.CodexFingerprintSecret)
	}
	state := &openAICodexThreadAffinityState{internalSubagent: original.isSubagent}
	if bindConversation && len(secret) >= codexFingerprintSeedBytes && original.clientSessionID != "" {
		if original.threadID != "" {
			state.selfAliasHash = openAICodexThreadAliasHash([]byte(secret), original.clientSessionID, original.threadID)
		}
		state.parentAliasHashes = uniqueCodexFingerprintHashes(
			openAICodexThreadAliasHash([]byte(secret), original.clientSessionID, original.parentThreadID),
			openAICodexThreadAliasHash([]byte(secret), original.clientSessionID, original.forkedThreadID),
		)
	}
	current := openAICodexThreadAffinityFromContext(c.Request.Context())
	if current != nil {
		if state.selfAliasHash == "" {
			state.selfAliasHash = current.selfAliasHash
		}
		if len(state.parentAliasHashes) == 0 {
			state.parentAliasHashes = append([]string(nil), current.parentAliasHashes...)
		}
		state.internalSubagent = state.internalSubagent || current.internalSubagent
		if state.selfAliasHash == current.selfAliasHash && equalOpenAICodexThreadHashes(state.parentAliasHashes, current.parentAliasHashes) {
			current.internalSubagent = state.internalSubagent
			return
		}
	}
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAICodexThreadAffinityContextKey{}, state))
}

// isOpenAICodexConversationTurn 仅让会生成对话输出的 Responses turn 建立线程绑定。
func isOpenAICodexConversationTurn(c *gin.Context, body []byte) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	normalizedPath := strings.TrimRight(strings.TrimSpace(c.Request.URL.Path), "/")
	switch normalizedPath {
	case "/v1/responses", "/openai/v1/responses", "/responses", "/backend-api/codex/responses":
	default:
		return false
	}
	if HasCompactionTriggerInInput(body) {
		return false
	}
	generate := gjson.GetBytes(body, "generate")
	return !generate.Exists() || generate.Type != gjson.False
}

func equalOpenAICodexThreadHashes(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// openAICodexThreadAliasHash 使用长度分隔 HMAC，避免保存或拼接歧义泄露原始线程 ID。
func openAICodexThreadAliasHash(secret []byte, sessionID, threadID string) string {
	sessionID = strings.TrimSpace(sessionID)
	threadID = strings.TrimSpace(threadID)
	if len(secret) == 0 || sessionID == "" || threadID == "" {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	writeCodexFingerprintHMACPart(mac, []byte("openai-codex-thread-affinity:v1"))
	writeCodexFingerprintHMACPart(mac, []byte(sessionID))
	writeCodexFingerprintHMACPart(mac, []byte(threadID))
	return hex.EncodeToString(mac.Sum(nil))
}

// openAICodexThreadAliasScopeKey 跨 HTTP/WS lane 复用父系索引，但仍按网关分组和 API Key 隔离。
func openAICodexThreadAliasScopeKey(groupID *int64) string {
	group := "simple"
	if groupID != nil && *groupID > 0 {
		group = fmt.Sprintf("%d", *groupID)
	}
	return "openai:v1:group:" + group + ":lineage:codex-thread"
}

func openAICodexThreadReservationAlias(groupID *int64, hash string) OpenAIUserConversationAlias {
	return OpenAIUserConversationAlias{
		ScopeKey: openAICodexThreadAliasScopeKey(groupID),
		Type:     openAICodexThreadAliasType,
		Hash:     strings.ToLower(strings.TrimSpace(hash)),
	}
}

// resolveOpenAICodexThreadBindings 同时查询当前线程和父系绑定；当前线程由调用方优先采用。
func resolveOpenAICodexThreadBindings(
	ctx context.Context,
	store OpenAIUserAffinityConversationStore,
	req OpenAIAccountScheduleRequest,
	identity openAIUserConversationIdentity,
	state *openAICodexThreadAffinityState,
) (*OpenAIUserConversationBinding, *OpenAIUserConversationBinding, error) {
	if store == nil || state == nil {
		return nil, nil, nil
	}
	scopeKey := openAICodexThreadAliasScopeKey(req.GroupID)
	lookup := func(hash string) (*OpenAIUserConversationBinding, error) {
		hash = strings.ToLower(strings.TrimSpace(hash))
		if len(hash) != 64 {
			return nil, nil
		}
		return store.GetOpenAIUserConversationBindingByAlias(
			ctx, identity.userID, identity.apiKeyID, scopeKey, openAICodexThreadAliasType, hash,
		)
	}
	selfBinding, err := lookup(state.selfAliasHash)
	if err != nil {
		return nil, nil, err
	}
	var parentBinding *OpenAIUserConversationBinding
	for _, hash := range sortedOpenAICodexThreadHashes(state.parentAliasHashes) {
		if hash == state.selfAliasHash {
			if selfBinding != nil && selfBinding.FirstOutputCommitted && selfBinding.Status != "provisional" {
				return selfBinding, nil, nil
			}
			return nil, nil, ErrOpenAICodexThreadLineageConflict
		}
		candidate, lookupErr := lookup(hash)
		if lookupErr != nil {
			return nil, nil, lookupErr
		}
		if candidate == nil {
			continue
		}
		if candidate.Status == "provisional" || !candidate.FirstOutputCommitted {
			if selfBinding != nil && selfBinding.FirstOutputCommitted && selfBinding.Status != "provisional" {
				return selfBinding, nil, nil
			}
			return nil, nil, ErrOpenAICodexParentThreadPending
		}
		if parentBinding != nil && (parentBinding.ID != candidate.ID || parentBinding.AccountID != candidate.AccountID) {
			if selfBinding != nil && selfBinding.FirstOutputCommitted && selfBinding.Status != "provisional" {
				return selfBinding, nil, nil
			}
			return nil, nil, ErrOpenAICodexThreadLineageConflict
		}
		parentBinding = candidate
	}
	return selfBinding, parentBinding, nil
}

// applyOpenAICodexThreadLineagePolicy 保留本地子代理语义，只控制上游可见的父系字段。
func applyOpenAICodexThreadLineagePolicy(c *gin.Context, account *Account, original *codexFingerprintOriginalIDs) {
	if original == nil {
		return
	}
	state := stagedOpenAICodexThreadAffinity(c)
	if state == nil || account != nil && state.allows(account.ID) {
		return
	}
	internalSubagent := original.isSubagent || state.internalSubagent
	original.parentThreadID = ""
	original.forkedThreadID = ""
	if internalSubagent {
		original.parentTurnID = ""
		original.rootTurnID = ""
	}
	original.subagentHeader = ""
	original.subagentKind = ""
	if original.threadSource == "subagent" {
		original.threadSource = ""
	}
	original.isSubagent = internalSubagent
}

func stripOpenAICodexLineageHeaders(c *gin.Context, account *Account, headers http.Header) {
	state := stagedOpenAICodexThreadAffinity(c)
	if headers == nil || state == nil || account != nil && state.allows(account.ID) {
		return
	}
	deleteOpenAIHeaderEqualFold(headers, "x-openai-subagent")
	deleteOpenAIHeaderEqualFold(headers, "x-codex-parent-thread-id")
	deleteOpenAIHeaderEqualFold(headers, "x-codex-forked-from-thread-id")
	deleteOpenAIHeaderEqualFold(headers, "parent_thread_id")
	deleteOpenAIHeaderEqualFold(headers, "forked_from_thread_id")
	raw := strings.TrimSpace(headers.Get("x-codex-turn-metadata"))
	if raw == "" {
		return
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		deleteOpenAIHeaderEqualFold(headers, "x-codex-turn-metadata")
		return
	}
	if stripOpenAICodexLineageMap(metadata, state.internalSubagent) {
		if rebuilt, err := json.Marshal(metadata); err == nil {
			headers.Set("x-codex-turn-metadata", string(rebuilt))
		}
	}
}

func stripOpenAICodexLineageClientMetadata(c *gin.Context, account *Account, body map[string]any) bool {
	state := stagedOpenAICodexThreadAffinity(c)
	if body == nil || state == nil || account != nil && state.allows(account.ID) {
		return false
	}
	metadata, ok := body["client_metadata"].(map[string]any)
	if !ok || metadata == nil {
		return false
	}
	return stripOpenAICodexLineageMap(metadata, state.internalSubagent)
}

func stripOpenAICodexLineageRaw(c *gin.Context, account *Account, body []byte) ([]byte, bool, error) {
	state := stagedOpenAICodexThreadAffinity(c)
	if len(body) == 0 || state == nil || account != nil && state.allows(account.ID) {
		return body, false, nil
	}
	result := gjson.GetBytes(body, "client_metadata")
	if !result.IsObject() {
		return body, false, nil
	}
	metadata := map[string]any{}
	if err := json.Unmarshal([]byte(result.Raw), &metadata); err != nil {
		return body, false, err
	}
	if !stripOpenAICodexLineageMap(metadata, state.internalSubagent) {
		return body, false, nil
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return body, false, err
	}
	next, err := sjson.SetRawBytes(body, "client_metadata", raw)
	if err != nil {
		return body, false, err
	}
	return next, true, nil
}

func stripOpenAICodexLineageMap(metadata map[string]any, stripTurnLineage bool) bool {
	if metadata == nil {
		return false
	}
	changed := false
	for _, key := range []string{
		"parent_thread_id", "forked_from_thread_id", "x-openai-subagent", "subagent_kind",
	} {
		if _, exists := metadata[key]; exists {
			delete(metadata, key)
			changed = true
		}
	}
	if stripTurnLineage {
		for _, key := range []string{"parent_turn_id", "root_turn_id"} {
			if _, exists := metadata[key]; exists {
				delete(metadata, key)
				changed = true
			}
		}
	}
	if value, _ := metadata["thread_source"].(string); isOpenAICodexDerivedSemantic(value) {
		delete(metadata, "thread_source")
		changed = true
	}
	if value, _ := metadata["request_kind"].(string); isOpenAICodexDerivedSemantic(value) {
		delete(metadata, "request_kind")
		changed = true
	}
	if raw, ok := metadata["x-codex-turn-metadata"].(string); ok && strings.TrimSpace(raw) != "" {
		embedded := map[string]any{}
		if err := json.Unmarshal([]byte(raw), &embedded); err != nil {
			delete(metadata, "x-codex-turn-metadata")
			return true
		}
		if stripOpenAICodexLineageMap(embedded, stripTurnLineage) {
			if rebuilt, err := json.Marshal(embedded); err == nil {
				metadata["x-codex-turn-metadata"] = string(rebuilt)
				changed = true
			}
		}
	}
	return changed
}

func isOpenAICodexDerivedSemantic(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{"subagent", "thread_spawn", "collab_spawn", "fork", "child", "side_chat", "side-chat", "sidechat"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func sortedOpenAICodexThreadHashes(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
