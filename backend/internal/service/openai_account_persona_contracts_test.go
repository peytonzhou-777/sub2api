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
