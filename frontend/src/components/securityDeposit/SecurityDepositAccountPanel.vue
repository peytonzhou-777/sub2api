<template>
  <section data-test="security-deposit-panel" class="card p-6">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <Icon name="shield" size="sm" class="text-emerald-600 dark:text-emerald-400" />
          <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('payment.securityDeposit.title') }}
          </h2>
        </div>
        <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
          {{ t('payment.securityDeposit.separateBalanceHint') }}
        </p>
      </div>
      <button
        v-if="error"
        type="button"
        class="btn btn-secondary btn-sm inline-flex items-center gap-2"
        @click="emit('refresh')"
      >
        <Icon name="refresh" size="sm" />
        {{ t('common.retry') }}
      </button>
    </div>

    <div v-if="loading" class="flex justify-center py-10">
      <div class="h-7 w-7 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
    </div>
    <p v-else-if="error" class="mt-5 text-sm text-red-600 dark:text-red-400">
      {{ t('payment.securityDeposit.loadFailed') }}
    </p>
    <template v-else-if="account">
      <div class="mt-5 flex flex-wrap items-end justify-between gap-3 border-b border-gray-100 pb-5 dark:border-dark-700">
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.totalBalance') }}</p>
          <p data-test="security-deposit-total" class="mt-1 text-2xl font-bold text-gray-900 dark:text-white">
            {{ formatCents(account.total_balance_cents) }}
          </p>
        </div>
        <div class="text-right text-xs text-gray-500 dark:text-gray-400">
          <p>{{ t('payment.securityDeposit.riskMultiplier', { multiplier: account.risk_multiplier }) }}</p>
          <p v-if="account.risk_multiplier > 1" class="mt-1 text-amber-600 dark:text-amber-400">
            {{ t('payment.securityDeposit.riskMultiplierAdjusted', { multiplier: account.risk_multiplier }) }}
          </p>
          <button
            type="button"
            data-test="security-deposit-account-details-toggle"
            class="ml-auto mt-3 inline-flex items-center gap-1.5 font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
            :aria-expanded="accountDetailsOpen"
            aria-controls="security-deposit-account-details"
            @click="accountDetailsOpen = !accountDetailsOpen"
          >
            <Icon :name="accountDetailsOpen ? 'chevronUp' : 'chevronDown'" size="sm" />
            {{ accountDetailsOpen
              ? t('payment.securityDeposit.hideAccountDetails')
              : t('payment.securityDeposit.viewAccountDetails') }}
          </button>
        </div>
      </div>

      <div
        v-if="accountDetailsOpen"
        id="security-deposit-account-details"
        data-test="security-deposit-account-details"
      >
        <div class="grid gap-x-6 gap-y-4 py-5 sm:grid-cols-2 lg:grid-cols-4">
          <div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.paidBalance') }}</p>
            <p data-test="security-deposit-paid" class="mt-1 font-semibold text-gray-900 dark:text-white">
              {{ formatCents(account.paid_balance_cents) }}
            </p>
          </div>
          <div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.adminGrantBalance') }}</p>
            <p data-test="security-deposit-admin-grant" class="mt-1 font-semibold text-gray-900 dark:text-white">
              {{ formatCents(account.admin_grant_balance_cents) }}
            </p>
          </div>
          <div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.refundable') }}</p>
            <p class="mt-1 font-semibold text-emerald-700 dark:text-emerald-400">
              {{ formatCents(account.refundable_cents) }}
            </p>
          </div>
          <div>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.refundPending') }}</p>
            <p class="mt-1 font-semibold text-gray-900 dark:text-white">
              {{ formatCents(account.paid_refund_reserved_cents) }}
            </p>
          </div>
        </div>

        <div class="flex flex-wrap gap-x-5 gap-y-2 border-t border-gray-100 pt-4 text-xs text-gray-500 dark:border-dark-700 dark:text-gray-400">
          <span>{{ t('payment.securityDeposit.timedLocked', { amount: formatCents(account.timed_locked_cents) }) }}</span>
          <span>{{ t('payment.securityDeposit.permanentLocked', { amount: formatCents(account.permanent_locked_cents) }) }}</span>
          <span v-if="account.next_unlock_at">
            {{ t('payment.securityDeposit.nextUnlockAt', { time: formatDateTime(account.next_unlock_at) }) }}
          </span>
        </div>
        <p class="mt-3 text-xs leading-5 text-gray-500 dark:text-gray-400">
          {{ t('payment.securityDeposit.lockExplanation') }}
        </p>

        <div
          v-if="account.bonus"
          data-test="security-deposit-bonus"
          class="mt-5 border-y border-gray-100 py-4 dark:border-dark-700"
        >
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-white">
                {{ t('payment.securityDeposit.bonusTitle') }}
              </p>
              <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
                {{ t('payment.securityDeposit.bonusDescription') }}
              </p>
            </div>
            <span v-if="account.bonus.expires_at" class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('payment.securityDeposit.bonusExpiresAt', { time: formatDateTime(account.bonus.expires_at) }) }}
            </span>
          </div>
          <div class="mt-4 grid gap-x-6 gap-y-4 sm:grid-cols-2 lg:grid-cols-4">
            <div>
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.bonusCurrent') }}</p>
              <p data-test="security-deposit-bonus-current" class="mt-1 font-semibold text-gray-900 dark:text-white">
                {{ formatBonusAmount(account.bonus.current_amount) }}
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.bonusDaily') }}</p>
              <p class="mt-1 font-semibold text-gray-900 dark:text-white">
                {{ formatBonusAmount(account.bonus.daily_amount) }}
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                {{ t('payment.securityDeposit.bonusCap', { ratio: account.bonus.cap_ratio }) }}
              </p>
              <p class="mt-1 font-semibold text-gray-900 dark:text-white">
                {{ formatBonusAmount(account.bonus.cap_amount) }}
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.bonusEstimated') }}</p>
              <p data-test="security-deposit-bonus-estimated" class="mt-1 font-semibold text-emerald-700 dark:text-emerald-400">
                {{ formatBonusAmount(account.bonus.estimated_grant_amount) }}
              </p>
            </div>
          </div>
          <p data-test="security-deposit-bonus-status" class="mt-4 text-xs leading-5 text-gray-500 dark:text-gray-400">
            {{ bonusStatus(account.bonus) }}
          </p>
        </div>

        <div v-if="account.lots.length" class="mt-5 border-t border-gray-100 pt-4 dark:border-dark-700">
          <button
            type="button"
            class="inline-flex items-center gap-2 text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400"
            @click="detailsOpen = !detailsOpen"
          >
            <Icon :name="detailsOpen ? 'chevronUp' : 'chevronDown'" size="sm" />
            {{ detailsOpen ? t('payment.securityDeposit.hideDetails') : t('payment.securityDeposit.viewDetails') }}
          </button>
          <div v-if="detailsOpen" data-test="security-deposit-lots" class="mt-4 divide-y divide-gray-100 dark:divide-dark-700">
            <div v-for="lot in account.lots" :key="lot.id" class="grid gap-2 py-4 text-sm sm:grid-cols-[minmax(0,1fr)_auto]">
              <div class="min-w-0">
                <p class="font-medium text-gray-900 dark:text-white">
                  {{ lot.bucket_type === 'admin_grant' ? t('payment.securityDeposit.adminGrantLot') : t('payment.securityDeposit.paidLot') }} #{{ lot.id }}
                </p>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ formatDateTime(lot.created_at) }} · {{ lotStatus(lot) }}
                </p>
              </div>
              <div class="text-left sm:text-right">
                <p class="font-semibold text-gray-900 dark:text-white">{{ formatCents(lot.remaining_cents) }}</p>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                  {{ t('payment.securityDeposit.originalAmount', { amount: formatCents(lot.original_cents) }) }}
                </p>
                <button
                  v-if="lot.self_refund_eligible"
                  type="button"
                  class="btn btn-secondary btn-sm mt-2"
                  :disabled="refundLoading"
                  :data-test="`security-deposit-refund-${lot.id}`"
                  @click="openRefund(lot)"
                >
                  {{ t('payment.securityDeposit.refundAction') }}
                </button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </template>
  </section>

  <BaseDialog
    :show="refundDialogOpen"
    :title="t('payment.securityDeposit.refundTitle')"
    width="normal"
    @close="closeRefundDialog"
  >
    <div v-if="refundLoading" class="flex justify-center py-10">
      <div class="h-7 w-7 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
    </div>
    <div v-else-if="refundPreview" class="space-y-4">
      <div class="grid gap-3 border-b border-gray-100 pb-4 dark:border-dark-700 sm:grid-cols-2">
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.refundPrincipal') }}</p>
          <p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ formatCents(refundPreview.principal_cents) }}</p>
        </div>
        <div>
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('payment.securityDeposit.gatewayRefundAmount') }}</p>
          <p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">
            {{ refundPreview.gateway_currency }} {{ refundPreview.gateway_amount }}
          </p>
        </div>
      </div>
      <div v-if="refundPreview.affected_api_keys.length" class="rounded-md border border-amber-200 bg-amber-50 p-3 dark:border-amber-900/60 dark:bg-amber-950/30">
        <p class="text-sm font-medium text-amber-800 dark:text-amber-300">
          {{ t('payment.securityDeposit.affectedKeys', { count: refundPreview.affected_api_keys.length }) }}
        </p>
        <ul class="mt-2 space-y-1 text-xs text-amber-700 dark:text-amber-300">
          <li v-for="key in refundPreview.affected_api_keys" :key="key.api_key_id" class="break-words">
            {{ key.api_key_name }} · {{ key.group_name }} · {{ t('payment.securityDeposit.requiredAfterRefund', { amount: formatCents(key.required_cents) }) }}
          </li>
        </ul>
      </div>
      <p class="text-sm leading-6 text-gray-600 dark:text-gray-300">
        {{ t('payment.securityDeposit.refundConfirmationHint') }}
      </p>
    </div>
    <template #footer>
      <div class="flex flex-wrap justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="refundSubmitting" @click="closeRefundDialog">
          {{ t('common.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="!refundPreview || refundSubmitting" @click="submitRefund">
          {{ refundSubmitting ? t('common.processing') : t('payment.securityDeposit.confirmRefund') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <TotpStepUpDialog :controller="refundStepUp" />
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import securityDepositsAPI from '@/api/securityDeposits'
import { useAppStore } from '@/stores/app'
import { isStepUpCancelled, useStepUp } from '@/composables/useStepUp'
import { formatDateTime } from '@/utils/format'
import type {
  SecurityDepositAccount,
  SecurityDepositBonusEstimate,
  SecurityDepositLot,
  SecurityDepositRefundPreview
} from '@/types/securityDeposit'

defineProps<{
  account: SecurityDepositAccount | null
  loading: boolean
  error?: boolean
}>()

const emit = defineEmits<{ refresh: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const accountDetailsOpen = ref(false)
const detailsOpen = ref(false)
const refundDialogOpen = ref(false)
const refundLoading = ref(false)
const refundSubmitting = ref(false)
const refundLotId = ref<number | null>(null)
const refundPreview = ref<SecurityDepositRefundPreview | null>(null)
const refundStepUp = useStepUp()

// 保证金统一以人民币分记账，展示层固定转换为元。
function formatCents(cents: number): string {
  return `¥${(Number(cents || 0) / 100).toFixed(2)}`
}

function formatBonusAmount(amount: number): string {
  return `$${Number(amount || 0).toFixed(2)}`
}

// bonusStatus 解释下一次赠额预估，避免把配置值误认为一定到账金额。
function bonusStatus(bonus: SecurityDepositBonusEstimate): string {
  if (bonus.reason === 'eligible') {
    return t('payment.securityDeposit.bonusEligible', {
      amount: formatBonusAmount(bonus.estimated_grant_amount),
      time: formatDateTime(bonus.next_grant_at),
      group: bonus.qualifying_group_name || '-'
    })
  }
  return t(`payment.securityDeposit.bonusReasons.${bonus.reason}`)
}

function lotStatus(lot: SecurityDepositLot): string {
  if (lot.bucket_type === 'admin_grant') return t('payment.securityDeposit.permanentlyLocked')
  if (lot.self_refund_eligible) return t('payment.securityDeposit.refundableNow')
  if (lot.admin_action_required) return t('payment.securityDeposit.contactAdmin')
  if (lot.locked_until) return t('payment.securityDeposit.lockedUntil', { time: formatDateTime(lot.locked_until) })
  return t('payment.securityDeposit.notRefundable')
}

async function openRefund(lot: SecurityDepositLot) {
  refundDialogOpen.value = true
  refundLoading.value = true
  refundLotId.value = lot.id
  refundPreview.value = null
  try {
    const { data } = await securityDepositsAPI.previewRefund(lot.id)
    refundPreview.value = data
  } catch (error: any) {
    refundDialogOpen.value = false
    appStore.showError(error?.message || t('payment.securityDeposit.refundFailed'))
    emit('refresh')
  } finally {
    refundLoading.value = false
  }
}

function closeRefundDialog() {
  if (refundSubmitting.value) return
  refundDialogOpen.value = false
  refundLotId.value = null
  refundPreview.value = null
}

async function submitRefund() {
  if (!refundLotId.value || !refundPreview.value) return
  refundSubmitting.value = true
  try {
    const lotId = refundLotId.value
    const { data } = await refundStepUp.run(() => securityDepositsAPI.createRefund(lotId))
    refundSubmitting.value = false
    closeRefundDialog()
    emit('refresh')
    if (data.state === 'pending' || data.state === 'manual_review') {
      appStore.showWarning(t('payment.securityDeposit.refundPendingReview'))
    } else {
      appStore.showSuccess(t('payment.securityDeposit.refundSucceeded'))
    }
  } catch (error: any) {
    if (!isStepUpCancelled(error)) {
      appStore.showError(error?.message || t('payment.securityDeposit.refundFailed'))
      emit('refresh')
    }
  } finally {
    refundSubmitting.value = false
  }
}
</script>
