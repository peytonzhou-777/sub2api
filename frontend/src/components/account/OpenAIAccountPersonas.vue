<template>
  <section class="border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="openai-account-personas">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h3 class="text-sm font-semibold text-gray-900 dark:text-gray-100">{{ t('admin.accounts.openai.dynamicPersona.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.dynamicPersona.summary', { count: personas.length, capacity: totalCapacity }) }}</p>
      </div>
      <button type="button" class="btn btn-secondary text-xs" @click="adding = !adding">
        <Icon name="plus" size="sm" class="mr-1" />
        {{ t('admin.accounts.openai.dynamicPersona.add') }}
      </button>
    </div>

    <div v-if="adding" class="mt-3 grid gap-3 border-y border-gray-100 py-3 dark:border-dark-700 sm:grid-cols-3">
      <label class="text-xs text-gray-600 dark:text-gray-300">
        <span>{{ t('admin.accounts.openai.dynamicPersona.profile') }}</span>
        <select v-model="createForm.profile_id" class="input mt-1 w-full">
          <option v-for="profile in profiles" :key="profile.id" :value="profile.id">{{ profileLabel(profile.id) }} · {{ profile.version }}</option>
        </select>
      </label>
      <label class="text-xs text-gray-600 dark:text-gray-300">
        <span>{{ t('admin.accounts.openai.dynamicPersona.proxy') }}</span>
        <select v-model="createForm.proxy_id" class="input mt-1 w-full">
          <option value="">{{ t('admin.accounts.openai.dynamicPersona.inheritProxy') }}</option>
          <option v-for="proxy in proxies" :key="proxy.id" :value="String(proxy.id)">{{ proxy.name }}</option>
        </select>
      </label>
      <label class="text-xs text-gray-600 dark:text-gray-300">
        <span>{{ t('admin.accounts.openai.dynamicPersona.maxClients') }}</span>
        <input v-model="createForm.max_active" type="number" min="1" max="10000" class="input mt-1 w-full" :placeholder="t('admin.accounts.openai.dynamicPersona.inheritPolicy')" />
      </label>
      <div class="flex justify-end gap-2 sm:col-span-3">
        <button type="button" class="btn btn-secondary text-xs" @click="adding = false">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn-primary text-xs" :disabled="busy === 'create'" @click="createPersona">{{ t('common.create') }}</button>
      </div>
    </div>

    <div v-if="loading" class="flex justify-center py-8">
      <span class="h-5 w-5 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></span>
    </div>
    <div v-else class="mt-3 divide-y divide-gray-100 border-y border-gray-100 dark:divide-dark-700 dark:border-dark-700">
      <article v-for="persona in personas" :key="persona.id" class="py-4" :data-testid="`openai-account-persona-${persona.id}`">
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <span class="font-medium text-gray-900 dark:text-gray-100">#{{ persona.position }} · {{ profileLabel(persona.profile_id) }}</span>
              <span class="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">{{ persona.profile_version }}</span>
              <span v-if="persona.default_protected" class="rounded bg-blue-50 px-1.5 py-0.5 text-xs text-blue-700 dark:bg-blue-950/40 dark:text-blue-300">{{ t('admin.accounts.openai.dynamicPersona.primary') }}</span>
              <span :class="stateClass(persona.state)" class="rounded px-1.5 py-0.5 text-xs">{{ persona.state }}</span>
            </div>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              OAuth: {{ persona.credential_state }} · epoch {{ persona.current_session_epoch || '-' }} · device {{ persona.installation_summary || '-' }}
            </p>
          </div>
          <div class="text-right text-xs text-gray-500 dark:text-gray-400">
            <div>{{ t('admin.accounts.openai.dynamicPersona.clients') }} {{ persona.active_client_sessions }}/{{ persona.effective_max_client_sessions }}</div>
            <div>{{ t('admin.accounts.openai.dynamicPersona.capacity') }} {{ persona.effective_max_concurrency }} · WS {{ persona.effective_max_websockets || '-' }}</div>
          </div>
        </div>

        <div class="mt-3 grid gap-3 sm:grid-cols-2">
          <label class="text-xs text-gray-600 dark:text-gray-300">
            <span>{{ t('admin.accounts.openai.dynamicPersona.proxy') }}</span>
            <select v-model="drafts[persona.id].proxy_id" class="input mt-1 w-full">
              <option value="">{{ t('admin.accounts.openai.dynamicPersona.inheritProxy') }}</option>
              <option v-for="proxy in proxies" :key="proxy.id" :value="String(proxy.id)">{{ proxy.name }}</option>
            </select>
          </label>
          <label class="text-xs text-gray-600 dark:text-gray-300">
            <span>{{ t('admin.accounts.openai.dynamicPersona.maxClients') }}</span>
            <input v-model="drafts[persona.id].max_active" type="number" min="1" max="10000" class="input mt-1 w-full" :placeholder="t('admin.accounts.openai.dynamicPersona.inheritPolicy')" />
          </label>
        </div>

        <div class="mt-3 flex flex-wrap gap-2">
          <button type="button" class="btn btn-secondary text-xs" :data-testid="`persona-save-${persona.id}`" :disabled="isBusy(persona)" @click="savePersona(persona)">{{ t('common.save') }}</button>
          <button v-if="!persona.default_protected" type="button" class="btn btn-secondary text-xs" :data-testid="`persona-authorize-${persona.id}`" :disabled="isBusy(persona)" @click="emit('authorize', persona)">
            {{ persona.authorized ? t('admin.accounts.openai.dynamicPersona.reauthorize') : t('admin.accounts.openai.dynamicPersona.authorize') }}
          </button>
          <button v-if="persona.authorized" type="button" class="btn btn-secondary text-xs" :data-testid="`persona-refresh-${persona.id}`" :disabled="isBusy(persona)" @click="refreshPersona(persona)">{{ t('admin.accounts.openai.dynamicPersona.refresh') }}</button>
          <button type="button" class="btn btn-secondary text-xs" :disabled="isBusy(persona)" @click="togglePersona(persona)">
            {{ persona.state === 'active' ? t('admin.accounts.openai.dynamicPersona.drain') : t('admin.accounts.openai.dynamicPersona.enable') }}
          </button>
          <button type="button" class="btn btn-secondary text-xs" :disabled="isBusy(persona)" @click="rotatePersona(persona, false)">{{ t('admin.accounts.openai.dynamicPersona.rotate') }}</button>
          <button type="button" class="btn btn-danger text-xs" :data-testid="`persona-force-rotate-${persona.id}`" :disabled="isBusy(persona)" @click="rotatePersona(persona, true)">{{ t('admin.accounts.openai.dynamicPersona.forceRotate') }}</button>
          <button v-if="!persona.default_protected" type="button" class="btn btn-danger text-xs" :data-testid="`persona-revoke-${persona.id}`" :disabled="isBusy(persona)" @click="revokePersona(persona)">{{ t('admin.accounts.openai.dynamicPersona.revoke') }}</button>
          <button v-if="!persona.default_protected" type="button" class="btn btn-danger text-xs" :disabled="isBusy(persona)" @click="hardDisablePersona(persona)">{{ t('admin.accounts.openai.dynamicPersona.hardDisable') }}</button>
          <button v-if="!persona.default_protected && persona.state === 'disabled'" type="button" class="btn btn-danger text-xs" :disabled="isBusy(persona)" @click="retirePersona(persona)">{{ t('admin.accounts.openai.dynamicPersona.retire') }}</button>
        </div>
      </article>
      <p v-if="personas.length === 0" class="py-6 text-center text-sm text-amber-600 dark:text-amber-300">{{ t('admin.accounts.openai.dynamicPersona.migrationRequired') }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import type { OpenAIAccountPersona, OpenAIAccountPersonaProfile } from '@/api/admin/accounts'
import type { Account, Proxy } from '@/types'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ account: Account; proxies: Proxy[] }>()
const emit = defineEmits<{ authorize: [persona: OpenAIAccountPersona] }>()
const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const adding = ref(false)
const busy = ref<string | null>(null)
const personas = ref<OpenAIAccountPersona[]>([])
const profiles = ref<OpenAIAccountPersonaProfile[]>([])
const drafts = reactive<Record<number, { proxy_id: string; max_active: string }>>({})
const createForm = reactive({ profile_id: 'opencode' as OpenAIAccountPersona['profile_id'], proxy_id: '', max_active: '' })
const totalCapacity = computed(() => personas.value.filter((item) => item.state === 'active' && item.authorized).reduce((sum, item) => sum + item.effective_max_concurrency, 0))

function profileLabel(profile: OpenAIAccountPersona['profile_id']) {
  return profile === 'opencode' ? 'OpenCode' : 'strict Codex'
}

function stateClass(state: OpenAIAccountPersona['state']) {
  if (state === 'active') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-300'
  if (state === 'draining') return 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

function resetDraft(persona: OpenAIAccountPersona) {
  drafts[persona.id] = {
    proxy_id: persona.proxy_id == null ? '' : String(persona.proxy_id),
    max_active: persona.max_active_client_sessions_override == null ? '' : String(persona.max_active_client_sessions_override)
  }
}

function isBusy(persona: OpenAIAccountPersona) {
  return busy.value === String(persona.id)
}

async function load() {
  loading.value = true
  try {
    const [personaList, profileList] = await Promise.all([
      adminAPI.accounts.listOpenAIAccountPersonas(props.account.id),
      adminAPI.accounts.listOpenAIAccountPersonaProfiles()
    ])
    personas.value = personaList
    profiles.value = profileList
    personaList.forEach(resetDraft)
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    loading.value = false
  }
}

async function mutate(persona: OpenAIAccountPersona, action: () => Promise<unknown>) {
  busy.value = String(persona.id)
  try {
    await action()
    await load()
    appStore.showSuccess(t('common.saved'))
  } catch (error) {
    if ((error as { response?: { status?: number } })?.response?.status === 409) {
      await load()
    }
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    busy.value = null
  }
}

async function createPersona() {
  busy.value = 'create'
  try {
    await adminAPI.accounts.createOpenAIAccountPersona(props.account.id, {
      profile_id: createForm.profile_id,
      proxy_id: createForm.proxy_id === '' ? null : Number(createForm.proxy_id),
      max_active_client_sessions_override: createForm.max_active === '' ? null : Number(createForm.max_active)
    })
    adding.value = false
    createForm.max_active = ''
    await load()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    busy.value = null
  }
}

async function savePersona(persona: OpenAIAccountPersona) {
  const draft = drafts[persona.id]
  await mutate(persona, () => adminAPI.accounts.updateOpenAIAccountPersona(props.account.id, persona.id, {
    row_version: persona.row_version,
    proxy_configured: true,
    proxy_id: draft.proxy_id === '' ? null : Number(draft.proxy_id),
    max_active_client_sessions_configured: true,
    max_active_client_sessions_override: draft.max_active === '' ? null : Number(draft.max_active)
  }))
}

async function togglePersona(persona: OpenAIAccountPersona) {
  await mutate(persona, () => adminAPI.accounts.updateOpenAIAccountPersona(props.account.id, persona.id, {
    row_version: persona.row_version,
    enabled: persona.state !== 'active'
  }))
}

async function refreshPersona(persona: OpenAIAccountPersona) {
  await mutate(persona, () => adminAPI.accounts.refreshOpenAIAccountPersona(props.account.id, persona.id))
}

async function rotatePersona(persona: OpenAIAccountPersona, force: boolean) {
  if (force && !window.confirm(t('admin.accounts.openai.dynamicPersona.forceRotateConfirm'))) return
  await mutate(persona, () => adminAPI.accounts.rotateOpenAIAccountPersonaSession(props.account.id, persona, force))
}

async function revokePersona(persona: OpenAIAccountPersona) {
  if (!window.confirm(t('admin.accounts.openai.dynamicPersona.revokeConfirm'))) return
  await mutate(persona, () => adminAPI.accounts.revokeOpenAIAccountPersona(props.account.id, persona))
}

async function hardDisablePersona(persona: OpenAIAccountPersona) {
  if (!window.confirm(t('admin.accounts.openai.dynamicPersona.hardDisableConfirm'))) return
  await mutate(persona, () => adminAPI.accounts.updateOpenAIAccountPersona(props.account.id, persona.id, {
    row_version: persona.row_version,
    state: 'disabled'
  }))
}

async function retirePersona(persona: OpenAIAccountPersona) {
  if (!window.confirm(t('admin.accounts.openai.dynamicPersona.retireConfirm'))) return
  await mutate(persona, () => adminAPI.accounts.retireOpenAIAccountPersona(props.account.id, persona))
}

watch(() => props.account.id, load, { immediate: true })
</script>
