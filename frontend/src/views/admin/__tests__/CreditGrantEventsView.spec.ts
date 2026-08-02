import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import CreditGrantEventsView from '../CreditGrantEventsView.vue'

const {
  listCreditGrantEvents,
  createCreditGrantEvent,
  updateCreditGrantEvent,
  deleteCreditGrantEvent,
  showSuccess,
  showError,
} = vi.hoisted(() => ({
  listCreditGrantEvents: vi.fn(),
  createCreditGrantEvent: vi.fn(),
  updateCreditGrantEvent: vi.fn(),
  deleteCreditGrantEvent: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  creditsAPI: {
    listCreditGrantEvents,
    createCreditGrantEvent,
    updateCreditGrantEvent,
    deleteCreditGrantEvent,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
    title: { type: String, default: '' },
  },
  emits: ['close'],
  template: `
    <section v-if="show" data-test="base-dialog">
      <h2>{{ title }}</h2>
      <slot />
      <footer><slot name="footer" /></footer>
    </section>
  `,
})

const ConfirmDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
  },
  emits: ['confirm', 'cancel'],
  template: '<button v-if="show" data-test="confirm-delete" @click="$emit(\'confirm\')">confirm</button>',
})

function createEvent(overrides: Record<string, unknown> = {}) {
  return {
    id: 3,
    name: '新用户赠额',
    credit_type: 'permanent',
    amount: 5,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T01:00:00Z',
    ...overrides,
  }
}

function mountView() {
  return mount(CreditGrantEventsView, {
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        BaseDialog: BaseDialogStub,
        ConfirmDialog: ConfirmDialogStub,
        Pagination: true,
        Icon: true,
      },
    },
  })
}

function findButtonByText(wrapper: VueWrapper, text: string) {
  const button = wrapper.findAll('button').find(candidate => candidate.text() === text)
  if (!button) throw new Error(`未找到按钮：${text}`)
  return button
}

describe('赠额事件管理页', () => {
  beforeEach(() => {
    for (const fn of [
      listCreditGrantEvents,
      createCreditGrantEvent,
      updateCreditGrantEvent,
      deleteCreditGrantEvent,
      showSuccess,
      showError,
    ]) fn.mockReset()

    listCreditGrantEvents.mockResolvedValue({
      items: [createEvent()],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    createCreditGrantEvent.mockResolvedValue(createEvent())
    updateCreditGrantEvent.mockResolvedValue(createEvent())
    deleteCreditGrantEvent.mockResolvedValue({ message: 'ok' })
  })

  it('创建限时事件时展示有效期并提交限时配置', async () => {
    const wrapper = mountView()
    await flushPromises()

    await findButtonByText(wrapper, 'admin.credits.grantEvents.create').trigger('click')
    await findButtonByText(wrapper, 'admin.credits.limited').trigger('click')

    const form = wrapper.get('#credit-grant-event-form')
    await form.get('input[type="text"], input:not([type])').setValue('限时欢迎赠额')
    const numericInputs = form.findAll('input[type="number"]')
    expect(numericInputs).toHaveLength(2)
    await numericInputs[0].setValue(12.5)
    await numericInputs[1].setValue(45)
    await form.trigger('submit')
    await flushPromises()

    expect(createCreditGrantEvent).toHaveBeenCalledWith({
      name: '限时欢迎赠额',
      credit_type: 'limited',
      amount: 12.5,
      validity_days: 45,
    })
    expect(listCreditGrantEvents).toHaveBeenCalledTimes(2)
    expect(showSuccess).toHaveBeenCalledWith('common.success')
    wrapper.unmount()
  })

  it('编辑携带版本时间，删除经确认后执行软删除接口', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="common.edit"]').trigger('click')
    const form = wrapper.get('#credit-grant-event-form')
    await form.get('input[type="text"], input:not([type])').setValue('改名后的赠额')
    await form.trigger('submit')
    await flushPromises()

    expect(updateCreditGrantEvent).toHaveBeenCalledWith(3, {
      name: '改名后的赠额',
      credit_type: 'permanent',
      amount: 5,
      expected_updated_at: '2026-08-01T01:00:00Z',
    })

    await wrapper.get('button[title="common.delete"]').trigger('click')
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(deleteCreditGrantEvent).toHaveBeenCalledWith(3, '2026-08-01T01:00:00Z')
    expect(showSuccess).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
