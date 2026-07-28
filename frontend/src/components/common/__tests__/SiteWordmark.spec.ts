import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SiteWordmark from '../SiteWordmark.vue'

describe('SiteWordmark', () => {
  it('未配置时在站点名称后追加默认 API 字样', () => {
    const wrapper = mount(SiteWordmark, { props: { name: '皮蛋粥' } })

    expect(wrapper.text()).toBe('皮蛋粥API')
    expect(wrapper.find('.site-wordmark-suffix').text()).toBe('API')
  })

  it('展示管理员配置的品牌后缀', () => {
    const wrapper = mount(SiteWordmark, { props: { name: '皮蛋粥', suffix: 'Pro' } })

    expect(wrapper.text()).toBe('皮蛋粥Pro')
    expect(wrapper.find('.site-wordmark-suffix').attributes('aria-label')).toBe('Pro')
  })
})
