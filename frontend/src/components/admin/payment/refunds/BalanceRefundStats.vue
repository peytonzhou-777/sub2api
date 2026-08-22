<template>
  <section class="grid grid-cols-2 gap-3 lg:grid-cols-4" aria-label="余额清退摘要">
    <div v-for="stat in stats" :key="stat.key" class="border-b border-gray-200 px-1 py-3 dark:border-dark-600">
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ stat.label }}</p>
      <p class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ stat.value }}</p>
      <div v-if="stat.amounts.length" class="mt-2 space-y-0.5 text-xs text-gray-500 dark:text-gray-400">
        <p v-for="entry in stat.amounts" :key="entry">{{ entry }}</p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AdminAccountRefundSummary } from '@/types/accountRefund'

const props = defineProps<{ summary: AdminAccountRefundSummary | null }>()
const { t } = useI18n()

function moneyLines(amounts?: Record<string, number>): string[] {
  if (!amounts) return []
  return Object.entries(amounts)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([currency, amount]) => new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount))
}

const stats = computed(() => [
  { key: 'refundable', label: t('balanceRefunds.stats.refundable'), value: props.summary?.refundable_users ?? '--', amounts: moneyLines(props.summary?.refundable_totals) },
  { key: 'automatic', label: t('balanceRefunds.stats.automatic'), value: props.summary?.automatic_users ?? '--', amounts: moneyLines(props.summary?.automatic_totals) },
  { key: 'processing', label: t('balanceRefunds.stats.processing'), value: props.summary?.processing_users ?? '--', amounts: [] },
  { key: 'review', label: t('balanceRefunds.stats.review'), value: props.summary?.manual_review_users ?? '--', amounts: moneyLines(props.summary?.manual_external_totals) },
])
</script>
