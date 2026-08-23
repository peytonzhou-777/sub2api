package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

type resetRebateStatsAccount struct {
	ID                 int64
	AccountID          int64
	AccountName        string
	PeriodStart        time.Time
	PeriodEnd          time.Time
	EffectiveStatRatio decimal.Decimal
}

type resetRebateStatsUser struct {
	UserID          int64
	Email           string
	Username        string
	Status          string
	Deleted         bool
	SkipCount       int64
	RawAmount       decimal.Decimal
	Weighted        decimal.Decimal
	Result          string
	ExclusionReason string
}

type resetRebateStatsContribution struct {
	UserID    int64
	Account   resetRebateStatsAccount
	RawAmount decimal.Decimal
	Weighted  decimal.Decimal
}

func classifyResetRebateStatsUser(user *resetRebateStatsUser) (string, string) {
	if user.Deleted {
		return "excluded", "用户已删除，未发放"
	}
	if user.SkipCount > 0 {
		return "excluded", ResetRebateExclusionSkipCount
	}
	if user.Weighted.Truncate(8).IsZero() {
		return "excluded", "金额过小，未发放"
	}
	return "pending", ""
}

// runStatistics 只读取本地用量并生成不可变账号、用户和贡献快照。
func (s *ResetRebateService) runStatistics(ctx context.Context, batchID int64) error {
	lock := s.batchMutex(batchID)
	lock.Lock()
	defer lock.Unlock()
	unlock, err := s.acquireResetRebateBatchLock(ctx, batchID)
	if err != nil {
		return err
	}
	defer unlock()

	var status string
	var version int
	if err = s.db.QueryRowContext(ctx, "SELECT mechanism_version, status FROM reset_rebate_batches WHERE id=$1", batchID).Scan(&version, &status); err != nil {
		return err
	}
	if version != ResetRebateMechanismV3 || status != ResetRebateStatusRunning {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, "DELETE FROM reset_rebate_user_account_items WHERE batch_id=$1", batchID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM reset_rebate_user_items WHERE batch_id=$1", batchID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reset_rebate_account_items SET raw_amount=0, weighted_amount=0 WHERE batch_id=$1`, batchID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE reset_rebate_batches SET progress_completed=0, failure_stage='', failure_code='', failure_message='', updated_at=NOW() WHERE id=$1`, batchID); err != nil {
		return err
	}

	accounts, err := s.loadStatsAccounts(ctx, tx, batchID)
	if err != nil {
		return err
	}
	accountByID := make(map[int64]resetRebateStatsAccount, len(accounts))
	accountRaw := make(map[int64]decimal.Decimal, len(accounts))
	accountWeighted := make(map[int64]decimal.Decimal, len(accounts))
	for _, account := range accounts {
		accountByID[account.AccountID] = account
		accountRaw[account.AccountID] = decimal.Zero
		accountWeighted[account.AccountID] = decimal.Zero
	}
	users := make(map[int64]*resetRebateStatsUser)
	totalRaw, totalWeighted := decimal.Zero, decimal.Zero
	contributions := make([]resetRebateStatsContribution, 0)
	rows, err := tx.QueryContext(ctx, `
		SELECT account_item.account_id, usage_log.user_id, COALESCE(app_user.email,''),
		       COALESCE(app_user.username,''), COALESCE(app_user.status,''), app_user.deleted_at IS NOT NULL,
		       COALESCE(app_user.reset_rebate_skip_count, 0),
		       COALESCE(SUM(usage_log.actual_cost),0)::text
		FROM reset_rebate_account_items AS account_item
		JOIN usage_logs AS usage_log
		  ON usage_log.account_id=account_item.account_id
		 AND usage_log.created_at>=account_item.period_start
		 AND usage_log.created_at<account_item.period_end
		JOIN users AS app_user ON app_user.id=usage_log.user_id
		WHERE account_item.batch_id=$1 AND account_item.included_in_statistics=TRUE
		GROUP BY account_item.account_id,usage_log.user_id,app_user.email,app_user.username,app_user.status,app_user.deleted_at,app_user.reset_rebate_skip_count
		HAVING COALESCE(SUM(usage_log.actual_cost),0)>0
		ORDER BY account_item.account_id,usage_log.user_id`, batchID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var accountID, userID int64
		var email, username, userStatus, rawText string
		var skipCount int64
		var deleted bool
		if err = rows.Scan(&accountID, &userID, &email, &username, &userStatus, &deleted, &skipCount, &rawText); err != nil {
			_ = rows.Close()
			return err
		}
		account, ok := accountByID[accountID]
		if !ok {
			_ = rows.Close()
			return fmt.Errorf("reset rebate account snapshot %d not found", accountID)
		}
		raw, parseErr := parseDecimalDB(rawText)
		if parseErr != nil {
			_ = rows.Close()
			return parseErr
		}
		weighted := raw.Mul(account.EffectiveStatRatio).Div(resetRebateHundred)
		contributions = append(contributions, resetRebateStatsContribution{UserID: userID, Account: account, RawAmount: raw, Weighted: weighted})
		item := users[userID]
		if item == nil {
			item = &resetRebateStatsUser{UserID: userID, Email: email, Username: username, Status: userStatus, Deleted: deleted, SkipCount: skipCount}
			users[userID] = item
		}
		item.RawAmount = item.RawAmount.Add(raw)
		item.Weighted = item.Weighted.Add(weighted)
		accountRaw[accountID] = accountRaw[accountID].Add(raw)
		accountWeighted[accountID] = accountWeighted[accountID].Add(weighted)
		totalRaw = totalRaw.Add(raw)
		totalWeighted = totalWeighted.Add(weighted)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if err = copyResetRebateContributions(ctx, tx, batchID, contributions); err != nil {
		return err
	}
	if err = updateResetRebateAccountTotals(ctx, tx, accounts, accountRaw, accountWeighted); err != nil {
		return err
	}

	eligibleCount, excludedCount := 0, 0
	userSnapshots := make([]*resetRebateStatsUser, 0, len(users))
	for _, user := range users {
		result, exclusion := classifyResetRebateStatsUser(user)
		if result == "excluded" {
			excludedCount++
		} else {
			eligibleCount++
		}
		user.Result = result
		user.ExclusionReason = exclusion
		userSnapshots = append(userSnapshots, user)
	}
	if err = copyResetRebateUsers(ctx, tx, batchID, userSnapshots); err != nil {
		return err
	}
	finalStatus := ResetRebateStatusReady
	if eligibleCount == 0 {
		finalStatus = ResetRebateStatusNotEligible
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE reset_rebate_batches SET status=$2, raw_amount=$3, weighted_amount=$4,
			expected_user_count=$5, excluded_user_count=$6, progress_completed=progress_total,
			failure_stage='', failure_code='', failure_message='', updated_at=NOW()
		WHERE id=$1
	`, batchID, finalStatus, decimalString(totalRaw, 16), decimalString(totalWeighted, 16), eligibleCount, excludedCount)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ResetRebateService) loadStatsAccounts(ctx context.Context, tx *sql.Tx, batchID int64) ([]resetRebateStatsAccount, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, account_id, account_name, period_start, period_end, effective_stat_ratio::text
		FROM reset_rebate_account_items WHERE batch_id=$1 AND included_in_statistics=TRUE ORDER BY account_id
	`, batchID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]resetRebateStatsAccount, 0)
	for rows.Next() {
		var item resetRebateStatsAccount
		var ratio string
		if err = rows.Scan(&item.ID, &item.AccountID, &item.AccountName, &item.PeriodStart, &item.PeriodEnd, &ratio); err != nil {
			return nil, err
		}
		item.EffectiveStatRatio, err = parseDecimalDB(ratio)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func copyResetRebateContributions(ctx context.Context, tx *sql.Tx, batchID int64, items []resetRebateStatsContribution) error {
	if len(items) == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, pq.CopyIn("reset_rebate_user_account_items", "batch_id", "user_id", "account_id", "account_name", "period_start", "period_end", "raw_amount", "effective_stat_ratio", "weighted_amount"))
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	for _, item := range items {
		if _, err = statement.ExecContext(ctx, batchID, item.UserID, item.Account.AccountID, item.Account.AccountName,
			item.Account.PeriodStart, item.Account.PeriodEnd, decimalString(item.RawAmount, 16),
			decimalString(item.Account.EffectiveStatRatio, 8), decimalString(item.Weighted, 16)); err != nil {
			return err
		}
	}
	_, err = statement.ExecContext(ctx)
	return err
}

func updateResetRebateAccountTotals(ctx context.Context, tx *sql.Tx, accounts []resetRebateStatsAccount, raw, weighted map[int64]decimal.Decimal) error {
	ids := make([]int64, 0, len(accounts))
	rawValues := make([]string, 0, len(accounts))
	weightedValues := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
		rawValues = append(rawValues, decimalString(raw[account.AccountID], 16))
		weightedValues = append(weightedValues, decimalString(weighted[account.AccountID], 16))
	}
	_, err := tx.ExecContext(ctx, `UPDATE reset_rebate_account_items AS item
		SET raw_amount=totals.raw_amount::numeric,weighted_amount=totals.weighted_amount::numeric
		FROM UNNEST($1::bigint[],$2::text[],$3::text[]) AS totals(id,raw_amount,weighted_amount)
		WHERE item.id=totals.id`, pq.Array(ids), pq.Array(rawValues), pq.Array(weightedValues))
	return err
}

func copyResetRebateUsers(ctx context.Context, tx *sql.Tx, batchID int64, users []*resetRebateStatsUser) error {
	if len(users) == 0 {
		return nil
	}
	statement, err := tx.PrepareContext(ctx, pq.CopyIn("reset_rebate_user_items", "batch_id", "user_id", "email", "username", "user_status", "user_deleted", "raw_amount", "weighted_amount", "expected_amount", "actual_issued_amount", "result", "exclusion_reason"))
	if err != nil {
		return err
	}
	defer func() { _ = statement.Close() }()
	for _, user := range users {
		if _, err = statement.ExecContext(ctx, batchID, user.UserID, user.Email, user.Username, user.Status, user.Deleted,
			decimalString(user.RawAmount, 16), decimalString(user.Weighted, 16), "0", "0", user.Result, user.ExclusionReason); err != nil {
			return err
		}
	}
	_, err = statement.ExecContext(ctx)
	return err
}

const resetRebateBatchSelect = `
	SELECT id, mechanism_version, group_id, group_name, admin_id, admin_email, status, failure_stage,
	       execution_mode, execution_cursor_user_id, initial_issued_at,
	       force_stat_ratio_enabled, force_stat_ratio::text, average_benefit_enabled,
	       average_benefit_duration_us, average_benefit_ratio::text, combined_payout_ratio::text,
	       account_count, excluded_account_count, risk_account_count,
	       progress_total, progress_completed, period_start, period_end,
	       raw_amount::text, weighted_amount::text, expected_amount::text, successful_amount::text,
	       failed_amount::text, excluded_amount::text, payout_ratio, rebate_reason, preview_version,
	       expected_user_count, successful_user_count, excluded_user_count, failed_user_count,
	       failure_code, failure_message, executed_by_admin_id, executed_by_admin_email,
	       first_executed_at, last_retry_at, created_at, updated_at
	FROM reset_rebate_batches`

type rowScanner interface{ Scan(...any) error }

func scanResetRebateBatch(row rowScanner) (*ResetRebateBatchView, error) {
	var item ResetRebateBatchView
	var groupID, executedBy sql.NullInt64
	var payout sql.NullInt64
	var periodStart, periodEnd, initialIssued, firstExecuted, lastRetry sql.NullTime
	if err := row.Scan(
		&item.ID, &item.MechanismVersion, &groupID, &item.GroupName, &item.AdminID, &item.AdminEmail,
		&item.Status, &item.FailureStage, &item.ExecutionMode, &item.ExecutionCursorUserID, &initialIssued,
		&item.ForceStatRatioEnabled, &item.ForceStatRatio, &item.AverageBenefitEnabled,
		&item.AverageBenefitDurationUS, &item.AverageBenefitRatio, &item.CombinedPayoutRatio,
		&item.AccountCount, &item.ExcludedAccountCount, &item.RiskAccountCount, &item.ProgressTotal, &item.ProgressCompleted,
		&periodStart, &periodEnd, &item.RawAmount, &item.WeightedAmount, &item.ExpectedAmount,
		&item.SuccessfulAmount, &item.FailedAmount, &item.ExcludedAmount, &payout, &item.RebateReason,
		&item.PreviewVersion, &item.ExpectedUserCount, &item.SuccessfulUserCount, &item.ExcludedUserCount,
		&item.FailedUserCount, &item.FailureCode, &item.FailureMessage, &executedBy,
		&item.ExecutedByAdminEmail, &firstExecuted, &lastRetry, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if groupID.Valid {
		value := groupID.Int64
		item.GroupID = &value
	}
	if payout.Valid {
		value := int(payout.Int64)
		item.PayoutRatio = &value
	}
	if executedBy.Valid {
		value := executedBy.Int64
		item.ExecutedByAdminID = &value
	}
	if periodStart.Valid {
		value := periodStart.Time
		item.PeriodStart = &value
	}
	if periodEnd.Valid {
		value := periodEnd.Time
		item.PeriodEnd = &value
	}
	if initialIssued.Valid {
		value := initialIssued.Time
		item.InitialIssuedAt = &value
	}
	if firstExecuted.Valid {
		value := firstExecuted.Time
		item.FirstExecutedAt = &value
	}
	if lastRetry.Valid {
		value := lastRetry.Time
		item.LastRetryAt = &value
	}
	return &item, nil
}

// GetBatch 返回明确机制版本的批次详情。
func (s *ResetRebateService) GetBatch(ctx context.Context, batchID int64) (*ResetRebateBatchView, error) {
	item, err := scanResetRebateBatch(s.db.QueryRowContext(ctx, resetRebateBatchSelect+" WHERE id=$1", batchID))
	if err == sql.ErrNoRows {
		return nil, infraerrors.New(http.StatusNotFound, "RESET_REBATE_NOT_FOUND", "reset rebate batch not found")
	}
	return item, err
}

type ResetRebateListFilter struct {
	Status          string
	AccountSearch   string
	AdminID         int64
	ExecutedAdminID int64
	CreatedStart    *time.Time
	CreatedEnd      *time.Time
}

// ListBatches 分页返回全部机制历史，并支持账号与管理员筛选。
func (s *ResetRebateService) ListBatches(ctx context.Context, page, pageSize int, filter ResetRebateListFilter) (*ResetRebatePage[ResetRebateBatchView], error) {
	where, args := []string{"1=1"}, make([]any, 0)
	add := func(condition string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(condition, len(args)))
	}
	if filter.Status != "" {
		add("status=$%d", filter.Status)
	}
	if filter.AdminID > 0 {
		add("admin_id=$%d", filter.AdminID)
	}
	if filter.ExecutedAdminID > 0 {
		add("executed_by_admin_id=$%d", filter.ExecutedAdminID)
	}
	if filter.CreatedStart != nil {
		add("created_at >= $%d", *filter.CreatedStart)
	}
	if filter.CreatedEnd != nil {
		add("created_at < $%d", *filter.CreatedEnd)
	}
	if strings.TrimSpace(filter.AccountSearch) != "" {
		args = append(args, "%"+strings.TrimSpace(filter.AccountSearch)+"%")
		position := len(args)
		where = append(where, fmt.Sprintf(`EXISTS(SELECT 1 FROM reset_rebate_account_items a WHERE a.batch_id=reset_rebate_batches.id AND (a.account_name ILIKE $%d OR a.account_id::text ILIKE $%d))`, position, position))
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM reset_rebate_batches WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	query := resetRebateBatchSelect + " WHERE " + clause + fmt.Sprintf(" ORDER BY created_at DESC, id DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]ResetRebateBatchView, 0)
	for rows.Next() {
		item, scanErr := scanResetRebateBatch(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, *item)
	}
	return &ResetRebatePage[ResetRebateBatchView]{Items: items, Total: total, Page: page, PageSize: pageSize}, rows.Err()
}

// ListAccounts 返回批次账号快照，不查询上游状态。
func (s *ResetRebateService) ListAccounts(ctx context.Context, batchID int64, page, pageSize int) (*ResetRebatePage[ResetRebateAccountView], error) {
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM reset_rebate_account_items WHERE batch_id=$1", batchID).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,account_id,account_name,platform,account_type,is_shadow,account_status,
		       account_error_message,schedulable,period_start,period_end,default_window_source,
		       window_risk,ratio_mode,auto_stat_ratio::text,manual_stat_ratio::text,
		       effective_stat_ratio::text,included_in_statistics,statistics_exclusion_reason,
		       raw_amount::text,weighted_amount::text
		FROM reset_rebate_account_items WHERE batch_id=$1 ORDER BY account_id LIMIT $2 OFFSET $3
	`, batchID, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]ResetRebateAccountView, 0)
	for rows.Next() {
		var item ResetRebateAccountView
		var manual sql.NullString
		if err = rows.Scan(&item.ID, &item.AccountID, &item.AccountName, &item.Platform, &item.AccountType, &item.IsShadow,
			&item.AccountStatus, &item.AccountErrorMessage, &item.Schedulable, &item.PeriodStart, &item.PeriodEnd,
			&item.DefaultWindowSource, &item.WindowRisk, &item.RatioMode, &item.AutoStatRatio, &manual,
			&item.EffectiveStatRatio, &item.IncludedInStatistics, &item.StatisticsExclusionReason,
			&item.RawAmount, &item.WeightedAmount); err != nil {
			return nil, err
		}
		if manual.Valid {
			value := manual.String
			item.ManualStatRatio = &value
		}
		items = append(items, item)
	}
	return &ResetRebatePage[ResetRebateAccountView]{Items: items, Total: total, Page: page, PageSize: pageSize}, rows.Err()
}

// ListUsers 返回逐用户汇总，可按身份或结果筛选。
func (s *ResetRebateService) ListUsers(ctx context.Context, batchID int64, page, pageSize int, search, result string) (*ResetRebatePage[ResetRebateUserView], error) {
	where, args := []string{"batch_id=$1"}, []any{batchID}
	if strings.TrimSpace(search) != "" {
		args = append(args, "%"+strings.TrimSpace(search)+"%")
		p := len(args)
		where = append(where, fmt.Sprintf("(user_id::text ILIKE $%d OR email ILIKE $%d OR username ILIKE $%d)", p, p, p))
	}
	if result != "" {
		args = append(args, result)
		where = append(where, fmt.Sprintf("result=$%d", len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM reset_rebate_user_items WHERE "+clause, args...).Scan(&total); err != nil {
		return nil, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,email,username,user_status,user_deleted,raw_amount::text,
		weighted_amount::text,expected_amount::text,actual_issued_amount::text,result,exclusion_reason,
		error_code,error_message,attempt_count,first_failed_at,last_attempt_at,grant_id,issued_at,expires_at
		FROM reset_rebate_user_items WHERE `+clause+fmt.Sprintf(" ORDER BY user_id LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]ResetRebateUserView, 0)
	for rows.Next() {
		item, scanErr := scanResetRebateUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, *item)
	}
	return &ResetRebatePage[ResetRebateUserView]{Items: items, Total: total, Page: page, PageSize: pageSize}, rows.Err()
}

func scanResetRebateUser(row rowScanner) (*ResetRebateUserView, error) {
	var item ResetRebateUserView
	var first, last, issued, expires sql.NullTime
	var grant sql.NullInt64
	if err := row.Scan(&item.ID, &item.UserID, &item.Email, &item.Username, &item.UserStatus, &item.UserDeleted,
		&item.RawAmount, &item.WeightedAmount, &item.ExpectedAmount, &item.ActualIssuedAmount, &item.Result,
		&item.ExclusionReason, &item.ErrorCode, &item.ErrorMessage, &item.AttemptCount, &first, &last, &grant, &issued, &expires); err != nil {
		return nil, err
	}
	if first.Valid {
		v := first.Time
		item.FirstFailedAt = &v
	}
	if last.Valid {
		v := last.Time
		item.LastAttemptAt = &v
	}
	if grant.Valid {
		v := grant.Int64
		item.GrantID = &v
	}
	if issued.Valid {
		v := issued.Time
		item.IssuedAt = &v
	}
	if expires.Valid {
		v := expires.Time
		item.ExpiresAt = &v
	}
	return &item, nil
}

// ListContributions 返回一个用户在各账号上的稳定贡献快照。
func (s *ResetRebateService) ListContributions(ctx context.Context, batchID, userID int64) ([]ResetRebateContributionView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account_id,account_name,period_start,period_end,raw_amount::text,
		effective_stat_ratio::text,weighted_amount::text FROM reset_rebate_user_account_items
		WHERE batch_id=$1 AND user_id=$2 ORDER BY account_id`, batchID, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]ResetRebateContributionView, 0)
	for rows.Next() {
		var item ResetRebateContributionView
		if err = rows.Scan(&item.AccountID, &item.AccountName, &item.PeriodStart, &item.PeriodEnd, &item.RawAmount, &item.EffectiveStatRatio, &item.WeightedAmount); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
