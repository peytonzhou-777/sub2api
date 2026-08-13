package routes

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	basemiddleware "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

// RegisterPaymentRoutes registers all payment-related routes:
// user-facing endpoints, webhook endpoints, and admin endpoints.
func RegisterPaymentRoutes(
	v1 *gin.RouterGroup,
	paymentHandler *handler.PaymentHandler,
	webhookHandler *handler.PaymentWebhookHandler,
	adminPaymentHandler *admin.PaymentHandler,
	jwtAuth middleware.JWTAuthMiddleware,
	adminAuth middleware.AdminAuthMiddleware,
	auditLog middleware.AuditLogMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware.PanelRateLimiter,
	redisClient *redis.Client,
) {
	// --- User-facing payment endpoints (authenticated) ---
	authenticated := v1.Group("/payment")
	authenticated.Use(gin.HandlerFunc(jwtAuth))
	authenticated.Use(middleware.BackendModeUserGuard(settingService))
	// 面板全局按用户限流
	authenticated.Use(panelRateLimiter.Global())
	{
		authenticated.GET("/config", paymentHandler.GetPaymentConfig)
		authenticated.GET("/checkout-info", paymentHandler.GetCheckoutInfo)
		authenticated.GET("/plans", paymentHandler.GetPlans)
		authenticated.GET("/limits", paymentHandler.GetLimits)
		authenticated.GET("/refunds/overview", paymentHandler.GetAccountRefundOverview)
		authenticated.POST("/refunds/lock", paymentHandler.LockAccountRefund)
		authenticated.POST("/refunds/donate", paymentHandler.DonateAccountRefund)

		orders := authenticated.Group("/orders")
		{
			orders.POST("", paymentHandler.CreateOrder)
			orders.POST("/verify", paymentHandler.VerifyOrder)
			orders.GET("/my", paymentHandler.GetMyOrders)
			orders.GET("/:id", paymentHandler.GetOrder)
			orders.POST("/:id/cancel", paymentHandler.CancelOrder)
			orders.GET("/refund-eligible-providers", paymentHandler.GetRefundEligibleProviders)
		}
	}

	// --- Public payment endpoints (no auth) ---
	// Signed resume-token recovery is the preferred public lookup path.
	// The legacy anonymous out_trade_no verify endpoint remains available as a
	// persisted-state compatibility path for staggered upgrades.
	public := v1.Group("/payment/public")
	{
		public.POST("/orders/verify", paymentHandler.VerifyOrderPublic)
		public.POST("/orders/resolve", paymentHandler.ResolveOrderPublicByResumeToken)
		public.GET("/refund-donations", paymentHandler.ListAccountRefundDonations)
	}

	// 账户被锁定后普通 JWT 会失效，退款专用会话只能访问当前清退流程。
	refundSession := v1.Group("/payment/refunds")
	refundSession.Use(panelRateLimiter.Global())
	{
		// 恢复入口与登录同级失败关闭限流，防止成为密码爆破旁路。
		rateLimiter := basemiddleware.NewRateLimiter(redisClient)
		refundSession.POST("/session/restore", rateLimiter.LimitWithOptions("account-refund-session-restore", 10, time.Minute, basemiddleware.RateLimitOptions{
			FailureMode: basemiddleware.RateLimitFailClose,
		}), gin.HandlerFunc(auditLog), paymentHandler.RestoreAccountRefundSession)
		refundSession.GET("/:refund_id", paymentHandler.GetAccountRefund)
		refundSession.POST("/:refund_id/confirm", paymentHandler.ConfirmAccountRefund)
		refundSession.POST("/:refund_id/donate", paymentHandler.DonateLockedAccountRefund)
		refundSession.POST("/:refund_id/cancel", paymentHandler.CancelAccountRefund)
	}

	// --- Webhook endpoints (no auth) ---
	webhook := v1.Group("/payment/webhook")
	{
		// EasyPay sends GET callbacks with query params
		webhook.GET("/easypay", webhookHandler.EasyPayNotify)
		webhook.POST("/easypay", webhookHandler.EasyPayNotify)
		webhook.POST("/alipay", webhookHandler.AlipayNotify)
		webhook.POST("/wxpay", webhookHandler.WxpayNotify)
		webhook.POST("/stripe", webhookHandler.StripeWebhook)
		webhook.POST("/airwallex", webhookHandler.AirwallexWebhook)
	}

	// --- Admin payment endpoints (admin auth) ---
	adminGroup := v1.Group("/admin/payment")
	adminGroup.Use(gin.HandlerFunc(adminAuth))
	adminGroup.Use(gin.HandlerFunc(auditLog))
	adminGroup.Use(middleware.AdminComplianceGuard(settingService))
	{
		// Dashboard
		adminGroup.GET("/dashboard", adminPaymentHandler.GetDashboard)

		// Config
		adminGroup.GET("/config", adminPaymentHandler.GetConfig)
		adminGroup.PUT("/config", adminPaymentHandler.UpdateConfig)

		// Orders
		adminOrders := adminGroup.Group("/orders")
		{
			adminOrders.GET("", adminPaymentHandler.ListOrders)
			adminOrders.GET("/:id", adminPaymentHandler.GetOrderDetail)
			adminOrders.POST("/:id/cancel", adminPaymentHandler.CancelOrder)
			adminOrders.POST("/:id/retry", adminPaymentHandler.RetryFulfillment)
			adminOrders.POST("/:id/refund", adminPaymentHandler.ProcessRefund)
			adminOrders.POST("/:id/refund/query", adminPaymentHandler.QueryAndFinalizeRefund)
		}

		// Subscription Plans
		plans := adminGroup.Group("/plans")
		{
			plans.GET("", adminPaymentHandler.ListPlans)
			plans.POST("", adminPaymentHandler.CreatePlan)
			plans.PUT("/:id", adminPaymentHandler.UpdatePlan)
			plans.DELETE("/:id", adminPaymentHandler.DeletePlan)
		}

		// 充值赠送活动
		rechargeBonusCampaigns := adminGroup.Group("/recharge-bonus-campaigns")
		{
			rechargeBonusCampaigns.GET("", adminPaymentHandler.ListRechargeBonusCampaigns)
			rechargeBonusCampaigns.POST("", adminPaymentHandler.CreateRechargeBonusCampaign)
			rechargeBonusCampaigns.PUT("/:id", adminPaymentHandler.UpdateRechargeBonusCampaign)
			rechargeBonusCampaigns.DELETE("/:id", adminPaymentHandler.DeleteRechargeBonusCampaign)
		}

		// Provider Instances
		providers := adminGroup.Group("/providers")
		{
			providers.GET("", adminPaymentHandler.ListProviders)
			providers.POST("", adminPaymentHandler.CreateProvider)
			providers.PUT("/:id", adminPaymentHandler.UpdateProvider)
			providers.DELETE("/:id", adminPaymentHandler.DeleteProvider)
		}
	}
}
