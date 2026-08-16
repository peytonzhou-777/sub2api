package service

import dbent "github.com/Wei-Shaw/sub2api/ent"

// AdminServiceDependencies 使用具名字段装配管理服务，避免长位置参数在测试和手工装配中错位。
// NewAdminService 保留为上游兼容入口；新增代码应优先使用本结构。
type AdminServiceDependencies struct {
	UserRepository                    UserRepository
	GroupRepository                   AdminGroupRepository
	AccountRepository                 AdminAccountRepository
	ProxyRepository                   ProxyRepository
	APIKeyRepository                  APIKeyRepository
	RedeemCodeRepository              RedeemCodeRepository
	UserGroupRateRepository           UserGroupRateRepository
	UserRPMCache                      UserRPMCache
	BillingCacheService               *BillingCacheService
	ProxyProber                       ProxyExitInfoProber
	ProxyLatencyCache                 ProxyLatencyCache
	AuthCacheInvalidator              APIKeyAuthCacheInvalidator
	EntClient                         *dbent.Client
	SettingService                    *SettingService
	DefaultSubscriptionAssigner       DefaultSubscriptionAssigner
	DefaultLimitedCreditGranter       *LimitedCreditService
	UserSubscriptionRepository        UserSubscriptionRepository
	PrivacyClientFactory              PrivacyClientFactory
	RuntimeBlocker                    AccountRuntimeBlocker
	AffiliateService                  *AffiliateService
	CompositeRouteRepository          CompositeModelRouteRepository
	CompositeResolver                 *CompositeRouteResolver
	ChannelCacheInvalidator           ChannelCacheInvalidator
	SecurityDepositGroupKeyReconciler SecurityDepositGroupKeyReconciler
}

// NewAdminServiceWithDependencies 通过具名依赖构造管理服务，并注入跨领域的窄接口能力。
func NewAdminServiceWithDependencies(deps AdminServiceDependencies) AdminService {
	svc := NewAdminService(
		deps.UserRepository,
		deps.GroupRepository,
		deps.AccountRepository,
		deps.ProxyRepository,
		deps.APIKeyRepository,
		deps.RedeemCodeRepository,
		deps.UserGroupRateRepository,
		deps.UserRPMCache,
		deps.BillingCacheService,
		deps.ProxyProber,
		deps.ProxyLatencyCache,
		deps.AuthCacheInvalidator,
		deps.EntClient,
		deps.SettingService,
		deps.DefaultSubscriptionAssigner,
		deps.DefaultLimitedCreditGranter,
		deps.UserSubscriptionRepository,
		deps.PrivacyClientFactory,
		deps.RuntimeBlocker,
		deps.AffiliateService,
		deps.CompositeRouteRepository,
		deps.CompositeResolver,
		deps.ChannelCacheInvalidator,
	)
	impl := svc.(*adminServiceImpl)
	impl.securityDepositGroupKeyReconciler = deps.SecurityDepositGroupKeyReconciler
	return svc
}
