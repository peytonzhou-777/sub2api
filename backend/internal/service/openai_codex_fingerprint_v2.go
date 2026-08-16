package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	codexFingerprintAlgorithmV2 = "v2"
	codexFingerprintSeedBytes   = 32
	codexFingerprintStateTTL    = 5 * time.Minute

	codexFingerprintLogicalTurnSourceContextKey = "codex_fingerprint_logical_turn_source"
)

type codexFingerprintKind string

const (
	codexFingerprintKindInstallation codexFingerprintKind = "installation"
	codexFingerprintKindSession      codexFingerprintKind = "session"
	codexFingerprintKindThread       codexFingerprintKind = "thread"
	codexFingerprintKindTurn         codexFingerprintKind = "turn"
	codexFingerprintKindWindow       codexFingerprintKind = "window"
	codexFingerprintKindPromptCache  codexFingerprintKind = "prompt_cache"
	codexFingerprintKindRequest      codexFingerprintKind = "request"
)

var (
	errCodexFingerprintSecretInvalid = errors.New("codex fingerprint cluster secret must contain at least 32 bytes")
	errCodexFingerprintSeedInvalid   = errors.New("codex fingerprint seed must be 64 lowercase hex characters")
	errCodexFingerprintEpochInvalid  = errors.New("codex fingerprint session epoch must be positive")
	errCodexFingerprintThreadMissing = errors.New("codex fingerprint thread source is required")
)

// CodexFingerprintState 是受保护存储中的账号级 v2 状态。
type CodexFingerprintState struct {
	Seed           string
	Version        string
	Epoch          int64
	EpochStartedAt time.Time
}

// CodexFingerprintSessionResolution 同时返回账号当前 epoch 与本 Thread 的绑定 epoch。
// 两者必须分离，旧 Thread 不能污染账号状态缓存或后台清理基准。
type CodexFingerprintSessionResolution struct {
	State      CodexFingerprintState
	BoundEpoch int64
	Rotated    bool
}

func (r CodexFingerprintSessionResolution) valid() bool {
	return r.State.valid() && r.BoundEpoch > 0
}

// CodexFingerprintStateRepository 原子初始化并读取账号内部指纹状态。
// 独立于通用账号写入，避免复制、导入或管理员编辑继承 seed。
type CodexFingerprintStateRepository interface {
	GetOrInitializeCodexFingerprintState(ctx context.Context, accountID int64, now time.Time) (*CodexFingerprintState, error)
}

// CodexFingerprintSessionRepository 原子解析 Thread 绑定 epoch，并在满足门槛时轮换当前 Session。
type CodexFingerprintSessionRepository interface {
	ResolveCodexFingerprintSessionState(
		ctx context.Context,
		accountID int64,
		threadSourceHash string,
		now time.Time,
		allowRotation bool,
		expectedEpochStartedAt time.Time,
		idleBefore time.Time,
		oldEpochCutoff time.Time,
	) (*CodexFingerprintSessionResolution, error)
}

// codexFingerprintOriginalIDs 保存本次逻辑请求的客户端原始标识。
// 构造完成后只读取，重试必须复用同一份输入，避免同一逻辑 turn 漂移。
type codexFingerprintOriginalIDs struct {
	clientSessionID string
	threadID        string
	turnID          string
	windowID        string
	promptCacheKey  string
	requestID       string
}

func (s CodexFingerprintState) valid() bool {
	if s.Version != codexFingerprintAlgorithmV2 || s.Epoch <= 0 || s.EpochStartedAt.IsZero() {
		return false
	}
	_, err := decodeCodexFingerprintSeed(s.Seed)
	return err == nil
}

type codexFingerprintStateCacheEntry struct {
	state     CodexFingerprintState
	expiresAt time.Time
}

// resolveCodexFingerprintContextForAttempt 在最终账号选定后构造本 attempt 的 v2 身份。
func (s *OpenAIGatewayService) resolveCodexFingerprintContextForAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	headers http.Header,
	body []byte,
) (*CodexFingerprintContext, error) {
	if account == nil || account.GetCodexFingerprintMode() == codexFingerprintOff {
		return nil, nil
	}
	secret := ""
	if s != nil && s.cfg != nil {
		secret = strings.TrimSpace(s.cfg.Gateway.CodexFingerprintSecret)
	}
	state := CodexFingerprintState{
		Seed:           account.CodexFingerprintSeed,
		Version:        account.CodexFingerprintVersion,
		Epoch:          account.CodexFingerprintEpoch,
		EpochStartedAt: derefTime(account.CodexFingerprintEpochStartedAt),
	}
	if state.Version == codexFingerprintAlgorithmV2 && secret == "" {
		return nil, errCodexFingerprintSecretInvalid
	}
	if secret == "" {
		return nil, nil
	}
	now := time.Now()
	mode := account.GetCodexFingerprintMode()
	original := extractCodexFingerprintOriginalIDs(headers, body)
	if original.threadID == "" {
		original.threadID = original.clientSessionID
	}
	if original.threadID == "" {
		original.threadID = explicitOpenAIRequestSessionID(c, body)
	}
	if original.turnID == "" {
		original.turnID = codexFingerprintLogicalTurnSource(c)
	}
	if original.threadID == "" {
		original.threadID = "logical-turn:" + original.turnID
	}
	original.threadID = codexFingerprintScopedThreadSource(c, original.threadID)
	if mode != codexFingerprintDevice && original.threadID == "" {
		return nil, errCodexFingerprintThreadMissing
	}
	if original.requestID == "" {
		original.requestID = original.turnID
	}
	if !state.valid() {
		stateRepo, ok := s.accountRepo.(CodexFingerprintStateRepository)
		if !ok {
			return nil, errors.New("codex fingerprint state repository unavailable")
		}
		if cached, ok := s.codexFingerprintStates.Load(account.ID); ok {
			entry, entryOK := cached.(codexFingerprintStateCacheEntry)
			if entryOK && now.Before(entry.expiresAt) {
				state = entry.state
			} else {
				s.codexFingerprintStates.Delete(account.ID)
			}
		}
		if !state.valid() {
			initialized, err := stateRepo.GetOrInitializeCodexFingerprintState(ctx, account.ID, now)
			if err != nil {
				return nil, fmt.Errorf("initialize codex fingerprint state: %w", err)
			}
			if initialized == nil || !initialized.valid() {
				return nil, errors.New("initialize codex fingerprint state: invalid persisted state")
			}
			state = *initialized
			s.codexFingerprintStates.Store(account.ID, codexFingerprintStateCacheEntry{
				state:     state,
				expiresAt: now.Add(codexFingerprintStateTTL),
			})
		}
	}
	if !state.valid() {
		return nil, errors.New("invalid codex fingerprint state")
	}
	attemptEpoch := state.Epoch
	if mode != codexFingerprintDevice {
		sessionRepo, ok := s.accountRepo.(CodexFingerprintSessionRepository)
		if !ok {
			return nil, errors.New("codex fingerprint session repository unavailable")
		}
		threadSourceHash := codexFingerprintThreadSourceHash([]byte(secret), original.threadID)
		resolved, err := sessionRepo.ResolveCodexFingerprintSessionState(
			ctx,
			account.ID,
			threadSourceHash,
			now,
			s.shouldRotateCodexFingerprintSession(account, state, now),
			state.EpochStartedAt,
			now.Add(-s.codexFingerprintIdleGate()),
			now.Add(-s.codexFingerprintOldEpochGrace()),
		)
		if err != nil {
			return nil, fmt.Errorf("resolve codex fingerprint session state: %w", err)
		}
		if resolved == nil || !resolved.valid() {
			return nil, errors.New("resolve codex fingerprint session state: invalid persisted state")
		}
		state = resolved.State
		attemptEpoch = resolved.BoundEpoch
		s.codexFingerprintStates.Store(account.ID, codexFingerprintStateCacheEntry{
			state:     state,
			expiresAt: now.Add(codexFingerprintStateTTL),
		})
		if resolved.Rotated && s.openaiWSPool != nil {
			s.openaiWSPool.ClearAccount(account.ID)
		}
	}
	return newCodexFingerprintContextV2(
		[]byte(secret), state.Seed, attemptEpoch, mode,
		account.GetOpenAIDeviceID(), original,
	)
}

// codexFingerprintScopedThreadSource 防止不同下游 API Key 的相同客户端标识
// 在同一个上游账号内收敛为同一 Thread；不改变调度 SessionHash 的业务含义。
func codexFingerprintScopedThreadSource(c *gin.Context, source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	return fmt.Sprintf("api-key:%d:%s", getAPIKeyIDFromContext(c), source)
}

func codexFingerprintThreadSourceHash(clusterSecret []byte, source string) string {
	mac := hmac.New(sha256.New, clusterSecret)
	writeCodexFingerprintHMACPart(mac, []byte("codex-fp-thread-binding:v1"))
	writeCodexFingerprintHMACPart(mac, []byte(strings.TrimSpace(source)))
	return hex.EncodeToString(mac.Sum(nil))
}

// shouldRotateCodexFingerprintSession 只读取账号上游活动与 WS 连接状态，不接触用户粘性数据。
func (s *OpenAIGatewayService) shouldRotateCodexFingerprintSession(account *Account, state CodexFingerprintState, now time.Time) bool {
	if s == nil || s.cfg == nil || account == nil || account.LastUsedAt == nil || state.EpochStartedAt.IsZero() {
		return false
	}
	minAgeHours := s.cfg.Gateway.CodexFingerprintMinSessionAgeHours
	idleMinutes := s.cfg.Gateway.CodexFingerprintIdleGateMinutes
	if minAgeHours <= 0 || idleMinutes <= 0 || now.Before(state.EpochStartedAt) || now.Before(*account.LastUsedAt) {
		return false
	}
	if now.Sub(state.EpochStartedAt) < time.Duration(minAgeHours)*time.Hour ||
		now.Sub(*account.LastUsedAt) < time.Duration(idleMinutes)*time.Minute {
		return false
	}
	if s.openaiWSPool != nil {
		if s.openaiWSPool.AccountPoolRotationBusy(account.ID) {
			return false
		}
	}
	return true
}

func (s *OpenAIGatewayService) codexFingerprintOldEpochGrace() time.Duration {
	if s == nil || s.cfg == nil || s.cfg.Gateway.CodexFingerprintOldEpochGraceHours <= 0 {
		return 48 * time.Hour
	}
	return time.Duration(s.cfg.Gateway.CodexFingerprintOldEpochGraceHours) * time.Hour
}

func (s *OpenAIGatewayService) codexFingerprintIdleGate() time.Duration {
	if s == nil || s.cfg == nil || s.cfg.Gateway.CodexFingerprintIdleGateMinutes <= 0 {
		return 2 * time.Hour
	}
	return time.Duration(s.cfg.Gateway.CodexFingerprintIdleGateMinutes) * time.Minute
}

// codexFingerprintLogicalTurnSource 为同一逻辑请求生成一次 Turn 来源，跨账号重试复用。
func codexFingerprintLogicalTurnSource(c *gin.Context) string {
	if c != nil {
		if value, ok := c.Get(codexFingerprintLogicalTurnSourceContextKey); ok {
			if source, sourceOK := value.(string); sourceOK && strings.TrimSpace(source) != "" {
				return strings.TrimSpace(source)
			}
		}
	}
	source := uuid.Must(uuid.NewV7()).String()
	if c != nil {
		c.Set(codexFingerprintLogicalTurnSourceContextKey, source)
	}
	return source
}

// applyCodexFingerprintRawForAttempt 在账号选定后统一改写 WS/透传 JSON，并暂存同一份握手头身份。
func (s *OpenAIGatewayService) applyCodexFingerprintRawForAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	newLogicalTurn bool,
) ([]byte, error) {
	if account == nil || !account.IsOpenAIOAuth() {
		return body, nil
	}
	if newLogicalTurn && c != nil {
		c.Set(codexFingerprintLogicalTurnSourceContextKey, "")
	}
	var headers http.Header
	if c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	fpContext, err := s.resolveCodexFingerprintContextForAttempt(ctx, c, account, headers, body)
	if err != nil {
		return body, err
	}
	fpIDs := codexFingerprintIDsFromContext(fpContext)
	if fpIDs == nil {
		fpIDs = resolveCodexFingerprintIDsFromRequest(account, headers)
	}
	stageCodexFingerprintIDs(c, fpIDs)
	if fpIDs == nil {
		return body, nil
	}
	next, changed, err := applyCodexFingerprintClientMetadataRaw(body, fpIDs)
	if err != nil {
		return body, err
	}
	if !changed {
		return body, nil
	}
	return next, nil
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func extractCodexFingerprintOriginalIDs(headers http.Header, body []byte) codexFingerprintOriginalIDs {
	original := codexFingerprintOriginalIDs{}
	if headers != nil {
		original.clientSessionID = extractClientSessionID(headers)
		original.threadID = firstNonEmptyHeader(headers, "thread-id", "thread_id")
		original.requestID = firstNonEmptyHeader(headers, "x-client-request-id")
		original.windowID = firstNonEmptyHeader(headers, "x-codex-window-id")
		if raw := strings.TrimSpace(headers.Get("x-codex-turn-metadata")); raw != "" {
			metadata := gjson.Parse(raw)
			original.turnID = strings.TrimSpace(metadata.Get("turn_id").String())
			if original.threadID == "" {
				original.threadID = strings.TrimSpace(metadata.Get("thread_id").String())
			}
			if original.windowID == "" {
				original.windowID = strings.TrimSpace(metadata.Get("window_id").String())
			}
		}
	}
	if len(body) > 0 {
		if original.clientSessionID == "" {
			original.clientSessionID = strings.TrimSpace(gjson.GetBytes(body, "client_metadata.session_id").String())
		}
		if original.threadID == "" {
			original.threadID = strings.TrimSpace(gjson.GetBytes(body, "client_metadata.thread_id").String())
		}
		if original.turnID == "" {
			original.turnID = strings.TrimSpace(gjson.GetBytes(body, "client_metadata.turn_id").String())
		}
		if original.windowID == "" {
			original.windowID = strings.TrimSpace(gjson.GetBytes(body, "client_metadata.x-codex-window-id").String())
		}
		if raw := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()); raw != "" {
			metadata := gjson.Parse(raw)
			if original.threadID == "" {
				original.threadID = strings.TrimSpace(metadata.Get("thread_id").String())
			}
			if original.turnID == "" {
				original.turnID = strings.TrimSpace(metadata.Get("turn_id").String())
			}
			if original.windowID == "" {
				original.windowID = strings.TrimSpace(metadata.Get("window_id").String())
			}
		}
		original.promptCacheKey = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	return original
}

func firstNonEmptyHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func codexFingerprintIDsFromContext(fp *CodexFingerprintContext) *codexFingerprintIDs {
	if fp == nil {
		return nil
	}
	return &codexFingerprintIDs{
		mode:           fp.mode,
		installationID: fp.installationID,
		sessionID:      fp.sessionID,
		threadID:       fp.threadID,
		turnID:         fp.turnID,
		windowID:       fp.windowID,
		promptCacheKey: fp.promptCacheKey,
		requestID:      fp.requestID,
	}
}

// CodexFingerprintContext 是最终账号选定后生成的不可变出站身份快照。
// 字段保持私有，调用方只能读取，不能在 failover attempt 之间就地修改。
type CodexFingerprintContext struct {
	mode             codexFingerprintMode
	algorithmVersion string
	sessionEpoch     int64
	installationID   string
	sessionID        string
	threadID         string
	turnID           string
	windowID         string
	promptCacheKey   string
	requestID        string
}

// Mode 返回本 attempt 使用的收敛模式。
func (c *CodexFingerprintContext) Mode() string {
	if c == nil {
		return string(codexFingerprintOff)
	}
	return string(c.mode)
}

// AlgorithmVersion 返回指纹算法版本。
func (c *CodexFingerprintContext) AlgorithmVersion() string {
	if c == nil {
		return ""
	}
	return c.algorithmVersion
}

// SessionEpoch 返回 Thread 创建时绑定的 Session epoch。
func (c *CodexFingerprintContext) SessionEpoch() int64 {
	if c == nil {
		return 0
	}
	return c.sessionEpoch
}

// InstallationID 返回账号设备标识。
func (c *CodexFingerprintContext) InstallationID() string {
	if c == nil {
		return ""
	}
	return c.installationID
}

// SessionID 返回账号当前 epoch 的 Session 标识。
func (c *CodexFingerprintContext) SessionID() string {
	if c == nil {
		return ""
	}
	return c.sessionID
}

// ThreadID 返回按原始 Thread 来源隔离后的标识。
func (c *CodexFingerprintContext) ThreadID() string {
	if c == nil {
		return ""
	}
	return c.threadID
}

// TurnID 返回按逻辑 turn 隔离后的标识。
func (c *CodexFingerprintContext) TurnID() string {
	if c == nil {
		return ""
	}
	return c.turnID
}

// WindowID 返回窗口标识。
func (c *CodexFingerprintContext) WindowID() string {
	if c == nil {
		return ""
	}
	return c.windowID
}

// PromptCacheKey 返回 prompt cache 的隔离标识。
func (c *CodexFingerprintContext) PromptCacheKey() string {
	if c == nil {
		return ""
	}
	return c.promptCacheKey
}

// RequestID 返回请求标识。
func (c *CodexFingerprintContext) RequestID() string {
	if c == nil {
		return ""
	}
	return c.requestID
}

// newCodexFingerprintContextV2 按账号 seed、epoch 和原始标识构造 v2 身份。
// installation 固定使用 epoch 0；Session 与子标识使用 Thread 创建时绑定的 epoch。
func newCodexFingerprintContextV2(
	clusterSecret []byte,
	seedHex string,
	epoch int64,
	mode codexFingerprintMode,
	configuredDeviceID string,
	original codexFingerprintOriginalIDs,
) (*CodexFingerprintContext, error) {
	if mode == codexFingerprintOff {
		return nil, nil
	}
	if len(clusterSecret) < codexFingerprintSeedBytes {
		return nil, errCodexFingerprintSecretInvalid
	}
	seed, err := decodeCodexFingerprintSeed(seedHex)
	if err != nil {
		return nil, err
	}
	if epoch <= 0 {
		return nil, errCodexFingerprintEpochInvalid
	}

	ctx := &CodexFingerprintContext{
		mode:             mode,
		algorithmVersion: codexFingerprintAlgorithmV2,
		sessionEpoch:     epoch,
		installationID:   strings.TrimSpace(configuredDeviceID),
	}
	if ctx.installationID == "" {
		ctx.installationID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, 0, codexFingerprintKindInstallation, "account-device")
	}
	if mode == codexFingerprintDevice {
		return ctx, nil
	}

	ctx.sessionID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindSession, "account-session")
	if mode == codexFingerprintFull {
		ctx.threadID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindThread, "account-thread")
		ctx.turnID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindTurn, original.turnID)
		ctx.windowID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindWindow, "account-window")
		return ctx, nil
	}
	threadSource := strings.TrimSpace(original.threadID)
	if threadSource == "" {
		threadSource = strings.TrimSpace(original.clientSessionID)
	}
	if threadSource == "" {
		return nil, errCodexFingerprintThreadMissing
	}
	ctx.threadID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindThread, threadSource)

	if value := strings.TrimSpace(original.turnID); value != "" {
		ctx.turnID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindTurn, value)
	}
	windowSource := strings.TrimSpace(original.windowID)
	if windowSource == "" {
		windowSource = threadSource + ":0"
	}
	ctx.windowID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindWindow, windowSource)
	if value := strings.TrimSpace(original.promptCacheKey); value != "" {
		ctx.promptCacheKey = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindPromptCache, value)
	}
	if value := strings.TrimSpace(original.requestID); value != "" {
		ctx.requestID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindRequest, value)
	}
	return ctx, nil
}

func decodeCodexFingerprintSeed(seedHex string) ([]byte, error) {
	seedHex = strings.TrimSpace(seedHex)
	if len(seedHex) != hex.EncodedLen(codexFingerprintSeedBytes) || seedHex != strings.ToLower(seedHex) {
		return nil, errCodexFingerprintSeedInvalid
	}
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != codexFingerprintSeedBytes {
		return nil, errCodexFingerprintSeedInvalid
	}
	return seed, nil
}

// deriveCodexFingerprintUUIDV2 使用带长度边界的 HMAC 输入域派生 UUIDv4 外形标识。
func deriveCodexFingerprintUUIDV2(
	clusterSecret []byte,
	seed []byte,
	epoch int64,
	kind codexFingerprintKind,
	originalValue string,
) string {
	mac := hmac.New(sha256.New, clusterSecret)
	writeCodexFingerprintHMACPart(mac, []byte("codex-fp:"+codexFingerprintAlgorithmV2))
	writeCodexFingerprintHMACPart(mac, seed)
	epochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBytes, uint64(epoch))
	writeCodexFingerprintHMACPart(mac, epochBytes)
	writeCodexFingerprintHMACPart(mac, []byte(kind))
	writeCodexFingerprintHMACPart(mac, []byte(originalValue))

	value := mac.Sum(nil)[:16]
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(value[0:4]),
		binary.BigEndian.Uint16(value[4:6]),
		binary.BigEndian.Uint16(value[6:8]),
		binary.BigEndian.Uint16(value[8:10]),
		value[10:16])
}

type codexFingerprintHMACWriter interface {
	Write([]byte) (int, error)
}

func writeCodexFingerprintHMACPart(w codexFingerprintHMACWriter, value []byte) {
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(value)))
	_, _ = w.Write(length)
	_, _ = w.Write(value)
}
