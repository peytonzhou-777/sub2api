import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface AdminLimitedCredit {
  id: number
  user_id: number
  source_type: string
  initial_amount: number
  used_amount: number
  frozen_amount: number
  expires_at: string
  status: string
  notes?: string
  created_at: string
  updated_at: string
  validity_days?: number
}

export interface CreditUser {
  id: number
  email: string
  username: string
  status: string
  balance: number
  frozen_balance: number
  limited_remaining_amount: number
  limited_active_count: number
  updated_at: string
}

export interface CreditUserDetail extends CreditUser {
  limited_credits: AdminLimitedCredit[]
}

export interface LimitedCreditLedgerEntry {
  id: number
  event_type: string
  amount: number
  notes?: string
  created_at: string
}

export async function listCreditUsers(page = 1, pageSize = 20, search = '') {
  const { data } = await apiClient.get<PaginatedResponse<CreditUser>>('/admin/credits/users', { params: { page, page_size: pageSize, search } })
  return data
}

export async function getCreditUser(id: number) {
  const { data } = await apiClient.get<CreditUserDetail>(`/admin/credits/users/${id}`)
  return data
}

export async function adjustBalance(id: number, payload: { operation: 'add' | 'subtract'; amount: number; notes: string; expected_updated_at: string }) {
  const { data } = await apiClient.post<CreditUserDetail>(`/admin/credits/users/${id}/balance-adjustments`, payload)
  return data
}

export async function createLimitedCredit(id: number, payload: { amount: number; validity_days: number; notes: string }) {
  const { data } = await apiClient.post<AdminLimitedCredit>(`/admin/credits/users/${id}/limited-credits`, payload)
  return data
}

export async function adjustLimitedCredit(userId: number, grantId: number, payload: Record<string, unknown>) {
  const { data } = await apiClient.post<AdminLimitedCredit>(`/admin/credits/users/${userId}/limited-credits/${grantId}/adjustments`, payload)
  return data
}

export async function revokeLimitedCredit(userId: number, grantId: number, payload: { expected_updated_at: string; notes: string }) {
  const { data } = await apiClient.post<AdminLimitedCredit>(`/admin/credits/users/${userId}/limited-credits/${grantId}/revoke`, payload)
  return data
}

export async function resetLimitedCredit(userId: number, grantId: number, payload: { expected_updated_at: string; notes: string }) {
  const { data } = await apiClient.post<AdminLimitedCredit>(`/admin/credits/users/${userId}/limited-credits/${grantId}/reset`, payload)
  return data
}

export async function listLimitedCreditLedger(userId: number, grantId: number) {
  const { data } = await apiClient.get<LimitedCreditLedgerEntry[]>(`/admin/credits/users/${userId}/limited-credits/${grantId}/ledger`)
  return data
}
