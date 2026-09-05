package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type openAIExecutionTargetRestorePersonaRepo struct {
	OpenAIAccountPersonaRepository
	persona OpenAIAccountPersona
	session OpenAIAccountPersonaSession
}

func (r *openAIExecutionTargetRestorePersonaRepo) GetAccountPersona(_ context.Context, accountID, personaID int64) (*OpenAIAccountPersona, error) {
	if accountID != r.persona.AccountID || personaID != r.persona.ID {
		return nil, ErrOpenAIAccountPersonaNotFound
	}
	value := r.persona
	return &value, nil
}

func (r *openAIExecutionTargetRestorePersonaRepo) GetAccountPersonaSession(_ context.Context, accountID, personaID, epoch int64, _ time.Time) (*OpenAIAccountPersonaSession, error) {
	if accountID != r.persona.AccountID || personaID != r.session.AccountPersonaID || epoch != r.session.SessionEpoch {
		return nil, ErrOpenAIAccountPersonaSessionNotFound
	}
	value := r.session
	return &value, nil
}

func TestRestoreOpenAIUserConversationExecutionTargetUsesInjectedPersonaRepository(t *testing.T) {
	startedAt := time.Now().UTC().Add(-time.Minute)
	repo := &openAIExecutionTargetRestorePersonaRepo{
		persona: OpenAIAccountPersona{
			ID: 51, AccountID: 56, Position: 0, ProfileID: SessionPersonaCodexCLIStrict,
			ProfileVersion: CodexOutboundProfileCLI0149, CredentialOwner: OpenAICredentialOwnerAccountPrimary,
			State: OpenAIAccountPersonaStateActive, Enabled: true, PersonaGeneration: 3,
			CurrentCredentialChainID: "chain-51", CurrentSessionEpoch: 7,
			DeviceSeed: []byte("0123456789abcdef0123456789abcdef"), InstallationID: "install-51",
		},
		session: OpenAIAccountPersonaSession{
			AccountPersonaID: 51, SessionEpoch: 7, UpstreamSessionID: "session-51-7",
			State: "current", PersonaGeneration: 3,
			CredentialChainID: "chain-51", ProfileID: SessionPersonaCodexCLIStrict,
			ProfileVersion: CodexOutboundProfileCLI0149, InstallationID: "install-51",
			ProxySnapshotSet: true, StartedAt: startedAt,
		},
	}
	svc := &OpenAIGatewayService{accountPersonaRepo: repo}

	target, err := svc.restoreOpenAIUserConversationExecutionTarget(context.Background(), &OpenAIUserConversationBinding{
		AccountID: 56, AccountPersonaID: 51, PersonaSessionEpoch: 7,
		CredentialChainID: "chain-51", ProfileID: SessionPersonaCodexCLIStrict,
		ProfileVersion: CodexOutboundProfileCLI0149, BindingEpoch: OpenAIConversationBindingEpoch,
		// 短期 lease 和原始客户端 Session 哈希不再是恢复持久 Thread 身份的前提。
		RootClientSessionHash: "",
	})
	require.NoError(t, err)
	require.Equal(t, int64(56), target.AccountID)
	require.Equal(t, int64(51), target.AccountPersonaID)
	require.Equal(t, int64(7), target.SessionEpoch)
}

func TestRestoreOpenAIUserConversationExecutionTargetRejectsOldBindingEpoch(t *testing.T) {
	svc := &OpenAIGatewayService{accountPersonaRepo: &openAIExecutionTargetRestorePersonaRepo{}}
	_, err := svc.restoreOpenAIUserConversationExecutionTarget(context.Background(), &OpenAIUserConversationBinding{
		AccountID: 56, AccountPersonaID: 51, PersonaSessionEpoch: 7,
		CredentialChainID: "chain-51", ProfileID: SessionPersonaCodexCLIStrict,
		ProfileVersion: CodexOutboundProfileCLI0149, BindingEpoch: OpenAIConversationBindingEpoch - 1,
	})
	require.ErrorIs(t, err, ErrOpenAIConversationResetRequired)
}

func TestRestoreOpenAIUserConversationExecutionTargetRequiresResetAfterSessionExpiry(t *testing.T) {
	repo := &openAIExecutionTargetRestorePersonaRepo{
		persona: OpenAIAccountPersona{ID: 51, AccountID: 56},
	}
	svc := &OpenAIGatewayService{accountPersonaRepo: repo}
	_, err := svc.restoreOpenAIUserConversationExecutionTarget(context.Background(), &OpenAIUserConversationBinding{
		AccountID: 56, AccountPersonaID: 51, PersonaSessionEpoch: 7,
		CredentialChainID: "chain-51", ProfileID: SessionPersonaCodexCLIStrict,
		ProfileVersion: CodexOutboundProfileCLI0149, BindingEpoch: OpenAIConversationBindingEpoch,
	})
	require.ErrorIs(t, err, ErrOpenAIConversationResetRequired)
}
