//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectAccountForSameAccountRetryNeverFallsBackToAnotherAccount(t *testing.T) {
	groupID := int64(7001)
	accounts := []Account{
		{
			ID:          7101,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			GroupIDs:    []int64{groupID},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_enabled": true,
			},
		},
		{
			ID:          7102,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Schedulable: true,
			Concurrency: 1,
			GroupIDs:    []int64{groupID},
		},
	}
	acquiredIDs := make([]int64, 0, 1)
	svc := &OpenAIGatewayService{
		accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
		cfg:         newSchedulerTestOpenAIWSV2Config(),
		concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
			acquiredIDs: &acquiredIDs,
		}),
	}

	selection, decision, err := svc.SelectAccountForSameAccountRetry(
		context.Background(),
		7102,
		&groupID,
		"gpt-5.1",
		OpenAIUpstreamTransportResponsesWebsocketV2Ingress,
		OpenAIEndpointCapabilityChatCompletions,
		false,
		PlatformOpenAI,
	)

	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Nil(t, selection)
	require.Equal(t, openAIAccountScheduleLayerSameAccountRetry, decision.Layer)
	require.Empty(t, acquiredIDs, "指定账号不合格时不得偷偷获取其他账号槽位")

	selection, decision, err = svc.SelectAccountForSameAccountRetry(
		context.Background(),
		7101,
		&groupID,
		"gpt-5.1",
		OpenAIUpstreamTransportResponsesWebsocketV2Ingress,
		OpenAIEndpointCapabilityChatCompletions,
		false,
		PlatformOpenAI,
	)

	require.NoError(t, err)
	require.NotNil(t, selection)
	require.Equal(t, int64(7101), selection.Account.ID)
	require.Equal(t, int64(7101), decision.SelectedAccountID)
	require.Equal(t, []int64{7101}, acquiredIDs)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}
