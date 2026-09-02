package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
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

// OpenAIPersonaOAuthStatus is the token-free operator view for the two fixed
// Persona slots. Secrets never cross the admin status boundary.
type OpenAIPersonaOAuthStatus struct {
	MappingMode string                         `json:"mapping_mode"`
	Slots       []OpenAIPersonaOAuthSlotStatus `json:"slots"`
}

type OpenAIPersonaOAuthSlotStatus struct {
	SlotID             int                     `json:"slot_id"`
	PersonaID          SessionPersonaID        `json:"persona"`
	State              SessionPersonaSlotState `json:"state"`
	Enabled            bool                    `json:"enabled"`
	Authorized         bool                    `json:"authorized"`
	CredentialChainID  string                  `json:"credential_chain_id,omitempty"`
	SlotGeneration     int64                   `json:"slot_generation"`
	SlotSetGeneration  int64                   `json:"slot_set_generation"`
	ChatGPTAccountID   string                  `json:"chatgpt_account_id,omitempty"`
	OAuthClientID      string                  `json:"oauth_client_id,omitempty"`
	InstallationID     string                  `json:"installation_id,omitempty"`
	AccessTokenExpires string                  `json:"access_token_expires_at,omitempty"`
}

// OpenAIPersonaOAuthExchangeResult carries the server-bound target alongside
// token data. The handler persists only this target; it never trusts Persona
// identity supplied by the browser after URL generation.
type OpenAIPersonaOAuthExchangeResult struct {
	TokenInfo         *OpenAITokenInfo
	AccountID         int64
	PersonaID         SessionPersonaID
	SlotID            int
	CredentialChainID string
	InstallationID    string
	SlotGeneration    int64
	SlotSetGeneration int64
}

func openAIPersonaForSlot(slotID int) (SessionPersona, error) {
	var personaID SessionPersonaID
	switch slotID {
	case 0:
		personaID = SessionPersonaCodexCLIStrict
	case 1:
		personaID = SessionPersonaOpenCode
	default:
		return SessionPersona{}, infraerrors.Newf(http.StatusBadRequest, "OPENAI_PERSONA_SLOT_INVALID", "unsupported OpenAI Persona slot %d", slotID)
	}
	persona, ok := NewDefaultSessionPersonaRegistry().Get(string(personaID))
	if !ok || !persona.Valid() {
		return SessionPersona{}, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_PERSONA_PROFILE_MISSING", "OpenAI Persona profile %q is unavailable", personaID)
	}
	return persona, nil
}

func openAIPersonaOAuthProfile(persona SessionPersona) OpenAIOAuthClientProfile {
	return OpenAIOAuthClientProfile{
		UserAgent:           persona.BuildUserAgent("", "", ""),
		Originator:          strings.TrimSpace(persona.Originator),
		IncludeRefreshScope: persona.ID != SessionPersonaOpenCode,
	}
}

// GetPersonaOAuthStatus returns the fixed slot topology and current
// authorization readiness without exposing token material.
func (s *OpenAIOAuthService) GetPersonaOAuthStatus(ctx context.Context, account *Account) (*OpenAIPersonaOAuthStatus, error) {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_ACCOUNT_INVALID", "account is not an OpenAI OAuth account")
	}
	if s.personaCredentialRepo == nil {
		return nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	mode := "legacy_v2"
	if account.IsOpenAIPersonaMappingEnabled() {
		mode = "persona_v3"
	}
	storedSlots, err := s.personaCredentialRepo.ListSlots(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	storedByID := make(map[int]OpenAIPersonaSlotRecord, len(storedSlots))
	for _, slot := range storedSlots {
		storedByID[slot.SlotID] = slot
	}
	result := &OpenAIPersonaOAuthStatus{MappingMode: mode, Slots: make([]OpenAIPersonaOAuthSlotStatus, 0, DefaultSessionPersonaSlotCount)}
	for slotID := 0; slotID < DefaultSessionPersonaSlotCount; slotID++ {
		persona, err := openAIPersonaForSlot(slotID)
		if err != nil {
			return nil, err
		}
		status := OpenAIPersonaOAuthSlotStatus{
			SlotID:            slotID,
			PersonaID:         persona.ID,
			State:             account.GetOpenAIPersonaSlotState(slotID),
			Enabled:           account.GetOpenAIPersonaSlotEnabled(slotID),
			Authorized:        false,
			SlotGeneration:    account.GetOpenAIPersonaSlotGeneration(slotID),
			SlotSetGeneration: account.GetOpenAIPersonaSlotSetGeneration(),
			ChatGPTAccountID:  strings.TrimSpace(account.GetChatGPTAccountID()),
			OAuthClientID:     strings.TrimSpace(persona.OAuthClientID),
		}
		if stored, ok := storedByID[slotID]; ok {
			status.State = stored.State
			status.Enabled = stored.Enabled
			status.CredentialChainID = stored.CredentialChainID
			status.SlotGeneration = stored.SlotGeneration
			status.SlotSetGeneration = stored.SlotSetGeneration
			status.Authorized = stored.Authorized && stored.CredentialChainID != ""
			if status.Authorized {
				record, loadErr := s.personaCredentialRepo.GetCredential(ctx, account.ID, persona.ID, slotID, stored.CredentialChainID)
				if loadErr != nil {
					return nil, loadErr
				}
				status.Authorized = record.State == "ready" || record.State == "refreshing"
				status.ChatGPTAccountID = strings.TrimSpace(record.ChatGPTAccountID)
				status.InstallationID = strings.TrimSpace(record.InstallationID)
				if status.Authorized {
					info, decryptErr := s.decryptPersonaCredential(record)
					if decryptErr != nil {
						status.Authorized = false
					} else {
						status.OAuthClientID = strings.TrimSpace(info.ClientID)
						if info.ExpiresAt > 0 {
							status.AccessTokenExpires = time.Unix(info.ExpiresAt, 0).UTC().Format(time.RFC3339)
						}
					}
				}
			}
		}
		result.Slots = append(result.Slots, status)
	}
	return result, nil
}

// GeneratePersonaAuthURL creates a server-bound OAuth attempt. Proxy, client
// ID, Persona, slot, generations and the future chain ID all come from the
// authoritative account and registry rather than browser-controlled fields.
func (s *OpenAIOAuthService) GeneratePersonaAuthURL(ctx context.Context, account *Account, slotID int) (*OpenAIAuthURLResult, error) {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_ACCOUNT_INVALID", "account is not an OpenAI OAuth account")
	}
	persona, err := openAIPersonaForSlot(slotID)
	if err != nil {
		return nil, err
	}
	state, err := openai.GenerateState()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_OAUTH_STATE_FAILED", "failed to generate state: %v", err)
	}
	verifier, err := openai.GenerateCodeVerifier()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_OAUTH_VERIFIER_FAILED", "failed to generate code verifier: %v", err)
	}
	sessionID, err := openai.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_OAUTH_SESSION_FAILED", "failed to generate session ID: %v", err)
	}
	chainNonce, err := openai.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_PERSONA_CHAIN_FAILED", "failed to generate credential chain ID: %v", err)
	}
	installationNonce, err := openai.GenerateSessionID()
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "OPENAI_PERSONA_INSTALLATION_FAILED", "failed to generate installation ID: %v", err)
	}
	proxyURL, err := s.openAIAccountProxyURL(ctx, account)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(persona.OAuthClientID)
	if clientID == "" {
		clientID = openai.ClientID
	}
	profile := openAIPersonaOAuthProfile(persona)
	chainID := string(persona.ID) + "-" + chainNonce
	s.sessionStore.Set(sessionID, &openai.OAuthSession{
		State:                    state,
		CodeVerifier:             verifier,
		ClientID:                 clientID,
		ProxyURL:                 proxyURL,
		RedirectURI:              openai.DefaultRedirectURI,
		CreatedAt:                time.Now(),
		AccountID:                account.ID,
		PersonaID:                string(persona.ID),
		SlotID:                   slotID,
		CredentialChainID:        chainID,
		InstallationID:           string(persona.ID) + "-" + installationNonce,
		SlotGeneration:           account.GetOpenAIPersonaSlotGeneration(slotID),
		SlotSetGeneration:        account.GetOpenAIPersonaSlotSetGeneration(),
		ExpectedChatGPTAccountID: strings.TrimSpace(account.GetChatGPTAccountID()),
		UserAgent:                profile.UserAgent,
		Originator:               profile.Originator,
	})
	originator := ""
	if persona.ID == SessionPersonaOpenCode {
		originator = profile.Originator
	}
	return &OpenAIAuthURLResult{
		AuthURL:   openai.BuildAuthorizationURLWithClient(state, openai.GenerateCodeChallenge(verifier), openai.DefaultRedirectURI, clientID, true, originator),
		SessionID: sessionID,
	}, nil
}

// ExchangePersonaCode exchanges a code only for the target fixed in the
// server-side session and rejects cross-account authorization.
func (s *OpenAIOAuthService) ExchangePersonaCode(ctx context.Context, accountID int64, slotID int, input *OpenAIExchangeCodeInput) (*OpenAIPersonaOAuthExchangeResult, error) {
	if input == nil {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_REQUEST_INVALID", "OAuth exchange input is required")
	}
	session, ok := s.sessionStore.Get(input.SessionID)
	if !ok || session.AccountID == 0 || session.PersonaID == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_SESSION_NOT_FOUND", "Persona OAuth session not found or expired")
	}
	if accountID != session.AccountID || slotID != session.SlotID {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_TARGET_MISMATCH", "OAuth session target does not match this account slot")
	}
	if input.State == "" || subtle.ConstantTimeCompare([]byte(input.State), []byte(session.State)) != 1 {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_OAUTH_INVALID_STATE", "invalid oauth state")
	}
	personaID, ok := ParseSessionPersonaID(session.PersonaID)
	if !ok {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_PROFILE_INVALID", "OAuth session Persona is invalid")
	}
	persona, err := openAIPersonaForSlot(slotID)
	if err != nil || persona.ID != personaID {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_PROFILE_INVALID", "OAuth session Persona does not match its slot")
	}
	profileClient, ok := s.oauthClient.(OpenAIPersonaOAuthClient)
	if !ok {
		return nil, infraerrors.New(http.StatusInternalServerError, "OPENAI_PERSONA_OAUTH_UNSUPPORTED", "profile-aware OpenAI OAuth client is unavailable")
	}
	tokenResp, err := profileClient.ExchangeCodeWithProfile(ctx, input.Code, session.CodeVerifier, session.RedirectURI, session.ProxyURL, session.ClientID, openAIPersonaOAuthProfile(persona))
	if err != nil {
		return nil, err
	}
	// The authorization code is single-use upstream. Consume the local attempt
	// immediately after a successful exchange, including account-mismatch
	// failures, so a stale target cannot be retried or repurposed.
	s.sessionStore.Delete(input.SessionID)
	tokenInfo := s.openAIPersonaTokenInfo(ctx, tokenResp, session.ClientID, session.ProxyURL)
	expectedAccountID := strings.TrimSpace(session.ExpectedChatGPTAccountID)
	actualAccountID := strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	if slotID != 0 && (expectedAccountID == "" || actualAccountID == "") {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_ACCOUNT_UNVERIFIABLE", "OpenCode authorization must expose the same ChatGPT account ID as slot 0")
	}
	if expectedAccountID != "" && !strings.EqualFold(expectedAccountID, actualAccountID) {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_ACCOUNT_MISMATCH", "OAuth authorization belongs to a different ChatGPT account")
	}
	return &OpenAIPersonaOAuthExchangeResult{
		TokenInfo:         tokenInfo,
		AccountID:         session.AccountID,
		PersonaID:         persona.ID,
		SlotID:            session.SlotID,
		CredentialChainID: session.CredentialChainID,
		InstallationID:    session.InstallationID,
		SlotGeneration:    session.SlotGeneration,
		SlotSetGeneration: session.SlotSetGeneration,
	}, nil
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

// PersistPersonaAuthorization commits a new independent OAuth chain and moves
// only the target slot's active pointer. No token is written to accounts JSON.
func (s *OpenAIOAuthService) PersistPersonaAuthorization(ctx context.Context, account *Account, result *OpenAIPersonaOAuthExchangeResult) error {
	if account == nil || result == nil || result.TokenInfo == nil || result.AccountID != account.ID {
		return infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_TARGET_MISMATCH", "OAuth result does not belong to this account")
	}
	if s.personaCredentialRepo == nil {
		return ErrOpenAIPersonaCredentialStoreUnavailable
	}
	persona, err := openAIPersonaForSlot(result.SlotID)
	if err != nil || persona.ID != result.PersonaID {
		return infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_TARGET_MISMATCH", "OAuth result Persona does not match its slot")
	}
	if result.SlotGeneration != account.GetOpenAIPersonaSlotGeneration(result.SlotID) || result.SlotSetGeneration != account.GetOpenAIPersonaSlotSetGeneration() {
		return infraerrors.New(http.StatusConflict, "OPENAI_PERSONA_GENERATION_CHANGED", "slot lifecycle changed during OAuth; generate a new authorization URL")
	}
	expectedAccountID := strings.TrimSpace(account.GetChatGPTAccountID())
	actualAccountID := strings.TrimSpace(result.TokenInfo.ChatGPTAccountID)
	if expectedAccountID == "" || actualAccountID == "" || !strings.EqualFold(expectedAccountID, actualAccountID) {
		return infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_ACCOUNT_MISMATCH", "Persona authorization must use the OpenAI account already assigned to this account")
	}
	if strings.TrimSpace(result.TokenInfo.AccessToken) == "" || strings.TrimSpace(result.TokenInfo.RefreshToken) == "" {
		return infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_TOKEN_INCOMPLETE", "Persona authorization did not return a complete token chain")
	}
	payload, err := s.encryptPersonaCredential(result.TokenInfo)
	if err != nil {
		return fmt.Errorf("encrypt OpenAI Persona credential: %w", err)
	}
	if err := s.personaCredentialRepo.Authorize(ctx, OpenAIPersonaCredentialWrite{
		AccountID: account.ID, PersonaID: result.PersonaID, SlotID: result.SlotID,
		CredentialChainID: result.CredentialChainID, EncryptedPayload: payload,
		ChatGPTAccountID: actualAccountID, InstallationID: result.InstallationID,
		SlotGeneration: result.SlotGeneration, SlotSetGeneration: result.SlotSetGeneration,
	}); err != nil {
		return err
	}
	if s.personaTokenCache != nil {
		binding := SessionPersonaSlotBinding{
			AccountID: account.ID, PersonaID: result.PersonaID, SlotID: result.SlotID,
			CredentialChainID: result.CredentialChainID,
		}
		_ = s.personaTokenCache.DeleteAccessToken(ctx, OpenAITokenCacheKeyForBinding(account, binding))
	}
	return nil
}

func copyOpenAICredentialContainer(raw any) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		return shallowCopyMap(value)
	case map[string]string:
		out := make(map[string]any, len(value))
		for key, item := range value {
			out[key] = item
		}
		return out
	case string:
		var out map[string]any
		if json.Unmarshal([]byte(value), &out) == nil && out != nil {
			return out
		}
	case json.RawMessage:
		var out map[string]any
		if json.Unmarshal(value, &out) == nil && out != nil {
			return out
		}
	}
	return map[string]any{}
}

// RefreshPersonaCredential is the only refresh entry point for Persona v3.
// Runtime, manual, and future background refreshers therefore share the same
// chain-scoped lock and token_version compare-and-swap boundary.
func (s *OpenAIOAuthService) RefreshPersonaCredential(ctx context.Context, account *Account, binding SessionPersonaSlotBinding) (*OpenAITokenInfo, error) {
	if account == nil || !account.IsOpenAIOAuth() || binding.AccountID != account.ID {
		return nil, fmt.Errorf("%w: account mismatch", ErrOpenAITokenBindingInvalid)
	}
	persona, err := openAIPersonaForSlot(binding.SlotID)
	if err != nil || persona.ID != binding.PersonaID {
		return nil, fmt.Errorf("%w: persona/slot mismatch", ErrOpenAITokenBindingInvalid)
	}
	if s.personaCredentialRepo == nil || s.personaCredentialEncryptor == nil || s.personaTokenCache == nil {
		return nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	cacheKey := OpenAITokenCacheKeyForBinding(account, binding)
	lockKey := cacheKey + ":refresh"
	locked, lockErr := s.personaTokenCache.AcquireRefreshLock(ctx, lockKey, 30*time.Second)
	if lockErr != nil {
		return nil, fmt.Errorf("acquire OpenAI Persona refresh lock: %w", lockErr)
	}
	if !locked {
		return s.waitForPersonaCredentialRefresh(ctx, account, binding)
	}
	defer func() { _ = s.personaTokenCache.ReleaseRefreshLock(context.Background(), lockKey) }()
	record, err := s.personaCredentialRepo.GetCredential(ctx, account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID)
	if err != nil {
		return nil, err
	}
	if record.State == "refreshing" {
		return s.waitForPersonaCredentialRefresh(ctx, account, binding)
	}
	if record.State != "ready" {
		return nil, ErrOpenAIPersonaCredentialChainNotReady
	}
	if strings.TrimSpace(binding.InstallationID) != "" && strings.TrimSpace(record.InstallationID) != strings.TrimSpace(binding.InstallationID) {
		return nil, fmt.Errorf("%w: installation mismatch", ErrOpenAIPersonaCredentialChainMismatch)
	}
	current, err := s.decryptPersonaCredential(record)
	if err != nil {
		return nil, err
	}
	if current.ExpiresAt > 0 && time.Until(time.Unix(current.ExpiresAt, 0)) > openAITokenRefreshSkew && strings.TrimSpace(current.AccessToken) != "" {
		return current, nil
	}
	refreshToken := strings.TrimSpace(current.RefreshToken)
	if refreshToken == "" {
		return nil, fmt.Errorf("%w: chain=%q has no refresh_token", ErrOpenAIPersonaCredentialChainExpired, binding.CredentialChainID)
	}
	profileClient, ok := s.oauthClient.(OpenAIPersonaOAuthClient)
	if !ok {
		return nil, fmt.Errorf("%w: profile-aware OAuth client unavailable", ErrOpenAIPersonaCredentialRefreshUnsupported)
	}
	proxyURL, err := s.openAIAccountProxyURL(ctx, account)
	if err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(current.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(persona.OAuthClientID)
	}
	claimed, err := s.personaCredentialRepo.ClaimRefresh(ctx, account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID, record.TokenVersion)
	if err != nil {
		return nil, fmt.Errorf("claim OpenAI Persona token refresh: %w", err)
	}
	if !claimed {
		return s.waitForPersonaCredentialRefresh(ctx, account, binding)
	}
	tokenResp, err := profileClient.RefreshTokenWithProfile(ctx, refreshToken, proxyURL, clientID, openAIPersonaOAuthProfile(persona))
	if err != nil {
		s.invalidatePersonaCredentialAfterRefresh(ctx, account, binding, record.TokenVersion, err)
		return nil, err
	}
	info := s.openAIPersonaTokenInfo(ctx, tokenResp, clientID, proxyURL)
	if info.RefreshToken == "" {
		info.RefreshToken = refreshToken
	}
	storedAccountID := strings.TrimSpace(record.ChatGPTAccountID)
	if info.ChatGPTAccountID != "" && storedAccountID != "" && !strings.EqualFold(info.ChatGPTAccountID, storedAccountID) {
		s.invalidatePersonaCredentialAfterRefresh(ctx, account, binding, record.TokenVersion, ErrOpenAIPersonaCredentialChainMismatch)
		return nil, fmt.Errorf("%w: refreshed chain belongs to a different ChatGPT account", ErrOpenAIPersonaCredentialChainMismatch)
	}
	if info.ChatGPTAccountID == "" {
		info.ChatGPTAccountID = storedAccountID
	}
	if strings.TrimSpace(info.AccessToken) == "" || strings.TrimSpace(info.RefreshToken) == "" {
		s.invalidatePersonaCredentialAfterRefresh(ctx, account, binding, record.TokenVersion, ErrOpenAIPersonaCredentialChainNotReady)
		return nil, fmt.Errorf("%w: refresh returned an incomplete token chain", ErrOpenAIPersonaCredentialChainNotReady)
	}
	payload, err := s.encryptPersonaCredential(info)
	if err != nil {
		s.invalidatePersonaCredentialAfterRefresh(ctx, account, binding, record.TokenVersion, err)
		return nil, err
	}
	write := OpenAIPersonaCredentialWrite{
		AccountID: account.ID, PersonaID: binding.PersonaID, SlotID: binding.SlotID,
		CredentialChainID: binding.CredentialChainID, EncryptedPayload: payload,
		ChatGPTAccountID: storedAccountID, InstallationID: record.InstallationID,
		SlotGeneration: binding.SlotGeneration, SlotSetGeneration: binding.SlotSetGeneration,
	}
	updated, err := s.personaCredentialRepo.CompareAndSwapToken(ctx, write, record.TokenVersion)
	if err != nil {
		// The refresh token may already have rotated upstream. Fence the old
		// version if the write did not commit; a committed CAS has already moved
		// the version and therefore cannot be invalidated by this best effort.
		s.invalidatePersonaCredentialAfterRefresh(context.Background(), account, binding, record.TokenVersion, errors.New("refresh persistence result was uncertain"))
		return nil, fmt.Errorf("persist rotated OpenAI Persona token: %w", err)
	}
	if !updated {
		latest, loadErr := s.personaCredentialRepo.GetCredential(ctx, account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID)
		if loadErr != nil {
			return nil, loadErr
		}
		if latest.State != "ready" || latest.TokenVersion <= record.TokenVersion {
			s.invalidatePersonaCredentialAfterRefresh(context.Background(), account, binding, record.TokenVersion, ErrOpenAIPersonaCredentialCASConflict)
			return nil, ErrOpenAIPersonaCredentialCASConflict
		}
		return s.decryptPersonaCredential(latest)
	}
	if s.personaTokenCache != nil {
		_ = s.personaTokenCache.DeleteAccessToken(ctx, cacheKey)
		cachePersonaToken(ctx, s.personaTokenCache, cacheKey, info)
	}
	return info, nil
}

func (s *OpenAIOAuthService) waitForPersonaCredentialRefresh(ctx context.Context, account *Account, binding SessionPersonaSlotBinding) (*OpenAITokenInfo, error) {
	baseline, err := s.personaCredentialRepo.GetCredential(ctx, account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID)
	if err != nil {
		return nil, err
	}
	if baseline.State == "ready" {
		info, decryptErr := s.decryptPersonaCredential(baseline)
		if decryptErr != nil {
			return nil, decryptErr
		}
		if strings.TrimSpace(info.AccessToken) != "" && info.ExpiresAt > 0 && time.Until(time.Unix(info.ExpiresAt, 0)) > openAITokenRefreshSkew {
			return info, nil
		}
	}
	ticker := time.NewTicker(openAIPersonaRefreshPollInterval)
	defer ticker.Stop()
	timeout := time.NewTimer(openAIPersonaRefreshWaitTimeout)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timeout.C:
			return nil, ErrOpenAIPersonaCredentialCASConflict
		case <-ticker.C:
		}
		latest, loadErr := s.personaCredentialRepo.GetCredential(ctx, account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID)
		if loadErr != nil {
			return nil, loadErr
		}
		if latest.State == "ready" && latest.TokenVersion > baseline.TokenVersion {
			return s.decryptPersonaCredential(latest)
		}
		if latest.State != "ready" && latest.State != "refreshing" {
			return nil, ErrOpenAIPersonaCredentialChainNotReady
		}
	}
}

func (s *OpenAIOAuthService) invalidatePersonaCredentialAfterRefresh(ctx context.Context, account *Account, binding SessionPersonaSlotBinding, tokenVersion int64, cause error) {
	if s == nil || s.personaCredentialRepo == nil || account == nil {
		return
	}
	reason := "refresh result is uncertain"
	if cause != nil {
		reason = cause.Error()
	}
	_ = s.personaCredentialRepo.MarkInvalidIfVersion(ctx, account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID, tokenVersion, reason)
	if s.personaTokenCache != nil {
		_ = s.personaTokenCache.DeleteAccessToken(ctx, OpenAITokenCacheKeyForBinding(account, binding))
	}
	if s.personaTransportInvalidator != nil {
		s.personaTransportInvalidator.InvalidateOpenAIPersonaTransport(account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID)
	}
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

// RevokePersonaAuthorization locally destroys every saved token chain for the
// fixed Persona slot while retaining immutable chain metadata for audit.
func (s *OpenAIOAuthService) RevokePersonaAuthorization(ctx context.Context, account *Account, slotID int) error {
	if account == nil || !account.IsOpenAIOAuth() {
		return infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_ACCOUNT_INVALID", "account is not an OpenAI OAuth account")
	}
	persona, err := openAIPersonaForSlot(slotID)
	if err != nil {
		return err
	}
	if s.personaCredentialRepo == nil {
		return ErrOpenAIPersonaCredentialStoreUnavailable
	}
	chainIDs, err := s.personaCredentialRepo.RevokeSlotAuthorization(ctx, account.ID, persona.ID, slotID)
	if err != nil {
		return err
	}
	for _, chainID := range chainIDs {
		binding := SessionPersonaSlotBinding{AccountID: account.ID, PersonaID: persona.ID, SlotID: slotID, CredentialChainID: chainID}
		if s.personaTokenCache != nil {
			_ = s.personaTokenCache.DeleteAccessToken(ctx, OpenAITokenCacheKeyForBinding(account, binding))
		}
		if s.personaTransportInvalidator != nil {
			s.personaTransportInvalidator.InvalidateOpenAIPersonaTransport(account.ID, persona.ID, slotID, chainID)
		}
	}
	return nil
}
