package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type securityDepositRepository struct {
	db *sql.DB
}

func (r *securityDepositRepository) HasAcceptedAgreement(ctx context.Context, userID, groupID int64, policyVersion, contentHash string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM security_deposit_agreements
    WHERE user_id = $1 AND group_id = $2 AND policy_version = $3 AND content_hash = $4
)`, userID, groupID, policyVersion, contentHash).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check security deposit agreement: %w", err)
	}
	return exists, nil
}

// AcceptAgreement 幂等写入同一用户、分组和版本的协议接受证据。
func (r *securityDepositRepository) AcceptAgreement(ctx context.Context, acceptance service.SecurityDepositAgreementAcceptance) (*service.SecurityDepositAgreementAcceptance, error) {
	acceptedAt := acceptance.AcceptedAt.UTC()
	if acceptedAt.IsZero() {
		acceptedAt = time.Now().UTC()
	}
	err := r.db.QueryRowContext(ctx, `
WITH inserted AS (
    INSERT INTO security_deposit_agreements (
        user_id, policy_version, content_hash, group_id,
        base_required_snapshot_cents, risk_multiplier_snapshot, required_snapshot_cents,
        accepted_at, client_ip, user_agent
    ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
    ON CONFLICT (user_id, group_id, policy_version, content_hash) DO NOTHING
    RETURNING id, accepted_at
)
SELECT id, accepted_at FROM inserted
UNION ALL
SELECT id, accepted_at
FROM security_deposit_agreements
WHERE user_id = $1 AND group_id = $4 AND policy_version = $2 AND content_hash = $3
LIMIT 1`,
		acceptance.UserID, acceptance.PolicyVersion, acceptance.ContentHash, acceptance.GroupID,
		acceptance.BaseRequiredSnapshotCents, acceptance.RiskMultiplierSnapshot, acceptance.RequiredSnapshotCents,
		acceptedAt, acceptance.ClientIP, acceptance.UserAgent,
	).Scan(&acceptance.ID, &acceptance.AcceptedAt)
	if err != nil {
		return nil, fmt.Errorf("insert security deposit agreement: %w", err)
	}
	return &acceptance, nil
}

// NewSecurityDepositRepository 创建保证金第一阶段只读仓储。
func NewSecurityDepositRepository(db *sql.DB) service.SecurityDepositRepository {
	return &securityDepositRepository{db: db}
}

type securityDepositAdminActionFingerprint struct {
	actionType string
	userID     int64
	lotID      *int64
	apiKeyID   *int64
	amount     int64
	operatorID int64
	reason     *string
}

// CreditAdminGrant 在同一事务内新增永久冻结批次、发放桶余额和不可变流水。
func (r *securityDepositRepository) CreditAdminGrant(ctx context.Context, input service.AdminSecurityDepositCreditInput) (*service.AdminSecurityDepositMutationResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin security deposit admin credit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := lockSecurityDepositAdminUserAndRisk(ctx, tx, input.UserID); err != nil {
		return nil, err
	}
	actionKey := service.SecurityDepositAdminActionKey(input.ActionType, input.UserID, input.IdempotencyKey)
	fingerprint := securityDepositAdminActionFingerprint{
		actionType: input.ActionType, userID: input.UserID, amount: input.AmountCents,
		operatorID: input.OperatorID, reason: input.Reason,
	}
	if existing, err := loadSecurityDepositAdminMutation(ctx, tx, actionKey, fingerprint); err != nil {
		return nil, err
	} else if existing != nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit duplicate security deposit admin credit: %w", err)
		}
		existing.AlreadyProcessed = true
		return existing, nil
	}

	if err := ensureSecurityDepositAdminGrantAccount(ctx, tx, input.UserID); err != nil {
		return nil, err
	}
	balances, reserved, err := lockSecurityDepositPenaltyAccounts(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	if _, err := lockSecurityDepositPenaltyLots(ctx, tx, input.UserID); err != nil {
		return nil, err
	}
	actionID, err := nextSecurityDepositAdminActionID(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sourceType := "admin"
	if input.ActionType == service.SecurityDepositAdminActionCompensation {
		sourceType = "compensation"
	}
	var lotID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO security_deposit_lots (
    user_id, bucket_type, source_type, original_cents, remaining_cents,
    currency, refund_policy, status, source_reference, notes, created_by, created_at, updated_at
) VALUES ($1, 'admin_grant', $2, $3, $3, 'CNY', 'never', 'active', $4, $5, $6, $7, $7)
RETURNING id`, input.UserID, sourceType, input.AmountCents, actionKey, input.Reason, input.OperatorID, now).Scan(&lotID)
	if err != nil {
		return nil, fmt.Errorf("insert security deposit admin grant lot: %w", err)
	}
	balances["admin_grant"] += input.AmountCents
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_accounts
SET balance_cents = $1, version = version + 1, updated_at = $2
WHERE user_id = $3 AND bucket_type = 'admin_grant'`, balances["admin_grant"], now, input.UserID); err != nil {
		return nil, fmt.Errorf("update security deposit admin grant account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_ledger (
    user_id, lot_id, bucket_type, entry_type, delta_cents, reserved_delta_cents,
    bucket_balance_after_cents, bucket_reserved_after_cents,
    operator_id, reason, idempotency_key, created_at
) VALUES ($1, $2, 'admin_grant', $3, $4, 0, $5, $6, $7, $8, $9, $10)`,
		input.UserID, lotID, input.ActionType, input.AmountCents, balances["admin_grant"], reserved["admin_grant"],
		input.OperatorID, input.Reason, fmt.Sprintf("%s:lot:%d", actionKey, lotID), now); err != nil {
		return nil, fmt.Errorf("insert security deposit admin credit ledger: %w", err)
	}
	result := &service.AdminSecurityDepositMutationResult{
		ActionID: actionID, ActionType: input.ActionType, UserID: input.UserID, LotID: &lotID,
		AmountCents: input.AmountCents, AdminGrantBalanceAfterCents: balances["admin_grant"], DisabledKeyIDs: []int64{},
	}
	fingerprint.lotID = &lotID
	if err := insertSecurityDepositAdminAction(ctx, tx, actionID, actionKey, fingerprint, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit security deposit admin credit: %w", err)
	}
	return result, nil
}

// DeductAdminGrant 仅从 admin_grant 批次按 FIFO 扣除，禁止跨桶补足。
func (r *securityDepositRepository) DeductAdminGrant(ctx context.Context, input service.AdminSecurityDepositDeductInput, enforcementEnabled bool) (*service.AdminSecurityDepositMutationResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin security deposit admin deduction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	multiplier, err := lockSecurityDepositAdminUserAndRisk(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	actionKey := service.SecurityDepositAdminActionKey(service.SecurityDepositAdminActionDeduct, input.UserID, input.IdempotencyKey)
	fingerprint := securityDepositAdminActionFingerprint{
		actionType: service.SecurityDepositAdminActionDeduct, userID: input.UserID, amount: input.AmountCents,
		operatorID: input.OperatorID, reason: input.Reason,
	}
	if existing, err := loadSecurityDepositAdminMutation(ctx, tx, actionKey, fingerprint); err != nil {
		return nil, err
	} else if existing != nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit duplicate security deposit admin deduction: %w", err)
		}
		existing.AlreadyProcessed = true
		return existing, nil
	}
	if err := ensureSecurityDepositAdminGrantAccount(ctx, tx, input.UserID); err != nil {
		return nil, err
	}
	balances, reserved, err := lockSecurityDepositPenaltyAccounts(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	lots, err := lockSecurityDepositPenaltyLots(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	if balances["admin_grant"] < input.AmountCents {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_ADMIN_GRANT_INSUFFICIENT", "administrator-granted security deposit is insufficient")
	}
	actionID, err := nextSecurityDepositAdminActionID(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	remaining := input.AmountCents
	for _, lot := range lots {
		if remaining == 0 {
			break
		}
		if lot.bucketType != "admin_grant" || lot.remainingCents <= 0 {
			continue
		}
		deduct := lot.remainingCents
		if deduct > remaining {
			deduct = remaining
		}
		status := "active"
		if lot.remainingCents-deduct == 0 {
			status = "exhausted"
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_lots
SET remaining_cents = remaining_cents - $1,
    admin_deducted_cents = admin_deducted_cents + $1,
    status = $2, updated_at = $3
WHERE id = $4`, deduct, status, now, lot.id); err != nil {
			return nil, fmt.Errorf("deduct security deposit admin lot %d: %w", lot.id, err)
		}
		balances["admin_grant"] -= deduct
		remaining -= deduct
		if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_ledger (
    user_id, lot_id, bucket_type, entry_type, delta_cents, reserved_delta_cents,
    bucket_balance_after_cents, bucket_reserved_after_cents,
    operator_id, reason, idempotency_key, created_at
) VALUES ($1, $2, 'admin_grant', 'admin_deduct', $3, 0, $4, $5, $6, $7, $8, $9)`,
			input.UserID, lot.id, -deduct, balances["admin_grant"], reserved["admin_grant"],
			input.OperatorID, input.Reason, fmt.Sprintf("%s:lot:%d", actionKey, lot.id), now); err != nil {
			return nil, fmt.Errorf("insert security deposit admin deduction ledger: %w", err)
		}
	}
	if remaining != 0 {
		return nil, fmt.Errorf("security deposit admin grant lots are inconsistent with account balance")
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_accounts
SET balance_cents = $1, version = version + 1, updated_at = $2
WHERE user_id = $3 AND bucket_type = 'admin_grant'`, balances["admin_grant"], now, input.UserID); err != nil {
		return nil, fmt.Errorf("update security deposit admin grant account: %w", err)
	}
	disabled, err := disableInsufficientSecurityDepositKeysTx(ctx, tx, input.UserID, multiplier, balances, reserved, enforcementEnabled, service.SecurityDepositAdminActionDeduct, actionID, now)
	if err != nil {
		return nil, err
	}
	result := &service.AdminSecurityDepositMutationResult{
		ActionID: actionID, ActionType: service.SecurityDepositAdminActionDeduct, UserID: input.UserID,
		AmountCents: input.AmountCents, AdminGrantBalanceAfterCents: balances["admin_grant"], DisabledKeyIDs: disabled,
	}
	if err := insertSecurityDepositAdminAction(ctx, tx, actionID, actionKey, fingerprint, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit security deposit admin deduction: %w", err)
	}
	return result, nil
}

// RevokeAdminGrantLot 撤销指定永久冻结批次当前全部剩余额，不触碰已处罚或已扣除部分。
func (r *securityDepositRepository) RevokeAdminGrantLot(ctx context.Context, input service.AdminSecurityDepositRevokeInput, enforcementEnabled bool) (*service.AdminSecurityDepositMutationResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin security deposit admin revoke: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	multiplier, err := lockSecurityDepositAdminUserAndRisk(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	actionKey := service.SecurityDepositAdminActionKey(service.SecurityDepositAdminActionRevoke, input.UserID, input.IdempotencyKey)
	fingerprint := securityDepositAdminActionFingerprint{
		actionType: service.SecurityDepositAdminActionRevoke, userID: input.UserID, lotID: &input.LotID,
		operatorID: input.OperatorID, reason: input.Reason,
	}
	if existing, err := loadSecurityDepositAdminMutation(ctx, tx, actionKey, fingerprint); err != nil {
		return nil, err
	} else if existing != nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit duplicate security deposit admin revoke: %w", err)
		}
		existing.AlreadyProcessed = true
		return existing, nil
	}
	if err := ensureSecurityDepositAdminGrantAccount(ctx, tx, input.UserID); err != nil {
		return nil, err
	}
	balances, reserved, err := lockSecurityDepositPenaltyAccounts(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	lots, err := lockSecurityDepositPenaltyLots(ctx, tx, input.UserID)
	if err != nil {
		return nil, err
	}
	var target *securityDepositPenaltyLot
	for i := range lots {
		if lots[i].id == input.LotID && lots[i].bucketType == "admin_grant" {
			target = &lots[i]
			break
		}
	}
	if target == nil {
		return nil, infraerrors.NotFound("SECURITY_DEPOSIT_ADMIN_GRANT_LOT_NOT_FOUND", "administrator-granted security deposit lot not found")
	}
	if target.remainingCents <= 0 {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_ADMIN_GRANT_LOT_EMPTY", "administrator-granted security deposit lot has no revocable balance")
	}
	actionID, err := nextSecurityDepositAdminActionID(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	amount := target.remainingCents
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_lots
SET remaining_cents = 0, revoked_cents = revoked_cents + $1,
    status = 'exhausted', updated_at = $2
WHERE id = $3`, amount, now, input.LotID); err != nil {
		return nil, fmt.Errorf("revoke security deposit admin lot: %w", err)
	}
	balances["admin_grant"] -= amount
	if balances["admin_grant"] < 0 {
		return nil, fmt.Errorf("security deposit admin grant account is inconsistent with lots")
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_accounts
SET balance_cents = $1, version = version + 1, updated_at = $2
WHERE user_id = $3 AND bucket_type = 'admin_grant'`, balances["admin_grant"], now, input.UserID); err != nil {
		return nil, fmt.Errorf("update security deposit admin grant account: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_ledger (
    user_id, lot_id, bucket_type, entry_type, delta_cents, reserved_delta_cents,
    bucket_balance_after_cents, bucket_reserved_after_cents,
    operator_id, reason, idempotency_key, created_at
) VALUES ($1, $2, 'admin_grant', 'admin_revoke', $3, 0, $4, $5, $6, $7, $8, $9)`,
		input.UserID, input.LotID, -amount, balances["admin_grant"], reserved["admin_grant"],
		input.OperatorID, input.Reason, fmt.Sprintf("%s:lot:%d", actionKey, input.LotID), now); err != nil {
		return nil, fmt.Errorf("insert security deposit admin revoke ledger: %w", err)
	}
	disabled, err := disableInsufficientSecurityDepositKeysTx(ctx, tx, input.UserID, multiplier, balances, reserved, enforcementEnabled, service.SecurityDepositAdminActionRevoke, actionID, now)
	if err != nil {
		return nil, err
	}
	result := &service.AdminSecurityDepositMutationResult{
		ActionID: actionID, ActionType: service.SecurityDepositAdminActionRevoke, UserID: input.UserID, LotID: &input.LotID,
		AmountCents: amount, AdminGrantBalanceAfterCents: balances["admin_grant"], DisabledKeyIDs: disabled,
	}
	fingerprint.amount = amount
	if err := insertSecurityDepositAdminAction(ctx, tx, actionID, actionKey, fingerprint, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit security deposit admin revoke: %w", err)
	}
	return result, nil
}

// UnlockSecurityLockedAPIKey 仅把 security_locked 降为 disabled，并清除锁定快照。
func (r *securityDepositRepository) UnlockSecurityLockedAPIKey(ctx context.Context, input service.AdminSecurityDepositUnlockInput) (*service.AdminSecurityDepositUnlockResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin security deposit key unlock: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockSecurityDepositAdminUserAndRisk(ctx, tx, input.UserID); err != nil {
		return nil, err
	}
	actionKey := service.SecurityDepositAdminActionKey(service.SecurityDepositAdminActionKeyUnlock, input.UserID, input.IdempotencyKey)
	fingerprint := securityDepositAdminActionFingerprint{
		actionType: service.SecurityDepositAdminActionKeyUnlock, userID: input.UserID, apiKeyID: &input.APIKeyID,
		operatorID: input.OperatorID, reason: input.Reason,
	}
	if existing, err := loadSecurityDepositAdminUnlock(ctx, tx, actionKey, fingerprint); err != nil {
		return nil, err
	} else if existing != nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit duplicate security deposit key unlock: %w", err)
		}
		existing.AlreadyProcessed = true
		return existing, nil
	}
	var status, apiKey string
	if err := tx.QueryRowContext(ctx, `
SELECT status, key
FROM api_keys
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
FOR UPDATE`, input.APIKeyID, input.UserID).Scan(&status, &apiKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, infraerrors.NotFound("API_KEY_NOT_FOUND", "API key not found")
		}
		return nil, fmt.Errorf("lock security deposit api key: %w", err)
	}
	if status != service.StatusAPIKeySecurityLocked {
		return nil, infraerrors.Conflict("API_KEY_NOT_SECURITY_LOCKED", "API key is not security locked")
	}
	actionID, err := nextSecurityDepositAdminActionID(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
UPDATE api_keys
SET status = $1,
    security_locked_at = NULL,
    security_lock_violation_id = NULL,
    security_lock_reason = NULL,
    disabled_reason = NULL,
    disabled_financial_event_type = NULL,
    disabled_financial_event_id = NULL,
    disabled_at = $2,
    updated_at = $2
WHERE id = $3 AND user_id = $4`, service.StatusAPIKeyDisabled, now, input.APIKeyID, input.UserID); err != nil {
		return nil, fmt.Errorf("unlock security deposit api key: %w", err)
	}
	result := &service.AdminSecurityDepositUnlockResult{
		ActionID: actionID, UserID: input.UserID, APIKeyID: input.APIKeyID,
		Status: service.StatusAPIKeyDisabled, APIKey: apiKey,
	}
	if err := insertSecurityDepositAdminAction(ctx, tx, actionID, actionKey, fingerprint, result); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit security deposit key unlock: %w", err)
	}
	return result, nil
}

func lockSecurityDepositAdminUserAndRisk(ctx context.Context, tx *sql.Tx, userID int64) (int64, error) {
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, userID).Scan(&lockedUserID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, infraerrors.NotFound("USER_NOT_FOUND", "user not found")
		}
		return 0, fmt.Errorf("lock security deposit admin user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_risk_profiles (user_id, cyber_strike_count, risk_multiplier, version)
VALUES ($1, 0, 1, 1)
ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return 0, fmt.Errorf("ensure security deposit risk profile: %w", err)
	}
	var multiplier int64
	if err := tx.QueryRowContext(ctx, `
SELECT risk_multiplier
FROM security_deposit_risk_profiles
WHERE user_id = $1
FOR UPDATE`, userID).Scan(&multiplier); err != nil {
		return 0, fmt.Errorf("lock security deposit risk profile: %w", err)
	}
	return multiplier, nil
}

func ensureSecurityDepositAdminGrantAccount(ctx context.Context, tx *sql.Tx, userID int64) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_accounts (user_id, bucket_type, currency, balance_cents, refund_reserved_cents, version)
VALUES ($1, 'admin_grant', 'CNY', 0, 0, 1)
ON CONFLICT (user_id, bucket_type) DO NOTHING`, userID); err != nil {
		return fmt.Errorf("ensure security deposit admin grant account: %w", err)
	}
	return nil
}

func disableInsufficientSecurityDepositKeysTx(
	ctx context.Context,
	tx *sql.Tx,
	userID, multiplier int64,
	balances, reserved map[string]int64,
	enforcementEnabled bool,
	eventType string,
	eventID int64,
	disabledAt time.Time,
) ([]int64, error) {
	if !enforcementEnabled {
		return []int64{}, nil
	}
	effectiveBalance := balances["paid"] - reserved["paid"] + balances["admin_grant"]
	rows, err := tx.QueryContext(ctx, `
UPDATE api_keys AS ak
SET status = $1,
    disabled_reason = $2,
    disabled_financial_event_type = $3,
    disabled_financial_event_id = $4,
    disabled_at = $5,
    updated_at = $5
FROM groups AS g
WHERE ak.group_id = g.id
  AND ak.user_id = $6
  AND ak.status = $7
  AND ak.deleted_at IS NULL
  AND g.security_deposit_base_required_cents > 0
  AND (g.security_deposit_base_required_cents::numeric * $8::numeric) > $9::numeric
RETURNING ak.id`,
		service.StatusAPIKeyDisabled, service.DisabledReasonSecurityDepositInsufficient,
		eventType, eventID, disabledAt, userID, service.StatusAPIKeyActive,
		multiplier, effectiveBalance)
	if err != nil {
		return nil, fmt.Errorf("disable insufficient security deposit api keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan disabled security deposit api key: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate disabled security deposit api keys: %w", err)
	}
	return ids, nil
}

func nextSecurityDepositAdminActionID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT nextval('security_deposit_admin_actions_id_seq')`).Scan(&id); err != nil {
		return 0, fmt.Errorf("allocate security deposit admin action id: %w", err)
	}
	return id, nil
}

func insertSecurityDepositAdminAction(ctx context.Context, tx *sql.Tx, actionID int64, actionKey string, fingerprint securityDepositAdminActionFingerprint, result any) error {
	snapshot, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal security deposit admin action result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_admin_actions (
    id, action_key, action_type, user_id, lot_id, api_key_id,
    amount_cents, operator_id, reason, result_snapshot
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)`,
		actionID, actionKey, fingerprint.actionType, fingerprint.userID,
		fingerprint.lotID, fingerprint.apiKeyID, fingerprint.amount,
		fingerprint.operatorID, fingerprint.reason, string(snapshot)); err != nil {
		return fmt.Errorf("insert security deposit admin action: %w", err)
	}
	return nil
}

func loadSecurityDepositAdminMutation(ctx context.Context, tx *sql.Tx, actionKey string, expected securityDepositAdminActionFingerprint) (*service.AdminSecurityDepositMutationResult, error) {
	snapshot, matched, err := loadSecurityDepositAdminActionSnapshot(ctx, tx, actionKey, expected)
	if err != nil || !matched {
		return nil, err
	}
	var result service.AdminSecurityDepositMutationResult
	if err := json.Unmarshal(snapshot, &result); err != nil {
		return nil, fmt.Errorf("unmarshal security deposit admin action result: %w", err)
	}
	return &result, nil
}

func loadSecurityDepositAdminUnlock(ctx context.Context, tx *sql.Tx, actionKey string, expected securityDepositAdminActionFingerprint) (*service.AdminSecurityDepositUnlockResult, error) {
	snapshot, matched, err := loadSecurityDepositAdminActionSnapshot(ctx, tx, actionKey, expected)
	if err != nil || !matched {
		return nil, err
	}
	var result service.AdminSecurityDepositUnlockResult
	if err := json.Unmarshal(snapshot, &result); err != nil {
		return nil, fmt.Errorf("unmarshal security deposit key unlock result: %w", err)
	}
	return &result, nil
}

func loadSecurityDepositAdminActionSnapshot(ctx context.Context, tx *sql.Tx, actionKey string, expected securityDepositAdminActionFingerprint) ([]byte, bool, error) {
	var actual securityDepositAdminActionFingerprint
	var snapshot []byte
	err := tx.QueryRowContext(ctx, `
SELECT action_type, user_id, lot_id, api_key_id, amount_cents, operator_id, reason, result_snapshot
FROM security_deposit_admin_actions
WHERE action_key = $1`, actionKey).Scan(
		&actual.actionType, &actual.userID, &actual.lotID, &actual.apiKeyID,
		&actual.amount, &actual.operatorID, &actual.reason, &snapshot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("load security deposit admin action: %w", err)
	}
	if !securityDepositAdminActionMatches(actual, expected) {
		return nil, false, service.ErrIdempotencyKeyConflict
	}
	return snapshot, true, nil
}

func securityDepositAdminActionMatches(actual, expected securityDepositAdminActionFingerprint) bool {
	if actual.actionType != expected.actionType || actual.userID != expected.userID ||
		actual.operatorID != expected.operatorID || !equalNullableString(actual.reason, expected.reason) {
		return false
	}
	switch actual.actionType {
	case service.SecurityDepositAdminActionAdd, service.SecurityDepositAdminActionCompensation:
		return actual.amount == expected.amount && actual.lotID != nil && actual.apiKeyID == nil
	case service.SecurityDepositAdminActionDeduct:
		return actual.amount == expected.amount && actual.lotID == nil && actual.apiKeyID == nil
	case service.SecurityDepositAdminActionRevoke:
		return equalNullableInt64(actual.lotID, expected.lotID) && actual.amount > 0 && actual.apiKeyID == nil
	case service.SecurityDepositAdminActionKeyUnlock:
		return actual.amount == 0 && actual.lotID == nil && equalNullableInt64(actual.apiKeyID, expected.apiKeyID)
	default:
		return false
	}
}

func equalNullableInt64(left, right *int64) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func equalNullableString(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func (r *securityDepositRepository) GetUserData(ctx context.Context, userID int64) (*service.SecurityDepositUserData, error) {
	data := &service.SecurityDepositUserData{RiskMultiplier: 1, Lots: []service.SecurityDepositLot{}}
	rows, err := r.db.QueryContext(ctx, `
SELECT bucket_type, balance_cents, refund_reserved_cents, version
FROM security_deposit_accounts
WHERE user_id = $1
ORDER BY bucket_type`, userID)
	if err != nil {
		return nil, fmt.Errorf("query security deposit accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var account service.SecurityDepositAccountRecord
		if err := rows.Scan(&account.BucketType, &account.BalanceCents, &account.RefundReservedCents, &account.Version); err != nil {
			return nil, fmt.Errorf("scan security deposit account: %w", err)
		}
		data.Accounts = append(data.Accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate security deposit accounts: %w", err)
	}

	err = r.db.QueryRowContext(ctx, `
SELECT cyber_strike_count, risk_multiplier
FROM security_deposit_risk_profiles
WHERE user_id = $1`, userID).Scan(&data.CyberStrikeCount, &data.RiskMultiplier)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("query security deposit risk profile: %w", err)
	}

	lotRows, err := r.db.QueryContext(ctx, `
SELECT id, bucket_type, source_type, payment_order_id, original_cents, remaining_cents,
       refund_reserved_cents, forfeited_cents, refunded_cents, admin_deducted_cents,
       revoked_cents, currency, locked_until, refund_policy, status, created_at
FROM security_deposit_lots
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 200`, userID)
	if err != nil {
		return nil, fmt.Errorf("query security deposit lots: %w", err)
	}
	defer func() { _ = lotRows.Close() }()
	for lotRows.Next() {
		var lot service.SecurityDepositLot
		if err := lotRows.Scan(
			&lot.ID, &lot.BucketType, &lot.SourceType, &lot.PaymentOrderID, &lot.OriginalCents,
			&lot.RemainingCents, &lot.RefundReservedCents, &lot.ForfeitedCents, &lot.RefundedCents,
			&lot.AdminDeductedCents, &lot.RevokedCents, &lot.Currency, &lot.LockedUntil,
			&lot.RefundPolicy, &lot.Status, &lot.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan security deposit lot: %w", err)
		}
		data.Lots = append(data.Lots, lot)
	}
	if err := lotRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate security deposit lots: %w", err)
	}
	return data, nil
}

type securityDepositPenaltyLot struct {
	id             int64
	bucketType     string
	remainingCents int64
	reservedCents  int64
}

// ApplyCyberPolicyPenalty 在一个数据库事务内完成幂等事件、扣罚、倍率升级和密钥处置。
func (r *securityDepositRepository) ApplyCyberPolicyPenalty(ctx context.Context, input service.SecurityDepositCyberPenaltyInput, maxRiskMultiplier int64, shadow bool) (*service.SecurityDepositCyberPenaltyResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("begin security deposit penalty: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 与保证金入账保持同一锁顺序：先用户，再风险档案、资金桶和批次。
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, input.Grant.UserID).Scan(&lockedUserID); err != nil {
		return nil, fmt.Errorf("lock security deposit penalty user: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_risk_profiles (user_id, cyber_strike_count, risk_multiplier, version)
VALUES ($1, 0, 1, 1)
ON CONFLICT (user_id) DO NOTHING`, input.Grant.UserID); err != nil {
		return nil, fmt.Errorf("ensure security deposit risk profile: %w", err)
	}

	var strikeBefore, currentMultiplier int64
	if err := tx.QueryRowContext(ctx, `
SELECT cyber_strike_count, risk_multiplier
FROM security_deposit_risk_profiles
WHERE user_id = $1
FOR UPDATE`, input.Grant.UserID).Scan(&strikeBefore, &currentMultiplier); err != nil {
		return nil, fmt.Errorf("lock security deposit risk profile: %w", err)
	}
	if existing, err := loadSecurityDepositPenaltyResult(ctx, tx, input.EventKey); err != nil {
		return nil, err
	} else if existing != nil {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit duplicate security deposit penalty: %w", err)
		}
		existing.AlreadyProcessed = true
		return existing, nil
	}

	var latestBaseRequiredCents int64
	if err := tx.QueryRowContext(ctx, `
SELECT security_deposit_base_required_cents
FROM groups
WHERE id = $1
FOR SHARE`, input.Grant.GroupID).Scan(&latestBaseRequiredCents); err != nil {
		return nil, fmt.Errorf("load latest security deposit group threshold: %w", err)
	}
	if latestBaseRequiredCents < 0 || currentMultiplier < 1 ||
		(latestBaseRequiredCents > 0 && currentMultiplier > math.MaxInt64/latestBaseRequiredCents) {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_THRESHOLD", "security deposit threshold is invalid")
	}
	latestRequiredCents := latestBaseRequiredCents * currentMultiplier

	multiplierAfter := currentMultiplier
	strikeAfter := strikeBefore
	state := "shadow"
	if !shadow {
		strikeAfter++
		if maxRiskMultiplier < 1 {
			maxRiskMultiplier = 1
		}
		if multiplierAfter < maxRiskMultiplier {
			multiplierAfter++
		}
		state = "processing"
	}
	processedAt := time.Now().UTC()
	var violationID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO security_deposit_violations (
    event_key, request_id, upstream_response_id, turn_index,
    user_id, api_key_id, group_id, policy_code, detector_version,
    base_required_snapshot_cents, risk_multiplier_before, required_snapshot_cents,
    risk_multiplier_after, forfeited_cents, shortfall_cents, state,
    api_key_name_snapshot, group_name_snapshot, processed_at
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7, $8, $9,
    $10, $11, $12,
    $13, 0, 0, $14,
    $15, $16, $17
)
ON CONFLICT (event_key) DO NOTHING
RETURNING id`,
		input.EventKey, input.RequestID, nullableString(input.UpstreamResponseID), nullableInt64(input.TurnIndex),
		input.Grant.UserID, input.APIKeyID, input.Grant.GroupID, input.PolicyCode, "openai-error-code-v1",
		latestBaseRequiredCents, currentMultiplier, latestRequiredCents,
		multiplierAfter, state, input.APIKeyName, input.GroupName, processedAt,
	).Scan(&violationID)
	if errors.Is(err, sql.ErrNoRows) {
		existing, loadErr := loadSecurityDepositPenaltyResult(ctx, tx, input.EventKey)
		if loadErr != nil {
			return nil, loadErr
		}
		if existing == nil {
			return nil, fmt.Errorf("security deposit penalty conflict without existing violation")
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit concurrent security deposit penalty: %w", err)
		}
		existing.AlreadyProcessed = true
		return existing, nil
	}
	if err != nil {
		return nil, fmt.Errorf("insert security deposit violation: %w", err)
	}

	result := &service.SecurityDepositCyberPenaltyResult{
		ViolationID: violationID, State: state,
		RiskMultiplierBefore: currentMultiplier,
		RiskMultiplierAfter:  multiplierAfter,
		DisabledKeyIDs:       []int64{},
	}
	if shadow {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit shadow security deposit penalty: %w", err)
		}
		return result, nil
	}

	accountBalances, accountReserved, err := lockSecurityDepositPenaltyAccounts(ctx, tx, input.Grant.UserID)
	if err != nil {
		return nil, err
	}
	lots, err := lockSecurityDepositPenaltyLots(ctx, tx, input.Grant.UserID)
	if err != nil {
		return nil, err
	}
	remainingTarget := latestRequiredCents
	for _, lot := range lots {
		if remainingTarget <= 0 {
			break
		}
		available := lot.remainingCents - lot.reservedCents
		accountAvailable := accountBalances[lot.bucketType] - accountReserved[lot.bucketType]
		if available > accountAvailable {
			available = accountAvailable
		}
		if available <= 0 {
			continue
		}
		deduct := available
		if deduct > remainingTarget {
			deduct = remainingTarget
		}
		newRemaining := lot.remainingCents - deduct
		lotStatus := "active"
		if newRemaining == 0 {
			lotStatus = "exhausted"
		}
		if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_lots
SET remaining_cents = remaining_cents - $1,
    forfeited_cents = forfeited_cents + $1,
    status = $2,
    updated_at = $3
WHERE id = $4`, deduct, lotStatus, processedAt, lot.id); err != nil {
			return nil, fmt.Errorf("forfeit security deposit lot %d: %w", lot.id, err)
		}
		accountBalances[lot.bucketType] -= deduct
		result.ForfeitedCents += deduct
		remainingTarget -= deduct
		ledgerKey := fmt.Sprintf("security_deposit:cyber:%d:lot:%d", violationID, lot.id)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_ledger (
    user_id, lot_id, bucket_type, entry_type, delta_cents, reserved_delta_cents,
    bucket_balance_after_cents, bucket_reserved_after_cents,
    group_id, api_key_id, violation_id, idempotency_key, created_at
) VALUES ($1, $2, $3, 'forfeit', $4, 0, $5, $6, $7, $8, $9, $10, $11)`,
			input.Grant.UserID, lot.id, lot.bucketType, -deduct,
			accountBalances[lot.bucketType], accountReserved[lot.bucketType],
			input.Grant.GroupID, input.APIKeyID, violationID, ledgerKey, processedAt,
		); err != nil {
			return nil, fmt.Errorf("insert security deposit forfeit ledger: %w", err)
		}
	}
	result.ShortfallCents = remainingTarget

	for bucketType, balance := range accountBalances {
		if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_accounts
SET balance_cents = $1, version = version + 1, updated_at = $2
WHERE user_id = $3 AND bucket_type = $4`, balance, processedAt, input.Grant.UserID, bucketType); err != nil {
			return nil, fmt.Errorf("update security deposit %s account: %w", bucketType, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_violations
SET forfeited_cents = $1, shortfall_cents = $2, state = 'processed', processed_at = $3
WHERE id = $4`, result.ForfeitedCents, result.ShortfallCents, processedAt, violationID); err != nil {
		return nil, fmt.Errorf("complete security deposit violation: %w", err)
	}
	result.State = "processed"

	if _, err := tx.ExecContext(ctx, `
INSERT INTO security_deposit_risk_events (
    user_id, event_type, violation_id, strike_count_before, strike_count_after,
    multiplier_before, multiplier_after, idempotency_key, created_at
) VALUES ($1, 'cyber_escalation', $2, $3, $4, $5, $6, $7, $8)`,
		input.Grant.UserID, violationID, strikeBefore, strikeAfter, currentMultiplier, multiplierAfter,
		fmt.Sprintf("security_deposit:cyber:%d:risk", violationID), processedAt,
	); err != nil {
		return nil, fmt.Errorf("insert security deposit risk event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE security_deposit_risk_profiles
SET cyber_strike_count = $1, risk_multiplier = $2, last_violation_id = $3,
    version = version + 1, updated_at = $4
WHERE user_id = $5`, strikeAfter, multiplierAfter, violationID, processedAt, input.Grant.UserID); err != nil {
		return nil, fmt.Errorf("update security deposit risk profile: %w", err)
	}

	// 请求已经通过 active 准入后才可能触发官方网安处罚；即使并发的普通资金事件
	// 先把密钥改成 disabled，也必须由该可信处罚升级为 security_locked。
	locked, err := tx.ExecContext(ctx, `
UPDATE api_keys
SET status = $1, security_locked_at = $2, security_lock_violation_id = $3,
    security_lock_reason = 'cyber_policy', updated_at = $2
WHERE id = $4 AND user_id = $5 AND status <> $6 AND deleted_at IS NULL`,
		service.StatusAPIKeySecurityLocked, processedAt, violationID, input.APIKeyID, input.Grant.UserID, service.StatusAPIKeySecurityLocked)
	if err != nil {
		return nil, fmt.Errorf("security lock triggering api key: %w", err)
	}
	if count, countErr := locked.RowsAffected(); countErr == nil {
		result.SecurityLocked = count > 0
	}

	effectiveBalance := accountBalances["paid"] - accountReserved["paid"] + accountBalances["admin_grant"]
	disabledRows, err := tx.QueryContext(ctx, `
UPDATE api_keys AS ak
SET status = $1,
    disabled_reason = $2,
    disabled_financial_event_type = 'cyber_policy',
    disabled_financial_event_id = $3,
    disabled_at = $4,
    updated_at = $4
FROM groups AS g
WHERE ak.group_id = g.id
  AND ak.user_id = $5
  AND ak.status = $6
  AND ak.deleted_at IS NULL
  AND g.security_deposit_base_required_cents > 0
  AND (g.security_deposit_base_required_cents::numeric * $7::numeric) > $8::numeric
RETURNING ak.id`,
		service.StatusAPIKeyDisabled, service.DisabledReasonSecurityDepositInsufficient,
		violationID, processedAt, input.Grant.UserID, service.StatusAPIKeyActive,
		multiplierAfter, effectiveBalance)
	if err != nil {
		return nil, fmt.Errorf("disable insufficient security deposit api keys: %w", err)
	}
	for disabledRows.Next() {
		var keyID int64
		if err := disabledRows.Scan(&keyID); err != nil {
			if closeErr := disabledRows.Close(); closeErr != nil {
				return nil, fmt.Errorf("scan disabled security deposit api key: %v; close rows: %w", err, closeErr)
			}
			return nil, fmt.Errorf("scan disabled security deposit api key: %w", err)
		}
		result.DisabledKeyIDs = append(result.DisabledKeyIDs, keyID)
	}
	if err := disabledRows.Close(); err != nil {
		return nil, fmt.Errorf("close disabled security deposit api keys: %w", err)
	}
	if err := disabledRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate disabled security deposit api keys: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit security deposit penalty: %w", err)
	}
	return result, nil
}

func loadSecurityDepositPenaltyResult(ctx context.Context, tx *sql.Tx, eventKey string) (*service.SecurityDepositCyberPenaltyResult, error) {
	result := &service.SecurityDepositCyberPenaltyResult{DisabledKeyIDs: []int64{}}
	err := tx.QueryRowContext(ctx, `
SELECT id, state, risk_multiplier_before, risk_multiplier_after, forfeited_cents, shortfall_cents
FROM security_deposit_violations
WHERE event_key = $1`, eventKey).Scan(
		&result.ViolationID, &result.State, &result.RiskMultiplierBefore,
		&result.RiskMultiplierAfter, &result.ForfeitedCents, &result.ShortfallCents,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load security deposit violation: %w", err)
	}
	return result, nil
}

func lockSecurityDepositPenaltyAccounts(ctx context.Context, tx *sql.Tx, userID int64) (map[string]int64, map[string]int64, error) {
	balances := map[string]int64{"paid": 0, "admin_grant": 0}
	reserved := map[string]int64{"paid": 0, "admin_grant": 0}
	rows, err := tx.QueryContext(ctx, `
SELECT bucket_type, balance_cents, refund_reserved_cents
FROM security_deposit_accounts
WHERE user_id = $1
ORDER BY bucket_type
FOR UPDATE`, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("lock security deposit accounts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var bucketType string
		var balance, refundReserved int64
		if err := rows.Scan(&bucketType, &balance, &refundReserved); err != nil {
			return nil, nil, fmt.Errorf("scan security deposit account for penalty: %w", err)
		}
		balances[bucketType] = balance
		reserved[bucketType] = refundReserved
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate security deposit accounts for penalty: %w", err)
	}
	return balances, reserved, nil
}

func lockSecurityDepositPenaltyLots(ctx context.Context, tx *sql.Tx, userID int64) ([]securityDepositPenaltyLot, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT id, bucket_type, remaining_cents, refund_reserved_cents
FROM security_deposit_lots
WHERE user_id = $1 AND remaining_cents > refund_reserved_cents
ORDER BY created_at, id
FOR UPDATE`, userID)
	if err != nil {
		return nil, fmt.Errorf("lock security deposit lots for penalty: %w", err)
	}
	defer func() { _ = rows.Close() }()
	lots := make([]securityDepositPenaltyLot, 0)
	for rows.Next() {
		var lot securityDepositPenaltyLot
		if err := rows.Scan(&lot.id, &lot.bucketType, &lot.remainingCents, &lot.reservedCents); err != nil {
			return nil, fmt.Errorf("scan security deposit lot for penalty: %w", err)
		}
		lots = append(lots, lot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate security deposit lots for penalty: %w", err)
	}
	return lots, nil
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func (r *securityDepositRepository) ListAdminUsers(ctx context.Context, page, pageSize int, search string) ([]service.AdminSecurityDepositUserSummary, int64, error) {
	offset := (page - 1) * pageSize
	search = strings.TrimSpace(search)
	pattern := "%" + strings.ToLower(search) + "%"
	var total int64
	if err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM users
WHERE deleted_at IS NULL
  AND ($1 = '' OR LOWER(email) LIKE $2 OR LOWER(username) LIKE $2)`, search, pattern).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count security deposit users: %w", err)
	}

	rows, err := r.db.QueryContext(ctx, `
WITH account_totals AS (
    SELECT user_id,
           COALESCE(SUM(balance_cents) FILTER (WHERE bucket_type = 'paid'), 0) AS paid_balance_cents,
           COALESCE(SUM(balance_cents) FILTER (WHERE bucket_type = 'admin_grant'), 0) AS admin_grant_balance_cents,
           COALESCE(SUM(refund_reserved_cents) FILTER (WHERE bucket_type = 'paid'), 0) AS paid_refund_reserved_cents
    FROM security_deposit_accounts
    GROUP BY user_id
), last_violations AS (
    SELECT user_id, MAX(created_at) AS last_violation_at
    FROM security_deposit_violations
    GROUP BY user_id
), lot_totals AS (
    SELECT user_id,
           COALESCE(SUM(GREATEST(remaining_cents - refund_reserved_cents, 0)) FILTER (
               WHERE refund_policy = 'timed_original_channel' AND locked_until > NOW()
           ), 0) AS timed_locked_cents,
           COALESCE(SUM(GREATEST(remaining_cents - refund_reserved_cents, 0)) FILTER (
               WHERE refund_policy = 'never'
           ), 0) AS permanent_locked_cents,
           COALESCE(SUM(GREATEST(remaining_cents - refund_reserved_cents, 0)) FILTER (
               WHERE refund_policy = 'timed_original_channel' AND locked_until <= NOW()
           ), 0) AS refundable_cents
    FROM security_deposit_lots
    GROUP BY user_id
)
SELECT u.id, u.email, u.username, u.status,
       COALESCE(a.paid_balance_cents, 0), COALESCE(a.admin_grant_balance_cents, 0),
       COALESCE(a.paid_balance_cents, 0) + COALESCE(a.admin_grant_balance_cents, 0),
       COALESCE(a.paid_balance_cents, 0) - COALESCE(a.paid_refund_reserved_cents, 0) + COALESCE(a.admin_grant_balance_cents, 0),
       COALESCE(l.timed_locked_cents, 0), COALESCE(l.permanent_locked_cents, 0), COALESCE(l.refundable_cents, 0),
       COALESCE(a.paid_refund_reserved_cents, 0), COALESCE(p.risk_multiplier, 1),
       COALESCE(p.cyber_strike_count, 0), v.last_violation_at
FROM users u
LEFT JOIN account_totals a ON a.user_id = u.id
LEFT JOIN security_deposit_risk_profiles p ON p.user_id = u.id
LEFT JOIN last_violations v ON v.user_id = u.id
LEFT JOIN lot_totals l ON l.user_id = u.id
WHERE u.deleted_at IS NULL
  AND ($1 = '' OR LOWER(u.email) LIKE $2 OR LOWER(u.username) LIKE $2)
ORDER BY (COALESCE(a.paid_balance_cents, 0) + COALESCE(a.admin_grant_balance_cents, 0)) DESC, u.id DESC
LIMIT $3 OFFSET $4`, search, pattern, pageSize, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("query security deposit users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.AdminSecurityDepositUserSummary, 0, pageSize)
	for rows.Next() {
		var item service.AdminSecurityDepositUserSummary
		if err := rows.Scan(
			&item.UserID, &item.Email, &item.Username, &item.Status, &item.PaidBalanceCents,
			&item.AdminGrantBalanceCents, &item.TotalBalanceCents, &item.EffectiveBalanceCents,
			&item.TimedLockedCents, &item.PermanentLockedCents, &item.RefundableCents,
			&item.PaidRefundReservedCents, &item.RiskMultiplier,
			&item.CyberStrikeCount, &item.LastViolationAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan security deposit user: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate security deposit users: %w", err)
	}
	return items, total, nil
}

func (r *securityDepositRepository) GetAdminUser(ctx context.Context, userID int64) (*service.AdminSecurityDepositUserSummary, error) {
	var item service.AdminSecurityDepositUserSummary
	err := r.db.QueryRowContext(ctx, `
SELECT u.id, u.email, u.username, u.status,
       COALESCE(paid.balance_cents, 0), COALESCE(admin_grant.balance_cents, 0),
       COALESCE(paid.balance_cents, 0) + COALESCE(admin_grant.balance_cents, 0),
       COALESCE(paid.balance_cents, 0) - COALESCE(paid.refund_reserved_cents, 0) + COALESCE(admin_grant.balance_cents, 0),
       COALESCE(lots.timed_locked_cents, 0), COALESCE(lots.permanent_locked_cents, 0), COALESCE(lots.refundable_cents, 0),
       COALESCE(paid.refund_reserved_cents, 0), COALESCE(profile.risk_multiplier, 1),
       COALESCE(profile.cyber_strike_count, 0), violations.last_violation_at
FROM users u
LEFT JOIN security_deposit_accounts paid ON paid.user_id = u.id AND paid.bucket_type = 'paid'
LEFT JOIN security_deposit_accounts admin_grant ON admin_grant.user_id = u.id AND admin_grant.bucket_type = 'admin_grant'
LEFT JOIN security_deposit_risk_profiles profile ON profile.user_id = u.id
LEFT JOIN (
    SELECT user_id, MAX(created_at) AS last_violation_at
    FROM security_deposit_violations
    GROUP BY user_id
) violations ON violations.user_id = u.id
LEFT JOIN (
    SELECT user_id,
           COALESCE(SUM(GREATEST(remaining_cents - refund_reserved_cents, 0)) FILTER (
               WHERE refund_policy = 'timed_original_channel' AND locked_until > NOW()
           ), 0) AS timed_locked_cents,
           COALESCE(SUM(GREATEST(remaining_cents - refund_reserved_cents, 0)) FILTER (
               WHERE refund_policy = 'never'
           ), 0) AS permanent_locked_cents,
           COALESCE(SUM(GREATEST(remaining_cents - refund_reserved_cents, 0)) FILTER (
               WHERE refund_policy = 'timed_original_channel' AND locked_until <= NOW()
           ), 0) AS refundable_cents
    FROM security_deposit_lots
    GROUP BY user_id
) lots ON lots.user_id = u.id
WHERE u.id = $1 AND u.deleted_at IS NULL`, userID).Scan(
		&item.UserID, &item.Email, &item.Username, &item.Status, &item.PaidBalanceCents,
		&item.AdminGrantBalanceCents, &item.TotalBalanceCents, &item.EffectiveBalanceCents,
		&item.TimedLockedCents, &item.PermanentLockedCents, &item.RefundableCents,
		&item.PaidRefundReservedCents, &item.RiskMultiplier,
		&item.CyberStrikeCount, &item.LastViolationAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query security deposit user detail: %w", err)
	}
	return &item, nil
}

func (r *securityDepositRepository) ListLedger(ctx context.Context, userID int64, limit int) ([]service.SecurityDepositLedgerEntry, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, lot_id, bucket_type, entry_type, delta_cents, reserved_delta_cents,
       bucket_balance_after_cents, bucket_reserved_after_cents, reason, created_at
FROM security_deposit_ledger
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query security deposit ledger: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.SecurityDepositLedgerEntry, 0)
	for rows.Next() {
		var item service.SecurityDepositLedgerEntry
		if err := rows.Scan(&item.ID, &item.LotID, &item.BucketType, &item.EntryType, &item.DeltaCents, &item.ReservedDeltaCents, &item.BucketBalanceAfterCents, &item.BucketReservedAfterCents, &item.Reason, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan security deposit ledger: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *securityDepositRepository) ListRefunds(ctx context.Context, userID int64, limit int) ([]service.SecurityDepositRefundView, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, refund_id, lot_id, principal_cents, mode, state, reason, created_at, completed_at
FROM security_deposit_refunds
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query security deposit refunds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.SecurityDepositRefundView, 0)
	for rows.Next() {
		var item service.SecurityDepositRefundView
		if err := rows.Scan(&item.ID, &item.RefundID, &item.LotID, &item.PrincipalCents, &item.Mode, &item.State, &item.Reason, &item.CreatedAt, &item.CompletedAt); err != nil {
			return nil, fmt.Errorf("scan security deposit refund: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *securityDepositRepository) ListViolations(ctx context.Context, userID int64, limit int) ([]service.SecurityDepositViolationView, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, event_key, request_id, api_key_id, group_id, policy_code,
       risk_multiplier_before, risk_multiplier_after, required_snapshot_cents,
       forfeited_cents, shortfall_cents, state, api_key_name_snapshot,
       group_name_snapshot, created_at, processed_at
FROM security_deposit_violations
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query security deposit violations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]service.SecurityDepositViolationView, 0)
	for rows.Next() {
		var item service.SecurityDepositViolationView
		if err := rows.Scan(
			&item.ID, &item.EventKey, &item.RequestID, &item.APIKeyID, &item.GroupID, &item.PolicyCode,
			&item.RiskMultiplierBefore, &item.RiskMultiplierAfter, &item.RequiredSnapshotCents,
			&item.ForfeitedCents, &item.ShortfallCents, &item.State, &item.APIKeyNameSnapshot,
			&item.GroupNameSnapshot, &item.CreatedAt, &item.ProcessedAt,
		); err != nil {
			return nil, fmt.Errorf("scan security deposit violation: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
