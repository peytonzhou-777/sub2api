import { apiClient } from '../client'

export interface OpenAIPersonaAdmissionPolicy {
  max_concurrency: number
  max_active_client_sessions: number
  requests_per_minute: number
  tokens_per_minute: number
  max_queue_depth_per_account: number
  max_subagents: number
  subagent_depth: number
  max_active_websockets: number
}

export interface OpenAIAccountAdmissionConfig {
  enabled: boolean
  queue_enabled: boolean
  max_wait_seconds: number
  max_concurrency: number
  max_active_client_sessions: number
  requests_per_minute: number
  tokens_per_minute: number
  max_subagents: number
  subagent_depth: number
  max_active_websockets: number
  default_output_tokens: number
  jitter_min_ms: number
  jitter_max_ms: number
  max_queue_depth_per_account: number
  interactive_burst: number
  background_aging_seconds: number
  persona_policies?: Record<string, OpenAIPersonaAdmissionPolicy>
  config_version: number
  updated_at: string
}

export interface OpenAIAccountAdmissionConfigResponse {
  config: OpenAIAccountAdmissionConfig
  effective_state: 'disabled' | 'enabled'
  config_version: number
  propagation_deadline_ms?: number
}

export async function getOpenAIAccountAdmission(): Promise<OpenAIAccountAdmissionConfigResponse> {
  const { data } = await apiClient.get<OpenAIAccountAdmissionConfigResponse>(
    '/admin/settings/openai-account-admission'
  )
  return data
}

export async function updateOpenAIAccountAdmission(
  config: OpenAIAccountAdmissionConfig
): Promise<OpenAIAccountAdmissionConfigResponse> {
  const { data } = await apiClient.put<OpenAIAccountAdmissionConfigResponse>(
    '/admin/settings/openai-account-admission',
    { ...config, expected_version: config.config_version }
  )
  return data
}

export const openAIAccountAdmissionSettingsAPI = {
  getOpenAIAccountAdmission,
  updateOpenAIAccountAdmission
}
