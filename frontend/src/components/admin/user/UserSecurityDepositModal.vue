<template>
  <BaseDialog :show="show" :title="t('admin.users.securityDeposit.title')" width="wide" @close="emit('close')">
    <div v-if="user" class="space-y-5">
      <div class="flex items-center justify-between gap-4 border-b border-gray-200 pb-4 dark:border-dark-600">
        <div class="min-w-0">
          <p class="truncate font-medium text-gray-900 dark:text-white">{{ user.email }}</p>
          <p class="text-sm text-gray-500 dark:text-dark-400">{{ user.username }}</p>
        </div>
        <button class="btn btn-secondary btn-sm" :disabled="loading" :title="t('common.refresh')" @click="load">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
        </button>
      </div>

      <div v-if="loading && !detail" class="flex justify-center py-12">
        <Icon name="refresh" size="lg" class="animate-spin text-primary-500" />
      </div>

      <template v-else-if="detail">
        <div class="grid grid-cols-2 gap-x-6 gap-y-4 md:grid-cols-4">
          <div>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.users.securityDeposit.total') }}</p>
            <p data-test="security-deposit-admin-total" class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">
              {{ formatCents(detail.account.total_balance_cents) }}
            </p>
          </div>
          <div>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.users.securityDeposit.paid') }}</p>
            <p class="mt-1 font-semibold text-gray-900 dark:text-white">{{ formatCents(detail.account.paid_balance_cents) }}</p>
          </div>
          <div>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.users.securityDeposit.adminGrant') }}</p>
            <p class="mt-1 font-semibold text-gray-900 dark:text-white">{{ formatCents(detail.account.admin_grant_balance_cents) }}</p>
          </div>
          <div>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.users.securityDeposit.risk') }}</p>
            <p class="mt-1 font-semibold text-gray-900 dark:text-white">
              {{ t('admin.users.securityDeposit.riskValue', { multiplier: detail.account.risk_multiplier, strikes: detail.account.cyber_strike_count }) }}
            </p>
          </div>
        </div>

        <div class="border-t border-gray-200 pt-5 dark:border-dark-600">
          <div class="grid grid-cols-3 overflow-hidden rounded-md border border-gray-200 dark:border-dark-600">
            <button
              v-for="option in operationOptions"
              :key="option.value"
              type="button"
              class="min-h-10 px-2 text-sm font-medium transition-colors"
              :class="operation === option.value
                ? 'bg-primary-600 text-white'
                : 'bg-white text-gray-600 hover:bg-gray-50 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700'"
              @click="operation = option.value"
            >
              {{ option.label }}
            </button>
          </div>

          <div class="mt-4 grid gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)_auto] md:items-end">
            <div>
              <label class="input-label">{{ t('admin.users.securityDeposit.amount') }}</label>
              <input v-model="amountYuan" type="number" min="0.01" step="0.01" class="input" data-test="security-deposit-admin-amount" />
            </div>
            <div>
              <label class="input-label">{{ t('admin.users.securityDeposit.reasonOptional') }}</label>
              <input v-model="reason" type="text" maxlength="1000" class="input" :placeholder="t('admin.users.securityDeposit.reasonPlaceholder')" />
            </div>
            <button class="btn btn-primary" :disabled="submitting || amountCents <= 0" data-test="security-deposit-admin-submit" @click="submitOperation">
              <Icon v-if="submitting" name="refresh" size="sm" class="animate-spin" />
              <Icon v-else :name="operation === 'deduct' ? 'ban' : 'plus'" size="sm" />
              {{ t('admin.users.securityDeposit.submit') }}
            </button>
          </div>
          <p v-if="operation === 'deduct'" class="mt-2 text-xs text-amber-700 dark:text-amber-300">
            {{ t('admin.users.securityDeposit.deductHint') }}
          </p>
        </div>

        <div class="border-t border-gray-200 pt-5 dark:border-dark-600">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.users.securityDeposit.lots') }}</h3>
          <div class="mt-3 max-h-56 divide-y divide-gray-100 overflow-y-auto dark:divide-dark-700">
            <div v-for="lot in detail.account.lots" :key="lot.id" class="py-3">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0 text-sm">
                  <p class="font-medium text-gray-900 dark:text-white">
                    #{{ lot.id }} · {{ lot.bucket_type === 'admin_grant' ? t('admin.users.securityDeposit.adminGrantLot') : t('admin.users.securityDeposit.paidLot') }}
                  </p>
                  <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                    {{ t('admin.users.securityDeposit.lotBalance', { remaining: formatCents(lot.remaining_cents), original: formatCents(lot.original_cents) }) }}
                  </p>
                </div>
                <div class="flex shrink-0 flex-wrap justify-end gap-2">
                  <button
                    v-if="lot.bucket_type === 'admin_grant' && lot.remaining_cents > 0"
                    class="btn btn-danger btn-sm"
                    :disabled="submitting"
                    @click="pendingRevokeLotId = pendingRevokeLotId === lot.id ? null : lot.id"
                  >
                    {{ t('admin.users.securityDeposit.revoke') }}
                  </button>
                  <button
                    v-if="lot.bucket_type === 'paid' && lot.remaining_cents > 0 && lot.refund_reserved_cents === 0"
                    class="btn btn-secondary btn-sm"
                    :disabled="submitting"
                    @click="selectLotRefund(lot.id, lot.provider_refund_enabled ? 'automatic' : 'manual')"
                  >
                    <Icon :name="lot.provider_refund_enabled ? 'creditCard' : 'clipboard'" size="sm" />
                    {{ lot.provider_refund_enabled ? t('admin.users.securityDeposit.originalRefund') : t('admin.users.securityDeposit.manualReserve') }}
                  </button>
                  <span v-if="lot.refund_reserved_cents > 0" class="text-xs font-medium text-amber-700 dark:text-amber-300">
                    {{ t('admin.users.securityDeposit.refundReserved', { amount: formatCents(lot.refund_reserved_cents) }) }}
                  </span>
                </div>
              </div>
              <div v-if="pendingRevokeLotId === lot.id" class="mt-3 flex items-center justify-between gap-3 bg-red-50 px-3 py-2 text-sm dark:bg-red-900/20">
                <span class="text-red-700 dark:text-red-300">{{ t('admin.users.securityDeposit.revokeConfirm', { amount: formatCents(lot.remaining_cents) }) }}</span>
                <div class="flex shrink-0 gap-2">
                  <button class="btn btn-secondary btn-sm" @click="pendingRevokeLotId = null">{{ t('common.cancel') }}</button>
                  <button class="btn btn-danger btn-sm" :disabled="submitting" @click="revokeLot(lot.id)">{{ t('common.confirm') }}</button>
                </div>
              </div>
              <div v-if="pendingRefundLotId === lot.id" class="mt-3 flex flex-col gap-3 bg-amber-50 px-3 py-3 text-sm dark:bg-amber-900/20 md:flex-row md:items-center md:justify-between">
                <span class="text-amber-800 dark:text-amber-200">
                  {{ pendingRefundMode === 'automatic'
                    ? t('admin.users.securityDeposit.originalRefundConfirm', { amount: formatCents(lot.remaining_cents) })
                    : t('admin.users.securityDeposit.manualReserveConfirm', { amount: formatCents(lot.remaining_cents) }) }}
                </span>
                <div class="flex shrink-0 gap-2">
                  <button class="btn btn-secondary btn-sm" @click="clearPendingRefund">{{ t('common.cancel') }}</button>
                  <button class="btn btn-primary btn-sm" :disabled="submitting" @click="submitLotRefund(lot.id)">{{ t('common.confirm') }}</button>
                </div>
              </div>
            </div>
            <p v-if="detail.account.lots.length === 0" class="py-6 text-center text-sm text-gray-500">
              {{ t('admin.users.securityDeposit.noLots') }}
            </p>
          </div>
        </div>

        <div class="border-t border-gray-200 pt-5 dark:border-dark-600">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.users.securityDeposit.refunds') }}</h3>
          <div class="mt-3 max-h-72 divide-y divide-gray-100 overflow-y-auto dark:divide-dark-700">
            <div v-for="refund in detail.refunds" :key="refund.id" class="py-3 text-sm">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <p class="font-medium text-gray-900 dark:text-white">
                    #{{ refund.refund_id }} · {{ formatCents(refund.principal_cents) }}
                  </p>
                  <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">
                    {{ refund.mode === 'manual_external' ? t('admin.users.securityDeposit.manualRefund') : t('admin.users.securityDeposit.originalRefund') }} ·
                    {{ refundStateLabel(refund.state) }} · {{ formatDateTime(refund.created_at) }}
                  </p>
                </div>
                <div v-if="refund.state === 'pending' || refund.state === 'manual_review'" class="flex shrink-0 flex-wrap justify-end gap-2">
                  <button v-if="refund.mode === 'automatic_original_channel'" class="btn btn-secondary btn-sm" :disabled="submitting" @click="queryRefund(refund.refund_id)">
                    <Icon name="refresh" size="sm" />
                    {{ t('admin.users.securityDeposit.queryRefund') }}
                  </button>
                  <button class="btn btn-secondary btn-sm" :disabled="submitting" @click="toggleManualRefund(refund.refund_id)">
                    {{ t('admin.users.securityDeposit.fillEvidence') }}
                  </button>
                  <button v-if="refund.mode === 'manual_external'" class="btn btn-danger btn-sm" :disabled="submitting" @click="cancelManualRefund(refund.refund_id)">
                    {{ t('common.cancel') }}
                  </button>
                </div>
              </div>
              <div v-if="editingManualRefundId === refund.refund_id" class="mt-3 grid gap-3 bg-gray-50 p-3 dark:bg-dark-800 md:grid-cols-2">
                <div>
                  <label class="input-label">{{ t('admin.users.securityDeposit.externalRefundId') }}</label>
                  <input v-model="externalRefundId" class="input" />
                </div>
                <div>
                  <label class="input-label">{{ t('admin.users.securityDeposit.externalRefundedAt') }}</label>
                  <input v-model="externalRefundedAt" type="datetime-local" class="input" />
                </div>
                <div class="md:col-span-2">
                  <label class="input-label">{{ t('admin.users.securityDeposit.externalEvidence') }}</label>
                  <input v-model="externalEvidence" class="input" :placeholder="t('admin.users.securityDeposit.externalEvidencePlaceholder')" />
                </div>
                <div class="flex justify-end gap-2 md:col-span-2">
                  <button class="btn btn-secondary btn-sm" @click="clearManualRefundForm">{{ t('common.cancel') }}</button>
                  <button
                    v-if="refund.mode === 'automatic_original_channel'"
                    class="btn btn-danger btn-sm"
                    :disabled="submitting || !externalEvidence.trim()"
                    @click="failAutomaticRefundReview(refund)"
                  >
                    {{ t('admin.users.securityDeposit.confirmGatewayFailed') }}
                  </button>
                  <button
                    class="btn btn-primary btn-sm"
                    :disabled="submitting || !externalRefundId.trim() || !externalRefundedAt || !externalEvidence.trim()"
                    @click="completeManualRefund(refund)"
                  >
                    {{ t('admin.users.securityDeposit.confirmExternalRefund') }}
                  </button>
                </div>
              </div>
            </div>
            <p v-if="detail.refunds.length === 0" class="py-6 text-center text-sm text-gray-500">
              {{ t('admin.users.securityDeposit.noRefunds') }}
            </p>
          </div>
        </div>

        <div class="border-t border-gray-200 pt-5 dark:border-dark-600">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.users.securityDeposit.ledger') }}</h3>
          <div class="mt-3 max-h-48 divide-y divide-gray-100 overflow-y-auto text-sm dark:divide-dark-700">
            <div v-for="entry in detail.ledger" :key="entry.id" class="flex items-center justify-between gap-4 py-2">
              <div class="min-w-0">
                <p class="font-medium text-gray-800 dark:text-gray-200">{{ entry.entry_type }} · #{{ entry.lot_id }}</p>
                <p class="truncate text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(entry.created_at) }}<template v-if="entry.reason"> · {{ entry.reason }}</template></p>
              </div>
              <span class="shrink-0 font-mono" :class="entry.delta_cents >= 0 ? 'text-emerald-600' : 'text-red-600'">
                {{ entry.delta_cents >= 0 ? '+' : '' }}{{ formatCents(entry.delta_cents) }}
              </span>
            </div>
          </div>
        </div>
      </template>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import type { AdminUser } from '@/types'
import type { AdminSecurityDepositUserDetail, SecurityDepositRefund, SecurityDepositRefundState } from '@/types/securityDeposit'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'

type Operation = 'admin_add' | 'compensation' | 'deduct'
type LotRefundMode = 'automatic' | 'manual'

const props = defineProps<{ show: boolean; user: AdminUser | null }>()
const emit = defineEmits<{ close: []; success: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const detail = ref<AdminSecurityDepositUserDetail | null>(null)
const loading = ref(false)
const submitting = ref(false)
const operation = ref<Operation>('admin_add')
const amountYuan = ref('')
const reason = ref('')
const pendingRevokeLotId = ref<number | null>(null)
const pendingRefundLotId = ref<number | null>(null)
const pendingRefundMode = ref<LotRefundMode | null>(null)
const editingManualRefundId = ref<string | null>(null)
const externalRefundId = ref('')
const externalRefundedAt = ref('')
const externalEvidence = ref('')

const operationOptions = computed(() => [
  { value: 'admin_add' as const, label: t('admin.users.securityDeposit.grant') },
  { value: 'compensation' as const, label: t('admin.users.securityDeposit.compensation') },
  { value: 'deduct' as const, label: t('admin.users.securityDeposit.deduct') },
])

const amountCents = computed(() => {
  const amount = Number(amountYuan.value)
  return Number.isFinite(amount) && amount > 0 ? Math.round(amount * 100) : 0
})

watch(() => props.show, (show) => {
  if (show && props.user) {
    detail.value = null
    amountYuan.value = ''
    reason.value = ''
    pendingRevokeLotId.value = null
    clearPendingRefund()
    clearManualRefundForm()
    void load()
  }
})

async function load() {
  if (!props.user) return
  loading.value = true
  try {
    detail.value = await adminAPI.securityDeposits.getUser(props.user.id)
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.securityDeposit.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function submitOperation() {
  if (!props.user || amountCents.value <= 0) return
  submitting.value = true
  try {
    if (operation.value === 'deduct') {
      await adminAPI.securityDeposits.deduct(props.user.id, amountCents.value, reason.value.trim())
    } else {
      await adminAPI.securityDeposits.credit(props.user.id, amountCents.value, operation.value, reason.value.trim())
    }
    amountYuan.value = ''
    reason.value = ''
    await load()
    emit('success')
    appStore.showSuccess(t('admin.users.securityDeposit.operationSucceeded'))
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.securityDeposit.operationFailed'))
  } finally {
    submitting.value = false
  }
}

async function revokeLot(lotId: number) {
  if (!props.user) return
  submitting.value = true
  try {
    await adminAPI.securityDeposits.revokeLot(props.user.id, lotId, reason.value.trim())
    pendingRevokeLotId.value = null
    reason.value = ''
    await load()
    emit('success')
    appStore.showSuccess(t('admin.users.securityDeposit.operationSucceeded'))
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.securityDeposit.operationFailed'))
  } finally {
    submitting.value = false
  }
}

function selectLotRefund(lotId: number, mode: LotRefundMode) {
  pendingRefundLotId.value = pendingRefundLotId.value === lotId ? null : lotId
  pendingRefundMode.value = pendingRefundLotId.value === null ? null : mode
}

function clearPendingRefund() {
  pendingRefundLotId.value = null
  pendingRefundMode.value = null
}

async function submitLotRefund(lotId: number) {
  if (!props.user || !pendingRefundMode.value) return
  submitting.value = true
  try {
    if (pendingRefundMode.value === 'automatic') {
      await adminAPI.securityDeposits.automaticallyRefundLot(props.user.id, lotId, reason.value.trim())
    } else {
      await adminAPI.securityDeposits.reserveManualRefund(props.user.id, lotId, reason.value.trim())
    }
    clearPendingRefund()
    reason.value = ''
    await load()
    emit('success')
    appStore.showSuccess(t('admin.users.securityDeposit.refundOperationSucceeded'))
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.securityDeposit.refundOperationFailed'))
  } finally {
    submitting.value = false
  }
}

function toggleManualRefund(refundId: string) {
  if (editingManualRefundId.value === refundId) {
    clearManualRefundForm()
    return
  }
  editingManualRefundId.value = refundId
  externalRefundId.value = ''
  externalEvidence.value = ''
  const now = new Date()
  now.setMinutes(now.getMinutes() - now.getTimezoneOffset())
  externalRefundedAt.value = now.toISOString().slice(0, 16)
}

function clearManualRefundForm() {
  editingManualRefundId.value = null
  externalRefundId.value = ''
  externalRefundedAt.value = ''
  externalEvidence.value = ''
}

async function completeManualRefund(refund: SecurityDepositRefund) {
  if (!props.user || !externalRefundId.value.trim() || !externalRefundedAt.value || !externalEvidence.value.trim()) return
  submitting.value = true
  try {
    await adminAPI.securityDeposits.completeManualRefund(props.user.id, refund.refund_id, {
      external_refund_id: externalRefundId.value.trim(),
      external_amount_cents: refund.principal_cents,
      external_refunded_at: new Date(externalRefundedAt.value).toISOString(),
      external_evidence: { reference: externalEvidence.value.trim() },
      reason: reason.value.trim() || undefined,
    })
    clearManualRefundForm()
    reason.value = ''
    await load()
    emit('success')
    appStore.showSuccess(t('admin.users.securityDeposit.refundOperationSucceeded'))
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.securityDeposit.refundOperationFailed'))
  } finally {
    submitting.value = false
  }
}

async function cancelManualRefund(refundId: string) {
  if (!props.user) return
  submitting.value = true
  try {
    await adminAPI.securityDeposits.cancelRefund(props.user.id, refundId, reason.value.trim())
    clearManualRefundForm()
    reason.value = ''
    await load()
    emit('success')
    appStore.showSuccess(t('admin.users.securityDeposit.refundOperationSucceeded'))
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.users.securityDeposit.refundOperationFailed'))
  } finally {
    submitting.value = false
  }
}

async function queryRefund(refundId: string) {
  if (!props.user) return
  submitting.value = true
  try {
    await adminAPI.securityDeposits.queryRefund(props.user.id, refundId)
    await load()
    appStore.showSuccess(t('admin.users.securityDeposit.refundQuerySucceeded'))
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.users.securityDeposit.refundQueryFailed'))
    await load()
  } finally {
    submitting.value = false
  }
}

async function failAutomaticRefundReview(refund: SecurityDepositRefund) {
  if (!props.user || !externalEvidence.value.trim()) return
  submitting.value = true
  try {
    await adminAPI.securityDeposits.failAutomaticRefundReview(
      props.user.id,
      refund.refund_id,
      { reference: externalEvidence.value.trim() },
      reason.value.trim() || undefined,
    )
    clearManualRefundForm()
    reason.value = ''
    await load()
    emit('success')
    appStore.showSuccess(t('admin.users.securityDeposit.refundReviewReleased'))
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.users.securityDeposit.refundOperationFailed'))
  } finally {
    submitting.value = false
  }
}

function refundStateLabel(state: SecurityDepositRefundState) {
  return t(`admin.users.securityDeposit.refundStates.${state}`)
}

function formatCents(cents: number) {
  return new Intl.NumberFormat(undefined, { style: 'currency', currency: 'CNY' }).format(cents / 100)
}
</script>
