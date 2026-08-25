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
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	openaiidentity "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	codexFingerprintAlgorithmV3 = "v3"
	codexFingerprintSeedBytes   = 32
	codexFingerprintStateTTL    = 5 * time.Minute
	codexFingerprintScopeV1     = 1
	codexFingerprintScopeV2     = 2

	codexFingerprintLogicalTurnSourceContextKey = "codex_fingerprint_logical_turn_source"
	codexFingerprintAdmissionPreparedContextKey = "codex_fingerprint_admission_prepared_account"
)

type codexFingerprintKind string

const (
	codexFingerprintKindInstallation codexFingerprintKind = "installation"
	codexFingerprintKindSession      codexFingerprintKind = "session"
	codexFingerprintKindThread       codexFingerprintKind = "thread"
	codexFingerprintKindTurn         codexFingerprintKind = "turn"
)

var (
	errCodexFingerprintSecretInvalid    = errors.New("codex fingerprint cluster secret must contain at least 32 bytes")
	errCodexFingerprintSeedInvalid      = errors.New("codex fingerprint seed must be 64 lowercase hex characters")
	errCodexFingerprintEpochInvalid     = errors.New("codex fingerprint session epoch must be positive")
	errCodexFingerprintEpochTimeInvalid = errors.New("codex fingerprint session epoch start time is invalid")
	errCodexFingerprintThreadMissing    = errors.New("codex fingerprint thread source is required")
	errCodexFingerprintFullSubagent     = errors.New("codex fingerprint full mode does not support subagent topology")
)

// CodexFingerprintState 是受保护存储中的账号级版本化指纹状态。
type CodexFingerprintState struct {
	Seed           string
	Version        string
	Epoch          int64
	EpochStartedAt time.Time
}

// CodexFingerprintSessionResolution 同时返回账号当前 epoch 与本 Thread 的绑定 epoch。
// 两者必须分离，旧 Thread 不能污染账号状态缓存或后台清理基准。
type CodexFingerprintSessionResolution struct {
	State                   CodexFingerprintState
	BoundEpoch              int64
	BoundEpochStartedAt     time.Time
	MatchedThreadSourceHash string
	BoundSessionScopeHash   string
	BoundScopeVersion       int
	BoundSessionSlot        int
	BoundSessionSlotCount   int
	RotationReason          string
	Rotated                 bool
	Created                 bool
}

func (r CodexFingerprintSessionResolution) valid() bool {
	return r.State.valid() && r.BoundEpoch > 0 && !r.BoundEpochStartedAt.IsZero()
}

// CodexFingerprintStateRepository 原子初始化并读取账号内部指纹状态。
// 独立于通用账号写入，避免复制、导入或管理员编辑继承 seed。
type CodexFingerprintStateRepository interface {
	GetOrInitializeCodexFingerprintState(ctx context.Context, accountID int64, now time.Time) (*CodexFingerprintState, error)
}

// CodexFingerprintSecretRepository 在共享数据库中校验各实例使用同一集群密钥。
type CodexFingerprintSecretRepository interface {
	ValidateCodexFingerprintSecret(ctx context.Context, secretHash string, now time.Time) error
}

// CodexFingerprintSessionRepository 原子解析 Thread 绑定 epoch，并在满足门槛时轮换当前 Session。
type CodexFingerprintSessionRepository interface {
	ResolveCodexFingerprintSessionState(
		ctx context.Context,
		request CodexFingerprintSessionRequest,
	) (*CodexFingerprintSessionResolution, error)
}

// CodexFingerprintSessionRequest 描述一个作用域内的 Thread 绑定与轮换门槛。
type CodexFingerprintSessionRequest struct {
	AccountID           int64
	SessionScopeHash    string
	SessionScopeVersion int
	SessionSlot         int
	SessionSlotCount    int
	ThreadSourceHashes  []string
	BindSourceHashes    []string
	Now                 time.Time
	RotationAllowed     bool
	MinAgeBefore        time.Time
	IdleBefore          time.Time
	MaxAgeBefore        time.Time
	OldEpochCutoff      time.Time
}

type codexFingerprintRotationThresholds struct {
	MinAgeBefore time.Time
	IdleBefore   time.Time
	MaxAgeBefore time.Time
}

// codexFingerprintOriginalIDs 保存本次逻辑请求的客户端原始标识。
// 构造完成后只读取，重试必须复用同一份输入，避免同一逻辑 turn 漂移。
type codexFingerprintOriginalIDs struct {
	clientScope            string
	threadScope            string
	sessionScope           string
	sessionScopeHash       string
	sessionScopeVersion    int
	sessionSlot            int
	sessionSlotCount       int
	clientSessionID        string
	originalPromptCacheKey string
	threadID               string
	parentThreadID         string
	forkedThreadID         string
	turnID                 string
	parentTurnID           string
	rootTurnID             string
	windowID               string
	subagentHeader         string
	subagentKind           string
	threadSource           string
	isSubagent             bool
}

func (s CodexFingerprintState) valid() bool {
	if s.Version != codexFingerprintAlgorithmV3 || s.Epoch <= 0 || s.EpochStartedAt.IsZero() {
		return false
	}
	_, err := decodeCodexFingerprintSeed(s.Seed)
	return err == nil
}

type codexFingerprintStateCacheEntry struct {
	state     CodexFingerprintState
	expiresAt time.Time
}

type codexFingerprintScopeCandidate struct {
	scope   string
	version int
	slot    int
	count   int
}

// resolveCodexFingerprintContextForAttempt 在最终账号选定后按持久化版本构造本 attempt 身份。
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
	if secret == "" {
		return nil, errCodexFingerprintSecretInvalid
	}
	if err := s.validateCodexFingerprintClusterSecret(ctx, secret); err != nil {
		return nil, err
	}
	now := time.Now()
	mode := account.GetCodexFingerprintMode()
	original := extractCodexFingerprintOriginalIDs(headers, body)
	// 父系字段只有在调度层验证绑定且最终账号一致后才允许参与上游身份派生。
	applyOpenAICodexThreadLineagePolicy(c, account, &original)
	// 在兼容链路生成临时 Thread 之前冻结根谱系；缺失时由用户作用域稳定回退。
	rootLineage := resolveCodexFingerprintRootLineage(original)
	// full 模式启用子代理阀门即表示账号需要父子拓扑；所有请求统一按 session
	// 派生 Thread，避免根请求先被压成 account-thread 后子线程无法闭合引用。
	if mode == codexFingerprintFull && account.GetCodexSubagentMaxInflightPerSession() > 0 {
		mode = codexFingerprintSession
	}
	if mode == codexFingerprintFull && original.isSubagent {
		return nil, errCodexFingerprintFullSubagent
	}
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
	identityHidden := codexIdentityEnforcement.Load() || account.GetOpenAIUserAgent() != ""
	if s != nil && s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		identityHidden = true
	}
	// 客户端身份和语义协议决定逻辑身份；HTTP/WS 只作为传输与连接池维度。
	original.clientScope = resolveCodexFingerprintSessionScope(c, headers, identityHidden)
	original.threadScope = resolveCodexFingerprintThreadScope(c, original.clientScope)
	if mode != codexFingerprintDevice && original.threadID == "" {
		return nil, errCodexFingerprintThreadMissing
	}
	initializedState := false
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
			initializedState = true
			s.codexFingerprintStates.Store(account.ID, codexFingerprintStateCacheEntry{
				state:     state,
				expiresAt: now.Add(codexFingerprintStateTTL),
			})
		}
	}
	if initializedState {
		logger.L().Info("codex fingerprint state initialized",
			zap.Int64("account_id", account.ID),
			zap.String("algorithm_version", state.Version),
			zap.Int64("epoch", state.Epoch),
		)
	}
	if !state.valid() {
		return nil, errors.New("invalid codex fingerprint state")
	}
	if mode != codexFingerprintDevice {
		seed, decodeErr := decodeCodexFingerprintSeed(state.Seed)
		if decodeErr != nil {
			return nil, decodeErr
		}
		original.sessionSlotCount = account.GetCodexSessionSlotCount()
		original.sessionSlot = resolveCodexFingerprintSessionSlot(
			[]byte(secret), seed, original.clientScope,
			resolveCodexFingerprintStableUserScope(c), rootLineage,
			original.sessionSlotCount,
		)
		original.sessionScope = resolveCodexFingerprintSlottedSessionScope(
			original.clientScope, original.sessionSlot, original.sessionSlotCount,
		)
		original.sessionScopeVersion = codexFingerprintScopeV2
		original.sessionScopeHash = codexFingerprintSessionScopeHashV2([]byte(secret), original.sessionScope)
	}
	attemptEpoch := state.Epoch
	attemptEpochStartedAt := state.EpochStartedAt
	if mode != codexFingerprintDevice {
		sessionRepo, ok := s.accountRepo.(CodexFingerprintSessionRepository)
		if !ok {
			return nil, errors.New("codex fingerprint session repository unavailable")
		}
		scopeCandidates := resolveCodexFingerprintScopeCandidates(
			c, headers, identityHidden, original.clientScope, original.sessionSlot, original.sessionSlotCount,
		)
		scopeByHash := make(map[string]codexFingerprintScopeCandidate, len(scopeCandidates))
		for _, candidate := range scopeCandidates {
			hash := codexFingerprintSessionScopeHashForCandidate([]byte(secret), candidate)
			scopeByHash[hash] = candidate
		}
		threadSourceHashes := resolveCodexFingerprintThreadSourceHashes(
			[]byte(secret), c, original.clientScope, scopeCandidates, original.threadID,
		)
		parentThreadHashes := resolveCodexFingerprintThreadSourceHashes(
			[]byte(secret), c, original.clientScope, scopeCandidates, original.parentThreadID,
		)
		forkedThreadHashes := resolveCodexFingerprintThreadSourceHashes(
			[]byte(secret), c, original.clientScope, scopeCandidates, original.forkedThreadID,
		)
		threadSourceHash := ""
		if len(threadSourceHashes) > 0 {
			threadSourceHash = threadSourceHashes[0]
		}
		epochPolicy := s.codexFingerprintEpochPolicy(ctx)
		thresholds := buildCodexFingerprintRotationThresholds(epochPolicy, account.ID, original.sessionScopeHash, now)
		rotationAllowed := true
		if s.openaiWSPool != nil {
			rotationAllowed = !s.openaiWSPool.SessionScopeRotationBusy(account.ID, original.sessionScopeHash)
		}
		resolved, err := sessionRepo.ResolveCodexFingerprintSessionState(
			ctx,
			CodexFingerprintSessionRequest{
				AccountID:           account.ID,
				SessionScopeHash:    original.sessionScopeHash,
				SessionScopeVersion: original.sessionScopeVersion,
				SessionSlot:         original.sessionSlot,
				SessionSlotCount:    original.sessionSlotCount,
				ThreadSourceHashes: uniqueCodexFingerprintHashes(
					append(append(threadSourceHashes, parentThreadHashes...), forkedThreadHashes...)...,
				),
				BindSourceHashes: func() []string {
					if !original.isSubagent {
						return nil
					}
					return []string{threadSourceHash}
				}(),
				Now:             now,
				RotationAllowed: rotationAllowed,
				MinAgeBefore:    thresholds.MinAgeBefore,
				IdleBefore:      thresholds.IdleBefore,
				MaxAgeBefore:    thresholds.MaxAgeBefore,
				OldEpochCutoff:  now.Add(-time.Duration(epochPolicy.OldEpochGraceHours) * time.Hour),
			},
		)
		if err != nil {
			return nil, fmt.Errorf("resolve codex fingerprint session state: %w", err)
		}
		if resolved == nil || !resolved.valid() {
			return nil, errors.New("resolve codex fingerprint session state: invalid persisted state")
		}
		if resolved.Rotated {
			logger.L().Info("codex fingerprint session rotated",
				zap.Int64("account_id", account.ID),
				zap.String("scope_hash", truncateCodexFingerprintHash(resolved.BoundSessionScopeHash)),
				zap.Int64("epoch", resolved.State.Epoch),
				zap.String("reason", resolved.RotationReason),
			)
		}
		if resolved.Created {
			logger.L().Info("codex fingerprint thread scope created",
				zap.Int64("account_id", account.ID),
				zap.String("scope_hash", truncateCodexFingerprintHash(resolved.BoundSessionScopeHash)),
				zap.Int64("epoch", resolved.BoundEpoch),
			)
		}
		state = resolved.State
		attemptEpoch = resolved.BoundEpoch
		attemptEpochStartedAt = resolved.BoundEpochStartedAt
		if resolved.BoundSessionScopeHash != original.sessionScopeHash {
			candidate, ok := scopeByHash[resolved.BoundSessionScopeHash]
			if !ok {
				return nil, errors.New("codex fingerprint thread binding belongs to unknown session scope")
			}
			// 已绑定父子或存量 Thread 始终继承原 scope/epoch，避免跨传输迁移时改写错误绑定。
			original.sessionScope = candidate.scope
			original.sessionScopeHash = resolved.BoundSessionScopeHash
			original.sessionScopeVersion = candidate.version
			original.sessionSlot = candidate.slot
			original.sessionSlotCount = candidate.count
		}
		if original.isSubagent {
			logger.L().Debug("codex fingerprint subagent topology resolved",
				zap.Int64("account_id", account.ID),
				zap.Int64("epoch", resolved.BoundEpoch),
				zap.String("thread_hash", truncateCodexFingerprintHash(threadSourceHash)),
				zap.String("parent_hash", truncateCodexFingerprintHash(firstCodexFingerprintHash(parentThreadHashes))),
				zap.Bool("has_fork", original.forkedThreadID != ""),
			)
		}
	}
	return newCodexFingerprintContextV3(
		[]byte(secret), state.Seed, attemptEpoch, attemptEpochStartedAt, mode,
		account.GetOpenAIDeviceID(), original,
	)
}

func (s *OpenAIGatewayService) validateCodexFingerprintClusterSecret(ctx context.Context, secret string) error {
	secretHash := sha256.Sum256([]byte("codex-fingerprint-cluster-secret:v1\x00" + secret))
	secretID := hex.EncodeToString(secretHash[:])
	if _, ok := s.codexFingerprintSecretIDs.Load(secretID); ok {
		return nil
	}
	repo, ok := s.accountRepo.(CodexFingerprintSecretRepository)
	if !ok {
		return errors.New("codex fingerprint secret repository unavailable")
	}
	if err := repo.ValidateCodexFingerprintSecret(ctx, secretID, time.Now()); err != nil {
		return fmt.Errorf("validate codex fingerprint cluster secret: %w", err)
	}
	s.codexFingerprintSecretIDs.Store(secretID, struct{}{})
	logger.L().Info("codex fingerprint cluster secret validated",
		zap.String("secret_id", truncateCodexFingerprintHash(secretID)),
	)
	return nil
}

func truncateCodexFingerprintHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

// resolveCodexFingerprintSessionScope 只按上游可见身份或语义协议拆分 Session。
// HTTP/WS 是传输、路由和连接池维度，不参与逻辑 Session 派生。
func resolveCodexFingerprintSessionScope(c *gin.Context, headers http.Header, identityEnforced bool) string {
	return resolveCodexFingerprintBaseSessionScope(c, headers, identityEnforced)
}

// resolveCodexFingerprintBaseSessionScope 按上游可见客户端身份选择稳定基础作用域。
func resolveCodexFingerprintBaseSessionScope(c *gin.Context, headers http.Header, identityEnforced bool) string {
	if headers == nil && c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	userAgent := ""
	if headers != nil {
		userAgent = strings.TrimSpace(headers.Get("User-Agent"))
	}
	if !identityEnforced {
		family := resolveCodexFingerprintVisibleClientFamily(userAgent)
		if family != "" {
			return "client:" + family
		}
	}
	return "protocol:" + resolveCodexFingerprintProtocolFamily(c)
}

// resolveCodexFingerprintVisibleClientFamily 与关闭强制统一后的最终身份配对保持同源。
// 非官方 UA 会被出站收口整体回退，不能仅凭入口 originator 虚构独立客户端槽位。
func resolveCodexFingerprintVisibleClientFamily(userAgent string) string {
	originator, pairedUA, ok := openaiidentity.PairCodexClientIdentity(userAgent)
	if !ok {
		return ""
	}
	if value := NormalizeSessionUserAgent(originator); value != "" {
		return value
	}
	return NormalizeSessionUserAgent(pairedUA)
}

// resolveCodexFingerprintThreadScope 在客户端会话槽位内继续按下游 API Key 隔离 Thread。
func resolveCodexFingerprintThreadScope(c *gin.Context, clientScope string) string {
	return fmt.Sprintf("api-key:%d:%s", getAPIKeyIDFromContext(c), strings.TrimSpace(clientScope))
}

func resolveCodexFingerprintProtocolFamily(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "unknown"
	}
	path := strings.ToLower(strings.TrimSpace(c.Request.URL.Path))
	switch {
	case isOpenAIResponsesCompactPath(c):
		return "responses"
	case strings.Contains(path, "/chat/completions"), strings.Contains(path, "/messages"):
		return "chat"
	case strings.Contains(path, "/responses"):
		return "responses"
	default:
		return "unknown"
	}
}

func resolveLegacyCodexFingerprintProtocolFamily(c *gin.Context) string {
	if isOpenAIResponsesCompactPath(c) {
		return "responses-compact"
	}
	return resolveCodexFingerprintProtocolFamily(c)
}

// resolveCodexFingerprintStableUserScope 优先使用本站稳定用户 ID，缺失时回退 API Key ID。
func resolveCodexFingerprintStableUserScope(c *gin.Context) string {
	if c != nil {
		if value, exists := c.Get("api_key"); exists {
			if apiKey, ok := value.(*APIKey); ok && apiKey != nil {
				if apiKey.UserID > 0 {
					return fmt.Sprintf("user:%d", apiKey.UserID)
				}
				if apiKey.ID > 0 {
					return fmt.Sprintf("api-key:%d", apiKey.ID)
				}
			}
		}
	}
	return "api-key:0"
}

func resolveCodexFingerprintRootLineage(original codexFingerprintOriginalIDs) string {
	for _, value := range []string{
		original.originalPromptCacheKey,
		original.clientSessionID,
		original.parentThreadID,
		original.forkedThreadID,
		original.threadID,
	} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "root-missing"
}

// resolveCodexFingerprintSessionSlot 将根谱系稳定映射到少量账号级 Session 槽位。
func resolveCodexFingerprintSessionSlot(
	clusterSecret, accountSeed []byte,
	identityScope, userScope, rootLineage string,
	slotCount int,
) int {
	if slotCount <= 1 {
		return 0
	}
	if slotCount > codexSessionSlotCountUpperBoundary {
		slotCount = codexSessionSlotCountUpperBoundary
	}
	mappingSource := strings.TrimSpace(rootLineage)
	if mappingSource == "" || mappingSource == "root-missing" {
		mappingSource = "user-fallback:" + strings.TrimSpace(userScope)
	} else {
		mappingSource = "root-lineage:" + mappingSource
	}
	mac := hmac.New(sha256.New, clusterSecret)
	writeCodexFingerprintHMACPart(mac, []byte("codex-fp-session-slot:v1"))
	writeCodexFingerprintHMACPart(mac, accountSeed)
	writeCodexFingerprintHMACPart(mac, []byte(strings.TrimSpace(identityScope)))
	writeCodexFingerprintHMACPart(mac, []byte(mappingSource))
	return int(binary.BigEndian.Uint64(mac.Sum(nil)[:8]) % uint64(slotCount))
}

func resolveCodexFingerprintSlottedSessionScope(identityScope string, slot, slotCount int) string {
	identityScope = strings.TrimSpace(identityScope)
	if slotCount <= 1 {
		return identityScope
	}
	return fmt.Sprintf("%s:slot:%d", identityScope, slot)
}

// resolveCodexFingerprintScopeCandidates 同时提供新 scope 与旧 transport scope，
// 让存量 Thread 和父子绑定在迁移期间安全继承，而新根只写入 v2 scope。
func resolveCodexFingerprintScopeCandidates(
	c *gin.Context,
	headers http.Header,
	identityEnforced bool,
	identityScope string,
	currentSlot int,
	currentSlotCount int,
) []codexFingerprintScopeCandidate {
	candidates := make([]codexFingerprintScopeCandidate, 0, 10)
	appendCandidate := func(candidate codexFingerprintScopeCandidate) {
		candidate.scope = strings.TrimSpace(candidate.scope)
		if candidate.scope == "" {
			return
		}
		for _, existing := range candidates {
			if existing.scope == candidate.scope && existing.version == candidate.version {
				return
			}
		}
		candidates = append(candidates, candidate)
	}
	// 当前配置优先，避免历史候选中相同 slot scope 的旧 count 覆盖观测元数据。
	appendCandidate(codexFingerprintScopeCandidate{
		scope:   resolveCodexFingerprintSlottedSessionScope(identityScope, currentSlot, currentSlotCount),
		version: codexFingerprintScopeV2,
		slot:    currentSlot,
		count:   currentSlotCount,
	})
	for count := 1; count <= codexSessionSlotCountUpperBoundary; count++ {
		if count == 1 {
			appendCandidate(codexFingerprintScopeCandidate{scope: identityScope, version: codexFingerprintScopeV2, count: 1})
			continue
		}
		for slot := 0; slot < count; slot++ {
			appendCandidate(codexFingerprintScopeCandidate{
				scope:   resolveCodexFingerprintSlottedSessionScope(identityScope, slot, count),
				version: codexFingerprintScopeV2, slot: slot, count: count,
			})
		}
	}
	legacyBase := resolveCodexFingerprintBaseSessionScope(c, headers, identityEnforced)
	if strings.HasPrefix(legacyBase, "protocol:") {
		legacyBase = "protocol:" + resolveLegacyCodexFingerprintProtocolFamily(c)
	}
	appendCandidate(codexFingerprintScopeCandidate{scope: legacyBase, version: codexFingerprintScopeV1, count: 1})
	appendCandidate(codexFingerprintScopeCandidate{scope: legacyBase + ":transport:http", version: codexFingerprintScopeV1, count: 1})
	appendCandidate(codexFingerprintScopeCandidate{scope: legacyBase + ":transport:ws", version: codexFingerprintScopeV1, count: 1})
	return candidates
}

func resolveCodexFingerprintThreadSourceHashes(
	clusterSecret []byte,
	c *gin.Context,
	identityScope string,
	candidates []codexFingerprintScopeCandidate,
	source string,
) []string {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	result := []string{codexFingerprintOptionalThreadSourceHash(
		clusterSecret, resolveCodexFingerprintThreadScope(c, identityScope), source,
	)}
	for _, candidate := range candidates {
		if candidate.version != codexFingerprintScopeV1 {
			continue
		}
		result = append(result, codexFingerprintOptionalThreadSourceHash(
			clusterSecret, resolveCodexFingerprintThreadScope(c, candidate.scope), source,
		))
	}
	return uniqueCodexFingerprintHashes(result...)
}

func firstCodexFingerprintHash(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// codexFingerprintScopedDerivationSource 使用带长度边界的客户端槽位前缀，
// 避免不同客户端族的相同原始标识映射到同一会话子标识。
func codexFingerprintScopedDerivationSource(clientScope, value string) string {
	clientScope = strings.TrimSpace(clientScope)
	if clientScope == "" {
		return value
	}
	return fmt.Sprintf("%d:%s:%s", len(clientScope), clientScope, value)
}

func codexFingerprintThreadSourceHash(clusterSecret []byte, source string) string {
	mac := hmac.New(sha256.New, clusterSecret)
	writeCodexFingerprintHMACPart(mac, []byte("codex-fp-thread-binding:v1"))
	writeCodexFingerprintHMACPart(mac, []byte(strings.TrimSpace(source)))
	return hex.EncodeToString(mac.Sum(nil))
}

func codexFingerprintOptionalThreadSourceHash(clusterSecret []byte, scope, source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	return codexFingerprintThreadSourceHash(clusterSecret, codexFingerprintScopedDerivationSource(scope, source))
}

func codexFingerprintSessionScopeHash(clusterSecret []byte, scope string) string {
	mac := hmac.New(sha256.New, clusterSecret)
	writeCodexFingerprintHMACPart(mac, []byte("codex-fp-session-scope:v1"))
	writeCodexFingerprintHMACPart(mac, []byte(strings.TrimSpace(scope)))
	return hex.EncodeToString(mac.Sum(nil))
}

func codexFingerprintSessionScopeHashV2(clusterSecret []byte, scope string) string {
	mac := hmac.New(sha256.New, clusterSecret)
	writeCodexFingerprintHMACPart(mac, []byte("codex-fp-session-scope:v2"))
	writeCodexFingerprintHMACPart(mac, []byte(strings.TrimSpace(scope)))
	return hex.EncodeToString(mac.Sum(nil))
}

func codexFingerprintSessionScopeHashForCandidate(clusterSecret []byte, candidate codexFingerprintScopeCandidate) string {
	if candidate.version == codexFingerprintScopeV1 {
		return codexFingerprintSessionScopeHash(clusterSecret, candidate.scope)
	}
	return codexFingerprintSessionScopeHashV2(clusterSecret, candidate.scope)
}

func uniqueCodexFingerprintHashes(values ...string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// codexFingerprintEpochPolicy 在一次解析内冻结完整策略；动态设置缺失时回退静态配置。
func (s *OpenAIGatewayService) codexFingerprintEpochPolicy(ctx context.Context) CodexFingerprintEpochPolicy {
	if s != nil && s.settingService != nil {
		return s.settingService.GetCodexFingerprintEpochPolicy(ctx)
	}
	fallback := &SettingService{}
	if s != nil {
		fallback.cfg = s.cfg
	}
	return fallback.codexFingerprintEpochPolicyFallback()
}

// buildCodexFingerprintRotationThresholds 生成作用域独立的年龄、空闲和最长寿命门槛。
func buildCodexFingerprintRotationThresholds(policy CodexFingerprintEpochPolicy, accountID int64, scopeHash string, now time.Time) codexFingerprintRotationThresholds {
	if ValidateCodexFingerprintEpochPolicy(policy) != nil {
		policy = defaultCodexFingerprintEpochPolicy()
	}
	jitter := codexFingerprintRotationJitter(accountID, scopeHash, policy.RotationJitterHours)
	return codexFingerprintRotationThresholds{
		MinAgeBefore: now.Add(-(time.Duration(policy.MinSessionAgeHours)*time.Hour + jitter)),
		IdleBefore:   now.Add(-time.Duration(policy.IdleGateMinutes) * time.Minute),
		MaxAgeBefore: now.Add(-(time.Duration(policy.MaxSessionAgeHours)*time.Hour + jitter)),
	}
}

func codexFingerprintRotationJitter(accountID int64, scopeHash string, maxHours int) time.Duration {
	if accountID <= 0 || maxHours <= 0 {
		return 0
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("codex-fp-rotation-jitter:v1:%d:%s", accountID, strings.TrimSpace(scopeHash))))
	seconds := binary.BigEndian.Uint64(sum[:8]) % uint64(time.Duration(maxHours)*time.Hour/time.Second)
	return time.Duration(seconds) * time.Second
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

// prepareCodexFingerprintForAttempt 在最终账号选定后只解析一次指纹，并暂存给请求头构造器。
func (s *OpenAIGatewayService) prepareCodexFingerprintForAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	newLogicalTurn bool,
) (*codexFingerprintIDs, error) {
	if account == nil || !account.IsOpenAIOAuth() {
		stageCodexFingerprintIDs(c, nil)
		return nil, nil
	}
	if c != nil {
		if preparedAccount, ok := c.Get(codexFingerprintAdmissionPreparedContextKey); ok {
			if preparedID, idOK := preparedAccount.(int64); idOK && preparedID == account.ID {
				if staged := stagedCodexFingerprintIDsForAccount(c, account); staged != nil {
					c.Set(codexFingerprintAdmissionPreparedContextKey, int64(0))
					return staged, nil
				}
			}
		}
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
		logger.L().Error("codex fingerprint state error",
			zap.Int64("account_id", account.ID),
			zap.Error(err),
		)
		return nil, err
	}
	if fpContext != nil {
		fpContext.accountID = account.ID
	}
	fpIDs := codexFingerprintIDsFromContext(fpContext)
	stageCodexFingerprintIDs(c, fpIDs)
	return fpIDs, nil
}

// PrepareCodexFingerprintForAdmission 在账号准入前冻结本逻辑 turn 的 Session 身份。
// 后续真正构造出站请求时复用同一快照，避免排队键与上游 Session 漂移。
func (s *OpenAIGatewayService) PrepareCodexFingerprintForAdmission(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	newLogicalTurn bool,
) error {
	if c != nil {
		c.Set(codexFingerprintAdmissionPreparedContextKey, int64(0))
	}
	_, err := s.prepareCodexFingerprintForAttempt(ctx, c, account, body, newLogicalTurn)
	if err == nil && c != nil && account != nil {
		c.Set(codexFingerprintAdmissionPreparedContextKey, account.ID)
	}
	return err
}

func codexFingerprintAdmissionPreparedForAccount(c *gin.Context, account *Account) bool {
	if c == nil || account == nil {
		return false
	}
	value, ok := c.Get(codexFingerprintAdmissionPreparedContextKey)
	preparedID, idOK := value.(int64)
	return ok && idOK && preparedID == account.ID && stagedCodexFingerprintIDsForAccount(c, account) != nil
}

// CodexFingerprintAdmissionScope 返回准入队列使用的不可逆 Session 实例键。
func CodexFingerprintAdmissionScope(c *gin.Context) (string, int64) {
	ids := stagedCodexFingerprintIDs(c)
	if ids == nil {
		return "", 0
	}
	return strings.TrimSpace(ids.sessionScopeHash), ids.sessionEpoch
}

// applyCodexFingerprintForAttempt 统一处理 HTTP/WS JSON；compact 可仅暂存头而不改 body。
func (s *OpenAIGatewayService) applyCodexFingerprintForAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	newLogicalTurn bool,
	rewriteBody bool,
) ([]byte, error) {
	fpIDs, err := s.prepareCodexFingerprintForAttempt(ctx, c, account, body, newLogicalTurn)
	if err != nil {
		return body, err
	}
	if fpIDs == nil {
		next, _, stripErr := stripOpenAICodexLineageRaw(c, account, body)
		if stripErr != nil {
			return body, fmt.Errorf("strip unauthorized Codex lineage: %w", stripErr)
		}
		return next, nil
	}
	if !rewriteBody {
		return body, nil
	}
	next, changed, err := applyCodexFingerprintClientMetadataRaw(body, fpIDs)
	if err != nil {
		return body, err
	}
	if !changed {
		next = body
	}
	stripped, _, stripErr := stripOpenAICodexLineageRaw(c, account, next)
	if stripErr != nil {
		return body, fmt.Errorf("strip unauthorized Codex lineage: %w", stripErr)
	}
	return stripped, nil
}

// applyCodexFingerprintRawForAttempt 在账号选定后统一改写 WS/透传 JSON，并暂存同一份握手头身份。
func (s *OpenAIGatewayService) applyCodexFingerprintRawForAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	newLogicalTurn bool,
) ([]byte, error) {
	next, err := s.applyCodexFingerprintForAttempt(ctx, c, account, body, newLogicalTurn, true)
	if err != nil {
		return body, err
	}
	strict, _, strictErr := s.prepareCodexOutboundBody(c, account, next, "ws", false)
	if strictErr != nil {
		return body, fmt.Errorf("prepare Codex WS outbound body: %w", strictErr)
	}
	return strict, nil
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func extractCodexFingerprintOriginalIDs(headers http.Header, body []byte) codexFingerprintOriginalIDs {
	original := codexFingerprintOriginalIDs{}
	// 官方客户端以 body 内嵌 turn metadata 为主载体；平铺字段和请求头仅作兼容回退。
	if len(body) > 0 {
		if raw := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()); raw != "" {
			applyCodexFingerprintOriginalMetadata(&original, gjson.Parse(raw), false)
		}
		original.clientSessionID = strings.TrimSpace(gjson.GetBytes(body, "client_metadata.session_id").String())
		original.originalPromptCacheKey = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
		fillCodexFingerprintOriginalBodyFallbacks(&original, body)
	}
	if headers != nil {
		if original.clientSessionID == "" {
			original.clientSessionID = extractClientSessionID(headers)
		}
		if original.threadID == "" {
			original.threadID = firstNonEmptyHeader(headers, "thread-id", "thread_id")
		}
		if original.parentThreadID == "" {
			original.parentThreadID = firstNonEmptyHeader(headers, "x-codex-parent-thread-id", "parent_thread_id")
		}
		if original.forkedThreadID == "" {
			original.forkedThreadID = firstNonEmptyHeader(headers, "x-codex-forked-from-thread-id", "forked_from_thread_id")
		}
		if original.windowID == "" {
			original.windowID = firstNonEmptyHeader(headers, "x-codex-window-id")
		}
		if original.subagentHeader == "" {
			original.subagentHeader = strings.TrimSpace(headers.Get("x-openai-subagent"))
		}
		if raw := strings.TrimSpace(headers.Get("x-codex-turn-metadata")); raw != "" {
			applyCodexFingerprintOriginalMetadata(&original, gjson.Parse(raw), true)
		}
	}
	original.subagentHeader, original.subagentKind = normalizeCodexSubagentIdentity(
		original.subagentHeader,
		original.subagentKind,
		original.parentThreadID != "" || original.forkedThreadID != "" || original.isSubagent,
	)
	original.threadSource = normalizeCodexThreadSource(original.threadSource)
	original.isSubagent = original.subagentHeader != "" || original.subagentKind != "" ||
		original.parentThreadID != "" || original.forkedThreadID != "" || original.threadSource == "subagent"
	return original
}

func fillCodexFingerprintOriginalBodyFallbacks(original *codexFingerprintOriginalIDs, body []byte) {
	if original == nil {
		return
	}
	fields := []struct {
		target *string
		path   string
	}{
		{&original.threadID, "client_metadata.thread_id"},
		{&original.parentThreadID, "client_metadata.parent_thread_id"},
		{&original.forkedThreadID, "client_metadata.forked_from_thread_id"},
		{&original.turnID, "client_metadata.turn_id"},
		{&original.parentTurnID, "client_metadata.parent_turn_id"},
		{&original.rootTurnID, "client_metadata.root_turn_id"},
		{&original.windowID, "client_metadata.x-codex-window-id"},
		{&original.threadSource, "client_metadata.thread_source"},
	}
	for _, field := range fields {
		if *field.target == "" {
			*field.target = strings.TrimSpace(gjson.GetBytes(body, field.path).String())
		}
	}
	if original.subagentHeader == "" {
		original.subagentHeader = strings.TrimSpace(gjson.GetBytes(body, "client_metadata.x-openai-subagent").String())
	}
	if original.subagentKind == "" {
		original.subagentKind = strings.TrimSpace(gjson.GetBytes(body, "client_metadata.subagent_kind").String())
	}
	if requestKind := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.request_kind").String()); isOpenAICodexDerivedSemantic(requestKind) {
		original.isSubagent = true
	}
}

func applyCodexFingerprintOriginalMetadata(original *codexFingerprintOriginalIDs, metadata gjson.Result, fallbackOnly bool) {
	if original == nil {
		return
	}
	fields := []struct {
		target *string
		path   string
	}{
		{&original.threadID, "thread_id"},
		{&original.parentThreadID, "parent_thread_id"},
		{&original.forkedThreadID, "forked_from_thread_id"},
		{&original.turnID, "turn_id"},
		{&original.parentTurnID, "parent_turn_id"},
		{&original.rootTurnID, "root_turn_id"},
		{&original.windowID, "window_id"},
		{&original.threadSource, "thread_source"},
	}
	for _, field := range fields {
		if !fallbackOnly || *field.target == "" {
			*field.target = strings.TrimSpace(metadata.Get(field.path).String())
		}
	}
	if !fallbackOnly || original.subagentKind == "" {
		original.subagentKind = strings.TrimSpace(metadata.Get("subagent_kind").String())
	}
	if original.subagentHeader == "" && original.subagentKind == "" {
		requestKind := strings.TrimSpace(metadata.Get("request_kind").String())
		if isOpenAICodexDerivedSemantic(requestKind) {
			original.isSubagent = true
		}
	}
	if original.subagentHeader == "" && original.subagentKind == "" {
		threadSource := strings.TrimSpace(metadata.Get("thread_source").String())
		if isOpenAICodexDerivedSemantic(threadSource) {
			original.isSubagent = true
		}
	}
}

// normalizeCodexSubagentIdentity 将同一子代理语义投影为官方区分的请求头值和 metadata 值。
func normalizeCodexSubagentIdentity(headerValue, metadataKind string, fallbackThreadSpawn bool) (string, string) {
	headerValue = strings.ToLower(strings.TrimSpace(headerValue))
	metadataKind = strings.ToLower(strings.TrimSpace(metadataKind))
	values := []string{headerValue, metadataKind}
	for _, value := range values {
		switch value {
		case "collab_spawn", "thread_spawn":
			return "collab_spawn", "thread_spawn"
		case "review":
			return "review", "review"
		case "compact":
			return "compact", "compact"
		case "memory_consolidation":
			return "memory_consolidation", "memory_consolidation"
		}
	}
	if headerValue != "" || metadataKind != "" {
		// 不透传任意代理标签，避免 agent 名称成为新的多用户可见特征。
		return "other", "other"
	}
	if fallbackThreadSpawn {
		return "collab_spawn", "thread_spawn"
	}
	return "", ""
}

func normalizeCodexThreadSource(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "user", "subagent", "memory_consolidation", "automation", "sdk":
		return value
	default:
		return ""
	}
}

// codexWindowGeneration 提取官方 `<thread_id>:<generation>` 中的窗口代数。
func codexWindowGeneration(value string) uint64 {
	value = strings.TrimSpace(value)
	separator := strings.LastIndexByte(value, ':')
	if separator < 0 || separator == len(value)-1 {
		return 0
	}
	generation, err := strconv.ParseUint(value[separator+1:], 10, 64)
	if err != nil {
		return 0
	}
	return generation
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
	turnStartedAtUnixMs := fp.turnStartedAtUnixMs
	if turnStartedAtUnixMs <= 0 {
		turnStartedAtUnixMs = time.Now().UnixMilli()
	}
	return &codexFingerprintIDs{
		accountID:           fp.accountID,
		mode:                fp.mode,
		sessionScopeHash:    fp.sessionScopeHash,
		sessionEpoch:        fp.sessionEpoch,
		sessionScopeVersion: fp.sessionScopeVersion,
		sessionSlot:         fp.sessionSlot,
		sessionSlotCount:    fp.sessionSlotCount,
		installationID:      fp.installationID,
		sessionID:           fp.sessionID,
		threadID:            fp.threadID,
		parentThreadID:      fp.parentThreadID,
		forkedThreadID:      fp.forkedThreadID,
		turnID:              fp.turnID,
		parentTurnID:        fp.parentTurnID,
		rootTurnID:          fp.rootTurnID,
		windowID:            fp.windowID,
		promptCacheKey:      fp.promptCacheKey,
		requestID:           fp.requestID,
		subagentHeader:      fp.subagentHeader,
		subagentKind:        fp.subagentKind,
		threadSource:        fp.threadSource,
		isSubagent:          fp.isSubagent,
		turnStartedAtUnixMs: turnStartedAtUnixMs,
	}
}

// CodexFingerprintContext 是最终账号选定后生成的不可变出站身份快照。
// 字段保持私有，调用方只能读取，不能在 failover attempt 之间就地修改。
type CodexFingerprintContext struct {
	accountID           int64
	mode                codexFingerprintMode
	algorithmVersion    string
	sessionEpoch        int64
	sessionScopeHash    string
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

// SessionID 返回账号当前 epoch 下、按客户端槽位隔离的 Session 标识。
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

// PromptCacheKey 返回与最终 Session 对齐的上游缓存标识。
func (c *CodexFingerprintContext) PromptCacheKey() string {
	if c == nil {
		return ""
	}
	return c.promptCacheKey
}

// RequestID 返回 CodexCLI 0.149.0 投影到请求头的 Thread 标识。
func (c *CodexFingerprintContext) RequestID() string {
	if c == nil {
		return ""
	}
	return c.requestID
}

// newCodexFingerprintContextV3 使用持久化 epoch 时间构造唯一受支持的 v3 身份。
// installation 固定使用 epoch 0；Session 与子标识使用 Thread 创建时绑定的 epoch。
func newCodexFingerprintContextV3(
	clusterSecret []byte,
	seedHex string,
	epoch int64,
	epochStartedAt time.Time,
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
	if !validCodexFingerprintUUIDV7Time(epochStartedAt) {
		return nil, errCodexFingerprintEpochTimeInvalid
	}

	ctx := &CodexFingerprintContext{
		mode:                mode,
		algorithmVersion:    codexFingerprintAlgorithmV3,
		sessionEpoch:        epoch,
		sessionScopeHash:    strings.TrimSpace(original.sessionScopeHash),
		sessionScopeVersion: original.sessionScopeVersion,
		sessionSlot:         original.sessionSlot,
		sessionSlotCount:    original.sessionSlotCount,
		installationID:      strings.TrimSpace(configuredDeviceID),
		subagentHeader:      original.subagentHeader,
		subagentKind:        original.subagentKind,
		threadSource:        original.threadSource,
		isSubagent:          original.isSubagent,
		turnStartedAtUnixMs: time.Now().UnixMilli(),
	}
	if ctx.installationID == "" {
		ctx.installationID = deriveCodexFingerprintInstallationUUIDV4(clusterSecret, seed, "account-device")
	}
	threadScope := strings.TrimSpace(original.threadScope)
	if threadScope == "" {
		threadScope = original.clientScope
	}
	if mode == codexFingerprintDevice {
		return ctx, nil
	}
	sessionScope := strings.TrimSpace(original.sessionScope)
	if sessionScope == "" {
		sessionScope = original.clientScope
	}
	sessionSource := codexFingerprintScopedDerivationSource(sessionScope, "account-session")
	ctx.sessionID, err = deriveCodexFingerprintSessionUUIDV7(
		clusterSecret, seed, epoch, epochStartedAt, sessionSource,
	)
	if err != nil {
		return nil, err
	}
	// session/full 统一复用最终 Session，消除下游用户、API Key 和原始 cache key 暴露出的多路身份。
	ctx.promptCacheKey = ctx.sessionID
	if mode == codexFingerprintFull {
		ctx.threadID = deriveCodexFingerprintStableUUIDV7(
			clusterSecret, seed, epoch, epochStartedAt, codexFingerprintKindThread,
			codexFingerprintScopedDerivationSource(threadScope, "account-thread"), "",
		)
		ctx.turnID = deriveCodexFingerprintStableUUIDV7(
			clusterSecret, seed, epoch, epochStartedAt, codexFingerprintKindTurn,
			codexFingerprintScopedDerivationSource(threadScope, original.turnID), original.turnID,
		)
		ctx.windowID = ctx.threadID + ":" + strconv.FormatUint(codexWindowGeneration(original.windowID), 10)
		ctx.requestID = ctx.threadID
		return ctx, nil
	}
	threadSource := strings.TrimSpace(original.threadID)
	if threadSource == "" {
		threadSource = strings.TrimSpace(original.clientSessionID)
	}
	if threadSource == "" {
		return nil, errCodexFingerprintThreadMissing
	}
	ctx.threadID = deriveCodexFingerprintStableUUIDV7(
		clusterSecret, seed, epoch, epochStartedAt, codexFingerprintKindThread,
		codexFingerprintScopedDerivationSource(threadScope, threadSource), threadSource,
	)
	if value := strings.TrimSpace(original.parentThreadID); value != "" {
		ctx.parentThreadID = deriveCodexFingerprintStableUUIDV7(
			clusterSecret, seed, epoch, epochStartedAt, codexFingerprintKindThread,
			codexFingerprintScopedDerivationSource(threadScope, value), value,
		)
	}
	if value := strings.TrimSpace(original.forkedThreadID); value != "" {
		ctx.forkedThreadID = deriveCodexFingerprintStableUUIDV7(
			clusterSecret, seed, epoch, epochStartedAt, codexFingerprintKindThread,
			codexFingerprintScopedDerivationSource(threadScope, value), value,
		)
	}

	if value := strings.TrimSpace(original.turnID); value != "" {
		ctx.turnID = deriveCodexFingerprintStableUUIDV7(
			clusterSecret, seed, epoch, epochStartedAt, codexFingerprintKindTurn,
			codexFingerprintScopedDerivationSource(threadScope, value), value,
		)
	}
	if value := strings.TrimSpace(original.parentTurnID); value != "" {
		ctx.parentTurnID = deriveCodexFingerprintStableUUIDV7(
			clusterSecret, seed, epoch, epochStartedAt, codexFingerprintKindTurn,
			codexFingerprintScopedDerivationSource(threadScope, value), value,
		)
	}
	if value := strings.TrimSpace(original.rootTurnID); value != "" {
		ctx.rootTurnID = deriveCodexFingerprintStableUUIDV7(
			clusterSecret, seed, epoch, epochStartedAt, codexFingerprintKindTurn,
			codexFingerprintScopedDerivationSource(threadScope, value), value,
		)
	}
	ctx.windowID = ctx.threadID + ":" + strconv.FormatUint(codexWindowGeneration(original.windowID), 10)
	// CodexCLI 0.149.0 直接以 Thread ID 作为 x-client-request-id，不再派生独立请求标识。
	ctx.requestID = ctx.threadID
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

// deriveCodexFingerprintInstallationUUIDV4 模拟持久化 installation ID 的 UUIDv4 形态。
func deriveCodexFingerprintInstallationUUIDV4(
	clusterSecret []byte,
	seed []byte,
	originalValue string,
) string {
	// installation 在 CodexCLI 0.149.0 中持久化且不随版本升级变化，继续保留既有派生域。
	value := deriveCodexFingerprintHMAC(clusterSecret, seed, "v2", 0, codexFingerprintKindInstallation, originalValue)[:16]
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return formatCodexFingerprintUUID(value)
}

// deriveCodexFingerprintStableUUIDV7 为 Thread、Turn 与 Window 生成稳定 UUIDv7。
// 原始标识本身是 UUIDv7 时保留其创建时间，其他客户端形态回退到绑定 epoch 时间。
func deriveCodexFingerprintStableUUIDV7(
	clusterSecret, seed []byte,
	epoch int64,
	epochStartedAt time.Time,
	kind codexFingerprintKind,
	originalValue string,
	timestampSource string,
) string {
	timestamp := epochStartedAt
	if parsed, err := uuid.Parse(strings.TrimSpace(timestampSource)); err == nil && parsed.Version() == uuid.Version(7) {
		timestamp = time.UnixMilli(codexFingerprintUUIDV7UnixMilliFromBytes(parsed[:])).UTC()
	}
	digest := deriveCodexFingerprintHMAC(
		clusterSecret, seed, codexFingerprintAlgorithmV3, epoch, kind, originalValue,
	)
	return formatCodexFingerprintUUIDV7(digest, timestamp)
}

// deriveCodexFingerprintSessionUUIDV7 将持久化 epoch 时间与 v3 HMAC 随机位组合为稳定 UUIDv7。
func deriveCodexFingerprintSessionUUIDV7(
	clusterSecret, seed []byte,
	epoch int64,
	epochStartedAt time.Time,
	source string,
) (string, error) {
	if !validCodexFingerprintUUIDV7Time(epochStartedAt) {
		return "", errCodexFingerprintEpochTimeInvalid
	}
	digest := deriveCodexFingerprintHMAC(
		clusterSecret, seed, codexFingerprintAlgorithmV3, epoch, codexFingerprintKindSession, source,
	)
	return formatCodexFingerprintUUIDV7(digest, epochStartedAt), nil
}

func formatCodexFingerprintUUIDV7(digest []byte, timestampAt time.Time) string {
	value := append([]byte(nil), digest[:16]...)
	timestamp := uint64(timestampAt.UTC().UnixMilli())
	value[0] = byte(timestamp >> 40)
	value[1] = byte(timestamp >> 32)
	value[2] = byte(timestamp >> 24)
	value[3] = byte(timestamp >> 16)
	value[4] = byte(timestamp >> 8)
	value[5] = byte(timestamp)
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return formatCodexFingerprintUUID(value)
}

func codexFingerprintUUIDV7UnixMilliFromBytes(value []byte) int64 {
	if len(value) < 6 {
		return 0
	}
	return int64(value[0])<<40 |
		int64(value[1])<<32 |
		int64(value[2])<<24 |
		int64(value[3])<<16 |
		int64(value[4])<<8 |
		int64(value[5])
}

func validCodexFingerprintUUIDV7Time(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	millis := value.UTC().UnixMilli()
	return millis >= 0 && uint64(millis) <= (1<<48)-1
}

func deriveCodexFingerprintHMAC(
	clusterSecret []byte,
	seed []byte,
	algorithmVersion string,
	epoch int64,
	kind codexFingerprintKind,
	originalValue string,
) []byte {
	mac := hmac.New(sha256.New, clusterSecret)
	writeCodexFingerprintHMACPart(mac, []byte("codex-fp:"+algorithmVersion))
	writeCodexFingerprintHMACPart(mac, seed)
	epochBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(epochBytes, uint64(epoch))
	writeCodexFingerprintHMACPart(mac, epochBytes)
	writeCodexFingerprintHMACPart(mac, []byte(kind))
	writeCodexFingerprintHMACPart(mac, []byte(originalValue))
	return mac.Sum(nil)
}

func formatCodexFingerprintUUID(value []byte) string {
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
