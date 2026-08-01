import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import SiteWordmark from '../SiteWordmark.vue'

describe('SiteWordmark', () => {
  it('在站点名称后追加紫色 API 字样', () => {
    const wrapper = mount(SiteWordmark, { props: { name: '皮蛋粥' } })

    expect(wrapper.text()).toBe('皮蛋粥API')
    expect(wrapper.find('.site-wordmark-suffix').text()).toBe('API')
  })
})
