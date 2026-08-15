import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface OpenAIUserAffinityResident {
  user_id: number
  user_email: string
  account_id: number
  scope_key: string
  generation: number
  assigned_at: string
  last_active_at: string | null
  expires_at: string
  touch_expires_at: string | null
}

export interface OpenAIUserAffinityEvent {
  id: number
  scope_key: string
  placement_generation: number
  source_account_id: number | null
  target_account_id: number | null
  event_type: string
  reason: string
  actor_admin_id: number | null
  created_at: string
}

export interface OpenAIUserAffinityPlacement {
  user_id: number
  scope_key: string
  account_id: number | null
  generation: number
  status: string
  assigned_at: string
  last_active_at: string | null
  expires_at: string
  assignment_reason: string
}

export interface OpenAIUserAffinityUserDetail {
  placement: OpenAIUserAffinityPlacement | null
  placements: OpenAIUserAffinityPlacement[]
  events: OpenAIUserAffinityEvent[]
}

export interface OpenAIUserAffinityAccountPolicy {
  account_id: number
  max_contact_users: number | null
  new_resident_cooldown_seconds: number | null
  capacity_failure_migration_threshold: number | null
  capacity_failure_window_seconds: number | null
  new_resident_cooldown_until: string | null
  affinity_config_version: number
}

export async function listOpenAIUserAffinityResidents(id: number, page = 1, pageSize = 50): Promise<PaginatedResponse<OpenAIUserAffinityResident>> {
  const { data } = await apiClient.get<PaginatedResponse<OpenAIUserAffinityResident>>(
    `/admin/accounts/${id}/affinity-residents`,
    { params: { page, page_size: pageSize } }
  )
  return data
}

export async function getOpenAIUserAffinityUserDetail(userId: number): Promise<OpenAIUserAffinityUserDetail> {
  const { data } = await apiClient.get<OpenAIUserAffinityUserDetail>(`/admin/accounts/user-affinity/${userId}`)
  return data
}

export async function resetOpenAIUserAffinityPlacement(userId: number, scopeKey: string, reason: string, excludeSourceAccount = false): Promise<void> {
  await apiClient.post(`/admin/accounts/user-affinity/${userId}/reset`, {
    scope_key: scopeKey,
    reason,
    exclude_source_account: excludeSourceAccount
  })
}

export async function getOpenAIUserAffinityAccountPolicy(id: number): Promise<OpenAIUserAffinityAccountPolicy> {
  const { data } = await apiClient.get<OpenAIUserAffinityAccountPolicy>(`/admin/accounts/${id}/affinity-policy`)
  return data
}

export async function updateOpenAIUserAffinityAccountPolicy(id: number, policy: OpenAIUserAffinityAccountPolicy): Promise<OpenAIUserAffinityAccountPolicy> {
  const { data } = await apiClient.put<OpenAIUserAffinityAccountPolicy>(`/admin/accounts/${id}/affinity-policy`, policy)
  return data
}

export const openAIUserAffinityAccountsAPI = {
  listOpenAIUserAffinityResidents,
  getOpenAIUserAffinityUserDetail,
  resetOpenAIUserAffinityPlacement,
  getOpenAIUserAffinityAccountPolicy,
  updateOpenAIUserAffinityAccountPolicy
}
