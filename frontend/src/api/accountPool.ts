import { apiClient } from './client'

export type AccountPoolFreshness = 'fresh' | 'stale' | 'unavailable'

export interface AccountPoolAccount {
  id: number
  platform: string
  type: string
  auth_mode?: string
  plan_type?: string
  privacy_mode?: string
  antigravity_tier?: string
  capacity: {
    current_concurrency: number | null
    max_concurrency: number
    observed_at: string | null
    state: AccountPoolFreshness
  }
  usage_windows: Array<{
    code: string
    label: string
    used_percent: number | null
    resets_at: string | null
    observed_at: string | null
    state: AccountPoolFreshness
  }>
  reset_count: number | null
  reset_count_state: string
  status: {
    code: string
    category?: string
    resume_at: string | null
    models: Array<{ kind: string; model: string; resume_at: string | null }>
  }
}

export interface AccountPoolPage {
  items: AccountPoolAccount[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface AccountPoolPersonalUsageWindow {
  code: '5h' | '7d'
  label: string
  start_at: string
  end_at: string
  requests: number
  tokens: number
  actual_cost: number
}

export interface AccountPoolPersonalUsage {
  account_id: number
  observed_at: string
  windows: AccountPoolPersonalUsageWindow[]
}

export async function listAccountPool(options: {
  page: number
  pageSize: number
  accountId?: string
  etag?: string
  signal?: AbortSignal
}): Promise<{ data?: AccountPoolPage; etag?: string; notModified: boolean }> {
  const response = await apiClient.get<AccountPoolPage>('/account-pool', {
    params: {
      page: options.page,
      page_size: options.pageSize,
      ...(options.accountId ? { account_id: options.accountId } : {}),
    },
    headers: options.etag ? { 'If-None-Match': options.etag } : undefined,
    signal: options.signal,
    validateStatus: (status) => status === 200 || status === 304,
  })
  return {
    data: response.status === 200 ? response.data : undefined,
    etag: response.headers.etag,
    notModified: response.status === 304,
  }
}

export async function getPersonalUsage(accountId: number, options?: { signal?: AbortSignal }): Promise<AccountPoolPersonalUsage> {
  const { data } = await apiClient.get<AccountPoolPersonalUsage>(`/account-pool/${accountId}/personal-usage`, {
    signal: options?.signal,
  })
  return data
}

export default { list: listAccountPool, getPersonalUsage }
