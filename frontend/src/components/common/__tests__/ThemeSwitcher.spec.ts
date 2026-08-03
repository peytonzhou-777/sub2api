import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ThemeSwitcher from '@/components/common/ThemeSwitcher.vue'
import { disposeTheme, initTheme, useTheme } from '@/composables/useTheme'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('ThemeSwitcher', () => {
  beforeEach(() => {
    disposeTheme()
    window.localStorage.clear()
    document.documentElement.className = ''
    vi.stubGlobal('matchMedia', vi.fn(() => ({
      matches: false,
      media: '(prefers-color-scheme: dark)',
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })))
    initTheme()
  })

  afterEach(() => {
    disposeTheme()
    vi.unstubAllGlobals()
  })

  it('展示三种模式并立即应用选择', async () => {
    const wrapper = mount(ThemeSwitcher, { attachTo: document.body })

    await wrapper.get('button[aria-haspopup="menu"]').trigger('click')
    const options = wrapper.findAll('[role="radio"]')
    expect(options).toHaveLength(3)
    expect(options[2].attributes('aria-checked')).toBe('true')

    await options[1].trigger('click')

    expect(useTheme().themeMode.value).toBe('dark')
    expect(useTheme().resolvedTheme.value).toBe('dark')
    expect(window.localStorage.getItem('theme')).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(wrapper.get('button[aria-haspopup="menu"]').attributes('aria-expanded')).toBe('false')

    wrapper.unmount()
  })

  it('支持 Escape 和点击外部关闭菜单', async () => {
    const wrapper = mount(ThemeSwitcher, { attachTo: document.body })
    const trigger = wrapper.get('button[aria-haspopup="menu"]')

    await trigger.trigger('click')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(trigger.attributes('aria-expanded')).toBe('false')

    await trigger.trigger('click')
    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await wrapper.vm.$nextTick()
    expect(trigger.attributes('aria-expanded')).toBe('false')

    wrapper.unmount()
  })
})
