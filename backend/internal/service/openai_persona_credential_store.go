package service

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrOpenAIPersonaCredentialStoreUnavailable = errors.New("openai persona credential store is unavailable")
	ErrOpenAIPersonaCredentialCASConflict      = errors.New("openai persona credential version changed")
)

// OpenAIPersonaCredentialRecord 只携带动态 AccountPersona 的加密凭据与非敏感元数据。
type OpenAIPersonaCredentialRecord struct {
	AccountPersonaID  int64
	AccountID         int64
	PersonaID         SessionPersonaID
	ProfileVersion    string
	PersonaGeneration int64
	CredentialChainID string
	EncryptedPayload  json.RawMessage
	ChatGPTAccountID  string
	InstallationID    string
	TokenVersion      int64
	State             string
	LastError         string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	LastRefreshedAt   *time.Time
}

type openAIPersonaCredentialEnvelope struct {
	FormatVersion int    `json:"format_version"`
	Ciphertext    string `json:"ciphertext"`
}

type openAIPersonaCredentialSecrets struct {
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	IDToken       string `json:"id_token,omitempty"`
	ExpiresAt     int64  `json:"expires_at"`
	OAuthClientID string `json:"oauth_client_id"`
	Email         string `json:"email,omitempty"`
}

func (s *OpenAIOAuthService) configurePersonaCredentialStore(encryptor SecretEncryptor, tokenCache OpenAITokenCache) {
	if s == nil {
		return
	}
	s.personaCredentialEncryptor = encryptor
	s.personaTokenCache = tokenCache
}

func (s *OpenAIOAuthService) encryptPersonaCredential(info *OpenAITokenInfo) (json.RawMessage, error) {
	if s == nil || s.personaCredentialEncryptor == nil {
		return nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	plain, err := json.Marshal(openAIPersonaCredentialSecrets{
		AccessToken: strings.TrimSpace(info.AccessToken), RefreshToken: strings.TrimSpace(info.RefreshToken),
		IDToken: strings.TrimSpace(info.IDToken), ExpiresAt: info.ExpiresAt,
		OAuthClientID: strings.TrimSpace(info.ClientID), Email: strings.TrimSpace(info.Email),
	})
	if err != nil {
		return nil, err
	}
	ciphertext, err := s.personaCredentialEncryptor.Encrypt(string(plain))
	if err != nil {
		return nil, err
	}
	return json.Marshal(openAIPersonaCredentialEnvelope{FormatVersion: 1, Ciphertext: ciphertext})
}

func (s *OpenAIOAuthService) decryptPersonaCredential(record *OpenAIPersonaCredentialRecord) (*OpenAITokenInfo, error) {
	if s == nil || s.personaCredentialEncryptor == nil || record == nil || (record.State != "ready" && record.State != "refreshing") {
		return nil, ErrOpenAIPersonaCredentialChainNotReady
	}
	var envelope openAIPersonaCredentialEnvelope
	if err := json.Unmarshal(record.EncryptedPayload, &envelope); err != nil || envelope.FormatVersion != 1 || strings.TrimSpace(envelope.Ciphertext) == "" {
		return nil, ErrOpenAIPersonaCredentialChainNotReady
	}
	plain, err := s.personaCredentialEncryptor.Decrypt(envelope.Ciphertext)
	if err != nil {
		return nil, err
	}
	var secrets openAIPersonaCredentialSecrets
	if err := json.Unmarshal([]byte(plain), &secrets); err != nil {
		return nil, err
	}
	return &OpenAITokenInfo{
		AccessToken: secrets.AccessToken, RefreshToken: secrets.RefreshToken,
		IDToken: secrets.IDToken, ExpiresAt: secrets.ExpiresAt,
		ClientID: secrets.OAuthClientID, Email: secrets.Email,
		ChatGPTAccountID: record.ChatGPTAccountID,
	}, nil
}
