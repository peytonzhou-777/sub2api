import { apiClient } from '../client'

export type ResetRebateStatus = 'running' | 'ready' | 'incomplete' | 'not_eligible' | 'expired' | 'executed' | 'failed'

export interface ResetRebateBatch {
  id: number
  group_id: number
  group_name: string
  admin_id: number
  admin_email: string
  period_start: string
  period_end: string
  status: ResetRebateStatus
  progress_total: number
  progress_completed: number
  progress_succeeded: number
  progress_failed: number
  participant_count: number
  actual_amount: number
  refundable_amount: number
  failed_account_amount: number
  weekly_usage_percent: number
  refundable_percent: number
  suggested_ratio: number
  configured_ratio?: number
  issued_user_count: number
  excluded_user_count: number
  issued_amount: number
  failure_code?: string
  failure_message?: string
  rebate_reason?: string
  snapshot_expires_at?: string
  issued_at?: string
  executed_at?: string
  created_at: string
}

export interface ResetRebateAccount {
  id: number
  account_id: number
  account_name: string
  platform: string
  account_type: string
  is_shadow: boolean
  in_group: boolean
  schedulable: boolean
  consumed_amount: number
  available_count?: number
  weekly_used_percent?: number
  weekly_window_seconds?: number
  included: boolean
  exclusion_reason?: string
  error_code?: string
  error_message?: string
  fetched_at?: string
}

export interface ResetRebateUser {
  id: number
  user_id: number
  email: string
  username: string
  user_status: string
  user_deleted: boolean
  actual_amount: number
  rebate_ratio: number
  theoretical_amount: number
  rebate_amount: number
  issued: boolean
  exclusion_reason?: string
  grant_id?: number
  current_grant_status?: string
  expires_at?: string
}

export interface ResetRebatePreview {
  batch: ResetRebateBatch
  ratio: number
  expected_issued_at: string
  expected_expires_at: string
  issued_user_count: number
  excluded_user_count: number
  total_amount: number
  users: ResetRebateUser[]
  total: number
  page: number
  page_size: number
}

export async function createResetRebate(payload: { group_id: number; start: string; end: string }) {
  const { data } = await apiClient.post<ResetRebateBatch>('/admin/reset-rebates', payload)
  return data
}

export async function getResetRebate(id: number) {
  const { data } = await apiClient.get<ResetRebateBatch>(`/admin/reset-rebates/${id}`)
  return data
}

export async function listResetRebates(page = 1, pageSize = 20, params: Record<string, unknown> = {}) {
  const { data } = await apiClient.get<{ items: ResetRebateBatch[]; total: number }>('/admin/reset-rebates', { params: { page, page_size: pageSize, ...params } })
  return data
}

export async function getLatestExecutedPeriodEnd(groupId: number) {
  const { data } = await apiClient.get<{ period_end?: string | null }>('/admin/reset-rebates/latest-executed-period-end', { params: { group_id: groupId } })
  return data.period_end ?? null
}

export async function listResetRebateAccounts(id: number, page = 1, pageSize = 50) {
  const { data } = await apiClient.get<{ items: ResetRebateAccount[]; total: number }>(`/admin/reset-rebates/${id}/accounts`, { params: { page, page_size: pageSize } })
  return data
}

export async function previewResetRebate(id: number, ratio: number, page = 1, pageSize = 50, search = '', reason = '') {
  const params: Record<string, unknown> = { ratio, page, page_size: pageSize, search }
  if (reason.trim()) params.reason = reason
  const { data } = await apiClient.get<ResetRebatePreview>(`/admin/reset-rebates/${id}/preview`, { params })
  return data
}

export async function executeResetRebate(id: number, ratio: number, reason = '') {
  const payload: Record<string, unknown> = { ratio, confirmed: true }
  if (reason.trim()) payload.reason = reason
  const { data } = await apiClient.post<ResetRebateBatch>(`/admin/reset-rebates/${id}/execute`, payload)
  return data
}

export async function deleteResetRebate(id: number) {
  await apiClient.delete(`/admin/reset-rebates/${id}`)
}

export async function exportResetRebateUsers(id: number, ratio: number) {
  const { data } = await apiClient.get<Blob>(`/admin/reset-rebates/${id}/users.csv`, { params: { ratio }, responseType: 'blob' })
  return data
}

export default { createResetRebate, getResetRebate, listResetRebates, getLatestExecutedPeriodEnd, listResetRebateAccounts, previewResetRebate, executeResetRebate, deleteResetRebate, exportResetRebateUsers }
