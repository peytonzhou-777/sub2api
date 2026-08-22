<template>
  <BaseDialog :show="show" :title="t(`balanceRefunds.actionTitles.${action}`)" @close="$emit('close')">
    <div v-if="item" class="space-y-4">
      <div class="border-y border-gray-200 py-3 text-sm dark:border-dark-600">
        <p class="font-medium text-gray-900 dark:text-white">{{ item.username || `#${item.user_id}` }}</p>
        <p class="text-gray-500">{{ item.email }}</p>
        <p v-for="line in moneyLines(item.refund_totals)" :key="line" class="mt-1 font-semibold text-gray-900 dark:text-white">{{ line }}</p>
        <p class="mt-2 text-xs text-gray-500">{{ t('balanceRefunds.clearAmount', { amount: item.other_limited_to_clear.toFixed(2) }) }}</p>
      </div>
      <label class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300">
        <input v-model="acknowledged" type="checkbox" class="mt-0.5 rounded border-gray-300" />
        <span>{{ t(`balanceRefunds.confirmations.${action}`) }}</span>
      </label>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="loading" @click="$emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="loading || !acknowledged" @click="$emit('confirm')">{{ t('common.confirm') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { AccountRefundAction, AdminAccountRefundListItem } from '@/types/accountRefund'

const props = defineProps<{ show: boolean; item: AdminAccountRefundListItem | null; action: AccountRefundAction; loading: boolean }>()
defineEmits<{ close: []; confirm: [] }>()
const { t } = useI18n()
const acknowledged = ref(false)
watch(() => [props.show, props.action], () => { acknowledged.value = false })

function moneyLines(amounts: Record<string, number>): string[] {
  return Object.entries(amounts).sort(([a], [b]) => a.localeCompare(b)).map(([currency, amount]) => new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount))
}
</script>
