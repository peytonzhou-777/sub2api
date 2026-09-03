package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIAccountPersonaProtectedAndSchedulable(t *testing.T) {
	persona := OpenAIAccountPersona{
		ID: 10, AccountID: 20, Position: 0, ProfileID: SessionPersonaCodexCLIStrict,
		CredentialOwner: OpenAICredentialOwnerAccountPrimary, State: OpenAIAccountPersonaStateActive,
		Enabled: true, CurrentCredentialChainID: "chain", CurrentSessionEpoch: 1,
	}
	require.True(t, persona.IsDefaultProtected())
	require.True(t, persona.AcceptsNewRoot())
	persona.State = OpenAIAccountPersonaStateDraining
	require.False(t, persona.AcceptsNewRoot())
}

func TestOpenAIExecutionTargetContextRequiresCompleteIdentity(t *testing.T) {
	target := OpenAIExecutionTarget{
		AccountID: 1, AccountPersonaID: 2, PersonaGeneration: 3, SessionEpoch: 4,
		CredentialChainID: "chain", ProfileID: SessionPersonaOpenCode, ProfileVersion: "1.0.0",
		InstallationID: "installation", UpstreamSessionID: "session",
	}
	ctx := ContextWithOpenAIExecutionTarget(context.Background(), target)
	got, ok := OpenAIExecutionTargetFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, target, got)

	invalid := target
	invalid.AccountPersonaID = 0
	_, ok = OpenAIExecutionTargetFromContext(ContextWithOpenAIExecutionTarget(context.Background(), invalid))
	require.False(t, ok)
}

func TestOpenAIExecutionTargetUsesHistoricalSessionIdentity(t *testing.T) {
	persona := OpenAIAccountPersona{
		ID: 2, AccountID: 1, ProfileID: SessionPersonaOpenCode,
		ProfileVersion: "1.18.23", PersonaGeneration: 9, InstallationID: "current-installation",
	}
	session := OpenAIAccountPersonaSession{
		AccountPersonaID: 2, SessionEpoch: 4, PersonaGeneration: 3,
		CredentialChainID: "historical-chain", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: "1.18.23", InstallationID: "historical-installation",
		UpstreamSessionID: "historical-session", ProxySnapshotSet: true,
	}
	target, err := OpenAIExecutionTargetFromPersonaSession(persona, session)
	require.NoError(t, err)
	require.Equal(t, int64(3), target.PersonaGeneration)
	require.Equal(t, "historical-installation", target.InstallationID)
}
