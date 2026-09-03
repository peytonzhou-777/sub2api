package service

import (
	"context"
	"testing"
	"time"

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
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
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
		DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
	}
	session := OpenAIAccountPersonaSession{
		AccountPersonaID: 2, SessionEpoch: 4, PersonaGeneration: 3,
		CredentialChainID: "historical-chain", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: "1.18.23", InstallationID: "historical-installation",
		UpstreamSessionID: "historical-session", ProxySnapshotSet: true,
		StartedAt: time.Unix(1_700_000_000, 0),
	}
	target, err := OpenAIExecutionTargetFromPersonaSession(persona, session)
	require.NoError(t, err)
	require.Equal(t, int64(3), target.PersonaGeneration)
	require.Equal(t, "historical-installation", target.InstallationID)
}

func TestOpenAIPersonaRuntimeConcurrencyScopeUsesInstanceIdentity(t *testing.T) {
	target := OpenAIExecutionTarget{
		AccountID: 42, AccountPersonaID: 108, PersonaGeneration: 5, SessionEpoch: 9,
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		CredentialChainID: "chain", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: "release", InstallationID: "install", UpstreamSessionID: "session",
	}
	ctx := ContextWithOpenAIExecutionTarget(context.Background(), target)
	persona, slotID, epoch, dynamic := OpenAIPersonaRuntimeConcurrencyScope(ctx, 42, SessionPersonaCodexCLIStrict, 1, 2)
	if !dynamic || persona != "account_persona_108_generation_5_epoch_9" || slotID != 0 || epoch != 9 {
		t.Fatalf("unexpected dynamic concurrency scope: %q/%d/%d dynamic=%t", persona, slotID, epoch, dynamic)
	}
}

func TestScopeOpenAIFailoverToPersonaOnlyForCredentialFailure(t *testing.T) {
	target := OpenAIExecutionTarget{
		AccountID: 42, AccountPersonaID: 108, PersonaGeneration: 5, SessionEpoch: 9,
		SessionStartedAt: time.Unix(1_700_000_000, 0), DeviceSeed: []byte("0123456789abcdef0123456789abcdef"),
		CredentialChainID: "chain", ProfileID: SessionPersonaOpenCode,
		ProfileVersion: "release", InstallationID: "install", UpstreamSessionID: "session",
	}
	ctx := ContextWithOpenAIExecutionTarget(context.Background(), target)
	next, scoped := ScopeOpenAIFailoverToPersona(ctx, &UpstreamFailoverError{
		Stage: GatewayFailureStageAccountAuth, Scope: GatewayFailureScopeAccount,
	})
	if !scoped {
		t.Fatal("credential failure was not scoped to AccountPersona")
	}
	if _, ok := OpenAIAttemptExclusionsFromContext(next).AccountPersonaIDs[108]; !ok {
		t.Fatal("AccountPersona exclusion missing")
	}
	if _, scoped = ScopeOpenAIFailoverToPersona(ctx, &UpstreamFailoverError{Stage: GatewayFailureStageInference}); scoped {
		t.Fatal("inference failure must remain account-scoped")
	}
}
