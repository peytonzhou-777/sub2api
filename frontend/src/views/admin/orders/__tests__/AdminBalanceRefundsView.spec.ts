import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AdminBalanceRefundsView from '../AdminBalanceRefundsView.vue'

const { getSummary, getList, getDetail, start, action, reconcile, showError, replace } = vi.hoisted(() => ({
  getSummary: vi.fn(),
  getList: vi.fn(),
  getDetail: vi.fn(),
  start: vi.fn(),
  action: vi.fn(),
  reconcile: vi.fn(),
  showError: vi.fn(),
  replace: vi.fn(),
}))

vi.mock('@/api/admin/accountRefunds', () => ({
  adminAccountRefundAPI: { getSummary, getList, getDetail, start, action, reconcile },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError }),
}))

vi.mock('@/utils/apiError', () => ({
  extractI18nErrorMessage: () => 'error',
}))

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace }),
}))

describe('AdminBalanceRefundsView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
    getSummary.mockResolvedValue({
      data: {
        refundable_totals: { CNY: 100 }, automatic_totals: { CNY: 100 }, manual_external_totals: {},
        refundable_users: 1, automatic_users: 1, processing_users: 0, manual_review_users: 0,
        calculated_at: '2026-08-22T08:00:00Z',
      },
    })
    getList.mockResolvedValue({ data: { items: [], total: 0, page: 1, page_size: 20, pages: 1 } })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('首次加载只读取摘要和列表，不触发任何资金动作', async () => {
    const wrapper = mount(AdminBalanceRefundsView, {
      global: {
        stubs: {
          AppLayout: { template: '<main><slot /></main>' },
          BalanceRefundStats: true,
          BalanceRefundTable: true,
          BalanceRefundDetailDrawer: true,
          BalanceRefundActionDialog: true,
          BalanceRefundReconcileDialog: true,
          Pagination: true,
          Icon: true,
        },
      },
    })
    await flushPromises()

    expect(getSummary).toHaveBeenCalledTimes(1)
    expect(getList).toHaveBeenCalledWith(expect.objectContaining({ tab: 'refundable', page: 1, page_size: 20 }))
    expect(getDetail).not.toHaveBeenCalled()
    expect(start).not.toHaveBeenCalled()
    expect(action).not.toHaveBeenCalled()
    expect(reconcile).not.toHaveBeenCalled()

    wrapper.unmount()
  })
})
