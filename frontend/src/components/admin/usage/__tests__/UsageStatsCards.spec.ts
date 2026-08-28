import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageStatsCards from '../UsageStatsCards.vue'

const messages: Record<string, string> = {
  'usage.totalRequests': 'Total Requests',
  'usage.inSelectedRange': 'in selected range',
  'usage.totalTokens': 'Total Tokens',
  'usage.in': 'In',
  'usage.out': 'Out',
  'usage.cacheTotal': 'Cache',
  'usage.cacheRate': 'Cache rate',
  'usage.cacheBreakdown': 'Cache Token Breakdown',
  'usage.cacheCreationTokensLabel': 'Cache Creation',
  'usage.cacheReadTokensLabel': 'Cache Read',
  'usage.totalCost': 'Total Cost',
  'usage.spendingRankExact': 'Global rank #{rank}',
  'usage.spendingRankTop': 'Top {rank} globally',
  'usage.accountCost': 'Cost',
  'usage.standardCost': 'Standard',
  'usage.avgDuration': 'Avg Duration',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const stats = {
  total_requests: 1,
  total_input_tokens: 100,
  total_output_tokens: 50,
  total_cache_tokens: 34,
  total_cache_creation_tokens: 12,
  total_cache_read_tokens: 22,
  total_tokens: 184,
  total_cost: 0.001,
  total_actual_cost: 0.001,
  total_account_cost: 0.001,
  average_duration_ms: 250,
}

describe('UsageStatsCards', () => {
  it('shows cache token breakdown values', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('Cache: 34')
    expect(text).toContain('Cache Token Breakdown')
    expect(text).toContain('Cache Creation')
    expect(text).toContain('12')
    expect(text).toContain('Cache Read')
    expect(text).toContain('22')
  })

  it('shows cache rate with threshold colors', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats: {
          ...stats,
          total_input_tokens: 10,
          total_tokens: 100,
          total_cache_tokens: 90,
          total_cache_creation_tokens: 0,
          total_cache_read_tokens: 90,
        },
      },
      global: {
        stubs: { Icon: true },
      },
    })

    const rate = wrapper.find('p span.text-green-600')
    expect(rate.exists()).toBe(true)
    expect(wrapper.text()).toContain('Cache rate: 90.0%')
  })

  it('uses yellow from 80% and red below 80%', async () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats: {
          ...stats,
          total_input_tokens: 20,
          total_tokens: 100,
          total_cache_tokens: 80,
          total_cache_creation_tokens: 0,
          total_cache_read_tokens: 80,
        },
      },
      global: {
        stubs: { Icon: true },
      },
    })
    expect(wrapper.find('p span.text-yellow-600').exists()).toBe(true)

    await wrapper.setProps({
      stats: {
        ...stats,
        total_input_tokens: 30,
        total_tokens: 100,
        total_cache_tokens: 70,
        total_cache_creation_tokens: 0,
        total_cache_read_tokens: 70,
      },
    })
    expect(wrapper.find('p span.text-red-600').exists()).toBe(true)
  })

  it('shows the configured global spending rank beside total cost', () => {
    const wrapper = mount(UsageStatsCards, {
      props: {
        stats: {
          ...stats,
          spending_rank: { visibility: 'exact', rank: 7 },
        },
      },
      global: {
        stubs: { Icon: true },
      },
    })

    expect(wrapper.text()).toContain('Global rank #{rank}')
  })
})
