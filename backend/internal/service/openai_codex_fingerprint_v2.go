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

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	openaiidentity "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
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
	errCodexFingerprintFullSubagent  = errors.New("codex fingerprint full mode does not support subagent topology")
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
	State                   CodexFingerprintState
	BoundEpoch              int64
	MatchedThreadSourceHash string
	BoundSessionScopeHash   string
	Rotated                 bool
	Created                 bool
}

func (r CodexFingerprintSessionResolution) valid() bool {
	return r.State.valid() && r.BoundEpoch > 0
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
	AccountID          int64
	SessionScopeHash   string
	ThreadSourceHashes []string
	BindSourceHashes   []string
	Now                time.Time
	RotationAllowed    bool
	MinAgeBefore       time.Time
	IdleBefore         time.Time
	MaxAgeBefore       time.Time
	OldEpochCutoff     time.Time
}

type codexFingerprintRotationThresholds struct {
	MinAgeBefore time.Time
	IdleBefore   time.Time
	MaxAgeBefore time.Time
}

// codexFingerprintOriginalIDs 保存本次逻辑请求的客户端原始标识。
// 构造完成后只读取，重试必须复用同一份输入，避免同一逻辑 turn 漂移。
type codexFingerprintOriginalIDs struct {
	clientScope      string
	threadScope      string
	sessionScopeHash string
	legacyUnscoped   bool
	clientSessionID  string
	threadID         string
	parentThreadID   string
	forkedThreadID   string
	turnID           string
	windowID         string
	promptCacheKey   string
	requestID        string
	subagentMarker   string
	isSubagent       bool
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
	if secret == "" {
		return nil, errCodexFingerprintSecretInvalid
	}
	if err := s.validateCodexFingerprintClusterSecret(ctx, secret); err != nil {
		return nil, err
	}
	now := time.Now()
	mode := account.GetCodexFingerprintMode()
	original := extractCodexFingerprintOriginalIDs(headers, body)
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
	original.clientScope = resolveCodexFingerprintSessionScope(c, headers, identityHidden)
	original.threadScope = resolveCodexFingerprintThreadScope(c, original.clientScope)
	original.sessionScopeHash = codexFingerprintSessionScopeHash([]byte(secret), original.clientScope)
	if mode != codexFingerprintDevice && original.threadID == "" {
		return nil, errCodexFingerprintThreadMissing
	}
	if original.requestID == "" {
		original.requestID = original.turnID
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
	attemptEpoch := state.Epoch
	if mode != codexFingerprintDevice {
		sessionRepo, ok := s.accountRepo.(CodexFingerprintSessionRepository)
		if !ok {
			return nil, errors.New("codex fingerprint session repository unavailable")
		}
		threadSourceHash := codexFingerprintThreadSourceHash(
			[]byte(secret),
			codexFingerprintScopedDerivationSource(original.threadScope, original.threadID),
		)
		parentThreadHash := codexFingerprintOptionalThreadSourceHash(
			[]byte(secret),
			original.threadScope, original.parentThreadID,
		)
		forkedThreadHash := codexFingerprintOptionalThreadSourceHash(
			[]byte(secret),
			original.threadScope, original.forkedThreadID,
		)
		legacyTransportAgnosticScope := resolveCodexFingerprintTransportAgnosticSessionScope(c, headers, identityHidden)
		legacyTransportAgnosticThreadScope := resolveCodexFingerprintThreadScope(c, legacyTransportAgnosticScope)
		legacyTransportAgnosticThreadHash := codexFingerprintThreadSourceHash(
			[]byte(secret),
			codexFingerprintScopedDerivationSource(legacyTransportAgnosticThreadScope, original.threadID),
		)
		legacyTransportAgnosticParentHash := codexFingerprintOptionalThreadSourceHash(
			[]byte(secret),
			legacyTransportAgnosticThreadScope, original.parentThreadID,
		)
		legacyTransportAgnosticForkHash := codexFingerprintOptionalThreadSourceHash(
			[]byte(secret),
			legacyTransportAgnosticThreadScope, original.forkedThreadID,
		)
		legacyClientScope := resolveCodexFingerprintLegacyClientScope(c, headers)
		legacyThreadScope := resolveCodexFingerprintThreadScope(c, legacyClientScope)
		legacyTransportAgnosticScopeHash := codexFingerprintSessionScopeHash([]byte(secret), legacyTransportAgnosticScope)
		legacyClientScopeHash := codexFingerprintSessionScopeHash([]byte(secret), legacyClientScope)
		legacyClientThreadHash := codexFingerprintThreadSourceHash(
			[]byte(secret),
			codexFingerprintScopedDerivationSource(legacyThreadScope, original.threadID),
		)
		legacyClientParentHash := codexFingerprintOptionalThreadSourceHash(
			[]byte(secret),
			legacyThreadScope, original.parentThreadID,
		)
		legacyClientForkHash := codexFingerprintOptionalThreadSourceHash(
			[]byte(secret),
			legacyThreadScope, original.forkedThreadID,
		)
		legacyAccountThreadHash := codexFingerprintThreadSourceHash([]byte(secret), original.threadID)
		legacyAccountParentHash := codexFingerprintOptionalThreadSourceHash([]byte(secret), "", original.parentThreadID)
		legacyAccountForkHash := codexFingerprintOptionalThreadSourceHash([]byte(secret), "", original.forkedThreadID)
		thresholds := s.codexFingerprintRotationThresholds(account.ID, original.sessionScopeHash, now)
		rotationAllowed := true
		if s.openaiWSPool != nil {
			rotationAllowed = !s.openaiWSPool.SessionScopeRotationBusy(account.ID, original.sessionScopeHash)
		}
		resolved, err := sessionRepo.ResolveCodexFingerprintSessionState(
			ctx,
			CodexFingerprintSessionRequest{
				AccountID:        account.ID,
				SessionScopeHash: original.sessionScopeHash,
				ThreadSourceHashes: uniqueCodexFingerprintHashes(
					threadSourceHash,
					parentThreadHash,
					forkedThreadHash,
					legacyTransportAgnosticThreadHash,
					legacyTransportAgnosticParentHash,
					legacyTransportAgnosticForkHash,
					legacyClientThreadHash,
					legacyClientParentHash,
					legacyClientForkHash,
					legacyAccountThreadHash,
					legacyAccountParentHash,
					legacyAccountForkHash,
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
				OldEpochCutoff:  now.Add(-s.codexFingerprintOldEpochGrace()),
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
				zap.String("reason", "age_policy"),
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
		switch {
		case resolved.BoundSessionScopeHash == original.sessionScopeHash:
			original.sessionScopeHash = resolved.BoundSessionScopeHash
		case resolved.BoundSessionScopeHash == legacyTransportAgnosticScopeHash:
			// 改动前的 Thread 继续使用未区分 HTTP/WS 的 Session，避免续聊中途换身份。
			original.clientScope = legacyTransportAgnosticScope
			original.threadScope = legacyTransportAgnosticThreadScope
			original.sessionScopeHash = resolved.BoundSessionScopeHash
			logger.L().Info("codex fingerprint legacy thread binding matched",
				zap.Int64("account_id", account.ID),
				zap.String("scope_hash", truncateCodexFingerprintHash(resolved.BoundSessionScopeHash)),
				zap.Int64("epoch", resolved.BoundEpoch),
				zap.String("legacy_scope", "transport_agnostic"),
			)
		case resolved.BoundSessionScopeHash == "":
			original.clientScope = ""
			original.threadScope = ""
			original.sessionScopeHash = ""
			original.legacyUnscoped = true
			logger.L().Info("codex fingerprint legacy thread binding matched",
				zap.Int64("account_id", account.ID),
				zap.String("scope_hash", "legacy-unscoped"),
				zap.Int64("epoch", resolved.BoundEpoch),
			)
		case resolved.BoundSessionScopeHash == legacyClientScopeHash:
			original.clientScope = legacyClientScope
			original.threadScope = legacyThreadScope
			original.sessionScopeHash = resolved.BoundSessionScopeHash
			logger.L().Info("codex fingerprint legacy thread binding matched",
				zap.Int64("account_id", account.ID),
				zap.String("scope_hash", truncateCodexFingerprintHash(resolved.BoundSessionScopeHash)),
				zap.Int64("epoch", resolved.BoundEpoch),
			)
		case resolved.MatchedThreadSourceHash == threadSourceHash ||
			resolved.MatchedThreadSourceHash == parentThreadHash ||
			resolved.MatchedThreadSourceHash == forkedThreadHash:
			// 未识别的 scope hash 仅兼容当前作用域，避免根据客户端可控标识猜测历史派生规则。
			original.sessionScopeHash = resolved.BoundSessionScopeHash
		default:
			original.sessionScopeHash = resolved.BoundSessionScopeHash
		}
		if original.isSubagent {
			logger.L().Debug("codex fingerprint subagent topology resolved",
				zap.Int64("account_id", account.ID),
				zap.Int64("epoch", resolved.BoundEpoch),
				zap.String("thread_hash", truncateCodexFingerprintHash(threadSourceHash)),
				zap.String("parent_hash", truncateCodexFingerprintHash(parentThreadHash)),
				zap.Bool("has_fork", original.forkedThreadID != ""),
			)
		}
		if resolved.Rotated && s.openaiWSPool != nil {
			s.openaiWSPool.ClearSessionScope(account.ID, resolved.BoundSessionScopeHash)
		}
	}
	return newCodexFingerprintContextV2(
		[]byte(secret), state.Seed, attemptEpoch, mode,
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

// resolveCodexFingerprintSessionScope 按上游可见身份和稳定的客户端入站传输拆分 Session。
// HTTP Bridge 属于 WS 客户端的内部降级，因此仍保留 WS 入站作用域，不按单次出站链路漂移。
func resolveCodexFingerprintSessionScope(c *gin.Context, headers http.Header, identityEnforced bool) string {
	baseScope := resolveCodexFingerprintTransportAgnosticSessionScope(c, headers, identityEnforced)
	switch GetOpenAIClientTransport(c) {
	case OpenAIClientTransportHTTP:
		return baseScope + ":transport:http"
	case OpenAIClientTransportWS:
		return baseScope + ":transport:ws"
	default:
		// 无法识别传输时保持原有最收敛作用域，也为非网关内部调用保留兼容。
		return baseScope
	}
}

// resolveCodexFingerprintTransportAgnosticSessionScope 逐字保留加入传输维度前的作用域规则，
// 既作为未知传输的保守回退，也用于旧 Thread 绑定续接。
func resolveCodexFingerprintTransportAgnosticSessionScope(c *gin.Context, headers http.Header, identityEnforced bool) string {
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

// resolveCodexFingerprintLegacyClientScope 保留上一版原始客户端槽位，供旧 Thread 续用。
func resolveCodexFingerprintLegacyClientScope(c *gin.Context, headers http.Header) string {
	if headers == nil && c != nil && c.Request != nil {
		headers = c.Request.Header
	}
	userAgent, originator := "", ""
	if headers != nil {
		userAgent = strings.TrimSpace(headers.Get("User-Agent"))
		originator = strings.TrimSpace(headers.Get("originator"))
	}
	family := resolveCodexFingerprintClientFamily(userAgent, originator)
	if family == "" {
		family = resolveCodexFingerprintLegacyUnknownClientFamily(c)
	}
	return "client:" + family
}

// resolveCodexFingerprintLegacyUnknownClientFamily 逐字保留上一版缺失身份时的路径回退。
func resolveCodexFingerprintLegacyUnknownClientFamily(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "unknown"
	}
	path := strings.ToLower(strings.TrimSpace(c.Request.URL.Path))
	switch {
	case strings.Contains(path, "/chat/completions"):
		return "unknown-chat"
	case strings.Contains(path, "/responses"):
		return "unknown-responses"
	default:
		return "unknown"
	}
}

// resolveCodexFingerprintThreadScope 在客户端会话槽位内继续按下游 API Key 隔离 Thread。
func resolveCodexFingerprintThreadScope(c *gin.Context, clientScope string) string {
	return fmt.Sprintf("api-key:%d:%s", getAPIKeyIDFromContext(c), strings.TrimSpace(clientScope))
}

func resolveCodexFingerprintClientFamily(userAgent, originator string) string {
	identity := strings.ToLower(strings.TrimSpace(userAgent + "\n" + originator))
	switch {
	case strings.Contains(identity, "openclaw"):
		return "openclaw"
	case openaiidentity.IsCodexOfficialClientRequestStrict(userAgent) ||
		openaiidentity.IsCodexOfficialClientOriginator(originator):
		return "codex"
	}
	if value := NormalizeSessionUserAgent(originator); value != "" {
		return "originator:" + value
	}
	if value := NormalizeSessionUserAgent(userAgent); value != "" {
		return "ua:" + value
	}
	return ""
}

func resolveCodexFingerprintProtocolFamily(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return "unknown"
	}
	path := strings.ToLower(strings.TrimSpace(c.Request.URL.Path))
	switch {
	case isOpenAIResponsesCompactPath(c):
		return "responses-compact"
	case strings.Contains(path, "/chat/completions"), strings.Contains(path, "/messages"):
		return "chat"
	case strings.Contains(path, "/responses"):
		return "responses"
	default:
		return "unknown"
	}
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

func hashInCodexFingerprintSet(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value != "" && value == candidate {
			return true
		}
	}
	return false
}

// codexFingerprintRotationThresholds 生成作用域独立的年龄、空闲和最长寿命门槛。
func (s *OpenAIGatewayService) codexFingerprintRotationThresholds(accountID int64, scopeHash string, now time.Time) codexFingerprintRotationThresholds {
	minAgeHours, maxAgeHours, idleMinutes, jitterHours := 72, 168, 120, 24
	if s != nil && s.cfg != nil {
		minAgeHours = s.cfg.Gateway.CodexFingerprintMinSessionAgeHours
		maxAgeHours = s.cfg.Gateway.CodexFingerprintMaxSessionAgeHours
		idleMinutes = s.cfg.Gateway.CodexFingerprintIdleGateMinutes
		jitterHours = s.cfg.Gateway.CodexFingerprintRotationJitterHours
	}
	if minAgeHours <= 0 {
		minAgeHours = 72
	}
	if maxAgeHours < minAgeHours {
		maxAgeHours = 168
	}
	if idleMinutes <= 0 {
		idleMinutes = 120
	}
	jitter := codexFingerprintRotationJitter(accountID, scopeHash, jitterHours)
	return codexFingerprintRotationThresholds{
		MinAgeBefore: now.Add(-(time.Duration(minAgeHours)*time.Hour + jitter)),
		IdleBefore:   now.Add(-time.Duration(idleMinutes) * time.Minute),
		MaxAgeBefore: now.Add(-(time.Duration(maxAgeHours)*time.Hour + jitter)),
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
	fpIDs := codexFingerprintIDsFromContext(fpContext)
	if fpIDs == nil {
		fpIDs = resolveCodexFingerprintIDsFromRequest(account, headers)
	}
	stageCodexFingerprintIDs(c, fpIDs)
	return fpIDs, nil
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
		return body, nil
	}
	if !rewriteBody {
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

// applyCodexFingerprintRawForAttempt 在账号选定后统一改写 WS/透传 JSON，并暂存同一份握手头身份。
func (s *OpenAIGatewayService) applyCodexFingerprintRawForAttempt(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	newLogicalTurn bool,
) ([]byte, error) {
	return s.applyCodexFingerprintForAttempt(ctx, c, account, body, newLogicalTurn, true)
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
		fillCodexFingerprintOriginalBodyFallbacks(&original, body)
		original.promptCacheKey = strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String())
	}
	if headers != nil {
		if original.clientSessionID == "" {
			original.clientSessionID = extractClientSessionID(headers)
		}
		if original.threadID == "" {
			original.threadID = firstNonEmptyHeader(headers, "thread-id", "thread_id")
		}
		if original.parentThreadID == "" {
			original.parentThreadID = firstNonEmptyHeader(headers, "x-codex-parent-thread-id")
		}
		original.requestID = firstNonEmptyHeader(headers, "x-client-request-id")
		if original.windowID == "" {
			original.windowID = firstNonEmptyHeader(headers, "x-codex-window-id")
		}
		if original.subagentMarker == "" {
			original.subagentMarker = strings.TrimSpace(headers.Get("x-openai-subagent"))
		}
		if raw := strings.TrimSpace(headers.Get("x-codex-turn-metadata")); raw != "" {
			applyCodexFingerprintOriginalMetadata(&original, gjson.Parse(raw), true)
		}
	}
	original.isSubagent = original.subagentMarker != "" || original.parentThreadID != "" || original.forkedThreadID != ""
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
		{&original.windowID, "client_metadata.x-codex-window-id"},
	}
	for _, field := range fields {
		if *field.target == "" {
			*field.target = strings.TrimSpace(gjson.GetBytes(body, field.path).String())
		}
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
		{&original.windowID, "window_id"},
	}
	for _, field := range fields {
		if !fallbackOnly || *field.target == "" {
			*field.target = strings.TrimSpace(metadata.Get(field.path).String())
		}
	}
	if !fallbackOnly || original.subagentMarker == "" {
		original.subagentMarker = strings.TrimSpace(metadata.Get("subagent_kind").String())
	}
	if original.subagentMarker == "" {
		requestKind := strings.ToLower(strings.TrimSpace(metadata.Get("request_kind").String()))
		if strings.Contains(requestKind, "subagent") {
			original.subagentMarker = requestKind
		}
	}
	if original.subagentMarker == "" {
		threadSource := strings.ToLower(strings.TrimSpace(metadata.Get("thread_source").String()))
		if strings.Contains(threadSource, "subagent") {
			original.subagentMarker = threadSource
		}
	}
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
		mode:             fp.mode,
		sessionScopeHash: fp.sessionScopeHash,
		sessionEpoch:     fp.sessionEpoch,
		installationID:   fp.installationID,
		sessionID:        fp.sessionID,
		threadID:         fp.threadID,
		parentThreadID:   fp.parentThreadID,
		forkedThreadID:   fp.forkedThreadID,
		turnID:           fp.turnID,
		windowID:         fp.windowID,
		promptCacheKey:   fp.promptCacheKey,
		requestID:        fp.requestID,
		isSubagent:       fp.isSubagent,
	}
}

// CodexFingerprintContext 是最终账号选定后生成的不可变出站身份快照。
// 字段保持私有，调用方只能读取，不能在 failover attempt 之间就地修改。
type CodexFingerprintContext struct {
	mode             codexFingerprintMode
	algorithmVersion string
	sessionEpoch     int64
	sessionScopeHash string
	installationID   string
	sessionID        string
	threadID         string
	parentThreadID   string
	forkedThreadID   string
	turnID           string
	windowID         string
	promptCacheKey   string
	requestID        string
	isSubagent       bool
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

// newCodexFingerprintContextV2 按账号 seed、epoch、客户端槽位和原始标识构造 v2 身份。
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
		sessionScopeHash: strings.TrimSpace(original.sessionScopeHash),
		installationID:   strings.TrimSpace(configuredDeviceID),
		isSubagent:       original.isSubagent,
	}
	if ctx.installationID == "" {
		ctx.installationID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, 0, codexFingerprintKindInstallation, "account-device")
	}
	if mode == codexFingerprintDevice {
		return ctx, nil
	}
	if original.legacyUnscoped {
		return populateLegacyUnscopedCodexFingerprintContext(ctx, clusterSecret, seed, epoch, mode, original)
	}

	ctx.sessionID = deriveCodexFingerprintUUIDV2(
		clusterSecret, seed, epoch, codexFingerprintKindSession,
		codexFingerprintScopedDerivationSource(original.clientScope, "account-session"),
	)
	threadScope := strings.TrimSpace(original.threadScope)
	if threadScope == "" {
		threadScope = original.clientScope
	}
	if mode == codexFingerprintFull {
		ctx.threadID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindThread,
			codexFingerprintScopedDerivationSource(threadScope, "account-thread"))
		ctx.turnID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindTurn,
			codexFingerprintScopedDerivationSource(threadScope, original.turnID))
		ctx.windowID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindWindow,
			codexFingerprintScopedDerivationSource(threadScope, "account-window"))
		return ctx, nil
	}
	threadSource := strings.TrimSpace(original.threadID)
	if threadSource == "" {
		threadSource = strings.TrimSpace(original.clientSessionID)
	}
	if threadSource == "" {
		return nil, errCodexFingerprintThreadMissing
	}
	ctx.threadID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindThread,
		codexFingerprintScopedDerivationSource(threadScope, threadSource))
	if value := strings.TrimSpace(original.parentThreadID); value != "" {
		ctx.parentThreadID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindThread,
			codexFingerprintScopedDerivationSource(threadScope, value))
	}
	if value := strings.TrimSpace(original.forkedThreadID); value != "" {
		ctx.forkedThreadID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindThread,
			codexFingerprintScopedDerivationSource(threadScope, value))
	}

	if value := strings.TrimSpace(original.turnID); value != "" {
		ctx.turnID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindTurn,
			codexFingerprintScopedDerivationSource(threadScope, value))
	}
	windowSource := strings.TrimSpace(original.windowID)
	if windowSource == "" {
		windowSource = threadSource + ":0"
	}
	ctx.windowID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindWindow,
		codexFingerprintScopedDerivationSource(threadScope, windowSource))
	if value := strings.TrimSpace(original.promptCacheKey); value != "" {
		ctx.promptCacheKey = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindPromptCache,
			codexFingerprintScopedDerivationSource(threadScope, value))
	}
	if value := strings.TrimSpace(original.requestID); value != "" {
		ctx.requestID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindRequest,
			codexFingerprintScopedDerivationSource(threadScope, value))
	}
	return ctx, nil
}

// populateLegacyUnscopedCodexFingerprintContext 保持 v2 初版 Thread 的派生结果不变。
func populateLegacyUnscopedCodexFingerprintContext(
	ctx *CodexFingerprintContext,
	clusterSecret, seed []byte,
	epoch int64,
	mode codexFingerprintMode,
	original codexFingerprintOriginalIDs,
) (*CodexFingerprintContext, error) {
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
	if value := strings.TrimSpace(original.parentThreadID); value != "" {
		ctx.parentThreadID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindThread, value)
	}
	if value := strings.TrimSpace(original.forkedThreadID); value != "" {
		ctx.forkedThreadID = deriveCodexFingerprintUUIDV2(clusterSecret, seed, epoch, codexFingerprintKindThread, value)
	}
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
