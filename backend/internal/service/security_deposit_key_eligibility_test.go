package service

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type keyEligibilityGateStub struct {
	insufficient map[int64]bool
	calls        map[int64]int
	err          error
}

func (s *keyEligibilityGateStub) CheckAccess(_ context.Context, userID, groupID int64) (*SecurityDepositAccessGrant, error) {
	if s.calls == nil {
		s.calls = map[int64]int{}
	}
	s.calls[groupID]++
	if s.err != nil {
		return nil, s.err
	}
	if s.insufficient[groupID] {
		return nil, infraerrors.Forbidden("SECURITY_DEPOSIT_REQUIRED", "insufficient")
	}
	return &SecurityDepositAccessGrant{UserID: userID, GroupID: groupID, Enforced: true}, nil
}

type keyEligibilityRepoStub struct {
	keys        []SecurityDepositKeyReference
	disabledIDs []int64
	eventType   string
	eventID     int64
}

func (s *keyEligibilityRepoStub) ListActiveSecurityDepositKeys(context.Context, int64) ([]SecurityDepositKeyReference, error) {
	return s.keys, nil
}

func (s *keyEligibilityRepoStub) DisableActiveSecurityDepositKeys(_ context.Context, keyIDs []int64, eventType string, eventID int64, _ time.Time) ([]SecurityDepositKeyReference, error) {
	s.disabledIDs = append([]int64(nil), keyIDs...)
	s.eventType = eventType
	s.eventID = eventID
	byID := make(map[int64]bool, len(keyIDs))
	for _, id := range keyIDs {
		byID[id] = true
	}
	result := make([]SecurityDepositKeyReference, 0, len(keyIDs))
	for _, key := range s.keys {
		if byID[key.ID] {
			result = append(result, key)
		}
	}
	return result, nil
}

type keyEligibilityInvalidatorStub struct{ keys []string }

func (s *keyEligibilityInvalidatorStub) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.keys = append(s.keys, key)
}
func (*keyEligibilityInvalidatorStub) InvalidateAuthCacheByUserID(context.Context, int64)  {}
func (*keyEligibilityInvalidatorStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}

func TestKeyEligibilityReconcilerDisablesOnlyInsufficientActiveGroups(t *testing.T) {
	gate := &keyEligibilityGateStub{insufficient: map[int64]bool{10: true}}
	repo := &keyEligibilityRepoStub{keys: []SecurityDepositKeyReference{
		{ID: 1, UserID: 7, Key: "sk-one", GroupID: 10},
		{ID: 2, UserID: 7, Key: "sk-two", GroupID: 10},
		{ID: 3, UserID: 7, Key: "sk-three", GroupID: 20},
	}}
	invalidator := &keyEligibilityInvalidatorStub{}
	reconciler := NewKeyEligibilityReconciler(gate, repo, invalidator)

	disabled, err := reconciler.DisableInsufficientKeys(context.Background(), 7, "refund", 91)

	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, repo.disabledIDs)
	require.Equal(t, "refund", repo.eventType)
	require.Equal(t, int64(91), repo.eventID)
	require.Len(t, disabled, 2)
	require.Equal(t, []string{"sk-one", "sk-two"}, invalidator.keys)
	require.Equal(t, 1, gate.calls[10], "同一分组只应读取一次资格")
	require.Equal(t, 1, gate.calls[20])
}

func TestKeyEligibilityReconcilerFailsClosedWhenAccessStatusUnavailable(t *testing.T) {
	gate := &keyEligibilityGateStub{err: infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_STATUS_UNAVAILABLE", "unavailable")}
	repo := &keyEligibilityRepoStub{keys: []SecurityDepositKeyReference{{ID: 1, UserID: 7, Key: "sk-one", GroupID: 10}}}
	reconciler := NewKeyEligibilityReconciler(gate, repo, nil)

	_, err := reconciler.DisableInsufficientKeys(context.Background(), 7, "admin_deduction", 92)

	require.Error(t, err)
	require.Empty(t, repo.disabledIDs)
}
