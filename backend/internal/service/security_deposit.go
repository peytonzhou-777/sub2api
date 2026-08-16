package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const defaultSecurityDepositMaxRiskMultiplier int64 = 8

// SecurityDepositAccountRecord 表示单个资金桶的持久化汇总。
type SecurityDepositAccountRecord struct {
	BucketType          string
	BalanceCents        int64
	RefundReservedCents int64
	Version             int64
}

// SecurityDepositLot 表示一笔保证金来源批次。
type SecurityDepositLot struct {
	ID                    int64      `json:"id"`
	BucketType            string     `json:"bucket_type"`
	SourceType            string     `json:"source_type"`
	PaymentOrderID        *int64     `json:"payment_order_id,omitempty"`
	OriginalCents         int64      `json:"original_cents"`
	RemainingCents        int64      `json:"remaining_cents"`
	RefundReservedCents   int64      `json:"refund_reserved_cents"`
	ForfeitedCents        int64      `json:"forfeited_cents"`
	RefundedCents         int64      `json:"refunded_cents"`
	AdminDeductedCents    int64      `json:"admin_deducted_cents"`
	RevokedCents          int64      `json:"revoked_cents"`
	Currency              string     `json:"currency"`
	LockedUntil           *time.Time `json:"locked_until"`
	RefundPolicy          string     `json:"refund_policy"`
	Status                string     `json:"status"`
	CreatedAt             time.Time  `json:"created_at"`
	RefundEligible        bool       `json:"refund_eligible"`
	SelfRefundEligible    bool       `json:"self_refund_eligible"`
	RefundBlockReason     string     `json:"refund_block_reason,omitempty"`
	AdminActionRequired   bool       `json:"admin_action_required"`
	ProviderRefundEnabled bool       `json:"provider_refund_enabled"`
	ProviderUserRefund    bool       `json:"provider_user_refund_enabled"`
}

// SecurityDepositLedgerEntry 是管理员可见的分桶资金流水。
type SecurityDepositLedgerEntry struct {
	ID                       int64     `json:"id"`
	LotID                    int64     `json:"lot_id"`
	BucketType               string    `json:"bucket_type"`
	EntryType                string    `json:"entry_type"`
	DeltaCents               int64     `json:"delta_cents"`
	ReservedDeltaCents       int64     `json:"reserved_delta_cents"`
	BucketBalanceAfterCents  int64     `json:"bucket_balance_after_cents"`
	BucketReservedAfterCents int64     `json:"bucket_reserved_after_cents"`
	Reason                   *string   `json:"reason,omitempty"`
	CreatedAt                time.Time `json:"created_at"`
}

// SecurityDepositRefundView 是管理员只读退款记录。
type SecurityDepositRefundView struct {
	ID             int64      `json:"id"`
	RefundID       string     `json:"refund_id"`
	LotID          int64      `json:"lot_id"`
	PrincipalCents int64      `json:"principal_cents"`
	Mode           string     `json:"mode"`
	State          string     `json:"state"`
	Reason         *string    `json:"reason,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	CompletedAt    *time.Time `json:"completed_at"`
}

// SecurityDepositViolationView 是不包含原始提示词的官方网安事件摘要。
type SecurityDepositViolationView struct {
	ID                    int64      `json:"id"`
	EventKey              string     `json:"event_key"`
	RequestID             string     `json:"request_id"`
	APIKeyID              int64      `json:"api_key_id"`
	GroupID               int64      `json:"group_id"`
	PolicyCode            string     `json:"policy_code"`
	RiskMultiplierBefore  int64      `json:"risk_multiplier_before"`
	RiskMultiplierAfter   int64      `json:"risk_multiplier_after"`
	RequiredSnapshotCents int64      `json:"required_snapshot_cents"`
	ForfeitedCents        int64      `json:"forfeited_cents"`
	ShortfallCents        int64      `json:"shortfall_cents"`
	State                 string     `json:"state"`
	APIKeyNameSnapshot    string     `json:"api_key_name_snapshot"`
	GroupNameSnapshot     string     `json:"group_name_snapshot"`
	CreatedAt             time.Time  `json:"created_at"`
	ProcessedAt           *time.Time `json:"processed_at"`
}

// SecurityDepositUserData 是仓储返回的账户聚合原料。
type SecurityDepositUserData struct {
	Accounts         []SecurityDepositAccountRecord
	Lots             []SecurityDepositLot
	CyberStrikeCount int64
	RiskMultiplier   int64
}

// SecurityDepositAccountSummary 是用户和管理员共享的保证金汇总口径。
type SecurityDepositAccountSummary struct {
	Currency                string               `json:"currency"`
	PaidBalanceCents        int64                `json:"paid_balance_cents"`
	AdminGrantBalanceCents  int64                `json:"admin_grant_balance_cents"`
	TotalBalanceCents       int64                `json:"total_balance_cents"`
	EffectiveBalanceCents   int64                `json:"effective_balance_cents"`
	TimedLockedCents        int64                `json:"timed_locked_cents"`
	PermanentLockedCents    int64                `json:"permanent_locked_cents"`
	RefundableCents         int64                `json:"refundable_cents"`
	PaidRefundReservedCents int64                `json:"paid_refund_reserved_cents"`
	CyberStrikeCount        int64                `json:"cyber_strike_count"`
	RiskMultiplier          int64                `json:"risk_multiplier"`
	MaxRiskMultiplier       int64                `json:"max_risk_multiplier"`
	NextUnlockAt            *time.Time           `json:"next_unlock_at"`
	EnforcementEnabled      bool                 `json:"enforcement_enabled"`
	SelfRefundEnabled       bool                 `json:"self_refund_enabled"`
	Lots                    []SecurityDepositLot `json:"lots"`
}

// SecurityDepositAgreement 是客户端展示的当前版本化保证金约定。
type SecurityDepositAgreement struct {
	Version     string `json:"version"`
	ContentHash string `json:"content_hash"`
	ContentZH   string `json:"content_zh"`
	ContentEN   string `json:"content_en"`
	FreezeHours int    `json:"freeze_hours"`
}

// SecurityDepositEligibility 返回单个分组的权威门槛和短缺报价。
type SecurityDepositEligibility struct {
	GroupID               int64  `json:"group_id"`
	GroupName             string `json:"group_name"`
	Currency              string `json:"currency"`
	BaseRequiredCents     int64  `json:"base_required_cents"`
	RiskMultiplier        int64  `json:"risk_multiplier"`
	RequiredCents         int64  `json:"required_cents"`
	EffectiveBalanceCents int64  `json:"effective_balance_cents"`
	ShortfallCents        int64  `json:"shortfall_cents"`
	Eligible              bool   `json:"eligible"`
	AgreementRequired     bool   `json:"agreement_required"`
	PolicyVersion         string `json:"policy_version"`
	ContentHash           string `json:"content_hash"`
	QuoteHash             string `json:"quote_hash"`
}

// SecurityDepositAccessGrant 是一次服务端准入检查产生的可信身份与准入快照。
// 处罚事务会据此定位用户和分组，并按处罚时的最新基础门槛与风险倍率结算。
type SecurityDepositAccessGrant struct {
	UserID                int64  `json:"user_id"`
	GroupID               int64  `json:"group_id"`
	BaseRequiredCents     int64  `json:"base_required_cents"`
	RiskMultiplier        int64  `json:"risk_multiplier"`
	RequiredCents         int64  `json:"required_cents"`
	EffectiveBalanceCents int64  `json:"effective_balance_cents"`
	PolicyVersion         string `json:"policy_version"`
	AccessVersion         string `json:"access_version"`
	Enforced              bool   `json:"enforced"`
}

// SecurityDepositAgreementAcceptance 是服务端持久化的协议接受事实。
type SecurityDepositAgreementAcceptance struct {
	ID                        int64
	UserID                    int64
	GroupID                   int64
	PolicyVersion             string
	ContentHash               string
	BaseRequiredSnapshotCents int64
	RiskMultiplierSnapshot    int64
	RequiredSnapshotCents     int64
	AcceptedAt                time.Time
	ClientIP                  string
	UserAgent                 string
}

// CreateSecurityDepositOrderRequest 不包含金额，金额始终从最新资格报价计算。
type CreateSecurityDepositOrderRequest struct {
	UserID            int64
	GroupID           int64
	PolicyVersion     string
	ContentHash       string
	QuoteHash         string
	PaymentType       string
	OpenID            string
	WechatResumeToken string
	ClientIP          string
	UserAgent         string
	IsMobile          bool
	IsWeChatBrowser   bool
	SrcHost           string
	SrcURL            string
	ReturnURL         string
	PaymentSource     string
	Locale            string
}

// CreateSecurityDepositOrderResult 同时覆盖并发下已足额和创建支付订单两种结果。
type CreateSecurityDepositOrderResult struct {
	Satisfied   bool                        `json:"satisfied"`
	Eligibility *SecurityDepositEligibility `json:"eligibility"`
	Payment     *CreateOrderResponse        `json:"payment,omitempty"`
}

// AdminSecurityDepositUserSummary 是管理员列表中的用户保证金摘要。
type AdminSecurityDepositUserSummary struct {
	UserID                  int64      `json:"user_id"`
	Email                   string     `json:"email"`
	Username                string     `json:"username"`
	Status                  string     `json:"status"`
	PaidBalanceCents        int64      `json:"paid_balance_cents"`
	AdminGrantBalanceCents  int64      `json:"admin_grant_balance_cents"`
	TotalBalanceCents       int64      `json:"total_balance_cents"`
	EffectiveBalanceCents   int64      `json:"effective_balance_cents"`
	TimedLockedCents        int64      `json:"timed_locked_cents"`
	PermanentLockedCents    int64      `json:"permanent_locked_cents"`
	RefundableCents         int64      `json:"refundable_cents"`
	PaidRefundReservedCents int64      `json:"paid_refund_reserved_cents"`
	RiskMultiplier          int64      `json:"risk_multiplier"`
	CyberStrikeCount        int64      `json:"cyber_strike_count"`
	LastViolationAt         *time.Time `json:"last_violation_at"`
}

// AdminSecurityDepositUserDetail 汇总管理员复核所需的只读证据链。
type AdminSecurityDepositUserDetail struct {
	User       AdminSecurityDepositUserSummary `json:"user"`
	Account    SecurityDepositAccountSummary   `json:"account"`
	Ledger     []SecurityDepositLedgerEntry    `json:"ledger"`
	Refunds    []SecurityDepositRefundView     `json:"refunds"`
	Violations []SecurityDepositViolationView  `json:"violations"`
}

// SecurityDepositRepository 定义第一阶段只读仓储接缝。
type SecurityDepositRepository interface {
	GetUserData(ctx context.Context, userID int64) (*SecurityDepositUserData, error)
	ListAdminUsers(ctx context.Context, page, pageSize int, search string) ([]AdminSecurityDepositUserSummary, int64, error)
	GetAdminUser(ctx context.Context, userID int64) (*AdminSecurityDepositUserSummary, error)
	ListLedger(ctx context.Context, userID int64, limit int) ([]SecurityDepositLedgerEntry, error)
	ListRefunds(ctx context.Context, userID int64, limit int) ([]SecurityDepositRefundView, error)
	ListViolations(ctx context.Context, userID int64, limit int) ([]SecurityDepositViolationView, error)
	HasAcceptedAgreement(ctx context.Context, userID, groupID int64, policyVersion, contentHash string) (bool, error)
	AcceptAgreement(ctx context.Context, acceptance SecurityDepositAgreementAcceptance) (*SecurityDepositAgreementAcceptance, error)
}

type securityDepositGroupAccess interface {
	GetAvailableGroups(ctx context.Context, userID int64) ([]Group, error)
}

type securityDepositPaymentCreator interface {
	CreateOrder(ctx context.Context, req CreateOrderRequest) (*CreateOrderResponse, error)
	ParseWeChatPaymentResumeToken(token string) (*WeChatPaymentResumeClaims, error)
}

// SecurityDepositService 提供保证金汇总、资格报价、协议接受和权威下单。
type SecurityDepositService struct {
	repo                 SecurityDepositRepository
	groupAccess          securityDepositGroupAccess
	paymentCreator       securityDepositPaymentCreator
	settings             *SettingService
	now                  func() time.Time
	keyEligibility       SecurityDepositKeyChangeReconciler
	authCacheInvalidator APIKeyAuthCacheInvalidator
}

// SetKeyEligibilityReconciler 注入资金事件后的统一密钥资格重算器。
func (s *SecurityDepositService) SetKeyEligibilityReconciler(reconciler SecurityDepositKeyChangeReconciler) {
	s.keyEligibility = reconciler
}

// reconcileKeysAfterBalanceChange 对每次保证金变化执行只禁用重算，不自动恢复任何密钥。
func (s *SecurityDepositService) reconcileKeysAfterBalanceChange(ctx context.Context, userID int64, eventType string, eventID int64, alreadyDisabled []int64) ([]int64, error) {
	if s == nil || s.keyEligibility == nil {
		return append([]int64(nil), alreadyDisabled...), nil
	}
	disabled, err := s.keyEligibility.DisableInsufficientKeys(ctx, userID, eventType, eventID)
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{}, len(alreadyDisabled)+len(disabled))
	merged := make([]int64, 0, len(alreadyDisabled)+len(disabled))
	for _, key := range append(append([]int64(nil), alreadyDisabled...), securityDepositKeyIDs(disabled)...) {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, key)
	}
	return merged, nil
}

func securityDepositKeyIDs(keys []SecurityDepositKeyReference) []int64 {
	ids := make([]int64, 0, len(keys))
	for _, key := range keys {
		ids = append(ids, key.ID)
	}
	return ids
}

// SetPenaltyDependencies 注入处罚提交后的认证缓存失效能力。
func (s *SecurityDepositService) SetPenaltyDependencies(invalidator APIKeyAuthCacheInvalidator) {
	s.authCacheInvalidator = invalidator
}

// DisableInsufficientKeys 在退款预留、管理员扣除等资金事件事务内禁用不足密钥。
func (s *SecurityDepositService) DisableInsufficientKeys(ctx context.Context, userID int64, eventType string, eventID int64) ([]SecurityDepositKeyReference, error) {
	if s == nil || s.keyEligibility == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_RECONCILER_UNAVAILABLE", "security deposit key eligibility reconciler is unavailable")
	}
	return s.keyEligibility.DisableInsufficientKeys(ctx, userID, eventType, eventID)
}

// NewSecurityDepositService 创建保证金领域服务。
func NewSecurityDepositService(repo SecurityDepositRepository) *SecurityDepositService {
	return &SecurityDepositService{repo: repo, now: time.Now}
}

// SetOrderDependencies 注入第二阶段下单依赖，保持只读单测和管理查询可独立构造。
func (s *SecurityDepositService) SetOrderDependencies(groupAccess securityDepositGroupAccess, paymentCreator securityDepositPaymentCreator, settings *SettingService) {
	s.groupAccess = groupAccess
	s.paymentCreator = paymentCreator
	s.settings = settings
}

// GetAccount 返回用户保证金账户，缺少账户行时按零余额和 1 倍风险档案处理。
func (s *SecurityDepositService) GetAccount(ctx context.Context, userID int64) (*SecurityDepositAccountSummary, error) {
	data, err := s.repo.GetUserData(ctx, userID)
	if err != nil {
		return nil, err
	}
	summary := buildSecurityDepositSummary(data, s.now().UTC())
	policy := s.securityDepositPolicy(ctx)
	summary.MaxRiskMultiplier = policy.MaxRiskMultiplier
	summary.EnforcementEnabled = policy.EnforcementEnabled
	summary.SelfRefundEnabled = policy.SelfRefundEnabled
	s.enrichSecurityDepositSelfRefundCapabilities(ctx, summary, policy.SelfRefundEnabled)
	return summary, nil
}

// enrichSecurityDepositSelfRefundCapabilities 合并平台开关与原支付实例当前退款能力。
func (s *SecurityDepositService) enrichSecurityDepositSelfRefundCapabilities(ctx context.Context, summary *SecurityDepositAccountSummary, selfRefundEnabled bool) {
	if summary == nil {
		return
	}
	reader, _ := s.paymentCreator.(securityDepositRefundCapabilityReader)
	for i := range summary.Lots {
		lot := &summary.Lots[i]
		if !lot.RefundEligible || lot.PaymentOrderID == nil {
			continue
		}
		if reader == nil {
			if selfRefundEnabled {
				lot.RefundBlockReason = "refund_route_unavailable"
			} else {
				lot.RefundBlockReason = "self_refund_disabled"
			}
			lot.AdminActionRequired = true
			continue
		}
		capability, err := reader.GetSecurityDepositRefundCapability(ctx, *lot.PaymentOrderID)
		if err != nil {
			lot.RefundBlockReason = "refund_route_unavailable"
			lot.AdminActionRequired = true
			continue
		}
		lot.ProviderRefundEnabled = capability.RefundEnabled
		lot.ProviderUserRefund = capability.AllowUserRefund
		switch {
		case !selfRefundEnabled:
			lot.RefundBlockReason = "self_refund_disabled"
			lot.AdminActionRequired = true
		case !capability.RefundEnabled:
			lot.RefundBlockReason = "provider_refund_disabled"
			lot.AdminActionRequired = true
		case !capability.AllowUserRefund:
			lot.RefundBlockReason = "provider_user_refund_disabled"
			lot.AdminActionRequired = true
		default:
			lot.SelfRefundEligible = true
			lot.RefundBlockReason = ""
			lot.AdminActionRequired = false
		}
	}
}

// GetAgreement 返回全局或指定分组当前生效的协议版本与内容。
func (s *SecurityDepositService) GetAgreement(ctx context.Context, userID, groupID int64) (*SecurityDepositAgreement, error) {
	policy := s.securityDepositPolicy(ctx)
	version := policy.Version
	if groupID > 0 {
		group, err := s.getAvailableGroup(ctx, userID, groupID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(group.SecurityDepositPolicyVersion) != "" {
			version = strings.TrimSpace(group.SecurityDepositPolicyVersion)
		}
	}
	return &SecurityDepositAgreement{
		Version: version, ContentHash: policy.ContentHash, ContentZH: policy.ContentZH,
		ContentEN: policy.ContentEN, FreezeHours: policy.FreezeHours,
	}, nil
}

// GetEligibility 按用户现有分组权限、风险倍率和有效余额计算准确差额。
func (s *SecurityDepositService) GetEligibility(ctx context.Context, userID, groupID int64) (*SecurityDepositEligibility, error) {
	if groupID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_GROUP", "group_id must be positive")
	}
	group, err := s.getAvailableGroup(ctx, userID, groupID)
	if err != nil {
		return nil, err
	}
	account, err := s.GetAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	agreement, err := s.GetAgreement(ctx, userID, groupID)
	if err != nil {
		return nil, err
	}
	required, err := multiplySecurityDepositThreshold(group.SecurityDepositBaseRequiredCents, account.RiskMultiplier)
	if err != nil {
		return nil, err
	}
	shortfall := required - account.EffectiveBalanceCents
	if shortfall < 0 {
		shortfall = 0
	}
	accepted, err := s.repo.HasAcceptedAgreement(ctx, userID, groupID, agreement.Version, agreement.ContentHash)
	if err != nil {
		return nil, fmt.Errorf("check security deposit agreement: %w", err)
	}
	eligibility := &SecurityDepositEligibility{
		GroupID: group.ID, GroupName: group.Name, Currency: "CNY",
		BaseRequiredCents: group.SecurityDepositBaseRequiredCents, RiskMultiplier: account.RiskMultiplier,
		RequiredCents: required, EffectiveBalanceCents: account.EffectiveBalanceCents,
		ShortfallCents: shortfall, Eligible: shortfall == 0,
		AgreementRequired: group.SecurityDepositBaseRequiredCents > 0 && !accepted,
		PolicyVersion:     agreement.Version, ContentHash: agreement.ContentHash,
	}
	eligibility.QuoteHash = securityDepositQuoteHash(userID, eligibility)
	return eligibility, nil
}

// GetAccessSnapshot 读取处罚和准入共用的最新保证金快照，不因当前余额不足而丢弃证据。
// 门槛为 0 或全局执法关闭时保持存量行为，不读取保证金账户。
func (s *SecurityDepositService) GetAccessSnapshot(ctx context.Context, userID, groupID int64) (*SecurityDepositAccessGrant, error) {
	if userID <= 0 || groupID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_ACCESS", "user_id and group_id must be positive")
	}
	group, err := s.getAvailableGroup(ctx, userID, groupID)
	if err != nil {
		return nil, err
	}
	policy, err := s.securityDepositPolicyStrict(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable(
			"SECURITY_DEPOSIT_STATUS_UNAVAILABLE",
			"security deposit policy is temporarily unavailable",
		).WithCause(err)
	}
	policyVersion := policy.Version
	if value := strings.TrimSpace(group.SecurityDepositPolicyVersion); value != "" {
		policyVersion = value
	}
	grant := &SecurityDepositAccessGrant{
		UserID: userID, GroupID: group.ID, BaseRequiredCents: group.SecurityDepositBaseRequiredCents,
		RiskMultiplier: 1, PolicyVersion: policyVersion,
		Enforced: policy.EnforcementEnabled && group.SecurityDepositBaseRequiredCents > 0,
	}
	if !grant.Enforced {
		grant.RequiredCents = group.SecurityDepositBaseRequiredCents
		grant.AccessVersion = securityDepositAccessVersion(grant, nil)
		return grant, nil
	}
	account, err := s.GetAccount(ctx, userID)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable(
			"SECURITY_DEPOSIT_STATUS_UNAVAILABLE",
			"security deposit status is temporarily unavailable",
		).WithCause(err)
	}
	grant.RiskMultiplier = account.RiskMultiplier
	grant.RequiredCents, err = multiplySecurityDepositThreshold(grant.BaseRequiredCents, grant.RiskMultiplier)
	if err != nil {
		return nil, err
	}
	grant.EffectiveBalanceCents = account.EffectiveBalanceCents
	grant.AccessVersion = securityDepositAccessVersion(grant, account)
	return grant, nil
}

// CheckAccess 按最新分组、双资金桶和风险档案执行权威准入检查。
func (s *SecurityDepositService) CheckAccess(ctx context.Context, userID, groupID int64) (*SecurityDepositAccessGrant, error) {
	grant, err := s.GetAccessSnapshot(ctx, userID, groupID)
	if err != nil {
		return nil, err
	}
	if !grant.Enforced {
		return grant, nil
	}
	if grant.EffectiveBalanceCents >= grant.RequiredCents {
		return grant, nil
	}
	shortfall := grant.RequiredCents - grant.EffectiveBalanceCents
	return nil, infraerrors.Forbidden(
		"SECURITY_DEPOSIT_REQUIRED",
		"security deposit is insufficient for this group",
	).WithMetadata(map[string]string{
		"group_id":                fmt.Sprintf("%d", grant.GroupID),
		"base_required_cents":     fmt.Sprintf("%d", grant.BaseRequiredCents),
		"risk_multiplier":         fmt.Sprintf("%d", grant.RiskMultiplier),
		"required_cents":          fmt.Sprintf("%d", grant.RequiredCents),
		"effective_balance_cents": fmt.Sprintf("%d", grant.EffectiveBalanceCents),
		"shortfall_cents":         fmt.Sprintf("%d", shortfall),
		"policy_version":          grant.PolicyVersion,
		"access_version":          grant.AccessVersion,
	})
}

// CreateOrder 接受当前协议并按服务端最新短缺创建保证金支付订单。
func (s *SecurityDepositService) CreateOrder(ctx context.Context, req CreateSecurityDepositOrderRequest) (*CreateSecurityDepositOrderResult, error) {
	if s.paymentCreator == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_PAYMENT_UNAVAILABLE", "security deposit payment is unavailable")
	}
	eligibility, err := s.GetEligibility(ctx, req.UserID, req.GroupID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.PolicyVersion) != eligibility.PolicyVersion || strings.TrimSpace(req.ContentHash) != eligibility.ContentHash {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_AGREEMENT_OUTDATED", "security deposit agreement has changed")
	}
	if strings.TrimSpace(req.QuoteHash) != eligibility.QuoteHash {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_QUOTE_CHANGED", "security deposit quote has changed").WithMetadata(securityDepositEligibilityMetadata(eligibility))
	}
	acceptedAt := s.now().UTC()
	acceptance, err := s.repo.AcceptAgreement(ctx, SecurityDepositAgreementAcceptance{
		UserID: req.UserID, GroupID: req.GroupID, PolicyVersion: eligibility.PolicyVersion,
		ContentHash: eligibility.ContentHash, BaseRequiredSnapshotCents: eligibility.BaseRequiredCents,
		RiskMultiplierSnapshot: eligibility.RiskMultiplier, RequiredSnapshotCents: eligibility.RequiredCents,
		AcceptedAt: acceptedAt, ClientIP: strings.TrimSpace(req.ClientIP), UserAgent: strings.TrimSpace(req.UserAgent),
	})
	if err != nil {
		return nil, fmt.Errorf("accept security deposit agreement: %w", err)
	}
	if eligibility.ShortfallCents == 0 {
		eligibility.AgreementRequired = false
		return &CreateSecurityDepositOrderResult{Satisfied: true, Eligibility: eligibility}, nil
	}
	if strings.TrimSpace(req.WechatResumeToken) != "" {
		claims, parseErr := s.paymentCreator.ParseWeChatPaymentResumeToken(req.WechatResumeToken)
		if parseErr != nil {
			return nil, parseErr
		}
		if req.PaymentType != "" && NormalizeVisibleMethod(req.PaymentType) != NormalizeVisibleMethod(claims.PaymentType) {
			return nil, infraerrors.BadRequest("INVALID_WECHAT_PAYMENT_RESUME_TOKEN", "wechat payment resume token payment type mismatch")
		}
		req.PaymentType = claims.PaymentType
		req.OpenID = claims.OpenID
	}
	group, err := s.getAvailableGroup(ctx, req.UserID, req.GroupID)
	if err != nil {
		return nil, err
	}
	policy := s.securityDepositPolicy(ctx)
	amount := decimal.NewFromInt(eligibility.ShortfallCents).Shift(-2).InexactFloat64()
	paymentResult, err := s.paymentCreator.CreateOrder(ctx, CreateOrderRequest{
		UserID: req.UserID, Amount: amount, PaymentType: req.PaymentType, OpenID: req.OpenID,
		ClientIP: req.ClientIP, IsMobile: req.IsMobile, IsWeChatBrowser: req.IsWeChatBrowser,
		SrcHost: req.SrcHost, SrcURL: req.SrcURL, ReturnURL: req.ReturnURL,
		PaymentSource: req.PaymentSource, OrderType: payment.OrderTypeSecurityDeposit, Locale: req.Locale,
		SecurityDeposit: &SecurityDepositOrderSnapshot{
			SchemaVersion: 1, GroupID: group.ID, GroupName: group.Name, AgreementID: acceptance.ID,
			PolicyVersion: eligibility.PolicyVersion, ContentHash: eligibility.ContentHash,
			BaseRequiredCents: eligibility.BaseRequiredCents, RiskMultiplier: eligibility.RiskMultiplier,
			RequiredCents: eligibility.RequiredCents, EffectiveBalanceBeforeCents: eligibility.EffectiveBalanceCents,
			PrincipalCents: eligibility.ShortfallCents, FreezeHours: policy.FreezeHours, Currency: "CNY",
		},
	})
	if err != nil {
		return nil, err
	}
	eligibility.AgreementRequired = false
	return &CreateSecurityDepositOrderResult{Satisfied: false, Eligibility: eligibility, Payment: paymentResult}, nil
}

func (s *SecurityDepositService) getAvailableGroup(ctx context.Context, userID, groupID int64) (*Group, error) {
	if s.groupAccess == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_GROUP_ACCESS_UNAVAILABLE", "group access is unavailable")
	}
	groups, err := s.groupAccess.GetAvailableGroups(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list available groups: %w", err)
	}
	for i := range groups {
		if groups[i].ID == groupID {
			return &groups[i], nil
		}
	}
	return nil, infraerrors.Forbidden("GROUP_NOT_ALLOWED", "group is not available to this user")
}

func (s *SecurityDepositService) securityDepositPolicy(ctx context.Context) SecurityDepositPolicyConfig {
	if s.settings == nil {
		var empty SettingService
		return empty.GetSecurityDepositPolicyConfig(ctx)
	}
	return s.settings.GetSecurityDepositPolicyConfig(ctx)
}

func (s *SecurityDepositService) securityDepositPolicyStrict(ctx context.Context) (SecurityDepositPolicyConfig, error) {
	if s.settings == nil {
		var empty SettingService
		return empty.GetSecurityDepositPolicyConfigStrict(ctx)
	}
	return s.settings.GetSecurityDepositPolicyConfigStrict(ctx)
}

func multiplySecurityDepositThreshold(base, multiplier int64) (int64, error) {
	if base < 0 || multiplier < 1 || (base > 0 && multiplier > math.MaxInt64/base) {
		return 0, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_THRESHOLD", "security deposit threshold is invalid")
	}
	return base * multiplier, nil
}

func securityDepositQuoteHash(userID int64, eligibility *SecurityDepositEligibility) string {
	canonical := fmt.Sprintf("v1|%d|%d|%d|%d|%d|%d|%s|%s", userID, eligibility.GroupID,
		eligibility.BaseRequiredCents, eligibility.RiskMultiplier, eligibility.RequiredCents,
		eligibility.EffectiveBalanceCents, eligibility.PolicyVersion, eligibility.ContentHash)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func securityDepositAccessVersion(grant *SecurityDepositAccessGrant, account *SecurityDepositAccountSummary) string {
	paidReserved := int64(0)
	cyberStrikeCount := int64(0)
	if account != nil {
		paidReserved = account.PaidRefundReservedCents
		cyberStrikeCount = account.CyberStrikeCount
	}
	canonical := fmt.Sprintf("v1|%d|%d|%d|%d|%d|%d|%d|%s", grant.UserID, grant.GroupID,
		grant.BaseRequiredCents, grant.RiskMultiplier, grant.RequiredCents,
		grant.EffectiveBalanceCents, paidReserved+cyberStrikeCount, grant.PolicyVersion)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func securityDepositEligibilityMetadata(eligibility *SecurityDepositEligibility) map[string]string {
	return map[string]string{
		"group_id":                fmt.Sprintf("%d", eligibility.GroupID),
		"base_required_cents":     fmt.Sprintf("%d", eligibility.BaseRequiredCents),
		"risk_multiplier":         fmt.Sprintf("%d", eligibility.RiskMultiplier),
		"required_cents":          fmt.Sprintf("%d", eligibility.RequiredCents),
		"effective_balance_cents": fmt.Sprintf("%d", eligibility.EffectiveBalanceCents),
		"shortfall_cents":         fmt.Sprintf("%d", eligibility.ShortfallCents),
		"quote_hash":              eligibility.QuoteHash,
	}
}

// ListAdminUsers 分页返回管理员保证金用户汇总。
func (s *SecurityDepositService) ListAdminUsers(ctx context.Context, page, pageSize int, search string) ([]AdminSecurityDepositUserSummary, int64, error) {
	return s.repo.ListAdminUsers(ctx, page, pageSize, search)
}

// GetAdminUserDetail 返回管理员复核所需的账户、流水、退款和处罚摘要。
func (s *SecurityDepositService) GetAdminUserDetail(ctx context.Context, userID int64) (*AdminSecurityDepositUserDetail, error) {
	user, err := s.repo.GetAdminUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, infraerrors.NotFound("USER_NOT_FOUND", "user not found")
	}
	account, err := s.GetAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	ledger, err := s.repo.ListLedger(ctx, userID, 200)
	if err != nil {
		return nil, err
	}
	refunds, err := s.repo.ListRefunds(ctx, userID, 100)
	if err != nil {
		return nil, err
	}
	violations, err := s.repo.ListViolations(ctx, userID, 100)
	if err != nil {
		return nil, err
	}
	return &AdminSecurityDepositUserDetail{User: *user, Account: *account, Ledger: ledger, Refunds: refunds, Violations: violations}, nil
}

func buildSecurityDepositSummary(data *SecurityDepositUserData, now time.Time) *SecurityDepositAccountSummary {
	summary := &SecurityDepositAccountSummary{
		Currency:          "CNY",
		RiskMultiplier:    1,
		MaxRiskMultiplier: defaultSecurityDepositMaxRiskMultiplier,
		Lots:              []SecurityDepositLot{},
	}
	if data == nil {
		return summary
	}
	if data.RiskMultiplier > 0 {
		summary.RiskMultiplier = data.RiskMultiplier
	}
	summary.CyberStrikeCount = data.CyberStrikeCount
	for _, account := range data.Accounts {
		switch account.BucketType {
		case "paid":
			summary.PaidBalanceCents = account.BalanceCents
			summary.PaidRefundReservedCents = account.RefundReservedCents
		case "admin_grant":
			summary.AdminGrantBalanceCents = account.BalanceCents
		}
	}
	summary.TotalBalanceCents = summary.PaidBalanceCents + summary.AdminGrantBalanceCents
	summary.EffectiveBalanceCents = summary.PaidBalanceCents - summary.PaidRefundReservedCents + summary.AdminGrantBalanceCents
	for i := range data.Lots {
		lot := data.Lots[i]
		available := lot.RemainingCents - lot.RefundReservedCents
		if available < 0 {
			available = 0
		}
		switch lot.RefundPolicy {
		case "never":
			summary.PermanentLockedCents += available
			lot.RefundBlockReason = "permanently_non_refundable"
		case "timed_original_channel":
			if lot.LockedUntil != nil && now.Before(lot.LockedUntil.UTC()) {
				summary.TimedLockedCents += available
				lot.RefundBlockReason = "frozen"
				if summary.NextUnlockAt == nil || lot.LockedUntil.Before(*summary.NextUnlockAt) {
					unlockAt := lot.LockedUntil.UTC()
					summary.NextUnlockAt = &unlockAt
				}
			} else if available > 0 {
				summary.RefundableCents += available
				lot.RefundEligible = true
				lot.RefundBlockReason = "self_refund_disabled"
				lot.AdminActionRequired = true
			}
			if lot.RefundReservedCents > 0 {
				lot.RefundEligible = false
				lot.RefundBlockReason = "refund_in_progress"
				lot.AdminActionRequired = false
			}
		}
		// 支付实例能力由服务层在基础资格计算后补充，客户端不得自行推导。
		lot.SelfRefundEligible = false
		data.Lots[i] = lot
	}
	summary.Lots = data.Lots
	return summary
}
