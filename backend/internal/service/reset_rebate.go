package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

const (
	ResetRebateMechanismV2       = 2
	ResetRebateDefaultPayout     = 90
	ResetRebateDefaultReason     = "官方重置！按账号重置天数返还消耗额度！"
	ResetRebateStatusRunning     = "running"
	ResetRebateStatusReady       = "ready"
	ResetRebateStatusExecuting   = "executing"
	ResetRebateStatusNotEligible = "not_eligible"
	ResetRebateStatusPartial     = "partial"
	ResetRebateStatusFailed      = "failed"
	ResetRebateStatusExecuted    = "executed"
	ResetRebateFailureStatistics = "statistics"
	ResetRebateFailureExecution  = "execution"
	ResetRebateRatioAuto         = "auto"
	ResetRebateRatioManual       = "manual"
	resetRebateExecutionInitial  = "initial"
	resetRebateExecutionRetry    = "retry"
	resetRebateValidity          = 168 * time.Hour
	resetRebateTaskTimeout       = 30 * time.Minute
	resetRebateExecutionTimeout  = 2 * time.Hour
)

var resetRebateHundred = decimal.NewFromInt(100)

// ResetRebateService 实现账号维度统计、版本化预览和逐用户发放。
type ResetRebateService struct {
	db                   *sql.DB
	authCacheInvalidator APIKeyAuthCacheInvalidator
	billingCache         *BillingCacheService
	batchLocks           sync.Map
}

// NewResetRebateService 创建不依赖任何上游配额服务的重置返利服务。
func NewResetRebateService(db *sql.DB, authCacheInvalidator APIKeyAuthCacheInvalidator, billingCache *BillingCacheService) *ResetRebateService {
	s := &ResetRebateService{db: db, authCacheInvalidator: authCacheInvalidator, billingCache: billingCache}
	go s.recoverRunningBatches()
	return s
}

type ResetRebateAccountInput struct {
	AccountID            int64     `json:"account_id"`
	PeriodStart          time.Time `json:"period_start"`
	PeriodEnd            time.Time `json:"period_end"`
	RatioMode            string    `json:"ratio_mode"`
	ManualRatio          *string   `json:"manual_ratio,omitempty"`
	DefaultWindowVersion string    `json:"default_window_version"`
	WindowModified       bool      `json:"window_modified"`
}

type ResetRebateCreateInput struct {
	MechanismVersion            int                       `json:"mechanism_version"`
	ForceStatRatioEnabled       bool                      `json:"force_stat_ratio_enabled"`
	ForceStatRatio              string                    `json:"force_stat_ratio"`
	AcknowledgedErrorAccountIDs []int64                   `json:"acknowledged_error_account_ids"`
	Accounts                    []ResetRebateAccountInput `json:"accounts"`
}

type ResetRebateActor struct {
	AdminID    int64
	AdminEmail string
}

type ResetRebateWindowDefault struct {
	AccountID     int64  `json:"account_id"`
	PeriodStart   string `json:"period_start"`
	PeriodEnd     string `json:"period_end"`
	HistoryCount  int    `json:"history_count"`
	WindowSource  string `json:"window_source"`
	WindowVersion string `json:"window_version"`
	Risk          string `json:"risk"`
	AutoStatRatio string `json:"auto_stat_ratio"`
	AccountStatus string `json:"account_status"`
	ErrorMessage  string `json:"error_message"`
}

type ResetRebateBatchView struct {
	ID                    int64      `json:"id"`
	MechanismVersion      int        `json:"mechanism_version"`
	GroupID               *int64     `json:"group_id,omitempty"`
	GroupName             string     `json:"group_name,omitempty"`
	AdminID               int64      `json:"admin_id"`
	AdminEmail            string     `json:"admin_email"`
	Status                string     `json:"status"`
	FailureStage          string     `json:"failure_stage,omitempty"`
	ExecutionMode         string     `json:"execution_mode,omitempty"`
	ExecutionCursorUserID int64      `json:"execution_cursor_user_id"`
	InitialIssuedAt       *time.Time `json:"initial_issued_at,omitempty"`
	ForceStatRatioEnabled bool       `json:"force_stat_ratio_enabled"`
	ForceStatRatio        string     `json:"force_stat_ratio"`
	AccountCount          int        `json:"account_count"`
	RiskAccountCount      int        `json:"risk_account_count"`
	ProgressTotal         int        `json:"progress_total"`
	ProgressCompleted     int        `json:"progress_completed"`
	PeriodStart           *time.Time `json:"period_start,omitempty"`
	PeriodEnd             *time.Time `json:"period_end,omitempty"`
	RawAmount             string     `json:"raw_amount"`
	WeightedAmount        string     `json:"weighted_amount"`
	ExpectedAmount        string     `json:"expected_amount"`
	SuccessfulAmount      string     `json:"successful_amount"`
	FailedAmount          string     `json:"failed_amount"`
	ExcludedAmount        string     `json:"excluded_amount"`
	PayoutRatio           *int       `json:"payout_ratio,omitempty"`
	RebateReason          string     `json:"rebate_reason"`
	PreviewVersion        int        `json:"preview_version"`
	ExpectedUserCount     int        `json:"expected_user_count"`
	SuccessfulUserCount   int        `json:"successful_user_count"`
	ExcludedUserCount     int        `json:"excluded_user_count"`
	FailedUserCount       int        `json:"failed_user_count"`
	FailureCode           string     `json:"failure_code,omitempty"`
	FailureMessage        string     `json:"failure_message,omitempty"`
	ExecutedByAdminID     *int64     `json:"executed_by_admin_id,omitempty"`
	ExecutedByAdminEmail  string     `json:"executed_by_admin_email,omitempty"`
	FirstExecutedAt       *time.Time `json:"first_executed_at,omitempty"`
	LastRetryAt           *time.Time `json:"last_retry_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

type ResetRebateAccountView struct {
	ID                  int64     `json:"id"`
	AccountID           int64     `json:"account_id"`
	AccountName         string    `json:"account_name"`
	Platform            string    `json:"platform"`
	AccountType         string    `json:"account_type"`
	IsShadow            bool      `json:"is_shadow"`
	AccountStatus       string    `json:"account_status"`
	AccountErrorMessage string    `json:"account_error_message"`
	Schedulable         bool      `json:"schedulable"`
	PeriodStart         time.Time `json:"period_start"`
	PeriodEnd           time.Time `json:"period_end"`
	DefaultWindowSource string    `json:"default_window_source"`
	WindowRisk          string    `json:"window_risk"`
	RatioMode           string    `json:"ratio_mode"`
	AutoStatRatio       string    `json:"auto_stat_ratio"`
	ManualStatRatio     *string   `json:"manual_stat_ratio,omitempty"`
	EffectiveStatRatio  string    `json:"effective_stat_ratio"`
	RawAmount           string    `json:"raw_amount"`
	WeightedAmount      string    `json:"weighted_amount"`
}

type ResetRebateUserView struct {
	ID                 int64      `json:"id"`
	UserID             int64      `json:"user_id"`
	Email              string     `json:"email"`
	Username           string     `json:"username"`
	UserStatus         string     `json:"user_status"`
	UserDeleted        bool       `json:"user_deleted"`
	RawAmount          string     `json:"raw_amount"`
	WeightedAmount     string     `json:"weighted_amount"`
	ExpectedAmount     string     `json:"expected_amount"`
	ActualIssuedAmount string     `json:"actual_issued_amount"`
	Result             string     `json:"result"`
	ExclusionReason    string     `json:"exclusion_reason,omitempty"`
	ErrorCode          string     `json:"error_code,omitempty"`
	ErrorMessage       string     `json:"error_message,omitempty"`
	AttemptCount       int        `json:"attempt_count"`
	FirstFailedAt      *time.Time `json:"first_failed_at,omitempty"`
	LastAttemptAt      *time.Time `json:"last_attempt_at,omitempty"`
	GrantID            *int64     `json:"grant_id,omitempty"`
	IssuedAt           *time.Time `json:"issued_at,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
}

type ResetRebateContributionView struct {
	AccountID          int64     `json:"account_id"`
	AccountName        string    `json:"account_name"`
	PeriodStart        time.Time `json:"period_start"`
	PeriodEnd          time.Time `json:"period_end"`
	RawAmount          string    `json:"raw_amount"`
	EffectiveStatRatio string    `json:"effective_stat_ratio"`
	WeightedAmount     string    `json:"weighted_amount"`
}

type ResetRebatePage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
}

type resetRebateAccountSnapshot struct {
	ID           int64
	Name         string
	Platform     string
	Type         string
	Status       string
	ErrorMessage string
	Schedulable  bool
	IsShadow     bool
}

// CalculateResetRebateAutoRatio 按精确 UTC 时长计算并向零截断到 8 位。
func CalculateResetRebateAutoRatio(start, end time.Time) (decimal.Decimal, error) {
	if !start.Before(end) {
		return decimal.Zero, infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_WINDOW", "period_start must be before period_end")
	}
	duration := decimal.NewFromInt(end.Sub(start).Nanoseconds())
	week := decimal.NewFromInt(resetRebateValidity.Nanoseconds())
	ratio := week.Sub(duration).Div(week).Mul(resetRebateHundred)
	if ratio.IsNegative() {
		ratio = decimal.Zero
	}
	if ratio.GreaterThan(resetRebateHundred) {
		ratio = resetRebateHundred
	}
	return ratio.Truncate(8), nil
}

func parseResetRebateRatio(raw string, field string) (decimal.Decimal, error) {
	value, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || value.IsNegative() || value.GreaterThan(resetRebateHundred) || value.Exponent() < -8 {
		return decimal.Zero, infraerrors.Newf(http.StatusBadRequest, "INVALID_RESET_REBATE_RATIO", "%s must be between 0 and 100 with at most 8 decimal places", field)
	}
	return value.Truncate(8), nil
}

func normalizeResetRebateReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ResetRebateDefaultReason, nil
	}
	if utf8.RuneCountInString(reason) > 100 {
		return "", infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_REASON", "reason must not exceed 100 characters")
	}
	return reason, nil
}

func decimalString(value decimal.Decimal, places int32) string {
	return value.Truncate(places).StringFixed(places)
}

// AccountWindowDefaults 返回服务端权威默认窗口，前端不得自行推测。
func (s *ResetRebateService) AccountWindowDefaults(ctx context.Context, accountIDs []int64) ([]ResetRebateWindowDefault, error) {
	if s == nil || s.db == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "RESET_REBATE_NOT_CONFIGURED", "reset rebate service is not configured")
	}
	ids, err := normalizeResetRebateIDs(accountIDs)
	if err != nil {
		return nil, err
	}
	accounts, err := s.loadAccountSnapshots(ctx, ids)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	windowsByAccount, err := s.latestWindowStartsByAccount(ctx, ids, 2)
	if err != nil {
		return nil, err
	}
	result := make([]ResetRebateWindowDefault, 0, len(ids))
	for _, id := range ids {
		account, ok := accounts[id]
		if !ok {
			return nil, infraerrors.Newf(http.StatusNotFound, "RESET_REBATE_ACCOUNT_NOT_FOUND", "account %d not found", id)
		}
		windows := windowsByAccount[id]
		start, end, source, risk := now.Add(-resetRebateValidity), now, "fallback", "no_history"
		if len(windows) >= 2 {
			start, end, source, risk = windows[1], windows[0], "history", ""
		} else if len(windows) == 1 {
			start, end, source, risk = windows[0], now, "current_window", "single_history"
		}
		ratio, ratioErr := CalculateResetRebateAutoRatio(start, end)
		if ratioErr != nil {
			return nil, ratioErr
		}
		result = append(result, ResetRebateWindowDefault{
			AccountID: id, PeriodStart: start.Format(time.RFC3339), PeriodEnd: end.Format(time.RFC3339),
			HistoryCount: len(windows), WindowSource: source, WindowVersion: resetRebateWindowVersion(windows), Risk: risk,
			AutoStatRatio: decimalString(ratio, 8), AccountStatus: account.Status, ErrorMessage: account.ErrorMessage,
		})
	}
	return result, nil
}

func normalizeResetRebateIDs(accountIDs []int64) ([]int64, error) {
	if len(accountIDs) == 0 {
		return nil, infraerrors.New(http.StatusBadRequest, "RESET_REBATE_ACCOUNTS_REQUIRED", "at least one account is required")
	}
	seen := make(map[int64]struct{}, len(accountIDs))
	ids := make([]int64, 0, len(accountIDs))
	for _, id := range accountIDs {
		if id <= 0 {
			return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_ACCOUNT", "account_id must be positive")
		}
		if _, exists := seen[id]; exists {
			return nil, infraerrors.New(http.StatusBadRequest, "DUPLICATE_RESET_REBATE_ACCOUNT", "account_id must not be duplicated")
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *ResetRebateService) loadAccountSnapshots(ctx context.Context, accountIDs []int64) (map[int64]resetRebateAccountSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, platform, type, status, COALESCE(error_message, ''), schedulable,
		       parent_account_id IS NOT NULL
		FROM accounts WHERE deleted_at IS NULL AND id = ANY($1)
	`, pq.Array(accountIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[int64]resetRebateAccountSnapshot, len(accountIDs))
	for rows.Next() {
		var item resetRebateAccountSnapshot
		if err = rows.Scan(&item.ID, &item.Name, &item.Platform, &item.Type, &item.Status, &item.ErrorMessage, &item.Schedulable, &item.IsShadow); err != nil {
			return nil, err
		}
		result[item.ID] = item
	}
	return result, rows.Err()
}

func (s *ResetRebateService) latestWindowStartsByAccount(ctx context.Context, accountIDs []int64, limit int) (map[int64][]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id, window_started_at
		FROM (
			SELECT account_id, window_started_at,
			       ROW_NUMBER() OVER(PARTITION BY account_id ORDER BY window_started_at DESC) AS row_number
			FROM account_usage_window_histories
			WHERE account_id = ANY($1) AND window_kind = 'codex_7d'
		) ranked
		WHERE row_number <= $2
		ORDER BY account_id, window_started_at DESC
	`, pq.Array(accountIDs), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make(map[int64][]time.Time, len(accountIDs))
	for rows.Next() {
		var accountID int64
		var value time.Time
		if err = rows.Scan(&accountID, &value); err != nil {
			return nil, err
		}
		result[accountID] = append(result[accountID], value.UTC())
	}
	return result, rows.Err()
}

func resetRebateWindowVersion(windows []time.Time) string {
	parts := make([]string, 0, len(windows)+1)
	parts = append(parts, strconv.Itoa(len(windows)))
	for _, value := range windows {
		parts = append(parts, value.UTC().Format(time.RFC3339Nano))
	}
	return strings.Join(parts, ":")
}

// CreateStats 冻结账号配置并启动纯本地消费聚合任务。
func (s *ResetRebateService) CreateStats(ctx context.Context, actor ResetRebateActor, input ResetRebateCreateInput) (*ResetRebateBatchView, error) {
	if input.MechanismVersion != ResetRebateMechanismV2 {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_MECHANISM", "mechanism_version must be 2")
	}
	ids := make([]int64, len(input.Accounts))
	for i := range input.Accounts {
		ids[i] = input.Accounts[i].AccountID
	}
	ids, err := normalizeResetRebateIDs(ids)
	if err != nil {
		return nil, err
	}
	forceRatio, err := parseResetRebateRatio(input.ForceStatRatio, "force_stat_ratio")
	if err != nil {
		return nil, err
	}
	accounts, err := s.loadAccountSnapshots(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(accounts) != len(ids) {
		return nil, infraerrors.New(http.StatusNotFound, "RESET_REBATE_ACCOUNT_NOT_FOUND", "one or more accounts do not exist")
	}
	errorIDs := make([]int64, 0)
	for _, id := range ids {
		if accounts[id].Status == StatusError {
			errorIDs = append(errorIDs, id)
		}
	}
	sort.Slice(errorIDs, func(i, j int) bool { return errorIDs[i] < errorIDs[j] })
	acknowledged, ackErr := normalizeOptionalResetRebateIDs(input.AcknowledgedErrorAccountIDs)
	if ackErr != nil || !equalInt64Slices(errorIDs, acknowledged) {
		return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_ERROR_ACCOUNTS_CHANGED", "error account confirmation is required").WithMetadata(map[string]string{
			"error_account_ids": joinInt64s(errorIDs),
		})
	}
	defaults, err := s.AccountWindowDefaults(ctx, ids)
	if err != nil {
		return nil, err
	}
	defaultByID := make(map[int64]ResetRebateWindowDefault, len(defaults))
	for _, item := range defaults {
		defaultByID[item.AccountID] = item
	}
	now := time.Now().UTC()
	type preparedAccount struct {
		input     ResetRebateAccountInput
		auto      decimal.Decimal
		manual    *decimal.Decimal
		effective decimal.Decimal
		source    string
		risk      string
	}
	prepared := make([]preparedAccount, 0, len(input.Accounts))
	var earliest, latest time.Time
	riskCount := 0
	for _, accountInput := range input.Accounts {
		accountInput.PeriodStart = accountInput.PeriodStart.UTC()
		accountInput.PeriodEnd = accountInput.PeriodEnd.UTC()
		if !accountInput.PeriodStart.Before(accountInput.PeriodEnd) || accountInput.PeriodEnd.After(now.Add(time.Second)) {
			return nil, infraerrors.Newf(http.StatusBadRequest, "INVALID_RESET_REBATE_WINDOW", "invalid time window for account %d", accountInput.AccountID)
		}
		auto, calcErr := CalculateResetRebateAutoRatio(accountInput.PeriodStart, accountInput.PeriodEnd)
		if calcErr != nil {
			return nil, calcErr
		}
		var manual *decimal.Decimal
		effective := auto
		if accountInput.RatioMode == ResetRebateRatioManual {
			if accountInput.ManualRatio == nil {
				return nil, infraerrors.New(http.StatusBadRequest, "RESET_REBATE_MANUAL_RATIO_REQUIRED", "manual_ratio is required in manual mode")
			}
			value, ratioErr := parseResetRebateRatio(*accountInput.ManualRatio, "manual_ratio")
			if ratioErr != nil {
				return nil, ratioErr
			}
			manual, effective = &value, value
		} else if accountInput.RatioMode != ResetRebateRatioAuto {
			return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_RATIO_MODE", "ratio_mode must be auto or manual")
		}
		if input.ForceStatRatioEnabled {
			effective = forceRatio
		}
		defaultValue := defaultByID[accountInput.AccountID]
		source, risk := "manual", ""
		if !accountInput.WindowModified {
			if accountInput.DefaultWindowVersion == "" || accountInput.DefaultWindowVersion != defaultValue.WindowVersion {
				return nil, infraerrors.Newf(http.StatusConflict, "RESET_REBATE_ACCOUNT_WINDOW_CHANGED", "default window changed for account %d", accountInput.AccountID)
			}
			source, risk = defaultValue.WindowSource, defaultValue.Risk
			if risk != "" {
				riskCount++
			}
		}
		prepared = append(prepared, preparedAccount{accountInput, auto, manual, effective, source, risk})
		if earliest.IsZero() || accountInput.PeriodStart.Before(earliest) {
			earliest = accountInput.PeriodStart
		}
		if latest.IsZero() || accountInput.PeriodEnd.After(latest) {
			latest = accountInput.PeriodEnd
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if actor.AdminEmail == "" {
		_ = tx.QueryRowContext(ctx, "SELECT email FROM users WHERE id = $1", actor.AdminID).Scan(&actor.AdminEmail)
	}
	var batchID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO reset_rebate_batches(
			mechanism_version, admin_id, admin_email, period_start, period_end, status,
			force_stat_ratio_enabled, force_stat_ratio, account_count, risk_account_count,
			progress_total, progress_completed, rebate_reason
		) VALUES(2, $1, $2, $3, $4, 'running', $5, $6, $7, $8, $7, 0, $9)
		RETURNING id
	`, actor.AdminID, actor.AdminEmail, earliest, latest, input.ForceStatRatioEnabled,
		decimalString(forceRatio, 8), len(prepared), riskCount, ResetRebateDefaultReason).Scan(&batchID)
	if err != nil {
		return nil, err
	}
	for _, item := range prepared {
		account := accounts[item.input.AccountID]
		var manual any
		if item.manual != nil {
			manual = decimalString(*item.manual, 8)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO reset_rebate_account_items(
				batch_id, account_id, account_name, platform, account_type, is_shadow,
				account_status, account_error_message, schedulable, period_start, period_end,
				default_window_source, window_risk, ratio_mode, auto_stat_ratio,
				manual_stat_ratio, effective_stat_ratio
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		`, batchID, account.ID, account.Name, account.Platform, account.Type, account.IsShadow,
			account.Status, account.ErrorMessage, account.Schedulable, item.input.PeriodStart, item.input.PeriodEnd,
			item.source, item.risk, item.input.RatioMode, decimalString(item.auto, 8), manual, decimalString(item.effective, 8))
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	go s.runStatisticsBackground(batchID)
	return s.GetBatch(ctx, batchID)
}

func normalizeOptionalResetRebateIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	result, err := normalizeResetRebateIDs(ids)
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func equalInt64Slices(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func joinInt64s(values []int64) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.FormatInt(value, 10)
	}
	return strings.Join(parts, ",")
}

func (s *ResetRebateService) batchMutex(batchID int64) *sync.Mutex {
	value, _ := s.batchLocks.LoadOrStore(batchID, &sync.Mutex{})
	mutex, ok := value.(*sync.Mutex)
	if !ok {
		panic("reset rebate batch lock has unexpected type")
	}
	return mutex
}

func (s *ResetRebateService) runStatisticsBackground(batchID int64) {
	var runErr error
	ctx, cancel := context.WithTimeout(context.Background(), resetRebateTaskTimeout)
	defer func() {
		cancel()
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("statistics panic: %v", recovered)
		}
		if runErr == nil {
			return
		}
		logger.LegacyPrintf("service.reset_rebate", "统计失败 batch=%d err=%v", batchID, runErr)
		failureCtx, failureCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer failureCancel()
		_, _ = s.db.ExecContext(failureCtx, `UPDATE reset_rebate_batches SET status='failed', failure_stage='statistics',
			failure_code='RESET_REBATE_STATISTICS_FAILED', failure_message='本地消费统计失败', updated_at=NOW() WHERE id=$1`, batchID)
	}()
	if err := s.runStatistics(ctx, batchID); err != nil {
		runErr = err
	}
}

func (s *ResetRebateService) recoverRunningBatches() {
	if s == nil || s.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM reset_rebate_batches WHERE mechanism_version=2 AND status='running'")
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		go s.runStatisticsBackground(id)
	}
	executionRows, err := s.db.QueryContext(ctx, "SELECT id FROM reset_rebate_batches WHERE mechanism_version=2 AND status='executing'")
	if err != nil {
		return
	}
	defer func() { _ = executionRows.Close() }()
	executionIDs := make([]int64, 0)
	for executionRows.Next() {
		var id int64
		if executionRows.Scan(&id) == nil {
			executionIDs = append(executionIDs, id)
		}
	}
	for _, id := range executionIDs {
		go s.runExecutionBackground(id)
	}
}

func parseDecimalDB(raw string) (decimal.Decimal, error) {
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("invalid decimal %q: %w", raw, err)
	}
	return value, nil
}
