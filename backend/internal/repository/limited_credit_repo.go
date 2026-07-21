package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type limitedCreditRepository struct {
	client *dbent.Client
	db     *sql.DB
}

func NewLimitedCreditRepository(client *dbent.Client, sqlDB *sql.DB) service.LimitedCreditRepository {
	return &limitedCreditRepository{client: client, db: sqlDB}
}

// CreateGrant 原子创建一份限时额度及其 grant 流水。
func (r *limitedCreditRepository) CreateGrant(ctx context.Context, grant *service.LimitedCreditGrant) (*service.LimitedCreditGrant, error) {
	created, err := r.CreateGrants(ctx, []*service.LimitedCreditGrant{grant})
	if err != nil {
		return nil, err
	}
	if len(created) != 1 {
		return nil, fmt.Errorf("unexpected limited credit grant count: %d", len(created))
	}
	return &created[0], nil
}

// CreateGrants 在同一事务中创建多份限时额度及其流水，任一失败则全部回滚。
func (r *limitedCreditRepository) CreateGrants(ctx context.Context, grants []*service.LimitedCreditGrant) ([]service.LimitedCreditGrant, error) {
	return r.createGrants(ctx, grants, r.withTx)
}

// CreateGrantsIndependent 使用独立事务创建多份限时额度，不复用调用方事务。
func (r *limitedCreditRepository) CreateGrantsIndependent(ctx context.Context, grants []*service.LimitedCreditGrant) ([]service.LimitedCreditGrant, error) {
	return r.createGrants(ctx, grants, r.withNewTx)
}

func (r *limitedCreditRepository) createGrants(ctx context.Context, grants []*service.LimitedCreditGrant, runTx func(context.Context, func(context.Context, *dbent.Client) error) error) ([]service.LimitedCreditGrant, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("limited credit repository client is nil")
	}
	if len(grants) == 0 {
		return []service.LimitedCreditGrant{}, nil
	}
	created := make([]service.LimitedCreditGrant, 0, len(grants))
	err := runTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		for _, grant := range grants {
			if grant == nil {
				return fmt.Errorf("limited credit grant is required")
			}
			item, err := createLimitedCreditGrant(txCtx, client, grant)
			if err != nil {
				return err
			}
			created = append(created, *item)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (r *limitedCreditRepository) withTx(ctx context.Context, fn func(context.Context, *dbent.Client) error) error {
	if tx := dbent.TxFromContext(ctx); tx != nil {
		return fn(ctx, tx.Client())
	}
	return r.withNewTx(ctx, fn)
}

func (r *limitedCreditRepository) withNewTx(ctx context.Context, fn func(context.Context, *dbent.Client) error) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin limited credit transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := fn(txCtx, tx.Client()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit limited credit transaction: %w", err)
	}
	return nil
}

func createLimitedCreditGrant(ctx context.Context, client *dbent.Client, grant *service.LimitedCreditGrant) (*service.LimitedCreditGrant, error) {
	status := grant.Status
	if status == "" {
		status = service.LimitedCreditStatusActive
	}
	sourceType := grant.SourceType
	if sourceType == "" {
		sourceType = service.LimitedCreditSourceRedeemCode
	}
	notes := nillableString(grant.Notes)

	created, err := client.UserLimitedCreditGrant.Create().
		SetUserID(grant.UserID).
		SetSourceType(sourceType).
		SetNillableSourceID(grant.SourceID).
		SetInitialAmount(grant.InitialAmount).
		SetUsedAmount(grant.UsedAmount).
		SetFrozenAmount(grant.FrozenAmount).
		SetExpiresAt(grant.ExpiresAt).
		SetStatus(status).
		SetNillableNotes(notes).
		Save(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := client.UserLimitedCreditLedger.Create().
		SetUserID(grant.UserID).
		SetGrantID(created.ID).
		SetEventType("grant").
		SetAmount(grant.InitialAmount).
		SetNillableNotes(notes).
		Save(ctx); err != nil {
		return nil, err
	}

	return limitedCreditGrantEntityToService(created), nil
}

// ListActiveByUser 返回尚未过期且仍有剩余权益的额度批次。
func (r *limitedCreditRepository) ListActiveByUser(ctx context.Context, userID int64) ([]service.LimitedCreditGrant, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("limited credit repository client is nil")
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.UserLimitedCreditGrant.Query().
		Where(
			dbgrant.UserIDEQ(userID),
			dbgrant.StatusEQ(service.LimitedCreditStatusActive),
			dbgrant.ExpiresAtGT(time.Now().UTC()),
		).
		Order(dbent.Asc(dbgrant.FieldExpiresAt), dbent.Asc(dbgrant.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]service.LimitedCreditGrant, 0, len(rows))
	for _, row := range rows {
		grant := limitedCreditGrantEntityToService(row)
		if grant == nil {
			continue
		}
		if grant.RemainingAmount() <= 0 && grant.FrozenAmount <= 0 {
			continue
		}
		out = append(out, *grant)
	}
	return out, nil
}

// GetAvailableAmount 汇总用户当前可立即抵扣的限时额度。
func (r *limitedCreditRepository) GetAvailableAmount(ctx context.Context, userID int64) (float64, error) {
	if r == nil {
		return 0, fmt.Errorf("limited credit repository is nil")
	}
	if r.db == nil {
		grants, err := r.ListActiveByUser(ctx, userID)
		if err != nil {
			return 0, err
		}
		var total float64
		for _, grant := range grants {
			total += grant.AvailableAmount()
		}
		return total, nil
	}
	var total float64
	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(GREATEST(initial_amount - used_amount - frozen_amount, 0)), 0)
		FROM user_limited_credit_grants
		WHERE user_id = $1
			AND status = $2
			AND expires_at > NOW()
	`, userID, service.LimitedCreditStatusActive).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

func limitedCreditGrantEntityToService(m *dbent.UserLimitedCreditGrant) *service.LimitedCreditGrant {
	if m == nil {
		return nil
	}
	return &service.LimitedCreditGrant{
		ID:            m.ID,
		UserID:        m.UserID,
		SourceType:    m.SourceType,
		SourceID:      m.SourceID,
		InitialAmount: m.InitialAmount,
		UsedAmount:    m.UsedAmount,
		FrozenAmount:  m.FrozenAmount,
		ExpiresAt:     m.ExpiresAt,
		Status:        m.Status,
		Notes:         derefString(m.Notes),
		CreatedAt:     m.CreatedAt,
		UpdatedAt:     m.UpdatedAt,
	}
}

func nillableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
