import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import SecurityDepositAccountPanel from '../SecurityDepositAccountPanel.vue'
import type { SecurityDepositAccount } from '@/types/securityDeposit'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) => {
      if (key === 'payment.securityDeposit.timedLocked') return `限时冻结 ${params?.amount}`
      if (key === 'payment.securityDeposit.permanentLocked') return `永久冻结 ${params?.amount}`
      return key
    },
  }),
}))

const account: SecurityDepositAccount = {
  currency: 'CNY', paid_balance_cents: 12345, admin_grant_balance_cents: 5000,
  total_balance_cents: 17345, effective_balance_cents: 17345, timed_locked_cents: 2345,
  permanent_locked_cents: 5000, refundable_cents: 10000, paid_refund_reserved_cents: 0,
  cyber_strike_count: 0, risk_multiplier: 1, max_risk_multiplier: 8, next_unlock_at: null,
  enforcement_enabled: false, self_refund_enabled: false, lots: [],
}

describe('SecurityDepositAccountPanel', () => {
  it('默认收起账户明细并可展开查看分项金额', async () => {
    const wrapper = mount(SecurityDepositAccountPanel, {
      props: { account, loading: false },
      global: { plugins: [createPinia()] },
    })

    expect(wrapper.get('[data-test="security-deposit-total"]').text()).toBe('¥173.45')
    expect(wrapper.find('[data-test="security-deposit-account-details"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="security-deposit-paid"]').exists()).toBe(false)

    const toggle = wrapper.get('[data-test="security-deposit-account-details-toggle"]')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    await toggle.trigger('click')

    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('[data-test="security-deposit-account-details"]').isVisible()).toBe(true)
    expect(wrapper.get('[data-test="security-deposit-paid"]').text()).toBe('¥123.45')
    expect(wrapper.get('[data-test="security-deposit-admin-grant"]').text()).toBe('¥50.00')
    expect(wrapper.text()).toContain('限时冻结 ¥23.45')
    expect(wrapper.text()).toContain('永久冻结 ¥50.00')

    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('[data-test="security-deposit-account-details"]').exists()).toBe(false)
  })
})
