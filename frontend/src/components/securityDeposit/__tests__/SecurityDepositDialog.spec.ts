import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const mocks = vi.hoisted(() => ({
  getEligibility: vi.fn(), getAgreement: vi.fn(), createOrder: vi.fn(),
  getCheckoutInfo: vi.fn(), showWarning: vi.fn(),
}))

vi.mock('@/api/securityDeposits', () => ({
  securityDepositsAPI: {
    getEligibility: mocks.getEligibility,
    getAgreement: mocks.getAgreement,
    createOrder: mocks.createOrder,
  },
}))
vi.mock('@/api/payment', () => ({ paymentAPI: { getCheckoutInfo: mocks.getCheckoutInfo } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showWarning: mocks.showWarning }) }))
vi.mock('vue-router', () => ({
  useRouter: () => ({ resolve: vi.fn(() => ({ href: '/payment' })), push: vi.fn() }),
}))
vi.mock('@/utils/device', () => ({ isMobileDevice: () => false }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    locale: { value: 'zh' },
    t: (key: string) => key,
  }),
}))

import SecurityDepositDialog from '../SecurityDepositDialog.vue'

describe('SecurityDepositDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getEligibility.mockResolvedValue({
      data: {
        group_id: 7, group_name: '受控分组', currency: 'CNY', base_required_cents: 10000,
        risk_multiplier: 2, required_cents: 20000, effective_balance_cents: 5000,
        shortfall_cents: 15000, eligible: false, agreement_required: true,
        policy_version: 'v1', content_hash: 'hash-v1', quote_hash: 'quote-v1',
      },
    })
    mocks.getAgreement.mockResolvedValue({
      data: { version: 'v1', content_hash: 'hash-v1', content_zh: '禁止破限破甲。', content_en: 'No bypassing.', freeze_hours: 24 },
    })
    mocks.getCheckoutInfo.mockResolvedValue({
      data: {
        methods: {
          alipay: { currency: 'CNY', daily_limit: 0, daily_used: 0, daily_remaining: 0, single_min: 1, single_max: 1000, fee_rate: 0, available: true },
        },
        global_min: 1, global_max: 1000, plans: [], balance_disabled: false,
        balance_recharge_multiplier: 1, subscription_usd_to_cny_rate: 0, recharge_fee_rate: 0,
        help_text: '', help_image_url: '', stripe_publishable_key: '', recharge_bonus_activity: null,
      },
    })
    mocks.createOrder.mockResolvedValue({
      data: {
        satisfied: true,
        eligibility: {
          group_id: 7, group_name: '受控分组', currency: 'CNY', base_required_cents: 10000,
          risk_multiplier: 2, required_cents: 20000, effective_balance_cents: 20000,
          shortfall_cents: 0, eligible: true, agreement_required: false,
          policy_version: 'v1', content_hash: 'hash-v1', quote_hash: 'quote-v2',
        },
      },
    })
  })

  it('展示服务端差额并在接受规则后按报价创建订单', async () => {
    const wrapper = mount(SecurityDepositDialog, {
      props: { show: true, groupId: 7 },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Icon: true,
          PaymentMethodSelector: { props: ['methods', 'selected'], template: '<div>{{ selected }}</div>' },
          PaymentStatusPanel: true,
        },
      },
    })
    await flushPromises()

    expect(wrapper.get('[data-test="security-deposit-amount-due"]').text()).toBe('¥150.00')
    await wrapper.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(mocks.createOrder).toHaveBeenCalledWith(expect.objectContaining({
      group_id: 7, agreement_version: 'v1', agreement_hash: 'hash-v1',
      quote_hash: 'quote-v1', accepted: true, payment_type: 'alipay',
    }))
    expect(mocks.createOrder.mock.calls[0][0]).not.toHaveProperty('amount')
    expect(wrapper.emitted('success')).toHaveLength(1)
  })
})
