package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type fakeSecurityDepositRepository struct {
	data                   *SecurityDepositUserData
	dataErr                error
	dataCalls              int
	adminUser              *AdminSecurityDepositUserSummary
	accepted               bool
	acceptanceID           int64
	penaltyInput           SecurityDepositCyberPenaltyInput
	penaltyMax             int64
	penaltyShadow          bool
	penaltyResult          *SecurityDepositCyberPenaltyResult
	penaltyErr             error
	adminCreditInput       AdminSecurityDepositCreditInput
	adminCreditResult      *AdminSecurityDepositMutationResult
	adminDeductInput       AdminSecurityDepositDeductInput
	adminDeductEnforcement bool
	adminDeductResult      *AdminSecurityDepositMutationResult
	adminRevokeInput       AdminSecurityDepositRevokeInput
	adminRevokeEnforcement bool
	adminRevokeResult      *AdminSecurityDepositMutationResult
	adminUnlockInput       AdminSecurityDepositUnlockInput
	adminUnlockResult      *AdminSecurityDepositUnlockResult
	refundRecord           *SecurityDepositRefundRecord
	refundTarget           *SecurityDepositRefundTarget
	refundReserveInput     SecurityDepositRefundReserveInput
	refundReserveResult    *SecurityDepositRefundRecord
	refundClaimResult      *SecurityDepositRefundRecord
	refundClaimed          bool
	refundFinalizeState    string
	refundFinalizeResult   *SecurityDepositRefundRecord
	refundImpacts          []SecurityDepositRefundImpact
	refundQueryPrevious    string
	refundQueryClaimed     bool
}

type fakeSecurityDepositGroupAccess struct {
	groups []Group
	err    error
}

func (f fakeSecurityDepositGroupAccess) GetAvailableGroups(context.Context, int64) ([]Group, error) {
	return f.groups, f.err
}

type securityDepositSettingRepoStub struct {
	values map[string]string
}

func (s *securityDepositSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (s *securityDepositSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := s.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (s *securityDepositSettingRepoStub) Set(context.Context, string, string) error { return nil }

func (s *securityDepositSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := s.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (s *securityDepositSettingRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}

func (s *securityDepositSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return s.values, nil
}

func (s *securityDepositSettingRepoStub) Delete(context.Context, string) error { return nil }

type fakeSecurityDepositPaymentCreator struct {
	request             CreateOrderRequest
	refundPlan          *SecurityDepositGatewayRefundPlan
	refundResponse      *payment.RefundResponse
	refundPrepareErr    error
	refundErr           error
	refundPrepareCalls  int
	refundExecuteCalls  int
	refundCapability    securityDepositRefundCapability
	refundCapabilityErr error
}

func (f *fakeSecurityDepositPaymentCreator) CreateOrder(_ context.Context, req CreateOrderRequest) (*CreateOrderResponse, error) {
	f.request = req
	return &CreateOrderResponse{OrderID: 77}, nil
}

func (f *fakeSecurityDepositPaymentCreator) ParseWeChatPaymentResumeToken(string) (*WeChatPaymentResumeClaims, error) {
	return nil, infraerrors.BadRequest("UNEXPECTED", "unexpected resume token")
}

func (f *fakeSecurityDepositPaymentCreator) PrepareSecurityDepositGatewayRefund(_ context.Context, paymentOrderID, userID, principalCents int64, requestID, reason string) (*SecurityDepositGatewayRefundPlan, error) {
	f.refundPrepareCalls++
	if f.refundPlan == nil {
		f.refundPlan = &SecurityDepositGatewayRefundPlan{
			PaymentOrderID: paymentOrderID, UserID: userID, PrincipalCents: principalCents,
			GatewayAmount: "100.00", GatewayCurrency: "CNY", RequestID: requestID, Reason: reason,
		}
	}
	return f.refundPlan, f.refundPrepareErr
}

func (f *fakeSecurityDepositPaymentCreator) PrepareUserSecurityDepositGatewayRefund(ctx context.Context, paymentOrderID, userID, principalCents int64, requestID, reason string) (*SecurityDepositGatewayRefundPlan, error) {
	return f.PrepareSecurityDepositGatewayRefund(ctx, paymentOrderID, userID, principalCents, requestID, reason)
}

func (f *fakeSecurityDepositPaymentCreator) ExecuteSecurityDepositGatewayRefund(context.Context, *SecurityDepositGatewayRefundPlan) (*payment.RefundResponse, error) {
	f.refundExecuteCalls++
	return f.refundResponse, f.refundErr
}

func (f *fakeSecurityDepositPaymentCreator) QuerySecurityDepositGatewayRefund(context.Context, int64, string, string) (*payment.RefundResponse, error) {
	return f.refundResponse, f.refundErr
}

func (f *fakeSecurityDepositPaymentCreator) GetSecurityDepositRefundCapability(context.Context, int64) (securityDepositRefundCapability, error) {
	return f.refundCapability, f.refundCapabilityErr
}

func (f *fakeSecurityDepositRepository) HasAcceptedAgreement(context.Context, int64, int64, string, string) (bool, error) {
	return f.accepted, nil
}

func (f *fakeSecurityDepositRepository) AcceptAgreement(_ context.Context, acceptance SecurityDepositAgreementAcceptance) (*SecurityDepositAgreementAcceptance, error) {
	f.accepted = true
	if f.acceptanceID == 0 {
		f.acceptanceID = 1
	}
	acceptance.ID = f.acceptanceID
	return &acceptance, nil
}

func (f *fakeSecurityDepositRepository) GetUserData(context.Context, int64) (*SecurityDepositUserData, error) {
	f.dataCalls++
	return f.data, f.dataErr
}

func (f *fakeSecurityDepositRepository) ApplyCyberPolicyPenalty(_ context.Context, input SecurityDepositCyberPenaltyInput, maxRiskMultiplier int64, shadow bool) (*SecurityDepositCyberPenaltyResult, error) {
	f.penaltyInput = input
	f.penaltyMax = maxRiskMultiplier
	f.penaltyShadow = shadow
	return f.penaltyResult, f.penaltyErr
}

func (f *fakeSecurityDepositRepository) CreditAdminGrant(_ context.Context, input AdminSecurityDepositCreditInput) (*AdminSecurityDepositMutationResult, error) {
	f.adminCreditInput = input
	return f.adminCreditResult, nil
}

func (f *fakeSecurityDepositRepository) DeductAdminGrant(_ context.Context, input AdminSecurityDepositDeductInput, enforcementEnabled bool) (*AdminSecurityDepositMutationResult, error) {
	f.adminDeductInput = input
	f.adminDeductEnforcement = enforcementEnabled
	return f.adminDeductResult, nil
}

func (f *fakeSecurityDepositRepository) RevokeAdminGrantLot(_ context.Context, input AdminSecurityDepositRevokeInput, enforcementEnabled bool) (*AdminSecurityDepositMutationResult, error) {
	f.adminRevokeInput = input
	f.adminRevokeEnforcement = enforcementEnabled
	return f.adminRevokeResult, nil
}

func (f *fakeSecurityDepositRepository) UnlockSecurityLockedAPIKey(_ context.Context, input AdminSecurityDepositUnlockInput) (*AdminSecurityDepositUnlockResult, error) {
	f.adminUnlockInput = input
	return f.adminUnlockResult, nil
}

func (f *fakeSecurityDepositRepository) ListAdminUsers(context.Context, int, int, string) ([]AdminSecurityDepositUserSummary, int64, error) {
	return nil, 0, nil
}

func (f *fakeSecurityDepositRepository) GetAdminUser(context.Context, int64) (*AdminSecurityDepositUserSummary, error) {
	return f.adminUser, nil
}

func (f *fakeSecurityDepositRepository) ListLedger(context.Context, int64, int) ([]SecurityDepositLedgerEntry, error) {
	return []SecurityDepositLedgerEntry{}, nil
}

func (f *fakeSecurityDepositRepository) ListRefunds(context.Context, int64, int) ([]SecurityDepositRefundView, error) {
	return []SecurityDepositRefundView{}, nil
}

func (f *fakeSecurityDepositRepository) ListViolations(context.Context, int64, int) ([]SecurityDepositViolationView, error) {
	return []SecurityDepositViolationView{}, nil
}

func (f *fakeSecurityDepositRepository) GetSecurityDepositRefund(context.Context, string) (*SecurityDepositRefundRecord, error) {
	return f.refundRecord, nil
}

func (f *fakeSecurityDepositRepository) GetSecurityDepositRefundTarget(context.Context, int64, int64) (*SecurityDepositRefundTarget, error) {
	return f.refundTarget, nil
}

func (f *fakeSecurityDepositRepository) PreviewSecurityDepositRefundImpact(context.Context, int64, int64, bool) ([]SecurityDepositRefundImpact, error) {
	return f.refundImpacts, nil
}

func (f *fakeSecurityDepositRepository) ReserveSecurityDepositRefund(_ context.Context, input SecurityDepositRefundReserveInput, _ bool) (*SecurityDepositRefundRecord, error) {
	f.refundReserveInput = input
	if f.refundReserveResult == nil {
		f.refundReserveResult = &SecurityDepositRefundRecord{RefundID: input.RefundID, State: SecurityDepositRefundStateReserved}
	}
	return f.refundReserveResult, nil
}

func (f *fakeSecurityDepositRepository) ClaimAutomaticSecurityDepositRefund(context.Context, string) (*SecurityDepositRefundRecord, bool, error) {
	return f.refundClaimResult, f.refundClaimed, nil
}

func (f *fakeSecurityDepositRepository) ClaimAutomaticSecurityDepositRefundQuery(context.Context, string, int64) (*SecurityDepositRefundRecord, string, bool, error) {
	return f.refundClaimResult, f.refundQueryPrevious, f.refundQueryClaimed, nil
}

func (f *fakeSecurityDepositRepository) FinalizeAutomaticSecurityDepositRefund(_ context.Context, _ string, state, _ string, _ map[string]any) (*SecurityDepositRefundRecord, error) {
	f.refundFinalizeState = state
	if f.refundFinalizeResult == nil {
		f.refundFinalizeResult = &SecurityDepositRefundRecord{State: state}
	}
	return f.refundFinalizeResult, nil
}

func (f *fakeSecurityDepositRepository) CompleteManualSecurityDepositRefund(context.Context, AdminSecurityDepositManualCompleteInput) (*SecurityDepositRefundRecord, error) {
	return f.refundFinalizeResult, nil
}

func (f *fakeSecurityDepositRepository) CancelSecurityDepositRefund(context.Context, AdminSecurityDepositRefundCancelInput) (*SecurityDepositRefundRecord, error) {
	return f.refundFinalizeResult, nil
}

func (f *fakeSecurityDepositRepository) FailAutomaticSecurityDepositRefundReview(context.Context, AdminSecurityDepositAutomaticReviewFailureInput) (*SecurityDepositRefundRecord, error) {
	return f.refundFinalizeResult, nil
}

func TestSecurityDepositServiceGetAccountDerivesSeparatedBalances(t *testing.T) {
	now := time.Date(2026, time.August, 16, 0, 0, 0, 0, time.UTC)
	unlockAt := now.Add(2 * time.Hour)
	repo := &fakeSecurityDepositRepository{data: &SecurityDepositUserData{
		Accounts: []SecurityDepositAccountRecord{
			{BucketType: "paid", BalanceCents: 10000, RefundReservedCents: 1000, Version: 2},
			{BucketType: "admin_grant", BalanceCents: 5000, Version: 1},
		},
		CyberStrikeCount: 2,
		RiskMultiplier:   3,
		Lots: []SecurityDepositLot{
			{ID: 1, BucketType: "paid", SourceType: "payment", RemainingCents: 4000, LockedUntil: &unlockAt, RefundPolicy: "timed_original_channel"},
			{ID: 2, BucketType: "paid", SourceType: "payment", RemainingCents: 3000, RefundReservedCents: 1000, RefundPolicy: "timed_original_channel"},
			{ID: 3, BucketType: "paid", SourceType: "payment", RemainingCents: 3000, RefundPolicy: "timed_original_channel"},
			{ID: 4, BucketType: "admin_grant", SourceType: "admin", RemainingCents: 5000, RefundPolicy: "never"},
		},
	}}
	service := NewSecurityDepositService(repo)
	service.now = func() time.Time { return now }

	summary, err := service.GetAccount(context.Background(), 9)
	require.NoError(t, err)
	require.Equal(t, int64(10000), summary.PaidBalanceCents)
	require.Equal(t, int64(5000), summary.AdminGrantBalanceCents)
	require.Equal(t, int64(15000), summary.TotalBalanceCents)
	require.Equal(t, int64(14000), summary.EffectiveBalanceCents)
	require.Equal(t, int64(4000), summary.TimedLockedCents)
	require.Equal(t, int64(5000), summary.PermanentLockedCents)
	require.Equal(t, int64(5000), summary.RefundableCents)
	require.Equal(t, int64(1000), summary.PaidRefundReservedCents)
	require.Equal(t, int64(3), summary.RiskMultiplier)
	require.Equal(t, int64(2), summary.CyberStrikeCount)
	require.Equal(t, &unlockAt, summary.NextUnlockAt)
	require.False(t, summary.SelfRefundEnabled)
	require.False(t, summary.Lots[1].RefundEligible)
	require.Equal(t, "refund_in_progress", summary.Lots[1].RefundBlockReason)
	require.False(t, summary.Lots[2].SelfRefundEligible)
	require.Equal(t, "self_refund_disabled", summary.Lots[2].RefundBlockReason)
	require.True(t, summary.Lots[2].AdminActionRequired)
	require.Equal(t, "permanently_non_refundable", summary.Lots[3].RefundBlockReason)
}

func TestSecurityDepositServiceGetAccountDefaultsToZeroAndOneMultiplier(t *testing.T) {
	service := NewSecurityDepositService(&fakeSecurityDepositRepository{data: &SecurityDepositUserData{}})
	summary, err := service.GetAccount(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "CNY", summary.Currency)
	require.Zero(t, summary.TotalBalanceCents)
	require.Equal(t, int64(1), summary.RiskMultiplier)
	require.Equal(t, int64(8), summary.MaxRiskMultiplier)
	require.Empty(t, summary.Lots)
}

func TestSecurityDepositServiceGetAccountCombinesSelfRefundAndProviderSwitches(t *testing.T) {
	orderID := int64(77)
	repo := &fakeSecurityDepositRepository{data: &SecurityDepositUserData{
		Accounts: []SecurityDepositAccountRecord{{BucketType: "paid", BalanceCents: 10000}},
		Lots: []SecurityDepositLot{{
			ID: 8, BucketType: "paid", SourceType: "payment", PaymentOrderID: &orderID,
			RemainingCents: 10000, RefundPolicy: "timed_original_channel",
		}},
	}}
	gateway := &fakeSecurityDepositPaymentCreator{refundCapability: securityDepositRefundCapability{
		RefundEnabled: true, AllowUserRefund: true,
	}}
	settings := NewSettingService(&securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositSelfRefundEnabled: "true",
	}}, nil)
	svc := NewSecurityDepositService(repo)
	svc.SetOrderDependencies(nil, gateway, settings)

	summary, err := svc.GetAccount(context.Background(), 7)
	require.NoError(t, err)
	require.True(t, summary.SelfRefundEnabled)
	require.True(t, summary.Lots[0].SelfRefundEligible)
	require.True(t, summary.Lots[0].ProviderRefundEnabled)
	require.True(t, summary.Lots[0].ProviderUserRefund)
	require.False(t, summary.Lots[0].AdminActionRequired)
}

func TestSecurityDepositServiceAdminDetailReturnsNotFound(t *testing.T) {
	service := NewSecurityDepositService(&fakeSecurityDepositRepository{})
	_, err := service.GetAdminUserDetail(context.Background(), 404)
	require.Equal(t, "USER_NOT_FOUND", infraerrors.Reason(err))
}

func TestSecurityDepositServiceQuotesAndCreatesExactShortfallOrder(t *testing.T) {
	repo := &fakeSecurityDepositRepository{data: &SecurityDepositUserData{
		Accounts:       []SecurityDepositAccountRecord{{BucketType: "paid", BalanceCents: 5000}},
		RiskMultiplier: 2,
	}}
	paymentCreator := &fakeSecurityDepositPaymentCreator{}
	service := NewSecurityDepositService(repo)
	service.SetOrderDependencies(fakeSecurityDepositGroupAccess{groups: []Group{{
		ID: 9, Name: "受保护分组", SecurityDepositBaseRequiredCents: 10000,
	}}}, paymentCreator, nil)

	eligibility, err := service.GetEligibility(context.Background(), 42, 9)
	require.NoError(t, err)
	require.Equal(t, int64(20000), eligibility.RequiredCents)
	require.Equal(t, int64(5000), eligibility.EffectiveBalanceCents)
	require.Equal(t, int64(15000), eligibility.ShortfallCents)
	require.False(t, eligibility.Eligible)
	require.True(t, eligibility.AgreementRequired)
	require.NotEmpty(t, eligibility.QuoteHash)

	result, err := service.CreateOrder(context.Background(), CreateSecurityDepositOrderRequest{
		UserID: 42, GroupID: 9, PolicyVersion: eligibility.PolicyVersion,
		ContentHash: eligibility.ContentHash, QuoteHash: eligibility.QuoteHash,
		PaymentType: "alipay", ClientIP: "127.0.0.1", UserAgent: "test",
	})
	require.NoError(t, err)
	require.False(t, result.Satisfied)
	require.Equal(t, int64(77), result.Payment.OrderID)
	require.Equal(t, 150.0, paymentCreator.request.Amount)
	require.Equal(t, "security_deposit", paymentCreator.request.OrderType)
	require.NotNil(t, paymentCreator.request.SecurityDeposit)
	require.Equal(t, int64(15000), paymentCreator.request.SecurityDeposit.PrincipalCents)
	require.Equal(t, 24, paymentCreator.request.SecurityDeposit.FreezeHours)
	require.Equal(t, int64(1), paymentCreator.request.SecurityDeposit.AgreementID)
}

func TestSecurityDepositServiceRejectsChangedQuoteBeforeAcceptingPayment(t *testing.T) {
	repo := &fakeSecurityDepositRepository{data: &SecurityDepositUserData{RiskMultiplier: 1}}
	service := NewSecurityDepositService(repo)
	service.SetOrderDependencies(fakeSecurityDepositGroupAccess{groups: []Group{{
		ID: 3, Name: "安全分组", SecurityDepositBaseRequiredCents: 10000,
	}}}, &fakeSecurityDepositPaymentCreator{}, nil)
	eligibility, err := service.GetEligibility(context.Background(), 8, 3)
	require.NoError(t, err)

	_, err = service.CreateOrder(context.Background(), CreateSecurityDepositOrderRequest{
		UserID: 8, GroupID: 3, PolicyVersion: eligibility.PolicyVersion,
		ContentHash: eligibility.ContentHash, QuoteHash: "stale", PaymentType: "alipay",
	})
	require.Equal(t, "SECURITY_DEPOSIT_QUOTE_CHANGED", infraerrors.Reason(err))
	require.False(t, repo.accepted)
}

func TestSecurityDepositGateAllowsWithoutAccountReadWhenEnforcementDisabled(t *testing.T) {
	repo := &fakeSecurityDepositRepository{dataErr: errors.New("unexpected account read")}
	service := NewSecurityDepositService(repo)
	service.SetOrderDependencies(fakeSecurityDepositGroupAccess{groups: []Group{{
		ID: 7, Name: "受保护分组", SecurityDepositBaseRequiredCents: 10000,
	}}}, nil, nil)

	grant, err := service.CheckAccess(context.Background(), 42, 7)
	require.NoError(t, err)
	require.False(t, grant.Enforced)
	require.Equal(t, int64(10000), grant.BaseRequiredCents)
	require.Zero(t, repo.dataCalls)
}

func TestSecurityDepositGateUsesCombinedBucketsAndRiskMultiplier(t *testing.T) {
	repo := &fakeSecurityDepositRepository{data: &SecurityDepositUserData{
		Accounts: []SecurityDepositAccountRecord{
			{BucketType: "paid", BalanceCents: 12000, RefundReservedCents: 2000},
			{BucketType: "admin_grant", BalanceCents: 10000},
		},
		RiskMultiplier: 2,
	}}
	settings := &SettingService{settingRepo: &securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositEnforcementEnabled: "true",
	}}}
	service := NewSecurityDepositService(repo)
	service.SetOrderDependencies(fakeSecurityDepositGroupAccess{groups: []Group{{
		ID: 7, Name: "受保护分组", SecurityDepositBaseRequiredCents: 10000,
	}}}, nil, settings)

	grant, err := service.CheckAccess(context.Background(), 42, 7)
	require.NoError(t, err)
	require.True(t, grant.Enforced)
	require.Equal(t, int64(2), grant.RiskMultiplier)
	require.Equal(t, int64(20000), grant.RequiredCents)
	require.Equal(t, int64(20000), grant.EffectiveBalanceCents)
}

func TestSecurityDepositGateReturnsStableShortfallError(t *testing.T) {
	repo := &fakeSecurityDepositRepository{data: &SecurityDepositUserData{
		Accounts:       []SecurityDepositAccountRecord{{BucketType: "paid", BalanceCents: 5000}},
		RiskMultiplier: 2,
	}}
	settings := &SettingService{settingRepo: &securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositEnforcementEnabled: "true",
	}}}
	service := NewSecurityDepositService(repo)
	service.SetOrderDependencies(fakeSecurityDepositGroupAccess{groups: []Group{{
		ID: 7, Name: "受保护分组", SecurityDepositBaseRequiredCents: 10000,
	}}}, nil, settings)

	_, err := service.CheckAccess(context.Background(), 42, 7)
	require.Equal(t, "SECURITY_DEPOSIT_REQUIRED", infraerrors.Reason(err))
	appErr := infraerrors.FromError(err)
	require.Equal(t, "15000", appErr.Metadata["shortfall_cents"])
	require.Equal(t, "20000", appErr.Metadata["required_cents"])
	require.Equal(t, "5000", appErr.Metadata["effective_balance_cents"])
}

func TestSecurityDepositAccessSnapshotPreservesInsufficientPenaltyEvidence(t *testing.T) {
	repo := &fakeSecurityDepositRepository{data: &SecurityDepositUserData{
		Accounts:       []SecurityDepositAccountRecord{{BucketType: "paid", BalanceCents: 5000}},
		RiskMultiplier: 2,
	}}
	settings := &SettingService{settingRepo: &securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositEnforcementEnabled: "true",
	}}}
	service := NewSecurityDepositService(repo)
	service.SetOrderDependencies(fakeSecurityDepositGroupAccess{groups: []Group{{
		ID: 7, Name: "受保护分组", SecurityDepositBaseRequiredCents: 10000,
	}}}, nil, settings)

	grant, err := service.GetAccessSnapshot(context.Background(), 42, 7)

	require.NoError(t, err)
	require.True(t, grant.Enforced)
	require.Equal(t, int64(20000), grant.RequiredCents)
	require.Equal(t, int64(5000), grant.EffectiveBalanceCents)
}

func TestSecurityDepositGateFailsClosedWhenProtectedAccountStateIsUnknown(t *testing.T) {
	repo := &fakeSecurityDepositRepository{dataErr: errors.New("database unavailable")}
	settings := &SettingService{settingRepo: &securityDepositSettingRepoStub{values: map[string]string{
		SettingKeySecurityDepositEnforcementEnabled: "true",
	}}}
	service := NewSecurityDepositService(repo)
	service.SetOrderDependencies(fakeSecurityDepositGroupAccess{groups: []Group{{
		ID: 7, Name: "受保护分组", SecurityDepositBaseRequiredCents: 10000,
	}}}, nil, settings)

	_, err := service.CheckAccess(context.Background(), 42, 7)
	require.Equal(t, "SECURITY_DEPOSIT_STATUS_UNAVAILABLE", infraerrors.Reason(err))
}
