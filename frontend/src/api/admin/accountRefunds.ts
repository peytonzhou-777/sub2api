import { apiClient } from '../client'
import type { BasePaginationResponse } from '@/types'
import type {
  AdminAccountRefundActionInput,
  AdminAccountRefundDetail,
  AdminAccountRefundListItem,
  AdminAccountRefundListParams,
  AdminAccountRefundReconcileInput,
  AdminAccountRefundSummary,
} from '@/types/accountRefund'
import type { AccountRefundRecord } from '@/types/payment'

const basePath = '/admin/payment/account-refunds'

/** 管理员余额清退工作台 API。 */
export const adminAccountRefundAPI = {
  getSummary() {
    return apiClient.get<AdminAccountRefundSummary>(`${basePath}/summary`)
  },

  getList(params?: AdminAccountRefundListParams) {
    return apiClient.get<BasePaginationResponse<AdminAccountRefundListItem>>(basePath, { params })
  },

  getDetail(userId: number) {
    return apiClient.get<AdminAccountRefundDetail>(`${basePath}/${userId}`)
  },

  start(userId: number, input: AdminAccountRefundActionInput, idempotencyKey: string) {
    return apiClient.post<AccountRefundRecord>(`${basePath}/${userId}/start`, input, { headers: { 'Idempotency-Key': idempotencyKey } })
  },

  action(userId: number, action: Exclude<keyof typeof actionPaths, never>, input: AdminAccountRefundActionInput) {
    return apiClient.post<AccountRefundRecord>(`${basePath}/${userId}/${actionPaths[action]}`, input)
  },

  reconcile(userId: number, input: AdminAccountRefundReconcileInput) {
    return apiClient.post<AccountRefundRecord>(`${basePath}/${userId}/reconcile`, input)
  },
}

const actionPaths = {
  advance: 'advance',
  confirm: 'confirm',
  continue: 'continue',
  recalculate: 'recalculate',
  finalize: 'finalize',
  cancel: 'cancel',
  'restore-access': 'restore-access',
} as const

export type AdminAccountRefundDirectAction = keyof typeof actionPaths
