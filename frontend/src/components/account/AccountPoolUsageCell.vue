<template>
  <div v-if="windows.length" class="space-y-1" :class="{ 'opacity-70': hasStaleWindow }">
    <template v-for="window in windows" :key="window.code">
      <UsageProgressBar
        v-if="window.used_percent != null"
        :label="window.label"
        :utilization="window.used_percent"
        :resets-at="window.resets_at"
        :show-now-when-idle="window.used_percent <= 0 && window.state === 'fresh'"
        :color="windowColor(window.code)"
      />
      <div v-else class="flex items-center gap-1 text-[10px] text-gray-400 dark:text-gray-500">
        <span class="w-[32px] shrink-0 rounded bg-gray-100 px-1 py-0.5 text-center dark:bg-gray-800">{{ window.label }}</span>
        <span>--</span>
      </div>
    </template>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-gray-500">--</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import UsageProgressBar from './UsageProgressBar.vue'
import type { AccountPoolAccount } from '@/api/accountPool'

const props = defineProps<{ windows: AccountPoolAccount['usage_windows'] }>()
const windows = computed(() => props.windows || [])
const hasStaleWindow = computed(() => windows.value.some(window => window.state === 'stale'))

function windowColor(code: string): 'indigo' | 'emerald' | 'purple' | 'amber' {
  if (code === '7d') return 'emerald'
  if (code === '7d_oi') return 'amber'
  if (code === '7d_sonnet') return 'purple'
  return 'indigo'
}
</script>
