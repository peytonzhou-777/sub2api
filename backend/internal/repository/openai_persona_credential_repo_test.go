//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIPersonaCredentialRepository_RefreshClaimAndCAS(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &openAIPersonaCredentialRepository{db: db}

	mock.ExpectExec(regexp.QuoteMeta("UPDATE openai_account_persona_credentials")).
		WithArgs(int64(71), string(service.SessionPersonaOpenCode), 1, "chain-71", int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	claimed, err := repo.ClaimRefresh(context.Background(), 71, service.SessionPersonaOpenCode, 1, "chain-71", 4)
	require.NoError(t, err)
	require.True(t, claimed)

	write := service.OpenAIPersonaCredentialWrite{
		AccountID: 71, PersonaID: service.SessionPersonaOpenCode, SlotID: 1,
		CredentialChainID: "chain-71",
		EncryptedPayload:  json.RawMessage(`{"format_version":1,"ciphertext":"next"}`),
		ChatGPTAccountID:  "acct-71", InstallationID: "install-71",
		SlotGeneration: 2, SlotSetGeneration: 3,
	}
	mock.ExpectExec(`(?s)UPDATE openai_account_persona_credentials.*state = 'ready'.*state = 'refreshing'`).
		WithArgs([]byte(write.EncryptedPayload), int64(71), string(service.SessionPersonaOpenCode), 1, "chain-71", int64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	swapped, err := repo.CompareAndSwapToken(context.Background(), write, 4)
	require.NoError(t, err)
	require.True(t, swapped)
	require.NoError(t, mock.ExpectationsWereMet())
}
