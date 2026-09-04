package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
)

// openAITurnStateCipher 为客户端可见状态提供站点级 AEAD 封装。
type openAITurnStateCipher struct{ key [32]byte }

func newOpenAITurnStateCipher(cfg *config.Config) *openAITurnStateCipher {
	if cfg == nil || strings.TrimSpace(cfg.JWT.Secret) == "" {
		return nil
	}
	return &openAITurnStateCipher{key: sha256.Sum256([]byte("sub2api/turn-state/v1\x00" + cfg.JWT.Secret))}
}
func (c *openAITurnStateCipher) wrap(raw, aad string) (string, error) {
	if c == nil || strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("turn-state cipher unavailable")
	}
	b, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(b)
	if err != nil {
		return "", err
	}
	n := make([]byte, g.NonceSize())
	if _, err = io.ReadFull(rand.Reader, n); err != nil {
		return "", err
	}
	sealed := g.Seal(n, n, []byte(raw), []byte(aad))
	return "ts1." + base64.RawURLEncoding.EncodeToString(sealed), nil
}
func (c *openAITurnStateCipher) unwrap(token, aad string) (string, error) {
	if c == nil || !strings.HasPrefix(token, "ts1.") {
		return "", fmt.Errorf("invalid turn-state wrapper")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "ts1."))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(b) < g.NonceSize() {
		return "", fmt.Errorf("turn-state wrapper too short")
	}
	out, err := g.Open(nil, b[:g.NonceSize()], b[g.NonceSize():], []byte(aad))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func turnStateAAD(accountID int64, sessionHash string) string {
	return fmt.Sprintf("account:%d\x00session:%s", accountID, strings.TrimSpace(sessionHash))
}

func turnStateAADForContext(c *gin.Context, accountID int64) string {
	base := turnStateAAD(accountID, openAITurnStateSessionHash(c))
	logicalTurn := ""
	if c != nil {
		if value, ok := c.Get(codexFingerprintLogicalTurnSourceContextKey); ok {
			logicalTurn, _ = value.(string)
		}
	}
	if c != nil && c.Request != nil {
		if target, ok := OpenAIExecutionTargetFromContext(c.Request.Context()); ok {
			return fmt.Sprintf("%s\x00turn:%s\x00persona:%d\x00generation:%d\x00epoch:%d\x00credential:%s\x00profile:%s", base, logicalTurn, target.AccountPersonaID, target.PersonaGeneration, target.SessionEpoch, target.CredentialChainID, target.ProfileVersion)
		}
	}
	return fmt.Sprintf("%s\x00turn:%s", base, logicalTurn)
}

// openAICodexTurnStateHeader 是 Codex 的回合状态头。上游在响应头中铸造该
// 不透明 blob，客户端在同一回合的后续请求中原样回带（codex-rs 侧从
// /responses SSE、/responses/compact JSON 与 WS 握手三种响应中捕获，见
// codex-api/src/sse/responses.rs 与 endpoint/compact.rs）。
const openAICodexTurnStateHeader = "x-codex-turn-state"

type openAICodexTurnStateOrigin struct {
	accountID int64
	expiresAt time.Time
}

type openAITurnStateClientToken struct {
	token     string
	expiresAt time.Time
}

func openAICodexTurnStateSeed(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	sessionID := extractClientSessionID(c.Request.Header)
	if sessionID == "" {
		return ""
	}
	return strconv.FormatInt(getAPIKeyIDFromContext(c), 10) + "\x00" + sessionID
}

// relayOpenAICodexTurnState 将上游 turn-state 显式写入下游响应头。跨账号溯源
// 由 openai_turn_state_guard.go 按 state 哈希精确绑定，不能再按根 Session 粗记。
func (s *OpenAIGatewayService) relayOpenAICodexTurnState(c *gin.Context, account *Account, upstream http.Header) {
	if c == nil || c.Writer == nil {
		return
	}
	canonical := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	state := extractOpenAICodexTurnState(upstream)
	if state == "" {
		c.Writer.Header().Del(canonical)
		return
	}
	if s.turnStateCipher != nil && account != nil {
		if wrapped, err := s.wrapTurnStateForClient(c, account.ID, state); err == nil {
			state = wrapped
		} else {
			// 加密失败时禁止回落到明文，避免安全边界静默失效。
			c.Writer.Header().Del(canonical)
			return
		}
	}
	c.Writer.Header().Set(canonical, state)
}

func (s *OpenAIGatewayService) wrapTurnStateForClient(c *gin.Context, accountID int64, raw string) (string, error) {
	aad := turnStateAADForContext(c, accountID)
	digest := sha256.Sum256([]byte(aad + "\x00" + raw))
	key := hex.EncodeToString(digest[:])
	if value, ok := s.turnStateClientTokens.Load(key); ok {
		if cached, valid := value.(openAITurnStateClientToken); valid && time.Now().Before(cached.expiresAt) {
			return cached.token, nil
		}
		s.turnStateClientTokens.Delete(key)
	}
	token, err := s.turnStateCipher.wrap(raw, aad)
	if err != nil {
		return "", err
	}
	s.turnStateClientTokens.Store(key, openAITurnStateClientToken{token: token, expiresAt: time.Now().Add(2 * time.Hour)})
	if s.turnStateClientTokenWrites.Add(1)%256 == 0 {
		now := time.Now()
		s.turnStateClientTokens.Range(func(k, v any) bool {
			if cached, ok := v.(openAITurnStateClientToken); !ok || now.After(cached.expiresAt) {
				s.turnStateClientTokens.Delete(k)
			}
			return true
		})
	}
	return token, nil
}

// noteOpenAICodexTurnStateProvenance 记录当前下游会话最近一次铸造账号。
// 持久化 turn-state 归属仍由 openai_turn_state_guard.go 负责，本表仅作快速补充守卫。
func (s *OpenAIGatewayService) noteOpenAICodexTurnStateProvenance(c *gin.Context, account *Account) {
	if s == nil || account == nil || account.ID <= 0 {
		return
	}
	seed := openAICodexTurnStateSeed(c)
	if seed == "" {
		return
	}
	s.openaiCodexTurnStateOrigins.Store(seed, openAICodexTurnStateOrigin{
		accountID: account.ID,
		expiresAt: time.Now().Add(s.openAIWSSessionStickyTTL()),
	})
	s.sweepOpenAICodexTurnStateOrigins()
}

// guardOpenAICodexTurnStateEcho 丢弃已知由其他账号铸造的 turn-state 回带值。
func (s *OpenAIGatewayService) guardOpenAICodexTurnStateEcho(c *gin.Context, account *Account, h http.Header) {
	if s == nil || h == nil || account == nil || strings.TrimSpace(h.Get(openAICodexTurnStateHeader)) == "" {
		return
	}
	seed := openAICodexTurnStateSeed(c)
	if seed == "" {
		return
	}
	raw, ok := s.openaiCodexTurnStateOrigins.Load(seed)
	if !ok {
		return
	}
	origin, ok := raw.(openAICodexTurnStateOrigin)
	if !ok || (!origin.expiresAt.IsZero() && time.Now().After(origin.expiresAt)) {
		s.openaiCodexTurnStateOrigins.Delete(seed)
		return
	}
	if origin.accountID != account.ID {
		h.Del(openAICodexTurnStateHeader)
	}
}

func (s *OpenAIGatewayService) sweepOpenAICodexTurnStateOrigins() {
	if s.openaiCodexTurnStateWrites.Add(1)%256 != 0 {
		return
	}
	now := time.Now()
	s.openaiCodexTurnStateOrigins.Range(func(key, value any) bool {
		origin, ok := value.(openAICodexTurnStateOrigin)
		if !ok || (!origin.expiresAt.IsZero() && now.After(origin.expiresAt)) {
			s.openaiCodexTurnStateOrigins.Delete(key)
		}
		return true
	})
}

// stageOpenAICodexTurnState 将上游 turn-state 暂存到延迟提交的响应头集合
// （首输出守卫路径先缓存头、见到首个输出事件才提交）。该 attempt 仍可能在
// 首输出超时后 failover，因此精确溯源也只能在真正提交时记录。
func stageOpenAICodexTurnState(dst *http.Header, upstream http.Header) {
	if dst == nil {
		return
	}
	canonical := http.CanonicalHeaderKey(openAICodexTurnStateHeader)
	state := extractOpenAICodexTurnState(upstream)
	if state == "" {
		if *dst != nil {
			dst.Del(canonical)
		}
		return
	}
	if *dst == nil {
		*dst = http.Header{}
	}
	dst.Set(canonical, state)
}

func extractOpenAICodexTurnState(upstream http.Header) string {
	if upstream == nil {
		return ""
	}
	return strings.TrimSpace(upstream.Get(openAICodexTurnStateHeader))
}
