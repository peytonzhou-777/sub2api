<template>
  <div class="overflow-x-auto border-y border-gray-200 dark:border-dark-600">
    <table class="min-w-full table-fixed divide-y divide-gray-200 dark:divide-dark-600">
      <thead class="bg-gray-50 dark:bg-dark-800">
        <tr>
          <th class="w-64 px-4 py-3 text-left text-xs font-medium text-gray-500">{{ t('balanceRefunds.table.user') }}</th>
          <th class="w-44 px-4 py-3 text-left text-xs font-medium text-gray-500">{{ t('balanceRefunds.table.balance') }}</th>
          <th class="w-44 px-4 py-3 text-left text-xs font-medium text-gray-500">{{ t('balanceRefunds.table.refund') }}</th>
          <th class="w-40 px-4 py-3 text-left text-xs font-medium text-gray-500">{{ t('balanceRefunds.table.status') }}</th>
          <th class="w-52 px-4 py-3 text-right text-xs font-medium text-gray-500">{{ t('common.actions') }}</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-900">
        <tr v-if="loading"><td colspan="5" class="h-32 text-center text-sm text-gray-500">{{ t('common.loading') }}</td></tr>
        <tr v-else-if="items.length === 0"><td colspan="5" class="h-32 text-center text-sm text-gray-500">{{ t('balanceRefunds.empty') }}</td></tr>
        <tr v-for="item in items" v-else :key="item.user_id" class="hover:bg-gray-50 dark:hover:bg-dark-800">
          <td class="px-4 py-3">
            <button class="max-w-full text-left" type="button" @click="$emit('select', item)">
              <span class="block truncate text-sm font-medium text-gray-900 dark:text-white">{{ item.username || `#${item.user_id}` }}</span>
              <span class="block truncate text-xs text-gray-500">{{ item.email }}</span>
            </button>
          </td>
          <td class="px-4 py-3 text-sm text-gray-700 dark:text-gray-300">
            <p>{{ item.permanent_balance.toFixed(2) }}</p>
            <p v-if="item.recharge_bonus_balance" class="text-xs text-gray-500">+ {{ item.recharge_bonus_balance.toFixed(2) }}</p>
          </td>
          <td class="px-4 py-3 text-sm font-medium text-gray-900 dark:text-white">
            <p v-for="line in moneyLines(item.refund_totals)" :key="line">{{ line }}</p>
            <span v-if="moneyLines(item.refund_totals).length === 0">--</span>
          </td>
          <td class="px-4 py-3">
            <span :class="statusClass(item.flow_state)" class="inline-flex rounded-full px-2 py-0.5 text-xs font-medium">{{ t(`balanceRefunds.states.${item.flow_state}`, item.flow_state) }}</span>
            <p v-if="item.review_reason_code" class="mt-1 truncate text-xs text-amber-600 dark:text-amber-400">{{ t(`balanceRefunds.reasons.${item.review_reason_code}`, item.review_reason_code) }}</p>
          </td>
          <td class="px-4 py-3">
            <div class="flex items-center justify-end gap-1">
              <button type="button" class="btn btn-ghost btn-sm" :title="t('common.view')" @click="$emit('select', item)"><Icon name="eye" size="sm" /></button>
              <button v-for="action in item.available_actions.slice(0, 2)" :key="action" type="button" class="btn btn-secondary btn-sm" @click="$emit('action', item, action)">{{ t(`balanceRefunds.actions.${action}`) }}</button>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { AccountRefundAction, AdminAccountRefundListItem } from '@/types/accountRefund'

defineProps<{ items: AdminAccountRefundListItem[]; loading: boolean }>()
defineEmits<{
  select: [item: AdminAccountRefundListItem]
  action: [item: AdminAccountRefundListItem, action: AccountRefundAction]
}>()
const { t } = useI18n()

function moneyLines(amounts: Record<string, number>): string[] {
  return Object.entries(amounts).sort(([a], [b]) => a.localeCompare(b)).map(([currency, amount]) => new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount))
}

function statusClass(state: string): string {
  if (state === 'succeeded' || state === 'donated') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  if (state === 'manual_review' || state === 'failed') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (state === 'canceled') return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
}
</script>
