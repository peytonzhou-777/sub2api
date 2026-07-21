import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import Toggle from '../Toggle.vue'

describe('Toggle', () => {
  it('renders Codex switch states and emits the next value', async () => {
    const wrapper = mount(Toggle, { props: { modelValue: false } })
    const button = wrapper.get('[role="switch"]')

    expect(button.classes()).toContain('codex-switch-off')
    expect(button.attributes('aria-checked')).toBe('false')

    await button.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[true]])

    await wrapper.setProps({ modelValue: true })
    expect(button.classes()).toContain('codex-switch-on')
    expect(button.get('.codex-switch-thumb').classes()).toContain('codex-switch-thumb-on')
    expect(button.attributes('aria-checked')).toBe('true')
  })
})
