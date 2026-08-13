<template>
  <AppLayout>
    <div class="mx-auto w-full max-w-6xl space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <button class="btn btn-secondary inline-flex items-center gap-2" :disabled="isLocked" @click="router.push('/purchase?tab=account')">
          <Icon name="arrowLeft" size="sm" />
          <span>{{ t('payment.refunds.backToAccount') }}</span>
        </button>
        <button class="btn btn-secondary" :disabled="loading || submitting" :title="t('common.refresh')" @click="loadRefund">
          <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
        </button>
      </div>

      <section
        v-if="contactInfo"
        data-test="refund-contact-info"
        class="flex items-start gap-3 border-y border-gray-200 py-4 dark:border-dark-700"
      >
        <Icon name="chat" size="md" class="mt-0.5 shrink-0 text-primary-600 dark:text-primary-400" />
        <div class="min-w-0">
          <h2 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('common.contactSupport') }}</h2>
          <p class="mt-1 whitespace-pre-wrap break-words text-sm leading-6 text-gray-600 dark:text-gray-300">{{ contactInfo }}</p>
        </div>
      </section>

      <div v-if="loading && !record" class="card flex min-h-56 items-center justify-center">
        <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      </div>

      <section v-else-if="recoveryRequired" class="mx-auto w-full max-w-md space-y-5">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('payment.refunds.restoreTitle') }}</h1>
          <p class="mt-2 text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('payment.refunds.restoreDescription') }}</p>
        </div>
        <form class="space-y-4" @submit.prevent="restoreSession">
          <div>
            <label for="refund-email" class="input-label">{{ t('auth.emailLabel') }}</label>
            <input id="refund-email" v-model.trim="recovery.email" type="email" autocomplete="email" required class="input" :placeholder="t('auth.emailPlaceholder')" />
          </div>
          <div>
            <label for="refund-password" class="input-label">{{ t('auth.passwordLabel') }}</label>
            <input id="refund-password" v-model="recovery.password" type="password" autocomplete="current-password" required class="input" :placeholder="t('auth.passwordPlaceholder')" />
          </div>
          <div>
            <label for="refund-totp" class="input-label">{{ t('payment.refunds.totpCode') }}</label>
            <input id="refund-totp" v-model.trim="recovery.totpCode" type="text" inputmode="numeric" autocomplete="one-time-code" maxlength="6" class="input" :placeholder="t('payment.refunds.totpPlaceholder')" />
          </div>
          <button type="submit" class="btn btn-primary w-full" :disabled="submitting">
            <Icon name="lock" size="sm" />
            <span>{{ submitting ? t('common.processing') : t('payment.refunds.restore') }}</span>
          </button>
        </form>
      </section>

      <template v-else-if="record && quote">
        <section class="border-b border-gray-200 pb-5 dark:border-dark-700">
          <div class="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
            <div>
              <p class="text-sm font-medium text-gray-500 dark:text-gray-400">{{ t('payment.refunds.title') }}</p>
              <p class="mt-2 text-3xl font-bold text-gray-900 dark:text-white">{{ formatMoney(quote.refund_credit_total) }}</p>
              <p class="mt-2 max-w-3xl text-sm leading-6 text-gray-600 dark:text-gray-300">{{ stateText }}</p>
            </div>
            <span class="badge shrink-0" :class="stateBadgeClass">{{ stateLabel }}</span>
          </div>
        </section>

        <section class="grid gap-4 md:grid-cols-3">
          <div class="card p-5">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.refunds.permanentBalance') }}</p>
            <p class="mt-2 text-xl font-semibold text-gray-900 dark:text-white">{{ formatMoney(quote.permanent_balance) }}</p>
          </div>
          <div class="card p-5">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.refunds.rechargeBonus') }}</p>
            <p class="mt-2 text-xl font-semibold text-gray-900 dark:text-white">{{ formatMoney(quote.recharge_bonus_balance) }}</p>
          </div>
          <div class="card p-5">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.refunds.otherCreditToClear') }}</p>
            <p class="mt-2 text-xl font-semibold text-amber-600 dark:text-amber-400">{{ formatMoney(quote.other_limited_to_clear) }}</p>
          </div>
        </section>

        <section class="card overflow-hidden">
          <div class="border-b border-gray-100 px-5 py-4 dark:border-dark-700">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('payment.refunds.gatewayTotals') }}</h2>
          </div>
          <div class="divide-y divide-gray-100 dark:divide-dark-700">
            <div v-for="(amount, currency) in quote.gateway_totals" :key="currency" class="flex items-center justify-between gap-4 px-5 py-4 text-sm">
              <span class="font-medium uppercase text-gray-600 dark:text-gray-300">{{ currency }}</span>
              <span class="font-semibold text-gray-900 dark:text-white">{{ formatCurrency(amount, String(currency)) }}</span>
            </div>
          </div>
        </section>

        <section>
          <div class="mb-3 flex items-center justify-between gap-4">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('payment.refunds.orders') }}</h2>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.refunds.orderCount', { count: quote.orders.length }) }}</span>
          </div>
          <div class="overflow-x-auto rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
            <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-700/60 dark:text-gray-400">
                <tr>
                  <th class="px-4 py-3 font-medium">{{ t('payment.refunds.order') }}</th>
                  <th class="px-4 py-3 font-medium">{{ t('payment.refunds.paid') }}</th>
                  <th class="px-4 py-3 font-medium">{{ t('payment.refunds.bonus') }}</th>
                  <th class="px-4 py-3 font-medium">{{ t('payment.refunds.eligibleCredit') }}</th>
                  <th class="px-4 py-3 font-medium">{{ t('payment.refunds.actualRefund') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="order in quote.orders" :key="order.order_id">
                  <td class="whitespace-nowrap px-4 py-4">
                    <p class="font-mono font-medium text-gray-900 dark:text-white">#{{ order.order_id }}</p>
                    <p class="mt-1 text-xs text-gray-500">{{ formatDateTime(order.completed_at) }}</p>
                  </td>
                  <td class="whitespace-nowrap px-4 py-4 text-gray-700 dark:text-gray-200">{{ formatCurrency(order.original_paid, order.currency) }}</td>
                  <td class="whitespace-nowrap px-4 py-4 text-gray-700 dark:text-gray-200">
                    {{ formatMoney(order.bonus_remaining) }}
                    <span v-if="order.bonus_rate > 0" class="ml-1 text-xs text-gray-500">({{ formatRate(order.bonus_rate) }})</span>
                  </td>
                  <td class="whitespace-nowrap px-4 py-4 text-gray-700 dark:text-gray-200">{{ formatMoney(order.eligible_credit) }}</td>
                  <td class="whitespace-nowrap px-4 py-4 font-semibold text-gray-900 dark:text-white">{{ formatCurrency(order.gateway_refund, order.currency) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <section v-if="quote.block_reason" class="rounded-lg border border-amber-200 bg-amber-50 p-4 dark:border-amber-800 dark:bg-amber-900/20">
          <div class="flex items-start gap-3">
            <Icon name="exclamationTriangle" size="md" class="mt-0.5 shrink-0 text-amber-600" />
            <div>
              <p class="font-medium text-amber-900 dark:text-amber-200">{{ t('payment.refunds.manualReview') }}</p>
              <p class="mt-1 text-sm text-amber-800 dark:text-amber-300">{{ quote.block_reason }}</p>
            </div>
          </div>
        </section>

        <section v-if="canStart" class="card p-5">
          <label class="flex items-start gap-3">
            <input v-model="acknowledged" type="checkbox" class="mt-1 h-4 w-4 rounded border-gray-300 text-primary-600" />
            <span class="text-sm leading-6 text-gray-700 dark:text-gray-200">{{ t('payment.refunds.lockAcknowledgement') }}</span>
          </label>
          <button class="btn btn-danger mt-5 w-full sm:w-auto" :disabled="!acknowledged || submitting" @click="startRefund">
            <Icon name="lock" size="sm" />
            <span>{{ submitting ? t('common.processing') : t('payment.refunds.start') }}</span>
          </button>
        </section>

        <section v-else-if="record.state === 'draining'" class="card flex items-center gap-4 p-5">
          <div class="h-6 w-6 shrink-0 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
          <div>
            <p class="font-medium text-gray-900 dark:text-white">{{ t('payment.refunds.draining') }}</p>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ record.message }}</p>
          </div>
        </section>

        <section v-else-if="record.state === 'ready_to_confirm'" class="card p-5">
          <label class="flex items-start gap-3">
            <input v-model="finalAcknowledged" type="checkbox" class="mt-1 h-4 w-4 rounded border-gray-300 text-primary-600" />
            <span class="text-sm leading-6 text-gray-700 dark:text-gray-200">{{ t('payment.refunds.finalAcknowledgement') }}</span>
          </label>
          <div class="mt-5 flex flex-wrap gap-3">
            <button class="btn btn-danger" :disabled="!finalAcknowledged || submitting" @click="confirmRefund">
              <Icon name="dollar" size="sm" />
              <span>{{ submitting ? t('common.processing') : t('payment.refunds.confirm') }}</span>
            </button>
            <button class="btn btn-secondary" :disabled="submitting" @click="cancelRefund">{{ t('common.cancel') }}</button>
          </div>
        </section>
      </template>

      <section v-if="canDonate || donations.length > 0" class="ml-auto w-full max-w-xl text-right">
        <button
          v-if="canDonate"
          data-test="refund-donate-trigger"
          type="button"
          class="text-xs text-gray-500 underline decoration-gray-400 underline-offset-4 transition-colors hover:text-primary-600 dark:text-gray-400 dark:hover:text-primary-400"
          :disabled="submitting"
          @click="donationDialogOpen = true"
        >
          {{ t('payment.refunds.donateTrigger') }}
        </button>

        <div v-if="donations.length > 0" data-test="refund-donation-list" class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-700">
          <h2 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('payment.refunds.donationList') }}</h2>
          <div class="mt-2 divide-y divide-gray-100 dark:divide-dark-700">
            <div
              v-for="donation in donations"
              :key="`${donation.masked_email}-${donation.donated_at}`"
              class="flex flex-wrap items-center justify-end gap-x-3 gap-y-1 py-2 text-xs"
            >
              <span class="font-medium text-gray-800 dark:text-gray-200">{{ donation.username }}</span>
              <span class="break-all text-gray-500 dark:text-gray-400">{{ donation.masked_email }}</span>
              <span class="font-semibold text-gray-900 dark:text-white">{{ formatMoney(donation.amount) }}</span>
            </div>
          </div>
        </div>
      </section>
    </div>

    <ConfirmDialog
      :show="donationDialogOpen"
      :title="t('payment.refunds.donateConfirmTitle')"
      :message="t('payment.refunds.donateConfirmMessage', { amount: formatMoney(quote?.donation_amount || 0) })"
      :confirm-text="t('payment.refunds.donateConfirm')"
      danger
      @confirm="confirmDonation"
      @cancel="donationDialogOpen = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { paymentAPI } from '@/api/payment'
import { useAppStore } from '@/stores'
import { useAuthStore } from '@/stores/auth'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import type { AccountRefundDonation, AccountRefundRecord } from '@/types/payment'
import AppLayout from '@/components/layout/AppLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'

const refundSessionKey = 'account_refund_session'
const refundIdKey = 'account_refund_id'
const { t } = useI18n()
const router = useRouter()
const appStore = useAppStore()
const authStore = useAuthStore()
const loading = ref(false)
const submitting = ref(false)
const recoveryRequired = ref(false)
const recovery = ref({ email: '', password: '', totpCode: '' })
const acknowledged = ref(false)
const finalAcknowledged = ref(false)
const record = ref<AccountRefundRecord | null>(null)
const donations = ref<AccountRefundDonation[]>([])
const donationDialogOpen = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | null = null

const quote = computed(() => record.value?.quote || null)
const contactInfo = computed(() => appStore.contactInfo.trim())
const isLocked = computed(() => !!record.value?.refund_id && !['estimate', 'canceled'].includes(record.value.state))
const canStart = computed(() => record.value?.state === 'estimate' && !!quote.value?.eligible)
const canDonate = computed(() => {
  if (!quote.value?.donation_eligible || Number(quote.value.donation_amount) <= 0) return false
  return ['estimate', 'draining', 'ready_to_confirm', 'manual_review'].includes(record.value?.state || '')
})
const stateLabel = computed(() => t(`payment.refunds.states.${record.value?.state || 'estimate'}`))
const stateText = computed(() => record.value?.message || (quote.value?.eligible ? t('payment.refunds.estimateReady') : t('payment.refunds.estimateBlocked')))
const stateBadgeClass = computed(() => {
  if (record.value?.state === 'succeeded') return 'badge-success'
  if (['failed', 'partial_external_success', 'manual_review'].includes(record.value?.state || '')) return 'badge-warning'
  return 'badge-info'
})

function formatMoney(value: number) { return `$${Number(value || 0).toFixed(2)}` }
function formatCurrency(value: number, currency: string) {
  try { return new Intl.NumberFormat(undefined, { style: 'currency', currency: currency.toUpperCase() }).format(value) }
  catch { return `${currency.toUpperCase()} ${Number(value || 0).toFixed(2)}` }
}
function formatRate(value: number) { return `${(value * 100).toFixed(2)}%` }

function schedulePoll() {
  if (pollTimer) clearTimeout(pollTimer)
  if (['draining', 'submitting', 'pending'].includes(record.value?.state || '')) {
    pollTimer = setTimeout(loadRefund, 2000)
  }
}

async function loadRefund() {
  loading.value = true
  try {
    const refundId = sessionStorage.getItem(refundIdKey)
    const sessionToken = sessionStorage.getItem(refundSessionKey)
    if ((!refundId || !sessionToken) && !authStore.isAuthenticated) {
      recoveryRequired.value = true
      return
    }
    const response = refundId && sessionToken
      ? await paymentAPI.getAccountRefund(refundId, sessionToken)
      : await paymentAPI.getAccountRefundOverview()
    recoveryRequired.value = false
    record.value = response.data
    if (response.data.state === 'donated') {
      await loadDonations()
    }
    if (['canceled', 'succeeded', 'donated'].includes(response.data.state)) {
      sessionStorage.removeItem(refundIdKey)
      sessionStorage.removeItem(refundSessionKey)
    }
  } catch (err: unknown) {
    if (['REFUND_SESSION_EXPIRED', 'INVALID_REFUND_SESSION', 'REFUND_SESSION_REQUIRED', 'USER_NOT_ACTIVE', 'USER_REFUND_LOCKED'].includes(extractApiErrorCode(err) || '')) {
      sessionStorage.removeItem(refundIdKey)
      sessionStorage.removeItem(refundSessionKey)
      recoveryRequired.value = true
      record.value = null
      return
    }
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    loading.value = false
    schedulePoll()
  }
}

async function loadDonations() {
  try {
    donations.value = (await paymentAPI.getAccountRefundDonations()).data
  } catch {
    donations.value = []
  }
}

async function restoreSession() {
  submitting.value = true
  try {
    const response = await paymentAPI.restoreAccountRefundSession(recovery.value.email, recovery.value.password, recovery.value.totpCode)
    if (!response.data.refund_id || !response.data.session_token) return
    sessionStorage.setItem(refundIdKey, response.data.refund_id)
    sessionStorage.setItem(refundSessionKey, response.data.session_token)
    recovery.value.password = ''
    recovery.value.totpCode = ''
    recoveryRequired.value = false
    record.value = response.data
    schedulePoll()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    submitting.value = false
  }
}

async function startRefund() {
  if (!quote.value) return
  submitting.value = true
  try {
    const response = await paymentAPI.lockAccountRefund(quote.value.quote_hash)
    record.value = response.data
    if (response.data.refund_id && response.data.session_token) {
      sessionStorage.setItem(refundIdKey, response.data.refund_id)
      sessionStorage.setItem(refundSessionKey, response.data.session_token)
    }
    schedulePoll()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally { submitting.value = false }
}

async function confirmRefund() {
  const refundId = record.value?.refund_id
  const sessionToken = sessionStorage.getItem(refundSessionKey)
  if (!refundId || !sessionToken || !quote.value) return
  submitting.value = true
  try {
    record.value = (await paymentAPI.confirmAccountRefund(refundId, quote.value.quote_hash, sessionToken)).data
    schedulePoll()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally { submitting.value = false }
}

async function confirmDonation() {
  if (!quote.value) return
  donationDialogOpen.value = false
  submitting.value = true
  try {
    const refundId = record.value?.refund_id
    const sessionToken = sessionStorage.getItem(refundSessionKey)
    const response = refundId && sessionToken
      ? await paymentAPI.donateLockedAccountRefund(refundId, quote.value.quote_hash, sessionToken)
      : await paymentAPI.donateAccountRefund(quote.value.quote_hash)
    record.value = response.data
    if (response.data.refund_id && response.data.session_token) {
      sessionStorage.setItem(refundIdKey, response.data.refund_id)
      sessionStorage.setItem(refundSessionKey, response.data.session_token)
    }
    if (response.data.state === 'donated') {
      sessionStorage.removeItem(refundIdKey)
      sessionStorage.removeItem(refundSessionKey)
      await loadDonations()
    } else {
      schedulePoll()
    }
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    submitting.value = false
  }
}

async function cancelRefund() {
  const refundId = record.value?.refund_id
  const sessionToken = sessionStorage.getItem(refundSessionKey)
  if (!refundId || !sessionToken) return
  submitting.value = true
  try {
    record.value = (await paymentAPI.cancelAccountRefund(refundId, sessionToken)).data
    sessionStorage.removeItem(refundIdKey)
    sessionStorage.removeItem(refundSessionKey)
    appStore.showSuccess(t('payment.refunds.canceled'))
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally { submitting.value = false }
}

onMounted(() => {
  // 清退锁定会使普通登录失效，因此客服信息始终通过公开设置加载。
  void appStore.fetchPublicSettings()
  void loadDonations()
  void loadRefund()
})
onBeforeUnmount(() => { if (pollTimer) clearTimeout(pollTimer) })
</script>
