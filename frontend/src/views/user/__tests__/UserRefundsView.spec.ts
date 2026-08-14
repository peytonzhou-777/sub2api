import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import UserRefundsView from '../UserRefundsView.vue'

const routerPush = vi.hoisted(() => vi.fn())
const fetchPublicSettings = vi.hoisted(() => vi.fn().mockResolvedValue(null))
const getAccountRefundOverview = vi.hoisted(() => vi.fn())
const getAccountRefundDonations = vi.hoisted(() => vi.fn())
const donateAccountRefund = vi.hoisted(() => vi.fn())
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
    getAccountRefundDonations,
    donateAccountRefund,
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
        donation_eligible: true,
        donation_amount: 100,
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
        ConfirmDialog: {
          props: ['show'],
          emits: ['confirm', 'cancel'],
          template: '<button v-if="show" data-test="confirm-donation" @click="$emit(\'confirm\')">confirm</button>',
        },
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
    getAccountRefundDonations.mockReset().mockResolvedValue({ data: [] })
    donateAccountRefund.mockReset().mockResolvedValue({
      data: {
        ...refundOverviewFixture().data,
        refund_id: 'refund-donation-1',
        state: 'donated',
      },
    })
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

  it('requires confirmation before donating and refreshes the donation list after success', async () => {
    getAccountRefundDonations
      .mockResolvedValueOnce({ data: [] })
      .mockResolvedValueOnce({
        data: [{
          username: 'alice',
          masked_email: 'a***@example.com',
          amount: 100,
          donated_at: '2026-08-14T01:00:00Z',
        }],
      })

    const wrapper = mountView()
    await flushPromises()

    expect(donateAccountRefund).not.toHaveBeenCalled()
    await wrapper.get('[data-test="refund-donate-trigger"]').trigger('click')
    expect(donateAccountRefund).not.toHaveBeenCalled()

    await wrapper.get('[data-test="confirm-donation"]').trigger('click')
    await flushPromises()

    expect(donateAccountRefund).toHaveBeenCalledWith('quote-hash')
    expect(getAccountRefundDonations).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="refund-donation-list"]').text()).toContain('alice')
    expect(wrapper.get('[data-test="refund-donation-list"]').text()).toContain('a***@example.com')
    expect(wrapper.get('[data-test="refund-donation-list"]').text()).toContain('$100.00')
  })

  it('shows an existing donation list below the donation action', async () => {
    getAccountRefundDonations.mockResolvedValue({
      data: [{
        username: 'bob',
        masked_email: 'b***@example.com',
        amount: 50,
        donated_at: '2026-08-13T01:00:00Z',
      }],
    })

    const wrapper = mountView()
    await flushPromises()

    const trigger = wrapper.get('[data-test="refund-donate-trigger"]')
    const list = wrapper.get('[data-test="refund-donation-list"]')
    expect(trigger.element.compareDocumentPosition(list.element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
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
