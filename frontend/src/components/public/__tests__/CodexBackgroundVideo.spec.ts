import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import CodexBackgroundVideo from '../CodexBackgroundVideo.vue'

describe('CodexBackgroundVideo', () => {
  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('减少动态效果时直接显示本地海报', async () => {
    vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: true } as MediaQueryList)
    const wrapper = mount(CodexBackgroundVideo)
    await wrapper.vm.$nextTick()

    expect(wrapper.find('img').attributes('src')).toBe('/assets/codex/floral-a-poster.webp')
    expect(wrapper.find('video').exists()).toBe(false)
  })

  it('常规模式使用本地视频并在失败时降级', async () => {
    vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: false } as MediaQueryList)
    vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue()
    const wrapper = mount(CodexBackgroundVideo)

    expect(wrapper.find('video').attributes('src')).toBe('/assets/codex/floral-a.mp4')
    await wrapper.find('video').trigger('error')
    expect(wrapper.find('img').exists()).toBe(true)
  })
})
