import { flushPromises, mount } from '@vue/test-utils'
import { nextTick, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AccountPoolView from '../AccountPoolView.vue'

const { listAccountPool, getPersonalUsage, replace, showError, route } = vi.hoisted(() => ({
  listAccountPool: vi.fn(),
  getPersonalUsage: vi.fn(),
  replace: vi.fn(),
  showError: vi.fn(),
  route: { query: {} as Record<string, unknown> },
}))

vi.mock('@/api/accountPool', () => ({
  default: { list: listAccountPool, getPersonalUsage },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => ({ replace }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      locale: ref('zh-CN'),
    }),
  }
})

const emptyPage = (pageSize = 20) => ({
  items: [],
  total: 40,
  page: 1,
  page_size: pageSize,
  pages: Math.ceil(40 / pageSize),
})

const PaginationStub = {
  props: ['page', 'total', 'pageSize'],
  emits: ['update:page', 'update:pageSize'],
  template: '<button data-test="page-size" @click="$emit(\'update:pageSize\', 50)">50</button>',
}

function mountView() {
  return mount(AccountPoolView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters"/><slot name="table"/><slot name="pagination"/></div>' },
        Icon: true,
        Pagination: PaginationStub,
      },
    },
  })
}

describe('AccountPoolView', () => {
  beforeEach(() => {
    vi.useRealTimers()
    localStorage.clear()
    route.query = {}
    listAccountPool.mockReset()
    replace.mockReset()
    showError.mockReset()
    listAccountPool.mockImplementation(async (options: { pageSize: number }) => ({
      data: emptyPage(options.pageSize),
      notModified: false,
    }))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('清空搜索时立即取消旧请求、移除 URL 并恢复第一页', async () => {
    route.query = { account_id: '7' }
    const wrapper = mountView()
    await flushPromises()
    const firstSignal = listAccountPool.mock.calls[0][0].signal as AbortSignal

    await wrapper.get('#account-pool-id').setValue('')
    await flushPromises()

    expect(firstSignal.aborted).toBe(true)
    expect(replace).toHaveBeenCalledWith({ query: { account_id: undefined } })
    expect(listAccountPool.mock.calls[1][0]).toMatchObject({ page: 1, accountId: undefined })
    wrapper.unmount()
  })

  it('页大小变更会持久化并从第一页重新加载', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="page-size"]').trigger('click')
    await flushPromises()

    expect(localStorage.getItem('table-page-size')).toBe('50')
    expect(listAccountPool.mock.calls[1][0]).toMatchObject({ page: 1, pageSize: 50 })
    wrapper.unmount()
  })

  it('429 后在 Retry-After 到期前不请求，到期后刷新一次', async () => {
    vi.useFakeTimers()
    listAccountPool
      .mockRejectedValueOnce({ status: 429, retryAfter: 5, message: 'rate limited' })
      .mockResolvedValue({ data: emptyPage(), notModified: false })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('#account-pool-id').setValue('8')
    await vi.advanceTimersByTimeAsync(300)
    await nextTick()
    expect(listAccountPool).toHaveBeenCalledTimes(1)
    expect(showError).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(4_699)
    expect(listAccountPool).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(listAccountPool).toHaveBeenCalledTimes(2)
    expect(listAccountPool.mock.calls[1][0]).toMatchObject({ accountId: '8' })
    wrapper.unmount()
  })

  it('复用管理员风格展示平台徽章、订阅档位、容量状态和用量进度条', async () => {
    listAccountPool.mockResolvedValueOnce({
      data: {
        items: [{
          id: 1,
          platform: 'openai',
          type: 'oauth',
          auth_mode: 'personal_access_token',
          plan_type: 'plus',
          privacy_mode: 'training_off',
          subscription_expires_at: '2026-08-31T00:00:00Z',
          openai_compact_mode: 'force_on',
          openai_compact_supported: true,
          openai_compact_checked_at: '2026-08-01T00:00:00Z',
          capacity: { current_concurrency: 0, max_concurrency: 30, observed_at: '2026-08-01T00:00:00Z', state: 'fresh' },
          usage_windows: [{ code: '5h', label: '5h', used_percent: 0, resets_at: null, observed_at: '2026-08-01T00:00:00Z', state: 'fresh' }],
          reset_count: 0,
          reset_count_state: 'fresh',
          status: { code: 'active', resume_at: null, models: [] },
        }],
        total: 1,
        page: 1,
        page_size: 20,
        pages: 1,
      },
      notModified: false,
    })
    const wrapper = mountView()
    await flushPromises()

    const headers = wrapper.findAll('thead th').map(header => header.text())
    expect(headers).toEqual([
      'accountPool.columns.id',
      'accountPool.columns.platformType',
      'accountPool.columns.capacity',
      'accountPool.columns.usageWindow',
      'accountPool.columns.personalUsage',
      'accountPool.columns.resetCount',
      'accountPool.columns.status',
    ])
    expect(wrapper.text()).toContain('OpenAI')
    expect(wrapper.text()).toContain('Plus')
    expect(wrapper.text()).toContain('0%')
    expect(wrapper.text()).not.toContain('admin.accounts.subscriptionExpires')
    expect(wrapper.text()).not.toContain('admin.accounts.openai.compactSupported')
    expect(wrapper.text()).not.toContain('accountPool.actions.query')
    expect(wrapper.text()).not.toContain('accountPool.actions.reset')
    wrapper.unmount()
  })
})
