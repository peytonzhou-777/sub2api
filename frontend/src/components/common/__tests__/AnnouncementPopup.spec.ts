import { createPinia, setActivePinia } from 'pinia'
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AnnouncementPopup from '../AnnouncementPopup.vue'
import { useAnnouncementStore } from '@/stores/announcements'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

describe('AnnouncementPopup', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    document.body.className = ''
    setActivePinia(createPinia())
  })

  it('使用统一的玻璃弹层展示并安全渲染公告内容', async () => {
    const store = useAnnouncementStore()
    store.currentPopup = {
      id: 7,
      title: '系统维护通知',
      content: '**维护时间**<script>alert(1)</script>',
      notify_mode: 'popup',
      created_at: '2026-07-12T00:00:00Z',
      read_at: null,
    }

    const wrapper = mount(AnnouncementPopup, { attachTo: document.body })
    const dialog = document.body.querySelector('[role="dialog"]')

    expect(dialog).not.toBeNull()
    expect(dialog?.querySelector('.announcement-popup-panel')).not.toBeNull()
    expect(dialog?.textContent).toContain('系统维护通知')
    expect(dialog?.querySelector('strong')?.textContent).toBe('维护时间')
    expect(dialog?.querySelector('script')).toBeNull()
    expect(document.body.classList.contains('announcement-popup-open')).toBe(true)

    wrapper.unmount()
    expect(document.body.classList.contains('announcement-popup-open')).toBe(false)
  })

  it('支持按钮与 Escape 键关闭公告', async () => {
    const store = useAnnouncementStore()
    store.currentPopup = {
      id: 8,
      title: '新公告',
      content: '公告正文',
      notify_mode: 'popup',
      created_at: '2026-07-12T00:00:00Z',
      read_at: null,
    }
    const dismiss = vi.spyOn(store, 'dismissPopup').mockResolvedValue()
    const wrapper = mount(AnnouncementPopup, { attachTo: document.body })

    const action = document.body.querySelector<HTMLButtonElement>('.announcement-popup-action')
    expect(action).not.toBeNull()
    action?.click()
    await wrapper.vm.$nextTick()
    expect(dismiss).toHaveBeenCalledTimes(1)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    expect(dismiss).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
