import { apiClient } from '../client'

export type RecurringCreditStatus = 'active' | 'stopped' | 'completed' | 'deleted'
export type RecurringCreditMode = 'finite' | 'permanent'
export type RecurringCreditScheduleType = 'immediate' | 'monthly' | 'weekly'
export type RecurringCreditBatchStatus = 'running' | 'succeeded' | 'empty' | 'skipped' | 'missed' | 'failed'

export interface RecurringCreditTaskInput {
  name: string
  admin_notes: string
  schedule_type: RecurringCreditScheduleType
  day_of_month?: number
  day_of_week?: number
  validity_days?: number
  local_time: string
  timezone: string
  amount: number
  execution_mode: RecurringCreditMode
  remaining_runs?: number
  initially_active: boolean
}

export type RecurringCreditTask = Omit<RecurringCreditTaskInput, 'initially_active'> & {
  id: number
  skip_count: number
  status: RecurringCreditStatus
  next_run_at?: string
  next_run_local?: string
  schedule_description: string
  version: number
  latest_batch_status?: RecurringCreditBatchStatus
  created_at: string
  updated_at: string
}

export interface RecurringCreditPreview {
  amount: number
  next_run_at: string
  next_run_local: string
  expires_at: string
  qualification_start: string
  qualification_end: string
  reference_eligible_count: number
  api_active_count: number
  site_active_count: number
  both_active_count: number
  estimated_total: number
  disclaimer: string
  skipped_runs?: string[]
}

export interface RecurringCreditBatch {
  id: number
  task_id: number
  task_name: string
  scheduled_at: string
  validity_days?: number
  expires_at: string
  qualification_start: string
  qualification_end: string
  qualification_cutoff_at?: string
  config_version: number
  eligibility_policy: 'period_usage_or_recharge' | 'rolling_30d_activity_v1'
  amount: number
  timezone: string
  status: RecurringCreditBatchStatus
  attempt_count: number
  eligible_user_count: number
  issued_user_count: number
  excluded_user_count: number
  usage_eligible_count: number
  recharge_eligible_count: number
  api_active_count: number
  site_active_count: number
  both_active_count: number
  snapshot_completed_at?: string
  issued_amount: number
  failure_code?: string
  failure_message?: string
  finished_at?: string
  created_at: string
}

export interface RecurringCreditUserItem {
  id: number
  batch_id: number
  user_id: number
  email: string
  username: string
  user_status: string
  user_deleted: boolean
  actual_cost: number
  net_recharge: number
  qualification_reason: string
  api_last_used_at?: string
  site_last_active_at?: string
  grant_amount: number
  grant_id?: number
  result: string
  exclusion_reason?: string
}

const base = '/admin/credits/recurring-grants'

export async function previewRecurringCredit(payload: RecurringCreditTaskInput, skipCount = 0) {
  const { data } = await apiClient.post<RecurringCreditPreview>(`${base}/preview`, payload, { params: skipCount ? { skip_count: skipCount } : undefined })
  return data
}

export async function createRecurringCredit(payload: RecurringCreditTaskInput, idempotencyKey: string) {
  const { data } = await apiClient.post<RecurringCreditTask>(base, payload, { headers: { 'Idempotency-Key': idempotencyKey } })
  return data
}

export async function listRecurringCredits(page = 1, pageSize = 20, params: Record<string, unknown> = {}) {
  const { data } = await apiClient.get<{ items: RecurringCreditTask[]; total: number }>(base, { params: { page, page_size: pageSize, ...params } })
  return data
}

export async function getRecurringCredit(id: number) {
  const { data } = await apiClient.get<RecurringCreditTask>(`${base}/${id}`)
  return data
}

export async function updateRecurringCredit(id: number, payload: RecurringCreditTaskInput, expectedVersion: number) {
  const { data } = await apiClient.put<RecurringCreditTask>(`${base}/${id}`, { ...payload, expected_version: expectedVersion })
  return data
}

export async function recurringCreditAction(id: number, action: string, expectedVersion: number, count?: number, configuration?: RecurringCreditTaskInput) {
  const { data } = await apiClient.post<RecurringCreditTask>(`${base}/${id}/${action}`, { expected_version: expectedVersion, count, configuration })
  return data
}

export async function deleteRecurringCredit(id: number, expectedVersion: number) {
  const { data } = await apiClient.delete<RecurringCreditTask>(`${base}/${id}`, { params: { expected_version: expectedVersion } })
  return data
}

export async function listRecurringCreditBatches(id: number, page = 1, pageSize = 20, params: Record<string, unknown> = {}) {
  const { data } = await apiClient.get<{ items: RecurringCreditBatch[]; total: number }>(`${base}/${id}/batches`, { params: { page, page_size: pageSize, ...params } })
  return data
}

export async function listRecurringCreditUsers(taskId: number, batchId: number, page = 1, pageSize = 50, search = '') {
  const { data } = await apiClient.get<{ items: RecurringCreditUserItem[]; total: number }>(`${base}/${taskId}/batches/${batchId}/users`, { params: { page, page_size: pageSize, search } })
  return data
}

export async function exportRecurringCreditUsers(taskId: number, batchId: number) {
  const { data } = await apiClient.get<Blob>(`${base}/${taskId}/batches/${batchId}/users.csv`, { responseType: 'blob' })
  return data
}

export default { previewRecurringCredit, createRecurringCredit, listRecurringCredits, getRecurringCredit, updateRecurringCredit, recurringCreditAction, deleteRecurringCredit, listRecurringCreditBatches, listRecurringCreditUsers, exportRecurringCreditUsers }
