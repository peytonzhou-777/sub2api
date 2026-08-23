import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'

import AccountPoolPersonalUsageCell from '../AccountPoolPersonalUsageCell.vue'

const { getPersonalUsage } = vi.hoisted(() => ({
  getPersonalUsage: vi.fn(),
}))

vi.mock('@/api/accountPool', () => ({
  default: { getPersonalUsage },
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, locale: ref('zh-CN') }),
  }
})

const account = (id: number, platform = 'openai', type = 'oauth') => ({
  id,
  platform,
  type,
  capacity: { current_concurrency: null, max_concurrency: 10, observed_at: null, state: 'unavailable' },
  usage_windows: [],
  reset_count: null,
  reset_count_state: 'unavailable',
  status: { code: 'active', resume_at: null, models: [] },
  is_current_residence: false,
  is_seven_day_contact: false,
  is_historical_contact: false,
})

describe('AccountPoolPersonalUsageCell', () => {
  it('按账号查询并保留零值窗口指标', async () => {
    getPersonalUsage.mockResolvedValueOnce({
      account_id: 17,
      observed_at: new Date().toISOString(),
      windows: [
        { code: '5h', label: '5h', start_at: '2026-08-01T07:00:00Z', end_at: '2026-08-01T12:00:00Z', requests: 0, input_tokens: 0, output_tokens: 0, tokens: 0, cache_rate: 0.95, actual_cost: 0 },
        { code: '7d', label: '7d', start_at: '2026-07-25T12:00:00Z', end_at: '2026-08-01T12:00:00Z', requests: 4, input_tokens: 120, output_tokens: 80, tokens: 300, cache_rate: 0.5, actual_cost: 1.25 },
      ],
    })
    const wrapper = mount(AccountPoolPersonalUsageCell, {
      props: { account: account(17) },
      global: { stubs: { Icon: true } },
    })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(getPersonalUsage).toHaveBeenCalledWith(17, expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(wrapper.find('[data-test="personal-usage-5h"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="personal-usage-7d"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('accountPool.personalUsage.requests')
    expect(wrapper.text()).toContain('accountPool.personalUsage.input')
    expect(wrapper.text()).toContain('accountPool.personalUsage.output')
    expect(wrapper.text()).toContain('accountPool.personalUsage.total')
    expect(wrapper.text()).toContain('accountPool.personalUsage.cacheRate')
    expect(wrapper.text()).toContain('accountPool.personalUsage.billing')
    expect(wrapper.text()).toContain('95%')
    expect(wrapper.text()).toContain('0.00$')
    expect(wrapper.text()).toContain('1.25$')
    wrapper.unmount()
  })

  it('非 OAuth/Setup Token 账号不展示查询入口', () => {
    const wrapper = mount(AccountPoolPersonalUsageCell, {
      props: { account: account(18, 'openai', 'apikey') },
      global: { stubs: { Icon: true } },
    })

    expect(wrapper.find('button').exists()).toBe(false)
    expect(wrapper.text()).toBe('--')
    wrapper.unmount()
  })
})
