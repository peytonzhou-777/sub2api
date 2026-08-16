package service

import (
	"context"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func newSecurityDepositAdminTestService(repo *fakeSecurityDepositRepository, enforcement bool, cache APIKeyAuthCacheInvalidator) *SecurityDepositService {
	settings := NewSettingService(&securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositEnforcementEnabled: boolString(enforcement),
	}}, nil)
	svc := NewSecurityDepositService(repo)
	svc.SetOrderDependencies(nil, nil, settings)
	svc.SetPenaltyDependencies(cache)
	return svc
}

func TestSecurityDepositAdminCredit_NormalizesOptionalReasonAndInvalidatesCache(t *testing.T) {
	repo := &fakeSecurityDepositRepository{adminCreditResult: &AdminSecurityDepositMutationResult{
		ActionID: 10, ActionType: SecurityDepositAdminActionAdd, UserID: 7,
	}}
	cache := &securityDepositAdminCacheStub{}
	svc := newSecurityDepositAdminTestService(repo, true, cache)
	reconciler := &securityDepositKeyChangeReconcilerStub{disabled: []SecurityDepositKeyReference{{ID: 4}}}
	svc.SetKeyEligibilityReconciler(reconciler)
	reason := "   "

	result, err := svc.AdminCreditAdminGrant(context.Background(), AdminSecurityDepositCreditInput{
		UserID: 7, OperatorID: 2, AmountCents: 10000, ActionType: SecurityDepositAdminActionAdd,
		Reason: &reason, IdempotencyKey: " credit-1 ",
	})

	require.NoError(t, err)
	require.Equal(t, int64(10), result.ActionID)
	require.Nil(t, repo.adminCreditInput.Reason)
	require.Equal(t, "credit-1", repo.adminCreditInput.IdempotencyKey)
	require.Equal(t, []int64{4}, result.DisabledKeyIDs)
	require.Equal(t, SecurityDepositAdminActionAdd, reconciler.eventType)
	require.Equal(t, []int64{7}, cache.userIDs)
}

func TestSecurityDepositAdminDeduct_PassesEnforcementAndKeepsPaidBucketOutsideContract(t *testing.T) {
	repo := &fakeSecurityDepositRepository{adminDeductResult: &AdminSecurityDepositMutationResult{
		ActionID: 11, ActionType: SecurityDepositAdminActionDeduct, UserID: 7, AmountCents: 5000,
	}}
	cache := &securityDepositAdminCacheStub{}
	svc := newSecurityDepositAdminTestService(repo, true, cache)
	reconciler := &securityDepositKeyChangeReconcilerStub{}
	svc.SetKeyEligibilityReconciler(reconciler)

	result, err := svc.AdminDeductAdminGrant(context.Background(), AdminSecurityDepositDeductInput{
		UserID: 7, OperatorID: 2, AmountCents: 5000, IdempotencyKey: "deduct-1",
	})

	require.NoError(t, err)
	require.Equal(t, int64(5000), result.AmountCents)
	require.True(t, repo.adminDeductEnforcement)
	require.Equal(t, int64(5000), repo.adminDeductInput.AmountCents)
	require.Equal(t, SecurityDepositAdminActionDeduct, reconciler.eventType)
	require.Equal(t, int64(11), reconciler.eventID)
	require.NotEmpty(t, cache.locks)
	require.Equal(t, cache.locks, cache.releases)
	require.Equal(t, []int64{7, 7}, cache.userIDs)
}

func TestSecurityDepositAdminRevoke_AllowsMissingReason(t *testing.T) {
	lotID := int64(33)
	repo := &fakeSecurityDepositRepository{adminRevokeResult: &AdminSecurityDepositMutationResult{
		ActionID: 12, ActionType: SecurityDepositAdminActionRevoke, UserID: 7, LotID: &lotID,
	}}
	svc := newSecurityDepositAdminTestService(repo, false, &securityDepositAdminCacheStub{})
	reconciler := &securityDepositKeyChangeReconcilerStub{}
	svc.SetKeyEligibilityReconciler(reconciler)

	result, err := svc.AdminRevokeAdminGrantLot(context.Background(), AdminSecurityDepositRevokeInput{
		UserID: 7, OperatorID: 2, LotID: lotID, IdempotencyKey: "revoke-1",
	})

	require.NoError(t, err)
	require.Equal(t, lotID, *result.LotID)
	require.False(t, repo.adminRevokeEnforcement)
	require.Nil(t, repo.adminRevokeInput.Reason)
	require.Equal(t, SecurityDepositAdminActionRevoke, reconciler.eventType)
	require.Equal(t, int64(12), reconciler.eventID)
}

func TestSecurityDepositAdminUnlock_InvalidatesKeyAndLeavesDisabled(t *testing.T) {
	repo := &fakeSecurityDepositRepository{adminUnlockResult: &AdminSecurityDepositUnlockResult{
		ActionID: 13, UserID: 7, APIKeyID: 9, Status: StatusAPIKeyDisabled, APIKey: "sk-test",
	}}
	cache := &securityDepositAdminCacheStub{}
	svc := newSecurityDepositAdminTestService(repo, true, cache)

	result, err := svc.AdminUnlockSecurityLockedAPIKey(context.Background(), AdminSecurityDepositUnlockInput{
		UserID: 7, OperatorID: 2, APIKeyID: 9, IdempotencyKey: "unlock-1",
	})

	require.NoError(t, err)
	require.Equal(t, StatusAPIKeyDisabled, result.Status)
	require.Equal(t, []string{"sk-test"}, cache.keys)
	require.Equal(t, []int64{7}, cache.userIDs)
}

func TestSecurityDepositAdminMutations_RequireIdempotencyKey(t *testing.T) {
	repo := &fakeSecurityDepositRepository{}
	svc := newSecurityDepositAdminTestService(repo, true, nil)

	_, err := svc.AdminCreditAdminGrant(context.Background(), AdminSecurityDepositCreditInput{
		UserID: 7, OperatorID: 2, AmountCents: 100, ActionType: SecurityDepositAdminActionAdd,
	})

	require.Error(t, err)
	require.Equal(t, infraerrors.Reason(ErrIdempotencyKeyRequired), infraerrors.Reason(err))
}

type securityDepositAdminCacheStub struct {
	keys     []string
	userIDs  []int64
	locks    []string
	releases []string
	active   int
	waiting  int
}

func (s *securityDepositAdminCacheStub) InvalidateAuthCacheByKey(_ context.Context, key string) {
	s.keys = append(s.keys, key)
}

func (s *securityDepositAdminCacheStub) InvalidateAuthCacheByGroupID(context.Context, int64) {}

func (s *securityDepositAdminCacheStub) InvalidateAuthCacheByUserID(_ context.Context, userID int64) {
	s.userIDs = append(s.userIDs, userID)
}

func (s *securityDepositAdminCacheStub) AcquireRefundBillingLock(_ context.Context, _ int64, refundID string) error {
	s.locks = append(s.locks, refundID)
	return nil
}

func (s *securityDepositAdminCacheStub) ReleaseRefundBillingLock(_ context.Context, _ int64, refundID string) error {
	s.releases = append(s.releases, refundID)
	return nil
}

func (s *securityDepositAdminCacheStub) IsRefundBillingLocked(context.Context, int64) (bool, error) {
	return len(s.locks) > len(s.releases), nil
}

func (s *securityDepositAdminCacheStub) GetUserConcurrency(context.Context, int64) (int, error) {
	return s.active, nil
}

func (s *securityDepositAdminCacheStub) GetUserWaitingCount(context.Context, int64) (int, error) {
	return s.waiting, nil
}
