<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.routeDetectionTitle')"
    width="wide"
    @close="handleClose"
  >
    <div class="space-y-5">
      <div v-if="account" class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-200 pb-4 dark:border-dark-500">
        <div class="min-w-0">
          <div class="truncate font-semibold text-gray-900 dark:text-gray-100">{{ account.name }}</div>
          <div class="mt-1 text-xs text-gray-500 dark:text-gray-400">#{{ account.id }} · OpenAI OAuth</div>
        </div>
        <span :class="['inline-flex min-h-8 items-center gap-2 rounded px-3 py-1.5 text-sm font-semibold', statusMeta.className]">
          <Icon v-if="running" name="refresh" size="sm" class="animate-spin" :stroke-width="2" />
          <Icon v-else :name="statusMeta.icon" size="sm" :stroke-width="2" />
          {{ statusMeta.label }}
        </span>
      </div>

      <div v-if="errorMessage" class="border-l-2 border-red-500 bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-500/10 dark:text-red-300">
        {{ errorMessage }}
      </div>

      <template v-if="result">
        <dl class="grid grid-cols-1 gap-x-8 gap-y-3 text-sm sm:grid-cols-2">
          <div>
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.routeDetectionRequestedModel') }}</dt>
            <dd class="mt-1 break-all font-mono text-gray-900 dark:text-gray-100">{{ result.requested_model }}</dd>
          </div>
          <div>
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.routeDetectionReportedModel') }}</dt>
            <dd class="mt-1 break-all font-mono text-gray-900 dark:text-gray-100">{{ result.reported_model || t('admin.accounts.routeDetectionNotReturned') }}</dd>
          </div>
          <div>
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.routeDetectionCheckedAt') }}</dt>
            <dd class="mt-1 text-gray-900 dark:text-gray-100">{{ formatCheckedAt(result.checked_at) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.routeDetectionCredentialAccount') }}</dt>
            <dd class="mt-1 font-mono text-gray-900 dark:text-gray-100">#{{ result.credential_account_id }}</dd>
          </div>
          <div class="sm:col-span-2">
            <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.routeDetectionReasonCode') }}</dt>
            <dd class="mt-1 font-mono text-gray-900 dark:text-gray-100">{{ result.reason_code }}</dd>
          </div>
        </dl>

        <section class="border-t border-gray-200 pt-4 dark:border-dark-500">
          <h4 class="mb-2 text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.accounts.routeDetectionResponseHeaders') }}</h4>
          <pre data-testid="route-detection-headers" class="overflow-x-auto rounded bg-gray-950 p-4 text-xs leading-6 text-gray-200"><code v-for="name in responseHeaderNames" :key="name" class="block whitespace-pre-wrap break-all"><span class="text-cyan-300">{{ name }}</span>: {{ result.response_headers?.[name] || t('admin.accounts.routeDetectionNotReturned') }}</code></pre>
        </section>
      </template>
    </div>

    <template #footer>
      <div class="flex w-full justify-end gap-2">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.close') }}
        </button>
        <button type="button" class="btn btn-primary inline-flex items-center gap-2" :disabled="running || !account" @click="runDetection">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': running }" :stroke-width="2" />
          {{ running ? t('admin.accounts.routeDetectionRunning') : t('admin.accounts.routeDetectionRunAgain') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account, CodexRouteDetectionResult, CodexRouteDetectionSnapshot } from '@/types'

const props = defineProps<{
  show: boolean
  account: Account | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'account-updated', account: Account): void
}>()

const { t } = useI18n()
const running = ref(false)
const result = ref<CodexRouteDetectionSnapshot | CodexRouteDetectionResult | null>(null)
const errorMessage = ref('')
let abortController: AbortController | null = null
let runID = 0

const responseHeaderNames = [
  'x-codex-primary-used-percent',
  'x-codex-primary-window-minutes',
  'x-codex-active-limit',
  'x-codex-safety-buffering-faster-model'
] as const

const statusMeta = computed(() => {
  if (running.value) {
    return {
      label: t('admin.accounts.routeDetectionRunning'),
      className: 'bg-amber-100 text-amber-800 dark:bg-amber-500/20 dark:text-amber-300',
      icon: 'refresh' as const
    }
  }
  switch (result.value?.status) {
    case 'sol':
      return { label: t('admin.accounts.routeDetectionStatusSol'), className: 'bg-green-100 text-green-700 dark:bg-green-500/20 dark:text-green-300', icon: 'checkCircle' as const }
    case 'luna':
      return { label: t('admin.accounts.routeDetectionStatusLuna'), className: 'bg-red-100 text-red-700 dark:bg-red-500/20 dark:text-red-300', icon: 'exclamationTriangle' as const }
    case 'inconclusive':
      return { label: t('admin.accounts.routeDetectionStatusInconclusive'), className: 'bg-gray-100 text-gray-700 dark:bg-dark-600 dark:text-gray-300', icon: 'questionCircle' as const }
    case 'error':
      return { label: t('admin.accounts.routeDetectionStatusError'), className: 'bg-red-100 text-red-700 dark:bg-red-500/20 dark:text-red-300', icon: 'xCircle' as const }
    default:
      return { label: t('admin.accounts.routeDetectionStatusNotChecked'), className: 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-400', icon: 'brain' as const }
  }
})

const formatCheckedAt = (value: string) => {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

const runDetection = async () => {
  const account = props.account
  if (!account || running.value) return

  abortController?.abort()
  abortController = new AbortController()
  const currentRunID = ++runID
  running.value = true
  errorMessage.value = ''
  result.value = null
  try {
    const detected = await adminAPI.accounts.detectCodexRoute(account.id, abortController.signal)
    if (currentRunID !== runID) return
    result.value = detected
    emit('account-updated', {
      ...account,
      extra: {
        ...(account.extra || {}),
        codex_route_detection: detected
      }
    })
  } catch (error) {
    if (currentRunID !== runID || abortController?.signal.aborted) return
    errorMessage.value = extractApiErrorMessage(error, t('admin.accounts.routeDetectionRequestFailed'))
  } finally {
    if (currentRunID === runID) running.value = false
  }
}

const stopDetection = () => {
  runID += 1
  abortController?.abort()
  abortController = null
  running.value = false
}

const handleClose = () => {
  stopDetection()
  emit('close')
}

watch(
  [() => props.show, () => props.account?.id],
  ([show]) => {
    stopDetection()
    errorMessage.value = ''
    result.value = show ? props.account?.extra?.codex_route_detection || null : null
    if (show && props.account) void runDetection()
  },
  { immediate: true }
)

onUnmounted(stopDetection)
</script>
