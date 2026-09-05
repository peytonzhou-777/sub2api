<template>
  <section class="card">
    <header class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div>
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.openAIAccountAdmission.title') }}</h2>
        <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.settings.openAIAccountAdmission.description') }}
        </p>
      </div>
      <Toggle v-if="config" v-model="config.enabled" />
    </header>

    <div v-if="loading" class="flex items-center justify-center py-10">
      <span class="h-6 w-6 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></span>
    </div>

    <div v-else-if="config" class="p-6">
      <div v-if="config.enabled" class="space-y-6">
        <label class="flex items-start justify-between gap-4 border-b border-gray-100 pb-5 dark:border-dark-700">
          <span class="min-w-0">
            <span class="block text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.settings.openAIAccountAdmission.queueEnabled') }}</span>
            <span class="mt-1 block text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.settings.openAIAccountAdmission.queueEnabledHint') }}</span>
          </span>
          <Toggle v-model="config.queue_enabled" class="mt-0.5 shrink-0" />
        </label>

        <div class="grid gap-5 md:grid-cols-2 xl:grid-cols-3">
          <NumberField v-model="config.max_concurrency" :label="t('admin.settings.openAIAccountAdmission.globalMaxConcurrency')" :hint="t('admin.settings.openAIAccountAdmission.globalMaxConcurrencyHint')" :min="0" :max="100000" />
          <NumberField v-model="config.default_max_active_users_per_persona" :label="t('admin.settings.openAIAccountAdmission.globalMaxActiveUsers')" :hint="t('admin.settings.openAIAccountAdmission.globalMaxActiveUsersHint')" :min="1" :max="10000" />
          <NumberField v-model="config.requests_per_minute" :label="t('admin.settings.openAIAccountAdmission.rpm')" :hint="t('admin.settings.openAIAccountAdmission.rpmHint')" :min="0" :max="100000" />
          <NumberField v-model="config.tokens_per_minute" :label="t('admin.settings.openAIAccountAdmission.tpm')" :hint="t('admin.settings.openAIAccountAdmission.tpmHint')" :min="0" :max="100000000" />
          <NumberField v-model="config.max_subagents" :label="t('admin.settings.openAIAccountAdmission.globalMaxSubagents')" :hint="t('admin.settings.openAIAccountAdmission.globalMaxSubagentsHint')" :min="0" :max="100000" />
          <NumberField v-model="config.subagent_depth" :label="t('admin.settings.openAIAccountAdmission.globalSubagentDepth')" :hint="t('admin.settings.openAIAccountAdmission.globalSubagentDepthHint')" :min="0" :max="64" />
          <NumberField v-model="config.max_active_websockets" :label="t('admin.settings.openAIAccountAdmission.globalMaxWebSockets')" :hint="t('admin.settings.openAIAccountAdmission.globalMaxWebSocketsHint')" :min="0" :max="10000" />
          <NumberField v-model="config.default_output_tokens" :label="t('admin.settings.openAIAccountAdmission.defaultOutputTokens')" :hint="t('admin.settings.openAIAccountAdmission.defaultOutputTokensHint')" :min="1" :max="1000000" />
          <NumberField v-model="config.max_wait_seconds" :label="t('admin.settings.openAIAccountAdmission.maxWait')" :hint="t('admin.settings.openAIAccountAdmission.maxWaitHint')" :min="1" :max="120" />
          <NumberField v-model="config.max_queue_depth_per_account" :label="t('admin.settings.openAIAccountAdmission.queueDepth')" :hint="t('admin.settings.openAIAccountAdmission.queueDepthHint')" :min="1" :max="10000" />
          <NumberField v-model="config.interactive_burst" :label="t('admin.settings.openAIAccountAdmission.interactiveBurst')" :hint="t('admin.settings.openAIAccountAdmission.interactiveBurstHint')" :min="1" :max="100" />
          <NumberField v-model="config.background_aging_seconds" :label="t('admin.settings.openAIAccountAdmission.backgroundAging')" :hint="t('admin.settings.openAIAccountAdmission.backgroundAgingHint')" :min="1" :max="120" />
          <NumberField v-model="config.jitter_min_ms" :label="t('admin.settings.openAIAccountAdmission.jitterMin')" :hint="t('admin.settings.openAIAccountAdmission.jitterMinHint')" :min="0" :max="5000" />
          <NumberField v-model="config.jitter_max_ms" :label="t('admin.settings.openAIAccountAdmission.jitterMax')" :hint="t('admin.settings.openAIAccountAdmission.jitterMaxHint')" :min="0" :max="5000" />
        </div>

        <div class="border-t border-gray-100 pt-6 dark:border-dark-700">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.settings.openAIAccountAdmission.personaTitle') }}</h3>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.settings.openAIAccountAdmission.personaDescription') }}</p>
          <div class="mt-5 grid gap-5 xl:grid-cols-2">
            <section v-for="persona in personaCards" :key="persona.id" class="rounded-xl border border-gray-200 p-5 dark:border-dark-700">
              <h4 class="font-semibold text-gray-900 dark:text-white">{{ persona.label }}</h4>
              <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('admin.settings.openAIAccountAdmission.inheritHint') }}</p>
              <div class="mt-4 grid gap-4 md:grid-cols-2">
                <NumberField v-model="persona.policy.max_concurrency" :label="t('admin.settings.openAIAccountAdmission.personaMaxConcurrency')" :hint="t('admin.settings.openAIAccountAdmission.inheritGlobal')" :min="0" :max="100000" />
                <NumberField v-model="persona.policy.max_active_users" :label="t('admin.settings.openAIAccountAdmission.personaMaxActiveUsers')" :hint="t('admin.settings.openAIAccountAdmission.inheritGlobal')" :min="0" :max="10000" />
                <NumberField v-model="persona.policy.requests_per_minute" :label="t('admin.settings.openAIAccountAdmission.personaRPM')" :hint="t('admin.settings.openAIAccountAdmission.inheritGlobal')" :min="0" :max="1000000" />
                <NumberField v-model="persona.policy.tokens_per_minute" :label="t('admin.settings.openAIAccountAdmission.personaTPM')" :hint="t('admin.settings.openAIAccountAdmission.inheritGlobal')" :min="0" :max="1000000000" />
                <NumberField v-model="persona.policy.max_queue_depth_per_account" :label="t('admin.settings.openAIAccountAdmission.personaQueueDepth')" :hint="t('admin.settings.openAIAccountAdmission.inheritGlobal')" :min="0" :max="100000" />
                <NumberField v-model="persona.policy.max_subagents" :label="t('admin.settings.openAIAccountAdmission.personaMaxSubagents')" :hint="t('admin.settings.openAIAccountAdmission.inheritGlobal')" :min="0" :max="100000" />
                <NumberField v-model="persona.policy.subagent_depth" :label="t('admin.settings.openAIAccountAdmission.personaSubagentDepth')" :hint="t('admin.settings.openAIAccountAdmission.inheritGlobal')" :min="0" :max="64" />
                <NumberField v-model="persona.policy.max_active_websockets" :label="t('admin.settings.openAIAccountAdmission.personaMaxWebSockets')" :hint="t('admin.settings.openAIAccountAdmission.inheritGlobal')" :min="0" :max="10000" />
              </div>
            </section>
          </div>
        </div>
      </div>

      <div class="flex justify-end" :class="config.enabled ? 'mt-6 border-t border-gray-100 pt-5 dark:border-dark-700' : ''">
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
import type { OpenAIAccountAdmissionConfig, OpenAIPersonaAdmissionPolicy } from '@/api/admin/settings'
import Toggle from '@/components/common/Toggle.vue'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage, extractApiErrorStatus } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const saving = ref(false)
const config = ref<OpenAIAccountAdmissionConfig | null>(null)

const emptyPersonaPolicy = (): OpenAIPersonaAdmissionPolicy => ({
  max_concurrency: 0,
  max_active_users: 0,
  requests_per_minute: 0,
  tokens_per_minute: 0,
  max_queue_depth_per_account: 0,
  max_subagents: 0,
  subagent_depth: 0,
  max_active_websockets: 0
})

function normalizeConfig(value: OpenAIAccountAdmissionConfig): OpenAIAccountAdmissionConfig {
  const policies = value.persona_policies || {}
  const normalizePolicy = (policy?: OpenAIPersonaAdmissionPolicy): OpenAIPersonaAdmissionPolicy => {
    const source = policy || emptyPersonaPolicy()
    const { max_active_client_sessions, ...current } = source
    return {
      ...emptyPersonaPolicy(),
      ...current,
      max_active_users: source.max_active_users || max_active_client_sessions || 0
    }
  }
  const current = { ...value }
  const max_active_client_sessions = current.max_active_client_sessions
  delete current.max_active_client_sessions
  delete current.max_active_client_sessions_per_user_group
  return {
    ...current,
    max_concurrency: value.max_concurrency || 0,
    default_max_active_users_per_persona: value.default_max_active_users_per_persona || max_active_client_sessions || 1,
    max_subagents: value.max_subagents || 0,
    subagent_depth: value.subagent_depth || 0,
    max_active_websockets: value.max_active_websockets || 0,
    persona_policies: {
      codex_cli_strict: normalizePolicy(policies.codex_cli_strict),
      opencode: normalizePolicy(policies.opencode)
    }
  }
}

const personaCards = computed(() => {
  const policies = config.value?.persona_policies
  if (!policies) return []
  return [
    { id: 'codex_cli_strict', label: t('admin.settings.openAIAccountAdmission.strictCodex'), policy: policies.codex_cli_strict },
    { id: 'opencode', label: t('admin.settings.openAIAccountAdmission.openCode'), policy: policies.opencode }
  ]
})

const NumberField = defineComponent({
  props: {
    modelValue: { type: Number, required: true },
    label: { type: String, required: true },
    hint: { type: String, required: true },
    min: { type: Number, required: true },
    max: { type: Number, required: true }
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () => h('label', { class: 'flex min-w-0 flex-col text-sm' }, [
      h('span', { class: 'font-medium text-gray-700 dark:text-gray-300' }, props.label),
      h('input', {
        class: 'input mt-2 w-full', type: 'number', value: props.modelValue,
        min: props.min, max: props.max,
        onInput: (event: Event) => emit('update:modelValue', Number((event.target as HTMLInputElement).value))
      }),
      h('span', { class: 'mt-1.5 text-xs leading-5 text-gray-500 dark:text-gray-400' }, props.hint)
    ])
  }
})

async function load() {
  loading.value = true
  try {
    config.value = normalizeConfig((await adminAPI.settings.getOpenAIAccountAdmission()).config)
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!config.value) return
  const personaValues = Object.values(config.value.persona_policies || {}).flatMap((policy) => Object.values(policy))
  const inheritedGlobalValues = [config.value.max_concurrency, config.value.default_max_active_users_per_persona, config.value.max_subagents, config.value.subagent_depth, config.value.max_active_websockets]
  if ([...personaValues, ...inheritedGlobalValues].some((value) => !Number.isInteger(value) || value < 0) || config.value.jitter_min_ms > config.value.jitter_max_ms) {
    appStore.showError(t('admin.settings.openAIAccountAdmission.validationError'))
    return
  }
  saving.value = true
  try {
    config.value = normalizeConfig((await adminAPI.settings.updateOpenAIAccountAdmission(config.value)).config)
    appStore.showSuccess(t('common.saved'))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
    // 仅版本冲突重载权威配置；普通校验或网络错误保留管理员草稿。
    if (extractApiErrorStatus(error) === 409) {
      await load()
    }
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
