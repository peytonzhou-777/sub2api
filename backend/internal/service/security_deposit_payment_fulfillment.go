package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"entgo.io/ent/dialect"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/securitydepositaccount"
	"github.com/Wei-Shaw/sub2api/ent/securitydepositledger"
	"github.com/Wei-Shaw/sub2api/ent/securitydepositlot"
	entuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// ExecuteSecurityDepositFulfillment 将已支付保证金订单幂等入账到 paid 资金桶。
func (s *PaymentService) ExecuteSecurityDepositFulfillment(ctx context.Context, orderID int64) error {
	order, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return infraerrors.NotFound("NOT_FOUND", "order not found")
	}
	if order.OrderType != payment.OrderTypeSecurityDeposit {
		return infraerrors.BadRequest("INVALID_ORDER_TYPE", "order is not a security deposit")
	}
	if order.Status == OrderStatusCompleted {
		return nil
	}
	if psIsRefundStatus(order.Status) {
		return infraerrors.BadRequest("INVALID_STATUS", "refund-related order cannot fulfill")
	}
	if order.Status != OrderStatusPaid && order.Status != OrderStatusFailed && order.Status != OrderStatusRecharging {
		return infraerrors.BadRequest("INVALID_STATUS", "order cannot fulfill in status "+order.Status)
	}
	lease, err := s.acquirePaymentFulfillmentLease(ctx, order)
	if err != nil {
		return err
	}
	if lease == nil {
		return nil
	}
	if err := s.fulfillSecurityDepositPayment(ctx, order, lease); err != nil {
		s.markFailed(ctx, orderID, lease, err)
		return err
	}
	return nil
}

func (s *PaymentService) fulfillSecurityDepositPayment(ctx context.Context, initialOrder *dbent.PaymentOrder, lease *paymentFulfillmentLease) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin security deposit fulfillment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)

	// 统一先锁用户再锁订单，与支付确认和账户清退保持一致。
	userQuery := tx.User.Query().Where(entuser.IDEQ(initialOrder.UserID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		userQuery = userQuery.ForUpdate()
	}
	if _, err := userQuery.Only(txCtx); err != nil {
		return fmt.Errorf("lock security deposit user: %w", err)
	}

	orderQuery := tx.PaymentOrder.Query().Where(
		paymentorder.IDEQ(initialOrder.ID),
		paymentorder.StatusEQ(OrderStatusRecharging),
		paymentorder.UpdatedAtEQ(lease.version),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		orderQuery = orderQuery.ForUpdate()
	}
	order, err := orderQuery.Only(txCtx)
	if err != nil {
		return fmt.Errorf("lock security deposit order: %w", err)
	}
	if order.PaidAt == nil {
		return infraerrors.BadRequest("INVALID_STATUS", "security deposit order is missing paid_at")
	}
	snapshot, err := securityDepositSnapshotFromOrder(order)
	if err != nil {
		return err
	}

	exists, err := tx.SecurityDepositLot.Query().Where(securitydepositlot.PaymentOrderIDEQ(order.ID)).Exist(txCtx)
	if err != nil {
		return fmt.Errorf("check security deposit payment lot: %w", err)
	}
	if exists {
		return infraerrors.Conflict("CONFLICT", "security deposit payment was already credited")
	}

	accountQuery := tx.SecurityDepositAccount.Query().Where(
		securitydepositaccount.UserIDEQ(order.UserID),
		securitydepositaccount.BucketTypeEQ(securitydepositaccount.BucketTypePaid),
	)
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		accountQuery = accountQuery.ForUpdate()
	}
	account, err := accountQuery.Only(txCtx)
	if dbent.IsNotFound(err) {
		account, err = tx.SecurityDepositAccount.Create().
			SetUserID(order.UserID).
			SetBucketType(securitydepositaccount.BucketTypePaid).
			SetCurrency(payment.DefaultPaymentCurrency).
			SetBalanceCents(0).
			SetRefundReservedCents(0).
			SetVersion(1).
			Save(txCtx)
	}
	if err != nil {
		return fmt.Errorf("load security deposit paid account: %w", err)
	}
	account, err = tx.SecurityDepositAccount.UpdateOne(account).
		AddBalanceCents(snapshot.PrincipalCents).
		AddVersion(1).
		Save(txCtx)
	if err != nil {
		return fmt.Errorf("credit security deposit paid account: %w", err)
	}

	lockedUntil := order.PaidAt.UTC().Add(time.Duration(snapshot.FreezeHours) * time.Hour)
	lot, err := tx.SecurityDepositLot.Create().
		SetUserID(order.UserID).
		SetBucketType(securitydepositlot.BucketTypePaid).
		SetSourceType(securitydepositlot.SourceTypePayment).
		SetPaymentOrderID(order.ID).
		SetOriginalCents(snapshot.PrincipalCents).
		SetRemainingCents(snapshot.PrincipalCents).
		SetCurrency(payment.DefaultPaymentCurrency).
		SetLockedUntil(lockedUntil).
		SetRefundPolicy(securitydepositlot.RefundPolicyTimedOriginalChannel).
		SetStatus("active").
		SetSourceReference(order.OutTradeNo).
		Save(txCtx)
	if err != nil {
		return fmt.Errorf("create security deposit payment lot: %w", err)
	}

	idempotencyKey := fmt.Sprintf("security_deposit:payment:%d", order.ID)
	if _, err := tx.SecurityDepositLedger.Create().
		SetUserID(order.UserID).
		SetLotID(lot.ID).
		SetBucketType(securitydepositledger.BucketTypePaid).
		SetEntryType(securitydepositledger.EntryTypePaymentCredit).
		SetDeltaCents(snapshot.PrincipalCents).
		SetReservedDeltaCents(0).
		SetBucketBalanceAfterCents(account.BalanceCents).
		SetBucketReservedAfterCents(account.RefundReservedCents).
		SetGroupID(snapshot.GroupID).
		SetPaymentOrderID(order.ID).
		SetIdempotencyKey(idempotencyKey).
		Save(txCtx); err != nil {
		return fmt.Errorf("create security deposit payment ledger: %w", err)
	}

	completedAt := time.Now().UTC()
	updated, err := tx.PaymentOrder.Update().Where(
		paymentorder.IDEQ(order.ID),
		paymentorder.StatusEQ(OrderStatusRecharging),
		paymentorder.UpdatedAtEQ(lease.version),
	).SetStatus(OrderStatusCompleted).SetCompletedAt(completedAt).Save(txCtx)
	if err != nil {
		return fmt.Errorf("complete security deposit payment order: %w", err)
	}
	if updated != 1 {
		return infraerrors.Conflict("CONFLICT", "security deposit fulfillment lease was lost")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit security deposit fulfillment: %w", err)
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, order.UserID)
	}
	s.writeAuditLog(ctx, order.ID, "SECURITY_DEPOSIT_CREDITED", "system", map[string]any{
		"principal_cents": snapshot.PrincipalCents,
		"group_id":        snapshot.GroupID,
		"locked_until":    lockedUntil,
	})
	return nil
}

func securityDepositSnapshotFromOrder(order *dbent.PaymentOrder) (*SecurityDepositOrderSnapshot, error) {
	if order == nil || order.OrderType != payment.OrderTypeSecurityDeposit {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ORDER", "invalid security deposit order")
	}
	value, ok := order.ProviderSnapshot["security_deposit"]
	if !ok {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ORDER", "security deposit snapshot is missing")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ORDER", "security deposit snapshot is invalid")
	}
	var snapshot SecurityDepositOrderSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ORDER", "security deposit snapshot is invalid")
	}
	if snapshot.SchemaVersion != 1 || snapshot.GroupID <= 0 || snapshot.AgreementID <= 0 ||
		snapshot.PrincipalCents <= 0 || snapshot.BaseRequiredCents < 0 || snapshot.RiskMultiplier < 1 ||
		snapshot.RequiredCents < snapshot.PrincipalCents || snapshot.FreezeHours < 0 ||
		snapshot.FreezeHours > maxSecurityDepositFreezeHours || snapshot.Currency != payment.DefaultPaymentCurrency ||
		strings.TrimSpace(snapshot.PolicyVersion) == "" || strings.TrimSpace(snapshot.ContentHash) == "" ||
		!snapshot.ProviderRefundEnabled {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ORDER", "security deposit snapshot failed validation")
	}
	return &snapshot, nil
}
