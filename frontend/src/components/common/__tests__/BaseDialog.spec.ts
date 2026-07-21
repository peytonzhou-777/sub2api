import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import BaseDialog from '../BaseDialog.vue'

describe('BaseDialog', () => {
  it('uses the Codex seam classes between dialog sections', () => {
    const wrapper = mount(BaseDialog, {
      props: { show: true, title: '测试弹窗' },
      slots: { default: '内容', footer: '操作' },
      global: {
        stubs: { Teleport: true, Transition: false, Icon: true }
      }
    })

    expect(wrapper.get('.modal-header').classes()).toContain('codex-seam-bottom')
    expect(wrapper.get('.modal-footer').classes()).toContain('codex-seam-top')
  })
})
