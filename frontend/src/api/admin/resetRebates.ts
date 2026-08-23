import { apiClient } from '../client'

export type ResetRebateStatus = 'running' | 'executing' | 'ready' | 'not_eligible' | 'partial' | 'failed' | 'executed' | 'incomplete' | 'expired'
export type ResetRebateRatioMode = 'auto' | 'manual'

export interface ResetRebateBatch {
  id: number
  mechanism_version: number
  group_id?: number
  group_name?: string
  admin_id: number
  admin_email: string
  status: ResetRebateStatus
  failure_stage?: 'statistics' | 'execution' | ''
  execution_mode?: 'initial' | 'retry' | ''
  execution_cursor_user_id?: number
  initial_issued_at?: string
  force_stat_ratio_enabled: boolean
  force_stat_ratio: string
  average_benefit_enabled: boolean
  average_benefit_duration_us: number
  average_benefit_ratio: string
  combined_payout_ratio: string
  account_count: number
  excluded_account_count: number
  risk_account_count: number
  progress_total: number
  progress_completed: number
  period_start?: string
  period_end?: string
  raw_amount: string
  weighted_amount: string
  expected_amount: string
  successful_amount: string
  failed_amount: string
  excluded_amount: string
  payout_ratio?: number
  rebate_reason: string
  preview_version: number
  expected_user_count: number
  successful_user_count: number
  excluded_user_count: number
  failed_user_count: number
  failure_code?: string
  failure_message?: string
  executed_by_admin_id?: number
  executed_by_admin_email?: string
  first_executed_at?: string
  last_retry_at?: string
  created_at: string
  updated_at: string
}

export interface ResetRebateWindowDefault {
  account_id: number
  period_start: string
  period_end: string
  history_count: number
  window_source: string
  window_version: string
  risk: string
  auto_stat_ratio: string
  account_status: string
  error_message: string
}

export interface ResetRebateAccountDraft {
  account_id: number
  period_start: string
  ratio_mode: ResetRebateRatioMode
  manual_ratio?: string
  default_window_version: string
  window_modified: boolean
}

export interface ResetRebateAccount extends Omit<ResetRebateAccountDraft, 'default_window_version' | 'window_modified' | 'ratio_mode'> {
  id: number
  period_end: string
  account_name: string
  platform: string
  account_type: string
  is_shadow: boolean
  account_status: string
  account_error_message: string
  schedulable: boolean
  default_window_source: string
  window_risk: string
  auto_stat_ratio: string
  manual_stat_ratio?: string
  ratio_mode: ResetRebateRatioMode | 'average'
  effective_stat_ratio: string
  included_in_statistics: boolean
  statistics_exclusion_reason?: string
  raw_amount: string
  weighted_amount: string
}

export interface ResetRebateUser {
  id: number
  user_id: number
  email: string
  username: string
  user_status: string
  user_deleted: boolean
  raw_amount: string
  weighted_amount: string
  expected_amount: string
  actual_issued_amount: string
  result: 'pending' | 'succeeded' | 'failed' | 'excluded'
  exclusion_reason?: string
  error_code?: string
  error_message?: string
  attempt_count: number
  first_failed_at?: string
  last_attempt_at?: string
  grant_id?: number
  issued_at?: string
  expires_at?: string
}

export interface ResetRebateContribution {
  account_id: number
  account_name: string
  period_start: string
  period_end: string
  raw_amount: string
  effective_stat_ratio: string
  weighted_amount: string
}

export interface ResetRebatePage<T> {
  items: T[]
  total: number
  page: number
  page_size: number
}

export interface ResetRebatePreview {
  batch: ResetRebateBatch
  users: ResetRebatePage<ResetRebateUser>
}

export async function accountWindowDefaults(accountIds: number[]) {
  const { data } = await apiClient.post<{ items: ResetRebateWindowDefault[] }>('/admin/reset-rebates/account-window-defaults', { account_ids: accountIds })
  return data.items
}

export async function create(payload: {
  mechanism_version: 3
  period_end: string
  average_benefit_enabled: boolean
  force_stat_ratio_enabled: boolean
  force_stat_ratio: string
  acknowledged_error_account_ids: number[]
  accounts: ResetRebateAccountDraft[]
}) {
  const { data } = await apiClient.post<ResetRebateBatch>('/admin/reset-rebates', payload)
  return data
}

export async function get(id: number) {
  const { data } = await apiClient.get<ResetRebateBatch>(`/admin/reset-rebates/${id}`)
  return data
}

export async function list(page = 1, pageSize = 20, params: Record<string, unknown> = {}) {
  const { data } = await apiClient.get<ResetRebatePage<ResetRebateBatch>>('/admin/reset-rebates', { params: { page, page_size: pageSize, ...params } })
  return data
}

export async function listAccounts(id: number, page = 1, pageSize = 50) {
  const { data } = await apiClient.get<ResetRebatePage<ResetRebateAccount>>(`/admin/reset-rebates/${id}/accounts`, { params: { page, page_size: pageSize } })
  return data
}

export async function listUsers(id: number, page = 1, pageSize = 50, search = '', result = '') {
  const { data } = await apiClient.get<ResetRebatePage<ResetRebateUser>>(`/admin/reset-rebates/${id}/users`, { params: { page, page_size: pageSize, search, result } })
  return data
}

export async function listContributions(id: number, userId: number) {
  const { data } = await apiClient.get<{ items: ResetRebateContribution[] }>(`/admin/reset-rebates/${id}/users/${userId}/contributions`)
  return data.items
}

export async function preview(id: number, payoutRatio: number, reason: string, page = 1, pageSize = 50, search = '') {
  const { data } = await apiClient.post<ResetRebatePreview>(`/admin/reset-rebates/${id}/preview`, { payout_ratio: payoutRatio, reason }, { params: { page, page_size: pageSize, search } })
  return data
}

export async function execute(id: number, previewVersion: number) {
  const { data } = await apiClient.post<ResetRebateBatch>(`/admin/reset-rebates/${id}/execute`, { preview_version: previewVersion, confirmed: true })
  return data
}

export async function retryFailures(id: number) {
  const { data } = await apiClient.post<ResetRebateBatch>(`/admin/reset-rebates/${id}/retry-failures`, { confirmed: true })
  return data
}

export async function remove(id: number) {
  await apiClient.delete(`/admin/reset-rebates/${id}`)
}

export async function exportCSV(id: number, kind: 'users' | 'accounts' | 'user-account-contributions' | 'failed-users') {
  const { data } = await apiClient.get<Blob>(`/admin/reset-rebates/${id}/${kind}.csv`, { responseType: 'blob' })
  return data
}

export default { accountWindowDefaults, create, get, list, listAccounts, listUsers, listContributions, preview, execute, retryFailures, remove, exportCSV }
