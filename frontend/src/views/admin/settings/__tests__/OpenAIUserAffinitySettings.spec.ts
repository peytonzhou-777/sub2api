import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import OpenAIUserAffinitySettings from '../OpenAIUserAffinitySettings.vue'

const getScheduling = vi.fn()
const updateScheduling = vi.fn()

vi.mock('@/api', () => ({
  adminAPI: {
    settings: {
      getOpenAIUserAffinityScheduling: (...args: unknown[]) => getScheduling(...args),
      updateOpenAIUserAffinityScheduling: (...args: unknown[]) => updateScheduling(...args)
    }
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: () => 'error'
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const config = {
  enabled: false,
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
  conversation_identity_ttl_seconds: 604800,
  config_version: 1,
  updated_at: '2026-08-15T00:00:00Z'
}

describe('OpenAIUserAffinitySettings', () => {
  beforeEach(() => {
    getScheduling.mockReset()
    updateScheduling.mockReset()
    getScheduling.mockResolvedValue({
      config: { ...config },
      effective_state: 'disabled',
      config_version: 1
    })
    updateScheduling.mockResolvedValue({
      config: { ...config, enabled: true, config_version: 2 },
      effective_state: 'enforce',
      config_version: 2
    })
  })

  it('关闭时收起详细配置，启用草稿后展开并始终保留保存入口', async () => {
    const wrapper = mount(OpenAIUserAffinitySettings)
    await flushPromises()

    expect(wrapper.find('[data-test="affinity-details"]').exists()).toBe(false)
    expect(wrapper.find('button.btn-primary').exists()).toBe(true)

    await wrapper.get('button[role="switch"]').trigger('click')

    expect(wrapper.find('[data-test="affinity-details"]').exists()).toBe(true)
    expect(wrapper.find('input[type="text"]').exists()).toBe(false)
    expect(wrapper.findAll('input[type="number"]')).toHaveLength(15)
    const identityTTL = wrapper.findAll('label').find(label => label.text().includes('admin.settings.openAIUserAffinity.conversationIdentityTTL'))
    expect(identityTTL).toBeDefined()
    await identityTTL!.get('input').setValue('432000')

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(updateScheduling).toHaveBeenCalledWith(expect.objectContaining({ enabled: true, conversation_identity_ttl_seconds: 432000 }))
    expect(updateScheduling.mock.calls[0]).toHaveLength(1)
  })
})
