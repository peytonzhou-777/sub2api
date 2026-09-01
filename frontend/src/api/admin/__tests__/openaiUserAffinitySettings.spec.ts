import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  type OpenAIUserAffinityConfig,
  updateOpenAIUserAffinityScheduling
} from '../openaiUserAffinitySettings'

const { put } = vi.hoisted(() => ({ put: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { put } }))

const config: OpenAIUserAffinityConfig = {
  enabled: true,
  mode: 'enforce',
  quota_reserve_ratio_5h: 0.1,
  quota_reserve_ratio_7d: 0.1,
  cold_start_demand_quantile: 0.75,
  best_fit_strategy: '7d_then_5h',
  best_fit_close_tolerance_ratio: 0.01,
  default_max_resident_users: 10,
  default_new_resident_cooldown_seconds: 300,
  resident_reentry_overcommit_enabled: true,
  capacity_failure_migration_threshold: 3,
  capacity_failure_window_seconds: 60,
  migration_stability_seconds: 60,
  follower_jitter_min_ms: 100,
  follower_jitter_max_ms: 500,
  touch_success_mode: 'upstream_accepted',
  manual_reset_exclude_source_account: false,
  resident_account_slot_count: 1,
  resident_ttl_seconds: 604800,
  conversation_active_ttl_seconds: 3600,
  config_version: 7,
  updated_at: '2026-08-15T00:00:00Z'
}

describe('OpenAI 用户粘性调度设置 API', () => {
  beforeEach(() => {
    put.mockReset()
    put.mockResolvedValue({
      data: {
        config,
        effective_state: 'enforce',
        config_version: config.config_version
      }
    })
  })

  it('更新请求只发送配置和期望版本，不再发送变更原因', async () => {
    await updateOpenAIUserAffinityScheduling(config)

    expect(put).toHaveBeenCalledWith(
      '/admin/settings/openai-user-affinity-scheduling',
      { ...config, expected_version: config.config_version }
    )
    expect(put.mock.calls[0][1]).not.toHaveProperty('reason')
  })
})
