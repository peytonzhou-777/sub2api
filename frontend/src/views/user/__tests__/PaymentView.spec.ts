import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import PaymentView from '../PaymentView.vue'
import { PAYMENT_RECOVERY_STORAGE_KEY } from '@/components/payment/paymentFlow'
import type { CheckoutInfoResponse, MethodLimit, RechargeBonusTier, SubscriptionPlan } from '@/types/payment'

const routeState = vi.hoisted(() => ({
  path: '/purchase',
  query: {} as Record<string, unknown>,
  reactiveRoute: null as null | {
    path: string
    query: Record<string, unknown>
  },
}))

const routerReplace = vi.hoisted(() => vi.fn())
const routerPush = vi.hoisted(() => vi.fn())
const routerResolve = vi.hoisted(() => vi.fn(() => ({ href: '/payment/stripe?mock=1' })))
const createOrder = vi.hoisted(() => vi.fn())
const refreshUser = vi.hoisted(() => vi.fn())
const authState = vi.hoisted(() => ({
  user: {
    username: 'demo-user',
    balance: 0,
  },
}))
const limitedCreditState = vi.hoisted(() => ({
  activeCredits: [] as Array<Record<string, unknown>>,
  loading: false,
  remainingAmount: 0,
  fetchActiveLimitedCredits: vi.fn().mockResolvedValue(undefined),
}))
const showError = vi.hoisted(() => vi.fn())
const showInfo = vi.hoisted(() => vi.fn())
const showWarning = vi.hoisted(() => vi.fn())
const getCheckoutInfo = vi.hoisted(() => vi.fn())
const bridgeInvoke = vi.hoisted(() => vi.fn())

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  const { reactive } = await vi.importActual<typeof import('vue')>('vue')
  const reactiveRoute = reactive(routeState)
  routeState.reactiveRoute = reactiveRoute
  return {
    ...actual,
    useRoute: () => reactiveRoute,
    useRouter: () => ({
      replace: routerReplace,
      push: routerPush,
      resolve: routerResolve,
    }),
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'payment.rechargeBonus.countdownDaysHours') {
          return `${params?.days}天${params?.hours}小时`
        }
        if (key === 'payment.rechargeBonus.participation') {
          return `可参与次数：${params?.remaining}`
        }
        if (key === 'payment.rechargeBonus.limitHint') {
          return '赠送额度以充值到账时可参与活动次数为准'
        }
        return key
      },
    }),
  }
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: authState.user,
    refreshUser,
  }),
}))

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({
    createOrder,
  }),
}))

vi.mock('@/stores/limitedCredits', () => ({
  useLimitedCreditStore: () => limitedCreditState,
}))
vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
    showInfo,
    showWarning,
  }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    getCheckoutInfo,
  },
}))

vi.mock('@/utils/device', () => ({
  isMobileDevice: () => true,
}))

beforeEach(() => {
  authState.user.balance = 0
  limitedCreditState.activeCredits = []
  limitedCreditState.loading = false
  limitedCreditState.remainingAmount = 0
  limitedCreditState.fetchActiveLimitedCredits.mockReset().mockResolvedValue(undefined)
})

function checkoutInfoFixture(overrides: Partial<CheckoutInfoResponse> = {}) {
  const wxpayMethod: MethodLimit = {
    daily_limit: 0,
    daily_used: 0,
    daily_remaining: 0,
    single_min: 0,
    single_max: 0,
    fee_rate: 0,
    available: true,
  }
  const data: CheckoutInfoResponse = {
    methods: {
      wxpay: wxpayMethod,
    },
    global_min: 0,
    global_max: 0,
    plans: [],
    balance_disabled: false,
    balance_recharge_multiplier: 1,
    subscription_usd_to_cny_rate: 0,
    recharge_fee_rate: 0,
    help_text: '',
    help_image_url: '',
    stripe_publishable_key: '',
    recharge_bonus_activity: null,
  }

  return {
    data: { ...data, ...overrides },
  }
}

function checkoutInfoWithPlansFixture(options: {
  checkout?: Partial<CheckoutInfoResponse>
  method?: Partial<MethodLimit>
  plan?: Partial<SubscriptionPlan>
} = {}) {
  const base = checkoutInfoFixture(options.checkout).data
  const plan: SubscriptionPlan = {
    id: 7,
    group_id: 3,
    name: 'Starter',
    description: '',
    price: 128,
    original_price: 0,
    validity_days: 30,
    validity_unit: 'day',
    rate_multiplier: 1,
    daily_limit_usd: null,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    features: [],
    group_platform: 'openai',
    sort_order: 1,
    for_sale: true,
    group_name: 'OpenAI',
    ...options.plan,
  }

  return {
    data: {
      ...base,
      methods: {
        ...base.methods,
        wxpay: {
          ...base.methods.wxpay,
          ...options.method,
        },
      },
      plans: [plan],
    },
  }
}

function jsapiOrderFixture(resumeToken: string) {
  return {
    order_id: 123,
    amount: 88,
    pay_amount: 88,
    fee_rate: 0,
    expires_at: '2099-01-01T00:10:00.000Z',
    payment_type: 'wxpay',
    out_trade_no: 'sub2_jsapi_123',
    result_type: 'jsapi_ready' as const,
    resume_token: resumeToken,
    jsapi: {
      appId: 'wx123',
      timeStamp: '1712345678',
      nonceStr: 'nonce',
      package: 'prepay_id=wx123',
      signType: 'RSA',
      paySign: 'signed',
    },
  }
}

function oauthOrderFixture() {
  return {
    order_id: 456,
    amount: 128,
    pay_amount: 128,
    fee_rate: 0,
    expires_at: '2099-01-01T00:10:00.000Z',
    payment_type: 'wxpay',
    result_type: 'oauth_required' as const,
    oauth: {
      authorize_url: '/api/v1/auth/oauth/wechat/payment/start?payment_type=wxpay&redirect=%2Fpurchase%3Ffrom%3Dwechat',
      appid: 'wx123',
      scope: 'snsapi_base',
      redirect_url: '/auth/wechat/payment/callback',
    },
  }
}

async function mountRechargeWithCampaign(
  description = '充值越多，赠送越多',
  tiers: RechargeBonusTier[] = [
    { min_amount: 100, max_amount: 1000, min_rate: 10, max_rate: 10 },
  ],
  participationLimit = 2,
  completedCount = 0,
) {
  vi.useRealTimers()
  routeState.path = '/purchase'
  routeState.query = {}
  routerReplace.mockReset().mockResolvedValue(undefined)
  routerPush.mockReset().mockResolvedValue(undefined)
  routerResolve.mockClear()
  createOrder.mockReset()
  refreshUser.mockReset()
  showError.mockReset()
  showInfo.mockReset()
  showWarning.mockReset()
  getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoFixture({
    balance_recharge_multiplier: 2,
    recharge_bonus_activity: {
      id: 9,
      name: '暑期充值活动',
      description,
      start_at: '2026-07-01T00:00:00Z',
      end_at: '2026-08-01T00:00:00Z',
      participation_limit: participationLimit,
      tiers,
      status: 'active',
      created_at: '2026-06-01T00:00:00Z',
      updated_at: '2026-06-01T00:00:00Z',
      completed_count: completedCount,
      remaining_count: participationLimit > 0 ? Math.max(0, participationLimit - completedCount) : null,
      validity_days: 30,
    },
  }))
  window.localStorage.clear()

  const wrapper = shallowMount(PaymentView, {
    global: {
      stubs: {
        AppLayout: {
          template: '<div><slot name="header-tabs" /><slot /></div>',
        },
        PageHeaderTabs: false,
        AmountInput: {
          name: 'AmountInput',
          props: ['modelValue', 'min', 'max'],
          template: '<button data-test="amount-input" @click="$emit(\'update:modelValue\', 300)">set</button>',
        },
        PaymentMethodSelector: true,
        Teleport: true,
        Transition: false,
      },
    },
  })
  await flushPromises()
  await flushPromises()
  return wrapper
}

describe('PaymentView recharge bonus campaign', () => {
  it('shows the active campaign name and description above amount selection', async () => {
    const wrapper = await mountRechargeWithCampaign()

    const panel = wrapper.find('[data-test="recharge-bonus-campaign"]')
    expect(panel.exists()).toBe(true)
    expect(panel.text()).toContain('暑期充值活动')
    expect(panel.text()).toContain('充值越多，赠送越多')
    expect(panel.get('[data-test="recharge-bonus-description"]').classes()).toContain('dark:text-[#f1f1f1]')
  })

  it('shows the countdown to the campaign end time', async () => {
    const now = new Date('2026-07-31T17:01:04Z').getTime()
    const dateNow = vi.spyOn(Date, 'now').mockReturnValue(now)

    const wrapper = await mountRechargeWithCampaign()

    expect(wrapper.get('[data-test="recharge-bonus-countdown"]').text()).toContain('06:58:56')
    dateNow.mockRestore()
  })

  it('shows days and hours when more than 24 hours remain', async () => {
    const now = new Date('2026-07-25T17:00:00Z').getTime()
    const dateNow = vi.spyOn(Date, 'now').mockReturnValue(now)

    const wrapper = await mountRechargeWithCampaign()

    expect(wrapper.get('[data-test="recharge-bonus-countdown"]').text()).toContain('6天7小时')
    dateNow.mockRestore()
  })

  it('shows the remaining participation count for a limited campaign', async () => {
    const wrapper = await mountRechargeWithCampaign('充值越多，赠送越多', undefined, 2, 1)

    expect(wrapper.get('[data-test="recharge-bonus-participation"]').text()).toBe('可参与次数：1')
  })

  it('hides participation progress for an unlimited campaign', async () => {
    const wrapper = await mountRechargeWithCampaign('充值越多，赠送越多', undefined, 0, 3)

    expect(wrapper.find('[data-test="recharge-bonus-participation"]').exists()).toBe(false)
  })

  it('shows interpolated limited-credit bonuses with two decimal places', async () => {
    const wrapper = await mountRechargeWithCampaign('充值越多，赠送越多', [
      { min_amount: 100, max_amount: 1000, min_rate: 5, max_rate: 10 },
    ])

    await wrapper.get('[data-test="amount-input"]').trigger('click')
    await flushPromises()

    const creditedRow = wrapper.get('[data-test="credited-amount-row"]')
    expect(creditedRow.text()).toContain('+$46.67')
    expect(creditedRow.text()).not.toContain('46.666666')
  })

  it('shows the campaign name without an empty description placeholder', async () => {
    const wrapper = await mountRechargeWithCampaign('')

    const panel = wrapper.find('[data-test="recharge-bonus-campaign"]')
    expect(panel.text()).toContain('暑期充值活动')
    expect(panel.find('[data-test="recharge-bonus-description"]').exists()).toBe(false)
  })

  it('shows payment and credited rows together and excludes campaign name from credited row', async () => {
    const wrapper = await mountRechargeWithCampaign()
    expect(wrapper.find('[data-test="payment-amount-row"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="credited-amount-row"]').exists()).toBe(true)

    await wrapper.find('[data-test="amount-input"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="payment-amount-row"]').exists()).toBe(true)
    const creditedRow = wrapper.find('[data-test="credited-amount-row"]')
    expect(creditedRow.exists()).toBe(true)
    expect(creditedRow.text()).toContain('$600.00')
    expect(creditedRow.text()).toContain('$60.00')
    expect(creditedRow.text()).not.toContain('暑期充值活动')
    const limitHint = wrapper.get('[data-test="recharge-bonus-limit-hint"]')
    expect(limitHint.classes()).toContain('text-right')
    expect(limitHint.text()).toBe('赠送额度以充值到账时可参与活动次数为准')
    const summaryPanel = wrapper.get('[data-test="payment-summary-panel"]').element
    const methodPanel = wrapper.get('[data-test="payment-method-panel"]').element
    expect(summaryPanel.compareDocumentPosition(methodPanel) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})


describe('PaymentView account tab', () => {
  async function mountAccount(overrides: Partial<CheckoutInfoResponse> = {}) {
    routeState.path = '/purchase'
    routeState.query = { tab: 'account' }
    getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoFixture(overrides))
    window.localStorage.clear()

    const wrapper = shallowMount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot name="header-tabs" /><slot /></div>' },
          PageHeaderTabs: false,
          PaymentMethodSelector: true,
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()
    return wrapper
  }

  it('shows the total balance and sorts active limited credits by expiration', async () => {
    authState.user.balance = 20
    limitedCreditState.remainingAmount = 12
    limitedCreditState.activeCredits = [
      {
        id: 2, initial_amount: 10, used_amount: 2, frozen_amount: 0,
        remaining_amount: 8, available_amount: 8,
        expires_at: '2099-02-01T00:00:00Z', status: 'active',
        created_at: '2026-01-01T00:00:00Z',
      },
      {
        id: 1, initial_amount: 5, used_amount: 1, frozen_amount: 0,
        remaining_amount: 4, available_amount: 4,
        expires_at: '2099-01-01T00:00:00Z', status: 'active',
        created_at: '2026-01-01T00:00:00Z',
      },
    ]

    const wrapper = await mountAccount()

    expect(wrapper.get('[data-test="account-total-balance"]').text()).toBe('$32.00')
    expect(wrapper.get('[data-test="permanent-balance"]').text()).toBe('$20.00')
    expect(wrapper.get('[data-test="limited-balance"]').text()).toBe('$12.00')
    expect(wrapper.get('[data-test="limited-balance-card"]').classes()).toEqual(
      wrapper.get('[data-test="permanent-balance-card"]').classes(),
    )
    expect(wrapper.get('[data-test="limited-balance-label"]').classes()).toContain('text-gray-500')
    expect(wrapper.get('[data-test="limited-balance"]').classes()).toContain('text-green-600')
    expect(wrapper.get('[data-test="limited-credit-list"]').classes()).not.toContain('max-w-2xl')
    const items = wrapper.findAll('[data-test="limited-credit-item"]')
    expect(items).toHaveLength(2)
    expect(items[0].text()).toContain('$1.00 / $5.00')
    expect(items[1].text()).toContain('$2.00 / $10.00')
    expect(items[0].findAll('[data-test="limited-credit-expiration"]')).toHaveLength(1)
    expect((items[0].text().match(/payment.account.daysRemaining/g) || [])).toHaveLength(1)
    expect(wrapper.get('[data-test="payment-tab-account"]').classes()).toContain('page-header-tab-active')
    expect(wrapper.get('[data-test="payment-tab-recharge"]').classes()).toContain('page-header-tab')
    expect(wrapper.get('[data-test="limited-credit-progress-track"]').classes()).toContain('bg-[#444444]')
    expect(wrapper.get('[data-test="limited-credit-progress-fill"]').classes()).toContain('bg-[#f4f4f4]')
  })

  it('shows the account empty state when no active limited credit exists', async () => {
    const wrapper = await mountAccount()
    expect(wrapper.find('[data-test="limited-credit-empty"]').exists()).toBe(true)
  })

  it('keeps recharge as default, ignores the legacy subscription tab, and hides subscription plans', async () => {
    routeState.path = '/purchase'
    routeState.query = { tab: 'subscription' }
    getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoWithPlansFixture())

    const wrapper = shallowMount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot name="header-tabs" /><slot /></div>' },
          PageHeaderTabs: false,
          AmountInput: true,
          PaymentMethodSelector: true,
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[data-test="account-balance-panel"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('payment.tabAccount')
    expect(wrapper.text()).not.toContain('Starter')
    expect(wrapper.text()).not.toContain('demo-user')
    expect(wrapper.text()).not.toContain('payment.rechargeAccount')
  })

  it('shows only the account content when balance recharge is disabled', async () => {
    routeState.query = {}
    const wrapper = await mountAccount({ balance_disabled: true })

    expect(wrapper.find('[data-test="account-balance-panel"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('payment.tabTopUp')
  })

  it('switches to the account tab when the current wallet route receives tab=account', async () => {
    routeState.path = '/purchase'
    routeState.query = {}
    getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoFixture())

    const wrapper = shallowMount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot name="header-tabs" /><slot /></div>' },
          PageHeaderTabs: false,
          AmountInput: true,
          PaymentMethodSelector: true,
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    expect(wrapper.find('[data-test="account-balance-panel"]').exists()).toBe(false)

    if (!routeState.reactiveRoute) throw new Error('reactive route is unavailable')
    routeState.reactiveRoute.query = { tab: 'account' }
    await flushPromises()

    expect(wrapper.find('[data-test="account-balance-panel"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="payment-tab-account"]').attributes('aria-selected')).toBe('true')
  })

  it('refreshes the user and limited credits after a balance order succeeds', async () => {
    routeState.query = {}
    getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoFixture())
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      orderId: 999, amount: 50, qrCode: 'mock-qr',
      expiresAt: '2099-01-01T00:10:00.000Z', paymentType: 'wxpay',
      payUrl: '', outTradeNo: 'sub2_balance_999', clientSecret: '',
      intentId: '', currency: 'CNY', countryCode: '', paymentEnv: '',
      payAmount: 50, orderType: 'balance', paymentMode: 'qr',
      resumeToken: '', createdAt: Date.now(),
    }))

    const wrapper = shallowMount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot name="header-tabs" /><slot /></div>' },
          PageHeaderTabs: false,
          PaymentStatusPanel: {
            template: '<button data-test="payment-success" @click="$emit(\'success\')" />',
          },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await wrapper.get('[data-test="payment-success"]').trigger('click')

    expect(refreshUser).toHaveBeenCalled()
    expect(limitedCreditState.fetchActiveLimitedCredits).toHaveBeenCalledWith(true)
  })
})

describe('PaymentView payment recovery', () => {
  beforeEach(() => {
    vi.useRealTimers()
    routeState.path = '/purchase'
    routeState.query = {}
    routerReplace.mockReset().mockResolvedValue(undefined)
    routerPush.mockReset().mockResolvedValue(undefined)
    routerResolve.mockClear()
    createOrder.mockReset()
    refreshUser.mockReset()
    showError.mockReset()
    showInfo.mockReset()
    showWarning.mockReset()
    bridgeInvoke.mockReset()
    window.localStorage.clear()
    ;(window as Window & { WeixinJSBridge?: { invoke: typeof bridgeInvoke } }).WeixinJSBridge = undefined
  })

  it('restores a custom EasyPay method as the selected payment method', async () => {
    getCheckoutInfo.mockResolvedValue(checkoutInfoFixture({
      methods: {
        wxpay: checkoutInfoFixture().data.methods.wxpay,
        ldc: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: 0,
          available: true,
          display_name: 'LDC Pay',
        },
      },
    }))
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      orderId: 888,
      amount: 66,
      qrCode: 'ldc-qr',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'ldc',
      payUrl: 'https://pay.example.com/ldc',
      outTradeNo: 'sub2_ldc_888',
      clientSecret: '',
      intentId: '',
      currency: '',
      countryCode: '',
      paymentEnv: '',
      payAmount: 66,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: '',
      createdAt: Date.now(),
    }))

    const wrapper = shallowMount(PaymentView, {
      global: {
        stubs: {
          AppLayout: {
            template: '<div><slot name="header-tabs" /><slot /></div>',
          },
          PageHeaderTabs: false,
          PaymentStatusPanel: {
            template: '<button data-test="payment-done" @click="$emit(\'done\')" />',
          },
          PaymentMethodSelector: {
            props: ['selected'],
            template: '<div data-test="method-selector">{{ selected }}</div>',
          },
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()
    await wrapper.find('[data-test="payment-done"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="method-selector"]').text()).toBe('ldc')
  })
})

describe('PaymentView WeChat JSAPI flow', () => {
  beforeEach(() => {
    routeState.path = '/purchase'
    routeState.query = {
      wechat_resume: '1',
      wechat_resume_token: 'resume-token-123',
    }
    routerReplace.mockReset().mockResolvedValue(undefined)
    routerPush.mockReset().mockResolvedValue(undefined)
    routerResolve.mockClear()
    createOrder.mockReset()
    refreshUser.mockReset()
    showError.mockReset()
    showInfo.mockReset()
    showWarning.mockReset()
    getCheckoutInfo.mockReset().mockResolvedValue(checkoutInfoFixture())
    bridgeInvoke.mockReset()
    window.localStorage.clear()
    ;(window as Window & { WeixinJSBridge?: { invoke: typeof bridgeInvoke } }).WeixinJSBridge = {
      invoke: bridgeInvoke,
    }
  })

  it('resets payment state and redirects to /payment/result after JSAPI reports success', async () => {
    createOrder.mockResolvedValue(jsapiOrderFixture('resume-token-123'))
    bridgeInvoke.mockImplementation((_action, _payload, callback) => {
      callback({ err_msg: 'get_brand_wcpay_request:ok' })
    })

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(routerReplace).toHaveBeenCalledWith({ path: '/purchase', query: {} })
    expect(routerPush).toHaveBeenCalledWith({
      path: '/payment/result',
      query: {
        order_id: '123',
        out_trade_no: 'sub2_jsapi_123',
        resume_token: 'resume-token-123',
      },
    })
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('resets payment state when JSAPI reports cancellation', async () => {
    createOrder.mockResolvedValue(jsapiOrderFixture('resume-token-cancel'))
    bridgeInvoke.mockImplementation((_action, _payload, callback) => {
      callback({ err_msg: 'get_brand_wcpay_request:cancel' })
    })

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(showInfo).toHaveBeenCalledWith('payment.qr.cancelled')
    expect(routerPush).not.toHaveBeenCalled()
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('clears stale recovery state when JSAPI never becomes available', async () => {
    vi.useFakeTimers()
    createOrder.mockResolvedValue(jsapiOrderFixture('resume-token-missing-bridge'))
    ;(window as Window & { WeixinJSBridge?: { invoke: typeof bridgeInvoke } }).WeixinJSBridge = undefined

    const wrapper = shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })

    await flushPromises()
    await vi.advanceTimersByTimeAsync(4000)
    await flushPromises()
    await flushPromises()

    expect(showError).toHaveBeenCalledWith(
      'payment.errors.wechatJsapiUnavailable payment.errors.wechatOpenInWeChatHint',
    )
    expect(routerPush).not.toHaveBeenCalled()
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
    expect(wrapper.html()).not.toContain('payment-status-panel-stub')
  })

  it('clears a stale recovery snapshot before handling wechat resume callback params', async () => {
    createOrder.mockRejectedValueOnce(new Error('resume failed'))
    window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
      orderId: 999,
      amount: 66,
      qrCode: 'stale-qr',
      expiresAt: '2099-01-01T00:10:00.000Z',
      paymentType: 'alipay',
      payUrl: 'https://pay.example.com/stale',
      outTradeNo: 'stale-out-trade-no',
      clientSecret: '',
      intentId: '',
      currency: '',
      countryCode: '',
      paymentEnv: '',
      payAmount: 66,
      orderType: 'balance',
      paymentMode: 'popup',
      resumeToken: '',
      createdAt: Date.UTC(2099, 0, 1, 0, 0, 0),
    }))

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(createOrder).toHaveBeenCalledWith(expect.objectContaining({
      wechat_resume_token: 'resume-token-123',
    }))
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toBeNull()
  })

  it('keeps subscription resume context for token-only WeChat callbacks', async () => {
    routeState.query = {
      wechat_resume: '1',
      wechat_resume_token: 'resume-subscription-7',
      payment_type: 'wxpay_direct',
      order_type: 'subscription',
      plan_id: '7',
    }
    getCheckoutInfo.mockResolvedValue(checkoutInfoWithPlansFixture())
    createOrder.mockResolvedValue(oauthOrderFixture())

    const originalLocation = window.location
    const locationState = {
      href: 'http://localhost/purchase',
      origin: 'http://localhost',
    }
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState,
    })

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(routerReplace).toHaveBeenCalledWith({ path: '/purchase', query: {} })
    expect(createOrder).toHaveBeenCalledWith(expect.objectContaining({
      payment_type: 'wxpay',
      order_type: 'subscription',
      plan_id: 7,
      wechat_resume_token: 'resume-subscription-7',
    }))
    expect(locationState.href).toContain('/api/v1/auth/oauth/wechat/payment/start?')
    expect(new URL(locationState.href, 'http://localhost').searchParams.get('redirect')).toBe(
      '/purchase?from=wechat&payment_type=wxpay&order_type=subscription&plan_id=7',
    )

    Object.defineProperty(window, 'location', {
      configurable: true,
      value: originalLocation,
    })
  })

  it('falls back to QR flow when mobile WeChat payment is unavailable', async () => {
    routeState.query = {
      wechat_resume: '1',
      wechat_resume_token: 'resume-token-h5',
      payment_type: 'wxpay_direct',
    }
    createOrder
      .mockRejectedValueOnce({ reason: 'WECHAT_H5_NOT_AUTHORIZED' })
      .mockResolvedValueOnce({
        order_id: 778,
        amount: 88,
        pay_amount: 88,
        fee_rate: 0,
        expires_at: '2099-01-01T00:10:00.000Z',
        payment_type: 'wxpay',
        qr_code: 'weixin://wxpay/bizpayurl?pr=fallback-native',
        out_trade_no: 'sub2_qr_778',
      })

    shallowMount(PaymentView, {
      global: {
        stubs: {
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()

    expect(createOrder).toHaveBeenNthCalledWith(1, expect.objectContaining({
      payment_type: 'wxpay',
      is_mobile: true,
      wechat_resume_token: 'resume-token-h5',
    }))
    expect(createOrder).toHaveBeenNthCalledWith(2, expect.objectContaining({
      payment_type: 'wxpay',
      is_mobile: false,
      payment_source: 'hosted_redirect',
    }))
    expect(showWarning).toHaveBeenCalledWith('payment.errors.mobilePaymentFallbackToQr')
    expect(showError).not.toHaveBeenCalled()
    expect(window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY)).toContain('weixin://wxpay/bizpayurl?pr=fallback-native')
  })
})
