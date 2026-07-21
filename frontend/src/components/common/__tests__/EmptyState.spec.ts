import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import EmptyState from '../EmptyState.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

describe('EmptyState', () => {
  it('renders text and actions without a decorative icon', () => {
    const wrapper = mount(EmptyState, {
      props: {
        title: '暂无数据',
        description: '暂时没有可展示的内容',
        actionText: '创建'
      },
      global: {
        stubs: { Icon: true }
      }
    })

    expect(wrapper.find('.empty-state-icon').exists()).toBe(false)
    expect(wrapper.get('.empty-state-title').text()).toBe('暂无数据')
    expect(wrapper.get('.empty-state-description').text()).toBe('暂时没有可展示的内容')
  })
})
