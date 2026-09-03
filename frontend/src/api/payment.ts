/**
 * User Payment API endpoints
 * Handles payment operations for regular users
 */

import { apiClient } from './client'
import type {
  PaymentConfig,
  SubscriptionPlan,
  MethodLimitsResponse,
  CheckoutInfoResponse,
  CreateOrderRequest,
  CreateOrderResult,
  PaymentOrder,
  AccountRefundRecord,
} from '@/types/payment'
import type { BasePaginationResponse } from '@/types'

export interface PublicOrderVerifyResult {
  out_trade_no: string
  status: string
  paid: boolean
  created_at: string
  expires_at: string
}

export const paymentAPI = {
  /** Get payment configuration (enabled types, limits, etc.) */
  getConfig() {
    return apiClient.get<PaymentConfig>('/payment/config')
  },

  /** Get available subscription plans */
  getPlans() {
    return apiClient.get<SubscriptionPlan[]>('/payment/plans')
  },

  /** Get all checkout page data in a single call */
  getCheckoutInfo() {
    return apiClient.get<CheckoutInfoResponse>('/payment/checkout-info')
  },

  /** Get payment method limits and fee rates */
  getLimits() {
    return apiClient.get<MethodLimitsResponse>('/payment/limits')
  },

  /** Create a new payment order */
  createOrder(data: CreateOrderRequest) {
    return apiClient.post<CreateOrderResult>('/payment/orders', data)
  },

  /** Get current user's orders */
  getMyOrders(params?: { page?: number; page_size?: number; status?: string }) {
    return apiClient.get<BasePaginationResponse<PaymentOrder>>('/payment/orders/my', { params })
  },

  /** Get a specific order by ID */
  getOrder(id: number) {
    return apiClient.get<PaymentOrder>(`/payment/orders/${id}`)
  },

  /** Cancel a pending order */
  cancelOrder(id: number) {
    return apiClient.post(`/payment/orders/${id}/cancel`)
  },

  /** Verify order payment status with upstream provider */
  verifyOrder(outTradeNo: string) {
    return apiClient.post<PaymentOrder>('/payment/orders/verify', { out_trade_no: outTradeNo })
  },

  /** Legacy-compatible public order lookup by out_trade_no */
  verifyOrderPublic(outTradeNo: string) {
    return apiClient.post<PublicOrderVerifyResult>('/payment/public/orders/verify', { out_trade_no: outTradeNo })
  },

  /** Resolve an order from a signed resume token without auth */
  resolveOrderPublicByResumeToken(resumeToken: string) {
    return apiClient.post<PublicOrderVerifyResult>('/payment/public/orders/resolve', { resume_token: resumeToken })
  },

  /** Get provider instance IDs that allow user refund */
  getRefundEligibleProviders() {
    return apiClient.get<{ provider_instance_ids: string[] }>('/payment/orders/refund-eligible-providers')
  },

  /** Get the authoritative account-wide refund quote. */
  getAccountRefundOverview() {
    return apiClient.get<AccountRefundRecord>('/payment/refunds/overview')
  },

  /** Lock the account and wait for all billable work to settle. */
  lockAccountRefund(quoteHash: string) {
    return apiClient.post<AccountRefundRecord>('/payment/refunds/lock', { quote_hash: quoteHash })
  },

  /** Restore only the dedicated session for an already locked account refund. */
  restoreAccountRefundSession(email: string, password: string, totpCode: string) {
    return apiClient.post<AccountRefundRecord>('/payment/refunds/session/restore', {
      email,
      password,
      totp_code: totpCode,
    }, { headers: { 'X-Skip-Auth': '1' } })
  },

  /** Read a locked refund with its dedicated session credential. */
  getAccountRefund(refundId: string, sessionToken: string) {
    return apiClient.get<AccountRefundRecord>(`/payment/refunds/${refundId}`, {
      headers: refundSessionHeaders(sessionToken),
    })
  },

  /** Confirm the immutable final quote. */
  confirmAccountRefund(refundId: string, quoteHash: string, sessionToken: string) {
    return apiClient.post<AccountRefundRecord>(`/payment/refunds/${refundId}/confirm`, { quote_hash: quoteHash }, {
      headers: refundSessionHeaders(sessionToken),
    })
  },

  /** Cancel before any gateway submission and restore the previous account state. */
  cancelAccountRefund(refundId: string, sessionToken: string) {
    return apiClient.post<AccountRefundRecord>(`/payment/refunds/${refundId}/cancel`, {}, {
      headers: refundSessionHeaders(sessionToken),
    })
  }
}

function refundSessionHeaders(sessionToken: string) {
  return {
    'X-Refund-Session': sessionToken,
    'X-Skip-Auth': '1',
  }
}
