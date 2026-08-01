<template>
  <div v-if="supported" class="space-y-1">
    <div v-if="data" class="flex items-start gap-2" :title="observedAtTitle">
      <div class="min-w-0 flex-1 space-y-1">
        <div v-for="window in data.windows" :key="window.code" class="space-y-0.5">
          <div class="flex flex-wrap items-center gap-1 text-[10px] text-gray-500 dark:text-gray-400">
            <span class="w-7 shrink-0 rounded bg-gray-100 px-1 py-0.5 text-center dark:bg-dark-800">{{ window.label }}</span>
            <span class="rounded bg-gray-100 px-1 py-0.5 dark:bg-dark-800">{{ t('accountPool.personalUsage.req') }} {{ formatCompactNumber(window.requests, { allowBillions: false }) }}</span>
            <span class="rounded bg-gray-100 px-1 py-0.5 dark:bg-dark-800">{{ t('accountPool.personalUsage.token') }} {{ formatCompactNumber(window.tokens) }}</span>
            <span class="rounded bg-gray-100 px-1 py-0.5 dark:bg-dark-800">{{ t('accountPool.personalUsage.actualCost') }} {{ formatCurrency(window.actual_cost) }}</span>
          </div>
        </div>
      </div>
      <button
        type="button"
        class="shrink-0 text-gray-500 hover:text-gray-700 disabled:cursor-not-allowed disabled:opacity-50 dark:text-gray-400 dark:hover:text-gray-200"
        :disabled="loading || isCoolingDown"
        :title="t('accountPool.personalUsage.query')"
        :aria-label="t('accountPool.personalUsage.query')"
        @click="query"
      >
        <Icon name="refresh" size="xs" :class="{ 'animate-spin': loading }" />
      </button>
    </div>
    <div v-else-if="error" class="flex items-center gap-2 text-xs text-red-500">
      <span>{{ error }}</span>
      <button
        type="button"
        class="text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200"
        :disabled="isCoolingDown"
        :title="t('accountPool.personalUsage.query')"
        :aria-label="t('accountPool.personalUsage.query')"
        @click="query"
      >
        <Icon name="refresh" size="xs" />
      </button>
    </div>
    <div v-else class="flex items-center gap-2">
      <span class="text-xs text-gray-400 dark:text-gray-500">--</span>
      <button
        type="button"
        class="inline-flex items-center gap-1 text-xs text-primary-600 hover:text-primary-700 disabled:cursor-not-allowed disabled:opacity-50 dark:text-primary-400 dark:hover:text-primary-300"
        :disabled="loading || isCoolingDown"
        :title="t('accountPool.personalUsage.query')"
        @click="query"
      >
        <Icon name="search" size="xs" :class="{ 'animate-spin': loading }" />
        <span>{{ loading ? t('accountPool.personalUsage.loading') : t('accountPool.personalUsage.query') }}</span>
      </button>
    </div>
    <p v-if="data" class="text-[10px] text-gray-400 dark:text-gray-500">{{ t('accountPool.personalUsage.observedAt') }}: {{ observedAtLabel }}</p>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-gray-500">--</span>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import accountPoolAPI, { type AccountPoolAccount, type AccountPoolPersonalUsage } from '@/api/accountPool'
import { formatCompactNumber, formatCurrency } from '@/utils/format'
import { useI18n } from 'vue-i18n'

const personalUsageCache = new Map<number, { data: AccountPoolPersonalUsage | null; loadedAt: number; retryUntil: number }>()
const cacheTTL = 30_000

const props = defineProps<{ account: AccountPoolAccount }>()
const { t } = useI18n()
const supported = computed(() =>
  (props.account.platform === 'openai' || props.account.platform === 'anthropic') &&
  (props.account.type === 'oauth' || props.account.type === 'setup-token'),
)
const cached = personalUsageCache.get(props.account.id)
const data = ref<AccountPoolPersonalUsage | null>(cached && Date.now() - cached.loadedAt < cacheTTL ? cached.data : null)
const loading = ref(false)
const error = ref('')
const retryUntil = ref(cached?.retryUntil ?? 0)
const clock = ref(Date.now())
let retryTimer: ReturnType<typeof setTimeout> | undefined
let controller: AbortController | undefined

const isCoolingDown = computed(() => retryUntil.value > clock.value)
const observedAtLabel = computed(() => {
  if (!data.value) return ''
  const observedAt = new Date(data.value.observed_at)
  return Number.isNaN(observedAt.getTime()) ? '--' : new Intl.DateTimeFormat(undefined, { dateStyle: 'short', timeStyle: 'short' }).format(observedAt)
})
const observedAtTitle = computed(() => data.value?.observed_at ? new Date(data.value.observed_at).toLocaleString() : '')

function armRetryTimer() {
  const remaining = retryUntil.value - Date.now()
  if (remaining <= 0) {
    clock.value = Date.now()
    return
  }
  if (retryTimer) clearTimeout(retryTimer)
  retryTimer = setTimeout(() => {
    retryTimer = undefined
    clock.value = Date.now()
  }, Math.min(remaining, 2_147_483_647))
}

function scheduleRetry(seconds?: number) {
  const delay = Math.min(Math.max(1, seconds ?? 30) * 1000, 2_147_483_647)
  const until = Date.now() + delay
  retryUntil.value = until
  personalUsageCache.set(props.account.id, { data: data.value, loadedAt: Date.now(), retryUntil: until })
  armRetryTimer()
}

async function query() {
  if (!supported.value || loading.value || isCoolingDown.value) return
  const cachedValue = personalUsageCache.get(props.account.id)
  if (cachedValue && Date.now() - cachedValue.loadedAt < cacheTTL && cachedValue.data) {
    data.value = cachedValue.data
    error.value = ''
    return
  }
  controller?.abort()
  controller = new AbortController()
  loading.value = true
  error.value = ''
  try {
    const result = await accountPoolAPI.getPersonalUsage(props.account.id, { signal: controller.signal })
    data.value = result
    personalUsageCache.set(props.account.id, { data: result, loadedAt: Date.now(), retryUntil: 0 })
  } catch (cause: unknown) {
    const apiError = cause as { code?: string; status?: number; retryAfter?: number; message?: string }
    if (apiError.code === 'ERR_CANCELED') return
    if (apiError.status === 429) {
      scheduleRetry(apiError.retryAfter)
      error.value = t('accountPool.personalUsage.unavailable')
    } else {
      error.value = apiError.message || t('accountPool.personalUsage.unavailable')
    }
  } finally {
    loading.value = false
  }
}

onBeforeUnmount(() => {
  controller?.abort()
  if (retryTimer) clearTimeout(retryTimer)
})

armRetryTimer()
</script>
