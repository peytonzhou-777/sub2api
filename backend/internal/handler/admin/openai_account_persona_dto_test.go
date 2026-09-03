//go:build unit

package admin

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountPersonaDTOOmitsIdentitySecrets(t *testing.T) {
	dto := openAIAccountPersonaFromService(service.OpenAIAccountPersona{
		ID: 1, AccountID: 2, Position: 1, ProfileID: service.SessionPersonaOpenCode,
		ProfileVersion: "1.18.23", CredentialOwner: service.OpenAICredentialOwnerPersonaIndependent,
		State: service.OpenAIAccountPersonaStateActive, Enabled: true,
		CurrentCredentialChainID: "secret-chain", CurrentSessionEpoch: 3,
		DeviceSeed: []byte("secret-device-seed"), InstallationID: "secret-installation",
	})
	payload, err := json.Marshal(dto)
	require.NoError(t, err)
	serialized := string(payload)
	require.NotContains(t, serialized, "secret-chain")
	require.NotContains(t, serialized, "secret-device-seed")
	require.NotContains(t, serialized, "secret-installation")
	require.NotContains(t, serialized, "upstream_session")
}
