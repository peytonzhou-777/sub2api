package service

import (
	"context"
	"fmt"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type securityDepositGatewayRefundInternal struct {
	order         *dbent.PaymentOrder
	gatewayAmount float64
}

// PrepareSecurityDepositGatewayRefund 校验原支付实例退款能力并生成稳定网关计划，不修改调用余额或订单状态。
func (s *PaymentService) PrepareSecurityDepositGatewayRefund(ctx context.Context, paymentOrderID, userID, principalCents int64, requestID, reason string) (*SecurityDepositGatewayRefundPlan, error) {
	return s.prepareSecurityDepositGatewayRefund(ctx, paymentOrderID, userID, principalCents, requestID, reason, false)
}

// PrepareUserSecurityDepositGatewayRefund 额外校验支付实例是否允许用户自助退款。
func (s *PaymentService) PrepareUserSecurityDepositGatewayRefund(ctx context.Context, paymentOrderID, userID, principalCents int64, requestID, reason string) (*SecurityDepositGatewayRefundPlan, error) {
	return s.prepareSecurityDepositGatewayRefund(ctx, paymentOrderID, userID, principalCents, requestID, reason, true)
}

func (s *PaymentService) prepareSecurityDepositGatewayRefund(ctx context.Context, paymentOrderID, userID, principalCents int64, requestID, reason string, requireUserRefund bool) (*SecurityDepositGatewayRefundPlan, error) {
	if s == nil || s.entClient == nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_GATEWAY_UNAVAILABLE", "security deposit refund gateway is unavailable")
	}
	if paymentOrderID <= 0 || userID <= 0 || principalCents <= 0 || strings.TrimSpace(requestID) == "" {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND_PLAN", "payment order, user, principal and request id are required")
	}
	order, err := s.entClient.PaymentOrder.Get(ctx, paymentOrderID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("SECURITY_DEPOSIT_PAYMENT_ORDER_NOT_FOUND", "security deposit payment order not found")
		}
		return nil, err
	}
	if order.UserID != userID || order.OrderType != payment.OrderTypeSecurityDeposit {
		return nil, infraerrors.BadRequest("SECURITY_DEPOSIT_PAYMENT_ORDER_MISMATCH", "payment order does not belong to this security deposit lot")
	}
	if order.Status != OrderStatusCompleted {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_PAYMENT_ORDER_NOT_REFUNDABLE", "security deposit payment order is not refundable")
	}
	if strings.TrimSpace(order.PaymentTradeNo) == "" {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_ROUTE_UNAVAILABLE", "security deposit payment order has no original gateway transaction")
	}
	principal := float64(principalCents) / 100
	if principal > order.Amount-order.RefundAmount+amountToleranceCNY {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_AMOUNT_EXCEEDED", "security deposit refund exceeds the remaining payment principal")
	}
	instance, err := s.getRefundOrderProviderInstance(ctx, order)
	if err != nil {
		return nil, infraerrors.InternalServer("PROVIDER_LOOKUP_FAILED", "failed to look up payment provider for this security deposit order").WithCause(err)
	}
	if instance == nil || !instance.RefundEnabled {
		return nil, infraerrors.Forbidden("SECURITY_DEPOSIT_PROVIDER_REFUND_DISABLED", "refund is not enabled for the original payment provider")
	}
	if requireUserRefund && !instance.AllowUserRefund {
		return nil, infraerrors.Forbidden("SECURITY_DEPOSIT_USER_REFUND_NOT_ALLOWED", "the original payment provider does not allow user refunds")
	}
	provider, err := s.getRefundProvider(ctx, order)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_ROUTE_UNAVAILABLE", "original payment provider is unavailable").WithCause(err)
	}
	if err := validateProviderSnapshotMetadata(order, provider.ProviderKey(), providerMerchantIdentityMetadata(provider)); err != nil {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_PROVIDER_MISMATCH", "original payment provider identity no longer matches the payment snapshot").WithCause(err)
	}
	currency := PaymentOrderCurrency(order)
	if currency != "CNY" {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_CURRENCY_UNSUPPORTED", "security deposit refunds currently require CNY")
	}
	gatewayAmount := calculateGatewayRefundAmount(order.Amount, order.PayAmount, principal, currency)
	return &SecurityDepositGatewayRefundPlan{
		PaymentOrderID: paymentOrderID, UserID: userID, PrincipalCents: principalCents,
		GatewayAmount: formatGatewayRefundAmount(gatewayAmount, order), GatewayCurrency: currency,
		RequestID: strings.TrimSpace(requestID), Reason: strings.TrimSpace(reason),
		internal: &securityDepositGatewayRefundInternal{order: order, gatewayAmount: gatewayAmount},
	}, nil
}

// GetSecurityDepositRefundCapability 返回原支付实例当前的两级退款开关。
func (s *PaymentService) GetSecurityDepositRefundCapability(ctx context.Context, paymentOrderID int64) (securityDepositRefundCapability, error) {
	if s == nil || s.entClient == nil || paymentOrderID <= 0 {
		return securityDepositRefundCapability{}, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_GATEWAY_UNAVAILABLE", "security deposit refund gateway is unavailable")
	}
	order, err := s.entClient.PaymentOrder.Get(ctx, paymentOrderID)
	if err != nil {
		return securityDepositRefundCapability{}, err
	}
	instance, err := s.getRefundOrderProviderInstance(ctx, order)
	if err != nil {
		return securityDepositRefundCapability{}, err
	}
	if instance == nil {
		return securityDepositRefundCapability{}, nil
	}
	return securityDepositRefundCapability{RefundEnabled: instance.RefundEnabled, AllowUserRefund: instance.AllowUserRefund}, nil
}

// ExecuteSecurityDepositGatewayRefund 只调用原支付网关；保证金预留和核销由保证金仓储负责。
func (s *PaymentService) ExecuteSecurityDepositGatewayRefund(ctx context.Context, plan *SecurityDepositGatewayRefundPlan) (*payment.RefundResponse, error) {
	if plan == nil {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND_PLAN", "security deposit refund plan is invalid")
	}
	internal, ok := plan.internal.(*securityDepositGatewayRefundInternal)
	if !ok || internal == nil || internal.order == nil {
		return nil, infraerrors.BadRequest("INVALID_SECURITY_DEPOSIT_REFUND_PLAN", "security deposit refund plan is invalid")
	}
	refundPlan := &RefundPlan{
		OrderID: plan.PaymentOrderID, Order: internal.order, RequestID: plan.RequestID,
		RefundAmount: float64(plan.PrincipalCents) / 100, GatewayAmount: internal.gatewayAmount,
		Reason: plan.Reason, Force: true, DeductBalance: false, DeductionType: payment.DeductionTypeNone,
	}
	response, err := s.gwRefund(ctx, refundPlan)
	if err != nil {
		return response, err
	}
	if err := validateRefundProviderResponse(response); err != nil {
		return response, fmt.Errorf("security deposit gateway refund: %w", err)
	}
	return response, nil
}

// QuerySecurityDepositGatewayRefund 查询已提交退款，不受退款开关后续关闭影响。
func (s *PaymentService) QuerySecurityDepositGatewayRefund(ctx context.Context, paymentOrderID int64, gatewayAmount, providerRefundID string) (*payment.RefundResponse, error) {
	if s == nil || s.entClient == nil || paymentOrderID <= 0 {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_GATEWAY_UNAVAILABLE", "security deposit refund gateway is unavailable")
	}
	order, err := s.entClient.PaymentOrder.Get(ctx, paymentOrderID)
	if err != nil {
		return nil, err
	}
	provider, err := s.getRefundProvider(ctx, order)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("SECURITY_DEPOSIT_REFUND_ROUTE_UNAVAILABLE", "original payment provider is unavailable").WithCause(err)
	}
	if err := validateProviderSnapshotMetadata(order, provider.ProviderKey(), providerMerchantIdentityMetadata(provider)); err != nil {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_PROVIDER_MISMATCH", "original payment provider identity no longer matches the payment snapshot").WithCause(err)
	}
	queryProvider, ok := provider.(payment.RefundQueryProvider)
	if !ok {
		return nil, infraerrors.Conflict("SECURITY_DEPOSIT_REFUND_QUERY_UNSUPPORTED", "the original payment provider does not support refund status queries")
	}
	response, err := queryProvider.QueryRefund(ctx, payment.RefundQueryRequest{
		TradeNo: order.PaymentTradeNo, OrderID: order.OutTradeNo,
		RefundID: strings.TrimSpace(providerRefundID), Amount: strings.TrimSpace(gatewayAmount),
	})
	if err != nil {
		return nil, err
	}
	if err := validateRefundProviderResponse(response); err != nil {
		if response != nil && strings.TrimSpace(response.Status) == payment.ProviderStatusFailed {
			return response, nil
		}
		return nil, err
	}
	return response, nil
}
