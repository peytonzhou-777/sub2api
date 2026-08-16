//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func newSecurityDepositCreateTestService(gate SecurityDepositAccessGate) (*APIKeyService, *apiKeyRepoStub) {
	groupID := int64(9)
	repo := &apiKeyRepoStub{allowCreate: true}
	svc := &APIKeyService{
		apiKeyRepo: repo,
		userRepo:   &mockUserRepo{getByIDUser: &User{ID: 7}},
		groupRepo: &groupRepoStubForGroupUpdate{group: &Group{
			ID: groupID, Status: StatusActive, SubscriptionType: SubscriptionTypeStandard,
		}},
		cfg: testConfig(),
	}
	svc.SetSecurityDepositGate(gate)
	return svc, repo
}

func TestAPIKeyCreate_InsufficientDepositCreatesDisabledKey(t *testing.T) {
	groupID := int64(9)
	gate := &rejectingSecurityDepositGate{}
	svc, repo := newSecurityDepositCreateTestService(gate)

	created, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "deposit-key", GroupID: &groupID})

	require.NoError(t, err)
	require.Equal(t, StatusAPIKeyDisabled, created.Status)
	require.NotNil(t, created.DisabledReason)
	require.Equal(t, DisabledReasonSecurityDepositInsufficient, *created.DisabledReason)
	require.NotNil(t, created.DisabledAt)
	require.Len(t, repo.createdKeys, 1)
	require.Equal(t, StatusAPIKeyDisabled, repo.createdKeys[0].Status)
	require.Len(t, gate.calls, 1)
}

type allowingSecurityDepositGate struct{}

func (allowingSecurityDepositGate) CheckAccess(_ context.Context, userID, groupID int64) (*SecurityDepositAccessGrant, error) {
	return &SecurityDepositAccessGrant{UserID: userID, GroupID: groupID, Enforced: true}, nil
}

func TestAPIKeyCreate_SatisfiedDepositCreatesActiveKey(t *testing.T) {
	groupID := int64(9)
	svc, repo := newSecurityDepositCreateTestService(allowingSecurityDepositGate{})

	created, err := svc.Create(context.Background(), 7, CreateAPIKeyRequest{Name: "deposit-key", GroupID: &groupID})

	require.NoError(t, err)
	require.Equal(t, StatusAPIKeyActive, created.Status)
	require.Nil(t, created.DisabledReason)
	require.Nil(t, created.DisabledAt)
	require.Len(t, repo.createdKeys, 1)
	require.Equal(t, StatusAPIKeyActive, repo.createdKeys[0].Status)
}
