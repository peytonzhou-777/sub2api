import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { nextTick } from 'vue'
import DashboardView from '../DashboardView.vue'

const authState = vi.hoisted(() => ({
  user: { balance: 100 },
}))
const limitedCreditState = vi.hoisted(() => ({
  store: null as { remainingAmount: number } | null,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: authState.user,
    isSimpleMode: false,
    refreshUser: vi.fn().mockResolvedValue(undefined),
  }),
}))

vi.mock('@/stores/limitedCredits', async () => {
  const { reactive } = await vi.importActual<typeof import('vue')>('vue')
  const store = reactive({ remainingAmount: 25 })
  limitedCreditState.store = store
  return { useLimitedCreditStore: () => store }
})

vi.mock('@/api/usage', () => ({
  usageAPI: {
    getDashboardStats: vi.fn().mockResolvedValue({}),
    getDashboardTrend: vi.fn().mockResolvedValue({ trend: [] }),
    getDashboardModels: vi.fn().mockResolvedValue({ models: [] }),
    getByDateRange: vi.fn().mockResolvedValue({ items: [] }),
  },
}))

vi.mock('@/api/user', () => ({
  getMyPlatformQuotas: vi.fn().mockResolvedValue({ platform_quotas: [] }),
}))

describe('user DashboardView', () => {
  beforeEach(() => {
    authState.user.balance = 100
    limitedCreditState.store!.remainingAmount = 25
  })

  async function mountView() {
    const wrapper = shallowMount(DashboardView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
        },
      },
    })
    await flushPromises()
    return wrapper
  }

  it('passes the sum of ordinary and limited balances to overview stats', async () => {
    const wrapper = await mountView()

    expect(wrapper.getComponent({ name: 'UserDashboardStats' }).props('balance')).toBe(125)
  })

  it('falls back to ordinary balance when no limited credit remains', async () => {
    limitedCreditState.store!.remainingAmount = 0
    const wrapper = await mountView()

    expect(wrapper.getComponent({ name: 'UserDashboardStats' }).props('balance')).toBe(100)
  })

  it('updates the overview balance when limited credit changes', async () => {
    const wrapper = await mountView()

    limitedCreditState.store!.remainingAmount = 40
    await nextTick()

    expect(wrapper.getComponent({ name: 'UserDashboardStats' }).props('balance')).toBe(140)
  })
})
