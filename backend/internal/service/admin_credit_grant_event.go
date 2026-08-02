package service

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbevent "github.com/Wei-Shaw/sub2api/ent/creditgrantevent"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	dbtrigger "github.com/Wei-Shaw/sub2api/ent/usercreditgranteventtrigger"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	CreditGrantEventTypePermanent = "permanent"
	CreditGrantEventTypeLimited   = "limited"
)

// CreditGrantEvent 表示一项当前可用的管理员赠额事件配置。
type CreditGrantEvent struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	CreditType   string    `json:"credit_type"`
	Amount       float64   `json:"amount"`
	ValidityDays *int      `json:"validity_days,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// CreditGrantEventInput 是事件创建和完整更新的输入。
type CreditGrantEventInput struct {
	Name         string
	CreditType   string
	Amount       float64
	ValidityDays *int
}

// CreditGrantEventUserStatus 汇总事件当前配置和指定用户的历史触发快照。
type CreditGrantEventUserStatus struct {
	CreditGrantEvent
	Triggered          bool       `json:"triggered"`
	TriggeredAt        *time.Time `json:"triggered_at,omitempty"`
	ActualCreditType   *string    `json:"actual_credit_type,omitempty"`
	ActualAmount       *float64   `json:"actual_amount,omitempty"`
	ActualValidityDays *int       `json:"actual_validity_days,omitempty"`
	ActualExpiresAt    *time.Time `json:"actual_expires_at,omitempty"`
}

func creditGrantEventFromEntity(row *dbent.CreditGrantEvent) CreditGrantEvent {
	return CreditGrantEvent{
		ID:           row.ID,
		Name:         row.Name,
		CreditType:   row.CreditType,
		Amount:       row.Amount,
		ValidityDays: row.ValidityDays,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func validateCreditGrantEventInput(input CreditGrantEventInput) (CreditGrantEventInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 100 {
		return input, infraerrors.New(http.StatusBadRequest, "INVALID_CREDIT_GRANT_EVENT_NAME", "name must contain between 1 and 100 characters")
	}
	if err := validateCreditAmount(input.Amount); err != nil {
		return input, err
	}
	scaled := input.Amount * 1e8
	if math.Abs(scaled-math.Round(scaled)) > 1e-6 {
		return input, infraerrors.New(http.StatusBadRequest, "INVALID_CREDIT_AMOUNT_SCALE", "amount must have at most 8 decimal places")
	}
	switch input.CreditType {
	case CreditGrantEventTypePermanent:
		if input.ValidityDays != nil {
			return input, infraerrors.New(http.StatusBadRequest, "INVALID_CREDIT_GRANT_EVENT_VALIDITY", "permanent credit must not include validity_days")
		}
	case CreditGrantEventTypeLimited:
		if input.ValidityDays == nil || *input.ValidityDays < 1 || *input.ValidityDays > MaxValidityDays {
			return input, infraerrors.New(http.StatusBadRequest, "INVALID_CREDIT_GRANT_EVENT_VALIDITY", "limited credit validity_days must be between 1 and 36500")
		}
	default:
		return input, infraerrors.New(http.StatusBadRequest, "INVALID_CREDIT_GRANT_EVENT_TYPE", "credit_type must be permanent or limited")
	}
	return input, nil
}

// ListCreditGrantEvents 分页返回未删除事件，名称支持模糊搜索。
func (s *adminServiceImpl) ListCreditGrantEvents(ctx context.Context, page, pageSize int, search string) ([]CreditGrantEvent, int64, error) {
	query := s.entClient.CreditGrantEvent.Query()
	if value := strings.TrimSpace(search); value != "" {
		query = query.Where(dbevent.NameContainsFold(value))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	rows, err := query.Order(dbent.Desc(dbevent.FieldCreatedAt), dbent.Desc(dbevent.FieldID)).Offset((page - 1) * pageSize).Limit(pageSize).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	items := make([]CreditGrantEvent, 0, len(rows))
	for _, row := range rows {
		items = append(items, creditGrantEventFromEntity(row))
	}
	return items, int64(total), nil
}

// CreateCreditGrantEvent 创建一项立即生效的赠额事件。
func (s *adminServiceImpl) CreateCreditGrantEvent(ctx context.Context, input CreditGrantEventInput) (*CreditGrantEvent, error) {
	input, err := validateCreditGrantEventInput(input)
	if err != nil {
		return nil, err
	}
	row, err := s.entClient.CreditGrantEvent.Create().
		SetName(input.Name).
		SetCreditType(input.CreditType).
		SetAmount(input.Amount).
		SetNillableValidityDays(input.ValidityDays).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	item := creditGrantEventFromEntity(row)
	return &item, nil
}

// UpdateCreditGrantEvent 完整更新未来触发配置，历史触发快照保持不变。
func (s *adminServiceImpl) UpdateCreditGrantEvent(ctx context.Context, id int64, input CreditGrantEventInput, expectedUpdatedAt time.Time) (*CreditGrantEvent, error) {
	input, err := validateCreditGrantEventInput(input)
	if err != nil {
		return nil, err
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	query := tx.CreditGrantEvent.Query().Where(dbevent.IDEQ(id))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	row, err := query.Only(txCtx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.New(http.StatusNotFound, "CREDIT_GRANT_EVENT_NOT_FOUND", "credit grant event not found")
	}
	if err != nil {
		return nil, err
	}
	if expectedUpdatedAt.IsZero() || !row.UpdatedAt.Equal(expectedUpdatedAt) {
		return nil, infraerrors.New(http.StatusConflict, "CREDIT_GRANT_EVENT_CHANGED", "credit grant event has changed, refresh and retry")
	}
	update := tx.CreditGrantEvent.UpdateOne(row).
		SetName(input.Name).
		SetCreditType(input.CreditType).
		SetAmount(input.Amount)
	if input.ValidityDays == nil {
		update = update.ClearValidityDays()
	} else {
		update = update.SetValidityDays(*input.ValidityDays)
	}
	updated, err := update.Save(txCtx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	item := creditGrantEventFromEntity(updated)
	return &item, nil
}

// DeleteCreditGrantEvent 软删除事件并阻止后续触发。
func (s *adminServiceImpl) DeleteCreditGrantEvent(ctx context.Context, id int64, expectedUpdatedAt time.Time) error {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	query := tx.CreditGrantEvent.Query().Where(dbevent.IDEQ(id))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		query = query.ForUpdate()
	}
	row, err := query.Only(txCtx)
	if dbent.IsNotFound(err) {
		return infraerrors.New(http.StatusNotFound, "CREDIT_GRANT_EVENT_NOT_FOUND", "credit grant event not found")
	}
	if err != nil {
		return err
	}
	if expectedUpdatedAt.IsZero() || !row.UpdatedAt.Equal(expectedUpdatedAt) {
		return infraerrors.New(http.StatusConflict, "CREDIT_GRANT_EVENT_CHANGED", "credit grant event has changed, refresh and retry")
	}
	if err = tx.CreditGrantEvent.DeleteOne(row).Exec(txCtx); err != nil {
		return err
	}
	return tx.Commit()
}

// TriggerCreditGrantEvent 原子完成发放、流水和每用户一次的触发记录。
func (s *adminServiceImpl) TriggerCreditGrantEvent(ctx context.Context, userID, eventID int64) (*CreditGrantEventUserStatus, error) {
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	eventQuery := tx.CreditGrantEvent.Query().Where(dbevent.IDEQ(eventID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		eventQuery = eventQuery.ForUpdate()
	}
	event, err := eventQuery.Only(txCtx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.New(http.StatusNotFound, "CREDIT_GRANT_EVENT_NOT_FOUND", "credit grant event not found")
	}
	if err != nil {
		return nil, err
	}
	userQuery := tx.User.Query().Where(dbuser.IDEQ(userID))
	if tx.Client().Driver().Dialect() == dialect.Postgres {
		userQuery = userQuery.ForUpdate()
	}
	userRow, err := userQuery.Only(txCtx)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.New(http.StatusNotFound, "USER_NOT_FOUND", "user not found")
	}
	if err != nil {
		return nil, err
	}
	exists, err := tx.UserCreditGrantEventTrigger.Query().Where(dbtrigger.EventIDEQ(eventID), dbtrigger.UserIDEQ(userID)).Exist(txCtx)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, creditGrantEventAlreadyTriggeredError()
	}

	triggeredAt := time.Now().UTC()
	notes := fmt.Sprintf("赠额事件：%s", event.Name)
	triggerCreate := tx.UserCreditGrantEventTrigger.Create().
		SetEvent(event).
		SetUser(userRow).
		SetCreditTypeSnapshot(event.CreditType).
		SetAmountSnapshot(event.Amount).
		SetTriggeredAt(triggeredAt)

	status := CreditGrantEventUserStatus{CreditGrantEvent: creditGrantEventFromEntity(event), Triggered: true, TriggeredAt: &triggeredAt}
	actualType, actualAmount := event.CreditType, event.Amount
	status.ActualCreditType, status.ActualAmount = &actualType, &actualAmount

	switch event.CreditType {
	case CreditGrantEventTypePermanent:
		if _, err = tx.User.UpdateOne(userRow).AddBalance(event.Amount).Save(txCtx); err != nil {
			return nil, err
		}
		code, generateErr := GenerateRedeemCode()
		if generateErr != nil {
			return nil, generateErr
		}
		history, createErr := tx.RedeemCode.Create().
			SetCode(code).
			SetType(AdjustmentTypeAdminBalance).
			SetValue(event.Amount).
			SetStatus(StatusUsed).
			SetUsedBy(userID).
			SetUsedAt(triggeredAt).
			SetNotes(notes).
			SetValidityDays(0).
			Save(txCtx)
		if createErr != nil {
			return nil, createErr
		}
		triggerCreate = triggerCreate.SetBalanceHistory(history)
	case CreditGrantEventTypeLimited:
		if event.ValidityDays == nil {
			return nil, infraerrors.New(http.StatusConflict, "INVALID_CREDIT_GRANT_EVENT", "limited credit grant event has no validity_days")
		}
		expiresAt := triggeredAt.AddDate(0, 0, *event.ValidityDays)
		grant, createErr := tx.UserLimitedCreditGrant.Create().
			SetUserID(userID).
			SetSourceType(LimitedCreditSourceCreditGrantEvent).
			SetSourceID(eventID).
			SetInitialAmount(event.Amount).
			SetExpiresAt(expiresAt).
			SetStatus(LimitedCreditStatusActive).
			SetNotes(notes).
			Save(txCtx)
		if createErr != nil {
			return nil, mapCreditGrantEventTriggerError(createErr)
		}
		if _, createErr = tx.UserLimitedCreditLedger.Create().
			SetUserID(userID).
			SetGrantID(grant.ID).
			SetEventType("grant").
			SetAmount(event.Amount).
			SetNotes(notes).
			Save(txCtx); createErr != nil {
			return nil, createErr
		}
		triggerCreate = triggerCreate.
			SetValidityDaysSnapshot(*event.ValidityDays).
			SetExpiresAt(expiresAt).
			SetLimitedCreditGrant(grant)
		actualDays := *event.ValidityDays
		status.ActualValidityDays, status.ActualExpiresAt = &actualDays, &expiresAt
	default:
		return nil, infraerrors.New(http.StatusConflict, "INVALID_CREDIT_GRANT_EVENT", "credit grant event type is invalid")
	}

	if _, err = triggerCreate.Save(txCtx); err != nil {
		return nil, mapCreditGrantEventTriggerError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, mapCreditGrantEventTriggerError(err)
	}
	s.invalidateAdminCreditCaches(ctx, userID)
	return &status, nil
}

func creditGrantEventAlreadyTriggeredError() error {
	return infraerrors.New(http.StatusConflict, "CREDIT_GRANT_EVENT_ALREADY_TRIGGERED", "credit grant event has already been triggered for this user")
}

func mapCreditGrantEventTriggerError(err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if dbent.IsConstraintError(err) && (strings.Contains(message, "event_user") || strings.Contains(message, "credit_event_user")) {
		return creditGrantEventAlreadyTriggeredError()
	}
	return err
}

// attachCreditGrantEventCounts 批量补充用户列表的赠额事件领取进度。
func (s *adminServiceImpl) attachCreditGrantEventCounts(ctx context.Context, users []AdminCreditUser) error {
	events, err := s.entClient.CreditGrantEvent.Query().Select(dbevent.FieldID).All(ctx)
	if err != nil {
		return err
	}
	for i := range users {
		users[i].ActiveEventCount = len(events)
	}
	if len(users) == 0 || len(events) == 0 {
		return nil
	}
	userIDs := make([]int64, 0, len(users))
	userIndexes := make(map[int64]int, len(users))
	for i := range users {
		userIDs = append(userIDs, users[i].ID)
		userIndexes[users[i].ID] = i
	}
	eventIDs := make([]int64, 0, len(events))
	for _, event := range events {
		eventIDs = append(eventIDs, event.ID)
	}
	rows, err := s.entClient.UserCreditGrantEventTrigger.Query().
		Where(dbtrigger.UserIDIn(userIDs...), dbtrigger.EventIDIn(eventIDs...)).
		All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if index, ok := userIndexes[row.UserID]; ok {
			users[index].TriggeredEventCount++
		}
	}
	return nil
}

// listCreditGrantEventStatuses 返回全部未删除事件及指定用户的触发状态。
func (s *adminServiceImpl) listCreditGrantEventStatuses(ctx context.Context, userID int64) ([]CreditGrantEventUserStatus, error) {
	events, err := s.entClient.CreditGrantEvent.Query().Order(dbent.Desc(dbevent.FieldCreatedAt), dbent.Desc(dbevent.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]CreditGrantEventUserStatus, 0, len(events))
	if len(events) == 0 {
		return statuses, nil
	}
	eventIDs := make([]int64, 0, len(events))
	for _, event := range events {
		eventIDs = append(eventIDs, event.ID)
	}
	triggers, err := s.entClient.UserCreditGrantEventTrigger.Query().Where(dbtrigger.UserIDEQ(userID), dbtrigger.EventIDIn(eventIDs...)).All(ctx)
	if err != nil {
		return nil, err
	}
	byEvent := make(map[int64]*dbent.UserCreditGrantEventTrigger, len(triggers))
	for _, trigger := range triggers {
		byEvent[trigger.EventID] = trigger
	}
	for _, event := range events {
		status := CreditGrantEventUserStatus{CreditGrantEvent: creditGrantEventFromEntity(event)}
		if trigger := byEvent[event.ID]; trigger != nil {
			status.Triggered = true
			triggeredAt := trigger.TriggeredAt
			actualType, actualAmount := trigger.CreditTypeSnapshot, trigger.AmountSnapshot
			status.TriggeredAt = &triggeredAt
			status.ActualCreditType = &actualType
			status.ActualAmount = &actualAmount
			status.ActualValidityDays = trigger.ValidityDaysSnapshot
			status.ActualExpiresAt = trigger.ExpiresAt
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}
