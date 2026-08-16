package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestAdminAutomaticSecurityDepositRefundPreservesPendingReservation(t *testing.T) {
	repo := &fakeSecurityDepositRepository{
		refundTarget: &SecurityDepositRefundTarget{
			UserID: 7, LotID: 8, PaymentOrderID: 9, PrincipalCents: 10000, Currency: "CNY",
		},
		refundClaimResult: &SecurityDepositRefundRecord{RefundID: "claimed", State: SecurityDepositRefundStateSubmitting},
		refundClaimed:     true,
		refundFinalizeResult: &SecurityDepositRefundRecord{
			RefundID: "final", UserID: 7, State: SecurityDepositRefundStatePending,
		},
	}
	cache := &securityDepositAdminCacheStub{}
	gateway := &fakeSecurityDepositPaymentCreator{
		refundResponse: &payment.RefundResponse{RefundID: "provider-refund-1", Status: payment.ProviderStatusPending},
	}
	svc := newSecurityDepositAdminTestService(repo, true, cache)
	svc.SetOrderDependencies(nil, gateway, svc.settings)

	result, err := svc.AdminAutomaticRefundPaidLot(context.Background(), AdminSecurityDepositAutomaticRefundInput{
		UserID: 7, LotID: 8, OperatorID: 2, IdempotencyKey: "refund-pending-1",
	})

	require.NoError(t, err)
	require.Equal(t, SecurityDepositRefundStatePending, result.State)
	require.Equal(t, SecurityDepositRefundModeAutomatic, repo.refundReserveInput.Mode)
	require.Equal(t, int64(10000), repo.refundReserveInput.PrincipalCents)
	require.Equal(t, SecurityDepositRefundStatePending, repo.refundFinalizeState)
	require.Equal(t, 1, gateway.refundPrepareCalls)
	require.Equal(t, 1, gateway.refundExecuteCalls)
	require.NotEmpty(t, cache.locks)
	require.Equal(t, cache.locks, cache.releases)
}

func TestUserSecurityDepositRefundChecksFreezeAndProviderBeforeFinancialFence(t *testing.T) {
	lockedUntil := time.Now().Add(time.Hour)
	repo := &fakeSecurityDepositRepository{refundTarget: &SecurityDepositRefundTarget{
		UserID: 7, LotID: 8, PaymentOrderID: 9, PrincipalCents: 10000, Currency: "CNY", LockedUntil: &lockedUntil,
	}}
	cache := &securityDepositAdminCacheStub{}
	gateway := &fakeSecurityDepositPaymentCreator{}
	settings := NewSettingService(&securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositEnforcementEnabled: "true",
		SettingKeySecurityDepositSelfRefundEnabled:  "true",
	}}, nil)
	svc := NewSecurityDepositService(repo)
	svc.SetOrderDependencies(nil, gateway, settings)
	svc.SetPenaltyDependencies(cache)

	_, err := svc.UserAutomaticRefundPaidLot(context.Background(), UserSecurityDepositAutomaticRefundInput{
		UserID: 7, LotID: 8, IdempotencyKey: "self-refund-frozen",
	})
	require.Error(t, err)
	require.Equal(t, "SECURITY_DEPOSIT_REFUND_FROZEN", infraerrors.Reason(err))
	require.Zero(t, gateway.refundPrepareCalls)
	require.Empty(t, cache.locks)

	repo.refundTarget.LockedUntil = nil
	gateway.refundPrepareErr = infraerrors.Forbidden("SECURITY_DEPOSIT_USER_REFUND_NOT_ALLOWED", "user refund disabled")
	_, err = svc.UserAutomaticRefundPaidLot(context.Background(), UserSecurityDepositAutomaticRefundInput{
		UserID: 7, LotID: 8, IdempotencyKey: "self-refund-provider-disabled",
	})
	require.Error(t, err)
	require.Equal(t, "SECURITY_DEPOSIT_USER_REFUND_NOT_ALLOWED", infraerrors.Reason(err))
	require.Empty(t, cache.locks)
}

func TestUserSecurityDepositRefundUsesUnlockedReservationAndFinancialFence(t *testing.T) {
	repo := &fakeSecurityDepositRepository{
		refundTarget: &SecurityDepositRefundTarget{
			UserID: 7, LotID: 8, PaymentOrderID: 9, PrincipalCents: 10000, Currency: "CNY",
		},
		refundClaimResult: &SecurityDepositRefundRecord{State: SecurityDepositRefundStateSubmitting},
		refundClaimed:     true,
		refundFinalizeResult: &SecurityDepositRefundRecord{
			UserID: 7, State: SecurityDepositRefundStateSucceeded,
		},
	}
	cache := &securityDepositAdminCacheStub{}
	gateway := &fakeSecurityDepositPaymentCreator{refundResponse: &payment.RefundResponse{RefundID: "provider-1", Status: payment.ProviderStatusSuccess}}
	settings := NewSettingService(&securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositEnforcementEnabled: "true",
		SettingKeySecurityDepositSelfRefundEnabled:  "true",
	}}, nil)
	svc := NewSecurityDepositService(repo)
	svc.SetOrderDependencies(nil, gateway, settings)
	svc.SetPenaltyDependencies(cache)

	result, err := svc.UserAutomaticRefundPaidLot(context.Background(), UserSecurityDepositAutomaticRefundInput{
		UserID: 7, LotID: 8, IdempotencyKey: "self-refund-success",
	})
	require.NoError(t, err)
	require.Equal(t, SecurityDepositRefundStateSucceeded, result.State)
	require.True(t, repo.refundReserveInput.RequireUnlocked)
	require.Equal(t, int64(7), repo.refundReserveInput.OperatorID)
	require.NotEmpty(t, cache.locks)
	require.Equal(t, cache.locks, cache.releases)
}

func TestQueryAutomaticSecurityDepositRefundFinalizesKnownGatewayState(t *testing.T) {
	repo := &fakeSecurityDepositRepository{
		refundClaimResult: &SecurityDepositRefundRecord{
			RefundID: "sdref-query", UserID: 7, PaymentOrderID: 9, GatewayAmount: "100.00",
			ProviderResponseSnapshot: map[string]any{"refund_id": "provider-1"},
		},
		refundQueryPrevious: SecurityDepositRefundStatePending,
		refundQueryClaimed:  true,
		refundFinalizeResult: &SecurityDepositRefundRecord{
			RefundID: "sdref-query", UserID: 7, State: SecurityDepositRefundStateSucceeded,
		},
	}
	gateway := &fakeSecurityDepositPaymentCreator{refundResponse: &payment.RefundResponse{RefundID: "provider-1", Status: payment.ProviderStatusSuccess}}
	svc := NewSecurityDepositService(repo)
	svc.SetOrderDependencies(nil, gateway, nil)

	result, err := svc.QueryAutomaticSecurityDepositRefund(context.Background(), 7, "sdref-query")
	require.NoError(t, err)
	require.Equal(t, SecurityDepositRefundStateSucceeded, result.State)
	require.Equal(t, SecurityDepositRefundStateSucceeded, repo.refundFinalizeState)
}

func TestAdminAutomaticSecurityDepositRefundChecksProviderBeforeFinancialFence(t *testing.T) {
	repo := &fakeSecurityDepositRepository{refundTarget: &SecurityDepositRefundTarget{
		UserID: 7, LotID: 8, PaymentOrderID: 9, PrincipalCents: 10000, Currency: "CNY",
	}}
	cache := &securityDepositAdminCacheStub{}
	gateway := &fakeSecurityDepositPaymentCreator{
		refundPrepareErr: infraerrors.Forbidden("SECURITY_DEPOSIT_PROVIDER_REFUND_DISABLED", "refund is disabled"),
	}
	svc := newSecurityDepositAdminTestService(repo, true, cache)
	svc.SetOrderDependencies(nil, gateway, svc.settings)

	result, err := svc.AdminAutomaticRefundPaidLot(context.Background(), AdminSecurityDepositAutomaticRefundInput{
		UserID: 7, LotID: 8, OperatorID: 2, IdempotencyKey: "refund-disabled-1",
	})

	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "SECURITY_DEPOSIT_PROVIDER_REFUND_DISABLED", infraerrors.Reason(err))
	require.Empty(t, cache.locks)
	require.Zero(t, gateway.refundExecuteCalls)
}

func TestAdminManualSecurityDepositRefundRequiresExternalFacts(t *testing.T) {
	svc := NewSecurityDepositService(&fakeSecurityDepositRepository{})

	result, err := svc.AdminCompleteManualRefund(context.Background(), AdminSecurityDepositManualCompleteInput{
		UserID: 7, RefundID: "sdref-manual", OperatorID: 2, ExternalAmountCents: 100,
		IdempotencyKey: "manual-complete-1",
	})

	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "SECURITY_DEPOSIT_EXTERNAL_REFUND_FACTS_REQUIRED", infraerrors.Reason(err))
}

func TestAdminManualSecurityDepositRefundCompletionUsesFinancialFence(t *testing.T) {
	repo := &fakeSecurityDepositRepository{
		refundFinalizeResult: &SecurityDepositRefundRecord{RefundID: "sdref-manual", State: SecurityDepositRefundStateSucceeded},
	}
	cache := &securityDepositAdminCacheStub{}
	svc := NewSecurityDepositService(repo)
	svc.SetPenaltyDependencies(cache)

	result, err := svc.AdminCompleteManualRefund(context.Background(), AdminSecurityDepositManualCompleteInput{
		UserID: 7, RefundID: "sdref-manual", OperatorID: 2,
		ExternalRefundID: "external-1", ExternalAmountCents: 100,
		ExternalRefundedAt: time.Now().UTC(), ExternalEvidence: map[string]any{"receipt": "stored"},
		IdempotencyKey: "manual-complete-1",
	})

	require.NoError(t, err)
	require.Equal(t, SecurityDepositRefundStateSucceeded, result.State)
	require.NotEmpty(t, cache.locks)
	require.Equal(t, cache.locks, cache.releases)
}

func TestAdminAutomaticRefundFailureReviewRequiresEvidence(t *testing.T) {
	svc := NewSecurityDepositService(&fakeSecurityDepositRepository{})
	result, err := svc.AdminFailAutomaticRefundReview(context.Background(), AdminSecurityDepositAutomaticReviewFailureInput{
		UserID: 7, RefundID: "sdref-unknown", OperatorID: 2, IdempotencyKey: "review-1",
	})
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, "SECURITY_DEPOSIT_REFUND_REVIEW_EVIDENCE_REQUIRED", infraerrors.Reason(err))
}
