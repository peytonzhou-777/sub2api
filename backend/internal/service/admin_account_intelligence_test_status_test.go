//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestUpdateAccountAcceptsIntelligenceTestStatusForOpenAIOAuth(t *testing.T) {
	for _, status := range []string{"passed", "failed"} {
		t.Run(status, func(t *testing.T) {
			accountID := int64(401)
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
				accountID: {
					ID:       accountID,
					Platform: PlatformOpenAI,
					Type:     AccountTypeOAuth,
					Status:   StatusActive,
				},
			}}

			updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(
				context.Background(),
				accountID,
				&UpdateAccountInput{IntelligenceTestStatus: &status},
			)

			require.NoError(t, err)
			require.Equal(t, status, updated.IntelligenceTestStatus)
			require.Equal(t, status, repo.accounts[accountID].IntelligenceTestStatus)
		})
	}
}

func TestUpdateAccountRejectsInvalidIntelligenceTestStatus(t *testing.T) {
	accountID := int64(402)
	invalid := "unmarked"
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
		accountID: {
			ID:       accountID,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
		},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(
		context.Background(),
		accountID,
		&UpdateAccountInput{IntelligenceTestStatus: &invalid},
	)

	require.Nil(t, updated)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "INVALID_INTELLIGENCE_TEST_STATUS", infraerrors.Reason(err))
	require.Empty(t, repo.accounts[accountID].IntelligenceTestStatus)
}

func TestUpdateAccountRejectsIntelligenceTestStatusForUnsupportedAccount(t *testing.T) {
	tests := []struct {
		name     string
		platform string
		typeName string
	}{
		{name: "OpenAI API key", platform: PlatformOpenAI, typeName: AccountTypeAPIKey},
		{name: "Grok OAuth", platform: PlatformGrok, typeName: AccountTypeOAuth},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountID := int64(410 + i)
			status := "passed"
			repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{
				accountID: {
					ID:       accountID,
					Platform: tt.platform,
					Type:     tt.typeName,
					Status:   StatusActive,
				},
			}}

			updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(
				context.Background(),
				accountID,
				&UpdateAccountInput{IntelligenceTestStatus: &status},
			)

			require.Nil(t, updated)
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
			require.Equal(t, "INTELLIGENCE_TEST_STATUS_UNSUPPORTED", infraerrors.Reason(err))
			require.Empty(t, repo.accounts[accountID].IntelligenceTestStatus)
		})
	}
}
