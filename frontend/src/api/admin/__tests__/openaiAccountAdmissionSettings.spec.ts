import { beforeEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../../client'
import {
  type OpenAIAccountAdmissionConfig,
  updateOpenAIAccountAdmission
} from '../openaiAccountAdmissionSettings'

vi.mock('../../client', () => ({
  apiClient: { put: vi.fn() }
}))

const config: OpenAIAccountAdmissionConfig = {
  enabled: true,
  queue_enabled: true,
  max_wait_seconds: 45,
  max_concurrency: 0,
  default_max_active_users_per_persona: 1,
  requests_per_minute: 60,
  tokens_per_minute: 120000,
  max_subagents: 0,
  subagent_depth: 0,
  max_active_websockets: 0,
  default_output_tokens: 4096,
  jitter_min_ms: 100,
  jitter_max_ms: 500,
  max_queue_depth_per_account: 100,
  interactive_burst: 4,
  background_aging_seconds: 5,
  config_version: 3,
  updated_at: ''
}

describe('OpenAI account admission settings API', () => {
  beforeEach(() => vi.clearAllMocks())

  it('submits the whole config with expected_version', async () => {
    vi.mocked(apiClient.put).mockResolvedValue({ data: { config } })
    await updateOpenAIAccountAdmission(config)
    expect(apiClient.put).toHaveBeenCalledWith('/admin/settings/openai-account-admission', {
      ...config,
      expected_version: 3
    })
  })
})
