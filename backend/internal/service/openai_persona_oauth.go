package service

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
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
func (s *OpenAIOAuthService) GetPersonaOAuthStatus(account *Account) (*OpenAIPersonaOAuthStatus, error) {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_ACCOUNT_INVALID", "account is not an OpenAI OAuth account")
	}
	mode := "legacy_v2"
	if account.IsOpenAIPersonaMappingEnabled() {
		mode = "persona_v3"
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
			Authorized:        account.HasOpenAIPersonaCredential(persona.ID, slotID),
			CredentialChainID: account.GetOpenAIPersonaCredentialChainID(persona.ID, slotID),
			SlotGeneration:    account.GetOpenAIPersonaSlotGeneration(slotID),
			SlotSetGeneration: account.GetOpenAIPersonaSlotSetGeneration(),
			ChatGPTAccountID:  strings.TrimSpace(account.GetChatGPTAccountID()),
			OAuthClientID:     strings.TrimSpace(persona.OAuthClientID),
		}
		if chain := account.findPersonaCredential(persona.ID, slotID); chain != nil {
			status.InstallationID = strings.TrimSpace(openAIMapString(chain, "installation_id"))
			if expiresAt := openAIPersonaCredentialExpiry(chain); expiresAt != nil {
				status.AccessTokenExpires = expiresAt.UTC().Format(time.RFC3339)
			}
		} else if slotID == 0 {
			if expiresAt := account.GetCredentialAsTime("expires_at"); expiresAt != nil {
				status.AccessTokenExpires = expiresAt.UTC().Format(time.RFC3339)
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
	chainID := "legacy-codex"
	if slotID != 0 {
		chainID = string(persona.ID) + "-" + chainNonce
	}
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

// BuildPersonaOAuthCredentials persists a newly authorized chain while moving
// only the slot's active pointer. Previous chains remain readable for existing
// Thread bindings and are marked admission-draining rather than deleted.
func (s *OpenAIOAuthService) BuildPersonaOAuthCredentials(account *Account, result *OpenAIPersonaOAuthExchangeResult) (map[string]any, error) {
	if account == nil || result == nil || result.TokenInfo == nil || result.AccountID != account.ID {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_TARGET_MISMATCH", "OAuth result does not belong to this account")
	}
	if result.SlotID == 0 {
		next := shallowCopyMap(account.Credentials)
		for key, value := range s.BuildAccountCredentials(result.TokenInfo) {
			next[key] = value
		}
		return next, nil
	}
	if result.SlotGeneration != account.GetOpenAIPersonaSlotGeneration(result.SlotID) || result.SlotSetGeneration != account.GetOpenAIPersonaSlotSetGeneration() {
		return nil, infraerrors.New(http.StatusConflict, "OPENAI_PERSONA_GENERATION_CHANGED", "slot lifecycle changed during OAuth; generate a new authorization URL")
	}
	if expected := strings.TrimSpace(account.GetChatGPTAccountID()); expected == "" || !strings.EqualFold(expected, strings.TrimSpace(result.TokenInfo.ChatGPTAccountID)) {
		return nil, infraerrors.New(http.StatusBadRequest, "OPENAI_PERSONA_ACCOUNT_MISMATCH", "OpenCode authorization must use the account already assigned to slot 0")
	}
	next := shallowCopyMap(account.Credentials)
	chains := copyOpenAICredentialContainer(next[openAIOAuthCredentialChainsKey])
	active := copyOpenAICredentialContainer(next[openAIPersonaActiveChainsKey])
	activeKey := strconv.Itoa(result.SlotID)
	if previousID := strings.TrimSpace(openAIMapString(active, activeKey)); previousID != "" && previousID != result.CredentialChainID {
		if previous, ok := chains[previousID].(map[string]any); ok {
			previousCopy := shallowCopyMap(previous)
			previousCopy["admission_state"] = "draining"
			previousCopy["draining_since"] = time.Now().UTC().Format(time.RFC3339)
			chains[previousID] = previousCopy
		}
	}
	chain := map[string]any{
		"account_id":          account.ID,
		"persona":             string(result.PersonaID),
		"slot_id":             result.SlotID,
		"credential_chain_id": result.CredentialChainID,
		"chatgpt_account_id":  strings.TrimSpace(result.TokenInfo.ChatGPTAccountID),
		"installation_id":     result.InstallationID,
		"slot_generation":     result.SlotGeneration,
		"slot_set_generation": result.SlotSetGeneration,
		"ready":               true,
		"state":               "ready",
		"admission_state":     "active",
		"token_version":       int64(1),
		"oauth_client_id":     strings.TrimSpace(result.TokenInfo.ClientID),
		"authorized_at":       time.Now().UTC().Format(time.RFC3339),
		"access_token":        result.TokenInfo.AccessToken,
		"refresh_token":       result.TokenInfo.RefreshToken,
		"id_token":            result.TokenInfo.IDToken,
		"expires_at":          time.Unix(result.TokenInfo.ExpiresAt, 0).UTC().Format(time.RFC3339),
	}
	if result.TokenInfo.Email != "" {
		chain["email"] = result.TokenInfo.Email
	}
	chains[result.CredentialChainID] = chain
	active[activeKey] = result.CredentialChainID
	next[openAIOAuthCredentialChainsKey] = chains
	next[openAIPersonaActiveChainsKey] = active
	return next, nil
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

// RefreshPersonaCredential refreshes exactly one bound chain and returns a
// full credential snapshot ready for the repository's guarded write path.
func (s *OpenAIOAuthService) RefreshPersonaCredential(ctx context.Context, account *Account, binding SessionPersonaSlotBinding) (*OpenAITokenInfo, map[string]any, error) {
	if account == nil || !account.IsOpenAIOAuth() || binding.AccountID != account.ID {
		return nil, nil, fmt.Errorf("%w: account mismatch", ErrOpenAITokenBindingInvalid)
	}
	persona, err := openAIPersonaForSlot(binding.SlotID)
	if err != nil || persona.ID != binding.PersonaID {
		return nil, nil, fmt.Errorf("%w: persona/slot mismatch", ErrOpenAITokenBindingInvalid)
	}
	chain := account.findPersonaCredentialByChainID(binding.PersonaID, binding.SlotID, binding.CredentialChainID)
	if chain == nil || !openAIPersonaCredentialReady(chain) {
		return nil, nil, fmt.Errorf("%w: chain=%q", ErrOpenAIPersonaCredentialChainNotReady, binding.CredentialChainID)
	}
	refreshToken := strings.TrimSpace(openAIMapString(chain, "refresh_token"))
	if refreshToken == "" {
		return nil, nil, fmt.Errorf("%w: chain=%q has no refresh_token", ErrOpenAIPersonaCredentialChainExpired, binding.CredentialChainID)
	}
	profileClient, ok := s.oauthClient.(OpenAIPersonaOAuthClient)
	if !ok {
		return nil, nil, fmt.Errorf("%w: profile-aware OAuth client unavailable", ErrOpenAIPersonaCredentialRefreshUnsupported)
	}
	proxyURL, err := s.openAIAccountProxyURL(ctx, account)
	if err != nil {
		return nil, nil, err
	}
	clientID := strings.TrimSpace(openAIMapString(chain, "oauth_client_id"))
	if clientID == "" {
		clientID = strings.TrimSpace(persona.OAuthClientID)
	}
	tokenResp, err := profileClient.RefreshTokenWithProfile(ctx, refreshToken, proxyURL, clientID, openAIPersonaOAuthProfile(persona))
	if err != nil {
		return nil, nil, err
	}
	info := s.openAIPersonaTokenInfo(ctx, tokenResp, clientID, proxyURL)
	if info.RefreshToken == "" {
		info.RefreshToken = refreshToken
	}
	storedAccountID := strings.TrimSpace(openAIMapString(chain, "chatgpt_account_id"))
	if info.ChatGPTAccountID != "" && storedAccountID != "" && !strings.EqualFold(info.ChatGPTAccountID, storedAccountID) {
		return nil, nil, fmt.Errorf("%w: refreshed chain belongs to a different ChatGPT account", ErrOpenAIPersonaCredentialChainMismatch)
	}
	next := shallowCopyMap(account.Credentials)
	updated := shallowCopyMap(chain)
	updated["access_token"] = info.AccessToken
	updated["refresh_token"] = info.RefreshToken
	updated["expires_at"] = time.Unix(info.ExpiresAt, 0).UTC().Format(time.RFC3339)
	updated["token_version"] = parseSessionPersonaInt64(chain["token_version"]) + 1
	updated["updated_at"] = time.Now().UTC().Format(time.RFC3339)
	if info.IDToken != "" {
		updated["id_token"] = info.IDToken
	}
	found := false
	for _, key := range []string{openAIPersonaCredentialsKey, openAIOAuthCredentialChainsKey} {
		if raw, ok := next[key]; ok {
			if replaced, ok := replaceOpenAIPersonaCredential(raw, binding.PersonaID, binding.SlotID, binding.CredentialChainID, updated); ok {
				next[key] = replaced
				found = true
				break
			}
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("%w: chain=%q", ErrOpenAIPersonaCredentialChainMissing, binding.CredentialChainID)
	}
	return info, next, nil
}

func replaceOpenAIPersonaCredential(raw any, persona SessionPersonaID, slotID int, chainID string, replacement map[string]any) (any, bool) {
	switch value := raw.(type) {
	case map[string]any:
		candidateChainID, hasChain := credentialChainIDFromMap(value)
		candidatePersona, hasPersona := credentialPersonaFromMap(value)
		candidateSlot, hasSlot := credentialSlotIDFromMap(value)
		if hasChain && hasPersona && hasSlot && candidateChainID == chainID && candidatePersona == persona && candidateSlot == slotID {
			return shallowCopyMap(replacement), true
		}
		out := shallowCopyMap(value)
		for key, item := range value {
			if replaced, ok := replaceOpenAIPersonaCredential(item, persona, slotID, chainID, replacement); ok {
				out[key] = replaced
				return out, true
			}
		}
	case []any:
		out := append([]any(nil), value...)
		for index, item := range value {
			if replaced, ok := replaceOpenAIPersonaCredential(item, persona, slotID, chainID, replacement); ok {
				out[index] = replaced
				return out, true
			}
		}
	case string:
		var decoded any
		if json.Unmarshal([]byte(value), &decoded) == nil {
			if replaced, ok := replaceOpenAIPersonaCredential(decoded, persona, slotID, chainID, replacement); ok {
				encoded, _ := json.Marshal(replaced)
				return string(encoded), true
			}
		}
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			if replaced, ok := replaceOpenAIPersonaCredential(decoded, persona, slotID, chainID, replacement); ok {
				encoded, _ := json.Marshal(replaced)
				return json.RawMessage(encoded), true
			}
		}
	}
	return raw, false
}
