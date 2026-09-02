//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

type openAIPersonaCredentialRepoStub struct {
	mu          sync.Mutex
	slots       map[int]OpenAIPersonaSlotRecord
	credentials map[string]*OpenAIPersonaCredentialRecord
	casCalls    int
	claimCalls  int
	markCalls   int
	revokeCalls int
}

func newOpenAIPersonaCredentialRepoStub() *openAIPersonaCredentialRepoStub {
	return &openAIPersonaCredentialRepoStub{
		slots:       make(map[int]OpenAIPersonaSlotRecord),
		credentials: make(map[string]*OpenAIPersonaCredentialRecord),
	}
}

func openAIPersonaCredentialTestKey(accountID int64, persona SessionPersonaID, slotID int, chainID string) string {
	return formatInt64(accountID) + "|" + string(persona) + "|" + formatInt(slotID) + "|" + strings.TrimSpace(chainID)
}

func (r *openAIPersonaCredentialRepoStub) ListSlots(_ context.Context, accountID int64) ([]OpenAIPersonaSlotRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]OpenAIPersonaSlotRecord, 0, len(r.slots))
	for _, slot := range r.slots {
		if slot.AccountID == accountID {
			result = append(result, slot)
		}
	}
	return result, nil
}

func (r *openAIPersonaCredentialRepoStub) GetCredential(_ context.Context, accountID int64, persona SessionPersonaID, slotID int, chainID string) (*OpenAIPersonaCredentialRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.credentials[openAIPersonaCredentialTestKey(accountID, persona, slotID, chainID)]
	if record == nil {
		return nil, ErrOpenAIPersonaCredentialChainMissing
	}
	copy := *record
	copy.EncryptedPayload = append(copy.EncryptedPayload[:0:0], copy.EncryptedPayload...)
	return &copy, nil
}

func (r *openAIPersonaCredentialRepoStub) Authorize(_ context.Context, input OpenAIPersonaCredentialWrite) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	key := openAIPersonaCredentialTestKey(input.AccountID, input.PersonaID, input.SlotID, input.CredentialChainID)
	if _, exists := r.credentials[key]; exists {
		return errors.New("duplicate Persona credential")
	}
	r.credentials[key] = &OpenAIPersonaCredentialRecord{
		AccountID: input.AccountID, PersonaID: input.PersonaID, SlotID: input.SlotID,
		CredentialChainID: input.CredentialChainID, EncryptedPayload: append([]byte(nil), input.EncryptedPayload...),
		ChatGPTAccountID: input.ChatGPTAccountID, InstallationID: input.InstallationID,
		TokenVersion: 1, State: "ready", CreatedAt: now, UpdatedAt: now, LastRefreshedAt: &now,
	}
	r.slots[input.SlotID] = OpenAIPersonaSlotRecord{
		AccountID: input.AccountID, PersonaID: input.PersonaID, SlotID: input.SlotID,
		CredentialChainID: input.CredentialChainID, Enabled: true, State: SessionPersonaSlotStateActive,
		Authorized: true, SlotGeneration: input.SlotGeneration, SlotSetGeneration: input.SlotSetGeneration,
		CreatedAt: now, UpdatedAt: now,
	}
	return nil
}

func (r *openAIPersonaCredentialRepoStub) CompareAndSwapToken(_ context.Context, input OpenAIPersonaCredentialWrite, expectedVersion int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.casCalls++
	key := openAIPersonaCredentialTestKey(input.AccountID, input.PersonaID, input.SlotID, input.CredentialChainID)
	record := r.credentials[key]
	if record == nil || record.State != "refreshing" || record.TokenVersion != expectedVersion {
		return false, nil
	}
	record.EncryptedPayload = append([]byte(nil), input.EncryptedPayload...)
	record.TokenVersion++
	record.State = "ready"
	record.UpdatedAt = time.Now().UTC()
	record.LastRefreshedAt = &record.UpdatedAt
	return true, nil
}

func (r *openAIPersonaCredentialRepoStub) ClaimRefresh(_ context.Context, accountID int64, persona SessionPersonaID, slotID int, chainID string, expectedVersion int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claimCalls++
	record := r.credentials[openAIPersonaCredentialTestKey(accountID, persona, slotID, chainID)]
	if record == nil || record.State != "ready" || record.TokenVersion != expectedVersion {
		return false, nil
	}
	record.State = "refreshing"
	record.UpdatedAt = time.Now().UTC()
	return true, nil
}

func (r *openAIPersonaCredentialRepoStub) MarkInvalidIfVersion(_ context.Context, accountID int64, persona SessionPersonaID, slotID int, chainID string, expectedVersion int64, lastError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markCalls++
	record := r.credentials[openAIPersonaCredentialTestKey(accountID, persona, slotID, chainID)]
	if record != nil && (record.State == "ready" || record.State == "refreshing") && record.TokenVersion == expectedVersion {
		record.State = "invalid"
		record.LastError = lastError
		slot := r.slots[slotID]
		if slot.CredentialChainID == strings.TrimSpace(chainID) {
			slot.CredentialChainID = ""
			slot.Authorized = false
			r.slots[slotID] = slot
		}
	}
	return nil
}

func (r *openAIPersonaCredentialRepoStub) RevokeSlotAuthorization(_ context.Context, accountID int64, persona SessionPersonaID, slotID int) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revokeCalls++
	var chainIDs []string
	for _, record := range r.credentials {
		if record.AccountID == accountID && record.PersonaID == persona && record.SlotID == slotID && record.State != "revoked" {
			record.EncryptedPayload = []byte(`{}`)
			record.TokenVersion++
			record.State = "revoked"
			chainIDs = append(chainIDs, record.CredentialChainID)
		}
	}
	slot := r.slots[slotID]
	slot.CredentialChainID = ""
	slot.Authorized = false
	r.slots[slotID] = slot
	return chainIDs, nil
}

type openAIPersonaTestEncryptor struct{}

func (openAIPersonaTestEncryptor) Encrypt(plaintext string) (string, error) {
	return "persona-test:" + plaintext, nil
}

func (openAIPersonaTestEncryptor) Decrypt(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "persona-test:") {
		return "", errors.New("invalid Persona test ciphertext")
	}
	return strings.TrimPrefix(ciphertext, "persona-test:"), nil
}

func seedOpenAIPersonaCredential(
	repo *openAIPersonaCredentialRepoStub,
	oauth *OpenAIOAuthService,
	account *Account,
	binding SessionPersonaSlotBinding,
	info *OpenAITokenInfo,
) error {
	slotGeneration := binding.SlotGeneration
	if slotGeneration <= 0 {
		slotGeneration = 1
	}
	slotSetGeneration := binding.SlotSetGeneration
	if slotSetGeneration <= 0 {
		slotSetGeneration = 1
	}
	payload, err := oauth.encryptPersonaCredential(info)
	if err != nil {
		return err
	}
	return repo.Authorize(context.Background(), OpenAIPersonaCredentialWrite{
		AccountID: account.ID, PersonaID: binding.PersonaID, SlotID: binding.SlotID,
		CredentialChainID: binding.CredentialChainID, EncryptedPayload: payload,
		ChatGPTAccountID: info.ChatGPTAccountID, InstallationID: binding.InstallationID,
		SlotGeneration: slotGeneration, SlotSetGeneration: slotSetGeneration,
	})
}
