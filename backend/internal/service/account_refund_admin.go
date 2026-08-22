package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/paymentproviderinstance"
	"github.com/Wei-Shaw/sub2api/ent/user"
	creditgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	AdminAccountRefundTabRefundable   = "refundable"
	AdminAccountRefundTabProcessing   = "processing"
	AdminAccountRefundTabManualReview = "manual_review"
	AdminAccountRefundTabCompleted    = "completed"
	AdminAccountRefundTabAll          = "all"
)

// AdminAccountRefundSummary 是管理员工作台的实时负债摘要。
type AdminAccountRefundSummary struct {
	RefundableTotals     map[string]float64 `json:"refundable_totals"`
	AutomaticTotals      map[string]float64 `json:"automatic_totals"`
	ManualExternalTotals map[string]float64 `json:"manual_external_totals"`
	RefundableUsers      int                `json:"refundable_users"`
	AutomaticUsers       int                `json:"automatic_users"`
	ProcessingUsers      int                `json:"processing_users"`
	ManualReviewUsers    int                `json:"manual_review_users"`
	CalculatedAt         time.Time          `json:"calculated_at"`
}

// AdminAccountRefundListParams 定义工作台筛选、排序和分页。
type AdminAccountRefundListParams struct {
	Page      int
	PageSize  int
	Tab       string
	Status    string
	Currency  string
	Keyword   string
	SortBy    string
	SortOrder string
}

// AdminAccountRefundActionInput 是除人工核验外所有管理员动作的并发契约。
type AdminAccountRefundActionInput struct {
	ExpectedStateRevision int64  `json:"expected_state_revision"`
	QuoteHash             string `json:"quote_hash,omitempty"`
}

// AdminAccountRefundStartInput 是管理员建立计费栅栏时提交的锁定报价。
type AdminAccountRefundStartInput struct {
	ExpectedStateRevision int64  `json:"expected_state_revision"`
	QuoteHash             string `json:"quote_hash"`
}

// AdminAccountRefundListItem 是列表所需的轻量用户清退摘要。
type AdminAccountRefundListItem struct {
	UserID               int64              `json:"user_id"`
	Username             string             `json:"username"`
	Email                string             `json:"email"`
	UserStatus           string             `json:"user_status"`
	PermanentBalance     float64            `json:"permanent_balance"`
	RechargeBonusBalance float64            `json:"recharge_bonus_balance"`
	OtherLimitedToClear  float64            `json:"other_limited_to_clear"`
	RefundTotals         map[string]float64 `json:"refund_totals"`
	CalculationStatus    string             `json:"calculation_status"`
	SelfServiceEligible  bool               `json:"self_service_eligible"`
	AdminExecutionMode   string             `json:"admin_execution_mode"`
	ReviewReasonCode     string             `json:"review_reason_code,omitempty"`
	FlowState            string             `json:"flow_state"`
	StateRevision        int64              `json:"state_revision"`
	AvailableActions     []string           `json:"available_actions"`
	UpdatedAt            time.Time          `json:"updated_at"`
}

// AdminAccountRefundTimelineEvent 是详情抽屉中的不可变状态时间线。
type AdminAccountRefundTimelineEvent struct {
	StateRevision    int64                        `json:"state_revision"`
	State            string                       `json:"state"`
	Message          string                       `json:"message,omitempty"`
	ReviewReasonCode string                       `json:"review_reason_code,omitempty"`
	FailureStage     string                       `json:"failure_stage,omitempty"`
	Actor            *AccountRefundActor          `json:"actor,omitempty"`
	Reconciliation   *AccountRefundReconciliation `json:"reconciliation,omitempty"`
	CreatedAt        time.Time                    `json:"created_at"`
}

// AdminAccountRefundDetail 汇总当前用户、权威报价、流程和审计时间线。
type AdminAccountRefundDetail struct {
	Item     AdminAccountRefundListItem        `json:"item"`
	Quote    *AccountRefundQuote               `json:"quote,omitempty"`
	Record   *AccountRefundRecord              `json:"record,omitempty"`
	Timeline []AdminAccountRefundTimelineEvent `json:"timeline"`
}

type accountRefundAdminSnapshot struct {
	user     *dbent.User
	quote    *AccountRefundQuote
	record   *AccountRefundRecord
	timeline []AdminAccountRefundTimelineEvent
}

// AdminStartAccountRefund 锁定 active/disabled 用户；重复幂等键返回原流程。
func (s *PaymentService) AdminStartAccountRefund(ctx context.Context, userID int64, input AdminAccountRefundStartInput, idempotencyKey string, actor AccountRefundActor) (*AccountRefundRecord, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if userID <= 0 || strings.TrimSpace(input.QuoteHash) == "" || idempotencyKey == "" {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "user_id, quote_hash and Idempotency-Key are required")
	}
	actor.Type = "admin"
	return s.lockAccountRefundWithOptions(ctx, userID, input.QuoteHash, false, accountRefundLockOptions{
		AdminInitiated: true, AllowDisabled: true, ExpectedStateRevision: input.ExpectedStateRevision, IdempotencyKey: idempotencyKey, Actor: &actor,
	})
}

// AdminAdvanceAccountRefund 推进排空或仅查询已提交退款状态。
func (s *PaymentService) AdminAdvanceAccountRefund(ctx context.Context, userID int64, input AdminAccountRefundActionInput, actor AccountRefundActor) (*AccountRefundRecord, error) {
	record, err := s.adminAccountRefundForAction(ctx, userID, input.ExpectedStateRevision, actor)
	if err != nil {
		return nil, err
	}
	if record.State != AccountRefundStateDraining && record.State != AccountRefundStateSubmitting && record.State != AccountRefundStatePending {
		return nil, infraerrors.Conflict("REFUND_ACTION_NOT_ALLOWED", "advance is not allowed in the current refund state")
	}
	if fence, ok := s.authCacheInvalidator.(RefundBillingFence); ok {
		if err := fence.AcquireRefundBillingLock(ctx, userID, record.RefundID); err != nil {
			return nil, infraerrors.ServiceUnavailable("REFUND_BILLING_FENCE_UNAVAILABLE", "cannot renew refund billing fence")
		}
	}
	return s.advanceAccountRefund(ctx, record)
}

// AdminConfirmAccountRefund 在第二确认点后执行自动原路退款。
func (s *PaymentService) AdminConfirmAccountRefund(ctx context.Context, userID int64, input AdminAccountRefundActionInput, actor AccountRefundActor) (*AccountRefundRecord, error) {
	record, err := s.adminAccountRefundForAction(ctx, userID, input.ExpectedStateRevision, actor)
	if err != nil {
		return nil, err
	}
	if record.State != AccountRefundStateReadyToConfirm {
		return nil, infraerrors.Conflict("REFUND_NOT_READY_TO_CONFIRM", "refund billing is not fully drained")
	}
	if record.Quote == nil || strings.TrimSpace(input.QuoteHash) == "" || input.QuoteHash != record.Quote.QuoteHash {
		return nil, infraerrors.Conflict("REFUND_QUOTE_CHANGED", "refund quote changed; review it again")
	}
	if record.Quote.AdminExecutionMode != AccountRefundAdminAutomatic {
		return nil, infraerrors.Conflict("REFUND_MANUAL_REVIEW", "this quote requires an external original-channel refund")
	}
	claimed, err := s.claimAccountRefundSubmission(ctx, record)
	if err != nil {
		return nil, err
	}
	return s.executeAccountRefundRoutes(ctx, claimed)
}

// AdminContinueAccountRefund 仅重试明确失败路由；待确认路由只允许查询。
func (s *PaymentService) AdminContinueAccountRefund(ctx context.Context, userID int64, input AdminAccountRefundActionInput, actor AccountRefundActor) (*AccountRefundRecord, error) {
	record, err := s.adminAccountRefundForAction(ctx, userID, input.ExpectedStateRevision, actor)
	if err != nil {
		return nil, err
	}
	switch record.State {
	case AccountRefundStateFailed, AccountRefundStatePartialExternalSuccess:
		if record.Quote == nil || record.Quote.AdminExecutionMode != AccountRefundAdminAutomatic {
			return nil, infraerrors.Conflict("REFUND_ACTION_NOT_ALLOWED", "automatic continuation is unavailable for this quote")
		}
		claimed, claimErr := s.claimAccountRefundSubmission(ctx, record)
		if claimErr != nil {
			return nil, claimErr
		}
		return s.executeAccountRefundRoutes(ctx, claimed)
	case AccountRefundStateSubmitting, AccountRefundStatePending:
		return s.reconcileAccountRefundPending(ctx, record)
	default:
		return nil, infraerrors.Conflict("REFUND_ACTION_NOT_ALLOWED", "continue is not allowed in the current refund state")
	}
}

// AdminCancelAccountRefundWithInput 在任何外部成功或未知结果出现前安全取消。
func (s *PaymentService) AdminCancelAccountRefundWithInput(ctx context.Context, userID int64, input AdminAccountRefundActionInput, actor AccountRefundActor) (*AccountRefundRecord, error) {
	record, err := s.adminAccountRefundForAction(ctx, userID, input.ExpectedStateRevision, actor)
	if err != nil {
		return nil, err
	}
	return s.cancelAccountRefundRecord(ctx, record)
}

// AdminRecalculateAccountRefund 仅重算未发生外部退款的异常报价。
func (s *PaymentService) AdminRecalculateAccountRefund(ctx context.Context, userID int64, input AdminAccountRefundActionInput, actor AccountRefundActor) (*AccountRefundRecord, error) {
	record, err := s.adminAccountRefundForAction(ctx, userID, input.ExpectedStateRevision, actor)
	if err != nil {
		return nil, err
	}
	if record.State != AccountRefundStateManualReview || accountRefundReviewReason(record) != AccountRefundReviewQuoteInconsistent || !accountRefundCanCancel(record) {
		return nil, infraerrors.Conflict("REFUND_ACTION_NOT_ALLOWED", "the current refund cannot be safely recalculated")
	}
	quote, err := s.buildAccountRefundQuote(ctx, userID)
	if err != nil {
		return nil, err
	}
	record.Quote = quote
	record.ReviewReasonCode = ""
	record.FailureStage = ""
	record.State = AccountRefundStateDraining
	record.Message = "refund quote recalculated; checking billing drain"
	record.UpdatedAt = time.Now().UTC()
	if err := s.writeAccountRefundAudit(ctx, record); err != nil {
		return nil, err
	}
	return s.refreshAccountRefundDrain(ctx, record)
}

// AdminFinalizeAccountRefund 只重试本地余额与订单收尾，绝不调用支付网关。
func (s *PaymentService) AdminFinalizeAccountRefund(ctx context.Context, userID int64, input AdminAccountRefundActionInput, actor AccountRefundActor) (*AccountRefundRecord, error) {
	record, err := s.adminAccountRefundForAction(ctx, userID, input.ExpectedStateRevision, actor)
	if err != nil {
		return nil, err
	}
	if record.State != AccountRefundStateManualReview || accountRefundReviewReason(record) != AccountRefundReviewFinalizeFailed || !accountRefundAllRoutesCompleted(record) {
		return nil, infraerrors.Conflict("REFUND_ACTION_NOT_ALLOWED", "local finalization is not available in the current refund state")
	}
	if err := s.finalizeAccountRefundCredits(ctx, record); err != nil {
		record.State = AccountRefundStateManualReview
		record.ReviewReasonCode = AccountRefundReviewFinalizeFailed
		record.FailureStage = AccountRefundFailurePostGateway
		record.Message = "finalize account credits: " + err.Error()
		record.UpdatedAt = time.Now().UTC()
		if auditErr := s.writeAccountRefundAudit(ctx, record); auditErr != nil {
			return nil, auditErr
		}
		return record, nil
	}
	if err := s.restoreTerminalAccountRefundAccess(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

// AdminRestoreAccountRefundAccess 幂等恢复终态遗留的 refund_locked 状态。
func (s *PaymentService) AdminRestoreAccountRefundAccess(ctx context.Context, userID int64, input AdminAccountRefundActionInput, actor AccountRefundActor) (*AccountRefundRecord, error) {
	record, err := s.adminAccountRefundForAction(ctx, userID, input.ExpectedStateRevision, actor)
	if err != nil {
		return nil, err
	}
	if !accountRefundTerminal(record.State) {
		return nil, infraerrors.Conflict("REFUND_ACTION_NOT_ALLOWED", "only a terminal refund can restore account access")
	}
	if err := s.restoreTerminalAccountRefundAccess(ctx, record); err != nil {
		return nil, err
	}
	record.Message = "terminal account access restored"
	record.UpdatedAt = time.Now().UTC()
	if err := s.writeAccountRefundAudit(ctx, record); err != nil {
		return nil, err
	}
	return record, nil
}

func (s *PaymentService) adminAccountRefundForAction(ctx context.Context, userID, expectedRevision int64, actor AccountRefundActor) (*AccountRefundRecord, error) {
	record, err := s.latestAccountRefundForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, infraerrors.NotFound("REFUND_NOT_FOUND", "account refund not found")
	}
	if expectedRevision <= 0 || record.StateRevision != expectedRevision {
		return nil, infraerrors.Conflict("REFUND_STATE_CHANGED", "account refund state changed; refresh and retry")
	}
	actor.Type = "admin"
	record.Actor = &actor
	return record, nil
}

// GetAdminAccountRefundSummary 只读计算全部当前清退负债，不推进任何流程。
func (s *PaymentService) GetAdminAccountRefundSummary(ctx context.Context) (*AdminAccountRefundSummary, error) {
	snapshots, err := s.loadAdminAccountRefundSnapshots(ctx, 0)
	if err != nil {
		return nil, err
	}
	result := &AdminAccountRefundSummary{
		RefundableTotals: map[string]float64{}, AutomaticTotals: map[string]float64{}, ManualExternalTotals: map[string]float64{}, CalculatedAt: time.Now().UTC(),
	}
	for _, snapshot := range snapshots {
		item := buildAdminAccountRefundListItem(snapshot)
		activeRefund := snapshot.record != nil && !accountRefundTerminal(snapshot.record.State)
		requiresManualReview := false
		if activeRefund {
			if snapshot.record.State == AccountRefundStateManualReview {
				requiresManualReview = true
			} else {
				result.ProcessingUsers++
			}
		}
		if !activeRefund && (item.CalculationStatus == AccountRefundCalculationManualReview || item.AdminExecutionMode == AccountRefundAdminManual) {
			requiresManualReview = true
		}
		if requiresManualReview {
			result.ManualReviewUsers++
		}
		if len(item.RefundTotals) == 0 {
			continue
		}
		result.RefundableUsers++
		addAccountRefundCurrencyTotals(result.RefundableTotals, item.RefundTotals)
		switch item.AdminExecutionMode {
		case AccountRefundAdminAutomatic:
			if !activeRefund {
				result.AutomaticUsers++
			}
			addAccountRefundCurrencyTotals(result.AutomaticTotals, item.RefundTotals)
		case AccountRefundAdminManual:
			addAccountRefundCurrencyTotals(result.ManualExternalTotals, item.RefundTotals)
		}
	}
	normalizeAccountRefundCurrencyTotals(result.RefundableTotals)
	normalizeAccountRefundCurrencyTotals(result.AutomaticTotals)
	normalizeAccountRefundCurrencyTotals(result.ManualExternalTotals)
	return result, nil
}

// ListAdminAccountRefunds 返回固定查询次数装配后的分页列表。
func (s *PaymentService) ListAdminAccountRefunds(ctx context.Context, params AdminAccountRefundListParams) ([]AdminAccountRefundListItem, int64, error) {
	snapshots, err := s.loadAdminAccountRefundSnapshots(ctx, 0)
	if err != nil {
		return nil, 0, err
	}
	params = normalizeAdminAccountRefundListParams(params)
	items := make([]AdminAccountRefundListItem, 0, len(snapshots))
	for _, snapshot := range snapshots {
		item := buildAdminAccountRefundListItem(snapshot)
		if !matchesAdminAccountRefundListItem(item, params) {
			continue
		}
		items = append(items, item)
	}
	sortAdminAccountRefundItems(items, params.SortBy, params.SortOrder, params.Currency)
	total := int64(len(items))
	start := (params.Page - 1) * params.PageSize
	if start >= len(items) {
		return []AdminAccountRefundListItem{}, total, nil
	}
	end := start + params.PageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total, nil
}

// GetAdminAccountRefundDetail 返回只读详情；不存在历史流程时仍返回当前报价。
func (s *PaymentService) GetAdminAccountRefundDetail(ctx context.Context, userID int64) (*AdminAccountRefundDetail, error) {
	snapshots, err := s.loadAdminAccountRefundSnapshots(ctx, userID)
	if err != nil {
		return nil, err
	}
	if len(snapshots) == 0 {
		return nil, infraerrors.NotFound("REFUND_USER_NOT_FOUND", "refund user not found")
	}
	snapshot := snapshots[0]
	return &AdminAccountRefundDetail{Item: buildAdminAccountRefundListItem(snapshot), Quote: snapshot.quote, Record: snapshot.record, Timeline: snapshot.timeline}, nil
}

// loadAdminAccountRefundSnapshots 批量装配用户、订单、赠额、支付实例和审计状态。
func (s *PaymentService) loadAdminAccountRefundSnapshots(ctx context.Context, onlyUserID int64) ([]accountRefundAdminSnapshot, error) {
	userQuery := s.entClient.User.Query().Where(user.DeletedAtIsNil())
	if onlyUserID > 0 {
		userQuery = userQuery.Where(user.IDEQ(onlyUserID))
	}
	users, err := userQuery.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list account refund users: %w", err)
	}
	if len(users) == 0 {
		return nil, nil
	}
	userIDs := make([]int64, 0, len(users))
	for _, row := range users {
		userIDs = append(userIDs, row.ID)
	}
	orders, err := s.entClient.PaymentOrder.Query().Where(
		paymentorder.UserIDIn(userIDs...), paymentorder.OrderTypeEQ(payment.OrderTypeBalance),
		paymentorder.Or(accountRefundHistoricalOrderPredicate(), accountRefundInFlightOrderPredicate()),
	).Order(dbent.Asc(paymentorder.FieldCompletedAt), dbent.Asc(paymentorder.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list account refund batch orders: %w", err)
	}
	grants, err := s.entClient.UserLimitedCreditGrant.Query().Where(creditgrant.UserIDIn(userIDs...)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list account refund batch credits: %w", err)
	}
	providerIDs := accountRefundProviderIDs(orders)
	providers := make(map[int64]*dbent.PaymentProviderInstance, len(providerIDs))
	if len(providerIDs) > 0 {
		rows, providerErr := s.entClient.PaymentProviderInstance.Query().Where(paymentproviderinstance.IDIn(providerIDs...)).All(ctx)
		if providerErr != nil {
			return nil, fmt.Errorf("list account refund batch providers: %w", providerErr)
		}
		for _, row := range rows {
			providers[row.ID] = row
		}
	}
	auditPrefix := accountRefundAuditPrefix
	if onlyUserID > 0 {
		auditPrefix += strconv.FormatInt(onlyUserID, 10) + ":"
	}
	audits, err := s.entClient.PaymentAuditLog.Query().Where(paymentauditlog.OrderIDHasPrefix(auditPrefix)).Order(dbent.Asc(paymentauditlog.FieldCreatedAt), dbent.Asc(paymentauditlog.FieldID)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list account refund batch audits: %w", err)
	}
	ordersByUser := make(map[int64][]*dbent.PaymentOrder)
	inFlightByUser := make(map[int64]int)
	for _, order := range orders {
		if isAccountRefundHistoricalOrder(order.Status) {
			ordersByUser[order.UserID] = append(ordersByUser[order.UserID], order)
		} else {
			inFlightByUser[order.UserID]++
		}
	}
	grantsByUser := make(map[int64][]*dbent.UserLimitedCreditGrant)
	for _, grant := range grants {
		grantsByUser[grant.UserID] = append(grantsByUser[grant.UserID], grant)
	}
	latestByUser := make(map[int64]*AccountRefundRecord)
	timelineByUser := make(map[int64][]AdminAccountRefundTimelineEvent)
	for _, audit := range audits {
		record, parseErr := parseAccountRefundRecord(audit)
		if parseErr != nil || record.UserID <= 0 {
			continue
		}
		if onlyUserID > 0 && record.UserID != onlyUserID {
			continue
		}
		previous := latestByUser[record.UserID]
		event := AdminAccountRefundTimelineEvent{
			StateRevision: record.StateRevision, State: record.State, Message: record.Message, ReviewReasonCode: accountRefundReviewReason(record), FailureStage: record.FailureStage, Actor: record.Actor, CreatedAt: audit.CreatedAt,
		}
		if len(record.Reconciliations) > 0 {
			latest := record.Reconciliations[len(record.Reconciliations)-1]
			if latestReconciliationCount(previous, record.RefundID) < len(record.Reconciliations) {
				event.Reconciliation = &latest
			}
		}
		timelineByUser[record.UserID] = append(timelineByUser[record.UserID], event)
		latestByUser[record.UserID] = record
	}
	now := time.Now().UTC()
	result := make([]accountRefundAdminSnapshot, 0, len(users))
	for _, row := range users {
		account := accountRefundUserFromEnt(row)
		quote := calculateAccountRefundQuote(accountRefundQuoteInputs{account: account, inFlight: inFlightByUser[row.ID], orders: ordersByUser[row.ID], grants: grantsByUser[row.ID], providers: providers, now: now})
		snapshot := accountRefundAdminSnapshot{user: row, quote: quote, record: latestByUser[row.ID], timeline: timelineByUser[row.ID]}
		if onlyUserID == 0 && !includeAdminAccountRefundSnapshot(snapshot) {
			continue
		}
		result = append(result, snapshot)
	}
	return result, nil
}

func latestReconciliationCount(record *AccountRefundRecord, refundID string) int {
	if record == nil || record.RefundID != refundID {
		return 0
	}
	return len(record.Reconciliations)
}

func accountRefundProviderIDs(orders []*dbent.PaymentOrder) []int64 {
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, order := range orders {
		if order.ProviderInstanceID == nil {
			continue
		}
		id, err := parseProviderInstanceID(*order.ProviderInstanceID)
		if err != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func parseProviderInstanceID(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid provider instance id")
	}
	return id, nil
}

func accountRefundUserFromEnt(row *dbent.User) *User {
	return &User{ID: row.ID, Email: row.Email, Username: row.Username, Balance: row.Balance, FrozenBalance: row.FrozenBalance, TotalRecharged: row.TotalRecharged, Status: row.Status, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, DeletedAt: row.DeletedAt}
}

func isAccountRefundHistoricalOrder(status string) bool {
	return status == OrderStatusCompleted || status == OrderStatusPartiallyRefunded || status == OrderStatusRefunded
}

func includeAdminAccountRefundSnapshot(snapshot accountRefundAdminSnapshot) bool {
	if snapshot.record != nil {
		return true
	}
	return snapshot.quote != nil && (snapshot.quote.RefundCreditTotal > accountRefundTolerance || snapshot.quote.CalculationStatus == AccountRefundCalculationManualReview && (snapshot.user.Balance > accountRefundTolerance || snapshot.quote.RechargeBonusBalance > accountRefundTolerance || snapshot.quote.OtherLimitedToClear > accountRefundTolerance))
}

func buildAdminAccountRefundListItem(snapshot accountRefundAdminSnapshot) AdminAccountRefundListItem {
	quote := snapshot.quote
	item := AdminAccountRefundListItem{UserID: snapshot.user.ID, Username: snapshot.user.Username, Email: snapshot.user.Email, UserStatus: snapshot.user.Status, RefundTotals: map[string]float64{}, FlowState: "estimate", UpdatedAt: snapshot.user.UpdatedAt}
	if quote != nil {
		item.PermanentBalance = quote.PermanentBalance
		item.RechargeBonusBalance = quote.RechargeBonusBalance
		item.OtherLimitedToClear = quote.OtherLimitedToClear
		item.CalculationStatus = quote.CalculationStatus
		item.SelfServiceEligible = quote.SelfServiceEligible
		item.AdminExecutionMode = quote.AdminExecutionMode
		item.ReviewReasonCode = quote.ReviewReasonCode
		for currency, amount := range quote.GatewayTotals {
			if amount > accountRefundTolerance {
				item.RefundTotals[currency] = normalizeRefundFloat(amount)
			}
		}
	}
	if record := snapshot.record; record != nil {
		item.FlowState = record.State
		item.StateRevision = record.StateRevision
		item.UpdatedAt = record.UpdatedAt
		if reason := accountRefundReviewReason(record); reason != "" {
			item.ReviewReasonCode = reason
		}
		if !accountRefundTerminal(record.State) {
			item.RefundTotals = outstandingAccountRefundTotals(record)
			if record.Quote != nil {
				item.AdminExecutionMode = record.Quote.AdminExecutionMode
			}
		}
	}
	item.AvailableActions = availableAdminAccountRefundActions(snapshot.user.Status, snapshot.record, quote)
	return item
}

func outstandingAccountRefundTotals(record *AccountRefundRecord) map[string]float64 {
	totals := map[string]float64{}
	if record == nil || record.Quote == nil {
		return totals
	}
	for _, route := range record.Quote.Orders {
		if route.GatewayStatus == payment.ProviderStatusSuccess || route.GatewayStatus == payment.ProviderStatusRefunded || route.GatewayRefund <= accountRefundTolerance {
			continue
		}
		totals[route.Currency] += route.GatewayRefund
	}
	normalizeAccountRefundCurrencyTotals(totals)
	return totals
}

func accountRefundReviewReason(record *AccountRefundRecord) string {
	if record == nil {
		return ""
	}
	if record.ReviewReasonCode != "" {
		return record.ReviewReasonCode
	}
	if record.State != AccountRefundStateManualReview {
		return ""
	}
	message := strings.ToLower(record.Message)
	switch {
	case strings.Contains(message, "cannot query"), strings.Contains(message, "outcome is unknown"):
		return AccountRefundReviewGatewayUnknown
	case strings.Contains(message, "provider is unavailable"):
		return AccountRefundReviewProviderUnavailable
	case strings.Contains(message, "query failed"):
		return AccountRefundReviewGatewayQueryFailed
	case strings.Contains(message, "finaliz"):
		return AccountRefundReviewFinalizeFailed
	case record.Quote != nil && record.Quote.CalculationStatus == AccountRefundCalculationManualReview:
		return AccountRefundReviewQuoteInconsistent
	default:
		return AccountRefundReviewLegacyUnknown
	}
}

func availableAdminAccountRefundActions(userStatus string, record *AccountRefundRecord, quote *AccountRefundQuote) []string {
	if record == nil || accountRefundTerminal(record.State) {
		if record != nil && (userStatus == StatusRefundLocked || accountRefundReviewReason(record) == AccountRefundReviewAccessRestoreFailed) {
			return []string{"restore-access"}
		}
		if quote != nil && quote.CalculationStatus == AccountRefundCalculationVerified && quote.RefundCreditTotal > accountRefundTolerance && quote.AdminExecutionMode != AccountRefundAdminBlocked {
			return []string{"start"}
		}
		return nil
	}
	switch record.State {
	case AccountRefundStateDraining:
		return []string{"advance", "cancel"}
	case AccountRefundStateReadyToConfirm:
		return []string{"confirm", "cancel"}
	case AccountRefundStateFailed:
		return []string{"continue", "cancel"}
	case AccountRefundStatePartialExternalSuccess:
		return []string{"continue"}
	case AccountRefundStateSubmitting, AccountRefundStatePending:
		return []string{"advance"}
	case AccountRefundStateManualReview:
		switch accountRefundReviewReason(record) {
		case AccountRefundReviewGatewayUnknown, AccountRefundReviewProviderUnavailable, AccountRefundReviewGatewayQueryFailed, AccountRefundReviewManualExternalRequired:
			return []string{"reconcile"}
		case AccountRefundReviewQuoteInconsistent:
			if accountRefundCanCancel(record) {
				return []string{"recalculate", "cancel"}
			}
			return []string{"recalculate"}
		case AccountRefundReviewFinalizeFailed:
			return []string{"finalize"}
		}
	}
	return nil
}

func normalizeAdminAccountRefundListParams(params AdminAccountRefundListParams) AdminAccountRefundListParams {
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	if params.PageSize > 100 {
		params.PageSize = 100
	}
	params.Tab = strings.ToLower(strings.TrimSpace(params.Tab))
	if params.Tab == "" {
		params.Tab = AdminAccountRefundTabRefundable
	}
	params.Status = strings.ToLower(strings.TrimSpace(params.Status))
	params.Currency = strings.ToUpper(strings.TrimSpace(params.Currency))
	params.Keyword = strings.ToLower(strings.TrimSpace(params.Keyword))
	params.SortBy = strings.ToLower(strings.TrimSpace(params.SortBy))
	if params.SortBy == "" {
		params.SortBy = "updated_at"
	}
	if params.SortBy == "refund_amount" && params.Currency == "" {
		params.SortBy = "updated_at"
	}
	params.SortOrder = strings.ToLower(strings.TrimSpace(params.SortOrder))
	if params.SortOrder != "asc" {
		params.SortOrder = "desc"
	}
	return params
}

func matchesAdminAccountRefundListItem(item AdminAccountRefundListItem, params AdminAccountRefundListParams) bool {
	switch params.Tab {
	case AdminAccountRefundTabRefundable:
		if len(item.RefundTotals) == 0 || item.FlowState != "estimate" && item.FlowState != AccountRefundStateCanceled && !accountRefundTerminal(item.FlowState) {
			return false
		}
	case AdminAccountRefundTabProcessing:
		if item.FlowState == "estimate" || item.FlowState == AccountRefundStateManualReview || accountRefundTerminal(item.FlowState) {
			return false
		}
	case AdminAccountRefundTabManualReview:
		if item.FlowState != AccountRefundStateManualReview && item.CalculationStatus != AccountRefundCalculationManualReview && item.AdminExecutionMode != AccountRefundAdminManual {
			return false
		}
	case AdminAccountRefundTabCompleted:
		if !accountRefundTerminal(item.FlowState) {
			return false
		}
	case AdminAccountRefundTabAll:
	default:
		return false
	}
	if params.Status != "" && item.FlowState != params.Status {
		return false
	}
	if params.Currency != "" {
		if _, ok := item.RefundTotals[params.Currency]; !ok {
			return false
		}
	}
	if params.Keyword != "" {
		haystack := strings.ToLower(fmt.Sprintf("%d %s %s", item.UserID, item.Username, item.Email))
		if !strings.Contains(haystack, params.Keyword) {
			return false
		}
	}
	return true
}

func sortAdminAccountRefundItems(items []AdminAccountRefundListItem, sortBy, order, currency string) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		comparison := 0
		switch sortBy {
		case "email":
			comparison = strings.Compare(strings.ToLower(left.Email), strings.ToLower(right.Email))
		case "refund_amount":
			leftAmount, rightAmount := left.RefundTotals[currency], right.RefundTotals[currency]
			if leftAmount < rightAmount {
				comparison = -1
			} else if leftAmount > rightAmount {
				comparison = 1
			}
		default:
			comparison = left.UpdatedAt.Compare(right.UpdatedAt)
		}
		if comparison == 0 {
			comparison = int(left.UserID - right.UserID)
		}
		if order == "asc" {
			return comparison < 0
		}
		return comparison > 0
	})
}

func addAccountRefundCurrencyTotals(target, source map[string]float64) {
	for currency, amount := range source {
		target[currency] += amount
	}
}

func normalizeAccountRefundCurrencyTotals(totals map[string]float64) {
	for currency, amount := range totals {
		totals[currency] = normalizeRefundFloat(amount)
	}
}
