import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AmountInput from '../AmountInput.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('AmountInput slider and input synchronization', () => {
  it('renders the requested quick amounts above one slider and one number input', () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 10, min: 5, max: 100 } })

    expect(wrapper.get('[data-test="amount-slider"]').attributes()).toMatchObject({
      min: '0',
      max: '95',
      step: '1',
    })
    expect(wrapper.get('[data-test="amount-number-input"]').attributes('value')).toBe('10')
    expect(wrapper.findAll('[data-test^="quick-amount-"]').map(button => button.text())).toEqual([
      '$10',
      '$20',
      '$50',
      '$100',
      '$200',
      '$500',
      '$800',
      '$1000',
    ])
    expect(wrapper.findAll('.amount-adjustment-button')).toHaveLength(6)
  })

  it('synchronizes a quick amount with the number input and slider', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 10, min: 1, max: 1000 } })

    await wrapper.get('[data-test="quick-amount-200"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([200])
    expect(wrapper.get('[data-test="amount-number-input"]').attributes('value')).toBe('200')

    await wrapper.setProps({ modelValue: 200 })

    expect(wrapper.get('[data-test="amount-slider"]').attributes('value')).toBe('119')
    expect(wrapper.get('[data-test="quick-amount-200"]').attributes('aria-pressed')).toBe('true')
  })

  it('disables quick amounts outside the configured range', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 20, min: 20, max: 500 } })

    expect(wrapper.get('[data-test="quick-amount-10"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="quick-amount-20"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-test="quick-amount-500"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-test="quick-amount-800"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="quick-amount-1000"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-test="quick-amount-800"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('uses gradient slider steps across the three amount segments', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 10, min: 1, max: 600 } })
    const slider = wrapper.get('[data-test="amount-slider"]')

    await slider.setValue('99')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([100])
    await slider.setValue('100')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([105])
    await slider.setValue('179')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([500])
    await slider.setValue('180')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([510])
  })

  it('keeps manually entered amounts independent from slider steps', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 100, min: 1, max: 600 } })
    const input = wrapper.get('[data-test="amount-number-input"]')

    await input.setValue('103.5')
    await input.trigger('blur')

    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([103.5])
  })

  it('clamps typed values to the configured range on blur', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 10, min: 5, max: 100 } })
    const input = wrapper.get('[data-test="amount-number-input"]')

    await input.setValue('120')
    await input.trigger('blur')

    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([100])
  })

  it('expands the slider range for typed values when no maximum is configured', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 10, min: 1, max: 0 } })

    await wrapper.setProps({ modelValue: 6000 })

    const slider = wrapper.get('[data-test="amount-slider"]')
    expect(slider.attributes('max')).toBe('729')
    expect(slider.attributes('value')).toBe('729')
  })

  it('disables adjustments that would cross limits and emits valid adjustments', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: 10, min: 1, max: 15 } })

    expect(wrapper.get('[data-test="amount-adjust--10"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="amount-adjust-10"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="amount-adjust--5"]').attributes('disabled')).toBeUndefined()

    await wrapper.get('[data-test="amount-adjust--5"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([5])
  })
})
