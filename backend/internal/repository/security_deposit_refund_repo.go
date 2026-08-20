package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GetSecurityDepositRefund 按稳定业务 ID 返回退款状态，未创建时返回 nil。
func (r *securityDepositRepository) GetSecurityDepositRefund(ctx context.Context, refundID string) (*service.SecurityDepositRefundRecord, error) {
	return loadSecurityDepositRefund(ctx, r.db, strings.TrimSpace(refundID), false)
}

// GetSecurityDepositRefundTarget 返回实付批次当前全部可预留本金。
func (r *securityDepositRepository) GetSecurityDepositRefundTarget(ctx context.Context, userID, lotID int64) (*service.SecurityDepositRefundTarget, error) {
	target := &service.SecurityDepositRefundTarget{UserID: userID, LotID: lotID}
	var bucketType, sourceType, refundPolicy, currency string
	var paymentOrderID sql.NullInt64
	var lockedUntil sql.NullTime
	var remaining, reserved int64
	err := r.db.QueryRowContext(ctx, `
SELECT bucket_type, source_type, payment_order_id, remaining_cents, refund_reserved_cents,
       refund_policy, currency, locked_until
FROM security_deposit_lots
WHERE id = $1 AND user_id = $2`, lotID, userID).Scan(
		&bucketType, &sourceType, &paymentOrderID, &remaining, &reserved, &refundPolicy, &currency, &lockedUntil,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infraerrors.NotFound("SECURITY_DEPOSIT_LOT_NOT_FOUND", "security deposit lot not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load security deposit refund target: %w", err)
	}
	if bucketType != "paid" || sourceType != "payment" || refundPolicy != "timed_original_channel" || !paymentOrderID.Valid {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_LOT_NOT_REFUNDABLE", "only user-paid security deposit lots can be refunded")
	}
	if reserved > 0 {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_ALREADY_ACTIVE", "this security deposit lot already has a refund in progress")
	}
	if remaining <= 0 {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_LOT_EMPTY", "security deposit lot has no refundable balance")
	}
	target.PaymentOrderID = paymentOrderID.Int64
	target.PrincipalCents = remaining
	target.Currency = currency
	target.LockedUntil = nullableTimePointer(lockedUntil)
	return target, nil
}

// PreviewSecurityDepositRefundImpact 按退款后有效余额预测会被自动禁用的 active 密钥。
func (r *securityDepositRepository) PreviewSecurityDepositRefundImpact(ctx context.Context, userID, principalCents int64, enforcementEnabled bool) ([]service.SecurityDepositRefundImpact, error) {
	if !enforcementEnabled {
		return []service.SecurityDepositRefundImpact{}, nil
	}
	var balanceAfter, multiplier int64
	err := r.db.QueryRowContext(ctx, `
SELECT GREATEST(
           COALESCE(SUM(a.balance_cents), 0)
           - COALESCE(SUM(CASE WHEN a.bucket_type = 'paid' THEN a.refund_reserved_cents ELSE 0 END), 0)
           - $2,
           0
       ),
       COALESCE((SELECT risk_multiplier FROM security_deposit_risk_profiles WHERE user_id = $1), 1)
FROM security_deposit_accounts AS a
WHERE a.user_id = $1`, userID, principalCents).Scan(&balanceAfter, &multiplier)
	if err != nil {
		return nil, fmt.Errorf("calculate security deposit refund impact balance: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT ak.id, ak.name, g.id, g.name,
       (g.security_deposit_base_required_cents::numeric * $2::numeric)::bigint
FROM api_keys AS ak
JOIN groups AS g ON g.id = ak.group_id
WHERE ak.user_id = $1
  AND ak.status = $3
  AND ak.deleted_at IS NULL
  AND g.security_deposit_base_required_cents > 0
  AND (g.security_deposit_base_required_cents::numeric * $2::numeric) > $4::numeric
ORDER BY ak.id`, userID, multiplier, service.StatusAPIKeyActive, balanceAfter)
	if err != nil {
		return nil, fmt.Errorf("query security deposit refund impact keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.SecurityDepositRefundImpact, 0)
	for rows.Next() {
		var item service.SecurityDepositRefundImpact
		if err := rows.Scan(&item.APIKeyID, &item.APIKeyName, &item.GroupID, &item.GroupName, &item.RequiredCents); err != nil {
			return nil, fmt.Errorf("scan security deposit refund impact key: %w", err)
		}
		item.BalanceAfterCents = balanceAfter
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate security deposit refund impact keys: %w", err)
	}
	return result, nil
}

// ReserveSecurityDepositRefund 原子建立预留并禁用余额不足的 active 密钥。
func (r *securityDepositRepository) ReserveSecurityDepositRefund(ctx context.Context, input service.SecurityDepositRefundReserveInput, enforcementEnabled bool) (*service.SecurityDepositRefundRecord, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin security deposit refund reserve: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	multiplier, err := lockSecurityDepositAdminUserAndRisk(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	if existing, err := loadSecurityDepositRefund(ctx, tx, input.RefundID, true); err != nil {
		return nil, err
	} else if existing != nil {
		if existing.UserID != input.UserID || existing.LotID != input.LotID || existing.Mode != input.Mode || existing.PrincipalCents != input.PrincipalCents {
			return nil, infraerrors.Conflict("IDEMPOTENCY_KEY_REUSED", "idempotency key was already used with different security deposit refund parameters")
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit duplicate security deposit refund reserve: %w", err)
		}
		existing.AlreadyProcessed = true
		return existing, nil
	}
	balances, reservedBalances, err := lockSecurityDepositPenaltyAccounts(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	if err := lockAllSecurityDepositLots(ctx, tx, input.UserID); err != nil {
		return nil, err
	}
	var bucketType, sourceType, refundPolicy, currency string
	var paymentOrderID sql.NullInt64
	var lockedUntil sql.NullTime
	var remaining, lotReserved int64
	err = tx.QueryRowContext(ctx, `
SELECT bucket_type, source_type, payment_order_id, remaining_cents, refund_reserved_cents,
       refund_policy, currency, locked_until
FROM security_deposit_lots
WHERE id = $1 AND user_id = $2
FOR UPDATE`, input.LotID, input.UserID).Scan(
		&bucketType, &sourceType, &paymentOrderID, &remaining, &lotReserved, &refundPolicy, &currency, &lockedUntil,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infraerrors.NotFound("SECURITY_DEPOSIT_LOT_NOT_FOUND", "security deposit lot not found")
	}
	if err != nil {
		return nil, fmt.Errorf("lock security deposit refund lot: %w", err)
	}
	if bucketType != "paid" || sourceType != "payment" || refundPolicy != "timed_original_channel" || !paymentOrderID.Valid {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_LOT_NOT_REFUNDABLE", "only user-paid security deposit lots can be refunded")
	}
	if input.RequireUnlocked && lockedUntil.Valid && time.Now().UTC().Before(lockedUntil.Time.UTC()) {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_FROZEN", "security deposit lot is still within its refund freeze period")
	}
	if paymentOrderID.Int64 != input.PaymentOrderID || remaining != input.PrincipalCents || currency != input.GatewayCurrency {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_QUOTE_CHANGED", "security deposit refund amount changed before reservation")
	}
	if lotReserved != 0 {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_ALREADY_ACTIVE", "this security deposit lot already has a refund in progress")
	}
	var activeRefundID string
	err = tx.QueryRowContext(ctx, `
SELECT refund_id
FROM security_deposit_refunds
WHERE lot_id = $1 AND state IN ('reserved', 'submitting', 'pending', 'manual_review')
ORDER BY id DESC
LIMIT 1
FOR UPDATE`, input.LotID).Scan(&activeRefundID)
	if err == nil {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_ALREADY_ACTIVE", "this security deposit lot already has a refund in progress")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("check active security deposit refund: %w", err)
	}
	now := time.Now().UTC()
	state := service.SecurityDepositRefundStateReserved
	if input.Mode == service.SecurityDepositRefundModeManual {
		state = service.SecurityDepositRefundStateManualReview
	}
	var refundRowID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO security_deposit_refunds (
    refund_id, user_id, lot_id, payment_order_id, principal_cents,
    gateway_amount, gateway_currency, mode, state, requested_by, reason,
    quote_hash, idempotency_key, provider_request_id, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING id`,
		input.RefundID, input.UserID, input.LotID, input.PaymentOrderID, input.PrincipalCents,
		input.GatewayAmount, input.GatewayCurrency, input.Mode, state, input.OperatorID, input.Reason,
		input.QuoteHash, input.IdempotencyKey, input.ProviderRequestID, now,
	).Scan(&refundRowID)
	if err != nil {
		return nil, fmt.Errorf("insert security deposit refund: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_lots
SET refund_reserved_cents = $1, status = 'refund_reserved', updated_at = $2
WHERE id = $3`, input.PrincipalCents, now, input.LotID); err != nil {
		return nil, fmt.Errorf("reserve security deposit refund lot: %w", err)
	}
	reservedBalances["paid"] += input.PrincipalCents
	if reservedBalances["paid"] > balances["paid"] {
		return nil, fmt.Errorf("security deposit paid account reservation exceeds balance")
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_accounts
SET refund_reserved_cents = $1, version = version + 1, updated_at = $2
WHERE user_id = $3 AND bucket_type = 'paid'`, reservedBalances["paid"], now, input.UserID); err != nil {
		return nil, fmt.Errorf("reserve security deposit paid account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_ledger (
    user_id, lot_id, bucket_type, entry_type, delta_cents, reserved_delta_cents,
    bucket_balance_after_cents, bucket_reserved_after_cents, refund_id, payment_order_id,
    operator_id, reason, idempotency_key, created_at
) VALUES ($1, $2, 'paid', 'refund_reserve', 0, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		input.UserID, input.LotID, input.PrincipalCents, balances["paid"], reservedBalances["paid"],
		refundRowID, input.PaymentOrderID, input.OperatorID, input.Reason,
		fmt.Sprintf("%s:reserve", input.IdempotencyKey), now); err != nil {
		return nil, fmt.Errorf("insert security deposit refund reserve ledger: %w", err)
	}
	disabled, err := disableInsufficientSecurityDepositKeysTx(ctx, tx, input.UserID, multiplier, balances, reservedBalances, enforcementEnabled, "refund_reserve", refundRowID, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit security deposit refund reserve: %w", err)
	}
	result, err := loadSecurityDepositRefund(ctx, r.db, input.RefundID, false)
	if result != nil {
		result.DisabledKeyIDs = disabled
	}
	return result, err
}

// ClaimAutomaticSecurityDepositRefund 在网关调用前把 reserved 原子推进为 submitting。
func (r *securityDepositRepository) ClaimAutomaticSecurityDepositRefund(ctx context.Context, refundID string) (*service.SecurityDepositRefundRecord, bool, error) {
	now := time.Now().UTC()
	result, err := r.db.ExecContext(ctx, `
UPDATE security_deposit_refunds
SET state = 'submitting', submitted_at = COALESCE(submitted_at, $1)
WHERE refund_id = $2 AND mode = 'automatic_original_channel' AND state = 'reserved'`, now, refundID)
	if err != nil {
		return nil, false, fmt.Errorf("claim automatic security deposit refund: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("read automatic security deposit refund claim: %w", err)
	}
	record, err := loadSecurityDepositRefund(ctx, r.db, refundID, false)
	if err != nil {
		return nil, false, err
	}
	if record == nil {
		return nil, false, infraerrors.NotFound("SECURITY_DEPOSIT_REFUND_NOT_FOUND", "security deposit refund not found")
	}
	return record, count == 1, nil
}

// ClaimAutomaticSecurityDepositRefundQuery 抢占 pending/unknown 查询，防止并发重复查询网关。
func (r *securityDepositRepository) ClaimAutomaticSecurityDepositRefundQuery(ctx context.Context, refundID string, userID int64) (*service.SecurityDepositRefundRecord, string, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, "", false, fmt.Errorf("begin security deposit refund query claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := loadSecurityDepositRefund(ctx, tx, refundID, true)
	if err != nil {
		return nil, "", false, err
	}
	if record == nil || record.UserID != userID {
		return nil, "", false, infraerrors.NotFound("SECURITY_DEPOSIT_REFUND_NOT_FOUND", "security deposit refund not found")
	}
	if record.Mode != service.SecurityDepositRefundModeAutomatic {
		return nil, "", false, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_MODE_MISMATCH", "refund is not an automatic original-channel refund")
	}
	previousState := record.State
	if previousState != service.SecurityDepositRefundStatePending && previousState != service.SecurityDepositRefundStateManualReview {
		if err := tx.Commit(); err != nil {
			return nil, "", false, err
		}
		return record, previousState, false, nil
	}
	result, err := tx.ExecContext(ctx, `
UPDATE security_deposit_refunds
SET state = 'submitting'
WHERE id = $1 AND state = $2`, record.ID, previousState)
	if err != nil {
		return nil, "", false, fmt.Errorf("claim security deposit refund query: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, "", false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", false, fmt.Errorf("commit security deposit refund query claim: %w", err)
	}
	record.State = service.SecurityDepositRefundStateSubmitting
	return record, previousState, count == 1, nil
}

// FinalizeAutomaticSecurityDepositRefund 根据网关确定性结果核销、释放或保留预留。
func (r *securityDepositRepository) FinalizeAutomaticSecurityDepositRefund(ctx context.Context, refundID, state, providerRefundID string, snapshot map[string]any) (*service.SecurityDepositRefundRecord, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin automatic security deposit refund finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := loadSecurityDepositRefund(ctx, tx, refundID, true)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, infraerrors.NotFound("SECURITY_DEPOSIT_REFUND_NOT_FOUND", "security deposit refund not found")
	}
	if record.State != service.SecurityDepositRefundStateSubmitting {
		record.AlreadyProcessed = true
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return record, nil
	}
	if record.Mode != service.SecurityDepositRefundModeAutomatic {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_MODE_MISMATCH", "refund is not an automatic original-channel refund")
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal security deposit refund provider response: %w", err)
	}
	now := time.Now().UTC()
	switch state {
	case service.SecurityDepositRefundStateSucceeded:
		if err := finalizeSecurityDepositRefundSuccessTx(ctx, tx, record, now, nil); err != nil {
			return nil, err
		}
	case service.SecurityDepositRefundStateFailedReleased:
		if err := releaseSecurityDepositRefundTx(ctx, tx, record, state, now, "automatic gateway failure", fmt.Sprintf("security_deposit:refund:%d:release", record.ID)); err != nil {
			return nil, err
		}
	case service.SecurityDepositRefundStatePending, service.SecurityDepositRefundStateManualReview:
		if _, err := tx.ExecContext(ctx, `
UPDATE payment_orders
SET refund_amount = CASE WHEN status = $1 THEN refund_amount ELSE refund_amount + $2 END,
    status = $1, refund_reason = $3,
    force_refund = TRUE, refund_at = NULL, failed_at = NULL, failed_reason = NULL
WHERE id = $4`, service.OrderStatusRefundPending, float64(record.PrincipalCents)/100, nullableStringValue(record.Reason), record.PaymentOrderID); err != nil {
			return nil, fmt.Errorf("mark security deposit payment order refund pending: %w", err)
		}
	default:
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND_STATE", "unsupported automatic security deposit refund state")
	}
	completedAt := any(nil)
	if state == service.SecurityDepositRefundStateSucceeded || state == service.SecurityDepositRefundStateFailedReleased {
		completedAt = now
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_refunds
SET state = $1, provider_response_snapshot = $2, external_refund_id = NULL,
    completed_at = $3
WHERE id = $4`, state, snapshotJSON, completedAt, record.ID); err != nil {
		return nil, fmt.Errorf("update automatic security deposit refund state: %w", err)
	}
	if strings.TrimSpace(providerRefundID) != "" {
		if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_refunds
SET provider_response_snapshot = COALESCE(provider_response_snapshot, '{}'::jsonb) || jsonb_build_object('refund_id', $1::text)
WHERE id = $2`, providerRefundID, record.ID); err != nil {
			return nil, fmt.Errorf("store security deposit provider refund id: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit automatic security deposit refund finalization: %w", err)
	}
	return loadSecurityDepositRefund(ctx, r.db, refundID, false)
}

// CompleteManualSecurityDepositRefund 校验外部退款事实后核销人工预留。
func (r *securityDepositRepository) CompleteManualSecurityDepositRefund(ctx context.Context, input service.AdminSecurityDepositManualCompleteInput) (*service.SecurityDepositRefundRecord, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin manual security deposit refund completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := loadSecurityDepositRefund(ctx, tx, input.RefundID, true)
	if err != nil {
		return nil, err
	}
	if record == nil || record.UserID != input.UserID {
		return nil, infraerrors.NotFound("SECURITY_DEPOSIT_REFUND_NOT_FOUND", "security deposit refund not found")
	}
	if record.Mode != service.SecurityDepositRefundModeManual && record.Mode != service.SecurityDepositRefundModeAutomatic {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_MODE_MISMATCH", "refund mode does not support manual evidence completion")
	}
	if record.State == service.SecurityDepositRefundStateSucceeded {
		if record.ExternalRefundID == nil || *record.ExternalRefundID != input.ExternalRefundID || record.PrincipalCents != input.ExternalAmountCents {
			return nil, infraerrors.Conflict("IDEMPOTENCY_KEY_REUSED", "manual refund was already completed with different external facts")
		}
		record.AlreadyProcessed = true
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return record, nil
	}
	if record.State != service.SecurityDepositRefundStateManualReview {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_NOT_COMPLETABLE", "manual refund is not awaiting external confirmation")
	}
	if record.PrincipalCents != input.ExternalAmountCents {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_EXTERNAL_REFUND_AMOUNT_MISMATCH", "external refund amount must equal the reserved principal")
	}
	evidence, err := json.Marshal(input.ExternalEvidence)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_EXTERNAL_EVIDENCE", "external refund evidence is invalid").WithCause(err)
	}
	now := time.Now().UTC()
	if err := finalizeSecurityDepositRefundSuccessTx(ctx, tx, record, now, &input); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_refunds
SET state = 'succeeded', external_refund_id = $1, external_refunded_at = $2,
    external_evidence = $3, completed_at = $4
WHERE id = $5`, input.ExternalRefundID, input.ExternalRefundedAt.UTC(), evidence, now, record.ID); err != nil {
		if isSecurityDepositUniqueViolation(err) {
			return nil, infraerrors.Conflict("SECURITY_DEPOSIT_EXTERNAL_REFUND_ID_REUSED", "external refund id has already been used")
		}
		return nil, fmt.Errorf("complete manual security deposit refund: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit manual security deposit refund completion: %w", err)
	}
	return loadSecurityDepositRefund(ctx, r.db, input.RefundID, false)
}

// CancelSecurityDepositRefund 释放尚未提交网关的人工预留；自动 pending/unknown 必须留待查询或核验。
func (r *securityDepositRepository) CancelSecurityDepositRefund(ctx context.Context, input service.AdminSecurityDepositRefundCancelInput) (*service.SecurityDepositRefundRecord, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin security deposit refund cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := loadSecurityDepositRefund(ctx, tx, input.RefundID, true)
	if err != nil {
		return nil, err
	}
	if record == nil || record.UserID != input.UserID {
		return nil, infraerrors.NotFound("SECURITY_DEPOSIT_REFUND_NOT_FOUND", "security deposit refund not found")
	}
	if record.State == service.SecurityDepositRefundStateCanceled || record.State == service.SecurityDepositRefundStateFailedReleased {
		record.AlreadyProcessed = true
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return record, nil
	}
	if record.Mode != service.SecurityDepositRefundModeManual || record.State != service.SecurityDepositRefundStateManualReview {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_CANNOT_CANCEL", "submitted or completed security deposit refunds cannot be canceled")
	}
	now := time.Now().UTC()
	if err := releaseSecurityDepositRefundTx(ctx, tx, record, service.SecurityDepositRefundStateCanceled, now, input.Reason, fmt.Sprintf("%s:cancel", service.SecurityDepositAdminActionKey("refund_cancel", input.UserID, input.IdempotencyKey))); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit security deposit refund cancellation: %w", err)
	}
	return loadSecurityDepositRefund(ctx, r.db, input.RefundID, false)
}

// FailAutomaticSecurityDepositRefundReview 仅在管理员凭证确认网关未退款后释放 unknown 预留。
func (r *securityDepositRepository) FailAutomaticSecurityDepositRefundReview(ctx context.Context, input service.AdminSecurityDepositAutomaticReviewFailureInput) (*service.SecurityDepositRefundRecord, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin automatic security deposit refund review failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := loadSecurityDepositRefund(ctx, tx, input.RefundID, true)
	if err != nil {
		return nil, err
	}
	if record == nil || record.UserID != input.UserID {
		return nil, infraerrors.NotFound("SECURITY_DEPOSIT_REFUND_NOT_FOUND", "security deposit refund not found")
	}
	if record.Mode != service.SecurityDepositRefundModeAutomatic {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_MODE_MISMATCH", "refund is not an automatic original-channel refund")
	}
	if record.State == service.SecurityDepositRefundStateFailedReleased {
		record.AlreadyProcessed = true
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return record, nil
	}
	if record.State != service.SecurityDepositRefundStateManualReview {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_NOT_REVIEWABLE", "refund is not awaiting manual review")
	}
	evidence, err := json.Marshal(input.Evidence)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND_REVIEW_EVIDENCE", "refund review evidence is invalid").WithCause(err)
	}
	now := time.Now().UTC()
	record.RequestedBy = &input.OperatorID
	if err := releaseSecurityDepositRefundTx(ctx, tx, record, service.SecurityDepositRefundStateFailedReleased, now,
		nullableStringValue(input.Reason), fmt.Sprintf("%s:failed", service.SecurityDepositAdminActionKey("refund_review", input.UserID, input.IdempotencyKey))); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_refunds
SET provider_response_snapshot = COALESCE(provider_response_snapshot, '{}'::jsonb)
        || jsonb_build_object('manual_review', jsonb_build_object(
            'outcome', 'failed', 'evidence', $1::jsonb, 'operator_id', $2::bigint,
            'reviewed_at', $3::timestamptz, 'reason', $4::text
        )),
    reason = $4
WHERE id = $5`, string(evidence), input.OperatorID, now, nullableStringValue(input.Reason), record.ID); err != nil {
		return nil, fmt.Errorf("store automatic security deposit refund review evidence: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit automatic security deposit refund review failure: %w", err)
	}
	return loadSecurityDepositRefund(ctx, r.db, input.RefundID, false)
}

func finalizeSecurityDepositRefundSuccessTx(ctx context.Context, tx *sql.Tx, record *service.SecurityDepositRefundRecord, now time.Time, manual *service.AdminSecurityDepositManualCompleteInput) error {
	operatorID := nullableInt64Value(record.RequestedBy)
	reason := nullableStringValue(record.Reason)
	ledgerKey := fmt.Sprintf("security_deposit:refund:%d:success", record.ID)
	if manual != nil {
		operatorID = manual.OperatorID
		reason = manual.Reason
		ledgerKey = fmt.Sprintf("%s:success", service.SecurityDepositAdminActionKey("manual_refund_complete", manual.UserID, manual.IdempotencyKey))
	}
	var lotRemaining, lotReserved int64
	if err := tx.QueryRowContext(ctx, `
SELECT remaining_cents, refund_reserved_cents
FROM security_deposit_lots
WHERE id = $1 AND user_id = $2
FOR UPDATE`, record.LotID, record.UserID).Scan(&lotRemaining, &lotReserved); err != nil {
		return fmt.Errorf("lock successful security deposit refund lot: %w", err)
	}
	if lotReserved != record.PrincipalCents || lotRemaining < record.PrincipalCents {
		return infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_RESERVATION_MISMATCH", "security deposit refund reservation no longer matches the lot")
	}
	var balance, reserved int64
	if err := tx.QueryRowContext(ctx, `
SELECT balance_cents, refund_reserved_cents
FROM security_deposit_accounts
WHERE user_id = $1 AND bucket_type = 'paid'
FOR UPDATE`, record.UserID).Scan(&balance, &reserved); err != nil {
		return fmt.Errorf("lock successful security deposit refund account: %w", err)
	}
	if balance < record.PrincipalCents || reserved < record.PrincipalCents {
		return infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_ACCOUNT_MISMATCH", "security deposit paid account no longer matches the refund reservation")
	}
	newRemaining := lotRemaining - record.PrincipalCents
	status := "active"
	if newRemaining == 0 {
		status = "refunded"
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_lots
SET remaining_cents = remaining_cents - $1,
    refund_reserved_cents = refund_reserved_cents - $1,
    refunded_cents = refunded_cents + $1,
    status = $2, updated_at = $3
WHERE id = $4`, record.PrincipalCents, status, now, record.LotID); err != nil {
		return fmt.Errorf("finalize successful security deposit refund lot: %w", err)
	}
	balance -= record.PrincipalCents
	reserved -= record.PrincipalCents
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_accounts
SET balance_cents = $1, refund_reserved_cents = $2, version = version + 1, updated_at = $3
WHERE user_id = $4 AND bucket_type = 'paid'`, balance, reserved, now, record.UserID); err != nil {
		return fmt.Errorf("finalize successful security deposit refund account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_ledger (
    user_id, lot_id, bucket_type, entry_type, delta_cents, reserved_delta_cents,
    bucket_balance_after_cents, bucket_reserved_after_cents, refund_id, payment_order_id,
    operator_id, reason, idempotency_key, created_at
) VALUES ($1, $2, 'paid', 'refund_success', $3, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		record.UserID, record.LotID, -record.PrincipalCents, balance, reserved,
		record.ID, record.PaymentOrderID, operatorID, reason, ledgerKey, now); err != nil {
		return fmt.Errorf("insert successful security deposit refund ledger: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE payment_orders
SET refund_amount = CASE WHEN status = $2 THEN refund_amount ELSE refund_amount + $1 END,
    status = CASE
        WHEN (CASE WHEN status = $2 THEN refund_amount ELSE refund_amount + $1 END) >= amount - 0.000001 THEN $3
        ELSE $4
    END,
    refund_reason = $5, refund_at = $6, force_refund = TRUE,
    failed_at = NULL, failed_reason = NULL
WHERE id = $7`, float64(record.PrincipalCents)/100, service.OrderStatusRefundPending,
		service.OrderStatusRefunded, service.OrderStatusPartiallyRefunded, reason, now, record.PaymentOrderID); err != nil {
		return fmt.Errorf("finalize security deposit payment order refund: %w", err)
	}
	return nil
}

func releaseSecurityDepositRefundTx(ctx context.Context, tx *sql.Tx, record *service.SecurityDepositRefundRecord, state string, now time.Time, reason any, ledgerKey string) error {
	var lotReserved int64
	if err := tx.QueryRowContext(ctx, `
SELECT refund_reserved_cents FROM security_deposit_lots
WHERE id = $1 AND user_id = $2 FOR UPDATE`, record.LotID, record.UserID).Scan(&lotReserved); err != nil {
		return fmt.Errorf("lock released security deposit refund lot: %w", err)
	}
	if lotReserved != record.PrincipalCents {
		return infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_RESERVATION_MISMATCH", "security deposit refund reservation no longer matches the lot")
	}
	var balance, reserved int64
	if err := tx.QueryRowContext(ctx, `
SELECT balance_cents, refund_reserved_cents FROM security_deposit_accounts
WHERE user_id = $1 AND bucket_type = 'paid' FOR UPDATE`, record.UserID).Scan(&balance, &reserved); err != nil {
		return fmt.Errorf("lock released security deposit refund account: %w", err)
	}
	if reserved < record.PrincipalCents {
		return infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_ACCOUNT_MISMATCH", "security deposit paid account no longer matches the refund reservation")
	}
	reserved -= record.PrincipalCents
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_lots
SET refund_reserved_cents = 0, status = 'active', updated_at = $1
WHERE id = $2`, now, record.LotID); err != nil {
		return fmt.Errorf("release security deposit refund lot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_accounts
SET refund_reserved_cents = $1, version = version + 1, updated_at = $2
WHERE user_id = $3 AND bucket_type = 'paid'`, reserved, now, record.UserID); err != nil {
		return fmt.Errorf("release security deposit refund account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_ledger (
    user_id, lot_id, bucket_type, entry_type, delta_cents, reserved_delta_cents,
    bucket_balance_after_cents, bucket_reserved_after_cents, refund_id, payment_order_id,
    operator_id, reason, idempotency_key, created_at
) VALUES ($1, $2, 'paid', 'refund_release', 0, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		record.UserID, record.LotID, -record.PrincipalCents, balance, reserved,
		record.ID, record.PaymentOrderID, nullableInt64Value(record.RequestedBy), reason, ledgerKey, now); err != nil {
		return fmt.Errorf("insert released security deposit refund ledger: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_refunds SET state = $1, completed_at = $2 WHERE id = $3`, state, now, record.ID); err != nil {
		return fmt.Errorf("mark security deposit refund released: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE payment_orders
SET refund_amount = GREATEST(refund_amount - $1, 0),
    status = CASE
        WHEN GREATEST(refund_amount - $1, 0) <= 0.000001 THEN $2
        WHEN GREATEST(refund_amount - $1, 0) >= amount - 0.000001 THEN $3
        ELSE $4
    END,
    refund_at = CASE WHEN GREATEST(refund_amount - $1, 0) <= 0.000001 THEN NULL ELSE refund_at END
WHERE id = $5 AND status = $6`, float64(record.PrincipalCents)/100,
		service.OrderStatusCompleted, service.OrderStatusRefunded, service.OrderStatusPartiallyRefunded,
		record.PaymentOrderID, service.OrderStatusRefundPending); err != nil {
		return fmt.Errorf("restore security deposit payment order after released refund: %w", err)
	}
	return nil
}

type securityDepositRefundQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadSecurityDepositRefund(ctx context.Context, queryer securityDepositRefundQueryer, refundID string, forUpdate bool) (*service.SecurityDepositRefundRecord, error) {
	if strings.TrimSpace(refundID) == "" {
		return nil, nil
	}
	query := `
SELECT id, refund_id, user_id, lot_id, payment_order_id, principal_cents,
       gateway_amount, gateway_currency, mode, state, requested_by, reason,
       provider_request_id, provider_response_snapshot, external_refund_id,
       external_refunded_at, external_evidence, created_at, submitted_at, completed_at
FROM security_deposit_refunds
WHERE refund_id = $1`
	if forUpdate {
		query += " FOR UPDATE"
	}
	record := &service.SecurityDepositRefundRecord{DisabledKeyIDs: []int64{}}
	var requestedBy sql.NullInt64
	var reason, providerRequestID, externalRefundID sql.NullString
	var externalRefundedAt, submittedAt, completedAt sql.NullTime
	var providerSnapshot, externalEvidence []byte
	err := queryer.QueryRowContext(ctx, query, refundID).Scan(
		&record.ID, &record.RefundID, &record.UserID, &record.LotID, &record.PaymentOrderID,
		&record.PrincipalCents, &record.GatewayAmount, &record.GatewayCurrency, &record.Mode,
		&record.State, &requestedBy, &reason, &providerRequestID, &providerSnapshot,
		&externalRefundID, &externalRefundedAt, &externalEvidence, &record.CreatedAt,
		&submittedAt, &completedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load security deposit refund: %w", err)
	}
	record.RequestedBy = nullableInt64Pointer(requestedBy)
	record.Reason = nullableStringPointer(reason)
	record.ProviderRequestID = nullableStringPointer(providerRequestID)
	record.ExternalRefundID = nullableStringPointer(externalRefundID)
	record.ExternalRefundedAt = nullableTimePointer(externalRefundedAt)
	record.SubmittedAt = nullableTimePointer(submittedAt)
	record.CompletedAt = nullableTimePointer(completedAt)
	if len(providerSnapshot) > 0 {
		_ = json.Unmarshal(providerSnapshot, &record.ProviderResponseSnapshot)
	}
	if len(externalEvidence) > 0 {
		_ = json.Unmarshal(externalEvidence, &record.ExternalEvidence)
	}
	return record, nil
}

func lockAllSecurityDepositLots(ctx context.Context, tx *sql.Tx, userID int64) error {
	rows, err := tx.QueryContext(ctx, `
SELECT id FROM security_deposit_lots
WHERE user_id = $1
ORDER BY created_at, id
FOR UPDATE`, userID)
	if err != nil {
		return fmt.Errorf("lock security deposit refund lots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan security deposit refund lot lock: %w", err)
		}
	}
	return rows.Err()
}

func nullableStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableInt64Pointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	return &value.Int64
}

func nullableTimePointer(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func nullableStringValue(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}

func nullableInt64Value(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func isSecurityDepositUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}
