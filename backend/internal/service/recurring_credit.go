package service

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const (
	RecurringCreditStatusActive    = "active"
	RecurringCreditStatusStopped   = "stopped"
	RecurringCreditStatusCompleted = "completed"
	RecurringCreditStatusDeleted   = "deleted"

	RecurringCreditModeFinite                 = "finite"
	RecurringCreditModePermanent              = "permanent"
	RecurringCreditMonthly                    = "monthly"
	RecurringCreditImmediate                  = "immediate"
	RecurringCreditWeekly                     = "weekly"
	LimitedCreditSourceRecurring              = "recurring_grant"
	RecurringCreditEligibilityLegacy          = "period_usage_or_recharge"
	RecurringCreditEligibilityRollingActivity = "rolling_30d_activity_v1"
	recurringCreditActivityPeriod             = 30 * 24 * time.Hour
)

// RecurringCreditTaskInput 是循环赠额任务创建和完整编辑的输入。
type RecurringCreditTaskInput struct {
	Name            string  `json:"name"`
	AdminNotes      string  `json:"admin_notes"`
	ScheduleType    string  `json:"schedule_type"`
	DayOfMonth      *int    `json:"day_of_month"`
	DayOfWeek       *int    `json:"day_of_week"`
	ValidityDays    *int    `json:"validity_days"`
	LocalTime       string  `json:"local_time"`
	Timezone        string  `json:"timezone"`
	Amount          float64 `json:"amount"`
	ExecutionMode   string  `json:"execution_mode"`
	RemainingRuns   *int    `json:"remaining_runs"`
	InitiallyActive bool    `json:"initially_active"`
}

// RecurringCreditActor 保存管理员操作审计身份。
type RecurringCreditActor struct {
	AdminID int64
	IP      string
}

// RecurringCreditTaskView 是管理端任务视图。
type RecurringCreditTaskView struct {
	ID                  int64      `json:"id"`
	Name                string     `json:"name"`
	AdminNotes          string     `json:"admin_notes"`
	ScheduleType        string     `json:"schedule_type"`
	DayOfMonth          *int       `json:"day_of_month,omitempty"`
	DayOfWeek           *int       `json:"day_of_week,omitempty"`
	LocalTime           string     `json:"local_time"`
	ValidityDays        *int       `json:"validity_days,omitempty"`
	Timezone            string     `json:"timezone"`
	Amount              float64    `json:"amount"`
	ExecutionMode       string     `json:"execution_mode"`
	RemainingRuns       *int       `json:"remaining_runs,omitempty"`
	SkipCount           int        `json:"skip_count"`
	Status              string     `json:"status"`
	NextRunAt           *time.Time `json:"next_run_at,omitempty"`
	NextRunLocal        string     `json:"next_run_local,omitempty"`
	ScheduleDescription string     `json:"schedule_description"`
	Version             int        `json:"version"`
	LatestBatchStatus   string     `json:"latest_batch_status,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// RecurringCreditBatchView 是管理端批次视图。
type RecurringCreditBatchView struct {
	ID                    int64      `json:"id"`
	TaskID                int64      `json:"task_id"`
	TaskName              string     `json:"task_name"`
	ScheduledAt           time.Time  `json:"scheduled_at"`
	ExpiresAt             time.Time  `json:"expires_at"`
	QualificationStart    time.Time  `json:"qualification_start"`
	QualificationEnd      time.Time  `json:"qualification_end"`
	QualificationCutoffAt *time.Time `json:"qualification_cutoff_at,omitempty"`
	ConfigVersion         int        `json:"config_version"`
	EligibilityPolicy     string     `json:"eligibility_policy"`
	ValidityDays          *int       `json:"validity_days,omitempty"`
	Amount                float64    `json:"amount"`
	Timezone              string     `json:"timezone"`
	Status                string     `json:"status"`
	AttemptCount          int        `json:"attempt_count"`
	EligibleUserCount     int        `json:"eligible_user_count"`
	IssuedUserCount       int        `json:"issued_user_count"`
	ExcludedUserCount     int        `json:"excluded_user_count"`
	UsageEligibleCount    int        `json:"usage_eligible_count"`
	RechargeEligibleCount int        `json:"recharge_eligible_count"`
	IssuedAmount          float64    `json:"issued_amount"`
	FailureCode           string     `json:"failure_code,omitempty"`
	FailureMessage        string     `json:"failure_message,omitempty"`
	APIActiveCount        int        `json:"api_active_count"`
	SiteActiveCount       int        `json:"site_active_count"`
	BothActiveCount       int        `json:"both_active_count"`
	SnapshotCompletedAt   *time.Time `json:"snapshot_completed_at,omitempty"`
	FinishedAt            *time.Time `json:"finished_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
}

// RecurringCreditUserItemView 是批次逐用户资格与发放视图。
type RecurringCreditUserItemView struct {
	ID                  int64      `json:"id"`
	BatchID             int64      `json:"batch_id"`
	UserID              int64      `json:"user_id"`
	Email               string     `json:"email"`
	Username            string     `json:"username"`
	UserStatus          string     `json:"user_status"`
	UserDeleted         bool       `json:"user_deleted"`
	ActualCost          float64    `json:"actual_cost"`
	NetRecharge         float64    `json:"net_recharge"`
	QualificationReason string     `json:"qualification_reason"`
	APILastUsedAt       *time.Time `json:"api_last_used_at,omitempty"`
	SiteLastActiveAt    *time.Time `json:"site_last_active_at,omitempty"`
	GrantAmount         float64    `json:"grant_amount"`
	GrantID             *int64     `json:"grant_id,omitempty"`
	Result              string     `json:"result"`
	ExclusionReason     string     `json:"exclusion_reason,omitempty"`
}

// RecurringCreditPreview 提供创建或重大修改前的参考成本摘要。
type RecurringCreditPreview struct {
	Amount                 float64     `json:"amount"`
	NextRunAt              time.Time   `json:"next_run_at"`
	NextRunLocal           string      `json:"next_run_local"`
	ExpiresAt              time.Time   `json:"expires_at"`
	QualificationStart     time.Time   `json:"qualification_start"`
	QualificationEnd       time.Time   `json:"qualification_end"`
	ReferenceEligibleCount int         `json:"reference_eligible_count"`
	EstimatedTotal         float64     `json:"estimated_total"`
	Disclaimer             string      `json:"disclaimer"`
	APIActiveCount         int         `json:"api_active_count"`
	SiteActiveCount        int         `json:"site_active_count"`
	BothActiveCount        int         `json:"both_active_count"`
	SkippedRuns            []time.Time `json:"skipped_runs,omitempty"`
}

// RecurringCreditListFilter 定义任务列表筛选条件。
type RecurringCreditListFilter struct {
	Search, Status, Mode, ScheduleType string
	IncludeDeleted                     bool
}

// RecurringCreditService 管理循环任务、成本预估和历史查询。
type RecurringCreditService struct {
	db                   *sql.DB
	defaultTimezone      string
	authCacheInvalidator APIKeyAuthCacheInvalidator
	billingCache         *BillingCacheService
	wakeCh               chan struct{}
}

// NewRecurringCreditService 创建循环赠额服务。
func NewRecurringCreditService(db *sql.DB, cfg *config.Config, auth APIKeyAuthCacheInvalidator, billing *BillingCacheService) *RecurringCreditService {
	tz := "Asia/Shanghai"
	if cfg != nil && strings.TrimSpace(cfg.Timezone) != "" {
		tz = strings.TrimSpace(cfg.Timezone)
	}
	return &RecurringCreditService{db: db, defaultTimezone: tz, authCacheInvalidator: auth, billingCache: billing, wakeCh: make(chan struct{}, 1)}
}

// notifyRunner 唤醒执行器，使立即任务无需等待下一分钟扫描。
func (s *RecurringCreditService) notifyRunner() {
	if s == nil || s.wakeCh == nil {
		return
	}
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}
func (s *RecurringCreditService) validateInput(input *RecurringCreditTaskInput) error {
	input.Name = strings.TrimSpace(input.Name)
	input.AdminNotes = strings.TrimSpace(input.AdminNotes)
	input.ScheduleType = strings.TrimSpace(input.ScheduleType)
	input.LocalTime = strings.TrimSpace(input.LocalTime)
	input.Timezone = strings.TrimSpace(input.Timezone)
	input.ExecutionMode = strings.TrimSpace(input.ExecutionMode)
	if input.Timezone == "" {
		input.Timezone = s.defaultTimezone
	}
	if input.Name == "" || len([]rune(input.Name)) > 100 || len([]rune(input.AdminNotes)) > 1000 {
		return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_NAME", "name must be 1-100 characters and admin_notes at most 1000 characters")
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_TIMEZONE", "timezone must be a valid IANA timezone")
	}
	if input.ScheduleType == RecurringCreditImmediate {
		if input.ValidityDays == nil || *input.ValidityDays < 1 || *input.ValidityDays > 36500 {
			return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_VALIDITY", "immediate mode requires validity_days between 1 and 36500")
		}
		amount := decimal.NewFromFloat(input.Amount)
		if amount.LessThan(decimal.NewFromFloat(0.01)) || amount.GreaterThan(decimal.NewFromInt(10000)) || amount.Exponent() < -8 {
			return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_AMOUNT", "amount must be between 0.01 and 10000 with at most 8 decimal places")
		}
		one := 1
		input.DayOfMonth = nil
		input.DayOfWeek = nil
		input.LocalTime = ""
		input.ExecutionMode = RecurringCreditModeFinite
		input.RemainingRuns = &one
		input.InitiallyActive = true
		return nil
	}
	input.ValidityDays = nil
	if _, _, err := parseLocalTime(input.LocalTime); err != nil {
		return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_TIME", "local_time must be HH:mm")
	}
	switch input.ScheduleType {
	case RecurringCreditMonthly:
		if input.DayOfMonth == nil || *input.DayOfMonth < 1 || *input.DayOfMonth > 28 {
			return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_DAY", "day_of_month must be between 1 and 28")
		}
		input.DayOfWeek = nil
	case RecurringCreditWeekly:
		if input.DayOfWeek == nil || *input.DayOfWeek < 1 || *input.DayOfWeek > 7 {
			return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_WEEKDAY", "day_of_week must be between 1 and 7")
		}
		input.DayOfMonth = nil
	default:
		return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_SCHEDULE", "schedule_type must be immediate, monthly or weekly")
	}
	amount := decimal.NewFromFloat(input.Amount)
	if amount.LessThan(decimal.NewFromFloat(0.01)) || amount.GreaterThan(decimal.NewFromInt(10000)) || amount.Exponent() < -8 {
		return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_AMOUNT", "amount must be between 0.01 and 10000 with at most 8 decimal places")
	}
	switch input.ExecutionMode {
	case RecurringCreditModeFinite:
		if input.RemainingRuns == nil || *input.RemainingRuns < 1 {
			return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_RUNS", "finite mode requires a positive remaining_runs")
		}
	case RecurringCreditModePermanent:
		input.RemainingRuns = nil
	default:
		return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_MODE", "execution_mode must be finite or permanent")
	}
	return nil
}

// Preview 计算下一计划时点，并按当前滚动 30 天活跃用户预估成本。
func (s *RecurringCreditService) Preview(ctx context.Context, input RecurringCreditTaskInput, skipCount int) (*RecurringCreditPreview, error) {
	if err := s.validateInput(&input); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if input.ScheduleType == RecurringCreditImmediate {
		if skipCount != 0 {
			return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_SKIP", "immediate tasks cannot skip executions")
		}
		stats, err := s.activityStats(ctx, now)
		if err != nil {
			return nil, err
		}
		expires := now.AddDate(0, 0, *input.ValidityDays)
		start, end := recurringCreditActivityWindow(now)
		return &RecurringCreditPreview{
			Amount: input.Amount, NextRunAt: now, NextRunLocal: now.In(mustLocation(input.Timezone)).Format(time.RFC3339),
			ExpiresAt: expires, QualificationStart: start, QualificationEnd: end,
			ReferenceEligibleCount: stats.EligibleCount, APIActiveCount: stats.APIActiveCount,
			SiteActiveCount: stats.SiteActiveCount, BothActiveCount: stats.BothActiveCount,
			EstimatedTotal: decimal.NewFromFloat(input.Amount).Mul(decimal.NewFromInt(int64(stats.EligibleCount))).InexactFloat64(),
			Disclaimer:     "近 30 天 API 或站内活跃用户；实际名单以执行器领取任务时的资格快照为准",
		}, nil
	}
	next, err := nextRecurringOccurrence(input, now)
	if err != nil {
		return nil, err
	}
	expires, err := nextRecurringOccurrence(input, next)
	if err != nil {
		return nil, err
	}
	stats, err := s.activityStats(ctx, now)
	if err != nil {
		return nil, err
	}
	start, end := recurringCreditActivityWindow(now)
	preview := &RecurringCreditPreview{
		Amount: input.Amount, NextRunAt: next, NextRunLocal: next.In(mustLocation(input.Timezone)).Format(time.RFC3339), ExpiresAt: expires,
		QualificationStart: start, QualificationEnd: end, ReferenceEligibleCount: stats.EligibleCount,
		APIActiveCount: stats.APIActiveCount, SiteActiveCount: stats.SiteActiveCount, BothActiveCount: stats.BothActiveCount,
		EstimatedTotal: decimal.NewFromFloat(input.Amount).Mul(decimal.NewFromInt(int64(stats.EligibleCount))).InexactFloat64(), Disclaimer: "近 30 天 API 或站内活跃用户；实际名单以执行器领取任务时的资格快照为准",
	}
	if skipCount > 0 {
		if skipCount > 100 {
			return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_SKIP", "skip_count must be between 1 and 100")
		}
		cursor := next
		for i := 0; i < skipCount; i++ {
			preview.SkippedRuns = append(preview.SkippedRuns, cursor)
			cursor, err = nextRecurringOccurrence(input, cursor)
			if err != nil {
				return nil, err
			}
		}
	}
	return preview, nil
}

// CreateTask 创建运行中或已停止任务，并写入幂等键和操作审计。
func (s *RecurringCreditService) CreateTask(ctx context.Context, input RecurringCreditTaskInput, actor RecurringCreditActor, idempotencyKey string) (*RecurringCreditTaskView, error) {
	if err := s.validateInput(&input); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "RECURRING_CREDIT_NOT_CONFIGURED", "recurring credit service is not configured")
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(idempotencyKey) > 128 {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "idempotency key is too long")
	}
	if idempotencyKey != "" {
		if existing, err := s.getTaskByIdempotencyKey(ctx, idempotencyKey); err == nil {
			return existing, nil
		}
	}
	status := RecurringCreditStatusStopped
	var next *time.Time
	if input.InitiallyActive {
		status = RecurringCreditStatusActive
		value := time.Now().UTC()
		var err error
		if input.ScheduleType != RecurringCreditImmediate {
			value, err = nextRecurringOccurrence(input, value)
		}
		if err != nil {
			return nil, err
		}
		next = &value
	}
	email := s.adminEmail(ctx, actor.AdminID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO recurring_credit_tasks
		(name,admin_notes,schedule_type,day_of_month,day_of_week,validity_days,local_time,timezone,amount,execution_mode,remaining_runs,status,next_run_at,idempotency_key,created_by_admin_id,created_by_admin_email,updated_by_admin_id,updated_by_admin_email)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),$15,$16,$15,$16) RETURNING id`, input.Name, input.AdminNotes, input.ScheduleType, input.DayOfMonth, input.DayOfWeek, input.ValidityDays, input.LocalTime, input.Timezone, input.Amount, input.ExecutionMode, input.RemainingRuns, status, next, idempotencyKey, actor.AdminID, email).Scan(&id)
	if err != nil {
		if idempotencyKey != "" {
			if existing, queryErr := s.getTaskByIdempotencyKey(ctx, idempotencyKey); queryErr == nil {
				return existing, nil
			}
		}
		return nil, err
	}
	after, err := queryTaskTx(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if err = insertRecurringAudit(ctx, tx, id, actor, email, "create", nil, after); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	if input.ScheduleType == RecurringCreditImmediate {
		s.notifyRunner()
	}
	return after, nil
}

// UpdateTask 完整更新未来配置，并通过 expectedVersion 执行乐观锁校验。
func (s *RecurringCreditService) UpdateTask(ctx context.Context, id int64, input RecurringCreditTaskInput, expectedVersion int, actor RecurringCreditActor) (*RecurringCreditTaskView, error) {
	if err := s.validateInput(&input); err != nil {
		return nil, err
	}
	return s.mutateTask(ctx, id, expectedVersion, actor, "update", func(ctx context.Context, tx *sql.Tx, before *RecurringCreditTaskView) error {
		if before.ScheduleType == RecurringCreditImmediate || input.ScheduleType == RecurringCreditImmediate {
			return invalidTaskState("immediate tasks cannot be edited or converted")
		}
		if input.ExecutionMode != before.ExecutionMode {
			return infraerrors.New(http.StatusBadRequest, "RECURRING_CREDIT_MODE_ACTION_REQUIRED", "use the explicit mode conversion or reactivation action")
		}
		var next *time.Time
		if before.Status == RecurringCreditStatusActive {
			value, err := nextRecurringOccurrence(input, time.Now().UTC())
			if err != nil {
				return err
			}
			next = &value
		}
		_, err := tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET name=$1,admin_notes=$2,schedule_type=$3,day_of_month=$4,day_of_week=$5,local_time=$6,timezone=$7,amount=$8,execution_mode=$9,remaining_runs=$10,next_run_at=$11 WHERE id=$12`, input.Name, input.AdminNotes, input.ScheduleType, input.DayOfMonth, input.DayOfWeek, input.LocalTime, input.Timezone, input.Amount, input.ExecutionMode, input.RemainingRuns, next, id)
		return err
	})
}

// TaskAction 执行停止、恢复、模式切换、跳过和重新启用等明确状态操作。
func (s *RecurringCreditService) TaskAction(ctx context.Context, id int64, action string, expectedVersion int, count *int, input *RecurringCreditTaskInput, actor RecurringCreditActor) (*RecurringCreditTaskView, error) {
	return s.mutateTask(ctx, id, expectedVersion, actor, action, func(ctx context.Context, tx *sql.Tx, before *RecurringCreditTaskView) error {
		now := time.Now().UTC()
		schedule := taskViewInput(before)
		if before.ScheduleType == RecurringCreditImmediate && action != "delete" {
			return invalidTaskState("immediate tasks only execute once and cannot change lifecycle state")
		}
		switch action {
		case "stop":
			if before.Status != RecurringCreditStatusActive {
				return invalidTaskState("only active tasks can be stopped")
			}
			_, err := tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET status='stopped',next_run_at=NULL WHERE id=$1`, id)
			return err
		case "resume":
			if before.Status != RecurringCreditStatusStopped {
				return invalidTaskState("only stopped tasks can be resumed")
			}
			next, err := nextRecurringOccurrence(schedule, now)
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET status='active',next_run_at=$2 WHERE id=$1`, id, next)
			}
			return err
		case "reactivate":
			if before.Status != RecurringCreditStatusCompleted || input == nil {
				return invalidTaskState("completed task and full configuration are required")
			}
			if err := s.validateInput(input); err != nil {
				return err
			}
			if input.ScheduleType == RecurringCreditImmediate {
				return invalidTaskState("completed tasks cannot be reactivated as immediate tasks")
			}
			next, err := nextRecurringOccurrence(*input, now)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET name=$2,admin_notes=$3,schedule_type=$4,day_of_month=$5,day_of_week=$6,local_time=$7,timezone=$8,amount=$9,execution_mode=$10,remaining_runs=$11,status='active',next_run_at=$12 WHERE id=$1`, id, input.Name, input.AdminNotes, input.ScheduleType, input.DayOfMonth, input.DayOfWeek, input.LocalTime, input.Timezone, input.Amount, input.ExecutionMode, input.RemainingRuns, next)
			return err
		case "make-permanent":
			if before.Status != RecurringCreditStatusActive || before.ExecutionMode != RecurringCreditModeFinite {
				return invalidTaskState("only active finite tasks can become permanent")
			}
			_, err := tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET execution_mode='permanent',remaining_runs=NULL WHERE id=$1`, id)
			return err
		case "make-finite":
			if before.Status != RecurringCreditStatusActive || before.ExecutionMode != RecurringCreditModePermanent || count == nil || *count < 1 {
				return invalidTaskState("active permanent task and positive remaining_runs are required")
			}
			_, err := tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET execution_mode='finite',remaining_runs=$2 WHERE id=$1`, id, *count)
			return err
		case "skip":
			if before.Status != RecurringCreditStatusActive || count == nil || *count < 1 || *count > 100 {
				return invalidTaskState("active task and skip_count between 1 and 100 are required")
			}
			_, err := tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET skip_count=$2 WHERE id=$1`, id, *count)
			return err
		case "cancel-skip":
			if before.Status != RecurringCreditStatusActive {
				return invalidTaskState("only active tasks can cancel skipping")
			}
			_, err := tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET skip_count=0 WHERE id=$1`, id)
			return err
		case "delete":
			_, err := tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET status='deleted',next_run_at=NULL,deleted_at=$2 WHERE id=$1`, id, now)
			return err
		default:
			return infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_ACTION", "unsupported task action")
		}
	})
}

func (s *RecurringCreditService) mutateTask(ctx context.Context, id int64, expectedVersion int, actor RecurringCreditActor, action string, mutate func(context.Context, *sql.Tx, *RecurringCreditTaskView) error) (*RecurringCreditTaskView, error) {
	if expectedVersion < 1 {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RECURRING_CREDIT_VERSION", "expected_version is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	before, err := queryTaskTx(ctx, tx, id, true)
	if err != nil {
		return nil, taskQueryError(err)
	}
	if before.Status == RecurringCreditStatusDeleted {
		return nil, infraerrors.New(http.StatusNotFound, "RECURRING_CREDIT_TASK_NOT_FOUND", "task not found")
	}
	if before.Version != expectedVersion {
		return nil, infraerrors.New(http.StatusConflict, "RECURRING_CREDIT_TASK_CHANGED", "task has changed, refresh and retry")
	}
	if err = mutate(ctx, tx, before); err != nil {
		return nil, err
	}
	var email string
	_ = tx.QueryRowContext(ctx, `SELECT email FROM users WHERE id=$1`, actor.AdminID).Scan(&email)
	result, err := tx.ExecContext(ctx, `UPDATE recurring_credit_tasks SET version=version+1,updated_at=NOW(),updated_by_admin_id=$2,updated_by_admin_email=$3 WHERE id=$1 AND version=$4`, id, actor.AdminID, email, expectedVersion)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, infraerrors.New(http.StatusConflict, "RECURRING_CREDIT_TASK_CHANGED", "task has changed, refresh and retry")
	}
	after, err := queryTaskTx(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if err = insertRecurringAudit(ctx, tx, id, actor, email, action, before, after); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return after, nil
}

// GetTask 返回任务详情及最近批次状态。
func (s *RecurringCreditService) GetTask(ctx context.Context, id int64) (*RecurringCreditTaskView, error) {
	task, err := queryTaskDB(ctx, s.db, id)
	if err != nil {
		return nil, taskQueryError(err)
	}
	return task, nil
}

func (s *RecurringCreditService) getTaskByIdempotencyKey(ctx context.Context, key string) (*RecurringCreditTaskView, error) {
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM recurring_credit_tasks WHERE idempotency_key=$1`, key).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}

// ListTasks 分页返回循环赠额任务。
func (s *RecurringCreditService) ListTasks(ctx context.Context, page, pageSize int, filter RecurringCreditListFilter) ([]RecurringCreditTaskView, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	where := []string{"1=1"}
	args := []any{}
	add := func(sqlText string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(sqlText, len(args)))
	}
	if !filter.IncludeDeleted {
		where = append(where, "t.status <> 'deleted'")
	}
	if filter.Search != "" {
		args = append(args, "%"+strings.TrimSpace(filter.Search)+"%")
		where = append(where, fmt.Sprintf("(CAST(t.id AS TEXT) ILIKE $%d OR t.name ILIKE $%d)", len(args), len(args)))
	}
	if filter.Status != "" {
		add("t.status=$%d", filter.Status)
	}
	if filter.Mode != "" {
		add("t.execution_mode=$%d", filter.Mode)
	}
	if filter.ScheduleType != "" {
		add("t.schedule_type=$%d", filter.ScheduleType)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recurring_credit_tasks t WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT `+taskSelectColumns+` FROM recurring_credit_tasks t WHERE `+clause+fmt.Sprintf(" ORDER BY t.created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]RecurringCreditTaskView, 0)
	for rows.Next() {
		item, scanErr := scanTask(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, *item)
	}
	return items, total, rows.Err()
}

// ListBatches 分页返回任务执行历史。
func (s *RecurringCreditService) ListBatches(ctx context.Context, taskID int64, page, pageSize int, status string, start, end *time.Time) ([]RecurringCreditBatchView, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	where := []string{"task_id=$1"}
	args := []any{taskID}
	if status != "" {
		args = append(args, status)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	if start != nil {
		args = append(args, *start)
		where = append(where, fmt.Sprintf("scheduled_at >= $%d", len(args)))
	}
	if end != nil {
		args = append(args, *end)
		where = append(where, fmt.Sprintf("scheduled_at < $%d", len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recurring_credit_batches WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT `+batchSelectColumns+` FROM recurring_credit_batches WHERE `+clause+fmt.Sprintf(" ORDER BY scheduled_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]RecurringCreditBatchView, 0)
	for rows.Next() {
		item, e := scanBatch(rows)
		if e != nil {
			return nil, 0, e
		}
		items = append(items, *item)
	}
	return items, total, rows.Err()
}

// ListUserItems 分页返回成功批次的逐用户明细。
func (s *RecurringCreditService) ListUserItems(ctx context.Context, batchID int64, page, pageSize int, search string) ([]RecurringCreditUserItemView, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 50
	}
	args := []any{batchID}
	where := "batch_id=$1"
	if strings.TrimSpace(search) != "" {
		args = append(args, "%"+strings.TrimSpace(search)+"%")
		where += fmt.Sprintf(" AND (CAST(user_id AS TEXT) ILIKE $%d OR email ILIKE $%d OR username ILIKE $%d)", len(args), len(args), len(args))
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recurring_credit_user_items WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT id,batch_id,user_id,email,username,user_status,user_deleted,actual_cost,net_recharge,api_last_used_at,site_last_active_at,qualification_reason,grant_amount,grant_id,result,exclusion_reason FROM recurring_credit_user_items WHERE `+where+fmt.Sprintf(" ORDER BY id LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	items := make([]RecurringCreditUserItemView, 0)
	for rows.Next() {
		var v RecurringCreditUserItemView
		var grant sql.NullInt64
		var apiLastUsed, siteLastActive sql.NullTime
		if err = rows.Scan(&v.ID, &v.BatchID, &v.UserID, &v.Email, &v.Username, &v.UserStatus, &v.UserDeleted, &v.ActualCost, &v.NetRecharge, &apiLastUsed, &siteLastActive, &v.QualificationReason, &v.GrantAmount, &grant, &v.Result, &v.ExclusionReason); err != nil {
			return nil, 0, err
		}
		if grant.Valid {
			v.GrantID = &grant.Int64
		}
		if apiLastUsed.Valid {
			value := apiLastUsed.Time
			v.APILastUsedAt = &value
		}
		if siteLastActive.Valid {
			value := siteLastActive.Time
			v.SiteLastActiveAt = &value
		}
		items = append(items, v)
	}
	return items, total, rows.Err()
}

// ExportUserItemsCSV 按批次资格策略导出对应口径的逐用户审计。
func (s *RecurringCreditService) ExportUserItemsCSV(ctx context.Context, batchID int64, writer io.Writer) error {
	var status, eligibilityPolicy string
	if err := s.db.QueryRowContext(ctx, `SELECT status,eligibility_policy FROM recurring_credit_batches WHERE id=$1`, batchID).Scan(&status, &eligibilityPolicy); err != nil {
		return taskQueryError(err)
	}
	if status != "succeeded" && status != "empty" {
		return infraerrors.New(http.StatusConflict, "RECURRING_CREDIT_BATCH_NOT_EXPORTABLE", "only succeeded or empty batches can be exported")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id,email,username,user_status,user_deleted,actual_cost,net_recharge,api_last_used_at,site_last_active_at,qualification_reason,grant_amount,grant_id,result,exclusion_reason FROM recurring_credit_user_items WHERE batch_id=$1 ORDER BY id`, batchID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	csvWriter := csv.NewWriter(writer)
	defer csvWriter.Flush()
	if eligibilityPolicy == RecurringCreditEligibilityRollingActivity {
		_ = csvWriter.Write([]string{"user_id", "email", "username", "user_status", "user_deleted", "api_last_used_at", "site_last_active_at", "qualification_reason", "grant_amount", "grant_id", "result", "exclusion_reason"})
	} else {
		_ = csvWriter.Write([]string{"user_id", "email", "username", "user_status", "user_deleted", "actual_cost", "net_recharge", "qualification_reason", "grant_amount", "grant_id", "result", "exclusion_reason"})
	}
	for rows.Next() {
		var userID int64
		var email, username, userStatus, reason, result, exclusion string
		var deleted bool
		var actual, recharge, amount float64
		var apiLastUsed, siteLastActive sql.NullTime
		var grant sql.NullInt64
		if err = rows.Scan(&userID, &email, &username, &userStatus, &deleted, &actual, &recharge, &apiLastUsed, &siteLastActive, &reason, &amount, &grant, &result, &exclusion); err != nil {
			return err
		}
		grantText := ""
		if grant.Valid {
			grantText = strconv.FormatInt(grant.Int64, 10)
		}
		if eligibilityPolicy == RecurringCreditEligibilityRollingActivity {
			apiLastUsedText, siteLastActiveText := "", ""
			if apiLastUsed.Valid {
				apiLastUsedText = apiLastUsed.Time.UTC().Format(time.RFC3339)
			}
			if siteLastActive.Valid {
				siteLastActiveText = siteLastActive.Time.UTC().Format(time.RFC3339)
			}
			_ = csvWriter.Write([]string{strconv.FormatInt(userID, 10), email, username, userStatus, strconv.FormatBool(deleted), apiLastUsedText, siteLastActiveText, reason, decimal.NewFromFloat(amount).String(), grantText, result, exclusion})
		} else {
			_ = csvWriter.Write([]string{strconv.FormatInt(userID, 10), email, username, userStatus, strconv.FormatBool(deleted), decimal.NewFromFloat(actual).String(), decimal.NewFromFloat(recharge).String(), reason, decimal.NewFromFloat(amount).String(), grantText, result, exclusion})
		}
	}
	return rows.Err()
}

func (s *RecurringCreditService) adminEmail(ctx context.Context, id int64) string {
	var email string
	_ = s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id=$1`, id).Scan(&email)
	return email
}

type recurringCreditActivityStats struct {
	EligibleCount   int
	APIActiveCount  int
	SiteActiveCount int
	BothActiveCount int
}

// recurringCreditActivityWindow 返回批次展示使用的严格滚动 30 天窗口。
func recurringCreditActivityWindow(cutoff time.Time) (time.Time, time.Time) {
	return cutoff.Add(-recurringCreditActivityPeriod), cutoff
}

func recurringCreditActivityReason(apiActive, siteActive bool) string {
	switch {
	case apiActive && siteActive:
		return "api_and_site_activity"
	case apiActive:
		return "api_activity"
	case siteActive:
		return "site_activity"
	default:
		return ""
	}
}

// activityStats 使用与批次快照一致的查询口径生成预览统计。
func (s *RecurringCreditService) activityStats(ctx context.Context, cutoff time.Time) (recurringCreditActivityStats, error) {
	var stats recurringCreditActivityStats
	err := s.db.QueryRowContext(ctx, rollingActivityStatsSQL, cutoff).Scan(
		&stats.EligibleCount,
		&stats.APIActiveCount,
		&stats.SiteActiveCount,
		&stats.BothActiveCount,
	)
	return stats, err
}

const rollingActivityCandidatesSQL = `WITH api_activity AS (
	SELECT user_id,MAX(last_used_at) AS api_last_used_at
	FROM api_keys
	WHERE last_used_at >= $1-INTERVAL '30 days 1 minute' AND last_used_at < $1
	GROUP BY user_id
), site_activity AS (
	SELECT id AS user_id,last_active_at AS site_last_active_at
	FROM users
	WHERE status='active' AND deleted_at IS NULL
		AND last_active_at >= $1-INTERVAL '30 days' AND last_active_at < $1
), candidates AS (
	SELECT COALESCE(a.user_id,s.user_id) AS user_id,a.api_last_used_at,s.site_last_active_at
	FROM api_activity a FULL OUTER JOIN site_activity s ON s.user_id=a.user_id
)`

const rollingActivityStatsSQL = rollingActivityCandidatesSQL + `
SELECT COUNT(*),
	COUNT(*) FILTER (WHERE c.api_last_used_at IS NOT NULL),
	COUNT(*) FILTER (WHERE c.site_last_active_at IS NOT NULL),
	COUNT(*) FILTER (WHERE c.api_last_used_at IS NOT NULL AND c.site_last_active_at IS NOT NULL)
FROM candidates c
JOIN users u ON u.id=c.user_id
WHERE u.status='active' AND u.deleted_at IS NULL`

const rollingActivitySnapshotSQL = rollingActivityCandidatesSQL + `
INSERT INTO recurring_credit_user_items(batch_id,user_id,email,username,user_status,user_deleted,actual_cost,net_recharge,api_last_used_at,site_last_active_at,qualification_reason,grant_amount,result,exclusion_reason)
SELECT $2,u.id,COALESCE(u.email,''),COALESCE(u.username,''),u.status,FALSE,0,0,c.api_last_used_at,c.site_last_active_at,
	CASE WHEN c.api_last_used_at IS NOT NULL AND c.site_last_active_at IS NOT NULL THEN 'api_and_site_activity' WHEN c.api_last_used_at IS NOT NULL THEN 'api_activity' ELSE 'site_activity' END,
	0,'pending',''
FROM candidates c JOIN users u ON u.id=c.user_id
WHERE u.status='active' AND u.deleted_at IS NULL
ON CONFLICT(batch_id,user_id) DO NOTHING`

const taskSelectColumns = `t.id,t.name,t.admin_notes,t.schedule_type,t.day_of_month,t.day_of_week,t.validity_days,t.local_time,t.timezone,t.amount,t.execution_mode,t.remaining_runs,t.skip_count,t.status,t.next_run_at,t.version,COALESCE((SELECT b.status FROM recurring_credit_batches b WHERE b.task_id=t.id ORDER BY b.scheduled_at DESC LIMIT 1),''),t.created_at,t.updated_at`

func queryTaskDB(ctx context.Context, db *sql.DB, id int64) (*RecurringCreditTaskView, error) {
	return scanTask(db.QueryRowContext(ctx, `SELECT `+taskSelectColumns+` FROM recurring_credit_tasks t WHERE t.id=$1`, id))
}
func queryTaskTx(ctx context.Context, tx *sql.Tx, id int64, lock bool) (*RecurringCreditTaskView, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return scanTask(tx.QueryRowContext(ctx, `SELECT `+taskSelectColumns+` FROM recurring_credit_tasks t WHERE t.id=$1`+suffix, id))
}

type sqlScanner interface{ Scan(...any) error }

func scanTask(scanner sqlScanner) (*RecurringCreditTaskView, error) {
	var v RecurringCreditTaskView
	var dom, dow, validity, remaining sql.NullInt64
	var next sql.NullTime
	err := scanner.Scan(&v.ID, &v.Name, &v.AdminNotes, &v.ScheduleType, &dom, &dow, &validity, &v.LocalTime, &v.Timezone, &v.Amount, &v.ExecutionMode, &remaining, &v.SkipCount, &v.Status, &next, &v.Version, &v.LatestBatchStatus, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if dom.Valid {
		x := int(dom.Int64)
		v.DayOfMonth = &x
	}
	if dow.Valid {
		x := int(dow.Int64)
		v.DayOfWeek = &x
	}
	if validity.Valid {
		x := int(validity.Int64)
		v.ValidityDays = &x
	}
	if remaining.Valid {
		x := int(remaining.Int64)
		v.RemainingRuns = &x
	}
	if next.Valid {
		x := next.Time.UTC()
		v.NextRunAt = &x
		v.NextRunLocal = x.In(mustLocation(v.Timezone)).Format(time.RFC3339)
	}
	v.ScheduleDescription = scheduleDescription(&v)
	return &v, nil
}

const batchSelectColumns = `id,task_id,task_name,scheduled_at,expires_at,qualification_start,qualification_end,qualification_cutoff_at,config_version,eligibility_policy,validity_days,amount,timezone,status,attempt_count,eligible_user_count,issued_user_count,excluded_user_count,usage_eligible_count,recharge_eligible_count,api_active_count,site_active_count,both_active_count,snapshot_completed_at,issued_amount,failure_code,failure_message,finished_at,created_at`

func scanBatch(scanner sqlScanner) (*RecurringCreditBatchView, error) {
	var v RecurringCreditBatchView
	var cutoff, snapshotCompleted, finished sql.NullTime
	var validity sql.NullInt64
	err := scanner.Scan(&v.ID, &v.TaskID, &v.TaskName, &v.ScheduledAt, &v.ExpiresAt, &v.QualificationStart, &v.QualificationEnd, &cutoff, &v.ConfigVersion, &v.EligibilityPolicy, &validity, &v.Amount, &v.Timezone, &v.Status, &v.AttemptCount, &v.EligibleUserCount, &v.IssuedUserCount, &v.ExcludedUserCount, &v.UsageEligibleCount, &v.RechargeEligibleCount, &v.APIActiveCount, &v.SiteActiveCount, &v.BothActiveCount, &snapshotCompleted, &v.IssuedAmount, &v.FailureCode, &v.FailureMessage, &finished, &v.CreatedAt)
	if cutoff.Valid {
		x := cutoff.Time
		v.QualificationCutoffAt = &x
	}
	if snapshotCompleted.Valid {
		x := snapshotCompleted.Time
		v.SnapshotCompletedAt = &x
	}
	if finished.Valid {
		x := finished.Time
		v.FinishedAt = &x
	}
	if validity.Valid {
		x := int(validity.Int64)
		v.ValidityDays = &x
	}
	return &v, err
}

func insertRecurringAudit(ctx context.Context, tx *sql.Tx, id int64, actor RecurringCreditActor, email, action string, before, after *RecurringCreditTaskView) error {
	var beforeJSON, afterJSON any
	if before != nil {
		value, _ := json.Marshal(before)
		beforeJSON = string(value)
	}
	if after != nil {
		value, _ := json.Marshal(after)
		afterJSON = string(value)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO recurring_credit_task_audits(task_id,admin_id,admin_email,client_ip,action,before_snapshot,after_snapshot) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, actor.AdminID, email, actor.IP, action, beforeJSON, afterJSON)
	return err
}

func taskQueryError(err error) error {
	if err == sql.ErrNoRows {
		return infraerrors.New(http.StatusNotFound, "RECURRING_CREDIT_TASK_NOT_FOUND", "task or batch not found")
	}
	return err
}
func invalidTaskState(message string) error {
	return infraerrors.New(http.StatusConflict, "INVALID_RECURRING_CREDIT_TASK_STATE", message)
}
func taskViewInput(v *RecurringCreditTaskView) RecurringCreditTaskInput {
	return RecurringCreditTaskInput{Name: v.Name, AdminNotes: v.AdminNotes, ScheduleType: v.ScheduleType, DayOfMonth: v.DayOfMonth, DayOfWeek: v.DayOfWeek, ValidityDays: v.ValidityDays, LocalTime: v.LocalTime, Timezone: v.Timezone, Amount: v.Amount, ExecutionMode: v.ExecutionMode, RemainingRuns: v.RemainingRuns}
}
func scheduleDescription(v *RecurringCreditTaskView) string {
	if v.ScheduleType == RecurringCreditImmediate && v.ValidityDays != nil {
		return fmt.Sprintf("立即执行（有效期%d天）", *v.ValidityDays)
	}
	if v.ScheduleType == RecurringCreditMonthly && v.DayOfMonth != nil {
		return fmt.Sprintf("每月%d日 %s", *v.DayOfMonth, v.LocalTime)
	}
	if v.DayOfWeek != nil {
		return fmt.Sprintf("每周%d %s", *v.DayOfWeek, v.LocalTime)
	}
	return ""
}

func parseLocalTime(value string) (int, int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return 0, 0, fmt.Errorf("invalid local time")
	}
	return parsed.Hour(), parsed.Minute(), nil
}
func mustLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return location
}

// nextRecurringOccurrence 返回严格晚于 after 的下一个日历计划时点。
func nextRecurringOccurrence(input RecurringCreditTaskInput, after time.Time) (time.Time, error) {
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	hour, minute, err := parseLocalTime(input.LocalTime)
	if err != nil {
		return time.Time{}, err
	}
	localAfter := after.In(location)
	if input.ScheduleType == RecurringCreditMonthly {
		year, month, _ := localAfter.Date()
		for i := 0; i < 240; i++ {
			candidate := resolveLocalWallTime(location, year, month, *input.DayOfMonth, hour, minute)
			if candidate.After(after) {
				return candidate.UTC(), nil
			}
			month++
			if month > 12 {
				month = 1
				year++
			}
		}
	} else {
		base := time.Date(localAfter.Year(), localAfter.Month(), localAfter.Day(), 0, 0, 0, 0, location)
		offset := (*input.DayOfWeek - int(base.Weekday()+6)%7 - 1 + 7) % 7
		date := base.AddDate(0, 0, offset)
		for i := 0; i < 520; i++ {
			year, month, day := date.Date()
			candidate := resolveLocalWallTime(location, year, month, day, hour, minute)
			if candidate.After(after) {
				return candidate.UTC(), nil
			}
			date = date.AddDate(0, 0, 7)
		}
	}
	return time.Time{}, fmt.Errorf("unable to resolve recurring occurrence")
}

// resolveLocalWallTime 在 DST 缺失时刻选择跳变后的首个有效分钟，重复时刻选择第一次出现。
func resolveLocalWallTime(location *time.Location, year int, month time.Month, day, hour, minute int) time.Time {
	targetKey := year*100000000 + int(month)*1000000 + day*10000 + hour*100 + minute
	anchor := time.Date(year, month, day, hour, minute, 0, 0, location)
	start := anchor.Add(-18 * time.Hour)
	var fallback time.Time
	for i := 0; i <= 36*60; i++ {
		candidate := start.Add(time.Duration(i) * time.Minute)
		local := candidate.In(location)
		key := local.Year()*100000000 + int(local.Month())*1000000 + local.Day()*10000 + local.Hour()*100 + local.Minute()
		if key == targetKey {
			return candidate
		}
		if key > targetKey && local.Year() == year && local.Month() == month && local.Day() == day && (fallback.IsZero() || candidate.Before(fallback)) {
			fallback = candidate
		}
	}
	if !fallback.IsZero() {
		return fallback
	}
	return anchor
}

func qualificationWindow(scheduleType, timezone string, scheduled time.Time) (time.Time, time.Time, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	local := scheduled.In(location)
	if scheduleType == RecurringCreditMonthly {
		end := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, location)
		return end.AddDate(0, -1, 0).UTC(), end.UTC(), nil
	}
	weekday := int(local.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).AddDate(0, 0, -(weekday - 1))
	return end.AddDate(0, 0, -7).UTC(), end.UTC(), nil
}
