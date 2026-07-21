package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	dbparticipation "github.com/Wei-Shaw/sub2api/ent/rechargebonusparticipation"
	dbgrant "github.com/Wei-Shaw/sub2api/ent/userlimitedcreditgrant"
)

// FulfillOrderBonus 原子占用用户活动次数并发放订单对应的限时额度。
func (s *RechargeBonusService) FulfillOrderBonus(ctx context.Context, order *dbent.PaymentOrder) (*RechargeBonusOrderSnapshot, error) {
	if order == nil {
		return nil, fmt.Errorf("payment order is required")
	}
	if order.RechargeBonusStatus == RechargeBonusStatusNone || order.RechargeBonusCampaignID == nil || order.RechargeBonusAmount <= 0 {
		return rechargeBonusSnapshotFromOrder(order), nil
	}
	if s == nil || s.client == nil || s.limitedCreditService == nil {
		return nil, fmt.Errorf("recharge bonus service is not configured")
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin recharge bonus fulfillment transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	current, err := tx.PaymentOrder.Get(txCtx, order.ID)
	if err != nil {
		return nil, fmt.Errorf("reload recharge bonus order: %w", err)
	}
	if current.RechargeBonusStatus == RechargeBonusStatusGranted || current.RechargeBonusStatus == RechargeBonusStatusLimitReached {
		return rechargeBonusSnapshotFromOrder(current), nil
	}
	if current.RechargeBonusStatus != RechargeBonusStatusEligible || current.RechargeBonusCampaignID == nil || current.RechargeBonusAmount <= 0 {
		return rechargeBonusSnapshotFromOrder(current), nil
	}

	// 通过条件空更新获取订单行锁，避免同一订单被并发履约后互相覆盖状态。
	locked, err := tx.PaymentOrder.Update().
		Where(
			paymentorder.IDEQ(current.ID),
			paymentorder.RechargeBonusStatusEQ(paymentorder.RechargeBonusStatusEligible),
		).
		SetRechargeBonusStatus(paymentorder.RechargeBonusStatusEligible).
		SetUpdatedAt(current.UpdatedAt).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("lock recharge bonus order: %w", err)
	}
	if locked == 0 {
		current, err = tx.PaymentOrder.Get(txCtx, order.ID)
		if err != nil {
			return nil, fmt.Errorf("reload completed recharge bonus order: %w", err)
		}
		return rechargeBonusSnapshotFromOrder(current), nil
	}

	existingGrant, err := tx.UserLimitedCreditGrant.Query().
		Where(
			dbgrant.SourceTypeEQ(LimitedCreditSourceRechargeBonus),
			dbgrant.SourceIDEQ(current.ID),
		).
		Only(txCtx)
	if err == nil {
		current, err = tx.PaymentOrder.UpdateOneID(current.ID).
			SetRechargeBonusStatus(RechargeBonusStatusGranted).
			SetRechargeBonusExpiresAt(existingGrant.ExpiresAt).
			SetUpdatedAt(current.UpdatedAt).
			Save(txCtx)
		if err != nil {
			return nil, fmt.Errorf("repair recharge bonus order snapshot: %w", err)
		}
		if err := writeRechargeBonusFulfillmentAudit(txCtx, tx.Client(), current, "RECHARGE_BONUS_GRANTED"); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit recharge bonus repair transaction: %w", err)
		}
		return rechargeBonusSnapshotFromOrder(current), nil
	}
	if !dbent.IsNotFound(err) {
		return nil, fmt.Errorf("query existing recharge bonus grant: %w", err)
	}

	campaign, err := tx.RechargeBonusCampaign.Get(txCtx, *current.RechargeBonusCampaignID)
	if err != nil {
		return nil, fmt.Errorf("load recharge bonus campaign for fulfillment: %w", err)
	}
	if err := tx.RechargeBonusParticipation.Create().
		SetCampaignID(campaign.ID).
		SetUserID(current.UserID).
		SetCompletedCount(0).
		OnConflictColumns(dbparticipation.FieldCampaignID, dbparticipation.FieldUserID).
		DoNothing().
		Exec(txCtx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("ensure recharge bonus participation: %w", err)
	}

	update := tx.RechargeBonusParticipation.Update().
		Where(
			dbparticipation.CampaignIDEQ(campaign.ID),
			dbparticipation.UserIDEQ(current.UserID),
		).
		AddCompletedCount(1)
	if campaign.ParticipationLimit > 0 {
		update = update.Where(dbparticipation.CompletedCountLT(campaign.ParticipationLimit))
	}
	claimed, err := update.Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("claim recharge bonus participation: %w", err)
	}
	if claimed == 0 {
		current, err = tx.PaymentOrder.UpdateOneID(current.ID).
			SetRechargeBonusStatus(RechargeBonusStatusLimitReached).
			SetUpdatedAt(current.UpdatedAt).
			Save(txCtx)
		if err != nil {
			return nil, fmt.Errorf("mark recharge bonus limit reached: %w", err)
		}
		if err := writeRechargeBonusFulfillmentAudit(txCtx, tx.Client(), current, "RECHARGE_BONUS_LIMIT_REACHED"); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit recharge bonus limit transaction: %w", err)
		}
		return rechargeBonusSnapshotFromOrder(current), nil
	}

	grantedAt := time.Now().UTC()
	grant, err := s.limitedCreditService.GrantFromRechargeBonus(
		txCtx,
		current.UserID,
		current.ID,
		current.RechargeBonusAmount,
		grantedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("grant recharge bonus limited credit: %w", err)
	}
	current, err = tx.PaymentOrder.UpdateOneID(current.ID).
		SetRechargeBonusStatus(RechargeBonusStatusGranted).
		SetRechargeBonusExpiresAt(grant.ExpiresAt).
		SetUpdatedAt(current.UpdatedAt).
		Save(txCtx)
	if err != nil {
		return nil, fmt.Errorf("mark recharge bonus granted: %w", err)
	}
	if err := writeRechargeBonusFulfillmentAudit(txCtx, tx.Client(), current, "RECHARGE_BONUS_GRANTED"); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit recharge bonus fulfillment transaction: %w", err)
	}
	s.invalidateRechargeBonusCaches(ctx, current.UserID)
	return rechargeBonusSnapshotFromOrder(current), nil
}

func writeRechargeBonusFulfillmentAudit(
	ctx context.Context,
	client *dbent.Client,
	order *dbent.PaymentOrder,
	action string,
) error {
	orderID := strconv.FormatInt(order.ID, 10)
	exists, err := client.PaymentAuditLog.Query().
		Where(paymentauditlog.OrderIDEQ(orderID), paymentauditlog.ActionEQ(action)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("check recharge bonus fulfillment audit: %w", err)
	}
	if exists {
		return nil
	}
	detail, err := json.Marshal(map[string]any{
		"campaignID": order.RechargeBonusCampaignID,
		"amount":     order.RechargeBonusAmount,
		"rate":       order.RechargeBonusRate,
		"expiresAt":  order.RechargeBonusExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("marshal recharge bonus fulfillment audit: %w", err)
	}
	if _, err := client.PaymentAuditLog.Create().
		SetOrderID(orderID).
		SetAction(action).
		SetDetail(string(detail)).
		SetOperator("system").
		Save(ctx); err != nil {
		return fmt.Errorf("write recharge bonus fulfillment audit: %w", err)
	}
	return nil
}

func (s *RechargeBonusService) invalidateRechargeBonusCaches(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService != nil {
		_ = s.billingCacheService.InvalidateUserBalance(ctx, userID)
	}
}

// RechargeBonusSnapshotFromOrder 将订单活动字段转换为公开响应快照。
func RechargeBonusSnapshotFromOrder(order *dbent.PaymentOrder) *RechargeBonusOrderSnapshot {
	return rechargeBonusSnapshotFromOrder(order)
}

func rechargeBonusSnapshotFromOrder(order *dbent.PaymentOrder) *RechargeBonusOrderSnapshot {
	if order == nil || order.RechargeBonusCampaignID == nil {
		return nil
	}
	name := ""
	if order.RechargeBonusCampaignName != nil {
		name = *order.RechargeBonusCampaignName
	}
	return &RechargeBonusOrderSnapshot{
		CampaignID:   *order.RechargeBonusCampaignID,
		CampaignName: name,
		Rate:         order.RechargeBonusRate,
		Amount:       order.RechargeBonusAmount,
		Status:       string(order.RechargeBonusStatus),
		ValidityDays: RechargeBonusValidityDays,
		ExpiresAt:    order.RechargeBonusExpiresAt,
	}
}
