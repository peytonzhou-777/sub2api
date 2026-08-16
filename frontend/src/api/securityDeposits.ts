import { apiClient } from './client'
import type {
  CreateSecurityDepositOrderRequest,
  CreateSecurityDepositOrderResult,
  SecurityDepositAccount,
  SecurityDepositAgreement,
  SecurityDepositEligibility,
  SecurityDepositRefund,
  SecurityDepositRefundPreview,
} from '@/types/securityDeposit'

function newIdempotencyKey(prefix: string) {
  const random = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `${Date.now()}-${Math.random().toString(16).slice(2)}`
  return `${prefix}-${random}`
}

export const securityDepositsAPI = {
  getAccount() {
    return apiClient.get<SecurityDepositAccount>('/security-deposits/account')
  },

  getEligibility(groupId: number) {
    return apiClient.get<SecurityDepositEligibility>('/security-deposits/eligibility', {
      params: { group_id: groupId },
    })
  },

  getAgreement(groupId?: number) {
    return apiClient.get<SecurityDepositAgreement>('/security-deposits/agreement', {
      params: groupId ? { group_id: groupId } : undefined,
    })
  },

  createOrder(data: CreateSecurityDepositOrderRequest) {
    return apiClient.post<CreateSecurityDepositOrderResult>('/security-deposits/orders', data)
  },

  previewRefund(lotId: number) {
    return apiClient.post<SecurityDepositRefundPreview>('/security-deposits/refunds/preview', { lot_id: lotId })
  },

  createRefund(lotId: number, idempotencyKey = newIdempotencyKey('security-deposit-refund')) {
    return apiClient.post<SecurityDepositRefund>('/security-deposits/refunds', { lot_id: lotId }, {
      headers: { 'Idempotency-Key': idempotencyKey },
    })
  },

  getRefund(refundId: string) {
    return apiClient.get<SecurityDepositRefund>(`/security-deposits/refunds/${encodeURIComponent(refundId)}`)
  },
}

export default securityDepositsAPI
