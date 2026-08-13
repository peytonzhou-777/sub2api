package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"entgo.io/ent/dialect"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/ent/user"
	creditgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const (
	AccountRefundStateDraining               = "draining"
	AccountRefundStateReadyToConfirm         = "ready_to_confirm"
	AccountRefundStateSubmitting             = "submitting"
	AccountRefundStatePending                = "pending"
	AccountRefundStateSucceeded              = "succeeded"
	AccountRefundStateFailed                 = "failed"
	AccountRefundStatePartialExternalSuccess = "partial_external_success"
	AccountRefundStateCanceled               = "canceled"
	AccountRefundStateManualReview           = "manual_review"
	AccountRefundStateDonated                = "donated"

	accountRefundAuditPrefix    = "account_refund:"
	accountRefundReason         = "account full clearance"
	accountRefundTolerance      = 0.00000001
	accountRefundDonationAction = "ACCOUNT_REFUND_DONATED"
)

func accountRefundInFlightOrderPredicate() predicate.PaymentOrder {
	return paymentorder.Or(
		paymentorder.StatusIn(OrderStatusPending, OrderStatusPaid, OrderStatusRecharging),
		paymentorder.And(paymentorder.StatusEQ(OrderStatusFailed), paymentorder.PaidAtNotNil()),
	)
}

// AccountRefundOrder 展示账户清退中一条只读支付路由。
type AccountRefundOrder struct {
	OrderID          int64     `json:"order_id"`
	CompletedAt      time.Time `json:"completed_at"`
	PaymentType      string    `json:"payment_type"`
	ProviderInstance string    `json:"provider_instance_id"`
	Currency         string    `json:"currency"`
	OriginalCredit   float64   `json:"original_credit"`
	OriginalPaid     float64   `json:"original_paid"`
	BonusRate        float64   `json:"bonus_rate"`
	BonusInitial     float64   `json:"bonus_initial"`
	BonusRemaining   float64   `json:"bonus_remaining"`
	EligibleCredit   float64   `json:"eligible_credit"`
	RefundCredit     float64   `json:"refund_credit"`
	GatewayRefund    float64   `json:"gateway_refund"`
	Allocation       string    `json:"allocation_confidence"`
	GatewayStatus    string    `json:"gateway_status,omitempty"`
	GatewayRefundID  string    `json:"gateway_refund_id,omitempty"`
	GatewayError     string    `json:"gateway_error,omitempty"`
}

// AccountRefundQuote 是账户级权威试算结果；不同币种始终独立汇总。
type AccountRefundQuote struct {
	Eligible             bool                 `json:"eligible"`
	BlockReason          string               `json:"block_reason,omitempty"`
	DonationEligible     bool                 `json:"donation_eligible"`
	DonationBlockReason  string               `json:"donation_block_reason,omitempty"`
	DonationAmount       float64              `json:"donation_amount"`
	TotalConfidence      string               `json:"total_confidence"`
	AllocationConfidence string               `json:"allocation_confidence"`
	PermanentBalance     float64              `json:"permanent_balance"`
	RechargeBonusBalance float64              `json:"recharge_bonus_balance"`
	OtherLimitedToClear  float64              `json:"other_limited_to_clear"`
	EligibleCreditTotal  float64              `json:"eligible_credit_total"`
	RefundCreditTotal    float64              `json:"refund_credit_total"`
	GatewayTotals        map[string]float64   `json:"gateway_totals"`
	Orders               []AccountRefundOrder `json:"orders"`
	QuoteHash            string               `json:"quote_hash"`
}

// AccountRefundRecord 是写入 payment_audit_logs.detail 的账户级状态快照。
type AccountRefundRecord struct {
	RefundID           string                 `json:"refund_id"`
	UserID             int64                  `json:"user_id"`
	State              string                 `json:"state"`
	PreviousUserStatus string                 `json:"previous_user_status"`
	Quote              *AccountRefundQuote    `json:"quote,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
	Message            string                 `json:"message,omitempty"`
	SessionToken       string                 `json:"session_token,omitempty"`
	DonationRequested  bool                   `json:"donation_requested,omitempty"`
	Donation           *AccountRefundDonation `json:"donation,omitempty"`
}

// AccountRefundDonation 是打赏名单中公开展示的不可变快照。
type AccountRefundDonation struct {
	Username    string    `json:"username"`
	MaskedEmail string    `json:"masked_email"`
	Amount      float64   `json:"amount"`
	DonatedAt   time.Time `json:"donated_at"`
}

type accountRefundDrainReader interface {
	GetUserConcurrency(ctx context.Context, userID int64) (int, error)
}

type accountRefundWaitReader interface {
	GetUserWaitingCount(ctx context.Context, userID int64) (int, error)
}

// SetAccountRefundDependencies 注入清退锁定、排空与专用会话恢复所需能力。
func (s *PaymentService) SetAccountRefundDependencies(auth APIKeyAuthCacheInvalidator, concurrency ConcurrencyCache, totpService *TotpService) {
	s.authCacheInvalidator = auth
	s.concurrencyCache = concurrency
	s.totpService = totpService
}

// ParseAccountRefundSession 校验锁定后接口使用的退款专用凭证。
func (s *PaymentService) ParseAccountRefundSession(token string) (*AccountRefundSessionClaims, error) {
	if strings.TrimSpace(token) == "" {
		return nil, infraerrors.Unauthorized("REFUND_SESSION_REQUIRED", "refund session is required")
	}
	return s.paymentResume().ParseAccountRefundSessionToken(token)
}

// RestoreAccountRefundSession 通过强身份验证只补发当前清退的专用凭证，不恢复普通登录权限。
func (s *PaymentService) RestoreAccountRefundSession(ctx context.Context, email, password, totpCode string) (*AccountRefundRecord, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	account, err := s.userRepo.GetByEmail(ctx, email)
	if err != nil || account == nil || !account.CheckPassword(password) {
		return nil, ErrInvalidCredentials
	}
	if account.Status != StatusRefundLocked {
		return nil, infraerrors.Conflict("NO_ACTIVE_ACCOUNT_REFUND", "account has no locked refund session")
	}
	if account.TotpEnabled {
		if s.totpService == nil {
			return nil, infraerrors.ServiceUnavailable("REFUND_SESSION_RECOVERY_UNAVAILABLE", "totp verification is unavailable")
		}
		if strings.TrimSpace(totpCode) == "" {
			return nil, infraerrors.BadRequest("TOTP_CODE_REQUIRED", "totp code is required")
		}
		if err := s.totpService.VerifyCode(ctx, account.ID, strings.TrimSpace(totpCode)); err != nil {
			return nil, err
		}
	}
	record, err := s.latestAccountRefundForUser(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	if record == nil || record.RefundID == "" || record.State == AccountRefundStateCanceled {
		return nil, infraerrors.Conflict("NO_ACTIVE_ACCOUNT_REFUND", "account has no recoverable refund session")
	}
	if fence, ok := s.authCacheInvalidator.(RefundBillingFence); ok {
		if err := fence.AcquireRefundBillingLock(ctx, account.ID, record.RefundID); err != nil {
			return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "cannot renew refund billing fence")
		}
	}
	token, err := s.paymentResume().CreateAccountRefundSessionToken(record.RefundID, account.ID)
	if err != nil {
		return nil, err
	}
	record.SessionToken = token
	return record, nil
}

// GetAccountRefundOverview 返回当前试算或正在进行的清退状态。
func (s *PaymentService) GetAccountRefundOverview(ctx context.Context, userID int64) (*AccountRefundRecord, error) {
	if current, err := s.latestAccountRefundForUser(ctx, userID); err != nil {
		return nil, err
	} else if current != nil && !accountRefundTerminal(current.State) {
		return s.refreshAccountRefundDrain(ctx, current)
	}
	quote, err := s.buildAccountRefundQuote(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &AccountRefundRecord{UserID: userID, State: "estimate", Quote: quote, UpdatedAt: time.Now().UTC()}, nil
}

// LockAccountRefund 原子锁定用户，并为二次确认签发退款专用会话。
func (s *PaymentService) LockAccountRefund(ctx context.Context, userID int64, quoteHash string) (*AccountRefundRecord, error) {
	return s.lockAccountRefund(ctx, userID, quoteHash, false)
}

// DonateAccountRefund 经用户二次确认后锁定账户，排空完成即放弃退款并计入打赏名单。
func (s *PaymentService) DonateAccountRefund(ctx context.Context, userID int64, quoteHash string) (*AccountRefundRecord, error) {
	return s.lockAccountRefund(ctx, userID, quoteHash, true)
}

func (s *PaymentService) lockAccountRefund(ctx context.Context, userID int64, quoteHash string, donationRequested bool) (*AccountRefundRecord, error) {
	quote, err := s.buildAccountRefundQuote(ctx, userID)
	if err != nil {
		return nil, err
	}
	if donationRequested && !quote.DonationEligible {
		return nil, infraerrors.Conflict("REFUND_DONATION_UNAVAILABLE", quote.DonationBlockReason)
	}
	if !donationRequested && !quote.Eligible {
		return nil, infraerrors.Conflict("REFUND_MANUAL_REVIEW", quote.BlockReason)
	}
	if strings.TrimSpace(quoteHash) == "" || quoteHash != quote.QuoteHash {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "refund quote changed; review it again")
	}
	if current, err := s.latestAccountRefundForUser(ctx, userID); err != nil {
		return nil, err
	} else if current != nil && !accountRefundTerminal(current.State) {
		return nil, infraerrors.Conflict("REFUND_ALREADY_ACTIVE", "an account refund is already active")
	}

	refundID, err := newAccountRefundID()
	if err != nil {
		return nil, fmt.Errorf("generate refund id: %w", err)
	}
	token, err := s.paymentResume().CreateAccountRefundSessionToken(refundID, userID)
	if err != nil {
		return nil, err
	}
	fence, ok := s.authCacheInvalidator.(RefundBillingFence)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "refund billing fence is not configured")
	}
	if err := fence.AcquireRefundBillingLock(ctx, userID, refundID); err != nil {
		return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "cannot establish refund billing fence")
	}
	fenceOwned := true
	defer func() {
		if fenceOwned {
			_ = fence.ReleaseRefundBillingLock(context.Background(), userID, refundID)
		}
	}()
	now := time.Now().UTC()
	record := &AccountRefundRecord{RefundID: refundID, UserID: userID, State: AccountRefundStateDraining, Quote: quote, CreatedAt: now, UpdatedAt: now, DonationRequested: donationRequested}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin account refund lock: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	userQuery := tx.User.Query().Where(user.IDEQ(userID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		userQuery = userQuery.ForUpdate()
	}
	lockedUser, err := userQuery.Only(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock refund user: %w", err)
	}
	if lockedUser.Status != StatusActive {
		return nil, infraerrors.Conflict("USER_INACTIVE", "only an active account can start a refund")
	}
	inFlightQuery := tx.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(userID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		accountRefundInFlightOrderPredicate(),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		inFlightQuery = inFlightQuery.ForUpdate()
	}
	inFlightOrders, err := inFlightQuery.All(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock in-flight recharge orders: %w", err)
	}
	if len(inFlightOrders) > 0 {
		return nil, infraerrors.Conflict("REFUND_RECHARGE_IN_FLIGHT", "finish or cancel all balance recharge orders before refund")
	}
	if math.Abs(lockedUser.Balance-quote.PermanentBalance) > accountRefundTolerance || lockedUser.FrozenBalance > accountRefundTolerance {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "balance changed; review the refund quote again")
	}
	record.PreviousUserStatus = lockedUser.Status
	if _, err = tx.User.UpdateOne(lockedUser).SetStatus(StatusRefundLocked).Save(txCtx); err != nil {
		return nil, fmt.Errorf("set refund lock: %w", err)
	}
	if err = writeAccountRefundAudit(txCtx, tx.Client(), record); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit account refund lock: %w", err)
	}
	fenceOwned = false
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	record.SessionToken = token
	return s.refreshAccountRefundDrain(ctx, record)
}

// GetAccountRefund 通过退款专用会话读取并推进排空状态。
func (s *PaymentService) GetAccountRefund(ctx context.Context, refundID string, userID int64) (*AccountRefundRecord, error) {
	if fence, ok := s.authCacheInvalidator.(RefundBillingFence); ok {
		if err := fence.AcquireRefundBillingLock(ctx, userID, refundID); err != nil {
			return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "cannot renew refund billing fence")
		}
	}
	record, err := s.latestAccountRefund(ctx, refundID, userID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, infraerrors.NotFound("REFUND_NOT_FOUND", "account refund not found")
	}
	if record.State == AccountRefundStatePending {
		return s.reconcileAccountRefundPending(ctx, record)
	}
	return s.refreshAccountRefundDrain(ctx, record)
}

// ConfirmAccountRefund 执行账户级全额清退；已成功的支付路由不会在本次循环内重试。
func (s *PaymentService) ConfirmAccountRefund(ctx context.Context, refundID string, userID int64, quoteHash string) (*AccountRefundRecord, error) {
	record, err := s.GetAccountRefund(ctx, refundID, userID)
	if err != nil {
		return nil, err
	}
	if record.State != AccountRefundStateReadyToConfirm {
		return nil, infraerrors.Conflict("REFUND_NOT_READY_TO_CONFIRM", "refund billing is not fully drained")
	}
	if record.Quote == nil || quoteHash != record.Quote.QuoteHash {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "refund quote changed; review it again")
	}
	record, err = s.claimAccountRefundSubmission(ctx, record)
	if err != nil {
		return nil, err
	}
	return s.executeAccountRefundRoutes(ctx, record)
}

// DonateLockedAccountRefund 将已排空的退款会话改为打赏终态，不调用任何支付网关。
func (s *PaymentService) DonateLockedAccountRefund(ctx context.Context, refundID string, userID int64, quoteHash string) (*AccountRefundRecord, error) {
	record, err := s.GetAccountRefund(ctx, refundID, userID)
	if err != nil {
		return nil, err
	}
	if record.State != AccountRefundStateDraining && record.State != AccountRefundStateReadyToConfirm && record.State != AccountRefundStateManualReview {
		return nil, infraerrors.Conflict("REFUND_NOT_READY_TO_DONATE", "refund billing is not fully drained")
	}
	if record.Quote == nil || strings.TrimSpace(quoteHash) == "" || quoteHash != record.Quote.QuoteHash {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "refund quote changed; review it again")
	}
	if record.State == AccountRefundStateDraining {
		record.DonationRequested = true
		record.Message = "waiting for billing to drain before donation"
		record.UpdatedAt = time.Now().UTC()
		if err := s.writeAccountRefundAudit(ctx, record); err != nil {
			return nil, err
		}
		return record, nil
	}
	return s.finalizeAccountRefundDonation(ctx, record)
}

// claimAccountRefundSubmission 在同一事务锁定资金输入与最新状态，只有一个确认请求能进入网关阶段。
func (s *PaymentService) claimAccountRefundSubmission(ctx context.Context, record *AccountRefundRecord) (*AccountRefundRecord, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin account refund confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	userQuery := tx.User.Query().Where(user.IDEQ(record.UserID), user.StatusEQ(StatusRefundLocked))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		userQuery = userQuery.ForUpdate()
	}
	lockedUser, err := userQuery.Only(txCtx)
	if err != nil {
		return nil, infraerrors.Conflict("REFUND_LOCK_LOST", "refund no longer owns the account lock")
	}
	completedOrderQuery := tx.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(record.UserID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		paymentorder.StatusEQ(OrderStatusCompleted),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		completedOrderQuery = completedOrderQuery.ForUpdate()
	}
	if _, err = completedOrderQuery.All(txCtx); err != nil {
		return nil, fmt.Errorf("lock refund orders: %w", err)
	}
	inFlightQuery := tx.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(record.UserID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		accountRefundInFlightOrderPredicate(),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		inFlightQuery = inFlightQuery.ForUpdate()
	}
	inFlightOrders, err := inFlightQuery.All(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock in-flight recharge orders: %w", err)
	}
	if len(inFlightOrders) > 0 {
		return nil, infraerrors.Conflict("REFUND_RECHARGE_IN_FLIGHT", "a balance recharge is still being processed")
	}
	grantQuery := tx.UserLimitedCreditGrant.Query().Where(creditgrant.UserIDEQ(record.UserID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		grantQuery = grantQuery.ForUpdate()
	}
	if _, err = grantQuery.All(txCtx); err != nil {
		return nil, fmt.Errorf("lock refund credits: %w", err)
	}
	auditQuery := tx.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(accountRefundAuditKey(record.UserID, record.RefundID)),
	).Order(dbent.Desc(paymentauditlog.FieldCreatedAt), dbent.Desc(paymentauditlog.FieldID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		auditQuery = auditQuery.ForUpdate()
	}
	latestRow, err := auditQuery.First(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock account refund state: %w", err)
	}
	latest, err := parseAccountRefundRecord(latestRow)
	if err != nil {
		return nil, err
	}
	if latest.State != AccountRefundStateReadyToConfirm {
		return nil, infraerrors.Conflict("REFUND_NOT_READY_TO_CONFIRM", "refund confirmation was already claimed or is not ready")
	}
	account := &User{ID: lockedUser.ID, Balance: lockedUser.Balance, FrozenBalance: lockedUser.FrozenBalance, TotalRecharged: lockedUser.TotalRecharged, Status: lockedUser.Status}
	currentQuote, err := s.buildAccountRefundQuoteWithClient(txCtx, tx.Client(), account)
	if err != nil {
		return nil, err
	}
	if latest.Quote == nil || !currentQuote.Eligible || currentQuote.QuoteHash != latest.Quote.QuoteHash {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "balance changed after lock; manual review is required")
	}
	latest.State = AccountRefundStateSubmitting
	latest.UpdatedAt = time.Now().UTC()
	if err := writeAccountRefundAudit(txCtx, tx.Client(), latest); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit account refund confirmation: %w", err)
	}
	return latest, nil
}

func (s *PaymentService) executeAccountRefundRoutes(ctx context.Context, record *AccountRefundRecord) (*AccountRefundRecord, error) {
	succeeded := 0
	for i := range record.Quote.Orders {
		route := &record.Quote.Orders[i]
		if route.GatewayStatus == payment.ProviderStatusSuccess || route.GatewayStatus == payment.ProviderStatusRefunded {
			succeeded++
			continue
		}
		if route.GatewayStatus == payment.ProviderStatusPending {
			record.State = AccountRefundStatePending
			return record, nil
		}
		order, err := s.entClient.PaymentOrder.Get(ctx, route.OrderID)
		if err != nil {
			route.GatewayError = err.Error()
			return s.failAccountRefund(ctx, record, succeeded, "load refund order: "+err.Error())
		}
		plan := &RefundPlan{OrderID: order.ID, Order: order, RequestID: accountRefundRouteRequestID(record.RefundID, order.ID), RefundAmount: route.RefundCredit, GatewayAmount: route.GatewayRefund, Reason: accountRefundReason, Force: true}
		resp, callErr := s.gwRefund(ctx, plan)
		if callErr != nil {
			route.GatewayError = callErr.Error()
			return s.failAccountRefund(ctx, record, succeeded, callErr.Error())
		}
		route.GatewayStatus = strings.TrimSpace(resp.Status)
		route.GatewayRefundID = refundResponseID(resp)
		if route.GatewayStatus == payment.ProviderStatusPending {
			_ = s.markAccountRefundOrderPending(ctx, plan, resp)
			record.State = AccountRefundStatePending
			record.Message = "gateway refund is pending confirmation"
			record.UpdatedAt = time.Now().UTC()
			if err := s.writeAccountRefundAudit(ctx, record); err != nil {
				return nil, err
			}
			return record, nil
		}
		if _, err := s.markRefundOk(ctx, plan); err != nil {
			route.GatewayError = err.Error()
			return s.failAccountRefund(ctx, record, succeeded+1, "gateway succeeded but local order update failed: "+err.Error())
		}
		succeeded++
	}

	if err := s.finalizeAccountRefundCredits(ctx, record); err != nil {
		return s.failAccountRefund(ctx, record, succeeded, "finalize account credits: "+err.Error())
	}
	record.State = AccountRefundStateSucceeded
	record.Message = "account refund completed"
	record.UpdatedAt = time.Now().UTC()
	if err := s.writeAccountRefundAudit(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

// CancelAccountRefund 只允许在调用任何支付网关前撤销，并恢复原账户状态。
func (s *PaymentService) CancelAccountRefund(ctx context.Context, refundID string, userID int64) (*AccountRefundRecord, error) {
	record, err := s.GetAccountRefund(ctx, refundID, userID)
	if err != nil {
		return nil, err
	}
	if record.State != AccountRefundStateDraining && record.State != AccountRefundStateReadyToConfirm {
		return nil, infraerrors.Conflict("REFUND_CANNOT_CANCEL_AFTER_SUBMISSION", "refund cannot be canceled after gateway submission")
	}
	restore := record.PreviousUserStatus
	if restore == "" {
		restore = StatusDisabled
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin refund cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	updated, err := tx.User.Update().Where(user.IDEQ(userID), user.StatusEQ(StatusRefundLocked)).SetStatus(restore).Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("restore account status: %w", err)
	}
	if updated == 0 {
		return nil, infraerrors.Conflict("REFUND_LOCK_LOST", "refund no longer owns the account lock")
	}
	record.State = AccountRefundStateCanceled
	record.Message = "account refund canceled"
	record.UpdatedAt = time.Now().UTC()
	if err := writeAccountRefundAudit(txCtx, tx.Client(), record); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit refund cancellation: %w", err)
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if fence, ok := s.authCacheInvalidator.(RefundBillingFence); ok {
		if err := fence.ReleaseRefundBillingLock(ctx, userID, refundID); err != nil {
			return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "account restored but refund billing fence could not be released")
		}
	}
	return record, nil
}

func (s *PaymentService) buildAccountRefundQuote(ctx context.Context, userID int64) (*AccountRefundQuote, error) {
	account, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get refund user: %w", err)
	}
	return s.buildAccountRefundQuoteWithClient(ctx, s.entClient, account)
}

type accountRefundBonusAggregate struct {
	initial   decimal.Decimal
	remaining decimal.Decimal
}

// buildAccountRefundQuoteWithClient 允许确认事务在已锁定资金行上复算同一份权威试算。
func (s *PaymentService) buildAccountRefundQuoteWithClient(ctx context.Context, client *dbent.Client, account *User) (*AccountRefundQuote, error) {
	inFlight, err := client.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(account.ID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		accountRefundInFlightOrderPredicate(),
	).Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("count in-flight recharge orders: %w", err)
	}
	orders, err := client.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(account.ID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		paymentorder.StatusEQ(OrderStatusCompleted),
	).Order(dbent.Asc(paymentorder.FieldCompletedAt), dbent.Asc(paymentorder.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list refundable orders: %w", err)
	}
	grants, err := client.UserLimitedCreditGrant.Query().Where(creditgrant.UserIDEQ(account.ID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list refund credits: %w", err)
	}

	quote := &AccountRefundQuote{TotalConfidence: "manual_review", AllocationConfidence: "exact", PermanentBalance: normalizeRefundFloat(account.Balance), GatewayTotals: map[string]float64{}, Orders: make([]AccountRefundOrder, 0, len(orders))}
	donationBlockReason := ""
	if inFlight > 0 {
		quote.BlockReason = "a balance recharge order is still pending or being fulfilled"
		donationBlockReason = quote.BlockReason
	}
	orderByID := make(map[int64]*dbent.PaymentOrder, len(orders))
	for _, order := range orders {
		orderByID[order.ID] = order
	}
	bonusByOrder := make(map[int64]accountRefundBonusAggregate)
	now := time.Now().UTC()
	for _, grant := range grants {
		if grant.SourceType == LimitedCreditSourceRechargeBonus && grant.SourceID != nil {
			if _, exists := orderByID[*grant.SourceID]; !exists {
				quote.BlockReason = "a recharge bonus cannot be matched to a completed recharge order"
				donationBlockReason = quote.BlockReason
				continue
			}
			aggregate := bonusByOrder[*grant.SourceID]
			aggregate.initial = aggregate.initial.Add(decimal.NewFromFloat(grant.InitialAmount))
			if grant.Status == LimitedCreditStatusActive && grant.ExpiresAt.After(now) {
				remaining := math.Max(0, grant.InitialAmount-grant.UsedAmount-grant.FrozenAmount)
				aggregate.remaining = aggregate.remaining.Add(decimal.NewFromFloat(remaining))
				quote.RechargeBonusBalance += remaining
			}
			bonusByOrder[*grant.SourceID] = aggregate
		} else if grant.Status == LimitedCreditStatusActive && grant.ExpiresAt.After(now) {
			quote.OtherLimitedToClear += math.Max(0, grant.InitialAmount-grant.UsedAmount-grant.FrozenAmount)
		}
		if grant.FrozenAmount > accountRefundTolerance {
			quote.BlockReason = "limited credits are still frozen"
			donationBlockReason = quote.BlockReason
		}
	}
	if account.FrozenBalance > accountRefundTolerance {
		quote.BlockReason = "permanent balance is still frozen"
		donationBlockReason = quote.BlockReason
	}
	if len(orders) == 0 && quote.BlockReason == "" {
		quote.BlockReason = "no completed balance recharge order is refundable"
	}
	if len(orders) == 0 {
		donationBlockReason = "no completed balance recharge order can support the donation amount"
	}

	totalOriginal := decimal.Zero
	totalBonusInitial := decimal.Zero
	currencies := make(map[string]struct{})
	for _, order := range orders {
		totalOriginal = totalOriginal.Add(decimal.NewFromFloat(order.Amount))
		bonus := bonusByOrder[order.ID]
		bonusInitial := bonus.initial.InexactFloat64()
		bonusRemaining := bonus.remaining.InexactFloat64()
		totalBonusInitial = totalBonusInitial.Add(bonus.initial)
		hasActualBonus := bonus.initial.GreaterThan(decimal.Zero)
		if hasActualBonus != (order.RechargeBonusStatus == paymentorder.RechargeBonusStatusGranted) {
			quote.BlockReason = "recharge bonus history cannot be reconciled to its order snapshot"
			donationBlockReason = quote.BlockReason
		}
		if strings.TrimSpace(order.PaymentTradeNo) == "" {
			quote.BlockReason = "a historical order has no original payment trade number"
			continue
		}
		if order.ProviderInstanceID == nil || strings.TrimSpace(*order.ProviderInstanceID) == "" {
			quote.BlockReason = "a historical order has no original payment provider"
			continue
		}
		instanceID, parseErr := strconv.ParseInt(strings.TrimSpace(*order.ProviderInstanceID), 10, 64)
		if parseErr != nil {
			quote.BlockReason = "a historical order has an invalid payment provider"
			continue
		}
		provider, providerErr := client.PaymentProviderInstance.Query().Where(
			paymentproviderinstance.IDEQ(instanceID),
			paymentproviderinstance.RefundEnabledEQ(true),
			paymentproviderinstance.AllowUserRefundEQ(true),
		).Only(ctx)
		if providerErr != nil || provider == nil {
			quote.BlockReason = "an original payment provider does not allow user refunds"
			continue
		}
		credit := decimal.NewFromFloat(order.Amount).Add(decimal.NewFromFloat(bonusRemaining))
		refundCredit := decimal.NewFromFloat(order.Amount)
		if hasActualBonus {
			if order.RechargeBonusRate <= 0 {
				quote.BlockReason = "a recharge bonus order has no valid bonus ratio snapshot"
			}
			refundCredit = credit.Div(decimal.NewFromInt(1).Add(decimal.NewFromFloat(order.RechargeBonusRate)))
		}
		if refundCredit.GreaterThan(decimal.NewFromFloat(order.Amount)) {
			refundCredit = decimal.NewFromFloat(order.Amount)
		}
		currency := PaymentOrderCurrency(order)
		currencies[currency] = struct{}{}
		gateway := calculateGatewayRefundAmount(order.Amount, order.PayAmount, refundCredit.InexactFloat64(), currency)
		completedAt := order.CreatedAt
		if order.CompletedAt != nil {
			completedAt = *order.CompletedAt
		}
		quote.Orders = append(quote.Orders, AccountRefundOrder{
			OrderID: order.ID, CompletedAt: completedAt, PaymentType: order.PaymentType,
			ProviderInstance: *order.ProviderInstanceID, Currency: currency,
			OriginalCredit: normalizeRefundFloat(order.Amount), OriginalPaid: normalizeRefundFloat(order.PayAmount),
			BonusRate: normalizeRefundFloat(order.RechargeBonusRate), BonusInitial: normalizeRefundFloat(bonusInitial), BonusRemaining: normalizeRefundFloat(bonusRemaining),
			EligibleCredit: normalizeRefundFloat(credit.InexactFloat64()), RefundCredit: normalizeRefundFloat(refundCredit.InexactFloat64()),
			GatewayRefund: gateway, Allocation: "exact",
		})
		quote.EligibleCreditTotal += credit.InexactFloat64()
		quote.RefundCreditTotal += refundCredit.InexactFloat64()
		quote.GatewayTotals[currency] += gateway
	}
	if len(currencies) > 1 {
		quote.BlockReason = "multiple payment currencies require manual refund allocation"
	}

	// 打赏不依赖原支付通道，仅按账户池总额和历史充值/赠额比例计算用户放弃的退款权益。
	donationDenominator := totalOriginal.Add(totalBonusInitial)
	donationPool := decimal.NewFromFloat(account.Balance).Add(decimal.NewFromFloat(quote.RechargeBonusBalance))
	donationAmount := decimal.Zero
	if donationDenominator.GreaterThan(decimal.Zero) {
		donationAmount = donationPool.Mul(totalOriginal).Div(donationDenominator)
	}
	if donationAmount.GreaterThan(totalOriginal) {
		donationAmount = totalOriginal
	}
	if decimal.NewFromFloat(account.Balance).LessThan(decimal.Zero) || decimal.NewFromFloat(account.Balance).GreaterThan(totalOriginal) {
		donationBlockReason = "permanent balance cannot be reconciled to completed recharge orders"
	}
	quote.DonationAmount = normalizeRefundFloat(donationAmount.InexactFloat64())
	if donationBlockReason == "" && quote.DonationAmount > accountRefundTolerance {
		quote.DonationEligible = true
	} else {
		if donationBlockReason == "" {
			donationBlockReason = "no refundable amount remains to donate"
		}
		quote.DonationBlockReason = donationBlockReason
	}

	// 任一永久额度或充值赠额发生消耗后，订单归属已不可证明，改用账户池权威总额和确定性路由分摊。
	permanentConsumed := totalOriginal.Sub(decimal.NewFromFloat(account.Balance)).Abs().GreaterThan(decimal.NewFromFloat(accountRefundTolerance))
	bonusConsumed := totalBonusInitial.Sub(decimal.NewFromFloat(quote.RechargeBonusBalance)).GreaterThan(decimal.NewFromFloat(accountRefundTolerance))
	if quote.BlockReason == "" && (permanentConsumed || bonusConsumed) {
		if decimal.NewFromFloat(account.Balance).LessThan(decimal.Zero) || decimal.NewFromFloat(account.Balance).GreaterThan(totalOriginal) {
			quote.BlockReason = "permanent balance cannot be reconciled to completed recharge orders"
		} else {
			quote.AllocationConfidence = "inferred"
			remainingPool := decimal.NewFromFloat(account.Balance).Add(decimal.NewFromFloat(quote.RechargeBonusBalance))
			denominator := totalOriginal.Add(totalBonusInitial)
			refundTotal := decimal.Zero
			if denominator.GreaterThan(decimal.Zero) {
				refundTotal = remainingPool.Mul(totalOriginal).Div(denominator)
			}
			if refundTotal.GreaterThan(totalOriginal) {
				refundTotal = totalOriginal
			}
			applyInferredAccountRefundAllocation(quote, refundTotal)
		}
	}
	for currency, amount := range quote.GatewayTotals {
		quote.GatewayTotals[currency] = normalizeRefundFloat(amount)
	}
	quote.RechargeBonusBalance = normalizeRefundFloat(quote.RechargeBonusBalance)
	quote.OtherLimitedToClear = normalizeRefundFloat(quote.OtherLimitedToClear)
	quote.EligibleCreditTotal = normalizeRefundFloat(quote.EligibleCreditTotal)
	quote.RefundCreditTotal = normalizeRefundFloat(quote.RefundCreditTotal)

	if quote.BlockReason == "" && totalOriginal.Sub(decimal.NewFromFloat(account.TotalRecharged)).Abs().GreaterThan(decimal.NewFromFloat(accountRefundTolerance)) {
		quote.BlockReason = "cumulative recharge total cannot be reconciled to completed recharge orders"
	}
	if quote.BlockReason == "" && quote.RefundCreditTotal <= accountRefundTolerance {
		quote.BlockReason = "no refundable amount remains"
	}
	if quote.BlockReason == "" {
		quote.Eligible = true
		quote.TotalConfidence = "reconciled"
	}
	quote.QuoteHash = accountRefundQuoteHash(quote)
	return quote, nil
}

// applyInferredAccountRefundAllocation 先固定账户池总额，再按历史订单权重分配，分摊不会反向改变总额。
func applyInferredAccountRefundAllocation(quote *AccountRefundQuote, refundTotal decimal.Decimal) {
	if quote == nil || len(quote.Orders) == 0 {
		return
	}
	creditWeights := make([]decimal.Decimal, len(quote.Orders))
	gatewayWeights := make([]decimal.Decimal, len(quote.Orders))
	for i := range quote.Orders {
		creditWeights[i] = decimal.NewFromFloat(quote.Orders[i].OriginalCredit)
		gatewayWeights[i] = decimal.NewFromFloat(quote.Orders[i].OriginalPaid)
	}
	creditUnits := allocateRefundUnits(refundTotal, creditWeights, 8)
	eligibleUnits := allocateRefundUnits(decimal.NewFromFloat(quote.PermanentBalance+quote.RechargeBonusBalance), creditWeights, 8)
	currency := quote.Orders[0].Currency
	totalOriginal := decimal.Zero
	totalPaid := decimal.Zero
	for i := range creditWeights {
		totalOriginal = totalOriginal.Add(creditWeights[i])
		totalPaid = totalPaid.Add(gatewayWeights[i])
	}
	gatewayTotal := decimal.Zero
	if totalOriginal.GreaterThan(decimal.Zero) {
		gatewayTotal = totalPaid.Mul(refundTotal).Div(totalOriginal).Round(int32(payment.CurrencyMaxFractionDigits(currency)))
	}
	gatewayUnits := allocateRefundUnits(gatewayTotal, gatewayWeights, payment.CurrencyMaxFractionDigits(currency))
	quote.GatewayTotals = map[string]float64{currency: gatewayTotal.InexactFloat64()}
	quote.EligibleCreditTotal = normalizeRefundFloat(quote.PermanentBalance + quote.RechargeBonusBalance)
	quote.RefundCreditTotal = normalizeRefundFloat(refundTotal.InexactFloat64())
	for i := range quote.Orders {
		quote.Orders[i].EligibleCredit = normalizeRefundFloat(decimal.NewFromInt(eligibleUnits[i]).Shift(-8).InexactFloat64())
		quote.Orders[i].RefundCredit = normalizeRefundFloat(decimal.NewFromInt(creditUnits[i]).Shift(-8).InexactFloat64())
		quote.Orders[i].GatewayRefund = decimal.NewFromInt(gatewayUnits[i]).Shift(int32(-payment.CurrencyMaxFractionDigits(currency))).InexactFloat64()
		quote.Orders[i].Allocation = "inferred"
	}
}

// allocateRefundUnits 按权重分配最小单位；余数从末笔开始补齐，结果严格守恒且不超过各自权重上限。
func allocateRefundUnits(total decimal.Decimal, weights []decimal.Decimal, fractionDigits int) []int64 {
	allocated := make([]int64, len(weights))
	if len(weights) == 0 || !total.GreaterThan(decimal.Zero) {
		return allocated
	}
	factor := decimal.New(1, int32(fractionDigits))
	totalUnits := total.Mul(factor).Round(0).IntPart()
	weightUnits := make([]int64, len(weights))
	weightTotal := int64(0)
	for i, weight := range weights {
		weightUnits[i] = weight.Mul(factor).Round(0).IntPart()
		weightTotal += weightUnits[i]
	}
	if weightTotal <= 0 || totalUnits <= 0 {
		return allocated
	}
	if totalUnits > weightTotal {
		totalUnits = weightTotal
	}
	used := int64(0)
	for i, capacity := range weightUnits {
		share := decimal.NewFromInt(totalUnits).Mul(decimal.NewFromInt(capacity)).Div(decimal.NewFromInt(weightTotal)).Floor().IntPart()
		if share > capacity {
			share = capacity
		}
		allocated[i] = share
		used += share
	}
	for remaining := totalUnits - used; remaining > 0; remaining-- {
		for i := len(allocated) - 1; i >= 0; i-- {
			if allocated[i] < weightUnits[i] {
				allocated[i]++
				break
			}
		}
	}
	return allocated
}

func (s *PaymentService) refreshAccountRefundDrain(ctx context.Context, record *AccountRefundRecord) (*AccountRefundRecord, error) {
	if record == nil || record.State != AccountRefundStateDraining {
		return record, nil
	}
	account, err := s.userRepo.GetByID(ctx, record.UserID)
	if err != nil {
		return nil, err
	}
	if account.Status != StatusRefundLocked {
		return nil, infraerrors.Conflict("REFUND_LOCK_LOST", "refund no longer owns the account lock")
	}
	if account.FrozenBalance > accountRefundTolerance {
		record.Message = "waiting for frozen permanent balance to settle"
		return record, nil
	}
	frozenCredits, err := s.entClient.UserLimitedCreditGrant.Query().Where(creditgrant.UserIDEQ(record.UserID), creditgrant.FrozenAmountGT(accountRefundTolerance)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if frozenCredits {
		record.Message = "waiting for frozen limited credits to settle"
		return record, nil
	}
	if reader, ok := any(s.concurrencyCache).(accountRefundDrainReader); ok && reader != nil {
		active, err := reader.GetUserConcurrency(ctx, record.UserID)
		if err != nil {
			return nil, infraerrors.ServiceUnavailable("REFUND_DRAIN_UNAVAILABLE", "cannot verify active API requests")
		}
		if active > 0 {
			record.Message = fmt.Sprintf("waiting for %d active API request(s)", active)
			return record, nil
		}
	}
	if reader, ok := any(s.concurrencyCache).(accountRefundWaitReader); ok && reader != nil {
		waiting, err := reader.GetUserWaitingCount(ctx, record.UserID)
		if err != nil {
			return nil, infraerrors.ServiceUnavailable("REFUND_DRAIN_UNAVAILABLE", "cannot verify waiting API requests")
		}
		if waiting > 0 {
			record.Message = fmt.Sprintf("waiting for %d queued API request(s)", waiting)
			return record, nil
		}
	}
	quote, err := s.buildAccountRefundQuote(ctx, record.UserID)
	if err != nil {
		return nil, err
	}
	if record.DonationRequested && quote.DonationEligible {
		record.State = AccountRefundStateReadyToConfirm
		record.Quote = quote
		record.Message = "billing drained; finalizing donation"
		record.UpdatedAt = time.Now().UTC()
		return s.finalizeAccountRefundDonation(ctx, record)
	}
	if !quote.Eligible {
		record.State = AccountRefundStateManualReview
		record.Message = quote.BlockReason
		record.Quote = quote
		record.UpdatedAt = time.Now().UTC()
		if err := s.writeAccountRefundAudit(ctx, record); err != nil {
			return nil, err
		}
		return record, nil
	}
	record.State = AccountRefundStateReadyToConfirm
	record.Quote = quote
	record.Message = "billing drained; review the final quote"
	record.UpdatedAt = time.Now().UTC()
	if err := s.writeAccountRefundAudit(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

// finalizeAccountRefundDonation 原子校验最终报价、清空余额并写入打赏终态。
func (s *PaymentService) finalizeAccountRefundDonation(ctx context.Context, record *AccountRefundRecord) (*AccountRefundRecord, error) {
	if record == nil || record.Quote == nil {
		return nil, infraerrors.Conflict("REFUND_STATE_INVALID", "refund quote is missing")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin account refund donation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)

	userQuery := tx.User.Query().Where(user.IDEQ(record.UserID), user.StatusEQ(StatusRefundLocked))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		userQuery = userQuery.ForUpdate()
	}
	lockedUser, err := userQuery.Only(txCtx)
	if err != nil {
		return nil, infraerrors.Conflict("REFUND_LOCK_LOST", "refund no longer owns the account lock")
	}
	if lockedUser.FrozenBalance > accountRefundTolerance {
		return nil, infraerrors.Conflict("REFUND_BILLING_DRAINING", "permanent balance is still frozen")
	}

	auditQuery := tx.PaymentAuditLog.Query().Where(
		paymentauditlog.OrderIDEQ(accountRefundAuditKey(record.UserID, record.RefundID)),
	).Order(dbent.Desc(paymentauditlog.FieldCreatedAt), dbent.Desc(paymentauditlog.FieldID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		auditQuery = auditQuery.ForUpdate()
	}
	latestRow, err := auditQuery.First(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock account refund donation state: %w", err)
	}
	latest, err := parseAccountRefundRecord(latestRow)
	if err != nil {
		return nil, err
	}
	if latest.State != AccountRefundStateReadyToConfirm && latest.State != AccountRefundStateDraining && latest.State != AccountRefundStateManualReview {
		return nil, infraerrors.Conflict("REFUND_NOT_READY_TO_DONATE", "refund donation was already completed or is not ready")
	}

	inFlightQuery := tx.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(record.UserID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		accountRefundInFlightOrderPredicate(),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		inFlightQuery = inFlightQuery.ForUpdate()
	}
	inFlightOrders, err := inFlightQuery.All(txCtx)
	if err != nil {
		return nil, fmt.Errorf("check donation in-flight recharge orders: %w", err)
	}
	if len(inFlightOrders) > 0 {
		return nil, infraerrors.Conflict("REFUND_RECHARGE_IN_FLIGHT", "a balance recharge is still being processed")
	}
	completedOrderQuery := tx.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(record.UserID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		paymentorder.StatusEQ(OrderStatusCompleted),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		completedOrderQuery = completedOrderQuery.ForUpdate()
	}
	if _, err := completedOrderQuery.All(txCtx); err != nil {
		return nil, fmt.Errorf("lock donation recharge orders: %w", err)
	}

	grantQuery := tx.UserLimitedCreditGrant.Query().Where(creditgrant.UserIDEQ(record.UserID), creditgrant.StatusEQ(LimitedCreditStatusActive))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		grantQuery = grantQuery.ForUpdate()
	}
	grants, err := grantQuery.All(txCtx)
	if err != nil {
		return nil, err
	}
	for _, grant := range grants {
		if grant.FrozenAmount > accountRefundTolerance {
			return nil, infraerrors.Conflict("REFUND_BILLING_DRAINING", "limited credit is still frozen")
		}
	}
	account := &User{ID: lockedUser.ID, Balance: lockedUser.Balance, FrozenBalance: lockedUser.FrozenBalance, TotalRecharged: lockedUser.TotalRecharged, Status: lockedUser.Status}
	currentQuote, err := s.buildAccountRefundQuoteWithClient(txCtx, tx.Client(), account)
	if err != nil {
		return nil, err
	}
	if !currentQuote.DonationEligible || currentQuote.QuoteHash != record.Quote.QuoteHash {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "balance changed after confirmation; review it again")
	}

	for _, grant := range grants {
		remaining := math.Max(0, grant.InitialAmount-grant.UsedAmount)
		if _, err := tx.UserLimitedCreditGrant.UpdateOne(grant).SetUsedAmount(grant.InitialAmount).SetFrozenAmount(0).SetStatus(LimitedCreditStatusDepleted).Save(txCtx); err != nil {
			return nil, err
		}
		if remaining > accountRefundTolerance {
			if _, err := tx.UserLimitedCreditLedger.Create().SetUserID(record.UserID).SetGrantID(grant.ID).SetEventType("refund_donation_clear").SetAmount(remaining).SetNotes("放弃退款并打赏站长清空").Save(txCtx); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.User.UpdateOne(lockedUser).SetBalance(0).Save(txCtx); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	latest.State = AccountRefundStateDonated
	latest.Quote = currentQuote
	latest.DonationRequested = true
	latest.Donation = &AccountRefundDonation{
		Username:    accountRefundDonationUsername(lockedUser.Username),
		MaskedEmail: maskAccountRefundDonationEmail(lockedUser.Email),
		Amount:      normalizeRefundFloat(currentQuote.DonationAmount),
		DonatedAt:   now,
	}
	latest.Message = "refund waived and donated"
	latest.UpdatedAt = now
	if err := writeAccountRefundDonationAudit(txCtx, tx.Client(), latest); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit account refund donation: %w", err)
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, record.UserID)
	}
	return latest, nil
}

// ListAccountRefundDonations 返回公开打赏名单，数据来自不可变终态快照。
func (s *PaymentService) ListAccountRefundDonations(ctx context.Context) ([]AccountRefundDonation, error) {
	rows, err := s.entClient.PaymentAuditLog.Query().Where(
		paymentauditlog.ActionEQ(accountRefundDonationAction),
	).Order(dbent.Desc(paymentauditlog.FieldCreatedAt), dbent.Desc(paymentauditlog.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	donations := make([]AccountRefundDonation, 0, len(rows))
	for _, row := range rows {
		record, parseErr := parseAccountRefundRecord(row)
		if parseErr != nil || record.State != AccountRefundStateDonated || record.Donation == nil {
			continue
		}
		donations = append(donations, *record.Donation)
	}
	return donations, nil
}

func (s *PaymentService) finalizeAccountRefundCredits(ctx context.Context, record *AccountRefundRecord) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	userQuery := tx.User.Query().Where(user.IDEQ(record.UserID), user.StatusEQ(StatusRefundLocked))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		userQuery = userQuery.ForUpdate()
	}
	lockedUser, err := userQuery.Only(txCtx)
	if err != nil {
		return err
	}
	if lockedUser.FrozenBalance > accountRefundTolerance {
		return infraerrors.Conflict("REFUND_BILLING_DRAINING", "permanent balance is still frozen")
	}
	if record.Quote == nil || math.Abs(lockedUser.Balance-record.Quote.PermanentBalance) > accountRefundTolerance {
		return infraerrors.Conflict("REFUND_QUOTE_CHANGED", "permanent balance changed after gateway submission; manual reconciliation is required")
	}
	inFlightQuery := tx.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(record.UserID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		accountRefundInFlightOrderPredicate(),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		inFlightQuery = inFlightQuery.ForUpdate()
	}
	inFlightOrders, err := inFlightQuery.All(txCtx)
	if err != nil {
		return fmt.Errorf("check final in-flight recharge orders: %w", err)
	}
	if len(inFlightOrders) > 0 {
		return infraerrors.Conflict("REFUND_RECHARGE_IN_FLIGHT", "a paid recharge arrived after refund submission; manual reconciliation is required")
	}
	grantQuery := tx.UserLimitedCreditGrant.Query().Where(creditgrant.UserIDEQ(record.UserID), creditgrant.StatusEQ(LimitedCreditStatusActive))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		grantQuery = grantQuery.ForUpdate()
	}
	grants, err := grantQuery.All(txCtx)
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if grant.FrozenAmount > accountRefundTolerance {
			return infraerrors.Conflict("REFUND_BILLING_DRAINING", "limited credit is still frozen")
		}
		remaining := math.Max(0, grant.InitialAmount-grant.UsedAmount)
		if _, err := tx.UserLimitedCreditGrant.UpdateOne(grant).SetUsedAmount(grant.InitialAmount).SetFrozenAmount(0).SetStatus(LimitedCreditStatusDepleted).Save(txCtx); err != nil {
			return err
		}
		if remaining > accountRefundTolerance {
			note := "账户余额清退清空"
			if _, err := tx.UserLimitedCreditLedger.Create().SetUserID(record.UserID).SetGrantID(grant.ID).SetEventType("refund_clear").SetAmount(remaining).SetNotes(note).Save(txCtx); err != nil {
				return err
			}
		}
	}
	if _, err := tx.User.UpdateOne(lockedUser).SetBalance(0).Save(txCtx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PaymentService) markAccountRefundOrderPending(ctx context.Context, plan *RefundPlan, resp *payment.RefundResponse) error {
	_, err := s.entClient.PaymentOrder.UpdateOneID(plan.OrderID).SetStatus(OrderStatusRefundPending).SetRefundAmount(plan.RefundAmount).SetRefundReason(plan.Reason).SetForceRefund(true).Save(ctx)
	if err == nil {
		s.writeAuditLog(ctx, plan.OrderID, "ACCOUNT_REFUND_PENDING", "user", map[string]any{"refundID": refundResponseID(resp), "refundAmount": plan.RefundAmount})
	}
	return err
}

// reconcileAccountRefundPending 只查询已提交路由，成功后继续剩余路由而不重复退款。
func (s *PaymentService) reconcileAccountRefundPending(ctx context.Context, record *AccountRefundRecord) (*AccountRefundRecord, error) {
	if record == nil || record.Quote == nil {
		return nil, infraerrors.Conflict("REFUND_STATE_INVALID", "refund quote is missing")
	}
	for i := range record.Quote.Orders {
		route := &record.Quote.Orders[i]
		if route.GatewayStatus != payment.ProviderStatusPending {
			continue
		}
		order, err := s.entClient.PaymentOrder.Get(ctx, route.OrderID)
		if err != nil {
			return nil, err
		}
		provider, err := s.getRefundProvider(ctx, order)
		if err != nil {
			return s.failAccountRefund(ctx, record, completedAccountRefundRoutes(record), err.Error())
		}
		queryProvider, ok := provider.(payment.RefundQueryProvider)
		if !ok {
			record.Message = "payment provider requires manual refund reconciliation"
			record.UpdatedAt = time.Now().UTC()
			if err := s.writeAccountRefundAudit(ctx, record); err != nil {
				return nil, err
			}
			return record, nil
		}
		resp, err := queryProvider.QueryRefund(ctx, payment.RefundQueryRequest{
			TradeNo: order.PaymentTradeNo, OrderID: order.OutTradeNo, RefundID: route.GatewayRefundID,
			Amount: formatGatewayRefundAmount(route.GatewayRefund, order),
		})
		if err != nil {
			record.Message = "refund status query failed: " + err.Error()
			record.UpdatedAt = time.Now().UTC()
			if auditErr := s.writeAccountRefundAudit(ctx, record); auditErr != nil {
				return nil, auditErr
			}
			return record, nil
		}
		if err := validateRefundProviderResponse(resp); err != nil {
			route.GatewayError = err.Error()
			return s.failAccountRefund(ctx, record, completedAccountRefundRoutes(record), err.Error())
		}
		route.GatewayStatus = strings.TrimSpace(resp.Status)
		if id := refundResponseID(resp); id != "" {
			route.GatewayRefundID = id
		}
		if route.GatewayStatus == payment.ProviderStatusPending {
			record.Message = "gateway refund is pending confirmation"
			record.UpdatedAt = time.Now().UTC()
			if err := s.writeAccountRefundAudit(ctx, record); err != nil {
				return nil, err
			}
			return record, nil
		}
		plan := &RefundPlan{OrderID: order.ID, Order: order, RequestID: accountRefundRouteRequestID(record.RefundID, order.ID), RefundAmount: route.RefundCredit, GatewayAmount: route.GatewayRefund, Reason: accountRefundReason, Force: true}
		if _, err := s.markRefundOk(ctx, plan); err != nil {
			route.GatewayError = err.Error()
			return s.failAccountRefund(ctx, record, completedAccountRefundRoutes(record)+1, err.Error())
		}
	}
	record.State = AccountRefundStateSubmitting
	record.UpdatedAt = time.Now().UTC()
	if err := s.writeAccountRefundAudit(ctx, record); err != nil {
		return nil, err
	}
	return s.executeAccountRefundRoutes(ctx, record)
}

func (s *PaymentService) failAccountRefund(ctx context.Context, record *AccountRefundRecord, succeeded int, message string) (*AccountRefundRecord, error) {
	if succeeded > 0 {
		record.State = AccountRefundStatePartialExternalSuccess
	} else {
		record.State = AccountRefundStateFailed
	}
	record.Message = message
	record.UpdatedAt = time.Now().UTC()
	if err := s.writeAccountRefundAudit(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *PaymentService) latestAccountRefundForUser(ctx context.Context, userID int64) (*AccountRefundRecord, error) {
	prefix := accountRefundAuditPrefix + strconv.FormatInt(userID, 10) + ":"
	row, err := s.entClient.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDHasPrefix(prefix)).Order(dbent.Desc(paymentauditlog.FieldCreatedAt), dbent.Desc(paymentauditlog.FieldID)).First(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseAccountRefundRecord(row)
}

func (s *PaymentService) latestAccountRefund(ctx context.Context, refundID string, userID int64) (*AccountRefundRecord, error) {
	key := accountRefundAuditKey(userID, refundID)
	row, err := s.entClient.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(key)).Order(dbent.Desc(paymentauditlog.FieldCreatedAt), dbent.Desc(paymentauditlog.FieldID)).First(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseAccountRefundRecord(row)
}

func parseAccountRefundRecord(row *dbent.PaymentAuditLog) (*AccountRefundRecord, error) {
	var record AccountRefundRecord
	if err := json.Unmarshal([]byte(row.Detail), &record); err != nil {
		return nil, fmt.Errorf("parse account refund audit: %w", err)
	}
	return &record, nil
}

func (s *PaymentService) writeAccountRefundAudit(ctx context.Context, record *AccountRefundRecord) error {
	return writeAccountRefundAudit(ctx, s.entClient, record)
}

func writeAccountRefundAudit(ctx context.Context, client *dbent.Client, record *AccountRefundRecord) error {
	detail, err := json.Marshal(record)
	if err != nil {
		return err
	}
	action := "ACCOUNT_REFUND_EVENT_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	_, err = client.PaymentAuditLog.Create().SetOrderID(accountRefundAuditKey(record.UserID, record.RefundID)).SetAction(action).SetDetail(string(detail)).SetOperator("user:" + strconv.FormatInt(record.UserID, 10)).Save(ctx)
	if err != nil {
		return fmt.Errorf("write account refund audit: %w", err)
	}
	return nil
}

func writeAccountRefundDonationAudit(ctx context.Context, client *dbent.Client, record *AccountRefundRecord) error {
	detail, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(accountRefundAuditKey(record.UserID, record.RefundID)).
		SetAction(accountRefundDonationAction).
		SetDetail(string(detail)).
		SetOperator("user:" + strconv.FormatInt(record.UserID, 10)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("write account refund donation audit: %w", err)
	}
	return nil
}

func accountRefundDonationUsername(username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return "匿名用户"
	}
	return username
}

func maskAccountRefundDonationEmail(email string) string {
	email = strings.TrimSpace(email)
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "***"
	}
	local := []rune(parts[0])
	return string(local[0]) + "***@" + parts[1]
}

func accountRefundAuditKey(userID int64, refundID string) string {
	return accountRefundAuditPrefix + strconv.FormatInt(userID, 10) + ":" + strings.TrimSpace(refundID)
}

func accountRefundRouteRequestID(refundID string, orderID int64) string {
	return "account-refund-" + strings.TrimSpace(refundID) + "-" + strconv.FormatInt(orderID, 10)
}

func newAccountRefundID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func accountRefundQuoteHash(quote *AccountRefundQuote) string {
	clone := *quote
	clone.QuoteHash = ""
	data, _ := json.Marshal(clone)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeRefundFloat(value float64) float64 {
	return math.Round(value*100000000) / 100000000
}

func accountRefundTerminal(state string) bool {
	switch state {
	case AccountRefundStateSucceeded, AccountRefundStateFailed, AccountRefundStatePartialExternalSuccess, AccountRefundStateCanceled, AccountRefundStateManualReview, AccountRefundStateDonated:
		return true
	default:
		return false
	}
}

func completedAccountRefundRoutes(record *AccountRefundRecord) int {
	if record == nil || record.Quote == nil {
		return 0
	}
	count := 0
	for _, route := range record.Quote.Orders {
		if route.GatewayStatus == payment.ProviderStatusSuccess || route.GatewayStatus == payment.ProviderStatusRefunded {
			count++
		}
	}
	return count
}
