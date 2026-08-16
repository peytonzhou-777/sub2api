import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import GroupOptionItem from '../GroupOptionItem.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: null }),
}))

describe('GroupOptionItem', () => {
  it('applies multiline and overflow-safe text styles', () => {
    const description = 'First section\nvery-long-unbroken-description-value-that-must-not-overflow'
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'Example group',
        platform: 'openai',
        description,
      },
      global: {
        stubs: {
          GroupBadge: true,
        },
      },
    })

    const descriptionElement = wrapper
      .findAll('span')
      .find((element) => element.text() === description)

    expect(descriptionElement).toBeDefined()
    expect(descriptionElement?.classes()).toContain('whitespace-pre-line')
    expect(descriptionElement?.classes()).toContain('[overflow-wrap:anywhere]')
    expect(descriptionElement?.classes()).toContain('line-clamp-3')
    expect(wrapper.find('[title]').attributes('title')).toBe(description)
  })

  it('does not render deposit information for a group without a threshold', () => {
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'No deposit group',
        platform: 'openai',
        securityDepositBaseRequiredCents: 0,
      },
      global: { stubs: { GroupBadge: true } },
    })

    expect(wrapper.find('[data-test="group-security-deposit-status"]').exists()).toBe(false)
  })

  it('shows the required and current deposits in green when the threshold is met', () => {
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'Eligible group',
        platform: 'openai',
        securityDepositBaseRequiredCents: 10000,
        securityDepositEligibility: {
          group_id: 1,
          base_required_cents: 10000,
          risk_multiplier: 1,
          required_cents: 10000,
          effective_balance_cents: 12000,
          shortfall_cents: 0,
          eligible: true,
        },
      },
      global: { stubs: { GroupBadge: true } },
    })

    expect(wrapper.get('[data-test="group-security-deposit-required"]').text()).toBe('keys.securityDeposit.required')
    const current = wrapper.get('[data-test="group-security-deposit-current"]')
    expect(current.text()).toContain('keys.securityDeposit.current')
    expect(current.text()).toContain('keys.securityDeposit.available')
    expect(current.classes()).toContain('text-emerald-600')
  })

  it('shows the current deposit and shortfall in amber when the threshold is not met', () => {
    const wrapper = mount(GroupOptionItem, {
      props: {
        name: 'Insufficient group',
        platform: 'openai',
        securityDepositBaseRequiredCents: 10000,
        securityDepositEligibility: {
          group_id: 2,
          base_required_cents: 10000,
          risk_multiplier: 2,
          required_cents: 20000,
          effective_balance_cents: 5000,
          shortfall_cents: 15000,
          eligible: false,
        },
      },
      global: { stubs: { GroupBadge: true } },
    })

    expect(wrapper.get('[data-test="group-security-deposit-required"]').text()).toBe('keys.securityDeposit.personalRequired')
    const current = wrapper.get('[data-test="group-security-deposit-current"]')
    expect(current.text()).toContain('keys.securityDeposit.current')
    expect(current.text()).toContain('keys.securityDeposit.shortfall')
    expect(current.classes()).toContain('text-amber-600')
  })
})
