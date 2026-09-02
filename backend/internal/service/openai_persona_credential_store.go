package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	// OpenAIPersonaSlot*ExtraKey 是账号调度快照中的非敏感 Persona 投影。
	// OAuth Token 只允许保存在 openai_account_persona_credentials。
	OpenAIPersonaActiveChainsExtraKey      = "openai_persona_slot_active_chain_ids"
	OpenAIPersonaSlotStateExtraKey         = "openai_persona_slot_states"
	OpenAIPersonaSlotEnabledExtraKey       = "openai_persona_slot_enabled"
	OpenAIPersonaSlotGenerationsExtraKey   = "openai_persona_slot_generations"
	OpenAIPersonaSlotSetGenerationExtraKey = "openai_persona_slot_set_generation"
	OpenAIPersonaSlotAuthorizedExtraKey    = "openai_persona_slot_authorized"
	OpenAIPersonaInstallationIDsExtraKey   = "openai_persona_slot_installation_ids"
)

var (
	ErrOpenAIPersonaCredentialStoreUnavailable = errors.New("openai persona credential store is unavailable")
	ErrOpenAIPersonaCredentialCASConflict      = errors.New("openai persona credential version changed")
)

// OpenAIPersonaSlotRecord 是永久槽位记录；禁用或撤销授权都不会删除它。
type OpenAIPersonaSlotRecord struct {
	AccountID         int64
	SlotID            int
	PersonaID         SessionPersonaID
	CredentialChainID string
	Enabled           bool
	State             SessionPersonaSlotState
	Authorized        bool
	SessionEpoch      int64
	SlotGeneration    int64
	SlotSetGeneration int64
	UpstreamSessionID string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DrainingStartedAt *time.Time
	DisabledAt        *time.Time
}

// OpenAIPersonaCredentialRecord 只携带加密封装和非敏感元数据。
type OpenAIPersonaCredentialRecord struct {
	AccountID         int64
	PersonaID         SessionPersonaID
	CredentialChainID string
	SlotID            int
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

type OpenAIPersonaCredentialWrite struct {
	AccountID         int64
	PersonaID         SessionPersonaID
	CredentialChainID string
	SlotID            int
	EncryptedPayload  json.RawMessage
	ChatGPTAccountID  string
	InstallationID    string
	SlotGeneration    int64
	SlotSetGeneration int64
}

// OpenAIPersonaCredentialRepository owns the transactional credential row,
// slot pointer, non-secret account projection, and scheduler outbox update.
type OpenAIPersonaCredentialRepository interface {
	ListSlots(ctx context.Context, accountID int64) ([]OpenAIPersonaSlotRecord, error)
	GetCredential(ctx context.Context, accountID int64, persona SessionPersonaID, slotID int, credentialChainID string) (*OpenAIPersonaCredentialRecord, error)
	Authorize(ctx context.Context, input OpenAIPersonaCredentialWrite) error
	ClaimRefresh(ctx context.Context, accountID int64, persona SessionPersonaID, slotID int, credentialChainID string, expectedVersion int64) (bool, error)
	CompareAndSwapToken(ctx context.Context, input OpenAIPersonaCredentialWrite, expectedVersion int64) (bool, error)
	MarkInvalidIfVersion(ctx context.Context, accountID int64, persona SessionPersonaID, slotID int, credentialChainID string, expectedVersion int64, lastError string) error
	RevokeSlotAuthorization(ctx context.Context, accountID int64, persona SessionPersonaID, slotID int) ([]string, error)
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

func (s *OpenAIOAuthService) configurePersonaCredentialStore(repo OpenAIPersonaCredentialRepository, encryptor SecretEncryptor, tokenCache OpenAITokenCache) {
	if s == nil {
		return
	}
	s.personaCredentialRepo = repo
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

func (s *OpenAIOAuthService) loadPersonaCredential(ctx context.Context, account *Account, binding SessionPersonaSlotBinding) (*OpenAITokenInfo, *OpenAIPersonaCredentialRecord, error) {
	if s == nil || s.personaCredentialRepo == nil || account == nil || binding.AccountID != account.ID {
		return nil, nil, ErrOpenAIPersonaCredentialStoreUnavailable
	}
	record, err := s.personaCredentialRepo.GetCredential(ctx, account.ID, binding.PersonaID, binding.SlotID, binding.CredentialChainID)
	if err != nil {
		return nil, nil, err
	}
	if record.AccountID != account.ID || record.PersonaID != binding.PersonaID || record.SlotID != binding.SlotID ||
		strings.TrimSpace(record.CredentialChainID) != strings.TrimSpace(binding.CredentialChainID) {
		return nil, nil, ErrOpenAIPersonaCredentialChainMismatch
	}
	if expected := strings.TrimSpace(account.GetChatGPTAccountID()); expected != "" &&
		record.ChatGPTAccountID != "" && !strings.EqualFold(expected, record.ChatGPTAccountID) {
		return nil, nil, ErrOpenAIPersonaCredentialChainMismatch
	}
	if binding.InstallationID != "" && strings.TrimSpace(binding.InstallationID) != strings.TrimSpace(record.InstallationID) {
		return nil, nil, ErrOpenAIPersonaCredentialChainMismatch
	}
	info, err := s.decryptPersonaCredential(record)
	return info, record, err
}
