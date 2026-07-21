package service

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbredeem "github.com/Wei-Shaw/sub2api/ent/redeemcode"
	dbgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	dbledger "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditledger"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	LimitedCreditSourceAdminManual = "admin_manual"
	LimitedCreditStatusRevoked     = "revoked"
)

// AdminCreditUser 是额度管理用户列表项。
type AdminCreditUser struct {
	ID                     int64     `json:"id"`
	Email                  string    `json:"email"`
	Username               string    `json:"username"`
	Status                 string    `json:"status"`
	Balance                float64   `json:"balance"`
	FrozenBalance          float64   `json:"frozen_balance"`
	LimitedRemainingAmount float64   `json:"limited_remaining_amount"`
	LimitedActiveCount     int       `json:"limited_active_count"`
	UpdatedAt              time.Time `json:"updated_at"`
}

// AdminCreditUserDetail 返回用户余额和最近失效的限时额度。
type AdminCreditUserDetail struct {
	AdminCreditUser
	LimitedCredits []LimitedCreditGrant `json:"limited_credits"`
}

type AdminBalanceAdjustmentInput struct {
	Operation         string
	Amount            float64
	Notes             string
	ExpectedUpdatedAt time.Time
}

type AdminLimitedCreditCreateInput struct {
	Amount       float64
	ValidityDays int
	Notes        string
}

type AdminLimitedCreditAdjustmentInput struct {
	AmountTarget      string
	AmountOperation   string
	Amount            float64
	ExpiryOperation   string
	ValidityDays      int
	Notes             string
	ExpectedUpdatedAt time.Time
}

// LimitedCreditLedgerEntry 是管理员侧限时额度流水。
type LimitedCreditLedgerEntry struct {
	ID        int64     `json:"id"`
	EventType string    `json:"event_type"`
	Amount    float64   `json:"amount"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// AdminCreditService 定义管理员额度管理能力，避免扩大通用 AdminService 的测试桩负担。
type AdminCreditService interface {
	ListCreditUsers(context.Context, int, int, string) ([]AdminCreditUser, int64, error)
	GetCreditUserDetail(context.Context, int64) (*AdminCreditUserDetail, error)
	AdjustCreditBalance(context.Context, int64, AdminBalanceAdjustmentInput) (*AdminCreditUserDetail, error)
	CreateAdminLimitedCredit(context.Context, int64, AdminLimitedCreditCreateInput) (*LimitedCreditGrant, error)
	AdjustAdminLimitedCredit(context.Context, int64, int64, AdminLimitedCreditAdjustmentInput) (*LimitedCreditGrant, error)
	RevokeAdminLimitedCredit(context.Context, int64, int64, time.Time, string) (*LimitedCreditGrant, error)
	ResetAdminLimitedCredit(context.Context, int64, int64, time.Time, string) (*LimitedCreditGrant, error)
	ListLimitedCreditLedger(context.Context, int64, int64) ([]LimitedCreditLedgerEntry, error)
}

func validateCreditAmount(amount float64) error {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 || amount >= 1e12 {
		return infraerrors.New(http.StatusBadRequest, "INVALID_CREDIT_AMOUNT", "amount must be a finite positive number within decimal(20,8) range")
	}
	return nil
}

func adminLedgerAmount(amount float64) float64 { return math.Abs(amount) }

func optionalCreditNotes(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func creditNotesValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func adminLimitedCreditFromEntity(row *dbent.UserLimitedCreditGrant) *LimitedCreditGrant {
	if row == nil {
		return nil
	}
	return &LimitedCreditGrant{ID: row.ID, UserID: row.UserID, SourceType: row.SourceType, SourceID: row.SourceID, InitialAmount: row.InitialAmount, UsedAmount: row.UsedAmount, FrozenAmount: row.FrozenAmount, ExpiresAt: row.ExpiresAt, Status: row.Status, Notes: creditNotesValue(row.Notes), CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
}

// ListCreditUsers 分页返回用户及有效限时额度汇总。
func (s *adminServiceImpl) ListCreditUsers(ctx context.Context, page, pageSize int, search string) ([]AdminCreditUser, int64, error) {
	search = strings.TrimSpace(search)
	if id, parseErr := strconv.ParseInt(search, 10, 64); parseErr == nil && id > 0 {
		user, err := s.GetUser(ctx, id)
		if err != nil {
			return []AdminCreditUser{}, 0, nil
		}
		detail, err := s.GetCreditUserDetail(ctx, user.ID)
		if err != nil {
			return nil, 0, err
		}
		return []AdminCreditUser{detail.AdminCreditUser}, 1, nil
	}
	users, total, err := s.ListUsers(ctx, page, pageSize, UserListFilters{Search: search}, "id", "desc")
	if err != nil {
		return nil, 0, err
	}
	result := make([]AdminCreditUser, 0, len(users))
	for _, user := range users {
		item := AdminCreditUser{ID: user.ID, Email: user.Email, Username: user.Username, Status: user.Status, Balance: user.Balance, FrozenBalance: user.FrozenBalance, UpdatedAt: user.UpdatedAt}
		grants, queryErr := s.entClient.UserLimitedCreditGrant.Query().Where(dbgrant.UserIDEQ(user.ID), dbgrant.StatusEQ(LimitedCreditStatusActive), dbgrant.ExpiresAtGT(time.Now().UTC())).All(ctx)
		if queryErr != nil {
			return nil, 0, queryErr
		}
		for _, grant := range grants {
			remaining := math.Max(grant.InitialAmount-grant.UsedAmount, 0)
			if remaining > 0 || grant.FrozenAmount > 0 {
				item.LimitedActiveCount++
				item.LimitedRemainingAmount += remaining
			}
		}
		result = append(result, item)
	}
	return result, total, nil
}

// GetCreditUserDetail 返回有效及失效后 30 天内的额度。
func (s *adminServiceImpl) GetCreditUserDetail(ctx context.Context, userID int64) (*AdminCreditUserDetail, error) {
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	rows, err := s.entClient.UserLimitedCreditGrant.Query().Where(dbgrant.UserIDEQ(userID)).Order(dbent.Asc(dbgrant.FieldExpiresAt), dbent.Asc(dbgrant.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	detail := &AdminCreditUserDetail{AdminCreditUser: AdminCreditUser{ID: user.ID, Email: user.Email, Username: user.Username, Status: user.Status, Balance: user.Balance, FrozenBalance: user.FrozenBalance, UpdatedAt: user.UpdatedAt}}
	for _, row := range rows {
		effectiveStatus := row.Status
		invalidAt := row.UpdatedAt
		if effectiveStatus == LimitedCreditStatusActive && !row.ExpiresAt.After(now) {
			effectiveStatus = LimitedCreditStatusExpired
			invalidAt = row.ExpiresAt
		}
		if effectiveStatus != LimitedCreditStatusActive && invalidAt.Before(now.AddDate(0, 0, -30)) {
			continue
		}
		grant := adminLimitedCreditFromEntity(row)
		grant.ValidityDays = s.resolveLimitedCreditValidityDays(ctx, grant)
		grant.Status = effectiveStatus
		detail.LimitedCredits = append(detail.LimitedCredits, *grant)
		if effectiveStatus == LimitedCreditStatusActive && (grant.RemainingAmount() > 0 || grant.FrozenAmount > 0) {
			detail.LimitedActiveCount++
			detail.LimitedRemainingAmount += grant.RemainingAmount()
		}
	}
	return detail, nil
}

// resolveLimitedCreditValidityDays 仅返回来源数据中有明确记录的有效期天数。
func (s *adminServiceImpl) resolveLimitedCreditValidityDays(ctx context.Context, grant *LimitedCreditGrant) *int {
	return resolveLimitedCreditValidityDaysWithClient(ctx, s.entClient, grant)
}

func resolveLimitedCreditValidityDaysWithClient(ctx context.Context, client *dbent.Client, grant *LimitedCreditGrant) *int {
	if grant == nil {
		return nil
	}
	if grant.SourceType == LimitedCreditSourceRechargeBonus {
		days := RechargeBonusValidityDays
		return &days
	}
	if grant.SourceType != LimitedCreditSourceRedeemCode || grant.SourceID == nil {
		return nil
	}
	code, err := client.RedeemCode.Query().Where(dbredeem.IDEQ(*grant.SourceID)).Only(ctx)
	if err != nil || code.ValidityDays <= 0 {
		return nil
	}
	days := code.ValidityDays
	return &days
}

// AdjustCreditBalance 使用乐观锁调整永久余额。
func (s *adminServiceImpl) AdjustCreditBalance(ctx context.Context, userID int64, input AdminBalanceAdjustmentInput) (*AdminCreditUserDetail, error) {
	if err := validateCreditAmount(input.Amount); err != nil {
		return nil, err
	}
	user, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !input.ExpectedUpdatedAt.IsZero() && !user.UpdatedAt.Equal(input.ExpectedUpdatedAt) {
		return nil, infraerrors.New(http.StatusConflict, "CREDIT_CHANGED", "credit has changed, refresh and retry")
	}
	if input.Operation == "subtract" && user.Balance-input.Amount < user.FrozenBalance {
		return nil, infraerrors.New(http.StatusBadRequest, "INSUFFICIENT_AVAILABLE_BALANCE", "balance cannot be reduced below frozen balance")
	}
	if input.Operation != "add" && input.Operation != "subtract" {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_OPERATION", "operation must be add or subtract")
	}
	if _, err = s.UpdateUserBalance(ctx, userID, input.Amount, input.Operation, input.Notes); err != nil {
		return nil, err
	}
	return s.GetCreditUserDetail(ctx, userID)
}

// CreateAdminLimitedCredit 创建管理员手工限时额度及 grant 流水。
func (s *adminServiceImpl) CreateAdminLimitedCredit(ctx context.Context, userID int64, input AdminLimitedCreditCreateInput) (*LimitedCreditGrant, error) {
	if err := validateCreditAmount(input.Amount); err != nil {
		return nil, err
	}
	if input.ValidityDays < 1 || input.ValidityDays > MaxValidityDays {
		return nil, infraerrors.New(http.StatusBadRequest, "INVALID_VALIDITY_DAYS", "validity_days must be between 1 and 36500")
	}
	if _, err := s.GetUser(ctx, userID); err != nil {
		return nil, err
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	created, err := tx.UserLimitedCreditGrant.Create().SetUserID(userID).SetSourceType(LimitedCreditSourceAdminManual).SetInitialAmount(input.Amount).SetExpiresAt(time.Now().UTC().AddDate(0, 0, input.ValidityDays)).SetStatus(LimitedCreditStatusActive).SetNillableNotes(optionalCreditNotes(input.Notes)).Save(txCtx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.UserLimitedCreditLedger.Create().SetUserID(userID).SetGrantID(created.ID).SetEventType("grant").SetAmount(input.Amount).SetNillableNotes(optionalCreditNotes(input.Notes)).Save(txCtx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateAdminCreditCaches(ctx, userID)
	return adminLimitedCreditFromEntity(created), nil
}

// AdjustAdminLimitedCredit 原子调整额度金额或到期时间并写入流水。
func (s *adminServiceImpl) AdjustAdminLimitedCredit(ctx context.Context, userID, grantID int64, input AdminLimitedCreditAdjustmentInput) (*LimitedCreditGrant, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	grant, err := tx.UserLimitedCreditGrant.Query().Where(dbgrant.IDEQ(grantID), dbgrant.UserIDEQ(userID)).Only(txCtx)
	if err != nil {
		return nil, err
	}
	if !input.ExpectedUpdatedAt.IsZero() && !grant.UpdatedAt.Equal(input.ExpectedUpdatedAt) {
		return nil, infraerrors.New(http.StatusConflict, "CREDIT_CHANGED", "credit has changed, refresh and retry")
	}
	if grant.Status != LimitedCreditStatusActive || !grant.ExpiresAt.After(time.Now().UTC()) {
		return nil, infraerrors.New(http.StatusBadRequest, "LIMITED_CREDIT_INACTIVE", "inactive limited credit cannot be adjusted")
	}
	update := tx.UserLimitedCreditGrant.UpdateOne(grant)
	eventType := "admin_extend_expiry"
	var eventAmount float64
	if input.AmountOperation != "" {
		if err := validateCreditAmount(input.Amount); err != nil {
			return nil, err
		}
		delta := input.Amount
		if input.AmountOperation == "subtract" {
			delta = -delta
		} else if input.AmountOperation != "add" {
			return nil, infraerrors.New(http.StatusBadRequest, "INVALID_OPERATION", "amount_operation must be add or subtract")
		}
		switch input.AmountTarget {
		case "used":
			newUsed := grant.UsedAmount + delta
			if newUsed < 0 || newUsed+grant.FrozenAmount > grant.InitialAmount {
				return nil, infraerrors.New(http.StatusBadRequest, "INVALID_USED_AMOUNT", "used amount must remain between zero and available upper limit")
			}
			status := LimitedCreditStatusActive
			if math.Abs(newUsed+grant.FrozenAmount-grant.InitialAmount) < 0.00000001 {
				status = LimitedCreditStatusDepleted
			}
			update = update.SetUsedAmount(newUsed).SetStatus(status)
			eventType, eventAmount = "admin_increase_used", input.Amount
			if input.AmountOperation == "subtract" {
				eventType = "admin_decrease_used"
			}
		case "initial", "":
			if grant.InitialAmount+delta < grant.UsedAmount+grant.FrozenAmount {
				return nil, infraerrors.New(http.StatusBadRequest, "LIMITED_CREDIT_FROZEN", "upper limit cannot be reduced below used and frozen amount")
			}
			update = update.AddInitialAmount(delta)
			eventType, eventAmount = "admin_increase_limit", input.Amount
			if input.AmountOperation == "subtract" {
				eventType = "admin_decrease_limit"
			}
		default:
			return nil, infraerrors.New(http.StatusBadRequest, "INVALID_AMOUNT_TARGET", "amount_target must be used or initial")
		}
	} else {
		if input.ValidityDays <= 0 || (input.ExpiryOperation != "add" && input.ExpiryOperation != "subtract") {
			return nil, infraerrors.New(http.StatusBadRequest, "INVALID_EXPIRY_ADJUSTMENT", "expiry adjustment is invalid")
		}
		days := input.ValidityDays
		if input.ExpiryOperation == "subtract" {
			days = -days
			eventType = "admin_reduce_expiry"
		}
		newExpiry := grant.ExpiresAt.AddDate(0, 0, days)
		if !newExpiry.After(time.Now().UTC()) {
			return nil, infraerrors.New(http.StatusBadRequest, "INVALID_EXPIRY", "expiry must remain in the future")
		}
		update = update.SetExpiresAt(newExpiry)
		eventAmount = float64(input.ValidityDays)
	}
	updated, err := update.Save(txCtx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.UserLimitedCreditLedger.Create().SetUserID(userID).SetGrantID(grantID).SetEventType(eventType).SetAmount(adminLedgerAmount(eventAmount)).SetNillableNotes(optionalCreditNotes(input.Notes)).Save(txCtx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateAdminCreditCaches(ctx, userID)
	return adminLimitedCreditFromEntity(updated), nil
}

// ResetAdminLimitedCredit 将已用额度清零，并在来源有明确有效期时同步重置到期时间。
func (s *adminServiceImpl) ResetAdminLimitedCredit(ctx context.Context, userID, grantID int64, expectedUpdatedAt time.Time, notes string) (*LimitedCreditGrant, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	grant, err := tx.UserLimitedCreditGrant.Query().Where(dbgrant.IDEQ(grantID), dbgrant.UserIDEQ(userID)).Only(txCtx)
	if err != nil {
		return nil, err
	}
	if !expectedUpdatedAt.IsZero() && !grant.UpdatedAt.Equal(expectedUpdatedAt) {
		return nil, infraerrors.New(http.StatusConflict, "CREDIT_CHANGED", "credit has changed, refresh and retry")
	}
	if grant.FrozenAmount > 0 || grant.Status == LimitedCreditStatusRevoked {
		return nil, infraerrors.New(http.StatusBadRequest, "LIMITED_CREDIT_NOT_RESETTABLE", "revoked or frozen limited credit cannot be reset")
	}
	serviceGrant := adminLimitedCreditFromEntity(grant)
	validityDays := resolveLimitedCreditValidityDaysWithClient(txCtx, tx.Client(), serviceGrant)
	update := tx.UserLimitedCreditGrant.UpdateOne(grant).SetUsedAmount(0).SetStatus(LimitedCreditStatusActive)
	if validityDays != nil {
		update = update.SetExpiresAt(time.Now().UTC().AddDate(0, 0, *validityDays))
	}
	updated, err := update.Save(txCtx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.UserLimitedCreditLedger.Create().SetUserID(userID).SetGrantID(grantID).SetEventType("admin_reset").SetAmount(adminLedgerAmount(grant.UsedAmount)).SetNillableNotes(optionalCreditNotes(notes)).Save(txCtx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateAdminCreditCaches(ctx, userID)
	result := adminLimitedCreditFromEntity(updated)
	result.ValidityDays = validityDays
	return result, nil
}

// RevokeAdminLimitedCredit 作废无冻结金额的有效额度。
func (s *adminServiceImpl) RevokeAdminLimitedCredit(ctx context.Context, userID, grantID int64, expectedUpdatedAt time.Time, notes string) (*LimitedCreditGrant, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	grant, err := tx.UserLimitedCreditGrant.Query().Where(dbgrant.IDEQ(grantID), dbgrant.UserIDEQ(userID)).Only(txCtx)
	if err != nil {
		return nil, err
	}
	if !expectedUpdatedAt.IsZero() && !grant.UpdatedAt.Equal(expectedUpdatedAt) {
		return nil, infraerrors.New(http.StatusConflict, "CREDIT_CHANGED", "credit has changed, refresh and retry")
	}
	if grant.Status != LimitedCreditStatusActive || grant.FrozenAmount > 0 {
		return nil, infraerrors.New(http.StatusBadRequest, "LIMITED_CREDIT_NOT_REVOCABLE", "limited credit is inactive or has frozen amount")
	}
	updated, err := tx.UserLimitedCreditGrant.UpdateOne(grant).SetStatus(LimitedCreditStatusRevoked).Save(txCtx)
	if err != nil {
		return nil, err
	}
	remaining := math.Max(grant.InitialAmount-grant.UsedAmount, 0)
	if _, err = tx.UserLimitedCreditLedger.Create().SetUserID(userID).SetGrantID(grantID).SetEventType("admin_revoke").SetAmount(adminLedgerAmount(remaining)).SetNillableNotes(optionalCreditNotes(notes)).Save(txCtx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateAdminCreditCaches(ctx, userID)
	return adminLimitedCreditFromEntity(updated), nil
}

// invalidateAdminCreditCaches 在事务提交后刷新鉴权和计费缓存，失败不回滚财务数据。
func (s *adminServiceImpl) invalidateAdminCreditCaches(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(ctx, userID)
	}
}

// ListLimitedCreditLedger 返回单份限时额度完整流水。
func (s *adminServiceImpl) ListLimitedCreditLedger(ctx context.Context, userID, grantID int64) ([]LimitedCreditLedgerEntry, error) {
	rows, err := s.entClient.UserLimitedCreditLedger.Query().Where(dbledger.UserIDEQ(userID), dbledger.GrantIDEQ(grantID)).Order(dbent.Desc(dbledger.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]LimitedCreditLedgerEntry, 0, len(rows))
	for _, row := range rows {
		result = append(result, LimitedCreditLedgerEntry{ID: row.ID, EventType: row.EventType, Amount: row.Amount, Notes: creditNotesValue(row.Notes), CreatedAt: row.CreatedAt})
	}
	return result, nil
}
