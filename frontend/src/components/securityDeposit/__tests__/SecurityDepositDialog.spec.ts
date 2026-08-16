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
    const agreement = wrapper.get('[data-test="security-deposit-agreement"]')
    Object.defineProperties(agreement.element, {
      clientHeight: { configurable: true, value: 200 },
      scrollHeight: { configurable: true, value: 600 },
      scrollTop: { configurable: true, value: 400 },
    })
    expect(wrapper.get('[data-test="security-deposit-agreement-read-hint"]').exists()).toBe(true)
    expect(wrapper.get('input[type="checkbox"]').attributes('disabled')).toBeDefined()

    await agreement.trigger('scroll')
    expect(wrapper.get('input[type="checkbox"]').attributes('disabled')).toBeUndefined()
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

  it('安全渲染 Markdown 协议并移除不可信 HTML', async () => {
    mocks.getAgreement.mockResolvedValue({
      data: {
        version: 'v1', content_hash: 'hash-v1', freeze_hours: 24,
        content_zh: '# 使用规则\n\n请**严格遵守**。<img src=x onerror="alert(1)"><script>alert(2)</script>',
        content_en: 'Terms',
      },
    })
    const wrapper = mount(SecurityDepositDialog, {
      props: { show: true, groupId: 7 },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Icon: true,
          PaymentMethodSelector: true,
          PaymentStatusPanel: true,
        },
      },
    })
    await flushPromises()

    const html = wrapper.get('[data-test="security-deposit-agreement"]').html()
    expect(html).toContain('<h1>使用规则</h1>')
    expect(html).toContain('<strong>严格遵守</strong>')
    expect(html).not.toContain('onerror')
    expect(html).not.toContain('<script')
  })

  it('已接受当前协议版本时无需重复滚动阅读', async () => {
    mocks.getEligibility.mockResolvedValue({
      data: {
        group_id: 7, group_name: '受控分组', currency: 'CNY', base_required_cents: 10000,
        risk_multiplier: 2, required_cents: 20000, effective_balance_cents: 5000,
        shortfall_cents: 15000, eligible: false, agreement_required: false,
        policy_version: 'v1', content_hash: 'hash-v1', quote_hash: 'quote-v1',
      },
    })
    const wrapper = mount(SecurityDepositDialog, {
      props: { show: true, groupId: 7 },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Icon: true,
          PaymentMethodSelector: true,
          PaymentStatusPanel: true,
        },
      },
    })
    await flushPromises()

    const checkbox = wrapper.get('input[type="checkbox"]')
    expect(checkbox.attributes('disabled')).toBeUndefined()
    expect((checkbox.element as HTMLInputElement).checked).toBe(true)
  })

  it('支付恢复流程无需重复滚动并自动继续', async () => {
    mount(SecurityDepositDialog, {
      props: { show: true, groupId: 7, resumeToken: 'resume-1', resumePaymentType: 'alipay' },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Icon: true,
          PaymentMethodSelector: true,
          PaymentStatusPanel: true,
        },
      },
    })
    await flushPromises()

    expect(mocks.createOrder).toHaveBeenCalledWith(expect.objectContaining({
      accepted: true,
      payment_type: 'alipay',
      wechat_resume_token: 'resume-1',
    }))
  })
})
