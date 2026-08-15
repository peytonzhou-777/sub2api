<template>
  <section class="card">
    <header class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div>
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.openAIUserAffinity.title') }}</h2>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.openAIUserAffinity.state') }}: {{ effectiveStateLabel }}
        </p>
      </div>
      <Toggle v-if="config" v-model="config.enabled" />
    </header>

    <div v-if="loading" class="flex items-center justify-center py-10">
      <span class="h-6 w-6 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></span>
    </div>

    <div v-else-if="config" class="space-y-6 p-6">
      <div class="grid gap-4 md:grid-cols-3">
        <label class="space-y-1 text-sm">
          <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.mode') }}</span>
          <select v-model="config.mode" class="input w-full">
            <option value="enforce">Enforce</option>
            <option value="shadow">Shadow</option>
          </select>
        </label>
        <label class="space-y-1 text-sm">
          <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.bestFitStrategy') }}</span>
          <select v-model="config.best_fit_strategy" class="input w-full">
            <option value="7d_then_5h">7d → 5h</option>
            <option value="5h_then_7d">5h → 7d</option>
          </select>
        </label>
        <label class="space-y-1 text-sm">
          <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.touchSuccessMode') }}</span>
          <select v-model="config.touch_success_mode" class="input w-full">
            <option value="upstream_accepted">upstream_accepted</option>
            <option value="response_completed">response_completed</option>
          </select>
        </label>
      </div>

      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <NumberField v-model="config.default_max_contact_users" :label="t('admin.settings.openAIUserAffinity.maxContactUsers')" :min="1" :max="10000" />
        <NumberField v-model="config.default_new_resident_cooldown_seconds" :label="t('admin.settings.openAIUserAffinity.cooldownSeconds')" :min="1" :max="86400" />
        <NumberField v-model="config.capacity_failure_migration_threshold" :label="t('admin.settings.openAIUserAffinity.failureThreshold')" :min="2" :max="100" />
        <NumberField v-model="config.capacity_failure_window_seconds" :label="t('admin.settings.openAIUserAffinity.failureWindow')" :min="10" :max="3600" />
        <NumberField v-model="config.migration_stability_seconds" :label="t('admin.settings.openAIUserAffinity.stabilitySeconds')" :min="0" :max="3600" />
        <NumberField v-model="config.follower_jitter_min_ms" :label="t('admin.settings.openAIUserAffinity.jitterMin')" :min="0" :max="10000" />
        <NumberField v-model="config.follower_jitter_max_ms" :label="t('admin.settings.openAIUserAffinity.jitterMax')" :min="0" :max="10000" />
        <NumberField v-model="config.cold_start_demand_quantile" :label="t('admin.settings.openAIUserAffinity.demandQuantile')" :min="0.5" :max="0.99" :step="0.01" />
        <NumberField v-model="config.quota_reserve_ratio_5h" :label="t('admin.settings.openAIUserAffinity.reserve5h')" :min="0" :max="0.9" :step="0.01" />
        <NumberField v-model="config.quota_reserve_ratio_7d" :label="t('admin.settings.openAIUserAffinity.reserve7d')" :min="0" :max="0.9" :step="0.01" />
        <NumberField v-model="config.best_fit_close_tolerance_ratio" :label="t('admin.settings.openAIUserAffinity.closeTolerance')" :min="0" :max="0.2" :step="0.01" />
      </div>

      <div class="grid gap-4 md:grid-cols-2">
        <label class="flex items-center justify-between gap-4 rounded border border-gray-200 px-4 py-3 dark:border-dark-700">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.reentryOvercommit') }}</span>
          <Toggle v-model="config.resident_reentry_overcommit_enabled" />
        </label>
        <label class="flex items-center justify-between gap-4 rounded border border-gray-200 px-4 py-3 dark:border-dark-700">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.resetExcludeSource') }}</span>
          <Toggle v-model="config.manual_reset_exclude_source_account" />
        </label>
      </div>

      <div class="flex flex-col gap-3 border-t border-gray-100 pt-5 dark:border-dark-700 sm:flex-row sm:items-end">
        <label class="flex-1 space-y-1 text-sm">
          <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIUserAffinity.changeReason') }}</span>
          <input v-model.trim="reason" class="input w-full" maxlength="200" />
        </label>
        <button type="button" class="btn btn-primary" :disabled="saving || !reason" @click="save">
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
const reason = ref('更新 OpenAI 用户粘性调度配置')

const effectiveStateLabel = computed(() => t(`admin.settings.openAIUserAffinity.states.${effectiveState.value}`))

const NumberField = defineComponent({
  props: {
    modelValue: { type: Number, required: true },
    label: { type: String, required: true },
    min: { type: Number, required: true },
    max: { type: Number, required: true },
    step: { type: Number, default: 1 }
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () => h('label', { class: 'space-y-1 text-sm' }, [
      h('span', { class: 'font-medium text-gray-700 dark:text-gray-300' }, props.label),
      h('input', {
        class: 'input w-full', type: 'number', value: props.modelValue,
        min: props.min, max: props.max, step: props.step,
        onInput: (event: Event) => emit('update:modelValue', Number((event.target as HTMLInputElement).value))
      })
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
  if (!config.value || !reason.value) return
  saving.value = true
  try {
    const result = await adminAPI.settings.updateOpenAIUserAffinityScheduling(config.value, reason.value)
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
