import { apiClient } from '../client'

export interface OpenAIUserAffinityConfig {
  enabled: boolean
  mode: 'shadow' | 'enforce'
  quota_reserve_ratio_5h: number
  quota_reserve_ratio_7d: number
  cold_start_demand_quantile: number
  best_fit_strategy: '7d_then_5h' | '5h_then_7d'
  best_fit_close_tolerance_ratio: number
  default_max_contact_users: number
  default_new_resident_cooldown_seconds: number
  resident_reentry_overcommit_enabled: boolean
  capacity_failure_migration_threshold: number
  capacity_failure_window_seconds: number
  migration_stability_seconds: number
  follower_jitter_min_ms: number
  follower_jitter_max_ms: number
  touch_success_mode: 'upstream_accepted' | 'response_completed'
  manual_reset_exclude_source_account: boolean
  config_version: number
  updated_at: string
}

export interface OpenAIUserAffinityConfigResponse {
  config: OpenAIUserAffinityConfig
  effective_state: 'disabled' | 'shadow' | 'enforce'
  config_version: number
  propagation_deadline_ms?: number
}

export async function getOpenAIUserAffinityScheduling(): Promise<OpenAIUserAffinityConfigResponse> {
  const { data } = await apiClient.get<OpenAIUserAffinityConfigResponse>(
    '/admin/settings/openai-user-affinity-scheduling'
  )
  return data
}

export async function updateOpenAIUserAffinityScheduling(
  config: OpenAIUserAffinityConfig,
  reason: string
): Promise<OpenAIUserAffinityConfigResponse> {
  const { data } = await apiClient.put<OpenAIUserAffinityConfigResponse>(
    '/admin/settings/openai-user-affinity-scheduling',
    { ...config, expected_version: config.config_version, reason }
  )
  return data
}

export const openAIUserAffinitySettingsAPI = {
  getOpenAIUserAffinityScheduling,
  updateOpenAIUserAffinityScheduling
}
