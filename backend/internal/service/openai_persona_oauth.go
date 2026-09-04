package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const (
	openAIPersonaRefreshWaitTimeout  = 15 * time.Second
	openAIPersonaRefreshPollInterval = 100 * time.Millisecond
)

// OpenAIPersonaOAuthExchangeResult carries the server-bound target alongside
// token data. The handler persists only this target; it never trusts Persona
// identity supplied by the browser after URL generation.
type OpenAIPersonaOAuthExchangeResult struct {
	TokenInfo         *OpenAITokenInfo
	AccountID         int64
	AccountPersonaID  int64
	PersonaID         SessionPersonaID
	ProfileVersion    string
	PersonaGeneration int64
	PersonaRowVersion int64
	CredentialChainID string
	InstallationID    string
}

// BuildPrimaryOpenAIPersona 将首次 Codex OAuth 结果转换为可原子建号的受保护 Persona。
func (s *OpenAIOAuthService) BuildPrimaryOpenAIPersona(tokenInfo *OpenAITokenInfo) (*OpenAIPrimaryPersonaCreate, error) {
	if tokenInfo == nil || strings.TrimSpace(tokenInfo.AccessToken) == "" || strings.TrimSpace(tokenInfo.RefreshToken) == "" ||
		strings.TrimSpace(tokenInfo.ChatGPTAccountID) == "" {
		return nil, infraerrors.BadRequest("OPENAI_PRIMARY_PERSONA_TOKEN_INCOMPLETE", "OpenAI OAuth did not return a complete primary credential chain")
	}
	profile, ok := NewDefaultSessionPersonaRegistry().Get(string(SessionPersonaCodexCLIStrict))
	if !ok || !profile.Valid() {
		return nil, infraerrors.InternalServer("OPENAI_PERSONA_PROFILE_MISSING", "strict Codex Persona profile is unavailable")
	}
	payload, err := s.encryptPersonaCredential(tokenInfo)
	if err != nil {
		return nil, fmt.Errorf("encrypt primary OpenAI Persona credential: %w", err)
	}
	seed, err := openai.GenerateRandomBytes(32)
	if err != nil {
		return nil, fmt.Errorf("generate primary OpenAI Persona device seed: %w", err)
	}
	chainNonce, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	installationNonce, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	upstreamSessionID, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	return &OpenAIPrimaryPersonaCreate{
		ProfileVersion: profile.EffectiveVersion(), CredentialChainID: string(profile.ID) + "-" + chainNonce,
		EncryptedPayload: payload, ChatGPTAccountID: strings.TrimSpace(tokenInfo.ChatGPTAccountID),
		OAuthClientID: strings.TrimSpace(tokenInfo.ClientID), DeviceSeed: seed,
		InstallationID:    string(profile.ID) + "-" + installationNonce,
		UpstreamSessionID: upstreamSessionID,
	}, nil
}

func (s *OpenAIOAuthService) ListAccountPersonas(ctx context.Context, accountID int64) ([]OpenAIAccountPersona, error) {
	if s == nil || s.accountPersonaRepo == nil {
		return nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	return s.accountPersonaRepo.ListAccountPersonas(ctx, accountID)
}

// ListAccountPersonasByAccountIDs 批量读取管理与容量投影所需 Persona；测试桩不支持批量时保持兼容回落。
func (s *OpenAIOAuthService) ListAccountPersonasByAccountIDs(ctx context.Context, accountIDs []int64) (map[int64][]OpenAIAccountPersona, error) {
	if s == nil || s.accountPersonaRepo == nil {
		return nil, ErrOpenAIAccountPersonaNotFound
	}
	if reader, ok := s.accountPersonaRepo.(OpenAIAccountPersonaBatchReader); ok {
		return reader.ListAccountPersonasByAccountIDs(ctx, accountIDs)
	}
	result := make(map[int64][]OpenAIAccountPersona, len(accountIDs))
	for _, accountID := range accountIDs {
		personas, err := s.accountPersonaRepo.ListAccountPersonas(ctx, accountID)
		if err != nil {
			return nil, err
		}
		result[accountID] = personas
	}
	return result, nil
}

// ListAccountPersonaAdminViews 返回账号详情页使用的动态 Persona 权威快照。
func (s *OpenAIOAuthService) ListAccountPersonaAdminViews(ctx context.Context, account *Account) ([]OpenAIAccountPersonaAdminView, error) {
	if account == nil || !account.IsOpenAIOAuth() || s == nil || s.accountPersonaRepo == nil {
		return nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	personas, err := s.accountPersonaRepo.ListAccountPersonas(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	stats := make(map[int64]OpenAIAccountPersonaLeaseStats)
	if reader, ok := s.accountPersonaRepo.(OpenAIAccountPersonaLeaseStatsReader); ok {
		stats, err = reader.ListAccountPersonaLeaseStats(ctx, account.ID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	}
	cfg := DefaultOpenAIAccountAdmissionConfig()
	if s.settingService != nil {
		if current, cfgErr := s.settingService.GetOpenAIAccountAdmissionConfig(ctx); cfgErr == nil {
			cfg = current
		}
	}
	views := make([]OpenAIAccountPersonaAdminView, 0, len(personas))
	for _, persona := range personas {
		policy := cfg.EffectiveOpenAIPersonaPolicyForAccount(account, persona.ProfileID, 0)
		view := OpenAIAccountPersonaAdminView{
			Persona: persona, CredentialState: "unconfigured", ProxyInherited: persona.ProxyID == nil,
			EffectiveMaxClientSessions: policy.MaxActiveClientSessions,
			EffectiveMaxConcurrency:    policy.MaxConcurrency, EffectiveMaxWebSockets: policy.MaxActiveWebSockets,
		}
		if persona.MaxActiveClientSessionsOverride != nil {
			view.EffectiveMaxClientSessions = *persona.MaxActiveClientSessionsOverride
		}
		if lease := stats[persona.ID]; lease.ActiveClientSessions > 0 || lease.EarliestReleaseAt != nil {
			view.ActiveClientSessions = lease.ActiveClientSessions
			view.EarliestClientSessionReleaseAt = lease.EarliestReleaseAt
		}
		if persona.CurrentCredentialChainID != "" {
			record, credentialErr := s.accountPersonaRepo.GetAccountPersonaCredential(ctx, persona.ID, persona.CurrentCredentialChainID)
			if credentialErr != nil {
				return nil, credentialErr
			}
			view.CredentialState = record.State
			updated := record.UpdatedAt
			view.CredentialUpdatedAt = &updated
			if record.State == "ready" || record.State == "refreshing" {
				if info, decryptErr := s.decryptPersonaCredential(record); decryptErr == nil && info.ExpiresAt > 0 {
					expires := time.Unix(info.ExpiresAt, 0).UTC()
					view.CredentialExpiresAt = &expires
				}
			}
		}
		if persona.CurrentSessionEpoch > 0 {
			session, sessionErr := s.accountPersonaRepo.GetAccountPersonaSession(ctx, account.ID, persona.ID, persona.CurrentSessionEpoch, time.Now().UTC())
			if sessionErr != nil {
				return nil, sessionErr
			}
			view.SessionState = session.State
			started := session.StartedAt
			view.SessionStartedAt = &started
			view.SessionLastActiveAt = session.LastActiveAt
			view.EffectiveProxyID = session.EffectiveProxyID
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *OpenAIOAuthService) CreateAccountPersona(ctx context.Context, account *Account, profileID SessionPersonaID, proxyID *int64, maxActive *int) (*OpenAIAccountPersona, error) {
	if account == nil || !account.IsOpenAIOAuth() || s == nil || s.accountPersonaRepo == nil {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_ACCOUNT_INVALID", "account is not an OpenAI OAuth account")
	}
	profile, ok := NewDefaultSessionPersonaRegistry().Get(string(profileID))
	if !ok || !profile.Valid() {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_PROFILE_INVALID", "unsupported OpenAI Persona profile")
	}
	if maxActive != nil && (*maxActive < 1 || *maxActive > maxPersonaPolicySessions) {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_CLIENT_LIMIT_INVALID", "max_active_client_sessions must be between 1 and 10000")
	}
	seed, err := openai.GenerateRandomBytes(32)
	if err != nil {
		return nil, err
	}
	nonce, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	return s.accountPersonaRepo.CreateAccountPersona(ctx, OpenAIAccountPersonaCreate{
		AccountID: account.ID, ProfileID: profile.ID, ProfileVersion: profile.EffectiveVersion(),
		ProxyID: proxyID, MaxActiveClientSessionsOverride: maxActive,
		DeviceSeed: seed, InstallationID: string(profile.ID) + "-" + nonce,
	})
}

func (s *OpenAIOAuthService) UpdateAccountPersona(ctx context.Context, input OpenAIAccountPersonaUpdate) (*OpenAIAccountPersona, error) {
	if s == nil || s.accountPersonaRepo == nil {
		return nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	if input.MaxActiveSessionsConfigured && input.MaxActiveClientSessionsOverride != nil &&
		(*input.MaxActiveClientSessionsOverride < 1 || *input.MaxActiveClientSessionsOverride > maxPersonaPolicySessions) {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_CLIENT_LIMIT_INVALID", "max_active_client_sessions must be between 1 and 10000")
	}
	if input.ProxyConfigured || (input.Enabled != nil && *input.Enabled) ||
		(input.State != nil && *input.State == OpenAIAccountPersonaStateActive) {
		input.NewUpstreamSessionID, _ = openai.GenerateSessionID()
		policy := defaultCodexFingerprintEpochPolicy()
		if s.settingService != nil {
			policy = s.settingService.GetCodexFingerprintEpochPolicy(ctx)
		}
		input.OldSessionExpiresAt = time.Now().Add(time.Duration(policy.OldEpochGraceHours) * time.Hour)
	}
	persona, err := s.accountPersonaRepo.UpdateAccountPersona(ctx, input)
	return persona, openAIAccountPersonaAPIError(err)
}

func (s *OpenAIOAuthService) RetireAccountPersona(ctx context.Context, accountID, accountPersonaID, expectedRowVersion int64) error {
	if s == nil || s.accountPersonaRepo == nil {
		return ErrOpenAIPersonaCredentialStoreUnavailable
	}
	return openAIAccountPersonaAPIError(s.accountPersonaRepo.RetireAccountPersona(ctx, accountID, accountPersonaID, expectedRowVersion))
}

func (s *OpenAIOAuthService) GenerateAccountPersonaAuthURL(ctx context.Context, account *Account, accountPersonaID int64) (*OpenAIAuthURLResult, error) {
	if account == nil || !account.IsOpenAIOAuth() || s == nil || s.accountPersonaRepo == nil {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_ACCOUNT_INVALID", "account is not an OpenAI OAuth account")
	}
	personaRecord, err := s.accountPersonaRepo.GetAccountPersona(ctx, account.ID, accountPersonaID)
	if err != nil {
		return nil, err
	}
	if personaRecord.IsDefaultProtected() {
		return nil, infraerrors.Conflict("DEFAULT_PERSONA_PROTECTED", "default Persona authorization is managed by account login")
	}
	return s.generateAccountPersonaAuthURL(ctx, account, personaRecord)
}

// GeneratePrimaryAccountPersonaAuthURL 将账号“重新授权”明确绑定到受保护的 position 0。
// Token 交换结果留在服务端，避免重新落回已废弃的账号顶层 runtime Token 路径。
func (s *OpenAIOAuthService) GeneratePrimaryAccountPersonaAuthURL(ctx context.Context, account *Account) (*OpenAIAuthURLResult, error) {
	if account == nil || !account.IsOpenAIOAuth() || s == nil || s.accountPersonaRepo == nil {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_ACCOUNT_INVALID", "account is not an OpenAI OAuth account")
	}
	personas, err := s.accountPersonaRepo.ListAccountPersonas(ctx, account.ID)
	if err != nil {
		return nil, openAIAccountPersonaAPIError(err)
	}
	for i := range personas {
		if personas[i].IsDefaultProtected() {
			return s.generateAccountPersonaAuthURL(ctx, account, &personas[i])
		}
	}
	return nil, infraerrors.Conflict("OPENAI_PRIMARY_PERSONA_MISSING", "OpenAI account primary Persona is unavailable")
}

func (s *OpenAIOAuthService) generateAccountPersonaAuthURL(ctx context.Context, account *Account, personaRecord *OpenAIAccountPersona) (*OpenAIAuthURLResult, error) {
	if personaRecord == nil || personaRecord.AccountID != account.ID {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_TARGET_MISMATCH", "OAuth target does not belong to this account")
	}
	profile, ok := NewDefaultSessionPersonaRegistry().Get(string(personaRecord.ProfileID))
	if !ok || !profile.Valid() || profile.EffectiveVersion() != personaRecord.ProfileVersion {
		return nil, infraerrors.Conflict("OPENAI_PERSONA_PROFILE_VERSION_UNAVAILABLE", "Persona profile version is unavailable")
	}
	state, err := openai.GenerateState()
	if err != nil {
		return nil, err
	}
	verifier, err := openai.GenerateCodeVerifier()
	if err != nil {
		return nil, err
	}
	sessionID, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	chainNonce, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	proxyURL, err := s.openAIAccountPersonaProxyURL(ctx, account, personaRecord)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(profile.OAuthClientID)
	if clientID == "" {
		clientID = openai.ClientID
	}
	oauthProfile := openAIPersonaOAuthProfile(profile)
	s.sessionStore.Set(sessionID, &openai.OAuthSession{
		State: state, CodeVerifier: verifier, ClientID: clientID, ProxyURL: proxyURL,
		RedirectURI: openai.DefaultRedirectURI, CreatedAt: time.Now(), AccountID: account.ID,
		AccountPersonaID: personaRecord.ID, PersonaID: string(profile.ID), ProfileVersion: profile.EffectiveVersion(),
		CredentialChainID: string(profile.ID) + "-" + chainNonce,
		InstallationID:    personaRecord.InstallationID, PersonaGeneration: personaRecord.PersonaGeneration,
		PersonaRowVersion:        personaRecord.RowVersion,
		ExpectedChatGPTAccountID: strings.TrimSpace(account.GetChatGPTAccountID()),
		UserAgent:                oauthProfile.UserAgent, Originator: oauthProfile.Originator,
	})
	originator := ""
	if profile.ID == SessionPersonaOpenCode {
		originator = oauthProfile.Originator
	}
	return &OpenAIAuthURLResult{
		AuthURL:   openai.BuildAuthorizationURLWithClient(state, openai.GenerateCodeChallenge(verifier), openai.DefaultRedirectURI, clientID, true, originator),
		SessionID: sessionID,
	}, nil
}

// ExchangePrimaryAccountPersonaCode 只接受由账号级主 Persona 入口签发的 OAuth Session。
// 浏览器无需也不能提交 Persona ID，因此不能把账号重新授权改投到其他 Persona。
func (s *OpenAIOAuthService) ExchangePrimaryAccountPersonaCode(ctx context.Context, accountID int64, input *OpenAIExchangeCodeInput) (*OpenAIPersonaOAuthExchangeResult, error) {
	if input == nil {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_REQUEST_INVALID", "OAuth exchange input is required")
	}
	session, ok := s.sessionStore.Get(input.SessionID)
	if !ok || session.AccountID != accountID || session.AccountPersonaID <= 0 {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_TARGET_MISMATCH", "OAuth session target does not match this account primary Persona")
	}
	persona, err := s.accountPersonaRepo.GetAccountPersona(ctx, accountID, session.AccountPersonaID)
	if err != nil {
		return nil, openAIAccountPersonaAPIError(err)
	}
	if !persona.IsDefaultProtected() {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_TARGET_MISMATCH", "OAuth session is not bound to the account primary Persona")
	}
	return s.ExchangeAccountPersonaCode(ctx, accountID, persona.ID, input)
}

func (s *OpenAIOAuthService) ExchangeAccountPersonaCode(ctx context.Context, accountID, accountPersonaID int64, input *OpenAIExchangeCodeInput) (*OpenAIPersonaOAuthExchangeResult, error) {
	if input == nil {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_REQUEST_INVALID", "OAuth exchange input is required")
	}
	session, ok := s.sessionStore.Get(input.SessionID)
	if !ok || session.AccountID != accountID || session.AccountPersonaID != accountPersonaID || session.PersonaID == "" {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_TARGET_MISMATCH", "OAuth session target does not match this AccountPersona")
	}
	if input.State == "" || subtle.ConstantTimeCompare([]byte(input.State), []byte(session.State)) != 1 {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_INVALID_STATE", "invalid OAuth state")
	}
	profile, ok := NewDefaultSessionPersonaRegistry().Get(session.PersonaID)
	if !ok || !profile.Valid() || profile.EffectiveVersion() != session.ProfileVersion {
		return nil, infraerrors.Conflict("OPENAI_PERSONA_PROFILE_VERSION_UNAVAILABLE", "Persona profile version is unavailable")
	}
	profileClient, ok := s.oauthClient.(OpenAIPersonaOAuthClient)
	if !ok {
		return nil, infraerrors.InternalServer("OPENAI_PERSONA_OAUTH_UNSUPPORTED", "profile-aware OpenAI OAuth client is unavailable")
	}
	tokenResp, err := profileClient.ExchangeCodeWithProfile(ctx, input.Code, session.CodeVerifier, session.RedirectURI, session.ProxyURL, session.ClientID, openAIPersonaOAuthProfile(profile))
	if err != nil {
		return nil, err
	}
	s.sessionStore.Delete(input.SessionID)
	tokenInfo := s.openAIPersonaTokenInfo(ctx, tokenResp, session.ClientID, session.ProxyURL)
	expectedAccountID := strings.TrimSpace(session.ExpectedChatGPTAccountID)
	actualAccountID := strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	if expectedAccountID == "" || actualAccountID == "" || !strings.EqualFold(expectedAccountID, actualAccountID) {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_ACCOUNT_MISMATCH", "Persona authorization belongs to a different ChatGPT account")
	}
	return &OpenAIPersonaOAuthExchangeResult{
		TokenInfo: tokenInfo, AccountID: accountID, AccountPersonaID: accountPersonaID,
		PersonaID: profile.ID, ProfileVersion: profile.EffectiveVersion(),
		PersonaGeneration: session.PersonaGeneration, PersonaRowVersion: session.PersonaRowVersion,
		CredentialChainID: session.CredentialChainID, InstallationID: session.InstallationID,
	}, nil
}

func (s *OpenAIOAuthService) PersistAccountPersonaAuthorization(ctx context.Context, account *Account, result *OpenAIPersonaOAuthExchangeResult) (*OpenAIAccountPersona, error) {
	return s.persistAccountPersonaAuthorization(ctx, account, result, false)
}

// PersistPrimaryAccountPersonaAuthorization 是 position 0 唯一的交互式重授权写入口。
// 它沿用 Persona generation/Session epoch 轮换，旧 Thread 仍按旧 scope 在宽限期内排空。
func (s *OpenAIOAuthService) PersistPrimaryAccountPersonaAuthorization(ctx context.Context, account *Account, result *OpenAIPersonaOAuthExchangeResult) (*OpenAIAccountPersona, error) {
	return s.persistAccountPersonaAuthorization(ctx, account, result, true)
}

func (s *OpenAIOAuthService) persistAccountPersonaAuthorization(ctx context.Context, account *Account, result *OpenAIPersonaOAuthExchangeResult, requirePrimary bool) (*OpenAIAccountPersona, error) {
	if account == nil || result == nil || result.TokenInfo == nil || result.AccountID != account.ID || result.AccountPersonaID <= 0 {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_TARGET_MISMATCH", "OAuth result does not belong to this AccountPersona")
	}
	persona, err := s.accountPersonaRepo.GetAccountPersona(ctx, account.ID, result.AccountPersonaID)
	if err != nil {
		return nil, openAIAccountPersonaAPIError(err)
	}
	if requirePrimary != persona.IsDefaultProtected() {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_TARGET_MISMATCH", "OAuth result does not match the requested authorization path")
	}
	if strings.TrimSpace(result.TokenInfo.AccessToken) == "" || strings.TrimSpace(result.TokenInfo.RefreshToken) == "" {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_TOKEN_INCOMPLETE", "Persona authorization did not return a complete token chain")
	}
	expectedAccountID := strings.TrimSpace(account.GetChatGPTAccountID())
	actualAccountID := strings.TrimSpace(result.TokenInfo.ChatGPTAccountID)
	if expectedAccountID == "" || actualAccountID == "" || !strings.EqualFold(expectedAccountID, actualAccountID) {
		return nil, infraerrors.BadRequest("OPENAI_PERSONA_ACCOUNT_MISMATCH", "Persona authorization must use the account primary identity")
	}
	payload, err := s.encryptPersonaCredential(result.TokenInfo)
	if err != nil {
		return nil, err
	}
	upstreamSessionID, err := openai.GenerateSessionID()
	if err != nil {
		return nil, err
	}
	policy := defaultCodexFingerprintEpochPolicy()
	if s.settingService != nil {
		policy = s.settingService.GetCodexFingerprintEpochPolicy(ctx)
	}
	authorization := OpenAIAccountPersonaAuthorization{
		AccountID: account.ID, AccountPersonaID: result.AccountPersonaID,
		ExpectedRowVersion: result.PersonaRowVersion, PersonaGeneration: result.PersonaGeneration,
		CredentialChainID: result.CredentialChainID, EncryptedPayload: payload,
		ChatGPTAccountID: actualAccountID, OAuthClientID: result.TokenInfo.ClientID,
		InstallationID: result.InstallationID, UpstreamSessionID: upstreamSessionID,
		OldSessionExpiresAt: time.Now().Add(time.Duration(policy.OldEpochGraceHours) * time.Hour),
	}
	if requirePrimary {
		return s.accountPersonaRepo.ReauthorizePrimaryAccountPersona(ctx, authorization)
	}
	return s.accountPersonaRepo.AuthorizeAccountPersona(ctx, authorization)
}

func (s *OpenAIOAuthService) RevokeAccountPersonaAuthorization(ctx context.Context, accountID, accountPersonaID, expectedRowVersion int64) error {
	if s == nil || s.accountPersonaRepo == nil {
		return ErrOpenAIPersonaCredentialStoreUnavailable
	}
	chainIDs, err := s.accountPersonaRepo.RevokeAccountPersonaAuthorization(ctx, accountID, accountPersonaID, expectedRowVersion)
	if err != nil {
		return openAIAccountPersonaAPIError(err)
	}
	for _, chainID := range chainIDs {
		if s.personaTokenCache != nil {
			_ = s.personaTokenCache.DeleteAccessToken(ctx, OpenAITokenCacheKeyForAccountPersona(accountPersonaID, chainID))
		}
		if s.personaTransportInvalidator != nil {
			s.personaTransportInvalidator.InvalidateOpenAIAccountPersonaCredentialTransport(accountID, accountPersonaID, chainID)
		}
	}
	return nil
}

// RefreshAccountPersonaCredential 是动态 Persona 的唯一刷新入口，锁和 CAS 均绑定稳定 Persona ID。
func (s *OpenAIOAuthService) RefreshAccountPersonaCredential(ctx context.Context, account *Account, accountPersonaID int64) (*OpenAITokenInfo, error) {
	if account == nil || !account.IsOpenAIOAuth() || s == nil || s.accountPersonaRepo == nil || s.personaTokenCache == nil {
		return nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	persona, err := s.accountPersonaRepo.GetAccountPersona(ctx, account.ID, accountPersonaID)
	if err != nil {
		return nil, openAIAccountPersonaAPIError(err)
	}
	if persona.CurrentCredentialChainID == "" {
		return nil, ErrOpenAIPersonaCredentialChainNotReady
	}
	cacheKey := OpenAITokenCacheKeyForAccountPersona(persona.ID, persona.CurrentCredentialChainID)
	lockKey := cacheKey + ":refresh"
	locked, err := s.personaTokenCache.AcquireRefreshLock(ctx, lockKey, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if !locked {
		return s.waitForAccountPersonaCredentialRefresh(ctx, persona)
	}
	defer func() { _ = s.personaTokenCache.ReleaseRefreshLock(context.Background(), lockKey) }()
	record, err := s.accountPersonaRepo.GetAccountPersonaCredential(ctx, persona.ID, persona.CurrentCredentialChainID)
	if err != nil {
		return nil, err
	}
	if record.State == "refreshing" {
		return s.waitForAccountPersonaCredentialRefresh(ctx, persona)
	}
	if record.State != "ready" {
		return nil, ErrOpenAIPersonaCredentialChainNotReady
	}
	current, err := s.decryptPersonaCredential(record)
	if err != nil {
		return nil, err
	}
	if current.ExpiresAt > 0 && time.Until(time.Unix(current.ExpiresAt, 0)) > openAITokenRefreshSkew && current.AccessToken != "" {
		return current, nil
	}
	refreshToken := strings.TrimSpace(current.RefreshToken)
	if refreshToken == "" {
		return nil, ErrOpenAIPersonaCredentialChainExpired
	}
	profile, ok := NewDefaultSessionPersonaRegistry().Get(string(persona.ProfileID))
	profileClient, clientOK := s.oauthClient.(OpenAIPersonaOAuthClient)
	if !ok || !profile.Valid() || !clientOK {
		return nil, ErrOpenAIPersonaCredentialRefreshUnsupported
	}
	proxyURL, err := s.openAIAccountPersonaProxyURL(ctx, account, persona)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(current.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(profile.OAuthClientID)
	}
	claimed, err := s.accountPersonaRepo.ClaimAccountPersonaCredentialRefresh(ctx, persona.ID, persona.CurrentCredentialChainID, record.TokenVersion)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return s.waitForAccountPersonaCredentialRefresh(ctx, persona)
	}
	tokenResp, err := profileClient.RefreshTokenWithProfile(ctx, refreshToken, proxyURL, clientID, openAIPersonaOAuthProfile(profile))
	if err != nil {
		_ = s.accountPersonaRepo.MarkAccountPersonaCredentialInvalid(context.Background(), persona.ID, persona.CurrentCredentialChainID, record.TokenVersion, err.Error())
		return nil, err
	}
	info := s.openAIPersonaTokenInfo(ctx, tokenResp, clientID, proxyURL)
	if info.RefreshToken == "" {
		info.RefreshToken = refreshToken
	}
	if info.ChatGPTAccountID == "" {
		info.ChatGPTAccountID = record.ChatGPTAccountID
	}
	if !strings.EqualFold(strings.TrimSpace(info.ChatGPTAccountID), strings.TrimSpace(record.ChatGPTAccountID)) ||
		strings.TrimSpace(info.AccessToken) == "" || strings.TrimSpace(info.RefreshToken) == "" {
		_ = s.accountPersonaRepo.MarkAccountPersonaCredentialInvalid(context.Background(), persona.ID, persona.CurrentCredentialChainID, record.TokenVersion, ErrOpenAIPersonaCredentialChainMismatch.Error())
		return nil, ErrOpenAIPersonaCredentialChainMismatch
	}
	payload, err := s.encryptPersonaCredential(info)
	if err != nil {
		return nil, err
	}
	updated, err := s.accountPersonaRepo.CompareAndSwapAccountPersonaToken(ctx, OpenAIAccountPersonaCredentialUpdate{
		AccountPersonaID: persona.ID, CredentialChainID: persona.CurrentCredentialChainID,
		EncryptedPayload: payload, ChatGPTAccountID: record.ChatGPTAccountID,
		InstallationID: record.InstallationID,
	}, record.TokenVersion)
	if err != nil {
		return nil, err
	}
	if !updated {
		return s.waitForAccountPersonaCredentialRefresh(ctx, persona)
	}
	_ = s.personaTokenCache.DeleteAccessToken(ctx, cacheKey)
	cachePersonaToken(ctx, s.personaTokenCache, cacheKey, info)
	return info, nil
}

func (s *OpenAIOAuthService) waitForAccountPersonaCredentialRefresh(ctx context.Context, persona *OpenAIAccountPersona) (*OpenAITokenInfo, error) {
	deadline := time.NewTimer(openAIPersonaRefreshWaitTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(openAIPersonaRefreshPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, ErrOpenAIPersonaCredentialCASConflict
		case <-ticker.C:
			record, err := s.accountPersonaRepo.GetAccountPersonaCredential(ctx, persona.ID, persona.CurrentCredentialChainID)
			if err != nil {
				return nil, err
			}
			if record.State == "ready" {
				return s.decryptPersonaCredential(record)
			}
			if record.State != "refreshing" {
				return nil, ErrOpenAIPersonaCredentialChainNotReady
			}
		}
	}
}

func openAIAccountPersonaAPIError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrOpenAIDefaultPersonaProtected):
		return infraerrors.Conflict("DEFAULT_PERSONA_PROTECTED", "default Persona is protected")
	case errors.Is(err, ErrOpenAIAccountPersonaCASConflict):
		return infraerrors.Conflict("OPENAI_PERSONA_VERSION_CONFLICT", "AccountPersona has changed")
	case errors.Is(err, ErrOpenAIAccountPersonaNotFound):
		return infraerrors.NotFound("OPENAI_PERSONA_NOT_FOUND", "AccountPersona not found")
	default:
		return err
	}
}

func (s *OpenAIOAuthService) openAIAccountPersonaProxyURL(ctx context.Context, account *Account, persona *OpenAIAccountPersona) (string, error) {
	if persona == nil {
		return "", ErrOpenAIAccountPersonaNotFound
	}
	proxyID := persona.ProxyID
	if proxyID == nil && account != nil {
		proxyID = account.ProxyID
	}
	if proxyID == nil {
		return "", nil
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *proxyID)
	if err != nil || proxy == nil {
		return "", infraerrors.BadRequest("OPENAI_OAUTH_PROXY_NOT_FOUND", "Persona proxy is unavailable")
	}
	return proxy.URL(), nil
}

func openAIPersonaOAuthProfile(persona SessionPersona) OpenAIOAuthClientProfile {
	return OpenAIOAuthClientProfile{
		UserAgent:           persona.BuildUserAgent("", "", ""),
		Originator:          strings.TrimSpace(persona.Originator),
		IncludeRefreshScope: persona.ID != SessionPersonaOpenCode,
	}
}

func (s *OpenAIOAuthService) openAIPersonaTokenInfo(ctx context.Context, tokenResp *openai.TokenResponse, clientID, proxyURL string) *OpenAITokenInfo {
	expiresIn := tokenResp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	info := &OpenAITokenInfo{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		IDToken:      tokenResp.IDToken,
		ExpiresIn:    expiresIn,
		ExpiresAt:    time.Now().Unix() + expiresIn,
		ClientID:     strings.TrimSpace(clientID),
	}
	if tokenResp.IDToken != "" {
		if claims, err := openai.ParseIDToken(tokenResp.IDToken); err == nil {
			userInfo := claims.GetUserInfo()
			info.Email = userInfo.Email
			info.ChatGPTAccountID = userInfo.ChatGPTAccountID
			info.ChatGPTUserID = userInfo.ChatGPTUserID
			info.OrganizationID = userInfo.OrganizationID
			info.PlanType = userInfo.PlanType
		} else {
			slog.Warn("openai_persona_oauth_id_token_parse_failed", "error", err)
		}
	}
	s.enrichTokenInfo(ctx, info, proxyURL)
	return info
}

func (s *OpenAIOAuthService) openAIAccountProxyURL(ctx context.Context, account *Account) (string, error) {
	if account == nil || account.ProxyID == nil || s.proxyRepo == nil {
		return "", nil
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
	if err != nil {
		return "", infraerrors.Newf(http.StatusBadRequest, "OPENAI_OAUTH_PROXY_NOT_FOUND", "proxy not found: %v", err)
	}
	if proxy == nil {
		return "", nil
	}
	return proxy.URL(), nil
}

func cachePersonaToken(ctx context.Context, cache OpenAITokenCache, cacheKey string, info *OpenAITokenInfo) {
	if cache == nil || info == nil || strings.TrimSpace(info.AccessToken) == "" {
		return
	}
	ttl := 30 * time.Minute
	if info.ExpiresAt > 0 {
		until := time.Until(time.Unix(info.ExpiresAt, 0))
		if until <= 0 {
			return
		}
		if until > openAITokenCacheSkew {
			ttl = until - openAITokenCacheSkew
		} else {
			ttl = until
		}
	}
	if err := cache.SetAccessToken(ctx, cacheKey, info.AccessToken, ttl); err != nil {
		slog.Warn("openai_persona_token_cache_set_failed", "cache_key", cacheKey, "error", err)
	}
}
