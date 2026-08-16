import { apiClient } from '../client'
import type {
  AdminSecurityDepositCreditType,
  AdminSecurityDepositMutationResult,
  AdminSecurityDepositUnlockResult,
  AdminSecurityDepositUserDetail,
  SecurityDepositRefund,
} from '@/types/securityDeposit'

function idempotencyHeaders() {
  const key = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`
  return { 'Idempotency-Key': key }
}

export async function getSecurityDepositUser(userId: number) {
  const { data } = await apiClient.get<AdminSecurityDepositUserDetail>(`/admin/security-deposits/users/${userId}`)
  return data
}

export async function creditSecurityDeposit(
  userId: number,
  amountCents: number,
  actionType: AdminSecurityDepositCreditType,
  reason?: string,
) {
  const { data } = await apiClient.post<AdminSecurityDepositMutationResult>(
    `/admin/security-deposits/users/${userId}/credits`,
    { amount_cents: amountCents, action_type: actionType, reason: reason || undefined },
    { headers: idempotencyHeaders() },
  )
  return data
}

export async function deductSecurityDeposit(userId: number, amountCents: number, reason?: string) {
  const { data } = await apiClient.post<AdminSecurityDepositMutationResult>(
    `/admin/security-deposits/users/${userId}/deductions`,
    { amount_cents: amountCents, reason: reason || undefined },
    { headers: idempotencyHeaders() },
  )
  return data
}

export async function revokeSecurityDepositLot(userId: number, lotId: number, reason?: string) {
  const { data } = await apiClient.post<AdminSecurityDepositMutationResult>(
    `/admin/security-deposits/users/${userId}/lots/${lotId}/revoke`,
    { reason: reason || undefined },
    { headers: idempotencyHeaders() },
  )
  return data
}

export async function unlockSecurityDepositApiKey(userId: number, apiKeyId: number, reason?: string) {
  const { data } = await apiClient.post<AdminSecurityDepositUnlockResult>(
    `/admin/security-deposits/users/${userId}/api-keys/${apiKeyId}/unlock`,
    { reason: reason || undefined },
    { headers: idempotencyHeaders() },
  )
  return data
}

export async function automaticallyRefundSecurityDepositLot(userId: number, lotId: number, reason?: string) {
  const { data } = await apiClient.post<SecurityDepositRefund>(
    `/admin/security-deposits/users/${userId}/lots/${lotId}/refunds/automatic`,
    { reason: reason || undefined },
    { headers: idempotencyHeaders() },
  )
  return data
}

export async function reserveManualSecurityDepositRefund(userId: number, lotId: number, reason?: string) {
  const { data } = await apiClient.post<SecurityDepositRefund>(
    `/admin/security-deposits/users/${userId}/lots/${lotId}/refunds/manual`,
    { reason: reason || undefined },
    { headers: idempotencyHeaders() },
  )
  return data
}

export async function completeManualSecurityDepositRefund(
  userId: number,
  refundId: string,
  input: {
    external_refund_id: string
    external_amount_cents: number
    external_refunded_at: string
    external_evidence: Record<string, unknown>
    reason?: string
  },
) {
  const { data } = await apiClient.post<SecurityDepositRefund>(
    `/admin/security-deposits/users/${userId}/refunds/${refundId}/complete-manual`,
    input,
    { headers: idempotencyHeaders() },
  )
  return data
}

export async function cancelSecurityDepositRefund(userId: number, refundId: string, reason?: string) {
  const { data } = await apiClient.post<SecurityDepositRefund>(
    `/admin/security-deposits/users/${userId}/refunds/${refundId}/cancel`,
    { reason: reason || undefined },
    { headers: idempotencyHeaders() },
  )
  return data
}

export async function querySecurityDepositRefund(userId: number, refundId: string) {
  const { data } = await apiClient.post<SecurityDepositRefund>(
    `/admin/security-deposits/users/${userId}/refunds/${encodeURIComponent(refundId)}/query`,
    {},
  )
  return data
}

export async function failAutomaticSecurityDepositRefundReview(
  userId: number,
  refundId: string,
  evidence: Record<string, unknown>,
  reason?: string,
) {
  const { data } = await apiClient.post<SecurityDepositRefund>(
    `/admin/security-deposits/users/${userId}/refunds/${encodeURIComponent(refundId)}/review-failed`,
    { evidence, reason: reason || undefined },
    { headers: idempotencyHeaders() },
  )
  return data
}

export const securityDepositsAdminAPI = {
  getUser: getSecurityDepositUser,
  credit: creditSecurityDeposit,
  deduct: deductSecurityDeposit,
  revokeLot: revokeSecurityDepositLot,
  unlockApiKey: unlockSecurityDepositApiKey,
  automaticallyRefundLot: automaticallyRefundSecurityDepositLot,
  reserveManualRefund: reserveManualSecurityDepositRefund,
  completeManualRefund: completeManualSecurityDepositRefund,
  cancelRefund: cancelSecurityDepositRefund,
  queryRefund: querySecurityDepositRefund,
  failAutomaticRefundReview: failAutomaticSecurityDepositRefundReview,
}

export default securityDepositsAdminAPI
