<template>
  <BaseDialog :show="show" :title="t('balanceRefunds.detailTitle')" width="wide" @close="$emit('close')">
    <div v-if="loading" class="flex h-48 items-center justify-center"><LoadingSpinner /></div>
    <div v-else-if="detail" class="space-y-5">
      <section class="grid grid-cols-2 gap-x-6 gap-y-3 border-y border-gray-200 py-4 text-sm dark:border-dark-600 md:grid-cols-4">
        <div><p class="text-xs text-gray-500">{{ t('balanceRefunds.table.user') }}</p><p class="truncate font-medium text-gray-900 dark:text-white">{{ detail.item.username || `#${detail.item.user_id}` }}</p></div>
        <div><p class="text-xs text-gray-500">{{ t('balanceRefunds.table.status') }}</p><p>{{ t(`balanceRefunds.states.${detail.item.flow_state}`, detail.item.flow_state) }}</p></div>
        <div><p class="text-xs text-gray-500">{{ t('balanceRefunds.permanent') }}</p><p>{{ detail.item.permanent_balance.toFixed(2) }}</p></div>
        <div><p class="text-xs text-gray-500">{{ t('balanceRefunds.bonus') }}</p><p>{{ detail.item.recharge_bonus_balance.toFixed(2) }}</p></div>
      </section>

      <section v-if="detail.quote">
        <h3 class="mb-2 text-sm font-semibold text-gray-900 dark:text-white">{{ t('balanceRefunds.routes') }}</h3>
        <div class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-600">
            <thead><tr class="text-left text-xs text-gray-500"><th class="py-2 pr-3">{{ t('balanceRefunds.order') }}</th><th class="px-3 py-2">{{ t('balanceRefunds.rate') }}</th><th class="px-3 py-2">{{ t('balanceRefunds.routeRefund') }}</th><th class="px-3 py-2">{{ t('balanceRefunds.gatewayStatus') }}</th></tr></thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="route in detail.quote.orders" :key="route.order_id"><td class="py-2 pr-3 font-mono">#{{ route.order_id }}</td><td class="px-3 py-2">{{ route.bonus_rate ? `${(route.bonus_rate * 100).toFixed(0)}%` : '--' }}</td><td class="px-3 py-2">{{ formatMoney(route.currency, route.gateway_refund) }}</td><td class="px-3 py-2">{{ route.gateway_status || '--' }}<p v-if="route.gateway_error" class="max-w-64 truncate text-xs text-red-500">{{ route.gateway_error }}</p></td></tr>
            </tbody>
          </table>
        </div>
      </section>

      <section v-if="detail.timeline.length">
        <h3 class="mb-2 text-sm font-semibold text-gray-900 dark:text-white">{{ t('balanceRefunds.timeline') }}</h3>
        <ol class="space-y-2 border-l border-gray-200 pl-4 dark:border-dark-600">
          <li v-for="event in detail.timeline" :key="event.state_revision" class="text-sm">
            <div class="flex flex-wrap items-center gap-2"><span class="font-medium text-gray-900 dark:text-white">{{ t(`balanceRefunds.states.${event.state}`, event.state) }}</span><span class="text-xs text-gray-500">{{ formatDate(event.created_at) }}</span><span v-if="event.actor?.actor_label" class="text-xs text-gray-500">{{ event.actor.actor_label }}</span></div>
            <p v-if="event.message" class="mt-0.5 text-xs text-gray-500">{{ event.message }}</p>
            <div v-if="event.reconciliation" class="mt-1 border-l-2 border-gray-200 pl-2 text-xs text-gray-500 dark:border-dark-600">
              <p>#{{ event.reconciliation.order_id }} · {{ t(`balanceRefunds.reconcileOutcomes.${event.reconciliation.outcome}`) }} · {{ formatDate(event.reconciliation.verified_at) }}</p>
              <p>{{ event.reconciliation.evidence }}<span v-if="event.reconciliation.external_refund_id"> · {{ event.reconciliation.external_refund_id }}</span></p>
              <p>{{ event.reconciliation.note }}</p>
            </div>
          </li>
        </ol>
      </section>
    </div>
    <template #footer>
      <div class="flex w-full flex-wrap justify-end gap-2">
        <button type="button" class="btn btn-secondary" @click="$emit('close')">{{ t('common.close') }}</button>
        <button v-for="action in detail?.item.available_actions || []" :key="action" type="button" class="btn btn-primary" @click="$emit('action', detail!.item, action)">{{ t(`balanceRefunds.actions.${action}`) }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import type { AccountRefundAction, AdminAccountRefundDetail, AdminAccountRefundListItem } from '@/types/accountRefund'

defineProps<{ show: boolean; loading: boolean; detail: AdminAccountRefundDetail | null }>()
defineEmits<{ close: []; action: [item: AdminAccountRefundListItem, action: AccountRefundAction] }>()
const { t } = useI18n()

function formatMoney(currency: string, amount: number): string {
  return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount)
}
function formatDate(value: string): string { return new Date(value).toLocaleString() }
</script>
