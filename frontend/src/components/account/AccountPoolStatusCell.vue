<template>
  <div class="flex flex-col items-start gap-1">
    <span :class="['badge text-xs', badgeClass]">{{ statusText }}</span>
    <span v-if="status.resume_at && countdown" class="text-[11px] text-gray-400 dark:text-gray-500">{{ countdown }}</span>
    <div v-if="status.models.length" class="flex flex-wrap gap-1">
      <span v-for="model in status.models" :key="`${model.kind}-${model.model}`" class="rounded bg-purple-100 px-1.5 py-0.5 text-[10px] text-purple-700 dark:bg-purple-900/30 dark:text-purple-400">
        {{ model.model }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatCountdown } from '@/utils/format'
import type { AccountPoolAccount } from '@/api/accountPool'

const props = defineProps<{ status: AccountPoolAccount['status'] }>()
const { t } = useI18n()

const statusText = computed(() => t(`accountPool.status.${props.status.code}`, props.status.code))
const countdown = computed(() => formatCountdown(props.status.resume_at))
const badgeClass = computed(() => {
  switch (props.status.code) {
    case 'active': return 'badge-success'
    case 'error':
    case 'overloaded': return 'badge-danger'
    case 'rate_limited':
    case 'temporarily_unavailable':
    case 'quota_exceeded': return 'badge-warning'
    default: return 'badge-gray'
  }
})
</script>
