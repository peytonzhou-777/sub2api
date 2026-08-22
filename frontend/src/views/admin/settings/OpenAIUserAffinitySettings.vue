<template>
  <section class="card">
    <header class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div>
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.openAIUserAffinity.title') }}</h2>
        <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.openAIUserAffinity.description') }}
        </p>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.openAIUserAffinity.state') }}: {{ effectiveStateLabel }}
        </p>
      </div>
      <Toggle v-if="config" v-model="config.enabled" />
    </header>

    <div v-if="loading" class="flex items-center justify-center py-10">
      <span class="h-6 w-6 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></span>
    </div>

    <div v-else-if="config" class="p-6">
      <div v-if="config.enabled" data-test="affinity-details" class="space-y-6">
        <div class="grid gap-5 md:grid-cols-2 xl:grid-cols-3">
          <label class="flex min-w-0 flex-col text-sm">
            <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.mode') }}</span>
            <select v-model="config.mode" class="input mt-2 w-full">
              <option value="enforce">{{ t('admin.settings.openAIUserAffinity.modes.enforce') }}</option>
              <option value="shadow">{{ t('admin.settings.openAIUserAffinity.modes.shadow') }}</option>
            </select>
            <span class="mt-1.5 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.settings.openAIUserAffinity.modeHint') }}</span>
          </label>
          <label class="flex min-w-0 flex-col text-sm">
            <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.bestFitStrategy') }}</span>
            <select v-model="config.best_fit_strategy" class="input mt-2 w-full">
              <option value="7d_then_5h">{{ t('admin.settings.openAIUserAffinity.bestFitStrategies.sevenDayThenFiveHour') }}</option>
              <option value="5h_then_7d">{{ t('admin.settings.openAIUserAffinity.bestFitStrategies.fiveHourThenSevenDay') }}</option>
            </select>
            <span class="mt-1.5 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.settings.openAIUserAffinity.bestFitStrategyHint') }}</span>
          </label>
          <label class="flex min-w-0 flex-col text-sm">
            <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.touchSuccessMode') }}</span>
            <select v-model="config.touch_success_mode" class="input mt-2 w-full">
              <option value="upstream_accepted">{{ t('admin.settings.openAIUserAffinity.touchSuccessModes.upstreamAccepted') }}</option>
              <option value="response_completed">{{ t('admin.settings.openAIUserAffinity.touchSuccessModes.responseCompleted') }}</option>
            </select>
            <span class="mt-1.5 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.settings.openAIUserAffinity.touchSuccessModeHint') }}</span>
          </label>
        </div>

        <div class="grid gap-5 md:grid-cols-2 xl:grid-cols-3">
          <NumberField v-model="config.resident_account_slot_count" :label="t('admin.settings.openAIUserAffinity.residentSlotCount')" :hint="t('admin.settings.openAIUserAffinity.residentSlotCountHint')" :min="1" :max="5" />
          <NumberField v-model="config.resident_ttl_seconds" :label="t('admin.settings.openAIUserAffinity.residentTTL')" :hint="t('admin.settings.openAIUserAffinity.residentTTLHint')" :min="86400" :max="2592000" />
          <NumberField v-model="config.conversation_active_ttl_seconds" :label="t('admin.settings.openAIUserAffinity.conversationActiveTTL')" :hint="t('admin.settings.openAIUserAffinity.conversationActiveTTLHint')" :min="300" :max="86400" />
          <NumberField v-model="config.default_max_contact_users" :label="t('admin.settings.openAIUserAffinity.maxContactUsers')" :hint="t('admin.settings.openAIUserAffinity.maxContactUsersHint')" :min="1" :max="10000" />
          <NumberField v-model="config.default_new_resident_cooldown_seconds" :label="t('admin.settings.openAIUserAffinity.cooldownSeconds')" :hint="t('admin.settings.openAIUserAffinity.cooldownSecondsHint')" :min="1" :max="86400" />
          <NumberField v-model="config.capacity_failure_migration_threshold" :label="t('admin.settings.openAIUserAffinity.failureThreshold')" :hint="t('admin.settings.openAIUserAffinity.failureThresholdHint')" :min="2" :max="100" />
          <NumberField v-model="config.capacity_failure_window_seconds" :label="t('admin.settings.openAIUserAffinity.failureWindow')" :hint="t('admin.settings.openAIUserAffinity.failureWindowHint')" :min="10" :max="3600" />
          <NumberField v-model="config.migration_stability_seconds" :label="t('admin.settings.openAIUserAffinity.stabilitySeconds')" :hint="t('admin.settings.openAIUserAffinity.stabilitySecondsHint')" :min="0" :max="3600" />
          <NumberField v-model="config.follower_jitter_min_ms" :label="t('admin.settings.openAIUserAffinity.jitterMin')" :hint="t('admin.settings.openAIUserAffinity.jitterMinHint')" :min="0" :max="10000" />
          <NumberField v-model="config.follower_jitter_max_ms" :label="t('admin.settings.openAIUserAffinity.jitterMax')" :hint="t('admin.settings.openAIUserAffinity.jitterMaxHint')" :min="0" :max="10000" />
          <NumberField v-model="config.cold_start_demand_quantile" :label="t('admin.settings.openAIUserAffinity.demandQuantile')" :hint="t('admin.settings.openAIUserAffinity.demandQuantileHint')" :min="0.5" :max="0.99" :step="0.01" />
          <NumberField v-model="config.quota_reserve_ratio_5h" :label="t('admin.settings.openAIUserAffinity.reserve5h')" :hint="t('admin.settings.openAIUserAffinity.reserve5hHint')" :min="0" :max="0.9" :step="0.01" />
          <NumberField v-model="config.quota_reserve_ratio_7d" :label="t('admin.settings.openAIUserAffinity.reserve7d')" :hint="t('admin.settings.openAIUserAffinity.reserve7dHint')" :min="0" :max="0.9" :step="0.01" />
          <NumberField v-model="config.best_fit_close_tolerance_ratio" :label="t('admin.settings.openAIUserAffinity.closeTolerance')" :hint="t('admin.settings.openAIUserAffinity.closeToleranceHint')" :min="0" :max="0.2" :step="0.01" />
        </div>

        <div class="grid gap-4 md:grid-cols-2">
          <label class="flex items-start justify-between gap-4 rounded border border-gray-200 px-4 py-3 dark:border-dark-700">
            <span class="min-w-0">
              <span class="block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.reentryOvercommit') }}</span>
              <span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.settings.openAIUserAffinity.reentryOvercommitHint') }}</span>
            </span>
            <Toggle v-model="config.resident_reentry_overcommit_enabled" class="mt-0.5 shrink-0" />
          </label>
          <label class="flex items-start justify-between gap-4 rounded border border-gray-200 px-4 py-3 dark:border-dark-700">
            <span class="min-w-0">
              <span class="block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.resetExcludeSource') }}</span>
              <span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.settings.openAIUserAffinity.resetExcludeSourceHint') }}</span>
            </span>
            <Toggle v-model="config.manual_reset_exclude_source_account" class="mt-0.5 shrink-0" />
          </label>
        </div>
      </div>

      <div
        class="flex justify-end"
        :class="config.enabled ? 'mt-6 border-t border-gray-100 pt-5 dark:border-dark-700' : ''"
      >
        <button type="button" class="btn btn-primary" :disabled="saving" @click="save">
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import type { OpenAIUserAffinityConfig } from '@/api/admin/settings'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const saving = ref(false)
const effectiveState = ref<'disabled' | 'shadow' | 'enforce'>('disabled')
const config = ref<OpenAIUserAffinityConfig | null>(null)

const effectiveStateLabel = computed(() => t(`admin.settings.openAIUserAffinity.states.${effectiveState.value}`))

const NumberField = defineComponent({
  props: {
    modelValue: { type: Number, required: true },
    label: { type: String, required: true },
    hint: { type: String, required: true },
    min: { type: Number, required: true },
    max: { type: Number, required: true },
    step: { type: Number, default: 1 }
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () => h('label', { class: 'flex min-w-0 flex-col text-sm' }, [
      h('span', { class: 'font-medium text-gray-700 dark:text-gray-300' }, props.label),
      h('input', {
        class: 'input mt-2 w-full', type: 'number', value: props.modelValue,
        min: props.min, max: props.max, step: props.step,
        onInput: (event: Event) => emit('update:modelValue', Number((event.target as HTMLInputElement).value))
      }),
      h('span', { class: 'mt-1.5 text-xs leading-5 text-gray-500 dark:text-gray-400' }, props.hint)
    ])
  }
})

async function load() {
  loading.value = true
  try {
    const result = await adminAPI.settings.getOpenAIUserAffinityScheduling()
    config.value = result.config
    effectiveState.value = result.effective_state
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!config.value) return
  saving.value = true
  try {
    const result = await adminAPI.settings.updateOpenAIUserAffinityScheduling(config.value)
    config.value = result.config
    effectiveState.value = result.effective_state
    appStore.showSuccess(t('common.saved'))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
    await load()
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
