package service

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccountitem "github.com/Wei-Shaw/sub2api/ent/resetrebateaccountitem"
	dbbatch "github.com/Wei-Shaw/sub2api/ent/resetrebatebatch"
	dbuseritem "github.com/Wei-Shaw/sub2api/ent/resetrebateuseritem"
	dbgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const (
	ResetRebateStatusRunning     = "running"
	ResetRebateStatusReady       = "ready"
	ResetRebateStatusIncomplete  = "incomplete"
	ResetRebateStatusNotEligible = "not_eligible"
	ResetRebateStatusExpired     = "expired"
	ResetRebateStatusExecuted    = "executed"
	ResetRebateStatusFailed      = "failed"

	LimitedCreditSourceResetRebate = "reset_rebate"
	resetRebateSnapshotTTL         = 30 * time.Minute
	resetRebateValidity            = 168 * time.Hour
	resetRebateMaxPeriod           = 7 * 24 * time.Hour
	resetRebateUpstreamConcurrency = 5
	resetRebateReasonMaxLength     = 100
)

// ResetRebateBatchView 是管理端使用的批次摘要。
type ResetRebateBatchView struct {
	ID                  int64      `json:"id"`
	GroupID             int64      `json:"group_id"`
	GroupName           string     `json:"group_name"`
	AdminID             int64      `json:"admin_id"`
	AdminEmail          string     `json:"admin_email"`
	PeriodStart         time.Time  `json:"period_start"`
	PeriodEnd           time.Time  `json:"period_end"`
	Status              string     `json:"status"`
	ProgressTotal       int        `json:"progress_total"`
	ProgressCompleted   int        `json:"progress_completed"`
	ProgressSucceeded   int        `json:"progress_succeeded"`
	ProgressFailed      int        `json:"progress_failed"`
	ParticipantCount    int        `json:"participant_count"`
	ActualAmount        float64    `json:"actual_amount"`
	RefundableAmount    float64    `json:"refundable_amount"`
	FailedAccountAmount float64    `json:"failed_account_amount"`
	WeeklyUsagePercent  float64    `json:"weekly_usage_percent"`
	RefundablePercent   float64    `json:"refundable_percent"`
	SuggestedRatio      int        `json:"suggested_ratio"`
	ConfiguredRatio     *int       `json:"configured_ratio,omitempty"`
	IssuedUserCount     int        `json:"issued_user_count"`
	ExcludedUserCount   int        `json:"excluded_user_count"`
	IssuedAmount        float64    `json:"issued_amount"`
	FailureCode         string     `json:"failure_code,omitempty"`
	FailureMessage      string     `json:"failure_message,omitempty"`
	RebateReason        string     `json:"rebate_reason,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	SnapshotExpiresAt   *time.Time `json:"snapshot_expires_at,omitempty"`
	IssuedAt            *time.Time `json:"issued_at,omitempty"`
	ExecutedAt          *time.Time `json:"executed_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

// ResetRebateAccountView 是账号资格审计明细。
type ResetRebateAccountView struct {
	ID                  int64      `json:"id"`
	AccountID           int64      `json:"account_id"`
	AccountName         string     `json:"account_name"`
	Platform            string     `json:"platform"`
	AccountType         string     `json:"account_type"`
	IsShadow            bool       `json:"is_shadow"`
	InGroup             bool       `json:"in_group"`
	Schedulable         bool       `json:"schedulable"`
	ConsumedAmount      float64    `json:"consumed_amount"`
	AvailableCount      *int       `json:"available_count,omitempty"`
	WeeklyUsedPercent   *float64   `json:"weekly_used_percent,omitempty"`
	WeeklyWindowSeconds *int64     `json:"weekly_window_seconds,omitempty"`
	Included            bool       `json:"included"`
	ExclusionReason     string     `json:"exclusion_reason,omitempty"`
	ErrorCode           string     `json:"error_code,omitempty"`
	ErrorMessage        string     `json:"error_message,omitempty"`
	FetchedAt           *time.Time `json:"fetched_at,omitempty"`
}

// ResetRebateUserView 是按指定比例计算的逐用户预览或执行结果。
type ResetRebateUserView struct {
	ID                 int64      `json:"id"`
	UserID             int64      `json:"user_id"`
	Email              string     `json:"email"`
	Username           string     `json:"username"`
	UserStatus         string     `json:"user_status"`
	UserDeleted        bool       `json:"user_deleted"`
	ActualAmount       float64    `json:"actual_amount"`
	RebateRatio        int        `json:"rebate_ratio"`
	TheoreticalAmount  float64    `json:"theoretical_amount"`
	RebateAmount       float64    `json:"rebate_amount"`
	Issued             bool       `json:"issued"`
	ExclusionReason    string     `json:"exclusion_reason,omitempty"`
	GrantID            *int64     `json:"grant_id,omitempty"`
	CurrentGrantStatus string     `json:"current_grant_status,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
}

// ResetRebatePreview 汇总同一不可变快照的发放预览。
type ResetRebatePreview struct {
	Batch             ResetRebateBatchView  `json:"batch"`
	Ratio             int                   `json:"ratio"`
	ExpectedIssuedAt  time.Time             `json:"expected_issued_at"`
	ExpectedExpiresAt time.Time             `json:"expected_expires_at"`
	IssuedUserCount   int                   `json:"issued_user_count"`
	ExcludedUserCount int                   `json:"excluded_user_count"`
	TotalAmount       float64               `json:"total_amount"`
	Users             []ResetRebateUserView `json:"users"`
	Total             int                   `json:"total"`
	Page              int                   `json:"page"`
	PageSize          int                   `json:"page_size"`
}

// ResetRebateListFilter 定义历史批次筛选条件。
type ResetRebateListFilter struct {
	GroupID int64
	AdminID int64
	Status  string
	Start   *time.Time
	End     *time.Time
}

// ResetRebateService 管理统计快照、上游查询和原子发放。
type ResetRebateService struct {
	entClient            *dbent.Client
	db                   *sql.DB
	quota                *OpenAIQuotaService
	authCacheInvalidator APIKeyAuthCacheInvalidator
	billingCache         *BillingCacheService
	mu                   sync.Mutex
	running              map[int64]struct{}
}

// NewResetRebateService 创建返利服务并恢复数据库中未完成的统计任务。
func NewResetRebateService(entClient *dbent.Client, db *sql.DB, quota *OpenAIQuotaService, authCacheInvalidator APIKeyAuthCacheInvalidator, billingCache *BillingCacheService) *ResetRebateService {
	s := &ResetRebateService{entClient: entClient, db: db, quota: quota, authCacheInvalidator: authCacheInvalidator, billingCache: billingCache, running: make(map[int64]struct{})}
	go s.resumeRunningTasks()
	return s
}

// CreateStats 冻结统计范围数据并异步查询候选账号的上游配额。
func (s *ResetRebateService) CreateStats(ctx context.Context, adminID, groupID int64, start, end time.Time) (*ResetRebateBatchView, error) {
	now := time.Now().UTC()
	start, end = start.UTC(), end.UTC()
	if !start.Before(end) || end.After(now) || end.Sub(start) > resetRebateMaxPeriod {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_PERIOD", "period must be non-empty, end no later than server time, and no longer than 7 days")
	}
	if s == nil || s.entClient == nil || s.db == nil || s.quota == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "RESET_REBATE_NOT_CONFIGURED", "reset rebate service is not configured")
	}
	if existing, err := s.entClient.ResetRebateBatch.Query().Where(dbbatch.AdminIDEQ(adminID), dbbatch.GroupIDEQ(groupID), dbbatch.PeriodStartEQ(start), dbbatch.PeriodEndEQ(end), dbbatch.StatusEQ(ResetRebateStatusRunning)).Only(ctx); err == nil {
		view := resetRebateBatchView(existing)
		return &view, nil
	}
	group, err := s.entClient.Group.Get(ctx, groupID)
	if err != nil {
		return nil, infraerrors.New(http.StatusNotFound, "RESET_REBATE_GROUP_NOT_FOUND", "group not found")
	}
	admin, err := s.entClient.User.Get(ctx, adminID)
	if err != nil {
		return nil, infraerrors.New(http.StatusUnauthorized, "RESET_REBATE_ADMIN_NOT_FOUND", "administrator not found")
	}
	batch, err := s.entClient.ResetRebateBatch.Create().SetGroupID(groupID).SetGroupName(group.Name).SetAdminID(adminID).SetAdminEmail(admin.Email).SetPeriodStart(start).SetPeriodEnd(end).SetStatus(ResetRebateStatusRunning).Save(ctx)
	if err != nil {
		// 并发重复请求命中唯一索引后返回已存在任务。
		if existing, queryErr := s.entClient.ResetRebateBatch.Query().Where(dbbatch.AdminIDEQ(adminID), dbbatch.GroupIDEQ(groupID), dbbatch.PeriodStartEQ(start), dbbatch.PeriodEndEQ(end), dbbatch.StatusEQ(ResetRebateStatusRunning)).Only(ctx); queryErr == nil {
			view := resetRebateBatchView(existing)
			return &view, nil
		}
		return nil, err
	}
	if err = s.freezeSnapshot(ctx, batch); err != nil {
		_, _ = s.entClient.ResetRebateBatch.UpdateOneID(batch.ID).SetStatus(ResetRebateStatusFailed).SetFailureCode("SNAPSHOT_FAILED").SetFailureMessage("failed to freeze usage snapshot").Save(context.Background())
		return nil, err
	}
	s.startTask(batch.ID)
	fresh, err := s.entClient.ResetRebateBatch.Get(ctx, batch.ID)
	if err != nil {
		return nil, err
	}
	view := resetRebateBatchView(fresh)
	return &view, nil
}

type resetRebateAccountSnapshot struct {
	accountID                      int64
	name, platform, accountType    string
	isShadow, inGroup, schedulable bool
	consumed                       float64
}

// freezeSnapshot 一次性冻结账号资格、账号消费和逐用户真实消费。
func (s *ResetRebateService) freezeSnapshot(ctx context.Context, batch *dbent.ResetRebateBatch) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ul.account_id, COALESCE(a.name, ''), COALESCE(a.platform, ''), COALESCE(a.type, ''),
		       COALESCE(a.parent_account_id IS NOT NULL, FALSE),
		       COALESCE(a.deleted_at IS NULL AND EXISTS (SELECT 1 FROM account_groups ag WHERE ag.account_id=a.id AND ag.group_id=$1), FALSE),
		       COALESCE(a.deleted_at IS NULL AND a.schedulable, FALSE), COALESCE(SUM(ul.actual_cost), 0)
		FROM usage_logs ul LEFT JOIN accounts a ON a.id=ul.account_id
		WHERE ul.group_id=$1 AND ul.created_at >= $2 AND ul.created_at < $3
		GROUP BY ul.account_id, a.name, a.platform, a.type, a.parent_account_id, a.deleted_at, a.schedulable, a.id`, batch.GroupID, batch.PeriodStart, batch.PeriodEnd)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	accounts := make([]resetRebateAccountSnapshot, 0)
	actual := decimal.Zero
	candidates := 0
	for rows.Next() {
		var a resetRebateAccountSnapshot
		if err = rows.Scan(&a.accountID, &a.name, &a.platform, &a.accountType, &a.isShadow, &a.inGroup, &a.schedulable, &a.consumed); err != nil {
			return err
		}
		accounts = append(accounts, a)
		if !a.isShadow {
			actual = actual.Add(decimal.NewFromFloat(a.consumed))
		}
		if !a.isShadow && a.platform == PlatformOpenAI && a.accountType == AccountTypeOAuth && a.inGroup && a.schedulable {
			candidates++
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}

	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	for _, a := range accounts {
		reason := resetRebateInitialExclusion(a)
		_, err = tx.ResetRebateAccountItem.Create().SetBatchID(batch.ID).SetAccountID(a.accountID).SetAccountName(a.name).SetPlatform(a.platform).SetAccountType(a.accountType).SetIsShadow(a.isShadow).SetInGroup(a.inGroup).SetSchedulable(a.schedulable).SetConsumedAmount(a.consumed).SetExclusionReason(reason).Save(txCtx)
		if err != nil {
			return err
		}
	}
	userRows, err := s.db.QueryContext(ctx, `
		SELECT ul.user_id, COALESCE(u.email,''), COALESCE(u.username,''), COALESCE(u.status,''), (u.deleted_at IS NOT NULL), COALESCE(SUM(ul.actual_cost),0)
		FROM usage_logs ul JOIN accounts a ON a.id=ul.account_id LEFT JOIN users u ON u.id=ul.user_id
		WHERE ul.group_id=$1 AND ul.created_at >= $2 AND ul.created_at < $3 AND a.parent_account_id IS NULL
		GROUP BY ul.user_id, u.email, u.username, u.status, u.deleted_at
		HAVING SUM(ul.actual_cost) > 0`, batch.GroupID, batch.PeriodStart, batch.PeriodEnd)
	if err != nil {
		return err
	}
	defer func() { _ = userRows.Close() }()
	for userRows.Next() {
		var userID int64
		var email, username, status string
		var deleted bool
		var amount float64
		if err = userRows.Scan(&userID, &email, &username, &status, &deleted, &amount); err != nil {
			return err
		}
		reason := ""
		if deleted {
			reason = "用户已删除，未发放"
		}
		_, err = tx.ResetRebateUserItem.Create().SetBatchID(batch.ID).SetUserID(userID).SetEmail(email).SetUsername(username).SetUserStatus(status).SetUserDeleted(deleted).SetActualAmount(amount).SetExclusionReason(reason).Save(txCtx)
		if err != nil {
			return err
		}
	}
	if err = userRows.Err(); err != nil {
		return err
	}
	_, err = tx.ResetRebateBatch.UpdateOneID(batch.ID).SetActualAmount(actual.InexactFloat64()).SetProgressTotal(candidates).Save(txCtx)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func resetRebateInitialExclusion(a resetRebateAccountSnapshot) string {
	switch {
	case a.isShadow:
		return "影子账号已排除"
	case a.platform != PlatformOpenAI:
		return "非 OpenAI 账号"
	case a.accountType != AccountTypeOAuth:
		return "非 OAuth 账号"
	case !a.inGroup:
		return "当前不在该分组"
	case !a.schedulable:
		return "当前不可调度"
	default:
		return "等待上游统计"
	}
}

func (s *ResetRebateService) resumeRunningTasks() {
	if s == nil || s.entClient == nil {
		return
	}
	rows, err := s.entClient.ResetRebateBatch.Query().Where(dbbatch.StatusEQ(ResetRebateStatusRunning)).All(context.Background())
	if err != nil {
		return
	}
	for _, row := range rows {
		s.startTask(row.ID)
	}
}

func (s *ResetRebateService) startTask(batchID int64) {
	s.mu.Lock()
	if _, ok := s.running[batchID]; ok {
		s.mu.Unlock()
		return
	}
	s.running[batchID] = struct{}{}
	s.mu.Unlock()
	go func() {
		defer func() { s.mu.Lock(); delete(s.running, batchID); s.mu.Unlock() }()
		defer func() {
			if recover() != nil {
				s.failTask(batchID)
			}
		}()
		s.runQuotaTask(context.Background(), batchID)
	}()
}

// runQuotaTask 以最多五路并发查询上游，并逐账号持久化进度。
func (s *ResetRebateService) runQuotaTask(ctx context.Context, batchID int64) {
	items, err := s.entClient.ResetRebateAccountItem.Query().Where(dbaccountitem.BatchIDEQ(batchID), dbaccountitem.IsShadowEQ(false), dbaccountitem.PlatformEQ(PlatformOpenAI), dbaccountitem.AccountTypeEQ(AccountTypeOAuth), dbaccountitem.InGroupEQ(true), dbaccountitem.SchedulableEQ(true), dbaccountitem.FetchedAtIsNil(), dbaccountitem.ErrorCodeEQ("")).All(ctx)
	if err != nil {
		s.failTask(batchID)
		return
	}
	sem := make(chan struct{}, resetRebateUpstreamConcurrency)
	var wg sync.WaitGroup
	for _, item := range items {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.queryAccount(ctx, item)
		}()
	}
	wg.Wait()
	if err = s.finalizeStats(ctx, batchID); err != nil {
		s.failTask(batchID)
	}
}

func (s *ResetRebateService) queryAccount(ctx context.Context, item *dbent.ResetRebateAccountItem) {
	usage, err := s.quota.QueryUsage(ctx, item.AccountID)
	if err != nil {
		code := infraerrors.Reason(err)
		if code == "" {
			code = "OPENAI_QUOTA_QUERY_FAILED"
		}
		// 只持久化稳定错误码和简化说明，避免把上游响应正文写入审计。
		message := "上游配额查询失败，请稍后重试"
		if _, saveErr := s.entClient.ResetRebateAccountItem.UpdateOneID(item.ID).SetErrorCode(code).SetErrorMessage(message).SetExclusionReason("上游查询失败").Save(ctx); saveErr != nil {
			s.failTask(item.BatchID)
			return
		}
		if saveErr := s.incrementProgress(ctx, item.BatchID, false); saveErr != nil {
			s.failTask(item.BatchID)
		}
		return
	}
	available := 0
	if usage.RateLimitResetCredits != nil {
		available = usage.RateLimitResetCredits.AvailableCount
	}
	fetchedAt := time.Unix(usage.FetchedAt, 0).UTC()
	window := ordinarySevenDayWindow(usage.RateLimit)
	update := s.entClient.ResetRebateAccountItem.UpdateOneID(item.ID).SetAvailableCount(available).SetFetchedAt(fetchedAt)
	if available <= 0 {
		if _, saveErr := update.SetIncluded(false).SetExclusionReason("无可用重置次数").Save(ctx); saveErr != nil {
			s.failTask(item.BatchID)
			return
		}
		if saveErr := s.incrementProgress(ctx, item.BatchID, true); saveErr != nil {
			s.failTask(item.BatchID)
		}
		return
	}
	if window == nil {
		if _, saveErr := update.SetErrorCode("OPENAI_WEEKLY_WINDOW_MISSING").SetErrorMessage("缺少普通 7 天周限窗口").SetExclusionReason("周限数据缺失").Save(ctx); saveErr != nil {
			s.failTask(item.BatchID)
			return
		}
		if saveErr := s.incrementProgress(ctx, item.BatchID, false); saveErr != nil {
			s.failTask(item.BatchID)
		}
		return
	}
	if _, saveErr := update.SetIncluded(true).SetExclusionReason("").SetWeeklyUsedPercent(window.UsedPercent).SetWeeklyWindowSeconds(window.LimitWindowSeconds).Save(ctx); saveErr != nil {
		s.failTask(item.BatchID)
		return
	}
	if saveErr := s.incrementProgress(ctx, item.BatchID, true); saveErr != nil {
		s.failTask(item.BatchID)
	}
}

func ordinarySevenDayWindow(limit *OpenAIRateLimit) *OpenAIRateLimitWindow {
	if limit == nil {
		return nil
	}
	for _, window := range []*OpenAIRateLimitWindow{limit.PrimaryWindow, limit.SecondaryWindow} {
		if window != nil && window.LimitWindowSeconds == int64((7*24*time.Hour)/time.Second) {
			return window
		}
	}
	return nil
}

func (s *ResetRebateService) incrementProgress(ctx context.Context, batchID int64, success bool) error {
	update := s.entClient.ResetRebateBatch.UpdateOneID(batchID).AddProgressCompleted(1)
	if success {
		update = update.AddProgressSucceeded(1)
	} else {
		update = update.AddProgressFailed(1)
	}
	_, err := update.Save(ctx)
	return err
}

func (s *ResetRebateService) finalizeStats(ctx context.Context, batchID int64) error {
	batch, err := s.entClient.ResetRebateBatch.Get(ctx, batchID)
	if err != nil {
		return err
	}
	if batch.Status != ResetRebateStatusRunning {
		return nil
	}
	accountItems, err := s.entClient.ResetRebateAccountItem.Query().Where(dbaccountitem.BatchIDEQ(batchID)).All(ctx)
	if err != nil {
		return err
	}
	refundable, failedAmount, weekly := decimal.Zero, decimal.Zero, decimal.Zero
	participantCount := 0
	for _, item := range accountItems {
		if item.ErrorCode != "" {
			failedAmount = failedAmount.Add(decimal.NewFromFloat(item.ConsumedAmount))
		}
		if item.Included {
			participantCount++
			refundable = refundable.Add(decimal.NewFromFloat(item.ConsumedAmount))
			if item.WeeklyUsedPercent != nil {
				weekly = weekly.Add(decimal.NewFromFloat(*item.WeeklyUsedPercent))
			}
		}
	}
	weeklyPct := 0.0
	if participantCount > 0 {
		weeklyPct = weekly.Div(decimal.NewFromInt(int64(participantCount))).InexactFloat64()
	}
	coverage := 0.0
	if batch.ActualAmount > 0 {
		coverage = refundable.Div(decimal.NewFromFloat(batch.ActualAmount)).Mul(decimal.NewFromInt(100)).InexactFloat64()
	}
	suggested := calculateResetRebateSuggestedRatio(weeklyPct, coverage)
	now := time.Now().UTC()
	expires := now.Add(resetRebateSnapshotTTL)
	status, code, message := resetRebateFinalStatus(batch.ActualAmount, batch.ProgressFailed, participantCount)
	_, err = s.entClient.ResetRebateBatch.UpdateOneID(batchID).SetStatus(status).SetParticipantCount(participantCount).SetRefundableAmount(refundable.InexactFloat64()).SetFailedAccountAmount(failedAmount.InexactFloat64()).SetWeeklyUsagePercent(weeklyPct).SetRefundablePercent(coverage).SetSuggestedRatio(suggested).SetFailureCode(code).SetFailureMessage(message).SetCompletedAt(now).SetSnapshotExpiresAt(expires).Save(ctx)
	return err
}

// resetRebateFinalStatus 按实际消费与统计完整性确定批次是否可强制发放。
func resetRebateFinalStatus(actualAmount float64, failedCount, participantCount int) (string, string, string) {
	if actualAmount <= 0 {
		return ResetRebateStatusNotEligible, "NO_ACTUAL_CONSUMPTION", "统计周期内没有可返利口径的实际消费"
	}
	if failedCount > 0 {
		return ResetRebateStatusIncomplete, "UPSTREAM_STATS_INCOMPLETE", "部分账号上游统计失败或缺少普通 7 天窗口，可由管理员强制发放"
	}
	if participantCount == 0 {
		return ResetRebateStatusReady, "NO_PARTICIPATING_ACCOUNTS", "没有当前可参与重置返利的账号，可由管理员强制发放"
	}
	return ResetRebateStatusReady, "", ""
}

func (s *ResetRebateService) failTask(batchID int64) {
	_, _ = s.entClient.ResetRebateBatch.UpdateOneID(batchID).SetStatus(ResetRebateStatusFailed).SetFailureCode("RESET_REBATE_STATS_FAILED").SetFailureMessage("统计任务发生内部错误").SetCompletedAt(time.Now().UTC()).Save(context.Background())
}

// GetBatch 返回批次并在读取时把超时可执行快照转换为 expired。
func (s *ResetRebateService) GetBatch(ctx context.Context, id int64) (*ResetRebateBatchView, error) {
	row, err := s.entClient.ResetRebateBatch.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	row, err = s.expireIfNeeded(ctx, row)
	if err != nil {
		return nil, err
	}
	view := resetRebateBatchView(row)
	return &view, nil
}

func (s *ResetRebateService) expireIfNeeded(ctx context.Context, row *dbent.ResetRebateBatch) (*dbent.ResetRebateBatch, error) {
	if resetRebateExecutableStatus(row.Status) && row.SnapshotExpiresAt != nil && !row.SnapshotExpiresAt.After(time.Now().UTC()) {
		return s.entClient.ResetRebateBatch.UpdateOneID(row.ID).SetStatus(ResetRebateStatusExpired).Save(ctx)
	}
	return row, nil
}

// ListBatches 分页查询返利历史。
func (s *ResetRebateService) ListBatches(ctx context.Context, page, pageSize int, filter ResetRebateListFilter) ([]ResetRebateBatchView, int, error) {
	page, pageSize = normalizeResetRebatePage(page, pageSize, 20)
	q := s.entClient.ResetRebateBatch.Query()
	if filter.GroupID > 0 {
		q = q.Where(dbbatch.GroupIDEQ(filter.GroupID))
	}
	if filter.AdminID > 0 {
		q = q.Where(dbbatch.AdminIDEQ(filter.AdminID))
	}
	if filter.Status != "" {
		q = q.Where(dbbatch.StatusEQ(filter.Status))
	}
	if filter.Start != nil {
		q = q.Where(dbbatch.PeriodEndGT(filter.Start.UTC()))
	}
	if filter.End != nil {
		q = q.Where(dbbatch.PeriodStartLT(filter.End.UTC()))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(dbent.Desc(dbbatch.FieldCreatedAt)).Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	result := make([]ResetRebateBatchView, 0, len(rows))
	for _, row := range rows {
		fresh, expireErr := s.expireIfNeeded(ctx, row)
		if expireErr != nil {
			fresh = row
		}
		result = append(result, resetRebateBatchView(fresh))
	}
	return result, total, nil
}

// LatestExecutedPeriodEnd 返回指定分组最近成功发放批次的统计截止时间。
func (s *ResetRebateService) LatestExecutedPeriodEnd(ctx context.Context, groupID int64) (*time.Time, error) {
	row, err := s.entClient.ResetRebateBatch.Query().Where(
		dbbatch.GroupIDEQ(groupID),
		dbbatch.StatusEQ(ResetRebateStatusExecuted),
	).Order(dbent.Desc(dbbatch.FieldPeriodEnd), dbent.Desc(dbbatch.FieldID)).First(ctx)
	if dbent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	periodEnd := row.PeriodEnd.UTC()
	return &periodEnd, nil
}

// ListAccounts 分页返回完整账号审计。
func (s *ResetRebateService) ListAccounts(ctx context.Context, batchID int64, page, pageSize int) ([]ResetRebateAccountView, int, error) {
	page, pageSize = normalizeResetRebatePage(page, pageSize, 50)
	q := s.entClient.ResetRebateAccountItem.Query().Where(dbaccountitem.BatchIDEQ(batchID))
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(dbent.Desc(dbaccountitem.FieldConsumedAmount), dbent.Asc(dbaccountitem.FieldAccountID)).Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	result := make([]ResetRebateAccountView, 0, len(rows))
	for _, row := range rows {
		result = append(result, resetRebateAccountView(row))
	}
	return result, total, nil
}

// Preview 基于同一快照、整数比例和返利原因计算逐用户预览。
func (s *ResetRebateService) Preview(ctx context.Context, batchID int64, ratio, page, pageSize int, search, reason string) (*ResetRebatePreview, error) {
	if ratio < 1 || ratio > 80 {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_RATIO", "ratio must be an integer between 1 and 80")
	}
	normalizedReason, err := normalizeResetRebateReason(reason)
	if err != nil {
		return nil, err
	}
	batch, err := s.entClient.ResetRebateBatch.Get(ctx, batchID)
	if err != nil {
		return nil, err
	}
	batch, err = s.expireIfNeeded(ctx, batch)
	if err != nil {
		return nil, err
	}
	if !resetRebateExecutableStatus(batch.Status) && batch.Status != ResetRebateStatusExecuted {
		return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_NOT_PREVIEWABLE", "batch is not ready for preview")
	}
	if batch.Status == ResetRebateStatusExecuted && batch.ConfiguredRatio != nil {
		ratio = *batch.ConfiguredRatio
	} else if resetRebateExecutableStatus(batch.Status) {
		// 记录最近一次预览的比例和原因，执行时校验以避免绕过逐用户核对。
		batch, err = s.entClient.ResetRebateBatch.UpdateOne(batch).SetConfiguredRatio(ratio).SetRebateReason(normalizedReason).Save(ctx)
		if err != nil {
			return nil, err
		}
	}
	page, pageSize = normalizeResetRebatePage(page, pageSize, 50)
	summaryRows, err := s.entClient.ResetRebateUserItem.Query().Where(dbuseritem.BatchIDEQ(batchID)).All(ctx)
	if err != nil {
		return nil, err
	}
	issuedCount, excludedCount, totalAmount := 0, 0, decimal.Zero
	for _, row := range summaryRows {
		amount := truncateResetRebate(row.ActualAmount, ratio)
		if row.UserDeleted || amount.IsZero() {
			excludedCount++
		} else {
			issuedCount++
			totalAmount = totalAmount.Add(amount)
		}
	}
	q := s.userItemQuery(batchID, search)
	totalRows, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := q.Order(func(selector *entsql.Selector) {
		selector.OrderExpr(entsql.Expr("CASE WHEN user_deleted THEN 0 ELSE actual_amount END DESC"))
		selector.OrderBy(entsql.Asc(selector.C(dbuseritem.FieldUserID)))
	}).Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	expectedExpiry := now.Add(resetRebateValidity)
	users := make([]ResetRebateUserView, 0, len(rows))
	for _, row := range rows {
		users = append(users, resetRebateUserView(row, ratio, expectedExpiry))
	}
	if batch.Status == ResetRebateStatusExecuted {
		s.enrichCurrentGrantStatuses(ctx, users)
	}
	return &ResetRebatePreview{Batch: resetRebateBatchView(batch), Ratio: ratio, ExpectedIssuedAt: now, ExpectedExpiresAt: expectedExpiry, IssuedUserCount: issuedCount, ExcludedUserCount: excludedCount, TotalAmount: totalAmount.InexactFloat64(), Users: users, Total: totalRows, Page: page, PageSize: pageSize}, nil
}

func (s *ResetRebateService) userItemQuery(batchID int64, search string) *dbent.ResetRebateUserItemQuery {
	q := s.entClient.ResetRebateUserItem.Query().Where(dbuseritem.BatchIDEQ(batchID))
	search = strings.TrimSpace(search)
	if search == "" {
		return q
	}
	if id, err := strconv.ParseInt(search, 10, 64); err == nil && id > 0 {
		return q.Where(dbuseritem.Or(dbuseritem.UserIDEQ(id), dbuseritem.EmailContainsFold(search), dbuseritem.UsernameContainsFold(search)))
	}
	return q.Where(dbuseritem.Or(dbuseritem.EmailContainsFold(search), dbuseritem.UsernameContainsFold(search)))
}

// Execute 原子创建全部限时额度、流水和发放审计，并保证重复确认幂等。
func (s *ResetRebateService) Execute(ctx context.Context, batchID int64, ratio int, reason string) (*ResetRebateBatchView, error) {
	if ratio < 1 || ratio > 80 {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_RATIO", "ratio must be an integer between 1 and 80")
	}
	normalizedReason, err := normalizeResetRebateReason(reason)
	if err != nil {
		return nil, err
	}
	_, _ = s.entClient.ResetRebateBatch.UpdateOneID(batchID).AddExecutionAttempts(1).Save(ctx)
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	batch, err := tx.ResetRebateBatch.Query().Where(dbbatch.IDEQ(batchID)).ForUpdate().Only(txCtx)
	if err != nil {
		return nil, err
	}
	if batch.Status == ResetRebateStatusExecuted {
		view := resetRebateBatchView(batch)
		return &view, nil
	}
	if !resetRebateExecutableStatus(batch.Status) {
		return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_NOT_EXECUTABLE", "batch is not ready")
	}
	if batch.ConfiguredRatio == nil || *batch.ConfiguredRatio != ratio {
		return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_PREVIEW_REQUIRED", "preview the same ratio before execution")
	}
	if batch.RebateReason != normalizedReason {
		return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_REASON_MISMATCH", "preview the same rebate reason before execution")
	}
	if batch.SnapshotExpiresAt == nil || !batch.SnapshotExpiresAt.After(time.Now().UTC()) {
		_, _ = tx.ResetRebateBatch.UpdateOne(batch).SetStatus(ResetRebateStatusExpired).Save(txCtx)
		_ = tx.Commit()
		return nil, infraerrors.New(http.StatusConflict, "RESET_REBATE_SNAPSHOT_EXPIRED", "snapshot has expired")
	}
	// 同一分组执行使用事务级 advisory lock 串行化，避免两个不同快照并发越过重叠检查。
	if _, err = tx.ExecContext(txCtx, "SELECT pg_advisory_xact_lock($1)", batch.GroupID); err != nil {
		return nil, err
	}
	conflict, err := tx.ResetRebateBatch.Query().Where(dbbatch.IDNEQ(batchID), dbbatch.GroupIDEQ(batch.GroupID), dbbatch.StatusEQ(ResetRebateStatusExecuted), dbbatch.PeriodStartLT(batch.PeriodEnd), dbbatch.PeriodEndGT(batch.PeriodStart)).First(txCtx)
	if err == nil {
		return nil, infraerrors.Newf(http.StatusConflict, "RESET_REBATE_PERIOD_OVERLAP", "period overlaps executed batch %d", conflict.ID)
	}
	if err != nil && !dbent.IsNotFound(err) {
		return nil, err
	}
	items, err := tx.ResetRebateUserItem.Query().Where(dbuseritem.BatchIDEQ(batchID)).Order(dbent.Asc(dbuseritem.FieldUserID)).All(txCtx)
	if err != nil {
		return nil, err
	}
	issuedAt := time.Now().UTC()
	expiresAt := issuedAt.Add(resetRebateValidity)
	issuedCount, excludedCount, total := 0, 0, decimal.Zero
	issuedUsers := make([]int64, 0, len(items))
	for _, item := range items {
		theoretical := truncateResetRebate(item.ActualAmount, ratio)
		update := tx.ResetRebateUserItem.UpdateOne(item).SetRebateRatio(ratio).SetRebateAmount(0).SetIssued(false)
		if item.UserDeleted {
			excludedCount++
			if _, err = update.SetExclusionReason("用户已删除，未发放").Save(txCtx); err != nil {
				return nil, err
			}
			continue
		}
		if theoretical.IsZero() {
			excludedCount++
			if _, err = update.SetExclusionReason("金额过小，未发放").Save(txCtx); err != nil {
				return nil, err
			}
			continue
		}
		amount := theoretical.InexactFloat64()
		grant, createErr := tx.UserLimitedCreditGrant.Create().SetUserID(item.UserID).SetSourceType(LimitedCreditSourceResetRebate).SetSourceID(batchID).SetInitialAmount(amount).SetExpiresAt(expiresAt).SetStatus(LimitedCreditStatusActive).SetCreatedAt(issuedAt).SetUpdatedAt(issuedAt).Save(txCtx)
		if createErr != nil {
			return nil, createErr
		}
		if _, createErr = tx.UserLimitedCreditLedger.Create().SetUserID(item.UserID).SetGrantID(grant.ID).SetEventType("grant").SetAmount(amount).SetBatchID(strconv.FormatInt(batchID, 10)).SetNotes("重置返利").SetCreatedAt(issuedAt).Save(txCtx); createErr != nil {
			return nil, createErr
		}
		if _, err = update.SetRebateAmount(amount).SetIssued(true).SetExclusionReason("").SetGrantID(grant.ID).SetExpiresAt(expiresAt).Save(txCtx); err != nil {
			return nil, err
		}
		issuedCount++
		total = total.Add(theoretical)
		issuedUsers = append(issuedUsers, item.UserID)
	}
	updated, err := tx.ResetRebateBatch.UpdateOne(batch).SetStatus(ResetRebateStatusExecuted).SetConfiguredRatio(ratio).SetRebateReason(normalizedReason).SetIssuedUserCount(issuedCount).SetExcludedUserCount(excludedCount).SetIssuedAmount(total.InexactFloat64()).SetIssuedAt(issuedAt).SetExecutedAt(issuedAt).Save(txCtx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	for _, userID := range issuedUsers {
		if s.authCacheInvalidator != nil {
			s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
		}
		if s.billingCache != nil {
			_ = s.billingCache.InvalidateUserBalance(ctx, userID)
		}
	}
	view := resetRebateBatchView(updated)
	return &view, nil
}

// enrichCurrentGrantStatuses 为成功批次展示单份额度经过后续调整后的当前状态。
func (s *ResetRebateService) enrichCurrentGrantStatuses(ctx context.Context, users []ResetRebateUserView) {
	ids := make([]int64, 0, len(users))
	for _, user := range users {
		if user.GrantID != nil {
			ids = append(ids, *user.GrantID)
		}
	}
	if len(ids) == 0 {
		return
	}
	rows, err := s.entClient.UserLimitedCreditGrant.Query().Where(dbgrant.IDIn(ids...)).All(ctx)
	if err != nil {
		return
	}
	statuses := make(map[int64]string, len(rows))
	now := time.Now().UTC()
	for _, row := range rows {
		status := row.Status
		if status == LimitedCreditStatusActive && !row.ExpiresAt.After(now) {
			status = LimitedCreditStatusExpired
		}
		statuses[row.ID] = status
	}
	for i := range users {
		if users[i].GrantID != nil {
			users[i].CurrentGrantStatus = statuses[*users[i].GrantID]
		}
	}
}

// DeleteBatch 仅清理未执行且不在运行中的快照。
func (s *ResetRebateService) DeleteBatch(ctx context.Context, batchID int64) error {
	batch, err := s.entClient.ResetRebateBatch.Get(ctx, batchID)
	if err != nil {
		return err
	}
	if batch.Status == ResetRebateStatusExecuted || batch.Status == ResetRebateStatusRunning {
		return infraerrors.New(http.StatusConflict, "RESET_REBATE_NOT_CLEANABLE", "running or executed batch cannot be cleaned")
	}
	return s.entClient.ResetRebateBatch.DeleteOne(batch).Exec(ctx)
}

// ExportUsersCSV 导出快照全部用户明细且不延长有效期。
func (s *ResetRebateService) ExportUsersCSV(ctx context.Context, batchID int64, ratio int, output io.Writer) error {
	if ratio < 1 || ratio > 80 {
		return infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_RATIO", "ratio must be an integer between 1 and 80")
	}
	batch, err := s.entClient.ResetRebateBatch.Get(ctx, batchID)
	if err != nil {
		return err
	}
	if batch.Status == ResetRebateStatusExecuted && batch.ConfiguredRatio != nil {
		ratio = *batch.ConfiguredRatio
	}
	rows, err := s.entClient.ResetRebateUserItem.Query().Where(dbuseritem.BatchIDEQ(batchID)).Order(dbent.Desc(dbuseritem.FieldActualAmount)).All(ctx)
	if err != nil {
		return err
	}
	views := make([]ResetRebateUserView, 0, len(rows))
	for _, row := range rows {
		views = append(views, resetRebateUserView(row, ratio, time.Now().UTC().Add(resetRebateValidity)))
	}
	if batch.Status == ResetRebateStatusExecuted {
		s.enrichCurrentGrantStatuses(ctx, views)
	}
	w := csv.NewWriter(output)
	defer w.Flush()
	if err = w.Write([]string{"用户ID", "邮箱", "用户名", "状态", "真实消耗", "返还比例", "理论返还", "发放金额", "是否发放", "排除原因", "限时额度ID", "额度当前状态", "到期时间"}); err != nil {
		return err
	}
	for _, view := range views {
		grantID, expiry := "", ""
		if view.GrantID != nil {
			grantID = strconv.FormatInt(*view.GrantID, 10)
		}
		if view.ExpiresAt != nil {
			expiry = view.ExpiresAt.Format(time.RFC3339)
		}
		if err = w.Write([]string{strconv.FormatInt(view.UserID, 10), view.Email, view.Username, view.UserStatus, fmt.Sprintf("%.8f", view.ActualAmount), strconv.Itoa(ratio), fmt.Sprintf("%.8f", view.TheoreticalAmount), fmt.Sprintf("%.8f", view.RebateAmount), strconv.FormatBool(view.Issued), view.ExclusionReason, grantID, view.CurrentGrantStatus, expiry}); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func truncateResetRebate(actual float64, ratio int) decimal.Decimal {
	if actual <= 0 || ratio <= 0 {
		return decimal.Zero
	}
	return decimal.NewFromFloat(actual).Mul(decimal.NewFromInt(int64(ratio))).Div(decimal.NewFromInt(100)).Truncate(8)
}

func calculateResetRebateSuggestedRatio(weeklyPercent, refundablePercent float64) int {
	return int(math.Floor((weeklyPercent * refundablePercent) / 100))
}

// normalizeResetRebateReason 去除返利原因首尾空白并限制用户可见文本长度。
func normalizeResetRebateReason(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > resetRebateReasonMaxLength {
		return "", infraerrors.New(http.StatusBadRequest, "INVALID_RESET_REBATE_REASON", "rebate reason must be at most 100 characters")
	}
	return value, nil
}

func resetRebateExecutableStatus(status string) bool {
	return status == ResetRebateStatusReady || status == ResetRebateStatusIncomplete
}

func resetRebateBatchView(row *dbent.ResetRebateBatch) ResetRebateBatchView {
	return ResetRebateBatchView{ID: row.ID, GroupID: row.GroupID, GroupName: row.GroupName, AdminID: row.AdminID, AdminEmail: row.AdminEmail, PeriodStart: row.PeriodStart, PeriodEnd: row.PeriodEnd, Status: row.Status, ProgressTotal: row.ProgressTotal, ProgressCompleted: row.ProgressCompleted, ProgressSucceeded: row.ProgressSucceeded, ProgressFailed: row.ProgressFailed, ParticipantCount: row.ParticipantCount, ActualAmount: row.ActualAmount, RefundableAmount: row.RefundableAmount, FailedAccountAmount: row.FailedAccountAmount, WeeklyUsagePercent: row.WeeklyUsagePercent, RefundablePercent: row.RefundablePercent, SuggestedRatio: row.SuggestedRatio, ConfiguredRatio: row.ConfiguredRatio, IssuedUserCount: row.IssuedUserCount, ExcludedUserCount: row.ExcludedUserCount, IssuedAmount: row.IssuedAmount, FailureCode: row.FailureCode, FailureMessage: row.FailureMessage, RebateReason: row.RebateReason, CompletedAt: row.CompletedAt, SnapshotExpiresAt: row.SnapshotExpiresAt, IssuedAt: row.IssuedAt, ExecutedAt: row.ExecutedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

func resetRebateAccountView(row *dbent.ResetRebateAccountItem) ResetRebateAccountView {
	return ResetRebateAccountView{ID: row.ID, AccountID: row.AccountID, AccountName: row.AccountName, Platform: row.Platform, AccountType: row.AccountType, IsShadow: row.IsShadow, InGroup: row.InGroup, Schedulable: row.Schedulable, ConsumedAmount: row.ConsumedAmount, AvailableCount: row.AvailableCount, WeeklyUsedPercent: row.WeeklyUsedPercent, WeeklyWindowSeconds: row.WeeklyWindowSeconds, Included: row.Included, ExclusionReason: row.ExclusionReason, ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage, FetchedAt: row.FetchedAt}
}

func resetRebateUserView(row *dbent.ResetRebateUserItem, ratio int, expectedExpiry time.Time) ResetRebateUserView {
	theoretical := truncateResetRebate(row.ActualAmount, ratio).InexactFloat64()
	amount, reason, issued, expiry := theoretical, row.ExclusionReason, false, &expectedExpiry
	if row.UserDeleted {
		amount, reason, expiry = 0, "用户已删除，未发放", nil
	} else if theoretical < 0.00000001 {
		amount, reason, expiry = 0, "金额过小，未发放", nil
	}
	if row.RebateRatio != nil {
		ratio = *row.RebateRatio
		theoretical = truncateResetRebate(row.ActualAmount, ratio).InexactFloat64()
		amount, issued, expiry = row.RebateAmount, row.Issued, row.ExpiresAt
	}
	return ResetRebateUserView{ID: row.ID, UserID: row.UserID, Email: row.Email, Username: row.Username, UserStatus: row.UserStatus, UserDeleted: row.UserDeleted, ActualAmount: row.ActualAmount, RebateRatio: ratio, TheoreticalAmount: theoretical, RebateAmount: amount, Issued: issued, ExclusionReason: reason, GrantID: row.GrantID, ExpiresAt: expiry}
}

func normalizeResetRebatePage(page, pageSize, defaultSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultSize
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}
