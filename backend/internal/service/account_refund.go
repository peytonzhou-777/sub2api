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
	"github.com/Wei-Shaw/sub2api/ent/securitydepositaccount"
	"github.com/Wei-Shaw/sub2api/ent/securitydepositledger"
	"github.com/Wei-Shaw/sub2api/ent/securitydepositlot"
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
	// AccountRefundStateDonated 仅用于兼容历史终态记录，不再提供新增入口。
	AccountRefundStateDonated = "donated"

	accountRefundAuditPrefix    = "account_refund:"
	accountRefundReason         = "account full clearance"
	accountRefundTolerance      = 0.00000001
	accountRefundGatewayUnknown = "unknown"

	AdminAccountRefundOutcomeSucceeded = "succeeded"
	AdminAccountRefundOutcomeFailed    = "failed"

	AccountRefundCalculationVerified     = "verified"
	AccountRefundCalculationManualReview = "manual_review"
	AccountRefundCalculationNone         = "none"

	AccountRefundAdminAutomatic     = "automatic"
	AccountRefundAdminManual        = "manual_external"
	AccountRefundAdminBlocked       = "blocked"
	AccountRefundFailurePreGateway  = "pre_gateway"
	AccountRefundFailureGateway     = "gateway"
	AccountRefundFailurePostGateway = "post_gateway"

	AccountRefundReviewQuoteInconsistent      = "quote_inconsistent"
	AccountRefundReviewGatewayUnknown         = "gateway_unknown"
	AccountRefundReviewProviderUnavailable    = "provider_unavailable"
	AccountRefundReviewGatewayQueryFailed     = "gateway_query_failed"
	AccountRefundReviewManualExternalRequired = "manual_external_required"
	AccountRefundReviewFinalizeFailed         = "finalize_failed"
	AccountRefundReviewAccessRestoreFailed    = "access_restore_failed"
	AccountRefundReviewLegacyUnknown          = "legacy_unknown"
)

func accountRefundInFlightOrderPredicate() predicate.PaymentOrder {
	return paymentorder.Or(
		paymentorder.StatusIn(OrderStatusPending, OrderStatusPaid, OrderStatusRecharging, OrderStatusRefundRequested, OrderStatusRefunding, OrderStatusRefundPending),
		paymentorder.And(paymentorder.StatusEQ(OrderStatusFailed), paymentorder.PaidAtNotNil()),
	)
}

func accountRefundHistoricalOrderPredicate() predicate.PaymentOrder {
	return paymentorder.StatusIn(OrderStatusCompleted, OrderStatusPartiallyRefunded, OrderStatusRefunded)
}

// AccountRefundOrder 展示账户清退中一条只读支付路由。
type AccountRefundOrder struct {
	OrderID            int64     `json:"order_id"`
	CompletedAt        time.Time `json:"completed_at"`
	PaymentType        string    `json:"payment_type"`
	ProviderInstance   string    `json:"provider_instance_id"`
	Currency           string    `json:"currency"`
	OriginalCredit     float64   `json:"original_credit"`
	OriginalPaid       float64   `json:"original_paid"`
	PreviouslyRefunded float64   `json:"previously_refunded"`
	BonusRate          float64   `json:"bonus_rate"`
	BonusInitial       float64   `json:"bonus_initial"`
	BonusRemaining     float64   `json:"bonus_remaining"`
	EligibleCredit     float64   `json:"eligible_credit"`
	RefundCredit       float64   `json:"refund_credit"`
	GatewayRefund      float64   `json:"gateway_refund"`
	Allocation         string    `json:"allocation_confidence"`
	GatewayStatus      string    `json:"gateway_status,omitempty"`
	GatewayRefundID    string    `json:"gateway_refund_id,omitempty"`
	GatewayError       string    `json:"gateway_error,omitempty"`
}

// AccountRefundQuote 是账户级权威试算结果；不同币种始终独立汇总。
type AccountRefundQuote struct {
	Eligible             bool                 `json:"eligible"`
	CalculationStatus    string               `json:"calculation_status"`
	SelfServiceEligible  bool                 `json:"self_service_eligible"`
	AdminExecutionMode   string               `json:"admin_execution_mode"`
	ReviewReasonCode     string               `json:"review_reason_code,omitempty"`
	BlockReason          string               `json:"block_reason,omitempty"`
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
	RefundID            string                        `json:"refund_id"`
	UserID              int64                         `json:"user_id"`
	State               string                        `json:"state"`
	PreviousUserStatus  string                        `json:"previous_user_status"`
	Quote               *AccountRefundQuote           `json:"quote,omitempty"`
	CreatedAt           time.Time                     `json:"created_at"`
	UpdatedAt           time.Time                     `json:"updated_at"`
	Message             string                        `json:"message,omitempty"`
	SessionToken        string                        `json:"session_token,omitempty"`
	StateRevision       int64                         `json:"state_revision"`
	ReviewReasonCode    string                        `json:"review_reason_code,omitempty"`
	FailureStage        string                        `json:"failure_stage,omitempty"`
	AdminInitiated      bool                          `json:"admin_initiated,omitempty"`
	StartIdempotencyKey string                        `json:"start_idempotency_key,omitempty"`
	Actor               *AccountRefundActor           `json:"actor,omitempty"`
	Reconciliations     []AccountRefundReconciliation `json:"reconciliations,omitempty"`
}

// AccountRefundActor 记录每次清退状态变更的真实操作人。
type AccountRefundActor struct {
	Type      string `json:"actor_type"`
	ID        int64  `json:"actor_id,omitempty"`
	Label     string `json:"actor_label,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// AccountRefundReconciliation 保存管理员逐订单人工核验的不可变依据。
type AccountRefundReconciliation struct {
	OrderID          int64               `json:"order_id"`
	Outcome          string              `json:"outcome"`
	ExternalRefundID string              `json:"external_refund_id,omitempty"`
	VerifiedAt       time.Time           `json:"verified_at"`
	Evidence         string              `json:"evidence"`
	Note             string              `json:"note"`
	Actor            *AccountRefundActor `json:"actor,omitempty"`
}

// AdminAccountRefundReconcileInput 是管理员核验一条未知网关退款的结果。
type AdminAccountRefundReconcileInput struct {
	OrderID               int64      `json:"order_id"`
	Outcome               string     `json:"outcome"`
	ExternalRefundID      string     `json:"external_refund_id,omitempty"`
	VerifiedAt            *time.Time `json:"verified_at,omitempty"`
	Evidence              string     `json:"evidence"`
	Note                  string     `json:"note"`
	ExpectedStateRevision int64      `json:"expected_state_revision"`
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
	if accountRefundTerminal(record.State) {
		if err := s.restoreTerminalAccountRefundAccess(ctx, record); err != nil {
			return nil, err
		}
	} else {
		if fence, ok := s.authCacheInvalidator.(RefundBillingFence); ok {
			if err := fence.AcquireRefundBillingLock(ctx, account.ID, record.RefundID); err != nil {
				return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "cannot renew refund billing fence")
			}
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
	} else if current != nil {
		if !accountRefundTerminal(current.State) {
			return s.advanceAccountRefund(ctx, current)
		}
		if err := s.restoreTerminalAccountRefundAccess(ctx, current); err != nil {
			return nil, err
		}
	}
	quote, err := s.buildAccountRefundQuote(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &AccountRefundRecord{UserID: userID, State: "estimate", Quote: quote, UpdatedAt: time.Now().UTC()}, nil
}

// LockAccountRefund 原子锁定用户，并为二次确认签发退款专用会话。
func (s *PaymentService) LockAccountRefund(ctx context.Context, userID int64, quoteHash string) (*AccountRefundRecord, error) {
	return s.lockAccountRefund(ctx, userID, quoteHash)
}

func (s *PaymentService) lockAccountRefund(ctx context.Context, userID int64, quoteHash string) (*AccountRefundRecord, error) {
	return s.lockAccountRefundWithOptions(ctx, userID, quoteHash, accountRefundLockOptions{IssueSession: true, Actor: &AccountRefundActor{Type: "user", ID: userID, Label: "user:" + strconv.FormatInt(userID, 10)}})
}

type accountRefundLockOptions struct {
	AdminInitiated        bool
	IssueSession          bool
	AllowDisabled         bool
	ExpectedStateRevision int64
	IdempotencyKey        string
	Actor                 *AccountRefundActor
}

func (s *PaymentService) lockAccountRefundWithOptions(ctx context.Context, userID int64, quoteHash string, options accountRefundLockOptions) (*AccountRefundRecord, error) {
	quote, err := s.buildAccountRefundQuote(ctx, userID)
	if err != nil {
		return nil, err
	}
	adminQuoteExecutable := options.AdminInitiated && quote.CalculationStatus == AccountRefundCalculationVerified && quote.AdminExecutionMode != AccountRefundAdminBlocked
	if !quote.Eligible && !adminQuoteExecutable {
		return nil, infraerrors.Conflict("REFUND_MANUAL_REVIEW", quote.BlockReason)
	}
	if strings.TrimSpace(quoteHash) == "" || quoteHash != quote.QuoteHash {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "refund quote changed; review it again")
	}
	if current, err := s.latestAccountRefundForUser(ctx, userID); err != nil {
		return nil, err
	} else if current != nil {
		if options.AdminInitiated && options.IdempotencyKey != "" && current.StartIdempotencyKey == options.IdempotencyKey && current.Actor != nil && current.Actor.ID == options.Actor.ID {
			return current, nil
		}
		if !accountRefundTerminal(current.State) {
			return nil, infraerrors.Conflict("REFUND_ALREADY_ACTIVE", "an account refund is already active")
		}
		if options.ExpectedStateRevision != current.StateRevision {
			return nil, infraerrors.Conflict("REFUND_STATE_CHANGED", "account refund state changed; refresh and retry")
		}
	} else if options.ExpectedStateRevision != 0 {
		return nil, infraerrors.Conflict("REFUND_STATE_CHANGED", "account refund state changed; refresh and retry")
	}

	refundID, err := newAccountRefundID()
	if err != nil {
		return nil, fmt.Errorf("generate refund id: %w", err)
	}
	token := ""
	if options.IssueSession {
		token, err = s.paymentResume().CreateAccountRefundSessionToken(refundID, userID)
		if err != nil {
			return nil, err
		}
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
	record := &AccountRefundRecord{RefundID: refundID, UserID: userID, State: AccountRefundStateDraining, Quote: quote, CreatedAt: now, UpdatedAt: now, AdminInitiated: options.AdminInitiated, StartIdempotencyKey: options.IdempotencyKey, Actor: options.Actor}
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
	if options.AdminInitiated {
		prefix := accountRefundAuditPrefix + strconv.FormatInt(userID, 10) + ":"
		auditQuery := tx.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDHasPrefix(prefix)).Order(dbent.Desc(paymentauditlog.FieldCreatedAt), dbent.Desc(paymentauditlog.FieldID))
		if tx.Client().Driver().Dialect() == dialect.Postgres {
			auditQuery = auditQuery.ForUpdate()
		}
		latestRow, auditErr := auditQuery.First(txCtx)
		if dbent.IsNotFound(auditErr) {
			if options.ExpectedStateRevision != 0 {
				return nil, infraerrors.Conflict("REFUND_STATE_CHANGED", "account refund state changed; refresh and retry")
			}
		} else if auditErr != nil {
			return nil, fmt.Errorf("lock latest account refund state: %w", auditErr)
		} else {
			latest, parseErr := parseAccountRefundRecord(latestRow)
			if parseErr != nil {
				return nil, parseErr
			}
			if options.IdempotencyKey != "" && latest.StartIdempotencyKey == options.IdempotencyKey && latest.Actor != nil && options.Actor != nil && latest.Actor.ID == options.Actor.ID {
				return latest, nil
			}
			if !accountRefundTerminal(latest.State) {
				return nil, infraerrors.Conflict("REFUND_ALREADY_ACTIVE", "an account refund is already active")
			}
			if latestRow.ID != options.ExpectedStateRevision {
				return nil, infraerrors.Conflict("REFUND_STATE_CHANGED", "account refund state changed; refresh and retry")
			}
		}
	}
	allowedStatus := lockedUser.Status == StatusActive || options.AllowDisabled && lockedUser.Status == StatusDisabled
	if !allowedStatus {
		return nil, infraerrors.Conflict("USER_INACTIVE", "only an active or disabled account can start a refund")
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
	record, err := s.latestAccountRefund(ctx, refundID, userID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, infraerrors.NotFound("REFUND_NOT_FOUND", "account refund not found")
	}
	// 已终结流程只读历史快照，避免完成或取消后的用户被重新加上计费锁。
	if accountRefundTerminal(record.State) {
		if err := s.restoreTerminalAccountRefundAccess(ctx, record); err != nil {
			return nil, err
		}
		return record, nil
	}
	if fence, ok := s.authCacheInvalidator.(RefundBillingFence); ok {
		if err := fence.AcquireRefundBillingLock(ctx, userID, refundID); err != nil {
			return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "cannot renew refund billing fence")
		}
	}
	return s.advanceAccountRefund(ctx, record)
}

func (s *PaymentService) advanceAccountRefund(ctx context.Context, record *AccountRefundRecord) (*AccountRefundRecord, error) {
	if record == nil {
		return nil, nil
	}
	switch record.State {
	case AccountRefundStateDraining:
		return s.refreshAccountRefundDrain(ctx, record)
	case AccountRefundStatePending, AccountRefundStateSubmitting:
		return s.reconcileAccountRefundPending(ctx, record)
	default:
		return record, nil
	}
}

// ConfirmAccountRefund 执行账户级余额清退；已成功的支付路由不会在本次循环内重试。
func (s *PaymentService) ConfirmAccountRefund(ctx context.Context, refundID string, userID int64, quoteHash string) (*AccountRefundRecord, error) {
	record, err := s.GetAccountRefund(ctx, refundID, userID)
	if err != nil {
		return nil, err
	}
	if record.State != AccountRefundStateReadyToConfirm && record.State != AccountRefundStateFailed && record.State != AccountRefundStatePartialExternalSuccess {
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

// claimAccountRefundSubmission 在同一事务锁定资金输入与最新状态，只有一个确认请求能进入网关阶段。
func (s *PaymentService) claimAccountRefundSubmission(ctx context.Context, record *AccountRefundRecord) (*AccountRefundRecord, error) {
	actor := record.Actor
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
	historicalOrderQuery := tx.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(record.UserID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		accountRefundHistoricalOrderPredicate(),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		historicalOrderQuery = historicalOrderQuery.ForUpdate()
	}
	if _, err = historicalOrderQuery.All(txCtx); err != nil {
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
	if record.StateRevision != 0 && latestRow.ID != record.StateRevision {
		return nil, infraerrors.Conflict("REFUND_STATE_CHANGED", "account refund state changed; refresh and retry")
	}
	latest.Actor = actor
	resumeSubmission := latest.State == AccountRefundStateFailed || latest.State == AccountRefundStatePartialExternalSuccess
	if latest.State != AccountRefundStateReadyToConfirm && !resumeSubmission {
		return nil, infraerrors.Conflict("REFUND_NOT_READY_TO_CONFIRM", "refund confirmation was already claimed or is not ready")
	}
	account := &User{ID: lockedUser.ID, Balance: lockedUser.Balance, FrozenBalance: lockedUser.FrozenBalance, TotalRecharged: lockedUser.TotalRecharged, Status: lockedUser.Status}
	if latest.Quote == nil || math.Abs(account.Balance-latest.Quote.PermanentBalance) > accountRefundTolerance {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "balance changed after lock; manual review is required")
	}
	if !resumeSubmission {
		currentQuote, quoteErr := s.buildAccountRefundQuoteWithClient(txCtx, tx.Client(), account)
		if quoteErr != nil {
			return nil, quoteErr
		}
		quoteExecutable := currentQuote.Eligible || latest.AdminInitiated && currentQuote.CalculationStatus == AccountRefundCalculationVerified && currentQuote.AdminExecutionMode == AccountRefundAdminAutomatic
		if !quoteExecutable || currentQuote.QuoteHash != latest.Quote.QuoteHash {
			return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "balance changed after lock; manual review is required")
		}
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
			order, err := s.entClient.PaymentOrder.Get(ctx, route.OrderID)
			if err != nil {
				return s.failAccountRefund(ctx, record, succeeded, "load completed refund order: "+err.Error())
			}
			if !accountRefundOrderReachedTarget(order, route) {
				plan := &RefundPlan{OrderID: order.ID, Order: order, RequestID: accountRefundRouteRequestID(record.RefundID, order.ID), RefundAmount: route.RefundCredit, GatewayAmount: route.GatewayRefund, Reason: accountRefundReason, Force: true}
				if err := s.markAccountRefundOrderSucceeded(ctx, route, plan, record.Actor); err != nil {
					return s.failAccountRefund(ctx, record, succeeded+1, "finalize confirmed gateway refund: "+err.Error())
				}
			}
			succeeded++
			continue
		}
		if route.GatewayStatus == payment.ProviderStatusPending || route.GatewayStatus == AccountRefundStateSubmitting || route.GatewayStatus == accountRefundGatewayUnknown {
			return s.reconcileAccountRefundPending(ctx, record)
		}
		if route.RefundCredit <= accountRefundTolerance || route.GatewayRefund <= accountRefundTolerance {
			continue
		}
		order, err := s.entClient.PaymentOrder.Get(ctx, route.OrderID)
		if err != nil {
			route.GatewayError = err.Error()
			return s.failAccountRefund(ctx, record, succeeded, "load refund order: "+err.Error())
		}
		plan := &RefundPlan{OrderID: order.ID, Order: order, RequestID: accountRefundRouteRequestID(record.RefundID, order.ID), RefundAmount: route.RefundCredit, GatewayAmount: route.GatewayRefund, Reason: accountRefundReason, Force: true}
		route.GatewayStatus = AccountRefundStateSubmitting
		route.GatewayError = ""
		record.State = AccountRefundStateSubmitting
		record.Message = "submitting refund route to original payment provider"
		record.UpdatedAt = time.Now().UTC()
		if err := s.writeAccountRefundAudit(ctx, record); err != nil {
			return nil, err
		}
		resp, callErr := s.gwRefund(ctx, plan)
		if callErr != nil {
			route.GatewayError = callErr.Error()
			if payment.IsRefundOutcomeUnknown(callErr) {
				route.GatewayStatus = accountRefundGatewayUnknown
				if pendingErr := s.markAccountRefundOrderPending(ctx, route, plan, nil, record.Actor); pendingErr != nil {
					route.GatewayError += "; mark order pending: " + pendingErr.Error()
				}
				record.State = AccountRefundStateManualReview
				record.ReviewReasonCode = AccountRefundReviewGatewayUnknown
				record.FailureStage = AccountRefundFailureGateway
				record.Message = "gateway refund outcome is unknown; manual reconciliation is required before any retry"
				record.UpdatedAt = time.Now().UTC()
				if err := s.writeAccountRefundAudit(ctx, record); err != nil {
					return nil, err
				}
				return record, nil
			}
			route.GatewayStatus = payment.ProviderStatusFailed
			return s.failAccountRefund(ctx, record, succeeded, callErr.Error())
		}
		route.GatewayStatus = strings.TrimSpace(resp.Status)
		route.GatewayRefundID = refundResponseID(resp)
		if route.GatewayStatus == payment.ProviderStatusPending {
			_ = s.markAccountRefundOrderPending(ctx, route, plan, resp, record.Actor)
			record.State = AccountRefundStatePending
			record.Message = "gateway refund is pending confirmation"
			record.UpdatedAt = time.Now().UTC()
			if err := s.writeAccountRefundAudit(ctx, record); err != nil {
				return nil, err
			}
			return record, nil
		}
		if err := s.markAccountRefundOrderSucceeded(ctx, route, plan, record.Actor); err != nil {
			route.GatewayError = err.Error()
			return s.failAccountRefund(ctx, record, succeeded+1, "gateway succeeded but local order update failed: "+err.Error())
		}
		succeeded++
	}

	if err := s.finalizeAccountRefundCredits(ctx, record); err != nil {
		record.State = AccountRefundStateManualReview
		record.ReviewReasonCode = AccountRefundReviewFinalizeFailed
		record.FailureStage = AccountRefundFailurePostGateway
		record.Message = "finalize account credits: " + err.Error()
		record.UpdatedAt = time.Now().UTC()
		if auditErr := s.writeAccountRefundAudit(ctx, record); auditErr != nil {
			return nil, auditErr
		}
		return record, nil
	}
	if err := s.restoreTerminalAccountRefundAccess(ctx, record); err != nil {
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
	return s.cancelAccountRefundRecord(ctx, record)
}

func (s *PaymentService) cancelAccountRefundRecord(ctx context.Context, record *AccountRefundRecord) (*AccountRefundRecord, error) {
	if !accountRefundCanCancel(record) {
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
	userQuery := tx.User.Query().Where(user.IDEQ(record.UserID), user.StatusEQ(StatusRefundLocked))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		userQuery = userQuery.ForUpdate()
	}
	lockedUser, err := userQuery.Only(txCtx)
	if err != nil {
		return nil, infraerrors.Conflict("REFUND_LOCK_LOST", "refund no longer owns the account lock")
	}
	auditQuery := tx.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDEQ(accountRefundAuditKey(record.UserID, record.RefundID))).Order(dbent.Desc(paymentauditlog.FieldCreatedAt), dbent.Desc(paymentauditlog.FieldID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		auditQuery = auditQuery.ForUpdate()
	}
	latestRow, err := auditQuery.First(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock refund cancellation state: %w", err)
	}
	if record.StateRevision != 0 && latestRow.ID != record.StateRevision {
		return nil, infraerrors.Conflict("REFUND_STATE_CHANGED", "account refund state changed; refresh and retry")
	}
	_, err = tx.User.UpdateOne(lockedUser).SetStatus(restore).Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("restore account status: %w", err)
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
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, record.UserID)
	}
	if fence, ok := s.authCacheInvalidator.(RefundBillingFence); ok {
		if err := fence.ReleaseRefundBillingLock(ctx, record.UserID, record.RefundID); err != nil {
			record.ReviewReasonCode = AccountRefundReviewAccessRestoreFailed
			record.FailureStage = AccountRefundFailurePostGateway
			record.Message = "account status restored but billing fence release failed"
			record.UpdatedAt = time.Now().UTC()
			_ = s.writeAccountRefundAudit(ctx, record)
			return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "account restored but refund billing fence could not be released")
		}
	}
	return record, nil
}

// GetAdminAccountRefund 返回用户最近一笔账户清退，供管理员人工核验。
func (s *PaymentService) GetAdminAccountRefund(ctx context.Context, userID int64) (*AccountRefundRecord, error) {
	record, err := s.latestAccountRefundForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, infraerrors.NotFound("REFUND_NOT_FOUND", "account refund not found")
	}
	return record, nil
}

// AdminCancelAccountRefund 在确认没有外部退款成功后取消清退并恢复用户。
func (s *PaymentService) AdminCancelAccountRefund(ctx context.Context, userID int64) (*AccountRefundRecord, error) {
	record, err := s.GetAdminAccountRefund(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.CancelAccountRefund(ctx, record.RefundID, userID)
}

// AdminReconcileAccountRefund 将不可查询的网关结果人工确认为成功或失败。
func (s *PaymentService) AdminReconcileAccountRefund(ctx context.Context, userID int64, input AdminAccountRefundReconcileInput) (*AccountRefundRecord, error) {
	return s.AdminReconcileAccountRefundWithActor(ctx, userID, input, AccountRefundActor{Type: "admin", Label: "admin"})
}

// AdminReconcileAccountRefundWithActor 记录结构化人工核验依据和真实管理员。
func (s *PaymentService) AdminReconcileAccountRefundWithActor(ctx context.Context, userID int64, input AdminAccountRefundReconcileInput, actor AccountRefundActor) (*AccountRefundRecord, error) {
	input.Outcome = strings.TrimSpace(strings.ToLower(input.Outcome))
	input.Note = strings.TrimSpace(input.Note)
	input.Evidence = strings.TrimSpace(input.Evidence)
	input.ExternalRefundID = strings.TrimSpace(input.ExternalRefundID)
	if userID <= 0 || input.OrderID <= 0 || input.Note == "" || input.VerifiedAt == nil {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user_id, order_id, note and verified_at are required")
	}
	if input.Outcome != AdminAccountRefundOutcomeSucceeded && input.Outcome != AdminAccountRefundOutcomeFailed {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "outcome must be succeeded or failed")
	}
	if input.ExpectedStateRevision > 0 && input.Evidence == "" {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "evidence is required")
	}
	if input.ExpectedStateRevision > 0 && input.Outcome == AdminAccountRefundOutcomeSucceeded && input.ExternalRefundID == "" {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "external_refund_id is required for a successful reconciliation")
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin account refund reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	userQuery := tx.User.Query().Where(user.IDEQ(userID), user.StatusEQ(StatusRefundLocked))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		userQuery = userQuery.ForUpdate()
	}
	if _, err := userQuery.Only(txCtx); err != nil {
		return nil, infraerrors.Conflict("REFUND_LOCK_LOST", "refund no longer owns the account lock")
	}

	prefix := accountRefundAuditPrefix + strconv.FormatInt(userID, 10) + ":"
	auditQuery := tx.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDHasPrefix(prefix)).Order(dbent.Desc(paymentauditlog.FieldCreatedAt), dbent.Desc(paymentauditlog.FieldID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		auditQuery = auditQuery.ForUpdate()
	}
	latestRow, err := auditQuery.First(txCtx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.NotFound("REFUND_NOT_FOUND", "account refund not found")
	}
	if err != nil {
		return nil, fmt.Errorf("lock account refund reconciliation state: %w", err)
	}
	record, err := parseAccountRefundRecord(latestRow)
	if err != nil {
		return nil, err
	}
	if input.ExpectedStateRevision > 0 && latestRow.ID != input.ExpectedStateRevision {
		return nil, infraerrors.Conflict("REFUND_STATE_CHANGED", "account refund state changed; refresh and retry")
	}
	actor.Type = "admin"
	record.Actor = &actor
	if record.Quote == nil || accountRefundTerminal(record.State) {
		return nil, infraerrors.Conflict("REFUND_NOT_RECONCILABLE", "account refund is already terminal or has no quote")
	}

	routeIndex := -1
	for i := range record.Quote.Orders {
		if record.Quote.Orders[i].OrderID == input.OrderID {
			routeIndex = i
			break
		}
	}
	if routeIndex < 0 {
		return nil, infraerrors.BadRequest("REFUND_ROUTE_NOT_FOUND", "order is not part of this account refund")
	}
	route := &record.Quote.Orders[routeIndex]
	reconciliation := AccountRefundReconciliation{
		OrderID: input.OrderID, Outcome: input.Outcome, ExternalRefundID: input.ExternalRefundID,
		VerifiedAt: input.VerifiedAt.UTC(), Evidence: input.Evidence, Note: input.Note, Actor: &actor,
	}
	manualExternal := accountRefundReviewReason(record) == AccountRefundReviewManualExternalRequired
	switch route.GatewayStatus {
	case payment.ProviderStatusPending, AccountRefundStateSubmitting, accountRefundGatewayUnknown:
	case "":
		if !manualExternal {
			return nil, infraerrors.Conflict("REFUND_ROUTE_NOT_RECONCILABLE", "refund route is not awaiting manual reconciliation")
		}
	default:
		return nil, infraerrors.Conflict("REFUND_ROUTE_NOT_RECONCILABLE", "refund route is not awaiting manual reconciliation")
	}

	orderQuery := tx.PaymentOrder.Query().Where(paymentorder.IDEQ(input.OrderID), paymentorder.UserIDEQ(userID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		orderQuery = orderQuery.ForUpdate()
	}
	order, err := orderQuery.Only(txCtx)
	if err != nil {
		return nil, infraerrors.NotFound("REFUND_ROUTE_NOT_FOUND", "payment order not found")
	}
	if input.Outcome == AdminAccountRefundOutcomeSucceeded {
		cumulativeRefund := accountRefundRouteCumulativeCredit(route)
		status := OrderStatusPartiallyRefunded
		if cumulativeRefund >= order.Amount-accountRefundTolerance {
			status = OrderStatusRefunded
		}
		if _, err := tx.PaymentOrder.UpdateOne(order).
			SetStatus(status).
			SetRefundAmount(cumulativeRefund).
			SetRefundReason(accountRefundReason).
			SetRefundAt(time.Now()).
			SetForceRefund(true).
			ClearFailedAt().
			ClearFailedReason().
			Save(txCtx); err != nil {
			return nil, fmt.Errorf("confirm reconciled refund route: %w", err)
		}
		route.GatewayStatus = payment.ProviderStatusSuccess
		route.GatewayRefundID = input.ExternalRefundID
		route.GatewayError = "manual reconciliation confirmed success: " + input.Note
	} else {
		if manualExternal {
			record.Reconciliations = append(record.Reconciliations, reconciliation)
			record.Message = "external refund was not completed; manual reconciliation is still required: " + input.Note
			record.UpdatedAt = time.Now().UTC()
			if err := writeAccountRefundAudit(txCtx, tx.Client(), record); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit account refund reconciliation: %w", err)
			}
			return record, nil
		}
		restoreStatus := OrderStatusCompleted
		if route.PreviouslyRefunded > accountRefundTolerance {
			restoreStatus = OrderStatusPartiallyRefunded
		}
		update := tx.PaymentOrder.UpdateOne(order).
			SetStatus(restoreStatus).
			SetRefundAmount(route.PreviouslyRefunded)
		if route.PreviouslyRefunded <= accountRefundTolerance {
			update = update.ClearRefundReason().ClearRefundAt().SetForceRefund(false)
		}
		if _, err := update.Save(txCtx); err != nil {
			return nil, fmt.Errorf("restore failed refund route: %w", err)
		}
		route.GatewayStatus = payment.ProviderStatusFailed
		route.GatewayError = "manual reconciliation confirmed failure: " + input.Note
	}
	record.Reconciliations = append(record.Reconciliations, reconciliation)

	completed := completedAccountRefundRoutes(record)
	if input.Outcome == AdminAccountRefundOutcomeSucceeded && accountRefundAllRoutesCompleted(record) {
		record.State = AccountRefundStateSubmitting
		record.Message = "all refund routes were manually confirmed; finalizing account credits"
	} else if manualExternal {
		record.State = AccountRefundStateManualReview
		record.ReviewReasonCode = AccountRefundReviewManualExternalRequired
		record.FailureStage = AccountRefundFailurePreGateway
		record.Message = "external refund route confirmed; remaining routes still require reconciliation"
	} else if completed > 0 {
		record.State = AccountRefundStatePartialExternalSuccess
		record.Message = "manual reconciliation completed; remaining refund routes require continuation"
	} else {
		record.State = AccountRefundStateFailed
		record.Message = "manual reconciliation confirmed no external refund; cancellation or retry is available"
	}
	record.UpdatedAt = time.Now().UTC()
	if record.State != AccountRefundStateManualReview {
		record.ReviewReasonCode = ""
		record.FailureStage = ""
	}
	if err := writeAccountRefundAudit(txCtx, tx.Client(), record); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit account refund reconciliation: %w", err)
	}
	s.writeAuditLog(ctx, input.OrderID, "ACCOUNT_REFUND_MANUAL_RECONCILED", "admin", map[string]any{
		"refundID": record.RefundID, "outcome": input.Outcome, "externalRefundID": input.ExternalRefundID, "verifiedAt": input.VerifiedAt, "evidence": input.Evidence, "note": input.Note,
	})
	if record.State == AccountRefundStateSubmitting {
		return s.executeAccountRefundRoutes(ctx, record)
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
	hasGrant  bool
}

type accountRefundOrderPool struct {
	order             *dbent.PaymentOrder
	campaign          bool
	principalCapacity decimal.Decimal
	gatewayCapacity   decimal.Decimal
	bonusInitial      decimal.Decimal
	bonusRemaining    decimal.Decimal
	bonusRate         decimal.Decimal
	refundFactor      decimal.Decimal
}

type accountRefundQuoteInputs struct {
	account   *User
	inFlight  int
	orders    []*dbent.PaymentOrder
	grants    []*dbent.UserLimitedCreditGrant
	providers map[int64]*dbent.PaymentProviderInstance
	now       time.Time
}

// rechargeBonusPercentToFraction 将订单快照中的百分数统一转换为小数比例。
func rechargeBonusPercentToFraction(percent float64) (decimal.Decimal, bool) {
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent <= 0 {
		return decimal.Zero, false
	}
	fraction := decimal.NewFromFloat(percent).Div(decimal.NewFromInt(100))
	if !fraction.GreaterThan(decimal.Zero) {
		return decimal.Zero, false
	}
	return fraction, true
}

// rechargeBonusRefundFactor 按活动赠额小数比例计算本金折算系数。
func rechargeBonusRefundFactor(rate decimal.Decimal) decimal.Decimal {
	return decimal.NewFromInt(1).Div(decimal.NewFromInt(1).Add(rate))
}

// buildAccountRefundQuoteWithClient 允许确认事务在已锁定资金行上复算同一份权威试算。
func (s *PaymentService) buildAccountRefundQuoteWithClient(ctx context.Context, client *dbent.Client, account *User) (*AccountRefundQuote, error) {
	inputs, err := loadAccountRefundQuoteInputs(ctx, client, account)
	if err != nil {
		return nil, err
	}
	return calculateAccountRefundQuote(inputs), nil
}

// loadAccountRefundQuoteInputs 以固定查询次数装配单用户报价输入。
func loadAccountRefundQuoteInputs(ctx context.Context, client *dbent.Client, account *User) (accountRefundQuoteInputs, error) {
	inFlight, err := client.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(account.ID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		accountRefundInFlightOrderPredicate(),
	).Count(ctx)
	if err != nil {
		return accountRefundQuoteInputs{}, fmt.Errorf("count in-flight recharge orders: %w", err)
	}
	orders, err := client.PaymentOrder.Query().Where(
		paymentorder.UserIDEQ(account.ID),
		paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		accountRefundHistoricalOrderPredicate(),
	).Order(dbent.Asc(paymentorder.FieldCompletedAt), dbent.Asc(paymentorder.FieldID)).All(ctx)
	if err != nil {
		return accountRefundQuoteInputs{}, fmt.Errorf("list refundable orders: %w", err)
	}
	grants, err := client.UserLimitedCreditGrant.Query().Where(creditgrant.UserIDEQ(account.ID)).All(ctx)
	if err != nil {
		return accountRefundQuoteInputs{}, fmt.Errorf("list refund credits: %w", err)
	}
	providerIDs := make([]int64, 0, len(orders))
	seenProviderIDs := make(map[int64]struct{}, len(orders))
	for _, order := range orders {
		if order.ProviderInstanceID == nil {
			continue
		}
		providerID, parseErr := strconv.ParseInt(strings.TrimSpace(*order.ProviderInstanceID), 10, 64)
		if parseErr != nil {
			continue
		}
		if _, exists := seenProviderIDs[providerID]; exists {
			continue
		}
		seenProviderIDs[providerID] = struct{}{}
		providerIDs = append(providerIDs, providerID)
	}
	providers := make(map[int64]*dbent.PaymentProviderInstance, len(providerIDs))
	if len(providerIDs) > 0 {
		rows, providerErr := client.PaymentProviderInstance.Query().Where(paymentproviderinstance.IDIn(providerIDs...)).All(ctx)
		if providerErr != nil {
			return accountRefundQuoteInputs{}, fmt.Errorf("list refund providers: %w", providerErr)
		}
		for _, provider := range rows {
			providers[provider.ID] = provider
		}
	}
	return accountRefundQuoteInputs{account: account, inFlight: inFlight, orders: orders, grants: grants, providers: providers, now: time.Now().UTC()}, nil
}

// calculateAccountRefundQuote 是用户单笔与管理员批量工作台共用的纯计算器。
func calculateAccountRefundQuote(inputs accountRefundQuoteInputs) *AccountRefundQuote {
	account := inputs.account
	inFlight := inputs.inFlight
	orders := inputs.orders
	grants := inputs.grants
	quote := &AccountRefundQuote{CalculationStatus: AccountRefundCalculationManualReview, AdminExecutionMode: AccountRefundAdminBlocked, TotalConfidence: "manual_review", AllocationConfidence: "deterministic", PermanentBalance: normalizeRefundFloat(account.Balance), GatewayTotals: map[string]float64{}, Orders: make([]AccountRefundOrder, 0, len(orders))}
	calculationBlockReason := ""
	selfServiceBlockReason := ""
	adminAutomatic := true
	setBlockReason := func(reason string) {
		if quote.BlockReason == "" {
			quote.BlockReason = reason
		}
		if calculationBlockReason == "" {
			calculationBlockReason = reason
		}
	}
	setExecutionReason := func(reason string, blocksAdminAutomatic bool) {
		if selfServiceBlockReason == "" {
			selfServiceBlockReason = reason
		}
		if blocksAdminAutomatic {
			adminAutomatic = false
		}
	}
	if inFlight > 0 {
		setBlockReason("a balance recharge order is still pending, being fulfilled, or being refunded")
	}
	orderByID := make(map[int64]*dbent.PaymentOrder, len(orders))
	for _, order := range orders {
		orderByID[order.ID] = order
	}
	bonusByOrder := make(map[int64]accountRefundBonusAggregate)
	now := inputs.now
	for _, grant := range grants {
		remaining := math.Max(0, grant.InitialAmount-grant.UsedAmount-grant.FrozenAmount)
		activeRemaining := decimal.Zero
		if grant.Status == LimitedCreditStatusActive && grant.ExpiresAt.After(now) {
			activeRemaining = decimal.NewFromFloat(remaining)
		}
		if grant.SourceType == LimitedCreditSourceRechargeBonus {
			if grant.SourceID == nil {
				quote.OtherLimitedToClear += activeRemaining.InexactFloat64()
				setBlockReason("a recharge bonus grant has no source order")
				continue
			}
			aggregate := bonusByOrder[*grant.SourceID]
			aggregate.hasGrant = true
			aggregate.initial = aggregate.initial.Add(decimal.NewFromFloat(grant.InitialAmount))
			aggregate.remaining = aggregate.remaining.Add(activeRemaining)
			bonusByOrder[*grant.SourceID] = aggregate
			if _, exists := orderByID[*grant.SourceID]; !exists {
				quote.OtherLimitedToClear += activeRemaining.InexactFloat64()
				setBlockReason("a recharge bonus cannot be matched to a historical recharge order")
			}
		} else {
			quote.OtherLimitedToClear += activeRemaining.InexactFloat64()
		}
		if grant.FrozenAmount > accountRefundTolerance {
			setBlockReason("limited credits are still frozen")
		}
	}
	if account.FrozenBalance > accountRefundTolerance {
		setBlockReason("permanent balance is still frozen")
	}
	if len(orders) == 0 && quote.BlockReason == "" {
		setBlockReason("no historical balance recharge order is refundable")
	}

	positiveBalance := decimal.NewFromFloat(math.Max(account.Balance, 0))
	if account.Balance < -accountRefundTolerance {
		setBlockReason("permanent balance is negative and requires manual reconciliation")
	}
	fullPriceCapacity := decimal.Zero
	campaignCapacity := decimal.Zero
	campaignBonusRemaining := decimal.Zero
	pools := make([]accountRefundOrderPool, 0, len(orders))
	for _, order := range orders {
		bonus := bonusByOrder[order.ID]
		hasCampaignSnapshot := order.RechargeBonusCampaignID != nil
		isCampaign := hasCampaignSnapshot && bonus.hasGrant
		bonusRate := decimal.Zero
		refundFactor := decimal.Zero
		if isCampaign {
			var valid bool
			bonusRate, valid = rechargeBonusPercentToFraction(order.RechargeBonusRate)
			if !valid {
				setBlockReason("a recharge bonus order has no valid percentage snapshot")
			} else {
				refundFactor = rechargeBonusRefundFactor(bonusRate)
			}
			if decimal.NewFromFloat(order.RechargeBonusAmount).Sub(bonus.initial).Abs().GreaterThan(decimal.NewFromFloat(accountRefundTolerance)) {
				setBlockReason("recharge bonus grant cannot be reconciled to its order snapshot")
			}
			campaignBonusRemaining = campaignBonusRemaining.Add(bonus.remaining)
			quote.RechargeBonusBalance += bonus.remaining.InexactFloat64()
		} else if bonus.hasGrant {
			// 活动快照缺失的异常赠额不参与退款，仍会在清退终态被清空。
			quote.OtherLimitedToClear += bonus.remaining.InexactFloat64()
		} else if hasCampaignSnapshot && order.RechargeBonusStatus == paymentorder.RechargeBonusStatusGranted {
			setBlockReason("a granted campaign order has no recharge bonus grant")
		}

		principalCapacity := decimal.NewFromFloat(order.Amount).Sub(decimal.NewFromFloat(order.RefundAmount))
		if principalCapacity.IsNegative() {
			setBlockReason("a historical order refund exceeds its original credited amount")
			principalCapacity = decimal.Zero
		}
		if order.Status == OrderStatusRefunded && principalCapacity.GreaterThan(decimal.NewFromFloat(accountRefundTolerance)) {
			setBlockReason("a refunded historical order has inconsistent remaining principal")
		}
		currency := PaymentOrderCurrency(order)
		gatewayRefunded := calculateGatewayRefundAmount(order.Amount, order.PayAmount, math.Min(math.Max(order.RefundAmount, 0), order.Amount), currency)
		gatewayCapacity := decimal.NewFromFloat(order.PayAmount).Sub(decimal.NewFromFloat(gatewayRefunded))
		if gatewayCapacity.IsNegative() {
			setBlockReason("a historical order exceeds its original gateway refund capacity")
			gatewayCapacity = decimal.Zero
		}
		if principalCapacity.GreaterThan(decimal.NewFromFloat(accountRefundTolerance)) || (isCampaign && bonus.remaining.GreaterThan(decimal.Zero)) {
			if strings.TrimSpace(order.PaymentTradeNo) == "" {
				setExecutionReason("a refundable historical order has no original payment trade number", true)
			}
			if order.ProviderInstanceID == nil || strings.TrimSpace(*order.ProviderInstanceID) == "" {
				setExecutionReason("a refundable historical order has no original payment provider", true)
			} else {
				instanceID, parseErr := strconv.ParseInt(strings.TrimSpace(*order.ProviderInstanceID), 10, 64)
				if parseErr != nil {
					setExecutionReason("a refundable historical order has an invalid payment provider", true)
				} else {
					provider := inputs.providers[instanceID]
					if provider == nil || !provider.RefundEnabled {
						setExecutionReason("an original payment provider does not support automatic refunds", true)
					} else if !provider.AllowUserRefund {
						setExecutionReason("an original payment provider does not allow user refunds", false)
					}
				}
			}
		}
		if isCampaign {
			campaignCapacity = campaignCapacity.Add(principalCapacity)
		} else {
			fullPriceCapacity = fullPriceCapacity.Add(principalCapacity)
		}
		pools = append(pools, accountRefundOrderPool{
			order: order, campaign: isCampaign, principalCapacity: principalCapacity, gatewayCapacity: gatewayCapacity,
			bonusInitial: bonus.initial, bonusRemaining: bonus.remaining, bonusRate: bonusRate, refundFactor: refundFactor,
		})
	}
	totalPrincipalCapacity := fullPriceCapacity.Add(campaignCapacity)
	if positiveBalance.GreaterThan(totalPrincipalCapacity.Add(decimal.NewFromFloat(accountRefundTolerance))) {
		setBlockReason("permanent balance exceeds historical remaining principal capacity")
	}
	fullPriceRemaining := decimal.Min(positiveBalance, fullPriceCapacity)
	campaignPermanentRemaining := positiveBalance.Sub(fullPriceRemaining)
	if campaignPermanentRemaining.GreaterThan(campaignCapacity.Add(decimal.NewFromFloat(accountRefundTolerance))) {
		setBlockReason("campaign permanent balance exceeds campaign order capacity")
	}

	fullPriceWeights := make([]decimal.Decimal, len(pools))
	campaignWeights := make([]decimal.Decimal, len(pools))
	for i := range pools {
		if pools[i].campaign {
			campaignWeights[i] = pools[i].principalCapacity
		} else {
			fullPriceWeights[i] = pools[i].principalCapacity
		}
	}
	fullPriceUnits := allocateRefundUnits(fullPriceRemaining, fullPriceWeights, 8)
	campaignUnits := allocateRefundUnits(campaignPermanentRemaining, campaignWeights, 8)
	rawRefundWeights := make([]decimal.Decimal, len(pools))
	creditCapacities := make([]decimal.Decimal, len(pools))
	rawRefundTotal := decimal.Zero
	for i := range pools {
		principalAllocation := decimal.NewFromInt(fullPriceUnits[i] + campaignUnits[i]).Shift(-8)
		eligibleCredit := principalAllocation
		refundCredit := principalAllocation
		if pools[i].campaign {
			eligibleCredit = eligibleCredit.Add(pools[i].bonusRemaining)
			refundCredit = eligibleCredit.Mul(pools[i].refundFactor)
		}
		rawRefundWeights[i] = refundCredit
		rawRefundTotal = rawRefundTotal.Add(refundCredit)
		creditCapacities[i] = pools[i].principalCapacity
		order := pools[i].order
		completedAt := order.CreatedAt
		if order.CompletedAt != nil {
			completedAt = *order.CompletedAt
		}
		quote.Orders = append(quote.Orders, AccountRefundOrder{
			OrderID: order.ID, CompletedAt: completedAt, PaymentType: order.PaymentType,
			ProviderInstance: psStringValue(order.ProviderInstanceID), Currency: PaymentOrderCurrency(order),
			OriginalCredit: normalizeRefundFloat(order.Amount), OriginalPaid: normalizeRefundFloat(order.PayAmount),
			PreviouslyRefunded: normalizeRefundFloat(math.Max(order.RefundAmount, 0)),
			BonusRate:          normalizeRefundFloat(pools[i].bonusRate.InexactFloat64()), BonusInitial: normalizeRefundFloat(pools[i].bonusInitial.InexactFloat64()), BonusRemaining: normalizeRefundFloat(pools[i].bonusRemaining.InexactFloat64()),
			EligibleCredit: normalizeRefundFloat(eligibleCredit.InexactFloat64()), Allocation: "deterministic",
		})
	}

	refundTotal := rawRefundTotal.Round(2)
	refundUnits, allocationClosed := allocateRefundUnitsWithCapacities(refundTotal, rawRefundWeights, creditCapacities, 2)
	if !allocationClosed {
		setBlockReason("refund total exceeds historical order remaining capacity")
	}
	allocatedRefundTotal := decimal.Zero
	for i := range quote.Orders {
		refundCredit := decimal.NewFromInt(refundUnits[i]).Shift(-2)
		quote.Orders[i].RefundCredit = refundCredit.InexactFloat64()
		allocatedRefundTotal = allocatedRefundTotal.Add(refundCredit)
		order := pools[i].order
		gateway := decimal.NewFromFloat(calculateGatewayRefundAmount(order.Amount, order.PayAmount, refundCredit.InexactFloat64(), PaymentOrderCurrency(order)))
		if gateway.GreaterThan(pools[i].gatewayCapacity.Add(decimal.NewFromFloat(accountRefundTolerance))) {
			setBlockReason("an order route exceeds its remaining gateway refund capacity")
		}
		quote.Orders[i].GatewayRefund = gateway.InexactFloat64()
		quote.GatewayTotals[PaymentOrderCurrency(order)] += gateway.InexactFloat64()
	}
	quote.EligibleCreditTotal = normalizeRefundFloat(positiveBalance.Add(campaignBonusRemaining).InexactFloat64())
	quote.RefundCreditTotal = normalizeRefundFloat(allocatedRefundTotal.InexactFloat64())
	if allocatedRefundTotal.Sub(refundTotal).Abs().GreaterThan(decimal.NewFromFloat(accountRefundTolerance)) {
		setBlockReason("order route allocation does not conserve the user refund total")
	}
	for currency, amount := range quote.GatewayTotals {
		quote.GatewayTotals[currency] = normalizeRefundFloat(amount)
	}
	quote.RechargeBonusBalance = normalizeRefundFloat(quote.RechargeBonusBalance)
	quote.OtherLimitedToClear = normalizeRefundFloat(quote.OtherLimitedToClear)
	quote.EligibleCreditTotal = normalizeRefundFloat(quote.EligibleCreditTotal)
	quote.RefundCreditTotal = normalizeRefundFloat(quote.RefundCreditTotal)

	if calculationBlockReason == "" && quote.RefundCreditTotal <= accountRefundTolerance {
		setBlockReason("no refundable amount remains")
	}
	if calculationBlockReason == "" {
		quote.CalculationStatus = AccountRefundCalculationVerified
		quote.TotalConfidence = "reconciled"
		quote.SelfServiceEligible = selfServiceBlockReason == ""
		quote.Eligible = quote.SelfServiceEligible
		if adminAutomatic {
			quote.AdminExecutionMode = AccountRefundAdminAutomatic
		} else {
			quote.AdminExecutionMode = AccountRefundAdminManual
		}
		if selfServiceBlockReason != "" {
			quote.BlockReason = selfServiceBlockReason
		} else {
			quote.BlockReason = ""
		}
	} else {
		quote.ReviewReasonCode = AccountRefundReviewQuoteInconsistent
	}
	quote.QuoteHash = accountRefundQuoteHash(quote)
	return quote
}

// allocateRefundUnitsWithCapacities 按权重分配最小单位，并确保每条订单路由不超过剩余容量。
func allocateRefundUnitsWithCapacities(total decimal.Decimal, weights, capacities []decimal.Decimal, fractionDigits int) ([]int64, bool) {
	allocated := make([]int64, len(capacities))
	if len(weights) != len(capacities) || len(capacities) == 0 {
		return allocated, !total.GreaterThan(decimal.Zero)
	}
	factor := decimal.New(1, int32(fractionDigits))
	totalUnits := total.Mul(factor).Round(0).IntPart()
	capacityUnits := make([]int64, len(capacities))
	capacityTotal := int64(0)
	weightTotal := decimal.Zero
	for i := range capacities {
		capacityUnits[i] = capacities[i].Mul(factor).Floor().IntPart()
		if capacityUnits[i] < 0 {
			capacityUnits[i] = 0
		}
		capacityTotal += capacityUnits[i]
		if weights[i].GreaterThan(decimal.Zero) {
			weightTotal = weightTotal.Add(weights[i])
		}
	}
	if totalUnits <= 0 {
		return allocated, true
	}
	if capacityTotal < totalUnits || !weightTotal.GreaterThan(decimal.Zero) {
		return allocated, false
	}
	used := int64(0)
	for i := range weights {
		if !weights[i].GreaterThan(decimal.Zero) || capacityUnits[i] == 0 {
			continue
		}
		share := decimal.NewFromInt(totalUnits).Mul(weights[i]).Div(weightTotal).Floor().IntPart()
		if share > capacityUnits[i] {
			share = capacityUnits[i]
		}
		allocated[i] = share
		used += share
	}
	for used < totalUnits {
		progressed := false
		for i := len(allocated) - 1; i >= 0 && used < totalUnits; i-- {
			if allocated[i] >= capacityUnits[i] {
				continue
			}
			allocated[i]++
			used++
			progressed = true
		}
		if !progressed {
			return allocated, false
		}
	}
	return allocated, true
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
	adminAutomaticReady := record.AdminInitiated && quote.CalculationStatus == AccountRefundCalculationVerified && quote.AdminExecutionMode == AccountRefundAdminAutomatic
	if !quote.Eligible && !adminAutomaticReady {
		if record.AdminInitiated && quote.CalculationStatus == AccountRefundCalculationVerified && quote.AdminExecutionMode == AccountRefundAdminManual {
			record.State = AccountRefundStateManualReview
			record.ReviewReasonCode = AccountRefundReviewManualExternalRequired
			record.FailureStage = AccountRefundFailurePreGateway
			record.Message = "automatic refund is unavailable; complete the original-channel refund externally and reconcile each route"
		} else {
			record.State = AccountRefundStateManualReview
			record.ReviewReasonCode = AccountRefundReviewQuoteInconsistent
			record.FailureStage = AccountRefundFailurePreGateway
			record.Message = quote.BlockReason
		}
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
	if err := clearAccountRefundAdminGrantSecurityDeposit(txCtx, tx.Client(), record.UserID, record.RefundID, time.Now().UTC()); err != nil {
		return err
	}
	if _, err := tx.User.UpdateOne(lockedUser).SetBalance(0).SetStatus(accountRefundRestoreStatus(record)).Save(txCtx); err != nil {
		return err
	}
	record.State = AccountRefundStateSucceeded
	record.Message = "account refund completed"
	record.UpdatedAt = time.Now().UTC()
	if err := writeAccountRefundAudit(txCtx, tx.Client(), record); err != nil {
		return err
	}
	return tx.Commit()
}

// clearAccountRefundAdminGrantSecurityDeposit 在账户清退事务内核销管理员发放保证金，实付保证金保持不变。
func clearAccountRefundAdminGrantSecurityDeposit(ctx context.Context, client *dbent.Client, userID int64, refundID string, now time.Time) error {
	accountQuery := client.SecurityDepositAccount.Query().Where(
		securitydepositaccount.UserIDEQ(userID),
		securitydepositaccount.BucketTypeEQ(securitydepositaccount.BucketTypeAdminGrant),
	)
	if client.Driver().Dialect() == dialect.Postgres {
		accountQuery = accountQuery.ForUpdate()
	}
	account, err := accountQuery.Only(ctx)
	if dbent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock account refund admin-grant security deposit: %w", err)
	}
	if account.RefundReservedCents != 0 {
		return fmt.Errorf("account refund admin-grant security deposit has reserved balance")
	}

	lotQuery := client.SecurityDepositLot.Query().Where(
		securitydepositlot.UserIDEQ(userID),
		securitydepositlot.BucketTypeEQ(securitydepositlot.BucketTypeAdminGrant),
		securitydepositlot.RemainingCentsGT(0),
	).Order(dbent.Asc(securitydepositlot.FieldCreatedAt), dbent.Asc(securitydepositlot.FieldID))
	if client.Driver().Dialect() == dialect.Postgres {
		lotQuery = lotQuery.ForUpdate()
	}
	lots, err := lotQuery.All(ctx)
	if err != nil {
		return fmt.Errorf("lock account refund admin-grant security deposit lots: %w", err)
	}
	remainingBalance := account.BalanceCents
	for _, lot := range lots {
		remainingBalance -= lot.RemainingCents
		if remainingBalance < 0 {
			return fmt.Errorf("account refund admin-grant security deposit account is inconsistent with lots")
		}
		if _, err := client.SecurityDepositLot.UpdateOne(lot).
			SetRemainingCents(0).
			AddRevokedCents(lot.RemainingCents).
			SetStatus("exhausted").
			SetUpdatedAt(now).
			Save(ctx); err != nil {
			return fmt.Errorf("clear account refund admin-grant security deposit lot %d: %w", lot.ID, err)
		}
		if _, err := client.SecurityDepositLedger.Create().
			SetUserID(userID).
			SetLotID(lot.ID).
			SetBucketType(securitydepositledger.BucketTypeAdminGrant).
			SetEntryType(securitydepositledger.EntryTypeAdminRevoke).
			SetDeltaCents(-lot.RemainingCents).
			SetBucketBalanceAfterCents(remainingBalance).
			SetBucketReservedAfterCents(0).
			SetReason("账户余额清退清空管理员发放保证金").
			SetIdempotencyKey(fmt.Sprintf("account_refund:%s:admin_grant:lot:%d", refundID, lot.ID)).
			SetCreatedAt(now).
			Save(ctx); err != nil {
			return fmt.Errorf("write account refund admin-grant security deposit ledger: %w", err)
		}
	}
	if remainingBalance != 0 {
		return fmt.Errorf("account refund admin-grant security deposit lots are inconsistent with account balance")
	}
	if account.BalanceCents == 0 {
		return nil
	}
	if _, err := client.SecurityDepositAccount.UpdateOne(account).
		SetBalanceCents(0).
		AddVersion(1).
		SetUpdatedAt(now).
		Save(ctx); err != nil {
		return fmt.Errorf("clear account refund admin-grant security deposit account: %w", err)
	}
	return nil
}

// restoreTerminalAccountRefundAccess 幂等恢复终态用户并释放可能遗留的计费栅栏。
func (s *PaymentService) restoreTerminalAccountRefundAccess(ctx context.Context, record *AccountRefundRecord) error {
	if record == nil || !accountRefundTerminal(record.State) {
		return nil
	}
	_, err := s.entClient.User.Update().
		Where(user.IDEQ(record.UserID), user.StatusEQ(StatusRefundLocked)).
		SetStatus(accountRefundRestoreStatus(record)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("restore terminal refund account status: %w", err)
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, record.UserID)
	}
	if fence, ok := s.authCacheInvalidator.(RefundBillingFence); ok {
		if err := fence.ReleaseRefundBillingLock(ctx, record.UserID, record.RefundID); err != nil {
			record.ReviewReasonCode = AccountRefundReviewAccessRestoreFailed
			record.FailureStage = AccountRefundFailurePostGateway
			record.Message = "refund completed but billing fence release failed"
			record.UpdatedAt = time.Now().UTC()
			_ = s.writeAccountRefundAudit(ctx, record)
			return infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "refund completed but billing fence could not be released")
		}
	}
	if record.ReviewReasonCode == AccountRefundReviewAccessRestoreFailed {
		record.ReviewReasonCode = ""
		record.FailureStage = ""
	}
	return nil
}

func accountRefundRestoreStatus(record *AccountRefundRecord) string {
	if record != nil && record.State != AccountRefundStateDonated {
		status := strings.TrimSpace(record.PreviousUserStatus)
		if status != "" && status != StatusRefundLocked {
			return status
		}
	}
	return StatusActive
}

func accountRefundRouteCumulativeCredit(route *AccountRefundOrder) float64 {
	return normalizeRefundFloat(route.PreviouslyRefunded + route.RefundCredit)
}

func accountRefundOrderReachedTarget(order *dbent.PaymentOrder, route *AccountRefundOrder) bool {
	if order.Status != OrderStatusPartiallyRefunded && order.Status != OrderStatusRefunded {
		return false
	}
	return math.Abs(order.RefundAmount-accountRefundRouteCumulativeCredit(route)) <= accountRefundTolerance
}

func accountRefundRouteOperator(_ *AccountRefundOrder, actors []*AccountRefundActor) string {
	if len(actors) > 0 && actors[0] != nil {
		return accountRefundActorOperator(actors[0], 0)
	}
	return "user"
}

// markAccountRefundOrderSucceeded 按历史已退额度加本次路由额度写入累计退款值。
func (s *PaymentService) markAccountRefundOrderSucceeded(ctx context.Context, route *AccountRefundOrder, plan *RefundPlan, actors ...*AccountRefundActor) error {
	cumulativeRefund := accountRefundRouteCumulativeCredit(route)
	status := OrderStatusPartiallyRefunded
	if cumulativeRefund >= plan.Order.Amount-accountRefundTolerance {
		status = OrderStatusRefunded
	}
	_, err := s.entClient.PaymentOrder.UpdateOneID(plan.OrderID).
		SetStatus(status).
		SetRefundAmount(cumulativeRefund).
		SetRefundReason(plan.Reason).
		SetRefundAt(time.Now()).
		SetForceRefund(true).
		ClearFailedAt().
		ClearFailedReason().
		Save(ctx)
	if err != nil {
		return fmt.Errorf("mark account refund route: %w", err)
	}
	s.writeAuditLog(ctx, plan.OrderID, "ACCOUNT_REFUND_SUCCESS", accountRefundRouteOperator(route, actors), map[string]any{
		"refundAmount": plan.RefundAmount, "cumulativeRefundAmount": cumulativeRefund, "reason": plan.Reason,
	})
	return nil
}

func (s *PaymentService) markAccountRefundOrderPending(ctx context.Context, route *AccountRefundOrder, plan *RefundPlan, resp *payment.RefundResponse, actors ...*AccountRefundActor) error {
	cumulativeRefund := accountRefundRouteCumulativeCredit(route)
	_, err := s.entClient.PaymentOrder.UpdateOneID(plan.OrderID).SetStatus(OrderStatusRefundPending).SetRefundAmount(cumulativeRefund).SetRefundReason(plan.Reason).SetForceRefund(true).Save(ctx)
	if err == nil {
		s.writeAuditLog(ctx, plan.OrderID, "ACCOUNT_REFUND_PENDING", accountRefundRouteOperator(route, actors), map[string]any{"refundID": refundResponseID(resp), "refundAmount": plan.RefundAmount, "cumulativeRefundAmount": cumulativeRefund})
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
		if route.GatewayStatus != payment.ProviderStatusPending && route.GatewayStatus != AccountRefundStateSubmitting && route.GatewayStatus != accountRefundGatewayUnknown {
			continue
		}
		order, err := s.entClient.PaymentOrder.Get(ctx, route.OrderID)
		if err != nil {
			return nil, err
		}
		if accountRefundOrderReachedTarget(order, route) {
			route.GatewayStatus = payment.ProviderStatusSuccess
			route.GatewayError = ""
			continue
		}
		provider, err := s.getRefundProvider(ctx, order)
		if err != nil {
			route.GatewayError = err.Error()
			record.State = AccountRefundStateManualReview
			record.ReviewReasonCode = AccountRefundReviewProviderUnavailable
			record.FailureStage = AccountRefundFailureGateway
			record.Message = "submitted refund provider is unavailable; manual reconciliation is required"
			record.UpdatedAt = time.Now().UTC()
			if auditErr := s.writeAccountRefundAudit(ctx, record); auditErr != nil {
				return nil, auditErr
			}
			return record, nil
		}
		queryProvider, ok := provider.(payment.RefundQueryProvider)
		if !ok {
			route.GatewayStatus = accountRefundGatewayUnknown
			record.State = AccountRefundStateManualReview
			record.ReviewReasonCode = AccountRefundReviewGatewayUnknown
			record.FailureStage = AccountRefundFailureGateway
			record.Message = "payment provider cannot query submitted refunds; manual reconciliation is required before any retry"
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
			route.GatewayError = err.Error()
			record.State = AccountRefundStateManualReview
			record.ReviewReasonCode = AccountRefundReviewGatewayQueryFailed
			record.FailureStage = AccountRefundFailureGateway
			record.Message = "refund status query failed; manual reconciliation is required: " + err.Error()
			record.UpdatedAt = time.Now().UTC()
			if auditErr := s.writeAccountRefundAudit(ctx, record); auditErr != nil {
				return nil, auditErr
			}
			return record, nil
		}
		if err := validateRefundProviderResponse(resp); err != nil {
			route.GatewayError = err.Error()
			route.GatewayStatus = payment.ProviderStatusFailed
			return s.failAccountRefund(ctx, record, completedAccountRefundRoutes(record), err.Error())
		}
		route.GatewayStatus = strings.TrimSpace(resp.Status)
		if id := refundResponseID(resp); id != "" {
			route.GatewayRefundID = id
		}
		if route.GatewayStatus == payment.ProviderStatusPending {
			record.State = AccountRefundStatePending
			record.Message = "gateway refund is pending confirmation"
			record.UpdatedAt = time.Now().UTC()
			if err := s.writeAccountRefundAudit(ctx, record); err != nil {
				return nil, err
			}
			return record, nil
		}
		plan := &RefundPlan{OrderID: order.ID, Order: order, RequestID: accountRefundRouteRequestID(record.RefundID, order.ID), RefundAmount: route.RefundCredit, GatewayAmount: route.GatewayRefund, Reason: accountRefundReason, Force: true}
		if err := s.markAccountRefundOrderSucceeded(ctx, route, plan, record.Actor); err != nil {
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
	record.ReviewReasonCode = ""
	record.FailureStage = AccountRefundFailureGateway
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
	record.StateRevision = row.ID
	return &record, nil
}

func (s *PaymentService) writeAccountRefundAudit(ctx context.Context, record *AccountRefundRecord) error {
	return writeAccountRefundAudit(ctx, s.entClient, record)
}

func writeAccountRefundAudit(ctx context.Context, client *dbent.Client, record *AccountRefundRecord) error {
	if record.Actor == nil {
		record.Actor = &AccountRefundActor{Type: "user", ID: record.UserID, Label: "user:" + strconv.FormatInt(record.UserID, 10)}
	}
	detail, err := json.Marshal(record)
	if err != nil {
		return err
	}
	action := "ACCOUNT_REFUND_EVENT_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	operator := accountRefundActorOperator(record.Actor, record.UserID)
	row, err := client.PaymentAuditLog.Create().SetOrderID(accountRefundAuditKey(record.UserID, record.RefundID)).SetAction(action).SetDetail(string(detail)).SetOperator(operator).Save(ctx)
	if err != nil {
		return fmt.Errorf("write account refund audit: %w", err)
	}
	record.StateRevision = row.ID
	return nil
}

func accountRefundActorOperator(actor *AccountRefundActor, userID int64) string {
	if actor == nil {
		return "user:" + strconv.FormatInt(userID, 10)
	}
	if label := strings.TrimSpace(actor.Label); label != "" {
		return label
	}
	actorType := strings.TrimSpace(actor.Type)
	if actorType == "" {
		actorType = "user"
	}
	if actor.ID > 0 {
		return actorType + ":" + strconv.FormatInt(actor.ID, 10)
	}
	return actorType
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
	case AccountRefundStateSucceeded, AccountRefundStateCanceled, AccountRefundStateDonated:
		return true
	default:
		return false
	}
}

func accountRefundCanCancel(record *AccountRefundRecord) bool {
	if record == nil {
		return false
	}
	switch record.State {
	case AccountRefundStateDraining, AccountRefundStateReadyToConfirm, AccountRefundStateFailed, AccountRefundStateManualReview:
	default:
		return false
	}
	if record.Quote == nil {
		return true
	}
	for _, route := range record.Quote.Orders {
		switch route.GatewayStatus {
		case payment.ProviderStatusSuccess, payment.ProviderStatusRefunded, payment.ProviderStatusPending, AccountRefundStateSubmitting, accountRefundGatewayUnknown:
			return false
		}
	}
	return true
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

func accountRefundAllRoutesCompleted(record *AccountRefundRecord) bool {
	if record == nil || record.Quote == nil {
		return false
	}
	for _, route := range record.Quote.Orders {
		if route.RefundCredit <= accountRefundTolerance || route.GatewayRefund <= accountRefundTolerance {
			continue
		}
		if route.GatewayStatus != payment.ProviderStatusSuccess && route.GatewayStatus != payment.ProviderStatusRefunded {
			return false
		}
	}
	return true
}
