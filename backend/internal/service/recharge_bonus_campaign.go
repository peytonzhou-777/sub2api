package service

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbcampaign "github.com/Wei-Shaw/sub2api/ent/rechargebonuscampaign"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	RechargeBonusCampaignStatusScheduled = "scheduled"
	RechargeBonusCampaignStatusActive    = "active"
	RechargeBonusCampaignStatusEnded     = "ended"
)

// RechargeBonusCampaign 表示后台和结算流程使用的一期充值活动。
type RechargeBonusCampaign struct {
	ID                 int64               `json:"id"`
	Name               string              `json:"name"`
	Description        string              `json:"description"`
	StartAt            time.Time           `json:"start_at"`
	EndAt              time.Time           `json:"end_at"`
	ParticipationLimit int                 `json:"participation_limit"`
	Tiers              []RechargeBonusTier `json:"tiers"`
	Status             string              `json:"status"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

// RechargeBonusService 管理充值活动配置、试算和到账发放。
type RechargeBonusService struct {
	client               *dbent.Client
	limitedCreditService *LimitedCreditService
	billingCacheService  *BillingCacheService
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

// NewRechargeBonusService 创建充值活动服务。
func NewRechargeBonusService(
	client *dbent.Client,
	limitedCreditService *LimitedCreditService,
	billingCacheService *BillingCacheService,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
) *RechargeBonusService {
	return &RechargeBonusService{
		client:               client,
		limitedCreditService: limitedCreditService,
		billingCacheService:  billingCacheService,
		authCacheInvalidator: authCacheInvalidator,
	}
}

// CreateCampaign 创建一期未开始或正在生效的充值活动。
func (s *RechargeBonusService) CreateCampaign(ctx context.Context, input RechargeBonusCampaignInput) (*RechargeBonusCampaign, error) {
	input = normalizeRechargeBonusCampaignInput(input)
	if err := ValidateRechargeBonusCampaign(input); err != nil {
		return nil, infraerrors.BadRequest("INVALID_RECHARGE_BONUS_CAMPAIGN", err.Error())
	}
	if err := s.ensureCampaignDoesNotOverlap(ctx, 0, input.StartAt, input.EndAt); err != nil {
		return nil, err
	}

	client := rechargeBonusClientFromContext(ctx, s.client)
	created, err := client.RechargeBonusCampaign.Create().
		SetName(input.Name).
		SetDescription(input.Description).
		SetStartAt(input.StartAt).
		SetEndAt(input.EndAt).
		SetParticipationLimit(input.ParticipationLimit).
		SetTiers(input.Tiers).
		Save(ctx)
	if err != nil {
		if isRechargeBonusOverlapConstraintError(err) {
			return nil, rechargeBonusOverlapError()
		}
		return nil, fmt.Errorf("create recharge bonus campaign: %w", err)
	}
	return rechargeBonusCampaignFromEntity(created, time.Now().UTC()), nil
}

// ListCampaigns 按开始时间倒序返回全部充值活动。
func (s *RechargeBonusService) ListCampaigns(ctx context.Context) ([]RechargeBonusCampaign, error) {
	client := rechargeBonusClientFromContext(ctx, s.client)
	rows, err := client.RechargeBonusCampaign.Query().
		Order(dbent.Desc(dbcampaign.FieldStartAt), dbent.Desc(dbcampaign.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list recharge bonus campaigns: %w", err)
	}
	now := time.Now().UTC()
	result := make([]RechargeBonusCampaign, 0, len(rows))
	for _, row := range rows {
		result = append(result, *rechargeBonusCampaignFromEntity(row, now))
	}
	return result, nil
}

// UpdateCampaign 更新预约活动，或提前结束已经开始的活动。
func (s *RechargeBonusService) UpdateCampaign(ctx context.Context, id int64, input RechargeBonusCampaignInput) (*RechargeBonusCampaign, error) {
	input = normalizeRechargeBonusCampaignInput(input)
	if err := ValidateRechargeBonusCampaign(input); err != nil {
		return nil, infraerrors.BadRequest("INVALID_RECHARGE_BONUS_CAMPAIGN", err.Error())
	}
	client := rechargeBonusClientFromContext(ctx, s.client)
	current, err := client.RechargeBonusCampaign.Get(ctx, id)
	if dbent.IsNotFound(err) {
		return nil, infraerrors.NotFound("RECHARGE_BONUS_CAMPAIGN_NOT_FOUND", "recharge bonus campaign not found")
	}
	if err != nil {
		return nil, fmt.Errorf("get recharge bonus campaign: %w", err)
	}

	now := time.Now().UTC()
	switch {
	case !now.Before(current.EndAt):
		return nil, infraerrors.Conflict("RECHARGE_BONUS_CAMPAIGN_ENDED", "ended campaign is read-only")
	case !now.Before(current.StartAt):
		if !startedCampaignConfigurationEqual(current, input) || input.EndAt.After(current.EndAt) {
			return nil, infraerrors.Conflict("RECHARGE_BONUS_CAMPAIGN_LOCKED", "started campaign can only end early")
		}
	default:
		if err := s.ensureCampaignDoesNotOverlap(ctx, id, input.StartAt, input.EndAt); err != nil {
			return nil, err
		}
	}

	updated, err := client.RechargeBonusCampaign.UpdateOneID(id).
		SetName(input.Name).
		SetDescription(input.Description).
		SetStartAt(input.StartAt).
		SetEndAt(input.EndAt).
		SetParticipationLimit(input.ParticipationLimit).
		SetTiers(input.Tiers).
		Save(ctx)
	if err != nil {
		if isRechargeBonusOverlapConstraintError(err) {
			return nil, rechargeBonusOverlapError()
		}
		return nil, fmt.Errorf("update recharge bonus campaign: %w", err)
	}
	return rechargeBonusCampaignFromEntity(updated, now), nil
}

// DeleteCampaign 删除尚未开始的充值活动。
func (s *RechargeBonusService) DeleteCampaign(ctx context.Context, id int64) error {
	client := rechargeBonusClientFromContext(ctx, s.client)
	row, err := client.RechargeBonusCampaign.Get(ctx, id)
	if dbent.IsNotFound(err) {
		return infraerrors.NotFound("RECHARGE_BONUS_CAMPAIGN_NOT_FOUND", "recharge bonus campaign not found")
	}
	if err != nil {
		return fmt.Errorf("get recharge bonus campaign: %w", err)
	}
	if !time.Now().UTC().Before(row.StartAt) {
		return infraerrors.Conflict("RECHARGE_BONUS_CAMPAIGN_LOCKED", "started campaign cannot be deleted")
	}
	if err := client.RechargeBonusCampaign.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("delete recharge bonus campaign: %w", err)
	}
	return nil
}

func (s *RechargeBonusService) ensureCampaignDoesNotOverlap(ctx context.Context, excludeID int64, startAt, endAt time.Time) error {
	client := rechargeBonusClientFromContext(ctx, s.client)
	query := client.RechargeBonusCampaign.Query().Where(
		dbcampaign.StartAtLT(endAt),
		dbcampaign.EndAtGT(startAt),
	)
	if excludeID > 0 {
		query = query.Where(dbcampaign.IDNEQ(excludeID))
	}
	exists, err := query.Exist(ctx)
	if err != nil {
		return fmt.Errorf("check recharge bonus campaign overlap: %w", err)
	}
	if exists {
		return rechargeBonusOverlapError()
	}
	return nil
}

func normalizeRechargeBonusCampaignInput(input RechargeBonusCampaignInput) RechargeBonusCampaignInput {
	input.Name = strings.TrimSpace(input.Name)
	input.StartAt = input.StartAt.UTC()
	input.EndAt = input.EndAt.UTC()
	input.Tiers = append([]RechargeBonusTier(nil), input.Tiers...)
	sort.SliceStable(input.Tiers, func(i, j int) bool {
		return input.Tiers[i].MinAmount < input.Tiers[j].MinAmount
	})
	return input
}

func rechargeBonusOverlapError() error {
	return infraerrors.Conflict("RECHARGE_BONUS_CAMPAIGN_OVERLAP", "recharge bonus campaign time range overlaps another campaign")
}

func isRechargeBonusOverlapConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "recharge_bonus_campaigns_no_overlap") ||
		strings.Contains(message, "conflicting key value violates exclusion constraint")
}

func rechargeBonusCampaignFromEntity(row *dbent.RechargeBonusCampaign, now time.Time) *RechargeBonusCampaign {
	if row == nil {
		return nil
	}
	status := RechargeBonusCampaignStatusEnded
	if now.Before(row.StartAt) {
		status = RechargeBonusCampaignStatusScheduled
	} else if now.Before(row.EndAt) {
		status = RechargeBonusCampaignStatusActive
	}
	return &RechargeBonusCampaign{
		ID:                 row.ID,
		Name:               row.Name,
		Description:        row.Description,
		StartAt:            row.StartAt,
		EndAt:              row.EndAt,
		ParticipationLimit: row.ParticipationLimit,
		Tiers:              append([]RechargeBonusTier(nil), row.Tiers...),
		Status:             status,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}

func startedCampaignConfigurationEqual(current *dbent.RechargeBonusCampaign, input RechargeBonusCampaignInput) bool {
	return current.Name == input.Name &&
		current.Description == input.Description &&
		current.StartAt.Equal(input.StartAt) &&
		current.ParticipationLimit == input.ParticipationLimit &&
		reflect.DeepEqual(current.Tiers, input.Tiers)
}
