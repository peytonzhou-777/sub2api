package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
)

// GetRechargeBonusOffer 返回充值页当前活动及用户参与进度。
func (s *PaymentService) GetRechargeBonusOffer(ctx context.Context, userID int64, at time.Time) (*RechargeBonusCampaignOffer, error) {
	if s == nil || s.rechargeBonusService == nil {
		return nil, nil
	}
	return s.rechargeBonusService.GetActiveCampaignOffer(ctx, userID, at)
}

// ListRechargeBonusCampaigns 返回全部充值活动。
func (s *PaymentService) ListRechargeBonusCampaigns(ctx context.Context) ([]RechargeBonusCampaign, error) {
	if s == nil || s.rechargeBonusService == nil {
		return nil, fmt.Errorf("recharge bonus service is not configured")
	}
	return s.rechargeBonusService.ListCampaigns(ctx)
}

// CreateRechargeBonusCampaign 创建充值活动。
func (s *PaymentService) CreateRechargeBonusCampaign(ctx context.Context, input RechargeBonusCampaignInput) (*RechargeBonusCampaign, error) {
	if s == nil || s.rechargeBonusService == nil {
		return nil, fmt.Errorf("recharge bonus service is not configured")
	}
	var created *RechargeBonusCampaign
	err := s.withRechargeBonusCampaignTransaction(ctx, func(txCtx context.Context, client *dbent.Client) error {
		var err error
		created, err = s.rechargeBonusService.CreateCampaign(txCtx, input)
		if err != nil {
			return err
		}
		return writeRechargeBonusCampaignAuditLog(txCtx, client, created.ID, "RECHARGE_BONUS_CAMPAIGN_CREATED", map[string]any{
			"name": created.Name, "startAt": created.StartAt, "endAt": created.EndAt,
		})
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// UpdateRechargeBonusCampaign 更新充值活动。
func (s *PaymentService) UpdateRechargeBonusCampaign(ctx context.Context, id int64, input RechargeBonusCampaignInput) (*RechargeBonusCampaign, error) {
	if s == nil || s.rechargeBonusService == nil {
		return nil, fmt.Errorf("recharge bonus service is not configured")
	}
	var updated *RechargeBonusCampaign
	err := s.withRechargeBonusCampaignTransaction(ctx, func(txCtx context.Context, client *dbent.Client) error {
		var err error
		updated, err = s.rechargeBonusService.UpdateCampaign(txCtx, id, input)
		if err != nil {
			return err
		}
		return writeRechargeBonusCampaignAuditLog(txCtx, client, id, "RECHARGE_BONUS_CAMPAIGN_UPDATED", map[string]any{
			"name": updated.Name, "startAt": updated.StartAt, "endAt": updated.EndAt,
		})
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// DeleteRechargeBonusCampaign 删除尚未开始的充值活动。
func (s *PaymentService) DeleteRechargeBonusCampaign(ctx context.Context, id int64) error {
	if s == nil || s.rechargeBonusService == nil {
		return fmt.Errorf("recharge bonus service is not configured")
	}
	return s.withRechargeBonusCampaignTransaction(ctx, func(txCtx context.Context, client *dbent.Client) error {
		if err := s.rechargeBonusService.DeleteCampaign(txCtx, id); err != nil {
			return err
		}
		return writeRechargeBonusCampaignAuditLog(txCtx, client, id, "RECHARGE_BONUS_CAMPAIGN_DELETED", map[string]any{})
	})
}

func (s *PaymentService) withRechargeBonusCampaignTransaction(
	ctx context.Context,
	operation func(context.Context, *dbent.Client) error,
) error {
	if s == nil || s.entClient == nil {
		return fmt.Errorf("payment database is not configured")
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin recharge bonus campaign transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := operation(txCtx, tx.Client()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recharge bonus campaign transaction: %w", err)
	}
	return nil
}

// writeRechargeBonusCampaignAuditLog 使用独立命名空间记录活动配置变更。
func writeRechargeBonusCampaignAuditLog(ctx context.Context, client *dbent.Client, campaignID int64, action string, detail map[string]any) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("marshal recharge bonus campaign audit: %w", err)
	}
	_, err = client.PaymentAuditLog.Create().
		SetOrderID(fmt.Sprintf("campaign:%d", campaignID)).
		SetAction(action).
		SetDetail(string(payload)).
		SetOperator("admin").
		Save(ctx)
	if err != nil {
		return fmt.Errorf("write recharge bonus campaign audit: %w", err)
	}
	return nil
}
