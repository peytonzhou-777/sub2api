import { beforeEach, describe, expect, it, vi } from 'vitest'
import { shallowMount } from '@vue/test-utils'
import AppHeader from '../AppHeader.vue'

const authState = vi.hoisted(() => ({
  user: {
    id: 1,
    username: 'demo',
    email: 'demo@example.com',
    role: 'user',
    balance: 1000,
    frozen_balance: 0,
    avatar_url: '',
  },
}))
const limitedCreditState = vi.hoisted(() => ({
  remainingAmount: 10,
  frozenAmount: 0,
  activeCredits: [
    { id: 2, initial_amount: 10, used_amount: 4, remaining_amount: 6, expires_at: '2099-02-01T00:00:00Z' },
    { id: 1, initial_amount: 10, used_amount: 6, remaining_amount: 4, expires_at: '2099-01-01T00:00:00Z' },
  ],
}))
const appState = vi.hoisted(() => ({
  cachedPublicSettings: {} as { payment_enabled?: boolean },
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ name: 'Dashboard', params: {}, meta: {} }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params?.date ? `${key}:${params.date}` : key,
    }),
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    contactInfo: '',
    docUrl: '',
    cachedPublicSettings: appState.cachedPublicSettings,
    toggleMobileSidebar: vi.fn(),
  }),
  useAuthStore: () => ({
    user: authState.user,
    isAdmin: false,
    isSimpleMode: false,
    logout: vi.fn(),
  }),
  useLimitedCreditStore: () => limitedCreditState,
  useOnboardingStore: () => ({ replay: vi.fn() }),
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ customMenuItems: [] }),
}))

describe('AppHeader balance display', () => {
  beforeEach(() => {
    authState.user.balance = 1000
    authState.user.frozen_balance = 0
    limitedCreditState.remainingAmount = 10
    limitedCreditState.frozenAmount = 0
    limitedCreditState.activeCredits = [
      { id: 2, initial_amount: 10, used_amount: 4, remaining_amount: 6, expires_at: '2099-02-01T00:00:00Z' },
      { id: 1, initial_amount: 10, used_amount: 6, remaining_amount: 4, expires_at: '2099-01-01T00:00:00Z' },
    ]
    appState.cachedPublicSettings = {}
  })

  function mountHeader() {
    return shallowMount(AppHeader, {
      global: {
        stubs: {
          AnnouncementBell: true,
          LocaleSwitcher: true,
          Icon: true,
          RouterLink: {
            props: ['to'],
            template: '<a :data-to="to"><slot /></a>',
          },
        },
      },
    })
  }

  it('shows the sum of ordinary and limited balances in the balance bar', () => {
    const wrapper = mountHeader()
    expect(wrapper.get('[data-test="header-balance"]').text()).toBe('$1010.00')
  })

  it('shows only the signal for the earliest expiring limited credit', () => {
    const now = Date.now()
    limitedCreditState.activeCredits = [
      { id: 1, initial_amount: 10, used_amount: 1, remaining_amount: 9, expires_at: new Date(now + 30 * 86_400_000).toISOString() },
      { id: 2, initial_amount: 10, used_amount: 7, remaining_amount: 3, expires_at: new Date(now + 2 * 86_400_000).toISOString() },
      { id: 3, initial_amount: 10, used_amount: 9, remaining_amount: 1, expires_at: new Date(now + 10 * 86_400_000).toISOString() },
    ]

    const wrapper = mountHeader()
    const signals = wrapper.findAll('[data-test="limited-credit-signal"]')

    expect(signals).toHaveLength(1)
    expect(signals[0].classes()).toContain('bg-red-500')
  })

  it('does not render frozen balance in the balance bar or popover', () => {
    authState.user.frozen_balance = 12
    limitedCreditState.frozenAmount = 3

    const wrapper = mountHeader()

    expect(wrapper.text()).not.toContain('common.frozenBalance')
    expect(wrapper.text()).not.toContain('$15.00')
    expect(wrapper.get('[data-test="balance-popover"]').text()).toContain('$1010.00')
    expect(wrapper.get('[data-test="balance-popover"]').text()).not.toContain('$1022.00')
  })

  it('shows the limited total, earliest credit, and account detail link in the popover', () => {
    const wrapper = mountHeader()

    expect(wrapper.get('[data-test="limited-credit-total"]').text()).toContain('$10.00')
    expect(wrapper.get('[data-test="earliest-limited-credit"]').text()).toContain('$4.00')
    expect(wrapper.get('[data-test="earliest-limited-credit"]').text()).toContain('common.expiresOn:')
    expect(wrapper.get('[data-test="limited-credit-details-link"]').attributes('data-to')).toBe('/purchase?tab=account')
  })

  it('hides the account detail link when payment is disabled', () => {
    appState.cachedPublicSettings = { payment_enabled: false }

    const wrapper = mountHeader()

    expect(wrapper.find('[data-test="limited-credit-details-link"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="limited-credit-total"]').text()).toContain('$10.00')
  })

  it('shows only ordinary balance and hides limited details when no limited balance exists', () => {
    limitedCreditState.remainingAmount = 0
    limitedCreditState.activeCredits = []

    const wrapper = mountHeader()

    expect(wrapper.get('[data-test="header-balance"]').text()).toBe('$1000.00')
    expect(wrapper.find('[data-test="limited-credit-total"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="earliest-limited-credit"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="limited-credit-details-link"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="limited-credit-signals"]').exists()).toBe(false)
  })
})
