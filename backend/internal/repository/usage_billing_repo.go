package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type usageBillingRepository struct {
	db *sql.DB
}

func NewUsageBillingRepository(_ *dbent.Client, sqlDB *sql.DB) service.UsageBillingRepository {
	return &usageBillingRepository{db: sqlDB}
}

func (r *usageBillingRepository) Apply(ctx context.Context, cmd *service.UsageBillingCommand) (_ *service.UsageBillingApplyResult, err error) {
	if cmd == nil {
		return &service.UsageBillingApplyResult{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}

	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingKey(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &service.UsageBillingApplyResult{Applied: false}, nil
	}

	result := &service.UsageBillingApplyResult{Applied: true}
	if err := r.applyUsageBillingEffects(ctx, tx, cmd, result); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func (r *usageBillingRepository) claimUsageBillingKey(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand) (bool, error) {
	return r.claimUsageBillingRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
}

func (r *usageBillingRepository) claimUsageBillingRequest(ctx context.Context, tx *sql.Tx, requestID string, apiKeyID int64, requestFingerprint string) (bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO usage_billing_dedup (request_id, api_key_id, request_fingerprint)
		VALUES ($1, $2, $3)
		ON CONFLICT (request_id, api_key_id) DO NOTHING
		RETURNING id
	`, requestID, apiKeyID, requestFingerprint).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		var existingFingerprint string
		if err := tx.QueryRowContext(ctx, `
			SELECT request_fingerprint
			FROM usage_billing_dedup
			WHERE request_id = $1 AND api_key_id = $2
		`, requestID, apiKeyID).Scan(&existingFingerprint); err != nil {
			return false, err
		}
		if strings.TrimSpace(existingFingerprint) != strings.TrimSpace(requestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var archivedFingerprint string
	err = tx.QueryRowContext(ctx, `
		SELECT request_fingerprint
		FROM usage_billing_dedup_archive
		WHERE request_id = $1 AND api_key_id = $2
	`, requestID, apiKeyID).Scan(&archivedFingerprint)
	if err == nil {
		if strings.TrimSpace(archivedFingerprint) != strings.TrimSpace(requestFingerprint) {
			return false, service.ErrUsageBillingRequestConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return true, nil
}

func (r *usageBillingRepository) ReserveBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageBalanceHold(ctx, cmd, reserveUsageBillingBatchImageBalance)
}

func (r *usageBillingRepository) CaptureBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageBalanceHold(ctx, cmd, captureUsageBillingBatchImageBalance)
}

func (r *usageBillingRepository) ReleaseBatchImageBalance(ctx context.Context, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	return r.applyBatchImageBalanceHold(ctx, cmd, releaseUsageBillingBatchImageBalance)
}

func (r *usageBillingRepository) applyBatchImageBalanceHold(
	ctx context.Context,
	cmd *service.BatchImageBalanceHoldCommand,
	apply func(context.Context, *sql.Tx, *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error),
) (_ *service.BatchImageBalanceHoldResult, err error) {
	if cmd == nil {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	cmd.Normalize()
	if cmd.RequestID == "" {
		return nil, service.ErrUsageBillingRequestIDRequired
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	applied, err := r.claimUsageBillingRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID, cmd.RequestFingerprint)
	if err != nil {
		return nil, err
	}
	if !applied {
		return &service.BatchImageBalanceHoldResult{Applied: false}, nil
	}

	result, err := apply(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	if result == nil {
		result = &service.BatchImageBalanceHoldResult{}
	}
	result.Applied = true

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return result, nil
}

func (r *usageBillingRepository) applyUsageBillingEffects(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, result *service.UsageBillingApplyResult) error {
	if cmd.SubscriptionCost > 0 && cmd.SubscriptionID != nil {
		if err := incrementUsageBillingSubscription(ctx, tx, *cmd.SubscriptionID, cmd.SubscriptionCost); err != nil {
			return err
		}
	}

	if cmd.BalanceCost > 0 {
		limitedCost := 0.0
		if cmd.RequestID != "" {
			var err error
			limitedCost, err = deductUsageBillingLimitedCredits(ctx, tx, cmd.UserID, cmd.APIKeyID, cmd.RequestID, cmd.BalanceCost)
			if err != nil {
				return err
			}
		}
		ordinaryCost := clampBillingAmount(cmd.BalanceCost - limitedCost)
		result.LimitedCreditCost = limitedCost
		result.OrdinaryBalanceCost = ordinaryCost
		if ordinaryCost > 0 {
			newBalance, sufficient, err := deductUsageBillingBalance(ctx, tx, cmd.UserID, ordinaryCost)
			if err != nil {
				return err
			}
			result.NewBalance = &newBalance
			result.BalanceOverdrafted = !sufficient
		}
	}

	if cmd.APIKeyQuotaCost > 0 {
		exhausted, err := incrementUsageBillingAPIKeyQuota(ctx, tx, cmd.APIKeyID, cmd.APIKeyQuotaCost)
		if err != nil {
			return err
		}
		result.APIKeyQuotaExhausted = exhausted
	}

	if cmd.APIKeyRateLimitCost > 0 {
		if err := incrementUsageBillingAPIKeyRateLimit(ctx, tx, cmd.APIKeyID, cmd.APIKeyRateLimitCost); err != nil {
			return err
		}
	}

	if cmd.AccountQuotaCost > 0 && (strings.EqualFold(cmd.AccountType, service.AccountTypeAPIKey) || strings.EqualFold(cmd.AccountType, service.AccountTypeBedrock)) {
		quotaState, err := incrementUsageBillingAccountQuota(ctx, tx, cmd.AccountID, cmd.AccountQuotaCost)
		if err != nil {
			return err
		}
		result.QuotaState = quotaState
	}

	return nil
}

func incrementUsageBillingSubscription(ctx context.Context, tx *sql.Tx, subscriptionID int64, costUSD float64) error {
	const updateSQL = `
		UPDATE user_subscriptions us
		SET
			daily_usage_usd = us.daily_usage_usd + $1,
			weekly_usage_usd = us.weekly_usage_usd + $1,
			monthly_usage_usd = us.monthly_usage_usd + $1,
			updated_at = NOW()
		FROM groups g
		WHERE us.id = $2
			AND us.deleted_at IS NULL
			AND us.group_id = g.id
			AND g.deleted_at IS NULL
	`
	res, err := tx.ExecContext(ctx, updateSQL, costUSD, subscriptionID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	return service.ErrSubscriptionNotFound
}

const billingAmountEpsilon = 0.00000001

func clampBillingAmount(amount float64) float64 {
	if amount <= billingAmountEpsilon {
		return 0
	}
	return amount
}

func deductUsageBillingLimitedCredits(ctx context.Context, tx *sql.Tx, userID, apiKeyID int64, requestID string, amount float64) (float64, error) {
	remaining := clampBillingAmount(amount)
	if remaining <= 0 {
		return 0, nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, initial_amount - used_amount - frozen_amount AS available
		FROM user_limited_credit_grants
		WHERE user_id = $1
			AND status = $2
			AND expires_at > NOW()
			AND initial_amount - used_amount - frozen_amount > $3
		ORDER BY expires_at ASC, id ASC
		FOR UPDATE
	`, userID, service.LimitedCreditStatusActive, billingAmountEpsilon)
	if err != nil {
		return 0, err
	}
	type grantAvailability struct {
		id        int64
		available float64
	}
	locked := make([]grantAvailability, 0)
	for rows.Next() {
		var grantID int64
		var available float64
		if err := rows.Scan(&grantID, &available); err != nil {
			_ = rows.Close()
			return 0, err
		}
		locked = append(locked, grantAvailability{id: grantID, available: available})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	deducted := 0.0
	for _, grant := range locked {
		if remaining <= 0 {
			break
		}
		available := clampBillingAmount(grant.available)
		if available <= 0 {
			continue
		}
		part := available
		if part > remaining {
			part = remaining
		}
		if err := consumeLimitedCreditGrant(ctx, tx, userID, grant.id, apiKeyID, requestID, part); err != nil {
			return 0, err
		}
		deducted += part
		remaining = clampBillingAmount(remaining - part)
	}
	return deducted, nil
}

func consumeLimitedCreditGrant(ctx context.Context, tx *sql.Tx, userID, grantID, apiKeyID int64, requestID string, amount float64) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE user_limited_credit_grants
		SET used_amount = used_amount + $1,
			status = CASE
				WHEN initial_amount - (used_amount + $1) - frozen_amount <= $2 THEN $3
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $4 AND user_id = $5
	`, amount, billingAmountEpsilon, service.LimitedCreditStatusDepleted, grantID, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrUserNotFound
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO user_limited_credit_ledger (user_id, grant_id, event_type, amount, request_id, api_key_id)
		VALUES ($1, $2, 'consume', $3, $4, $5)
	`, userID, grantID, amount, requestID, apiKeyID)
	return err
}
func deductUsageBillingBalance(ctx context.Context, tx *sql.Tx, userID int64, amount float64) (float64, bool, error) {
	var newBalance float64
	err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance - $1,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL AND balance >= $1
		RETURNING balance
	`, amount, userID).Scan(&newBalance)
	if err == nil {
		return newBalance, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}

	err = tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance - $1,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING balance
	`, amount, userID).Scan(&newBalance)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, service.ErrUserNotFound
	}
	if err != nil {
		return 0, false, err
	}
	return newBalance, false, nil
}

func reserveUsageBillingBatchImageBalance(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if cmd.HoldAmount <= 0 {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	limitedReserved := 0.0
	if cmd.RequestID != "" && cmd.BatchID != "" {
		var err error
		limitedReserved, err = reserveUsageBillingLimitedCredits(ctx, tx, cmd)
		if err != nil {
			return nil, err
		}
	}
	ordinaryHold := clampBillingAmount(cmd.HoldAmount - limitedReserved)
	result := &service.BatchImageBalanceHoldResult{
		LimitedCreditCost:   limitedReserved,
		OrdinaryBalanceCost: ordinaryHold,
	}
	if ordinaryHold <= 0 {
		return result, nil
	}

	var balance, frozen float64
	err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance - $1,
			frozen_balance = COALESCE(frozen_balance, 0) + $1,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL AND balance >= $1
		RETURNING balance, frozen_balance
	`, ordinaryHold, cmd.UserID).Scan(&balance, &frozen)
	if err == nil {
		result.NewBalance = &balance
		result.FrozenBalance = &frozen
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists, existsErr := userExistsForBilling(ctx, tx, cmd.UserID); existsErr != nil {
		return nil, existsErr
	} else if !exists {
		return nil, service.ErrUserNotFound
	}
	return nil, service.ErrBatchImageInsufficientBalance
}

func captureUsageBillingBatchImageBalance(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if cmd.HoldAmount <= 0 && cmd.ActualAmount <= 0 {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	if cmd.ActualAmount-cmd.HoldAmount > billingAmountEpsilon {
		return nil, service.ErrBatchImageSettlementCostExceedsHold
	}

	limitedReserved := 0.0
	limitedCaptured := 0.0
	if cmd.RequestID != "" && cmd.BatchID != "" {
		limitedResult, err := captureUsageBillingLimitedCredits(ctx, tx, cmd)
		if err != nil {
			return nil, err
		}
		limitedReserved = limitedResult.totalReserved
		limitedCaptured = limitedResult.captured
	}
	ordinaryHold := clampBillingAmount(cmd.HoldAmount - limitedReserved)
	ordinaryActual := clampBillingAmount(cmd.ActualAmount - limitedReserved)
	if ordinaryActual > ordinaryHold {
		ordinaryActual = ordinaryHold
	}
	result := &service.BatchImageBalanceHoldResult{
		LimitedCreditCost:   limitedCaptured,
		OrdinaryBalanceCost: ordinaryActual,
	}
	if ordinaryHold <= 0 {
		return result, nil
	}

	var balance, frozen float64
	err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance
				+ CASE WHEN $1 > $2 THEN $1 - $2 ELSE 0 END
				- CASE WHEN $2 > $1 THEN $2 - $1 ELSE 0 END,
			frozen_balance = COALESCE(frozen_balance, 0) - $1,
			updated_at = NOW()
		WHERE id = $3 AND deleted_at IS NULL AND COALESCE(frozen_balance, 0) >= $1
		RETURNING balance, frozen_balance
	`, ordinaryHold, ordinaryActual, cmd.UserID).Scan(&balance, &frozen)
	if err == nil {
		result.NewBalance = &balance
		result.FrozenBalance = &frozen
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists, existsErr := userExistsForBilling(ctx, tx, cmd.UserID); existsErr != nil {
		return nil, existsErr
	} else if !exists {
		return nil, service.ErrUserNotFound
	}
	return nil, errors.New("batch image frozen balance is insufficient")
}

func releaseUsageBillingBatchImageBalance(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*service.BatchImageBalanceHoldResult, error) {
	if cmd.HoldAmount <= 0 {
		return &service.BatchImageBalanceHoldResult{}, nil
	}
	// 释放前校验该 job 确实预留过 hold（hold request id 已被 claim），
	// 防止从未成功冻结的 job 触发"幻影释放"，从其他用户的冻结资金池中凭空生成余额。
	held, heldErr := batchImageHoldClaimExists(ctx, tx, service.BatchImageHoldRequestID(cmd.BatchID), cmd.APIKeyID)
	if heldErr != nil {
		return nil, heldErr
	}
	if !held {
		logger.LegacyPrintf("repository.usage_billing", "[BatchImage] release skipped, hold was never reserved: batch=%s", cmd.BatchID)
		return &service.BatchImageBalanceHoldResult{}, nil
	}

	limitedReserved := 0.0
	limitedReleased := 0.0
	if cmd.RequestID != "" && cmd.BatchID != "" {
		limitedResult, err := releaseUsageBillingLimitedCredits(ctx, tx, cmd)
		if err != nil {
			return nil, err
		}
		limitedReserved = limitedResult.totalReserved
		limitedReleased = limitedResult.released
	}
	ordinaryHold := clampBillingAmount(cmd.HoldAmount - limitedReserved)
	result := &service.BatchImageBalanceHoldResult{
		LimitedCreditCost:   limitedReleased,
		OrdinaryBalanceCost: ordinaryHold,
	}
	if ordinaryHold <= 0 {
		return result, nil
	}

	var balance, frozen float64
	err := tx.QueryRowContext(ctx, `
		UPDATE users
		SET balance = balance + $1,
			frozen_balance = COALESCE(frozen_balance, 0) - $1,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL AND COALESCE(frozen_balance, 0) >= $1
		RETURNING balance, frozen_balance
	`, ordinaryHold, cmd.UserID).Scan(&balance, &frozen)
	if err == nil {
		result.NewBalance = &balance
		result.FrozenBalance = &frozen
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if exists, existsErr := userExistsForBilling(ctx, tx, cmd.UserID); existsErr != nil {
		return nil, existsErr
	} else if !exists {
		return nil, service.ErrUserNotFound
	}
	return nil, errors.New("batch image frozen balance is insufficient")
}

type limitedCreditBatchAllocation struct {
	grantID       int64
	totalReserved float64
	openAmount    float64
}

type limitedCreditBatchResult struct {
	totalReserved float64
	captured      float64
	released      float64
}

func reserveUsageBillingLimitedCredits(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (float64, error) {
	remaining := clampBillingAmount(cmd.HoldAmount)
	if remaining <= 0 {
		return 0, nil
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, initial_amount - used_amount - frozen_amount AS available
		FROM user_limited_credit_grants
		WHERE user_id = $1
			AND status = $2
			AND expires_at > NOW()
			AND initial_amount - used_amount - frozen_amount > $3
		ORDER BY expires_at ASC, id ASC
		FOR UPDATE
	`, cmd.UserID, service.LimitedCreditStatusActive, billingAmountEpsilon)
	if err != nil {
		return 0, err
	}
	type grantAvailability struct {
		id        int64
		available float64
	}
	locked := make([]grantAvailability, 0)
	for rows.Next() {
		var grantID int64
		var available float64
		if err := rows.Scan(&grantID, &available); err != nil {
			_ = rows.Close()
			return 0, err
		}
		locked = append(locked, grantAvailability{id: grantID, available: available})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	reserved := 0.0
	for _, grant := range locked {
		if remaining <= 0 {
			break
		}
		available := clampBillingAmount(grant.available)
		if available <= 0 {
			continue
		}
		part := available
		if part > remaining {
			part = remaining
		}
		if err := reserveLimitedCreditGrant(ctx, tx, cmd, grant.id, part); err != nil {
			return 0, err
		}
		reserved += part
		remaining = clampBillingAmount(remaining - part)
	}
	return reserved, nil
}

func reserveLimitedCreditGrant(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, grantID int64, amount float64) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE user_limited_credit_grants
		SET frozen_amount = frozen_amount + $1,
			updated_at = NOW()
		WHERE id = $2
			AND user_id = $3
			AND initial_amount - used_amount - frozen_amount + $4 >= $1
	`, amount, grantID, cmd.UserID, billingAmountEpsilon)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("limited credit available amount is insufficient")
	}
	return insertLimitedCreditLedger(ctx, tx, cmd.UserID, grantID, "reserve", amount, cmd.RequestID, cmd.APIKeyID, cmd.BatchID)
}

func captureUsageBillingLimitedCredits(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*limitedCreditBatchResult, error) {
	allocations, totalReserved, err := getLimitedCreditBatchAllocations(ctx, tx, cmd.UserID, cmd.BatchID)
	if err != nil {
		return nil, err
	}
	result := &limitedCreditBatchResult{totalReserved: totalReserved}
	captureRemaining := cmd.ActualAmount
	if captureRemaining > totalReserved {
		captureRemaining = totalReserved
	}
	captureRemaining = clampBillingAmount(captureRemaining)
	for _, allocation := range allocations {
		openAmount := clampBillingAmount(allocation.openAmount)
		if openAmount <= 0 {
			continue
		}
		capturePart := 0.0
		if captureRemaining > 0 {
			capturePart = openAmount
			if capturePart > captureRemaining {
				capturePart = captureRemaining
			}
			if err := captureLimitedCreditGrant(ctx, tx, cmd, allocation.grantID, capturePart); err != nil {
				return nil, err
			}
			result.captured += capturePart
			captureRemaining = clampBillingAmount(captureRemaining - capturePart)
		}
		releasePart := clampBillingAmount(openAmount - capturePart)
		if releasePart > 0 {
			if err := releaseLimitedCreditGrant(ctx, tx, cmd, allocation.grantID, releasePart); err != nil {
				return nil, err
			}
			result.released += releasePart
		}
	}
	return result, nil
}

func releaseUsageBillingLimitedCredits(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand) (*limitedCreditBatchResult, error) {
	allocations, totalReserved, err := getLimitedCreditBatchAllocations(ctx, tx, cmd.UserID, cmd.BatchID)
	if err != nil {
		return nil, err
	}
	result := &limitedCreditBatchResult{totalReserved: totalReserved}
	for _, allocation := range allocations {
		openAmount := clampBillingAmount(allocation.openAmount)
		if openAmount <= 0 {
			continue
		}
		if err := releaseLimitedCreditGrant(ctx, tx, cmd, allocation.grantID, openAmount); err != nil {
			return nil, err
		}
		result.released += openAmount
	}
	return result, nil
}

func getLimitedCreditBatchAllocations(ctx context.Context, tx *sql.Tx, userID int64, batchID string) ([]limitedCreditBatchAllocation, float64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT
			g.id,
			COALESCE(SUM(CASE WHEN l.event_type = 'reserve' THEN l.amount ELSE 0 END), 0) AS total_reserved,
			COALESCE(SUM(CASE
				WHEN l.event_type = 'reserve' THEN l.amount
				WHEN l.event_type IN ('capture', 'release') THEN -l.amount
				ELSE 0
			END), 0) AS open_amount
		FROM user_limited_credit_ledger l
		JOIN user_limited_credit_grants g ON g.id = l.grant_id
		WHERE l.user_id = $1
			AND l.batch_id = $2
		GROUP BY g.id, g.expires_at
		HAVING COALESCE(SUM(CASE WHEN l.event_type = 'reserve' THEN l.amount ELSE 0 END), 0) > $3
		ORDER BY g.expires_at ASC, g.id ASC
	`, userID, batchID, billingAmountEpsilon)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	allocations := make([]limitedCreditBatchAllocation, 0)
	totalReserved := 0.0
	for rows.Next() {
		var allocation limitedCreditBatchAllocation
		if err := rows.Scan(&allocation.grantID, &allocation.totalReserved, &allocation.openAmount); err != nil {
			return nil, 0, err
		}
		allocation.totalReserved = clampBillingAmount(allocation.totalReserved)
		allocation.openAmount = clampBillingAmount(allocation.openAmount)
		totalReserved += allocation.totalReserved
		allocations = append(allocations, allocation)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return allocations, totalReserved, nil
}

func captureLimitedCreditGrant(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, grantID int64, amount float64) error {
	if amount <= 0 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE user_limited_credit_grants
		SET used_amount = used_amount + $1,
			frozen_amount = frozen_amount - $1,
			status = CASE
				WHEN initial_amount - (used_amount + $1) - (frozen_amount - $1) <= $2
					AND frozen_amount - $1 <= $2 THEN $3
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $4 AND user_id = $5 AND frozen_amount + $2 >= $1
	`, amount, billingAmountEpsilon, service.LimitedCreditStatusDepleted, grantID, cmd.UserID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("limited credit frozen amount is insufficient")
	}
	return insertLimitedCreditLedger(ctx, tx, cmd.UserID, grantID, "capture", amount, cmd.RequestID, cmd.APIKeyID, cmd.BatchID)
}

func releaseLimitedCreditGrant(ctx context.Context, tx *sql.Tx, cmd *service.BatchImageBalanceHoldCommand, grantID int64, amount float64) error {
	if amount <= 0 {
		return nil
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE user_limited_credit_grants
		SET frozen_amount = frozen_amount - $1,
			updated_at = NOW()
		WHERE id = $2 AND user_id = $3 AND frozen_amount + $4 >= $1
	`, amount, grantID, cmd.UserID, billingAmountEpsilon)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("limited credit frozen amount is insufficient")
	}
	return insertLimitedCreditLedger(ctx, tx, cmd.UserID, grantID, "release", amount, cmd.RequestID, cmd.APIKeyID, cmd.BatchID)
}

func insertLimitedCreditLedger(ctx context.Context, tx *sql.Tx, userID, grantID int64, eventType string, amount float64, requestID string, apiKeyID int64, batchID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO user_limited_credit_ledger (user_id, grant_id, event_type, amount, request_id, api_key_id, batch_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, userID, grantID, eventType, amount, requestID, apiKeyID, batchID)
	return err
}

// batchImageHoldClaimExists 检查 hold request id 是否已在 dedup（或归档）表中被 claim，
// 即该 batch 的冻结操作确实成功提交过。
func batchImageHoldClaimExists(ctx context.Context, tx *sql.Tx, holdRequestID string, apiKeyID int64) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `
		SELECT 1
		FROM usage_billing_dedup
		WHERE request_id = $1 AND api_key_id = $2
	`, holdRequestID, apiKeyID).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	err = tx.QueryRowContext(ctx, `
		SELECT 1
		FROM usage_billing_dedup_archive
		WHERE request_id = $1 AND api_key_id = $2
	`, holdRequestID, apiKeyID).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func userExistsForBilling(ctx context.Context, tx *sql.Tx, userID int64) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `
		SELECT 1
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`, userID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func incrementUsageBillingAPIKeyQuota(ctx context.Context, tx *sql.Tx, apiKeyID int64, amount float64) (bool, error) {
	var exhausted bool
	err := tx.QueryRowContext(ctx, `
		UPDATE api_keys
		SET quota_used = quota_used + $1,
			status = CASE
				WHEN quota > 0
					AND status = $3
					AND quota_used < quota
					AND quota_used + $1 >= quota
				THEN $4
				ELSE status
			END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING quota > 0 AND quota_used >= quota AND quota_used - $1 < quota
	`, amount, apiKeyID, service.StatusAPIKeyActive, service.StatusAPIKeyQuotaExhausted).Scan(&exhausted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, service.ErrAPIKeyNotFound
	}
	if err != nil {
		return false, err
	}
	return exhausted, nil
}

func incrementUsageBillingAPIKeyRateLimit(ctx context.Context, tx *sql.Tx, apiKeyID int64, cost float64) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE api_keys SET
			usage_5h = CASE WHEN window_5h_start IS NOT NULL AND window_5h_start + INTERVAL '5 hours' <= NOW() THEN $1 ELSE usage_5h + $1 END,
			usage_1d = CASE WHEN window_1d_start IS NOT NULL AND window_1d_start + INTERVAL '24 hours' <= NOW() THEN $1 ELSE usage_1d + $1 END,
			usage_7d = CASE WHEN window_7d_start IS NOT NULL AND window_7d_start + INTERVAL '7 days' <= NOW() THEN $1 ELSE usage_7d + $1 END,
			window_5h_start = CASE WHEN window_5h_start IS NULL OR window_5h_start + INTERVAL '5 hours' <= NOW() THEN NOW() ELSE window_5h_start END,
			window_1d_start = CASE WHEN window_1d_start IS NULL OR window_1d_start + INTERVAL '24 hours' <= NOW() THEN date_trunc('day', NOW()) ELSE window_1d_start END,
			window_7d_start = CASE WHEN window_7d_start IS NULL OR window_7d_start + INTERVAL '7 days' <= NOW() THEN date_trunc('day', NOW()) ELSE window_7d_start END,
			updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
	`, cost, apiKeyID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return service.ErrAPIKeyNotFound
	}
	return nil
}

func incrementUsageBillingAccountQuota(ctx context.Context, tx *sql.Tx, accountID int64, amount float64) (*service.AccountQuotaState, error) {
	rows, err := tx.QueryContext(ctx,
		`UPDATE accounts SET extra = (
			COALESCE(extra, '{}'::jsonb)
			|| jsonb_build_object('quota_used', COALESCE((extra->>'quota_used')::numeric, 0) + $1)
			|| CASE WHEN COALESCE((extra->>'quota_daily_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_daily_used',
					CASE WHEN `+dailyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_daily_used')::numeric, 0) + $1 END,
					'quota_daily_start',
					CASE WHEN `+dailyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_daily_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+dailyExpiredExpr+` AND `+nextDailyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_daily_reset_at', `+nextDailyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
			|| CASE WHEN COALESCE((extra->>'quota_weekly_limit')::numeric, 0) > 0 THEN
				jsonb_build_object(
					'quota_weekly_used',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN $1
					ELSE COALESCE((extra->>'quota_weekly_used')::numeric, 0) + $1 END,
					'quota_weekly_start',
					CASE WHEN `+weeklyExpiredExpr+`
					THEN `+nowUTC+`
					ELSE COALESCE(extra->>'quota_weekly_start', `+nowUTC+`) END
				)
				|| CASE WHEN `+weeklyExpiredExpr+` AND `+nextWeeklyResetAtExpr+` IS NOT NULL
				   THEN jsonb_build_object('quota_weekly_reset_at', `+nextWeeklyResetAtExpr+`)
				   ELSE '{}'::jsonb END
			ELSE '{}'::jsonb END
		), updated_at = NOW()
		WHERE id = $2 AND deleted_at IS NULL
		RETURNING
			COALESCE((extra->>'quota_used')::numeric, 0),
			COALESCE((extra->>'quota_limit')::numeric, 0),
			COALESCE((extra->>'quota_daily_used')::numeric, 0),
			COALESCE((extra->>'quota_daily_limit')::numeric, 0),
			COALESCE((extra->>'quota_weekly_used')::numeric, 0),
			COALESCE((extra->>'quota_weekly_limit')::numeric, 0)`,
		amount, accountID)
	if err != nil {
		return nil, err
	}

	var state service.AccountQuotaState
	if rows.Next() {
		if err := rows.Scan(
			&state.TotalUsed, &state.TotalLimit,
			&state.DailyUsed, &state.DailyLimit,
			&state.WeeklyUsed, &state.WeeklyLimit,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
	} else {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
		return nil, service.ErrAccountNotFound
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	// 必须在执行下一条 SQL 前显式关闭 rows：pq 驱动在同一连接上
	// 不允许前一条查询的结果集未耗尽时启动新查询，否则会返回
	// "unexpected Parse response" 错误。
	if err := rows.Close(); err != nil {
		return nil, err
	}
	// 任意维度额度在本次递增中从"未超"跨越到"已超"时，必须刷新调度快照，
	// 否则 Redis 中缓存的 Account 仍显示旧的 used 值，后续请求会继续选中本账号，
	// 最终观察到 daily_used / weekly_used 大幅超过配置的 limit。
	// 对于日/周额度，即使本次触发了周期重置（pre=0、post=amount），
	// 判定式 (post-amount) < limit 同样成立，逻辑与总额度保持一致。
	crossedTotal := state.TotalLimit > 0 && state.TotalUsed >= state.TotalLimit && (state.TotalUsed-amount) < state.TotalLimit
	crossedDaily := state.DailyLimit > 0 && state.DailyUsed >= state.DailyLimit && (state.DailyUsed-amount) < state.DailyLimit
	crossedWeekly := state.WeeklyLimit > 0 && state.WeeklyUsed >= state.WeeklyLimit && (state.WeeklyUsed-amount) < state.WeeklyLimit
	if crossedTotal || crossedDaily || crossedWeekly {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
			logger.LegacyPrintf("repository.usage_billing", "[SchedulerOutbox] enqueue quota exceeded failed: account=%d err=%v", accountID, err)
			return nil, err
		}
	}
	return &state, nil
}
