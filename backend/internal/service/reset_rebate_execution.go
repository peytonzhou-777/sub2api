package service

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/shopspring/decimal"
)

type ResetRebatePreview struct {
	Batch *ResetRebateBatchView                 `json:"batch"`
	Users *ResetRebatePage[ResetRebateUserView] `json:"users"`
}

func classifyResetRebatePreviewUser(deleted bool, currentResult, currentExclusion string, expected decimal.Decimal) (string, string) {
	if deleted {
		return "excluded", "用户已删除，未发放"
	}
	if currentResult == "excluded" && currentExclusion == ResetRebateExclusionSkipCount {
		return "excluded", ResetRebateExclusionSkipCount
	}
	if expected.IsZero() {
		return "excluded", "金额过小，未发放"
	}
	return "pending", ""
}

// Preview 冻结发放比例、原因和递增预览版本，并重算逐用户最终金额。
func (s *ResetRebateService) Preview(ctx context.Context, batchID int64, payoutRatio, page, pageSize int, reason, search string) (*ResetRebatePreview, error) {
	if payoutRatio < 1 || payoutRatio > 100 {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_PAYOUT_RATIO", "payout_ratio must be between 1 and 100")
	}
	reason, err := normalizeResetRebateReason(reason)
	if err != nil {
		return nil, err
	}
	lock := s.batchMutex(batchID)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := s.acquireResetRebateBatchLock(ctx, batchID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	var status string
	if err = tx.QueryRowContext(ctx, `SELECT mechanism_version,status FROM reset_rebate_batches WHERE id=$1 FOR UPDATE`, batchID).Scan(&version, &status); err != nil {
		if err == sql.ErrNoRows {
			return nil, infraerrors.New(http.StatusNotFound, "RESET_REBATE_NOT_FOUND", "reset rebate batch not found")
		}
		return nil, err
	}
	if version != ResetRebateMechanismV2 {
		return nil, infraerrors.New(http.StatusConflict, "LEGACY_RESET_REBATE_READ_ONLY", "legacy reset rebate batches are read-only")
	}
	if status != ResetRebateStatusReady {
		return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_NOT_PREVIEWABLE", "batch is not ready for preview")
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,weighted_amount::text,user_deleted,result,exclusion_reason
		FROM reset_rebate_user_items WHERE batch_id=$1 ORDER BY user_id`, batchID)
	if err != nil {
		return nil, err
	}
	type previewRow struct {
		id              int64
		weighted        string
		deleted         bool
		result          string
		exclusionReason string
	}
	values := make([]previewRow, 0)
	for rows.Next() {
		var value previewRow
		if err = rows.Scan(&value.id, &value.weighted, &value.deleted, &value.result, &value.exclusionReason); err != nil {
			_ = rows.Close()
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	ratio := decimal.NewFromInt(int64(payoutRatio))
	expectedTotal, excludedTotal := decimal.Zero, decimal.Zero
	expectedCount, excludedCount := 0, 0
	for _, value := range values {
		weighted, parseErr := parseDecimalDB(value.weighted)
		if parseErr != nil {
			return nil, parseErr
		}
		expected := weighted.Mul(ratio).Div(resetRebateHundred).Truncate(8)
		result, exclusion := classifyResetRebatePreviewUser(value.deleted, value.result, value.exclusionReason, expected)
		if result == "excluded" {
			excludedCount++
			excludedTotal = excludedTotal.Add(expected)
		} else {
			expectedCount++
			expectedTotal = expectedTotal.Add(expected)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE reset_rebate_user_items SET expected_amount=$2,result=$3,
			exclusion_reason=$4,error_code='',error_message='',updated_at=NOW() WHERE id=$1`, value.id, decimalString(expected, 8), result, exclusion); err != nil {
			return nil, err
		}
	}
	var previewVersion int
	err = tx.QueryRowContext(ctx, `UPDATE reset_rebate_batches SET payout_ratio=$2,rebate_reason=$3,
		preview_version=preview_version+1,expected_amount=$4,excluded_amount=$5,
		expected_user_count=$6,excluded_user_count=$7,updated_at=NOW() WHERE id=$1 RETURNING preview_version`,
		batchID, payoutRatio, reason, decimalString(expectedTotal, 8), decimalString(excludedTotal, 8), expectedCount, excludedCount).Scan(&previewVersion)
	if err != nil {
		return nil, err
	}
	if expectedCount == 0 {
		return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_NOT_ELIGIBLE", "no user has a positive payout amount")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	batch, err := s.GetBatch(ctx, batchID)
	if err != nil {
		return nil, err
	}
	batch.PreviewVersion = previewVersion
	users, err := s.ListUsers(ctx, batchID, page, pageSize, search, "")
	if err != nil {
		return nil, err
	}
	return &ResetRebatePreview{Batch: batch, Users: users}, nil
}

// Execute 按当前预览逐用户独立发放；一个用户失败不会回滚其他用户。
func (s *ResetRebateService) Execute(ctx context.Context, batchID int64, previewVersion int, actor ResetRebateActor) (*ResetRebateBatchView, error) {
	return s.claimResetRebateExecution(ctx, batchID, previewVersion, actor, false)
}

// RetryFailures 只处理请求开始时仍为 failed 的用户。
func (s *ResetRebateService) RetryFailures(ctx context.Context, batchID int64, actor ResetRebateActor) (*ResetRebateBatchView, error) {
	return s.claimResetRebateExecution(ctx, batchID, 0, actor, true)
}

func (s *ResetRebateService) claimResetRebateExecution(ctx context.Context, batchID int64, previewVersion int, actor ResetRebateActor, retry bool) (*ResetRebateBatchView, error) {
	lock := s.batchMutex(batchID)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := s.acquireResetRebateBatchLock(ctx, batchID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var version, currentPreview int
	var status, failureStage string
	err = tx.QueryRowContext(ctx, `SELECT mechanism_version,status,failure_stage,preview_version
		FROM reset_rebate_batches WHERE id=$1 FOR UPDATE`, batchID).Scan(&version, &status, &failureStage, &currentPreview)
	if err == sql.ErrNoRows {
		return nil, infraerrors.New(http.StatusNotFound, "RESET_REBATE_NOT_FOUND", "reset rebate batch not found")
	}
	if err != nil {
		return nil, err
	}
	if version != ResetRebateMechanismV2 {
		return nil, infraerrors.New(http.StatusConflict, "LEGACY_RESET_REBATE_READ_ONLY", "legacy reset rebate batches are read-only")
	}
	if retry {
		if status != ResetRebateStatusPartial && (status != ResetRebateStatusFailed || failureStage != ResetRebateFailureExecution) {
			return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_NOT_RETRYABLE", "batch has no retryable failed users")
		}
	} else {
		if status == ResetRebateStatusExecuted {
			_ = tx.Rollback()
			return s.GetBatch(ctx, batchID)
		}
		if status != ResetRebateStatusReady {
			return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_NOT_EXECUTABLE", "batch is not ready")
		}
		if currentPreview == 0 || currentPreview != previewVersion {
			return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_PREVIEW_STALE", "preview_version is stale")
		}
	}
	if actor.AdminEmail == "" {
		_ = tx.QueryRowContext(ctx, "SELECT email FROM users WHERE id=$1", actor.AdminID).Scan(&actor.AdminEmail)
	}
	now := time.Now().UTC()
	if retry {
		_, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches SET status='executing',failure_stage='execution',
			execution_mode='retry',execution_cursor_user_id=0,execution_admin_id=$3,execution_admin_email=$4,
			last_retry_at=$2,failure_code='',failure_message='',updated_at=NOW()
			WHERE id=$1`, batchID, now, actor.AdminID, actor.AdminEmail)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches SET status='executing',failure_stage='execution',
			execution_mode='initial',execution_cursor_user_id=0,execution_admin_id=$2,execution_admin_email=$3,
			executed_by_admin_id=COALESCE(executed_by_admin_id,$2),
			executed_by_admin_email=CASE WHEN executed_by_admin_id IS NULL THEN $3 ELSE executed_by_admin_email END,
			first_executed_at=COALESCE(first_executed_at,$4),initial_issued_at=COALESCE(initial_issued_at,$4),
			failure_code='',failure_message='',updated_at=NOW() WHERE id=$1`, batchID, actor.AdminID, actor.AdminEmail, now)
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	go s.runExecutionBackground(batchID)
	return s.GetBatch(ctx, batchID)
}

func (s *ResetRebateService) runExecutionBackground(batchID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), resetRebateExecutionTimeout)
	defer cancel()
	if err := s.runExecution(ctx, batchID); err != nil {
		logger.LegacyPrintf("service.reset_rebate", "后台发放中断 batch=%d err=%v", batchID, err)
		failureCtx, failureCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer failureCancel()
		s.markResetRebateExecutionInterrupted(failureCtx, batchID)
	}
}

func (s *ResetRebateService) runExecution(ctx context.Context, batchID int64) error {
	lock := s.batchMutex(batchID)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := s.acquireResetRebateBatchLock(ctx, batchID)
	if err != nil {
		return err
	}
	defer unlock()
	var status, mode, adminEmail string
	var cursor, adminID int64
	var initialIssued sql.NullTime
	err = s.db.QueryRowContext(ctx, `SELECT status,execution_mode,execution_cursor_user_id,
		COALESCE(execution_admin_id,0),execution_admin_email,initial_issued_at
		FROM reset_rebate_batches WHERE id=$1`, batchID).Scan(&status, &mode, &cursor, &adminID, &adminEmail, &initialIssued)
	if err != nil {
		return err
	}
	if status != ResetRebateStatusExecuting || (mode != resetRebateExecutionInitial && mode != resetRebateExecutionRetry) {
		return nil
	}
	resultFilter := "pending"
	if mode == resetRebateExecutionRetry {
		resultFilter = "failed"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM reset_rebate_user_items
		WHERE batch_id=$1 AND result=$2 AND user_id>$3 ORDER BY user_id`, batchID, resultFilter, cursor)
	if err != nil {
		return err
	}
	userIDs := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		userIDs = append(userIDs, id)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	issuedAt := time.Time{}
	if initialIssued.Valid && mode == resetRebateExecutionInitial {
		issuedAt = initialIssued.Time.UTC()
	}
	actor := ResetRebateActor{AdminID: adminID, AdminEmail: adminEmail}
	if err = s.processResetRebateUsers(ctx, batchID, userIDs, actor, mode, issuedAt, s.issueResetRebateUser, s.recordResetRebateUserFailure); err != nil {
		return err
	}
	_, err = s.refreshExecutionSummary(ctx, batchID)
	return err
}

func (s *ResetRebateService) markResetRebateExecutionInterrupted(ctx context.Context, batchID int64) {
	lock := s.batchMutex(batchID)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := s.acquireResetRebateBatchLock(ctx, batchID)
	if err != nil {
		return
	}
	defer unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `UPDATE reset_rebate_user_items SET result='failed',
		error_code='RESET_REBATE_EXECUTION_INTERRUPTED',error_message='后台发放中断，尚未处理',
		first_failed_at=COALESCE(first_failed_at,NOW()),updated_at=NOW()
		WHERE batch_id=$1 AND result='pending'
		AND EXISTS(SELECT 1 FROM reset_rebate_batches batch WHERE batch.id=$1 AND batch.status='executing')`, batchID)
	if err != nil {
		return
	}
	_, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches AS batch SET
		status=CASE
			WHEN EXISTS(SELECT 1 FROM reset_rebate_user_items item WHERE item.batch_id=batch.id AND item.result='succeeded') THEN 'partial'
			ELSE 'failed' END,
		failure_stage='execution',execution_mode='',failure_code='RESET_REBATE_EXECUTION_INTERRUPTED',
		failure_message='后台发放中断，可重试失败和未处理用户',
		successful_user_count=(SELECT COUNT(*) FROM reset_rebate_user_items item WHERE item.batch_id=batch.id AND item.result='succeeded'),
		excluded_user_count=(SELECT COUNT(*) FROM reset_rebate_user_items item WHERE item.batch_id=batch.id AND item.result='excluded'),
		failed_user_count=(SELECT COUNT(*) FROM reset_rebate_user_items item WHERE item.batch_id=batch.id AND item.result='failed'),
		successful_amount=COALESCE((SELECT SUM(actual_issued_amount) FROM reset_rebate_user_items item WHERE item.batch_id=batch.id AND item.result='succeeded'),0),
		failed_amount=COALESCE((SELECT SUM(expected_amount) FROM reset_rebate_user_items item WHERE item.batch_id=batch.id AND item.result='failed'),0),
		excluded_amount=COALESCE((SELECT SUM(expected_amount) FROM reset_rebate_user_items item WHERE item.batch_id=batch.id AND item.result='excluded'),0),
		updated_at=NOW()
		WHERE id=$1 AND status='executing'`, batchID)
	if err == nil {
		_ = tx.Commit()
	}
}

type resetRebateIssueUserFunc func(context.Context, int64, int64, ResetRebateActor, string, time.Time) error
type resetRebateRecordFailureFunc func(context.Context, int64, int64, ResetRebateActor, string, error) error

// processResetRebateUsers 隔离单用户失败，确保后续用户继续发放。
func (s *ResetRebateService) processResetRebateUsers(ctx context.Context, batchID int64, userIDs []int64, actor ResetRebateActor, attemptType string, issuedAt time.Time, issue resetRebateIssueUserFunc, recordFailure resetRebateRecordFailureFunc) error {
	for _, userID := range userIDs {
		if issueErr := issue(ctx, batchID, userID, actor, attemptType, issuedAt); issueErr != nil {
			logger.LegacyPrintf("service.reset_rebate", "用户发放失败 batch=%d user=%d err=%v", batchID, userID, issueErr)
			if recordErr := recordFailure(ctx, batchID, userID, actor, attemptType, issueErr); recordErr != nil {
				return recordErr
			}
			continue
		}
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
		if s.billingCache != nil {
			if cacheErr := s.billingCache.InvalidateUserBalance(ctx, userID); cacheErr != nil {
				logger.LegacyPrintf("service.reset_rebate", "余额缓存失效失败 batch=%d user=%d err=%v", batchID, userID, cacheErr)
			}
		}
	}
	return nil
}

// acquireResetRebateBatchLock 在多实例部署下串行化同一批次的全部修改操作。
func (s *ResetRebateService) acquireResetRebateBatchLock(ctx context.Context, batchID int64) (func(), error) {
	const namespace int64 = 0x5252420000000000
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	lockKey := namespace + batchID
	if _, err = connection.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return func() {
		_, _ = connection.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", lockKey)
		_ = connection.Close()
	}, nil
}

func (s *ResetRebateService) issueResetRebateUser(ctx context.Context, batchID, userID int64, actor ResetRebateActor, attemptType string, commonIssuedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var expectedText, result string
	var attemptCount int
	err = tx.QueryRowContext(ctx, `SELECT expected_amount::text,result,attempt_count FROM reset_rebate_user_items
		WHERE batch_id=$1 AND user_id=$2 FOR UPDATE`, batchID, userID).Scan(&expectedText, &result, &attemptCount)
	if err != nil {
		return err
	}
	if result == "succeeded" {
		_, err = tx.ExecContext(ctx, `INSERT INTO reset_rebate_user_attempts(batch_id,user_id,attempt_no,admin_id,admin_email,attempt_type,result,expected_amount)
			VALUES($1,$2,$3,$4,$5,$6,'skipped_already_succeeded',$7) ON CONFLICT DO NOTHING`, batchID, userID, attemptCount+1, actor.AdminID, actor.AdminEmail, attemptType, expectedText)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches SET execution_cursor_user_id=GREATEST(execution_cursor_user_id,$2) WHERE id=$1`, batchID, userID); err != nil {
			return err
		}
		return tx.Commit()
	}
	if result == "excluded" {
		if _, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches SET execution_cursor_user_id=GREATEST(execution_cursor_user_id,$2) WHERE id=$1`, batchID, userID); err != nil {
			return err
		}
		return tx.Commit()
	}
	expected, err := parseDecimalDB(expectedText)
	if err != nil || !expected.IsPositive() {
		if err != nil {
			return err
		}
		return fmt.Errorf("expected amount is not positive")
	}
	issuedAt := commonIssuedAt
	if attemptType == resetRebateExecutionRetry || issuedAt.IsZero() {
		issuedAt = time.Now().UTC()
	}
	expiresAt := issuedAt.Add(resetRebateValidity)
	var grantID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM user_limited_credit_grants WHERE source_type='reset_rebate' AND source_id=$1 AND user_id=$2`, batchID, userID).Scan(&grantID)
	if err == sql.ErrNoRows {
		err = tx.QueryRowContext(ctx, `INSERT INTO user_limited_credit_grants(user_id,source_type,source_id,initial_amount,used_amount,frozen_amount,expires_at,status,notes,created_at,updated_at)
			VALUES($1,'reset_rebate',$2,$3,0,0,$4,'active','重置返利',$5,$5) RETURNING id`, userID, batchID, decimalString(expected, 8), expiresAt, issuedAt).Scan(&grantID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO user_limited_credit_ledger(user_id,grant_id,event_type,amount,batch_id,notes,created_at)
			VALUES($1,$2,'grant',$3,$4,'重置返利',$5)`, userID, grantID, decimalString(expected, 8), strconv.FormatInt(batchID, 10), issuedAt)
		if err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	attemptNo := attemptCount + 1
	_, err = tx.ExecContext(ctx, `UPDATE reset_rebate_user_items SET result='succeeded',actual_issued_amount=$3,
		error_code='',error_message='',attempt_count=$4,last_attempt_at=$5,grant_id=$6,issued_at=$5,expires_at=$7,updated_at=NOW()
		WHERE batch_id=$1 AND user_id=$2`, batchID, userID, decimalString(expected, 8), attemptNo, issuedAt, grantID, expiresAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO reset_rebate_user_attempts(batch_id,user_id,attempt_no,admin_id,admin_email,attempt_type,result,expected_amount,grant_id)
		VALUES($1,$2,$3,$4,$5,$6,'succeeded',$7,$8)`, batchID, userID, attemptNo, actor.AdminID, actor.AdminEmail, attemptType, decimalString(expected, 8), grantID)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches SET execution_cursor_user_id=GREATEST(execution_cursor_user_id,$2) WHERE id=$1`, batchID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ResetRebateService) recordResetRebateUserFailure(ctx context.Context, batchID, userID int64, actor ResetRebateActor, attemptType string, cause error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var attemptNo int
	var expected string
	errorMessage := "用户额度写入失败"
	if cause != nil {
		errorMessage = cause.Error()
	}
	err = tx.QueryRowContext(ctx, `UPDATE reset_rebate_user_items SET result='failed',error_code='RESET_REBATE_USER_GRANT_FAILED',
		error_message=$3,attempt_count=attempt_count+1,first_failed_at=COALESCE(first_failed_at,NOW()),
		last_attempt_at=NOW(),updated_at=NOW() WHERE batch_id=$1 AND user_id=$2 AND result<>'succeeded'
		RETURNING attempt_count,expected_amount::text`, batchID, userID, errorMessage).Scan(&attemptNo, &expected)
	if err == sql.ErrNoRows {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO reset_rebate_user_attempts(batch_id,user_id,attempt_no,admin_id,admin_email,attempt_type,result,expected_amount,error_code,error_message)
		VALUES($1,$2,$3,$4,$5,$6,'failed',$7,'RESET_REBATE_USER_GRANT_FAILED',$8)`, batchID, userID, attemptNo, actor.AdminID, actor.AdminEmail, attemptType, expected, errorMessage)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches SET execution_cursor_user_id=GREATEST(execution_cursor_user_id,$2) WHERE id=$1`, batchID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ResetRebateService) refreshExecutionSummary(ctx context.Context, batchID int64) (*ResetRebateBatchView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var succeeded, excluded, failed int
	var successAmount, failedAmount, excludedAmount string
	err = tx.QueryRowContext(ctx, `SELECT
		COUNT(*) FILTER(WHERE result='succeeded'),COUNT(*) FILTER(WHERE result='excluded'),COUNT(*) FILTER(WHERE result='failed'),
		COALESCE(SUM(actual_issued_amount) FILTER(WHERE result='succeeded'),0)::text,
		COALESCE(SUM(expected_amount) FILTER(WHERE result='failed'),0)::text,
		COALESCE(SUM(expected_amount) FILTER(WHERE result='excluded'),0)::text
		FROM reset_rebate_user_items WHERE batch_id=$1`, batchID).Scan(&succeeded, &excluded, &failed, &successAmount, &failedAmount, &excludedAmount)
	if err != nil {
		return nil, err
	}
	status, stage := ResetRebateStatusExecuted, ""
	if failed > 0 && succeeded > 0 {
		status, stage = ResetRebateStatusPartial, ResetRebateFailureExecution
	} else if failed > 0 {
		status, stage = ResetRebateStatusFailed, ResetRebateFailureExecution
	}
	_, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches SET status=$2,failure_stage=$3,execution_mode='',
		successful_user_count=$4,excluded_user_count=$5,failed_user_count=$6,
		successful_amount=$7,failed_amount=$8,excluded_amount=$9,
		failure_code=CASE WHEN $6>0 THEN 'RESET_REBATE_USER_FAILURES' ELSE '' END,
		failure_message=CASE WHEN $6>0 THEN '部分用户发放失败，请查看失败名单' ELSE '' END,updated_at=NOW() WHERE id=$1`,
		batchID, status, stage, succeeded, excluded, failed, successAmount, failedAmount, excludedAmount)
	if err != nil {
		return nil, err
	}
	// 仅在批次进入终态时消费一次用户排除计次；快照标记保证重试或恢复不会重复扣减。
	if _, err = tx.ExecContext(ctx, `UPDATE users AS app_user
		SET reset_rebate_skip_count=GREATEST(app_user.reset_rebate_skip_count-1, 0), updated_at=NOW()
		WHERE app_user.id IN (
			SELECT item.user_id FROM reset_rebate_user_items AS item
			WHERE item.batch_id=$1 AND item.result='excluded'
			  AND item.exclusion_reason=$2 AND item.skip_count_consumed=FALSE
		)`, batchID, ResetRebateExclusionSkipCount); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reset_rebate_user_items
		SET skip_count_consumed=TRUE, updated_at=NOW()
		WHERE batch_id=$1 AND result='excluded'
		  AND exclusion_reason=$2 AND skip_count_consumed=FALSE`, batchID, ResetRebateExclusionSkipCount); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetBatch(ctx, batchID)
}

// DeleteBatch 仅允许删除从未成功发放且不在运行中的快照。
func (s *ResetRebateService) DeleteBatch(ctx context.Context, batchID int64) error {
	lock := s.batchMutex(batchID)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := s.acquireResetRebateBatchLock(ctx, batchID)
	if err != nil {
		return err
	}
	defer unlock()
	result, err := s.db.ExecContext(ctx, `DELETE FROM reset_rebate_batches AS batch
		WHERE batch.id=$1 AND batch.status NOT IN ('running','executing','partial','executed')
		AND batch.successful_user_count=0
		AND NOT EXISTS(SELECT 1 FROM user_limited_credit_grants grant
			WHERE grant.source_type='reset_rebate' AND grant.source_id=batch.id)`, batchID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		if _, getErr := s.GetBatch(ctx, batchID); getErr != nil {
			return getErr
		}
		return infraerrors.New(http.StatusConflict, "RESET_REBATE_NOT_DELETABLE", "batch cannot be deleted")
	}
	return nil
}

// ExportUsersCSV 导出完整用户汇总与发放结果。
func (s *ResetRebateService) ExportUsersCSV(ctx context.Context, batchID int64, writer io.Writer) error {
	return s.exportResetRebateRows(ctx, writer, []string{"用户ID", "邮箱", "用户名", "原始消耗", "计入统计消耗", "预计金额", "实际发放", "结果", "排除原因", "错误码", "错误原因"},
		`SELECT user_id::text,email,username,raw_amount::text,weighted_amount::text,expected_amount::text,
		actual_issued_amount::text,result,exclusion_reason,error_code,error_message FROM reset_rebate_user_items WHERE batch_id=$1 ORDER BY user_id`, batchID, 11)
}

// ExportContributionsCSV 导出完整逐用户逐账号贡献。
func (s *ResetRebateService) ExportContributionsCSV(ctx context.Context, batchID int64, writer io.Writer) error {
	return s.exportResetRebateRows(ctx, writer, []string{"用户ID", "账号ID", "账号名称", "开始时间", "结束时间", "原始消耗", "有效统计比例", "计入统计消耗"},
		`SELECT user_id::text,account_id::text,account_name,period_start::text,period_end::text,raw_amount::text,
		effective_stat_ratio::text,weighted_amount::text FROM reset_rebate_user_account_items WHERE batch_id=$1 ORDER BY user_id,account_id`, batchID, 8)
}

// ExportFailedUsersCSV 导出当前失败用户及最近错误。
func (s *ResetRebateService) ExportFailedUsersCSV(ctx context.Context, batchID int64, writer io.Writer) error {
	return s.exportResetRebateRows(ctx, writer, []string{"用户ID", "邮箱", "用户名", "应发金额", "错误码", "错误原因", "尝试次数", "首次失败时间", "最近尝试时间"},
		`SELECT user_id::text,email,username,expected_amount::text,error_code,error_message,attempt_count::text,
		COALESCE(first_failed_at::text,''),COALESCE(last_attempt_at::text,'') FROM reset_rebate_user_items WHERE batch_id=$1 AND result='failed' ORDER BY user_id`, batchID, 9)
}

func (s *ResetRebateService) exportResetRebateRows(ctx context.Context, writer io.Writer, header []string, query string, batchID int64, columnCount int) error {
	rows, err := s.db.QueryContext(ctx, query, batchID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	csvWriter := csv.NewWriter(writer)
	if err = csvWriter.Write(header); err != nil {
		return err
	}
	for rows.Next() {
		values := make([]string, columnCount)
		dest := make([]any, columnCount)
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return err
		}
		if err = csvWriter.Write(values); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	if err = csvWriter.Error(); err != nil {
		return err
	}
	return rows.Err()
}

func truncateResetRebateAmount(weighted decimal.Decimal, payoutRatio int) decimal.Decimal {
	return weighted.Mul(decimal.NewFromInt(int64(payoutRatio))).Div(resetRebateHundred).Truncate(8)
}
