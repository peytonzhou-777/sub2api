import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AmountInput from '../AmountInput.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('AmountInput quick amount bonuses', () => {
  it('shows two-decimal bonus amounts in the upper-right corner', () => {
    const wrapper = mount(AmountInput, {
      props: {
        modelValue: null,
        amounts: [10, 20],
        bonusAmounts: { 10: 5, 20: 6.36363636 },
      },
    })

    const bonuses = wrapper.findAll('[data-test="quick-amount-bonus"]')
    expect(bonuses.map(item => item.text())).toEqual(['+5.00$', '+6.36$'])
    expect(bonuses.every(item => item.classes().includes('text-red-600'))).toBe(true)
  })

  it('does not show a badge when the amount has no bonus', () => {
    const wrapper = mount(AmountInput, {
      props: { modelValue: null, amounts: [10], bonusAmounts: {} },
    })

    expect(wrapper.find('[data-test="quick-amount-bonus"]').exists()).toBe(false)
  })
})
