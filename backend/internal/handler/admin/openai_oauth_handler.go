package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// OpenAIOAuthHandler handles OpenAI OAuth-related operations
type OpenAIOAuthHandler struct {
	openaiOAuthService *service.OpenAIOAuthService
	adminService       service.AdminService
	quotaService       openAIQuotaService
	rateLimitService   openAIAccountStateRecoverer
}

type openAIQuotaService interface {
	QueryUsage(ctx context.Context, accountID int64) (*service.OpenAIQuotaUsage, error)
	CacheResetCreditsSnapshot(ctx context.Context, accountID int64, credits *service.OpenAIRateLimitResetCredits) error
	CachePostResetSnapshot(ctx context.Context, accountID int64, usage *service.OpenAIQuotaUsage) error
	ResetCredit(ctx context.Context, accountID int64) (*service.OpenAIQuotaResetResult, error)
}

type openAIAccountStateRecoverer interface {
	RecoverAccountState(ctx context.Context, accountID int64, options service.AccountRecoveryOptions) (*service.SuccessfulTestRecoveryResult, error)
}

// openAIQuotaResetPostProcessTimeout bounds the work performed AFTER the
// (non-refundable) reset credit has already been consumed upstream. The whole
// request must stay comfortably inside the panel HTTP client timeout, otherwise
// the browser aborts a mutation that already succeeded and the operator retries
// it — spending a second credit.
const openAIQuotaResetPostProcessTimeout = 8 * time.Second

type openAIQuotaResetResponse struct {
	service.OpenAIQuotaResetResult
	Quota                 *service.OpenAIQuotaUsage `json:"quota,omitempty"`
	Account               *dto.Account              `json:"account,omitempty"`
	CacheRefreshed        bool                      `json:"cache_refreshed"`
	AccountStateRecovered bool                      `json:"account_state_recovered"`
	WarningCode           string                    `json:"warning_code,omitempty"`
}

// openAIQuotaRefreshResponse is the reset-credit-persisting variant of the quota
// query. The usage payload is embedded so the shape stays identical to the plain
// query; cache_persisted reports whether the snapshot write succeeded, because a
// failed display-cache write must never discard a successful upstream read.
type openAIQuotaRefreshResponse struct {
	service.OpenAIQuotaUsage
	CachePersisted bool `json:"cache_persisted"`
}

// openAIQuotaResetPostProcessContext detaches the post-reset bookkeeping from the
// client connection. The credit is already spent at that point, so account-state
// recovery must complete even if the operator closes the tab (mirrors
// systemUpdateContext, added for the same reason in #4504).
func openAIQuotaResetPostProcessContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	return context.WithTimeout(base, openAIQuotaResetPostProcessTimeout)
}

func oauthPlatformFromPath(c *gin.Context) string {
	return service.PlatformOpenAI
}

// NewOpenAIOAuthHandler creates a new OpenAI OAuth handler
func NewOpenAIOAuthHandler(
	openaiOAuthService *service.OpenAIOAuthService,
	adminService service.AdminService,
	quotaService *service.OpenAIQuotaService,
	rateLimitService *service.RateLimitService,
) *OpenAIOAuthHandler {
	h := &OpenAIOAuthHandler{
		openaiOAuthService: openaiOAuthService,
		adminService:       adminService,
	}
	// Assign through explicit nil checks: storing a nil *Service in an interface
	// field yields a non-nil interface, which would silently defeat the
	// `== nil` capability guards below and panic instead of returning 400.
	if quotaService != nil {
		h.quotaService = quotaService
	}
	if rateLimitService != nil {
		h.rateLimitService = rateLimitService
	}
	return h
}

// OpenAIGenerateAuthURLRequest represents the request for generating OpenAI auth URL
type OpenAIGenerateAuthURLRequest struct {
	ProxyID     *int64 `json:"proxy_id"`
	RedirectURI string `json:"redirect_uri"`
}

// GenerateAuthURL generates OpenAI OAuth authorization URL
// POST /api/v1/admin/openai/generate-auth-url
func (h *OpenAIOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req OpenAIGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Allow empty body
		req = OpenAIGenerateAuthURLRequest{}
	}

	result, err := h.openaiOAuthService.GenerateAuthURL(
		c.Request.Context(),
		req.ProxyID,
		req.RedirectURI,
		oauthPlatformFromPath(c),
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, result)
}

// OpenAIExchangeCodeRequest represents the request for exchanging OpenAI auth code
type OpenAIExchangeCodeRequest struct {
	SessionID   string `json:"session_id" binding:"required"`
	Code        string `json:"code" binding:"required"`
	State       string `json:"state" binding:"required"`
	RedirectURI string `json:"redirect_uri"`
	ProxyID     *int64 `json:"proxy_id"`
}

type openAIPersonaExchangeCodeRequest struct {
	SessionID string `json:"session_id" binding:"required"`
	Code      string `json:"code" binding:"required"`
	State     string `json:"state" binding:"required"`
}

func parseOpenAIAccountID(c *gin.Context) (int64, bool) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return accountID, true
}

type openAIAccountPersonaDTO struct {
	ID                              int64                             `json:"id"`
	AccountID                       int64                             `json:"account_id"`
	Position                        int                               `json:"position"`
	ProfileID                       service.SessionPersonaID          `json:"profile_id"`
	ProfileVersion                  string                            `json:"profile_version"`
	CredentialOwner                 service.OpenAICredentialOwner     `json:"credential_owner"`
	State                           service.OpenAIAccountPersonaState `json:"state"`
	Enabled                         bool                              `json:"enabled"`
	Authorized                      bool                              `json:"authorized"`
	PersonaGeneration               int64                             `json:"persona_generation"`
	CurrentSessionEpoch             int64                             `json:"current_session_epoch"`
	ProxyID                         *int64                            `json:"proxy_id,omitempty"`
	MaxActiveClientSessionsOverride *int                              `json:"max_active_client_sessions_override,omitempty"`
	RowVersion                      int64                             `json:"row_version"`
	DefaultProtected                bool                              `json:"default_protected"`
	CredentialState                 string                            `json:"credential_state"`
	CredentialUpdatedAt             *time.Time                        `json:"credential_updated_at,omitempty"`
	CredentialExpiresAt             *time.Time                        `json:"credential_expires_at,omitempty"`
	InstallationSummary             string                            `json:"installation_summary"`
	SessionState                    service.OpenAIPersonaSessionState `json:"session_state,omitempty"`
	SessionStartedAt                *time.Time                        `json:"session_started_at,omitempty"`
	SessionLastActiveAt             *time.Time                        `json:"session_last_active_at,omitempty"`
	ActiveClientSessions            int                               `json:"active_client_sessions"`
	EarliestClientSessionReleaseAt  *time.Time                        `json:"earliest_client_session_release_at,omitempty"`
	EffectiveMaxClientSessions      int                               `json:"effective_max_client_sessions"`
	EffectiveMaxConcurrency         int                               `json:"effective_max_concurrency"`
	EffectiveMaxWebSockets          int                               `json:"effective_max_websockets"`
	EffectiveProxyID                *int64                            `json:"effective_proxy_id,omitempty"`
	ProxyInherited                  bool                              `json:"proxy_inherited"`
	CreatedAt                       time.Time                         `json:"created_at"`
	UpdatedAt                       time.Time                         `json:"updated_at"`
}

func summarizeOpenAIInstallationID(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:6])
}

func openAIAccountPersonaFromService(persona service.OpenAIAccountPersona) openAIAccountPersonaDTO {
	return openAIAccountPersonaFromAdminView(service.OpenAIAccountPersonaAdminView{Persona: persona})
}

func openAIAccountPersonaFromAdminView(view service.OpenAIAccountPersonaAdminView) openAIAccountPersonaDTO {
	persona := view.Persona
	return openAIAccountPersonaDTO{
		ID: persona.ID, AccountID: persona.AccountID, Position: persona.Position,
		ProfileID: persona.ProfileID, ProfileVersion: persona.ProfileVersion,
		CredentialOwner: persona.CredentialOwner, State: persona.State, Enabled: persona.Enabled,
		Authorized:        persona.CurrentCredentialChainID != "" && persona.CurrentSessionEpoch > 0,
		PersonaGeneration: persona.PersonaGeneration, CurrentSessionEpoch: persona.CurrentSessionEpoch,
		ProxyID: persona.ProxyID, MaxActiveClientSessionsOverride: persona.MaxActiveClientSessionsOverride,
		RowVersion: persona.RowVersion, DefaultProtected: persona.IsDefaultProtected(),
		CredentialState: view.CredentialState, CredentialUpdatedAt: view.CredentialUpdatedAt,
		CredentialExpiresAt: view.CredentialExpiresAt, InstallationSummary: summarizeOpenAIInstallationID(persona.InstallationID),
		SessionState: view.SessionState, SessionStartedAt: view.SessionStartedAt, SessionLastActiveAt: view.SessionLastActiveAt,
		ActiveClientSessions: view.ActiveClientSessions, EarliestClientSessionReleaseAt: view.EarliestClientSessionReleaseAt,
		EffectiveMaxClientSessions: view.EffectiveMaxClientSessions, EffectiveMaxConcurrency: view.EffectiveMaxConcurrency,
		EffectiveMaxWebSockets: view.EffectiveMaxWebSockets, EffectiveProxyID: view.EffectiveProxyID, ProxyInherited: view.ProxyInherited,
		CreatedAt: persona.CreatedAt, UpdatedAt: persona.UpdatedAt,
	}
}

func parseOpenAIAccountPersonaTarget(c *gin.Context) (int64, int64, bool) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, 0, false
	}
	personaID, err := strconv.ParseInt(c.Param("persona_id"), 10, 64)
	if err != nil || personaID <= 0 {
		response.BadRequest(c, "Invalid AccountPersona ID")
		return 0, 0, false
	}
	return accountID, personaID, true
}

// ListAccountPersonas 返回动态 Persona 的脱敏管理视图。
func (h *OpenAIOAuthHandler) ListAccountPersonas(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	personas, err := h.openaiOAuthService.ListAccountPersonaAdminViews(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result := make([]openAIAccountPersonaDTO, 0, len(personas))
	for _, persona := range personas {
		result = append(result, openAIAccountPersonaFromAdminView(persona))
	}
	response.Success(c, result)
}

func (h *OpenAIOAuthHandler) CreateAccountPersona(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || accountID <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req struct {
		ProfileID                       service.SessionPersonaID `json:"profile_id" binding:"required"`
		ProxyID                         *int64                   `json:"proxy_id"`
		MaxActiveClientSessionsOverride *int                     `json:"max_active_client_sessions_override"`
	}
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	persona, err := h.openaiOAuthService.CreateAccountPersona(c.Request.Context(), account, req.ProfileID, req.ProxyID, req.MaxActiveClientSessionsOverride)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, openAIAccountPersonaFromService(*persona))
}

func (h *OpenAIOAuthHandler) UpdateAccountPersona(c *gin.Context) {
	accountID, personaID, ok := parseOpenAIAccountPersonaTarget(c)
	if !ok {
		return
	}
	var req struct {
		RowVersion                      int64                              `json:"row_version" binding:"required"`
		Enabled                         *bool                              `json:"enabled"`
		State                           *service.OpenAIAccountPersonaState `json:"state"`
		ProxyConfigured                 bool                               `json:"proxy_configured"`
		ProxyID                         *int64                             `json:"proxy_id"`
		MaxActiveSessionsConfigured     bool                               `json:"max_active_client_sessions_configured"`
		MaxActiveClientSessionsOverride *int                               `json:"max_active_client_sessions_override"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	persona, err := h.openaiOAuthService.UpdateAccountPersona(c.Request.Context(), service.OpenAIAccountPersonaUpdate{
		AccountID: accountID, AccountPersonaID: personaID, ExpectedRowVersion: req.RowVersion,
		Enabled: req.Enabled, State: req.State, ProxyConfigured: req.ProxyConfigured, ProxyID: req.ProxyID,
		MaxActiveSessionsConfigured:     req.MaxActiveSessionsConfigured,
		MaxActiveClientSessionsOverride: req.MaxActiveClientSessionsOverride,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, openAIAccountPersonaFromService(*persona))
}

func (h *OpenAIOAuthHandler) DeleteAccountPersona(c *gin.Context) {
	accountID, personaID, ok := parseOpenAIAccountPersonaTarget(c)
	if !ok {
		return
	}
	rowVersion, err := strconv.ParseInt(c.Query("row_version"), 10, 64)
	if err != nil || rowVersion <= 0 {
		response.BadRequest(c, "row_version is required")
		return
	}
	if err = h.openaiOAuthService.RetireAccountPersona(c.Request.Context(), accountID, personaID, rowVersion); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"retired": true})
}

func (h *OpenAIOAuthHandler) GenerateAccountPersonaAuthURL(c *gin.Context) {
	accountID, personaID, ok := parseOpenAIAccountPersonaTarget(c)
	if !ok {
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result, err := h.openaiOAuthService.GenerateAccountPersonaAuthURL(c.Request.Context(), account, personaID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// GeneratePrimaryAccountPersonaAuthURL 为账号菜单“重新授权”签发 position 0 专用 OAuth Session。
func (h *OpenAIOAuthHandler) GeneratePrimaryAccountPersonaAuthURL(c *gin.Context) {
	accountID, ok := parseOpenAIAccountID(c)
	if !ok {
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	result, err := h.openaiOAuthService.GeneratePrimaryAccountPersonaAuthURL(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *OpenAIOAuthHandler) ExchangeAccountPersonaCode(c *gin.Context) {
	accountID, personaID, ok := parseOpenAIAccountPersonaTarget(c)
	if !ok {
		return
	}
	var req openAIPersonaExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.openaiOAuthService.ExchangeAccountPersonaCode(c.Request.Context(), accountID, personaID, &service.OpenAIExchangeCodeInput{
		SessionID: req.SessionID, Code: req.Code, State: req.State,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	persona, err := h.openaiOAuthService.PersistAccountPersonaAuthorization(c.Request.Context(), account, result)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, openAIAccountPersonaFromService(*persona))
}

// ExchangePrimaryAccountPersonaCode 在服务端交换并直接轮换 position 0 凭据，
// 不再把 runtime Token 回传浏览器后写入账号顶层 credentials。
func (h *OpenAIOAuthHandler) ExchangePrimaryAccountPersonaCode(c *gin.Context) {
	accountID, ok := parseOpenAIAccountID(c)
	if !ok {
		return
	}
	var req openAIPersonaExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	result, err := h.openaiOAuthService.ExchangePrimaryAccountPersonaCode(c.Request.Context(), accountID, &service.OpenAIExchangeCodeInput{
		SessionID: req.SessionID, Code: req.Code, State: req.State,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if _, err = h.openaiOAuthService.PersistPrimaryAccountPersonaAuthorization(c.Request.Context(), account, result); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	updatedAccount, clearErr := h.adminService.ClearAccountError(c.Request.Context(), accountID)
	if clearErr != nil {
		slog.Warn("openai_primary_persona_reauth.clear_account_error_failed", "account_id", accountID, "error", clearErr)
		updatedAccount, _ = h.adminService.GetAccount(c.Request.Context(), accountID)
	}
	if updatedAccount == nil {
		updatedAccount = account
	}
	response.Success(c, dto.AccountFromService(updatedAccount))
}

func (h *OpenAIOAuthHandler) RevokeAccountPersonaAuthorization(c *gin.Context) {
	accountID, personaID, ok := parseOpenAIAccountPersonaTarget(c)
	if !ok {
		return
	}
	var req struct {
		RowVersion int64 `json:"row_version" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.openaiOAuthService.RevokeAccountPersonaAuthorization(c.Request.Context(), accountID, personaID, req.RowVersion); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"revoked": true})
}

func (h *OpenAIOAuthHandler) RefreshAccountPersonaAuthorization(c *gin.Context) {
	accountID, personaID, ok := parseOpenAIAccountPersonaTarget(c)
	if !ok {
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if _, err = h.openaiOAuthService.RefreshAccountPersonaCredential(c.Request.Context(), account, personaID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	personas, err := h.openaiOAuthService.ListAccountPersonas(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	for _, persona := range personas {
		if persona.ID == personaID {
			response.Success(c, openAIAccountPersonaFromService(persona))
			return
		}
	}
	response.NotFound(c, "AccountPersona not found")
}

// RotateAccountPersonaSession 推进单个 Persona 的 Session epoch，不返回原始出站 Session ID。
func (h *OpenAIOAuthHandler) RotateAccountPersonaSession(c *gin.Context) {
	accountID, personaID, ok := parseOpenAIAccountPersonaTarget(c)
	if !ok {
		return
	}
	var req struct {
		RowVersion   int64  `json:"row_version" binding:"required"`
		Force        bool   `json:"force"`
		Confirmation string `json:"confirmation"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if req.Force && req.Confirmation != "FORCE_ROTATE_PERSONA_SESSION" {
		response.BadRequest(c, "force rotation requires explicit confirmation")
		return
	}
	result, err := h.openaiOAuthService.RotateAccountPersonaSession(
		c.Request.Context(), accountID, personaID, req.RowVersion, req.Force,
	)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, openAIAccountPersonaFromService(result.Persona))
}

func (h *OpenAIOAuthHandler) ListAccountPersonaProfiles(c *gin.Context) {
	profiles := service.NewDefaultSessionPersonaRegistry().List()
	type profileDTO struct {
		ID                  service.SessionPersonaID          `json:"id"`
		Version             string                            `json:"version"`
		SupportedTransports []service.SessionPersonaTransport `json:"supported_transports"`
		Compression         service.SessionPersonaCompression `json:"compression"`
	}
	result := make([]profileDTO, 0, len(profiles))
	for _, profile := range profiles {
		result = append(result, profileDTO{
			ID: profile.ID, Version: profile.EffectiveVersion(),
			SupportedTransports: append([]service.SessionPersonaTransport(nil), profile.SupportedTransports...),
			Compression:         profile.Compression,
		})
	}
	response.Success(c, result)
}

// ExchangeCode exchanges OpenAI authorization code for tokens
// POST /api/v1/admin/openai/exchange-code
func (h *OpenAIOAuthHandler) ExchangeCode(c *gin.Context) {
	var req OpenAIExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	tokenInfo, err := h.openaiOAuthService.ExchangeCode(c.Request.Context(), &service.OpenAIExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// OpenAIRefreshTokenRequest represents the request for refreshing OpenAI token
type OpenAIRefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
	RT           string `json:"rt"`
	ClientID     string `json:"client_id"`
	ProxyID      *int64 `json:"proxy_id"`
}

type OpenAICodexPATCreateRequest struct {
	AccessToken             string         `json:"access_token" binding:"required"`
	Name                    string         `json:"name"`
	Notes                   *string        `json:"notes"`
	GroupIDs                []int64        `json:"group_ids"`
	ProxyID                 *int64         `json:"proxy_id"`
	Concurrency             *int           `json:"concurrency"`
	Priority                *int           `json:"priority"`
	RateMultiplier          *float64       `json:"rate_multiplier"`
	LoadFactor              *int           `json:"load_factor"`
	ExpiresAt               *int64         `json:"expires_at"`
	AutoPauseOnExpired      *bool          `json:"auto_pause_on_expired"`
	CredentialExtras        map[string]any `json:"credential_extras"`
	Extra                   map[string]any `json:"extra"`
	SkipDefaultGroupBind    *bool          `json:"skip_default_group_bind"`
	ConfirmMixedChannelRisk *bool          `json:"confirm_mixed_channel_risk"`
}

// RefreshToken refreshes an OpenAI OAuth token
// POST /api/v1/admin/openai/refresh-token
func (h *OpenAIOAuthHandler) RefreshToken(c *gin.Context) {
	var req OpenAIRefreshTokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	refreshToken := strings.TrimSpace(req.RefreshToken)
	if refreshToken == "" {
		refreshToken = strings.TrimSpace(req.RT)
	}
	if refreshToken == "" {
		response.BadRequest(c, "refresh_token is required")
		return
	}

	var proxyURL string
	if req.ProxyID != nil {
		proxy, err := h.adminService.GetProxy(c.Request.Context(), *req.ProxyID)
		if err == nil && proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	// 未指定 client_id 时，根据请求路径平台自动设置默认值，避免 repository 层盲猜
	clientID := strings.TrimSpace(req.ClientID)
	if clientID == "" {
		platform := oauthPlatformFromPath(c)
		clientID, _ = openai.OAuthClientConfigByPlatform(platform)
	}

	tokenInfo, err := h.openaiOAuthService.RefreshTokenWithClientID(c.Request.Context(), refreshToken, proxyURL, clientID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, tokenInfo)
}

// RefreshAccountToken refreshes token for a specific OpenAI account
// POST /api/v1/admin/openai/accounts/:id/refresh
func (h *OpenAIOAuthHandler) RefreshAccountToken(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}

	// Get account
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	platform := oauthPlatformFromPath(c)
	if account.Platform != platform {
		response.BadRequest(c, "Account platform does not match OAuth endpoint")
		return
	}

	// Only refresh OAuth-based accounts
	if !account.IsOAuth() {
		response.BadRequest(c, "Cannot refresh non-OAuth account credentials")
		return
	}

	// spark 影子账号凭据透传母账号、自身恒空,刷新无意义;在调用上游前早拒,避免先打上游
	// 再被凭据写守卫拦下的无谓副作用(外审第6轮)。
	if account.IsCredentialShadow() {
		response.BadRequest(c, "Cannot refresh spark shadow account; its credentials are managed by the parent account")
		return
	}

	// Use OpenAI OAuth service to refresh token
	tokenInfo, err := h.openaiOAuthService.RefreshAccountToken(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if account.IsOpenAIOAuth() && !account.IsOpenAIPersonalAccessToken() && !account.IsOpenAIAgentIdentity() {
		// Persona refresh already persisted the credential chain. Never write
		// returned runtime tokens back to the account-level credentials JSON.
		updatedAccount, loadErr := h.adminService.GetAccount(c.Request.Context(), accountID)
		if loadErr != nil || updatedAccount == nil {
			if loadErr != nil {
				response.ErrorFrom(c, loadErr)
			} else {
				response.NotFound(c, "Account not found")
			}
			return
		}
		response.Success(c, dto.AccountFromService(updatedAccount))
		return
	}

	// Build new credentials from token info
	newCredentials := h.openaiOAuthService.BuildAccountCredentials(tokenInfo)

	// Preserve non-token settings from existing credentials
	for k, v := range account.Credentials {
		if _, exists := newCredentials[k]; !exists {
			newCredentials[k] = v
		}
	}
	newCredentials = service.NormalizeOpenAIPersonalAccessTokenCredentials(account, tokenInfo, newCredentials)

	updatedAccount, err := h.adminService.UpdateAccount(c.Request.Context(), accountID, &service.UpdateAccountInput{
		Credentials: newCredentials,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.AccountFromService(updatedAccount))
}

// CreateAccountFromOAuth creates a new OpenAI OAuth account from token info
// POST /api/v1/admin/openai/create-from-oauth
func (h *OpenAIOAuthHandler) CreateAccountFromOAuth(c *gin.Context) {
	var req struct {
		SessionID   string  `json:"session_id" binding:"required"`
		Code        string  `json:"code" binding:"required"`
		State       string  `json:"state" binding:"required"`
		RedirectURI string  `json:"redirect_uri"`
		ProxyID     *int64  `json:"proxy_id"`
		Name        string  `json:"name"`
		Concurrency int     `json:"concurrency"`
		Priority    int     `json:"priority"`
		GroupIDs    []int64 `json:"group_ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	// Exchange code for tokens
	tokenInfo, err := h.openaiOAuthService.ExchangeCode(c.Request.Context(), &service.OpenAIExchangeCodeInput{
		SessionID:   req.SessionID,
		Code:        req.Code,
		State:       req.State,
		RedirectURI: req.RedirectURI,
		ProxyID:     req.ProxyID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	primaryPersona, err := h.openaiOAuthService.BuildPrimaryOpenAIPersona(tokenInfo)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 账号只保存非机密身份元数据，Token 的唯一权威副本属于 position 0。
	credentials := h.openaiOAuthService.BuildAccountIdentityCredentials(tokenInfo)

	platform := oauthPlatformFromPath(c)

	// Use email as default name if not provided
	name := req.Name
	if name == "" && tokenInfo.Email != "" {
		name = tokenInfo.Email
	}
	if name == "" {
		name = "OpenAI OAuth Account"
	}

	// Create account
	account, err := h.adminService.CreateAccount(c.Request.Context(), &service.CreateAccountInput{
		Name:                 name,
		Platform:             platform,
		Type:                 "oauth",
		Credentials:          credentials,
		Extra:                nil,
		ProxyID:              req.ProxyID,
		Concurrency:          req.Concurrency,
		Priority:             req.Priority,
		GroupIDs:             req.GroupIDs,
		PrimaryOpenAIPersona: primaryPersona,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.AccountFromService(account))
}

// CreateAccountFromCodexPAT creates an OpenAI OAuth account from a Codex at-* personal access token.
// POST /api/v1/admin/openai/create-from-codex-pat
func (h *OpenAIOAuthHandler) CreateAccountFromCodexPAT(c *gin.Context) {
	var req OpenAICodexPATCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := service.ValidateOpenAILongContextBillingExtra(service.PlatformOpenAI, req.Extra); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if req.Concurrency != nil && *req.Concurrency < 0 {
		response.BadRequest(c, "concurrency must be >= 0")
		return
	}
	if req.Priority != nil && *req.Priority < 0 {
		response.BadRequest(c, "priority must be >= 0")
		return
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	if req.LoadFactor != nil && *req.LoadFactor > 10000 {
		response.BadRequest(c, "load_factor must be <= 10000")
		return
	}

	var proxyURL string
	if req.ProxyID != nil {
		proxy, err := h.adminService.GetProxy(c.Request.Context(), *req.ProxyID)
		if err != nil {
			response.ErrorFrom(c, err)
			return
		}
		if proxy != nil {
			proxyURL = proxy.URL()
		}
	}

	tokenInfo, err := h.openaiOAuthService.ValidateCodexPersonalAccessToken(c.Request.Context(), req.AccessToken, proxyURL)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	credentials := mergeCodexImportMap(
		h.openaiOAuthService.BuildAccountCredentials(tokenInfo),
		sanitizeCodexImportCredentialExtras(req.CredentialExtras),
	)
	extra := mergeCodexImportMap(req.Extra, map[string]any{
		"import_source":       "codex_personal_access_token",
		"auth_provider":       "codex_personal_access_token",
		"imported_at":         time.Now().UTC().Format(time.RFC3339),
		"access_token_sha256": codexTokenFingerprint(req.AccessToken),
	})

	concurrency := 3
	if req.Concurrency != nil {
		concurrency = *req.Concurrency
	}
	priority := 50
	if req.Priority != nil {
		priority = *req.Priority
	}
	skipDefaultGroupBind := false
	if req.SkipDefaultGroupBind != nil {
		skipDefaultGroupBind = *req.SkipDefaultGroupBind
	}

	account, err := h.adminService.CreateAccount(c.Request.Context(), &service.CreateAccountInput{
		Name:                  buildOpenAICodexPATAccountName(req.Name, tokenInfo),
		Notes:                 req.Notes,
		Platform:              service.PlatformOpenAI,
		Type:                  service.AccountTypeOAuth,
		Credentials:           credentials,
		Extra:                 extra,
		ProxyID:               req.ProxyID,
		Concurrency:           concurrency,
		Priority:              priority,
		RateMultiplier:        req.RateMultiplier,
		LoadFactor:            req.LoadFactor,
		GroupIDs:              req.GroupIDs,
		ExpiresAt:             req.ExpiresAt,
		AutoPauseOnExpired:    req.AutoPauseOnExpired,
		SkipDefaultGroupBind:  skipDefaultGroupBind,
		SkipMixedChannelCheck: req.ConfirmMixedChannelRisk != nil && *req.ConfirmMixedChannelRisk,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.AccountFromService(account))
}

func buildOpenAICodexPATAccountName(name string, tokenInfo *service.OpenAITokenInfo) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	if tokenInfo != nil {
		for _, candidate := range []string{tokenInfo.Email, tokenInfo.ChatGPTAccountID, tokenInfo.ChatGPTUserID} {
			if candidate = strings.TrimSpace(candidate); candidate != "" {
				return candidate
			}
		}
	}
	return "Codex PAT Account"
}

// QueryQuota queries the rate-limit / quota usage for an OpenAI account.
// GET /api/v1/admin/openai/accounts/:id/quota
func (h *OpenAIOAuthHandler) QueryQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}

	usage, err := h.quotaService.QueryUsage(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	service.NotifyOpenAIAutoResetCredit(accountID)
	response.Success(c, usage)
}

// RefreshQuota queries the rate-limit / quota usage AND persists the reset-credit
// snapshot so the card can be rehydrated without an upstream round-trip.
// POST /api/v1/admin/openai/accounts/:id/quota/refresh
//
// It is a POST (not a GET with a side-effect flag) because it writes account
// state: the audit middleware only records mutating verbs, so a persisting GET
// would mutate the database without an audit trail.
func (h *OpenAIOAuthHandler) RefreshQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}

	usage, err := h.quotaService.QueryUsage(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if usage == nil {
		response.Error(c, http.StatusInternalServerError, "openai quota query returned an empty result")
		return
	}
	service.NotifyOpenAIAutoResetCredit(accountID)

	refreshResponse := openAIQuotaRefreshResponse{OpenAIQuotaUsage: *usage}
	// A failed snapshot write leaves the previous cache intact — report it as a
	// partial success instead of discarding the usage payload we just fetched,
	// which would leave the card without a credit count at all.
	if err := h.quotaService.CacheResetCreditsSnapshot(c.Request.Context(), accountID, usage.RateLimitResetCredits); err != nil {
		slog.Warn("openai_quota_reset_credit_cache_persist_failed", "account_id", accountID, "error", err)
		response.Success(c, refreshResponse)
		return
	}
	refreshResponse.CachePersisted = true
	response.Success(c, refreshResponse)
}

// CreateShadowRequest is the request body for CreateShadow.
type CreateShadowRequest struct {
	Name        string  `json:"name"`
	Priority    int     `json:"priority"`
	Concurrency int     `json:"concurrency"`
	GroupIDs    []int64 `json:"group_ids"`
}

// CreateShadow creates a spark-dimension shadow account for a parent OpenAI OAuth account.
// POST /api/v1/admin/accounts/:id/shadow
func (h *OpenAIOAuthHandler) CreateShadow(c *gin.Context) {
	parentID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}

	var req CreateShadowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}

	shadow, err := h.adminService.CreateShadow(c.Request.Context(), parentID, service.ShadowOptions{
		Name:        req.Name,
		Priority:    req.Priority,
		Concurrency: req.Concurrency,
		GroupIDs:    req.GroupIDs,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, dto.AccountFromServiceShallow(shadow))
}

// ResetQuota consumes one rate-limit reset credit for an OpenAI account.
// POST /api/v1/admin/openai/accounts/:id/reset-quota
func (h *OpenAIOAuthHandler) ResetQuota(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	if h.quotaService == nil {
		response.BadRequest(c, "openai quota service is not enabled")
		return
	}
	result, err := h.quotaService.ResetCredit(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if result == nil {
		response.Error(c, http.StatusInternalServerError, "openai quota reset returned an empty result")
		return
	}

	resetResponse := openAIQuotaResetResponse{OpenAIQuotaResetResult: *result}
	postCtx, cancelPost := openAIQuotaResetPostProcessContext(c.Request.Context())
	defer cancelPost()

	postResult := service.RunOpenAIQuotaResetPostProcess(
		postCtx,
		accountID,
		h.quotaService,
		h.rateLimitService,
		h.adminService.GetAccount,
	)
	resetResponse.Quota = postResult.Quota
	resetResponse.CacheRefreshed = postResult.CacheRefreshed
	resetResponse.AccountStateRecovered = postResult.AccountStateRecovered
	resetResponse.WarningCode = postResult.WarningCode
	if postResult.Account != nil {
		resetResponse.Account = dto.AccountFromService(postResult.Account)
	}
	response.Success(c, resetResponse)
}
