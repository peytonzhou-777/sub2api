import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import HeroSloganCarousel from '../HeroSloganCarousel.vue'

describe('HeroSloganCarousel', () => {
  it('隐藏标题并渲染四组双标语与分组无障碍文本', () => {
    const wrapper = mount(HeroSloganCarousel)

    expect(wrapper.find('h2').exists()).toBe(false)
    expect(wrapper.get('section').attributes('aria-label')).toBe('选择本站理由')
    expect(wrapper.findAll('.codex-slogan-item')).toHaveLength(4)
    expect(wrapper.text()).toContain('省心注册')
    expect(wrapper.text()).toContain('一键接入')
    expect(wrapper.text()).toContain('定价透明')
    expect(wrapper.text()).toContain('安心调用')
    expect(wrapper.findAll('.codex-slogan-item')[1].attributes('aria-label')).toBe('额度同享、按量计费')
  })

  it('为各槽位设置递增错峰延时', () => {
    const wrapper = mount(HeroSloganCarousel)
    const delays = wrapper.findAll('.codex-slogan-item').map((item) => item.attributes('style'))

    expect(delays).toEqual([
      '--slogan-delay: 0ms;',
      '--slogan-delay: 180ms;',
      '--slogan-delay: 360ms;',
      '--slogan-delay: 540ms;',
    ])
  })

  it('仅将分隔符作为文案分组规则，不渲染可见竖线', () => {
    const wrapper = mount(HeroSloganCarousel)

    expect(wrapper.text()).not.toContain('|')
  })
})
