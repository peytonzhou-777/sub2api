<template>
  <BaseDialog :show="show" :title="t('balanceRefunds.reconcileTitle')" width="wide" @close="$emit('close')">
    <div v-if="detail?.quote" class="space-y-4">
      <div>
        <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('balanceRefunds.order') }}</label>
        <select v-model.number="form.order_id" class="input">
          <option v-for="route in pendingRoutes" :key="route.order_id" :value="route.order_id">#{{ route.order_id }} · {{ formatMoney(route.currency, route.gateway_refund) }}</option>
        </select>
      </div>
      <div class="grid grid-cols-2 gap-3">
        <label class="flex items-center gap-2 rounded border border-gray-200 p-3 text-sm dark:border-dark-600"><input v-model="form.outcome" type="radio" value="succeeded" />{{ t('balanceRefunds.reconcileSucceeded') }}</label>
        <label class="flex items-center gap-2 rounded border border-gray-200 p-3 text-sm dark:border-dark-600"><input v-model="form.outcome" type="radio" value="failed" />{{ t('balanceRefunds.reconcileFailed') }}</label>
      </div>
      <input v-if="form.outcome === 'succeeded'" v-model.trim="form.external_refund_id" class="input" :placeholder="t('balanceRefunds.externalRefundId')" />
      <input v-model.trim="form.evidence" class="input" :placeholder="t('balanceRefunds.evidence')" />
      <textarea v-model.trim="form.note" rows="3" class="input" :placeholder="t('balanceRefunds.note')"></textarea>
      <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300"><input v-model="acknowledged" type="checkbox" class="mt-0.5 rounded border-gray-300" /><span>{{ t('balanceRefunds.reconcileConfirmation') }}</span></label>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="loading" @click="$emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="loading || !valid" @click="submit">{{ t('common.confirm') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { AdminAccountRefundDetail, AdminAccountRefundReconcileInput } from '@/types/accountRefund'

const props = defineProps<{ show: boolean; loading: boolean; detail: AdminAccountRefundDetail | null }>()
const emit = defineEmits<{ close: []; confirm: [input: AdminAccountRefundReconcileInput] }>()
const { t } = useI18n()
const acknowledged = ref(false)
const form = reactive({ order_id: 0, outcome: 'succeeded' as 'succeeded' | 'failed', external_refund_id: '', evidence: '', note: '' })
const pendingRoutes = computed(() => props.detail?.quote?.orders.filter(route => !['success', 'refunded'].includes(route.gateway_status || '')) || [])
const valid = computed(() => acknowledged.value && form.order_id > 0 && form.evidence.length > 0 && form.note.length > 0 && (form.outcome === 'failed' || form.external_refund_id.length > 0))

watch(() => props.show, show => {
  if (!show) return
  form.order_id = pendingRoutes.value[0]?.order_id || 0
  form.outcome = 'succeeded'
  form.external_refund_id = ''
  form.evidence = ''
  form.note = ''
  acknowledged.value = false
})

function submit() {
  if (!valid.value || !props.detail) return
  emit('confirm', { ...form, verified_at: new Date().toISOString(), expected_state_revision: props.detail.item.state_revision })
}
function formatMoney(currency: string, amount: number): string { return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount) }
</script>
