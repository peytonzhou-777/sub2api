import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import UserRefundsView from '../UserRefundsView.vue'

const routerPush = vi.hoisted(() => vi.fn())
const fetchPublicSettings = vi.hoisted(() => vi.fn().mockResolvedValue(null))
const getAccountRefundOverview = vi.hoisted(() => vi.fn())
const appState = vi.hoisted(() => ({
  contactInfo: '',
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    ...appState,
    fetchPublicSettings,
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAuthenticated: true }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    getAccountRefundOverview,
  },
}))

function refundOverviewFixture() {
  return {
    data: {
      user_id: 1,
      state: 'estimate',
      updated_at: '2026-08-14T00:00:00Z',
      quote: {
        eligible: true,
        total_confidence: 'reconciled',
        allocation_confidence: 'inferred',
        permanent_balance: 100,
        recharge_bonus_balance: 20,
        other_limited_to_clear: 15,
        eligible_credit_total: 120,
        refund_credit_total: 100,
        gateway_totals: { cny: 100 },
        orders: [],
        quote_hash: 'quote-hash',
      },
    },
  }
}

function mountView() {
  return shallowMount(UserRefundsView, {
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        Icon: true,
      },
    },
  })
}

describe('UserRefundsView', () => {
  beforeEach(() => {
    sessionStorage.clear()
    appState.contactInfo = ''
    routerPush.mockReset()
    fetchPublicSettings.mockReset().mockResolvedValue(null)
    getAccountRefundOverview.mockReset().mockResolvedValue(refundOverviewFixture())
  })

  it('loads public settings and displays the configured customer service contact', async () => {
    appState.contactInfo = '客服 QQ：123456789'

    const wrapper = mountView()
    await flushPromises()

    expect(fetchPublicSettings).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-test="refund-contact-info"]').text()).toContain('客服 QQ：123456789')
  })

  it('does not render an empty customer service section', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-test="refund-contact-info"]').exists()).toBe(false)
  })

  it('does not render the removed donation action or donation list', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-test="refund-donate-trigger"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="refund-donation-list"]').exists()).toBe(false)
  })

  it('shows a continue action after a confirmed gateway failure', async () => {
    const fixture = refundOverviewFixture()
    getAccountRefundOverview.mockResolvedValue({
      data: {
        ...fixture.data,
        refund_id: 'refund-failed-1',
        state: 'failed',
        quote: {
          ...fixture.data.quote,
          orders: [{
            order_id: 1,
            completed_at: '2026-08-14T00:00:00Z',
            payment_type: 'alipay',
            provider_instance_id: '1',
            currency: 'CNY',
            original_credit: 100,
            original_paid: 100,
            bonus_rate: 0,
            bonus_initial: 0,
            bonus_remaining: 0,
            eligible_credit: 100,
            refund_credit: 100,
            gateway_refund: 100,
            allocation_confidence: 'deterministic',
            gateway_status: 'failed',
          }],
        },
      },
    })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="refund-recovery-actions"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="refund-continue"]').exists()).toBe(true)
  })

  it('only offers cancellation for manual review before any gateway submission', async () => {
    const fixture = refundOverviewFixture()
    getAccountRefundOverview.mockResolvedValue({
      data: {
        ...fixture.data,
        refund_id: 'refund-manual-1',
        state: 'manual_review',
        quote: { ...fixture.data.quote, orders: [] },
      },
    })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="refund-cancel-recovery"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="refund-continue"]').exists()).toBe(false)
  })
})
