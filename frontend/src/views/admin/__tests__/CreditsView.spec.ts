import { defineComponent } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import CreditsView from '../CreditsView.vue'

const {
  listCreditUsers,
  getCreditUser,
  adjustBalance,
  createLimitedCredit,
  adjustLimitedCredit,
  resetLimitedCredit,
  revokeLimitedCredit,
  listLimitedCreditLedger,
  getUserBalanceHistory,
  showSuccess,
  showError,
  replace,
} = vi.hoisted(() => ({
  listCreditUsers: vi.fn(),
  getCreditUser: vi.fn(),
  adjustBalance: vi.fn(),
  createLimitedCredit: vi.fn(),
  adjustLimitedCredit: vi.fn(),
  resetLimitedCredit: vi.fn(),
  revokeLimitedCredit: vi.fn(),
  listLimitedCreditLedger: vi.fn(),
  getUserBalanceHistory: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
  replace: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  creditsAPI: {
    listCreditUsers,
    getCreditUser,
    adjustBalance,
    createLimitedCredit,
    adjustLimitedCredit,
    resetLimitedCredit,
    revokeLimitedCredit,
    listLimitedCreditLedger,
  },
  adminAPI: {
    users: {
      getUserBalanceHistory,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace }),
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

function createCreditUser(overrides: Record<string, unknown> = {}) {
  return {
    id: 7,
    email: 'credit-user@example.com',
    username: 'credit-user',
    status: 'active',
    balance: 10,
    frozen_balance: 0,
    limited_remaining_amount: 0,
    limited_active_count: 0,
    updated_at: '2026-07-20T00:00:00Z',
    ...overrides,
  }
}

function createDetail(overrides: Record<string, unknown> = {}) {
  return {
    ...createCreditUser(),
    limited_credits: [],
    ...overrides,
  }
}

function mountView() {
  return mount(CreditsView, {
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        BaseDialog: BaseDialogStub,
        Select: true,
        Icon: true,
        Teleport: true,
      },
    },
  })
}

function findButtonByText(wrapper: VueWrapper, text: string) {
  const button = wrapper.findAll('button').find(candidate => candidate.text() === text)
  if (!button) throw new Error(`未找到按钮：${text}`)
  return button
}

function conflictDialog(wrapper: VueWrapper) {
  const dialog = wrapper.findComponent(ConfirmDialog)
  if (!dialog.exists()) throw new Error('未找到额度冲突确认弹窗')
  return dialog
}

async function openBalanceForm(
  wrapper: VueWrapper,
  options: { amount?: number; notes?: string } = {},
) {
  await findButtonByText(wrapper, 'admin.credits.manage').trigger('click')
  await flushPromises()

  await findButtonByText(wrapper, 'admin.credits.add').trigger('click')
  await wrapper.vm.$nextTick()

  const form = wrapper.get('#credit-action')
  await form.get('input[type="number"]').setValue(options.amount ?? 12.5)
  await form.get('textarea').setValue(options.notes ?? '保持原始备注')
  return form
}

async function submitPermanentBalance(
  wrapper: VueWrapper,
  options: { amount?: number; notes?: string } = {},
) {
  const form = await openBalanceForm(wrapper, options)
  await form.trigger('submit')
  await flushPromises()
}

describe('额度管理永久额度冲突处理', () => {
  beforeEach(() => {
    for (const fn of [
      listCreditUsers,
      getCreditUser,
      adjustBalance,
      createLimitedCredit,
      adjustLimitedCredit,
      resetLimitedCredit,
      revokeLimitedCredit,
      listLimitedCreditLedger,
      getUserBalanceHistory,
      showSuccess,
      showError,
      replace,
    ]) {
      fn.mockReset()
    }

    listCreditUsers.mockResolvedValue({
      items: [createCreditUser()],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    getCreditUser.mockResolvedValue(createDetail())
    adjustBalance.mockResolvedValue(createDetail())
    createLimitedCredit.mockResolvedValue({})
    adjustLimitedCredit.mockResolvedValue({})
    resetLimitedCredit.mockResolvedValue({})
    revokeLimitedCredit.mockResolvedValue({})
    listLimitedCreditLedger.mockResolvedValue([])
    getUserBalanceHistory.mockResolvedValue({ items: [] })
    replace.mockResolvedValue(undefined)
  })

  it('首次 409 会刷新用户详情并显示确认弹窗，不会自动提交第二次请求', async () => {
    const initial = createDetail({ updated_at: '2026-07-20T00:00:00Z' })
    const fresh = createDetail({ updated_at: '2026-07-20T01:00:00Z', balance: 11 })
    getCreditUser.mockResolvedValueOnce(initial).mockResolvedValueOnce(fresh)
    adjustBalance.mockRejectedValueOnce({ status: 409, message: '用户资料已发生变化' })

    const wrapper = mountView()
    await flushPromises()
    await submitPermanentBalance(wrapper, { amount: 12.5, notes: '首次调整备注' })

    expect(adjustBalance).toHaveBeenCalledTimes(1)
    expect(adjustBalance).toHaveBeenCalledWith(7, {
      operation: 'add',
      amount: 12.5,
      notes: '首次调整备注',
      expected_updated_at: '2026-07-20T00:00:00Z',
    })
    expect(getCreditUser).toHaveBeenCalledTimes(2)
    expect(getCreditUser).toHaveBeenNthCalledWith(2, 7)

    const dialog = conflictDialog(wrapper)
    expect(dialog.props('show')).toBe(true)
    expect(dialog.props('title')).toBe('admin.credits.balanceConflict.title')
    expect(dialog.props('message')).toBe('admin.credits.balanceConflict.message')
    expect(dialog.props('confirmText')).toBe('admin.credits.balanceConflict.retry')
    wrapper.unmount()
  })

  it('取消冲突确认后不发送第二次请求，且保留管理员填写的表单内容', async () => {
    const initial = createDetail({ updated_at: '2026-07-20T00:00:00Z' })
    const fresh = createDetail({ updated_at: '2026-07-20T01:00:00Z' })
    getCreditUser.mockResolvedValueOnce(initial).mockResolvedValueOnce(fresh)
    adjustBalance.mockRejectedValueOnce({ status: 409, message: '用户资料已发生变化' })

    const wrapper = mountView()
    await flushPromises()
    await submitPermanentBalance(wrapper, { amount: 7.25, notes: '取消后保留的备注' })

    conflictDialog(wrapper).vm.$emit('cancel')
    await flushPromises()

    expect(adjustBalance).toHaveBeenCalledTimes(1)
    expect(conflictDialog(wrapper).props('show')).toBe(false)
    expect((wrapper.get('#credit-action input[type="number"]').element as HTMLInputElement).value).toBe('7.25')
    expect((wrapper.get('#credit-action textarea').element as HTMLTextAreaElement).value).toBe('取消后保留的备注')
    wrapper.unmount()
  })

  it('确认重试只发送一次，并使用刷新后的版本和原表单数据，成功后刷新详情与列表', async () => {
    const initial = createDetail({ updated_at: '2026-07-20T00:00:00Z' })
    const fresh = createDetail({ updated_at: '2026-07-20T01:00:00Z', balance: 11 })
    const afterSuccess = createDetail({ updated_at: '2026-07-20T02:00:00Z', balance: 23.5 })
    getCreditUser
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(fresh)
      .mockResolvedValueOnce(afterSuccess)
    adjustBalance
      .mockRejectedValueOnce({ status: 409, message: '用户资料已发生变化' })
      .mockResolvedValueOnce(afterSuccess)

    const wrapper = mountView()
    await flushPromises()
    await submitPermanentBalance(wrapper, { amount: 12.5, notes: '重试保持的备注' })

    conflictDialog(wrapper).vm.$emit('confirm')
    await flushPromises()

    expect(adjustBalance).toHaveBeenCalledTimes(2)
    expect(adjustBalance).toHaveBeenNthCalledWith(2, 7, {
      operation: 'add',
      amount: 12.5,
      notes: '重试保持的备注',
      expected_updated_at: '2026-07-20T01:00:00Z',
    })
    expect(getCreditUser).toHaveBeenCalledTimes(3)
    expect(listCreditUsers).toHaveBeenCalledTimes(2)
    expect(showSuccess).toHaveBeenCalledWith('common.success')
    expect(wrapper.find('#credit-action').exists()).toBe(false)
    wrapper.unmount()
  })

  it('确认重试再次返回 409 时只刷新一次详情并停止重试', async () => {
    const initial = createDetail({ updated_at: '2026-07-20T00:00:00Z' })
    const fresh = createDetail({ updated_at: '2026-07-20T01:00:00Z' })
    const latest = createDetail({ updated_at: '2026-07-20T02:00:00Z' })
    getCreditUser
      .mockResolvedValueOnce(initial)
      .mockResolvedValueOnce(fresh)
      .mockResolvedValueOnce(latest)
    adjustBalance
      .mockRejectedValueOnce({ status: 409, message: '首次冲突' })
      .mockRejectedValueOnce({
        response: {
          status: 409,
          data: { message: '第二次冲突' },
        },
        message: 'Request failed with status code 409',
      })

    const wrapper = mountView()
    await flushPromises()
    await submitPermanentBalance(wrapper, { amount: 3, notes: '二次冲突备注' })

    conflictDialog(wrapper).vm.$emit('confirm')
    await flushPromises()

    expect(adjustBalance).toHaveBeenCalledTimes(2)
    expect(getCreditUser).toHaveBeenCalledTimes(3)
    expect(getCreditUser).toHaveBeenNthCalledWith(3, 7)
    expect(conflictDialog(wrapper).props('show')).toBe(false)
    expect(showError).toHaveBeenCalledWith('admin.credits.balanceConflict.retryExhausted')
    wrapper.unmount()
  })

  it('永久额度已提交成功后详情刷新返回 409 时不会重试', async () => {
    const initial = createDetail({ updated_at: '2026-07-20T00:00:00Z' })
    getCreditUser
      .mockResolvedValueOnce(initial)
      .mockRejectedValueOnce({ status: 409, message: '刷新详情失败' })
    adjustBalance.mockResolvedValueOnce(createDetail())

    const wrapper = mountView()
    await flushPromises()
    await submitPermanentBalance(wrapper, { amount: 5, notes: '刷新失败不重放' })

    expect(adjustBalance).toHaveBeenCalledTimes(1)
    expect(getCreditUser).toHaveBeenCalledTimes(2)
    expect(listCreditUsers).toHaveBeenCalledTimes(1)
    expect(conflictDialog(wrapper).props('show')).toBe(false)
    expect(showError).toHaveBeenCalledWith('刷新详情失败')
    expect(showSuccess).toHaveBeenCalledWith('common.success')
    expect(wrapper.find('#credit-action').exists()).toBe(false)
    wrapper.unmount()
  })
  it('关闭操作后会忽略旧冲突响应，且不会覆盖当前详情版本', async () => {
    const initial = createDetail({ updated_at: '2026-07-20T00:00:00Z' })
    const fresh = createDetail({ updated_at: '2026-07-20T01:00:00Z' })
    let resolveFresh: ((value: ReturnType<typeof createDetail>) => void) | undefined
    const freshPromise = new Promise<ReturnType<typeof createDetail>>(resolve => {
      resolveFresh = resolve
    })
    getCreditUser
      .mockResolvedValueOnce(initial)
      .mockImplementationOnce(() => freshPromise)
    adjustBalance.mockRejectedValueOnce({ status: 409, message: '用户资料已发生变化' })

    const wrapper = mountView()
    await flushPromises()
    const form = await openBalanceForm(wrapper, { amount: 9, notes: '旧操作' })
    await form.trigger('submit')
    await flushPromises()

    expect(getCreditUser).toHaveBeenCalledTimes(2)
    await findButtonByText(wrapper, 'common.cancel').trigger('click')
    await wrapper.vm.$nextTick()
    await findButtonByText(wrapper, 'admin.credits.add').trigger('click')
    await wrapper.vm.$nextTick()

    if (!resolveFresh) throw new Error('未捕获详情刷新回调')
    resolveFresh(fresh)
    await flushPromises()

    expect(adjustBalance).toHaveBeenCalledTimes(1)
    expect(conflictDialog(wrapper).props('show')).toBe(false)
    expect(wrapper.find('#credit-action').exists()).toBe(true)

    const newForm = wrapper.get('#credit-action')
    await newForm.get('input[type="number"]').setValue(4)
    await newForm.get('textarea').setValue('新操作')
    await newForm.trigger('submit')
    await flushPromises()

    expect(adjustBalance).toHaveBeenCalledTimes(2)
    expect(adjustBalance).toHaveBeenNthCalledWith(2, 7, {
      operation: 'add',
      amount: 4,
      notes: '新操作',
      expected_updated_at: '2026-07-20T00:00:00Z',
    })
    expect(wrapper.find('#credit-action').exists()).toBe(false)
    wrapper.unmount()
  })


  it.each([
    ['拦截器扁平错误', { status: 422, code: 'INVALID_AMOUNT', message: '扁平错误消息' }, '扁平错误消息'],
    [
      'Axios 原始错误',
      {
        response: {
          status: 422,
          data: { message: 'Axios 响应错误消息' },
        },
        message: 'Request failed with status code 422',
      },
      'Axios 响应错误消息',
    ],
    [
      'Axios 原始错误缺少响应消息',
      {
        response: {
          status: 500,
          data: {},
        },
        message: 'Request failed with status code 500',
      },
      'common.error',
    ],
    [
      '缺少 message 的扁平错误',
      { status: 500, error: 'INTERNAL_ERROR', reason: 'INTERNAL_ERROR' },
      'common.error',
    ],
  ])('%s 不进入冲突流程，并显示后端错误消息', async (_name, error, expectedMessage) => {
    const initial = createDetail({ updated_at: '2026-07-20T00:00:00Z' })
    getCreditUser.mockResolvedValueOnce(initial)
    adjustBalance.mockRejectedValueOnce(error)

    const wrapper = mountView()
    await flushPromises()
    await submitPermanentBalance(wrapper)

    expect(adjustBalance).toHaveBeenCalledTimes(1)
    expect(getCreditUser).toHaveBeenCalledTimes(1)
    expect(conflictDialog(wrapper).props('show')).toBe(false)
    expect(showError).toHaveBeenCalledWith(expectedMessage)
    wrapper.unmount()
  })
})
