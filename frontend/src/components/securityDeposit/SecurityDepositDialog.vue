<template>
  <BaseDialog
    :show="show"
    :title="t('keys.securityDeposit.dialogTitle')"
    width="wide"
    @close="closeDialog"
  >
    <div v-if="loading" class="flex justify-center py-16">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
    </div>

    <PaymentStatusPanel
      v-else-if="phase === 'paying'"
      :order-id="paymentState.orderId"
      :amount="paymentState.amount"
      :pay-amount="paymentState.payAmount"
      :qr-code="paymentState.qrCode"
      :expires-at="paymentState.expiresAt"
      :payment-type="paymentState.paymentType"
      :pay-url="paymentState.payUrl"
      :order-type="paymentState.orderType"
      :currency="paymentState.currency || 'CNY'"
      :out-trade-no="paymentState.outTradeNo"
      :mobile-alipay-deep-link="paymentState.alipayMobilePrecreateDeepLink"
      @done="resetPayment"
      @success="handlePaymentSuccess"
      @settled="removeRecoverySnapshot"
    />

    <div v-else-if="eligibility && agreement" class="space-y-5">
      <div class="grid gap-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700 sm:grid-cols-2 lg:grid-cols-3">
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('keys.securityDeposit.group') }}</p>
          <p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ eligibility.group_name }}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('keys.securityDeposit.currentBalance') }}</p>
          <p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ formatCents(eligibility.effective_balance_cents) }}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('keys.securityDeposit.baseRequired') }}</p>
          <p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ formatCents(eligibility.base_required_cents) }}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('keys.securityDeposit.riskMultiplierLabel') }}</p>
          <p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ eligibility.risk_multiplier }}x</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('keys.securityDeposit.personalThreshold') }}</p>
          <p class="mt-1 text-sm font-semibold text-gray-900 dark:text-white">{{ formatCents(eligibility.required_cents) }}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('keys.securityDeposit.amountDue') }}</p>
          <p data-test="security-deposit-amount-due" class="mt-1 text-base font-bold text-amber-600 dark:text-amber-400">
            {{ formatCents(eligibility.shortfall_cents) }}
          </p>
        </div>
      </div>

      <div>
        <div class="mb-2 flex items-center justify-between gap-3">
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('keys.securityDeposit.agreementTitle', { version: agreement.version }) }}
          </label>
          <span class="text-xs text-gray-400">{{ agreement.content_hash.slice(0, 12) }}</span>
        </div>
        <div
          ref="agreementBody"
          data-test="security-deposit-agreement"
          class="max-h-56 overflow-y-auto rounded-lg border border-gray-200 bg-gray-50 p-4 text-sm leading-6 text-gray-700 outline-none focus-visible:ring-2 focus-visible:ring-primary-500/30 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300 [&_a]:text-primary-600 [&_a]:underline [&_h1]:mb-3 [&_h1]:text-base [&_h1]:font-semibold [&_li]:mb-2 [&_ol]:list-decimal [&_ol]:pl-5 [&_p]:mb-3 [&_strong]:font-semibold [&_ul]:list-disc [&_ul]:pl-5"
          tabindex="0"
          @scroll="handleAgreementScroll"
          v-html="renderedAgreementHtml"
        ></div>
        <p
          v-if="agreementNeedsReading && !agreementReadComplete"
          data-test="security-deposit-agreement-read-hint"
          class="mt-2 text-xs text-amber-600 dark:text-amber-400"
        >
          {{ t('keys.securityDeposit.agreementReadHint') }}
        </p>
      </div>

      <label
        class="flex items-start gap-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700"
        :class="agreementReadComplete ? 'cursor-pointer' : 'cursor-not-allowed opacity-70'"
      >
        <input
          v-model="accepted"
          type="checkbox"
          class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500"
          :disabled="!agreementReadComplete"
        />
        <span class="text-sm leading-5 text-gray-700 dark:text-gray-300">
          {{ t('keys.securityDeposit.acceptAgreement') }}
        </span>
      </label>

      <div class="flex items-start gap-2 text-xs leading-5 text-gray-500 dark:text-gray-400">
        <Icon name="clock" size="sm" class="mt-0.5 shrink-0" />
        <span>{{ t('keys.securityDeposit.freezeHint', { hours: agreement.freeze_hours }) }}</span>
      </div>

      <PaymentMethodSelector
        v-if="methodOptions.length"
        :methods="methodOptions"
        :selected="selectedMethod"
        @select="selectedMethod = $event"
      />
      <p v-else class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-700 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-300">
        {{ t('keys.securityDeposit.noPaymentMethod') }}
      </p>

      <p v-if="errorMessage" class="text-sm text-red-600 dark:text-red-400">{{ errorMessage }}</p>
    </div>

    <template #footer>
      <div v-if="phase === 'select'" class="flex flex-wrap justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="submitting" @click="closeDialog">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="!canSubmit"
          @click="submitDeposit"
        >
          {{ submitting
            ? t('common.processing')
            : t('keys.securityDeposit.payAction', { amount: formatCents(eligibility?.shortfall_cents || 0) }) }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import PaymentMethodSelector, { type PaymentMethodOption } from '@/components/payment/PaymentMethodSelector.vue'
import PaymentStatusPanel from '@/components/payment/PaymentStatusPanel.vue'
import { getPaymentPopupFeatures } from '@/components/payment/providerConfig'
import {
  PAYMENT_RECOVERY_STORAGE_KEY,
  clearPaymentRecoverySnapshot,
  decidePaymentLaunch,
  getVisibleMethods,
  normalizeVisibleMethod,
  writePaymentRecoverySnapshot,
  type PaymentRecoverySnapshot,
} from '@/components/payment/paymentFlow'
import { paymentAPI } from '@/api/payment'
import { securityDepositsAPI } from '@/api/securityDeposits'
import { useAppStore } from '@/stores/app'
import { isMobileDevice } from '@/utils/device'
import type { CheckoutInfoResponse } from '@/types/payment'
import type { SecurityDepositAgreement, SecurityDepositEligibility } from '@/types/securityDeposit'

const props = defineProps<{
  show: boolean
  groupId: number | null
  resumeToken?: string
  resumePaymentType?: string
}>()

const emit = defineEmits<{ close: []; success: [eligibility: SecurityDepositEligibility] }>()
const { t, locale } = useI18n()
const router = useRouter()
const appStore = useAppStore()

const loading = ref(false)
const submitting = ref(false)
const phase = ref<'select' | 'paying'>('select')
const eligibility = ref<SecurityDepositEligibility | null>(null)
const agreement = ref<SecurityDepositAgreement | null>(null)
const checkout = ref<CheckoutInfoResponse | null>(null)
const selectedMethod = ref('')
const accepted = ref(false)
const agreementReadComplete = ref(false)
const agreementBody = ref<HTMLElement | null>(null)
const errorMessage = ref('')
const paymentState = ref<PaymentRecoverySnapshot>(emptyPaymentState())
let resumedToken = ''

const agreementContent = computed(() => {
  if (!agreement.value) return ''
  return locale.value.toLowerCase().startsWith('zh') ? agreement.value.content_zh : agreement.value.content_en
})

const renderedAgreementHtml = computed(() => {
  const html = marked.parse(agreementContent.value, { breaks: true, gfm: true }) as string
  return DOMPurify.sanitize(html)
})

const agreementAcceptanceExempt = computed(() => Boolean(
  props.resumeToken || (eligibility.value && !eligibility.value.agreement_required),
))
const agreementNeedsReading = computed(() => Boolean(agreement.value && !agreementAcceptanceExempt.value))

const methodOptions = computed<PaymentMethodOption[]>(() => {
  if (!checkout.value || !eligibility.value) return []
  const amount = eligibility.value.shortfall_cents / 100
  return Object.entries(getVisibleMethods(checkout.value.methods))
    .filter(([, limit]) => !limit.currency || limit.currency.toUpperCase() === 'CNY')
    .map(([type, limit]) => ({
      type,
      display_name: limit.display_name,
      fee_rate: limit.fee_rate || 0,
      available: limit.available !== false
        && (limit.single_min <= 0 || amount >= limit.single_min)
        && (limit.single_max <= 0 || amount <= limit.single_max),
    }))
})

const canSubmit = computed(() => Boolean(
  eligibility.value
  && agreement.value
  && eligibility.value.shortfall_cents > 0
  && accepted.value
  && agreementReadComplete.value
  && selectedMethod.value
  && methodOptions.value.some(method => method.type === selectedMethod.value && method.available)
  && !submitting.value,
))

watch(
  () => [props.show, props.groupId, props.resumeToken] as const,
  ([show]) => {
    if (show && props.groupId) void loadDialog()
  },
  { immediate: true },
)

watch(() => locale.value, async () => {
  if (!props.show || phase.value !== 'select') return
  resetAgreementAcceptance()
  await nextTick()
  updateAgreementReadState()
})

// 每次打开都重新读取权威报价，避免复用已过期的差额和协议。
async function loadDialog() {
  if (!props.groupId) return
  loading.value = true
  errorMessage.value = ''
  phase.value = 'select'
  accepted.value = false
  agreementReadComplete.value = false
  try {
    const [eligibilityResponse, agreementResponse, checkoutResponse] = await Promise.all([
      securityDepositsAPI.getEligibility(props.groupId),
      securityDepositsAPI.getAgreement(props.groupId),
      paymentAPI.getCheckoutInfo(),
    ])
    eligibility.value = eligibilityResponse.data
    agreement.value = agreementResponse.data
    checkout.value = checkoutResponse.data
    resetAgreementAcceptance()
    const availableMethods = methodOptions.value.filter(method => method.available)
    const requestedMethod = normalizeVisibleMethod(props.resumePaymentType || '')
    selectedMethod.value = availableMethods.some(method => method.type === requestedMethod)
      ? requestedMethod
      : (availableMethods[0]?.type || '')

    if (eligibilityResponse.data.shortfall_cents === 0) {
      emit('success', eligibilityResponse.data)
      emit('close')
      return
    }
    if (props.resumeToken && resumedToken !== props.resumeToken) {
      resumedToken = props.resumeToken
      await submitDeposit()
    }
  } catch (error: unknown) {
    errorMessage.value = apiErrorMessage(error)
  } finally {
    loading.value = false
    await nextTick()
    updateAgreementReadState()
  }
}

// 首次接受当前协议时必须阅读到底；内容完整可见时无需制造无意义滚动。
function updateAgreementReadState() {
  if (agreementReadComplete.value || agreementAcceptanceExempt.value) {
    agreementReadComplete.value = true
    return
  }
  const element = agreementBody.value
  if (!element || element.clientHeight <= 0) return
  if (element.scrollHeight <= element.clientHeight + 4) agreementReadComplete.value = true
}

function handleAgreementScroll() {
  const element = agreementBody.value
  if (!element || agreementReadComplete.value) return
  if (element.scrollTop + element.clientHeight >= element.scrollHeight - 4) {
    agreementReadComplete.value = true
  }
}

function resetAgreementAcceptance() {
  accepted.value = agreementAcceptanceExempt.value
  agreementReadComplete.value = agreementAcceptanceExempt.value
}

async function submitDeposit() {
  if (!eligibility.value || !agreement.value || !props.groupId || (!canSubmit.value && !props.resumeToken)) return
  submitting.value = true
  errorMessage.value = ''
  const paymentType = normalizeVisibleMethod(props.resumePaymentType || selectedMethod.value) || selectedMethod.value
  try {
    const response = await securityDepositsAPI.createOrder({
      group_id: props.groupId,
      agreement_version: agreement.value.version,
      agreement_hash: agreement.value.content_hash,
      quote_hash: eligibility.value.quote_hash,
      accepted: true,
      payment_type: paymentType,
      wechat_resume_token: props.resumeToken || undefined,
      return_url: `${window.location.origin}/payment/result`,
      payment_source: paymentType === 'wxpay' && isWechatBrowser() ? 'wechat_in_app_resume' : 'hosted_redirect',
      is_mobile: isMobileDevice(),
    })
    eligibility.value = response.data.eligibility
    if (response.data.satisfied || !response.data.payment) {
      emit('success', response.data.eligibility)
      emit('close')
      return
    }
    await launchPayment(response.data.payment, paymentType)
  } catch (error: unknown) {
    const code = apiErrorCode(error)
    if (code === 'SECURITY_DEPOSIT_QUOTE_CHANGED' || code === 'SECURITY_DEPOSIT_AGREEMENT_OUTDATED') {
      appStore.showWarning(t('keys.securityDeposit.quoteChanged'))
      await loadDialog()
    } else {
      errorMessage.value = apiErrorMessage(error)
    }
  } finally {
    submitting.value = false
  }
}

async function launchPayment(result: NonNullable<Awaited<ReturnType<typeof securityDepositsAPI.createOrder>>['data']['payment']>, paymentType: string) {
  const visibleMethod = normalizeVisibleMethod(paymentType) || paymentType
  const stripeRouteUrl = result.client_secret && visibleMethod !== 'airwallex'
    ? router.resolve({
        path: '/payment/stripe',
        query: {
          order_id: String(result.order_id),
          client_secret: result.client_secret,
          method: visibleMethod === 'stripe' ? undefined : visibleMethod === 'wxpay' ? 'wechat_pay' : 'alipay',
          resume_token: result.resume_token || undefined,
        },
      }).href
    : ''
  const airwallexRouteUrl = result.client_secret && result.intent_id
    ? router.resolve({
        path: '/payment/airwallex',
        query: {
          order_id: String(result.order_id),
          out_trade_no: result.out_trade_no || undefined,
          resume_token: result.resume_token || undefined,
        },
      }).href
    : ''
  const decision = decidePaymentLaunch(result, {
    visibleMethod,
    orderType: 'security_deposit',
    isMobile: isMobileDevice(),
    isWechatBrowser: isWechatBrowser(),
    forceQRCode: Boolean(checkout.value?.alipay_force_qrcode && visibleMethod === 'alipay'),
    mobilePrecreateDeepLink: checkout.value?.alipay_mobile_precreate_deep_link === true,
    stripePopupUrl: stripeRouteUrl,
    stripeRouteUrl,
    airwallexRouteUrl,
  })

  if (decision.kind === 'wechat_oauth' && decision.oauth?.authorize_url) {
    window.location.href = buildWechatAuthorizeURL(decision.oauth.authorize_url, visibleMethod)
    return
  }
  if (decision.kind === 'unhandled') {
    throw new Error(t('keys.securityDeposit.paymentUnavailable'))
  }

  paymentState.value = decision.paymentState
  phase.value = 'paying'
  writePaymentRecoverySnapshot(window.localStorage, decision.recovery, PAYMENT_RECOVERY_STORAGE_KEY)

  if (decision.kind === 'stripe_popup') {
    const popup = window.open(decision.paymentState.payUrl, 'paymentPopup', getPaymentPopupFeatures())
    if (!popup || popup.closed) window.location.href = decision.paymentState.payUrl
  } else if (decision.kind === 'stripe_route' || decision.kind === 'airwallex_route') {
    window.location.href = decision.paymentState.payUrl
  } else if (decision.kind === 'redirect_waiting' && decision.paymentState.payUrl) {
    if (isMobileDevice()) window.location.href = decision.paymentState.payUrl
    else {
      const popup = window.open(decision.paymentState.payUrl, 'paymentPopup', getPaymentPopupFeatures())
      if (!popup || popup.closed) window.location.href = decision.paymentState.payUrl
    }
  } else if (decision.kind === 'wechat_jsapi' && decision.jsapi) {
    await invokeWechatJSAPI(decision.jsapi as Record<string, unknown>)
  }
}

function buildWechatAuthorizeURL(authorizeURL: string, paymentType: string): string {
  try {
    const target = new URL(authorizeURL, window.location.origin)
    const redirect = new URL(window.location.pathname, window.location.origin)
    redirect.searchParams.set('security_deposit_group_id', String(props.groupId))
    redirect.searchParams.set('payment_type', paymentType)
    redirect.searchParams.set('order_type', 'security_deposit')
    target.searchParams.set('redirect', `${redirect.pathname}${redirect.search}`)
    return target.toString()
  } catch {
    return authorizeURL
  }
}

async function invokeWechatJSAPI(payload: Record<string, unknown>) {
  const bridgeWindow = window as Window & {
    WeixinJSBridge?: { invoke: (action: string, data: Record<string, unknown>, callback: (result: Record<string, unknown>) => void) => void }
  }
  if (!bridgeWindow.WeixinJSBridge) throw new Error(t('keys.securityDeposit.wechatUnavailable'))
  const result = await new Promise<Record<string, unknown>>(resolve => {
    bridgeWindow.WeixinJSBridge?.invoke('getBrandWCPayRequest', payload, resolve)
  })
  const message = String(result.err_msg || '').toLowerCase()
  if (message.includes('cancel')) {
    resetPayment()
    return
  }
  if (message && !message.includes('ok')) throw new Error(message)
  await handlePaymentSuccess()
}

async function handlePaymentSuccess() {
  const state = { ...paymentState.value }
  removeRecoverySnapshot()
  await router.push({
    path: '/payment/result',
    query: {
      order_id: state.orderId ? String(state.orderId) : undefined,
      out_trade_no: state.outTradeNo || undefined,
      resume_token: state.resumeToken || undefined,
    },
  })
}

function resetPayment() {
  phase.value = 'select'
  paymentState.value = emptyPaymentState()
  removeRecoverySnapshot()
}

function removeRecoverySnapshot() {
  clearPaymentRecoverySnapshot(window.localStorage, PAYMENT_RECOVERY_STORAGE_KEY)
}

function closeDialog() {
  if (submitting.value) return
  emit('close')
}

function emptyPaymentState(): PaymentRecoverySnapshot {
  return {
    orderId: 0, amount: 0, qrCode: '', expiresAt: '', paymentType: '', payUrl: '',
    outTradeNo: '', clientSecret: '', intentId: '', currency: '', countryCode: '',
    paymentEnv: '', payAmount: 0, orderType: 'security_deposit', paymentMode: '',
    resumeToken: '', alipayMobilePrecreateDeepLink: false, createdAt: 0,
  }
}

function formatCents(cents: number): string {
  return `¥${(Number(cents || 0) / 100).toFixed(2)}`
}

function isWechatBrowser(): boolean {
  return /MicroMessenger/i.test(window.navigator.userAgent)
}

function apiErrorCode(error: unknown): string {
  if (!error || typeof error !== 'object') return ''
  const candidate = error as Record<string, unknown>
  return String(candidate.code || candidate.reason || '')
}

function apiErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message
  if (error && typeof error === 'object' && 'message' in error) return String(error.message)
  return t('keys.securityDeposit.loadFailed')
}
</script>
