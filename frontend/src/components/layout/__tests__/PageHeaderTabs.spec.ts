import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PageHeaderTabs from '../PageHeaderTabs.vue'

const tabs = [
  { key: 'general', label: '通用设置', icon: 'home' as const },
  { key: 'security', label: '安全设置', icon: 'shield' as const },
  { key: 'payment', label: '支付设置', icon: 'creditCard' as const },
]

afterEach(() => {
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('PageHeaderTabs', () => {
  it('renders the active Codex-style tab and emits selection changes', async () => {
    const wrapper = mount(PageHeaderTabs, {
      props: {
        tabs,
        activeKey: 'security',
        tablistLabel: '系统设置',
      },
      global: { stubs: { Icon: true } },
    })

    const activeTab = wrapper.get('[data-test="page-header-tab-security"]')
    expect(activeTab.attributes('aria-selected')).toBe('true')
    expect(activeTab.classes()).toContain('page-header-tab-active')

    await wrapper.get('[data-test="page-header-tab-payment"]').trigger('click')
    expect(wrapper.emitted('select')).toEqual([['payment']])
  })

  it('supports directional and boundary keyboard navigation', async () => {
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      callback(0)
      return 1
    })
    const wrapper = mount(PageHeaderTabs, {
      attachTo: document.body,
      props: {
        tabs,
        activeKey: 'general',
        tablistLabel: '系统设置',
      },
      global: { stubs: { Icon: true } },
    })

    await wrapper.get('[data-test="page-header-tab-general"]').trigger('keydown', { key: 'ArrowRight' })
    await wrapper.get('[data-test="page-header-tab-security"]').trigger('keydown', { key: 'End' })

    expect(wrapper.emitted('select')).toEqual([['security'], ['payment']])
    expect(document.activeElement?.id).toBe('page-header-tab-payment')
    wrapper.unmount()
  })
})
