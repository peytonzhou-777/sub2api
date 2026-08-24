<template>
  <Teleport to="body">
    <div v-if="show" class="fixed inset-0 z-[10000] flex items-center justify-center bg-black/50 p-4" @click.self="$emit('close')">
      <div class="flex max-h-[90vh] w-full max-w-5xl flex-col overflow-hidden rounded-lg bg-white shadow-xl dark:bg-dark-800">
        <header class="flex items-center justify-between border-b border-gray-200 px-5 py-4 dark:border-dark-700">
          <div>
            <h2 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.accounts.userAffinity.title') }}</h2>
            <p v-if="account" class="text-sm text-gray-500">{{ account.name }} · #{{ account.id }}</p>
            <p v-else class="text-sm text-gray-500">#{{ userId }}</p>
          </div>
          <button class="rounded p-2 text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-700" :title="t('common.close')" @click="$emit('close')">
            <Icon name="x" size="sm" />
          </button>
        </header>

        <div class="overflow-y-auto p-5">
          <div v-if="loading" class="flex justify-center py-12"><span class="h-6 w-6 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" /></div>
          <template v-else>
            <div v-if="account && policy" class="mb-5 grid gap-3 border-b border-gray-100 pb-5 sm:grid-cols-2 lg:grid-cols-4 dark:border-dark-700">
              <PolicyField v-model="policy.max_contact_users" :label="t('admin.accounts.userAffinity.maxContactUsers')" />
              <PolicyField v-model="policy.new_resident_cooldown_seconds" :label="t('admin.accounts.userAffinity.cooldownSeconds')" />
              <PolicyField v-model="policy.capacity_failure_migration_threshold" :label="t('admin.accounts.userAffinity.failureThreshold')" />
              <PolicyField v-model="policy.capacity_failure_window_seconds" :label="t('admin.accounts.userAffinity.failureWindow')" />
              <div class="sm:col-span-2 lg:col-span-4 flex justify-end">
                <button class="btn btn-secondary btn-sm" :disabled="policySaving" @click="savePolicy">{{ t('common.save') }}</button>
              </div>
            </div>

            <div v-if="account" class="overflow-x-auto">
              <table class="w-full text-left text-sm">
                <thead class="border-b border-gray-200 text-xs uppercase text-gray-500 dark:border-dark-700">
                  <tr><th class="px-3 py-2">{{ t('admin.accounts.userAffinity.user') }}</th><th class="px-3 py-2">Scope</th><th class="px-3 py-2">{{ t('admin.accounts.userAffinity.slot') }}</th><th class="px-3 py-2">{{ t('admin.accounts.userAffinity.status') }}</th><th class="px-3 py-2">{{ t('admin.accounts.userAffinity.heat') }}</th><th class="px-3 py-2">{{ t('admin.accounts.userAffinity.lastActive') }}</th><th class="px-3 py-2">{{ t('admin.accounts.userAffinity.residenceExpiry') }}</th><th class="px-3 py-2"></th></tr>
                </thead>
                <tbody>
                  <tr v-for="resident in residents" :key="resident.resident_slot_id" class="border-b border-gray-100 dark:border-dark-700">
                    <td class="px-3 py-3"><div class="font-medium text-gray-900 dark:text-white">{{ resident.user_email || `#${resident.user_id}` }}</div><div class="text-xs text-gray-500">#{{ resident.user_id }}</div></td>
                    <td class="max-w-56 break-all px-3 py-3 font-mono text-xs">{{ resident.scope_key }}</td>
                    <td class="px-3 py-3 font-mono">{{ resident.slot_index }} / g{{ resident.generation }}</td>
                    <td class="px-3 py-3"><span :class="slotStatusClass(resident.status)" class="rounded px-1.5 py-0.5 text-xs font-medium">{{ resident.status }}</span></td>
                    <td class="px-3 py-3"><div>{{ resident.usage_score.toFixed(2) }}</div><div v-if="resident.soft_owner || resident.active_route" class="text-xs text-gray-500">{{ resident.soft_owner ? t('admin.accounts.userAffinity.softOwner') : t('admin.accounts.userAffinity.activeRoute') }}</div></td>
                    <td class="px-3 py-3">{{ formatDateTime(resident.last_active_at) }}</td>
                    <td class="px-3 py-3">{{ formatDateTime(resident.expires_at) }}</td>
                    <td class="px-3 py-3 text-right"><button class="btn btn-secondary btn-sm" @click="inspectUser(resident.user_id, resident.scope_key)">{{ t('common.details') }}</button></td>
                  </tr>
                  <tr v-if="residents.length === 0"><td colspan="8" class="px-3 py-10 text-center text-gray-500">{{ t('common.noData') }}</td></tr>
                </tbody>
              </table>
            </div>

            <div v-if="detail" class="mt-6 border-t border-gray-200 pt-5 dark:border-dark-700">
              <div class="mb-3 flex flex-wrap items-center justify-between gap-3">
                <h3 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.accounts.userAffinity.migrationHistory') }} · #{{ inspectedUserId }}</h3>
                <div class="flex gap-2">
                  <select v-if="scopeOptions.length > 1" v-model="inspectedScopeKey" class="input max-w-72 font-mono text-xs">
                    <option v-for="scopeKey in scopeOptions" :key="scopeKey" :value="scopeKey">{{ scopeKey }}</option>
                  </select>
                  <button class="btn btn-danger btn-sm" :disabled="resetting || !inspectedScopeKey" @click="resetUser">{{ t('admin.accounts.userAffinity.reset') }}</button>
                </div>
              </div>
              <div class="mb-4 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
                <div v-for="slot in inspectedSlots" :key="slot.id" class="border-b border-gray-100 py-2 text-sm dark:border-dark-700">
                  <div class="break-all font-mono text-xs text-gray-500">{{ slot.scope_key }}</div>
                  <div class="mt-1">#{{ slot.account_id }} · {{ t('admin.accounts.userAffinity.slotValue', { index: slot.slot_index, generation: slot.generation }) }}</div>
                  <div class="mt-1 flex items-center justify-between gap-2"><span :class="slotStatusClass(slot.status)" class="rounded px-1.5 py-0.5 text-xs font-medium">{{ slot.status }}</span><span class="text-xs text-gray-500">{{ t('admin.accounts.userAffinity.heatValue', { value: slot.usage_score.toFixed(2) }) }}</span></div>
                  <div class="mt-1 text-xs text-gray-500">{{ t('admin.accounts.userAffinity.activeRouteUsers', { count: slot.active_route_user_count }) }}<span v-if="slot.soft_owner_user_id"> · {{ t('admin.accounts.userAffinity.softOwnerUser', { id: slot.soft_owner_user_id }) }}</span> · {{ formatDateTime(slot.expires_at) }}</div>
                </div>
                <div v-if="inspectedSlots.length === 0" class="py-4 text-sm text-gray-500">{{ t('common.noData') }}</div>
              </div>
              <div class="space-y-2">
                <div v-for="event in detail.events" :key="event.id" class="grid gap-2 border-b border-gray-100 py-2 text-sm sm:grid-cols-[10rem_1fr_12rem] dark:border-dark-700">
                  <span><span class="font-medium">{{ event.event_type }}</span><span class="mt-1 block break-all font-mono text-xs text-gray-500">{{ event.scope_key }}</span></span>
                  <span class="text-gray-600 dark:text-gray-300">{{ event.source_account_id ?? '-' }} → {{ event.target_account_id ?? '-' }}<span v-if="event.resident_slot_id"> · slot #{{ event.resident_slot_id }}</span> · {{ event.reason }}</span>
                  <span class="text-gray-500">{{ formatDateTime(event.created_at) }}</span>
                </div>
              </div>
            </div>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, ref, watch } from 'vue'
import type { PropType } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { OpenAIUserAffinityAccountPolicy, OpenAIUserAffinityResident, OpenAIUserAffinityUserDetail } from '@/api/admin/accounts'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null; userId?: number | null }>()
defineEmits<{ close: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const policySaving = ref(false)
const resetting = ref(false)
const residents = ref<OpenAIUserAffinityResident[]>([])
const policy = ref<OpenAIUserAffinityAccountPolicy | null>(null)
const detail = ref<OpenAIUserAffinityUserDetail | null>(null)
const inspectedUserId = ref<number | null>(null)
const inspectedScopeKey = ref('')
const scopeOptions = computed(() => {
  const scopes = new Set<string>()
  const residentSlots = detail.value?.resident_slots ?? []
  residentSlots.forEach(slot => scopes.add(slot.scope_key))
  detail.value?.placements.forEach(placement => scopes.add(placement.scope_key))
  return [...scopes]
})
const inspectedSlots = computed(() => (detail.value?.resident_slots ?? []).filter(slot => slot.scope_key === inspectedScopeKey.value))

const PolicyField = defineComponent({
  props: { modelValue: { type: Number as PropType<number | null>, default: null }, label: { type: String, required: true } },
  emits: ['update:modelValue'],
  setup(fieldProps, { emit }) {
    return () => h('label', { class: 'space-y-1 text-sm' }, [
      h('span', { class: 'font-medium text-gray-700 dark:text-gray-300' }, fieldProps.label),
      h('input', { class: 'input w-full', type: 'number', value: fieldProps.modelValue ?? '', placeholder: t('admin.accounts.userAffinity.inherit'), onInput: (event: Event) => {
        const value = (event.target as HTMLInputElement).value
        emit('update:modelValue', value === '' ? null : Number(value))
      } })
    ])
  }
})

function formatDateTime(value: string | null | undefined) {
  return value ? new Date(value).toLocaleString() : '-'
}

function slotStatusClass(status: string) {
  if (status === 'active') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  if (status === 'draining' || status === 'replacement_pending') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

async function load() {
  if (!props.account && !props.userId) return
  loading.value = true
  residents.value = []
  policy.value = null
  detail.value = null
  try {
    if (props.account) {
      const [residentPage, accountPolicy] = await Promise.all([
        adminAPI.accounts.listOpenAIUserAffinityResidents(props.account.id),
        adminAPI.accounts.getOpenAIUserAffinityAccountPolicy(props.account.id)
      ])
      residents.value = residentPage.items
      policy.value = accountPolicy
    } else if (props.userId) {
      await inspectUser(props.userId, '')
    }
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    loading.value = false
  }
}

async function savePolicy() {
  if (!props.account || !policy.value) return
  policySaving.value = true
  try {
    policy.value = await adminAPI.accounts.updateOpenAIUserAffinityAccountPolicy(props.account.id, policy.value)
    appStore.showSuccess(t('common.saved'))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    policySaving.value = false
  }
}

async function inspectUser(userId: number, scopeKey: string) {
  inspectedUserId.value = userId
  detail.value = await adminAPI.accounts.getOpenAIUserAffinityUserDetail(userId)
  const residentSlots = detail.value.resident_slots ?? []
  const requestedSlot = residentSlots.find(slot => slot.scope_key === scopeKey)
  const requestedPlacement = detail.value.placements.find(placement => placement.scope_key === scopeKey)
  const defaultScope = requestedSlot?.scope_key
    ?? requestedPlacement?.scope_key
    ?? residentSlots.find(slot => slot.status === 'active')?.scope_key
    ?? detail.value.placements.find(placement => placement.status === 'active')?.scope_key
    ?? residentSlots[0]?.scope_key
    ?? detail.value.placements[0]?.scope_key
  inspectedScopeKey.value = defaultScope ?? ''
}

async function resetUser() {
  if (!inspectedUserId.value || !inspectedScopeKey.value) return
  resetting.value = true
  try {
    await adminAPI.accounts.resetOpenAIUserAffinityPlacement(inspectedUserId.value, inspectedScopeKey.value)
    appStore.showSuccess(t('common.saved'))
    await load()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    resetting.value = false
  }
}

watch(() => [props.show, props.account?.id, props.userId] as const, ([show]) => { if (show) load() })
</script>
