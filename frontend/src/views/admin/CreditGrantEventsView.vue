<template>
  <AppLayout>
    <div class="space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex w-full max-w-xl gap-2">
          <div class="relative min-w-0 flex-1">
            <Icon name="search" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
            <input
              v-model="search"
              class="input pl-10"
              :placeholder="t('admin.credits.grantEvents.search')"
              @keyup.enter="loadEvents(1)"
            />
          </div>
          <button class="btn btn-secondary" :disabled="loading" @click="loadEvents(1)">
            <Icon name="refresh" size="sm" />
            {{ t('common.refresh') }}
          </button>
        </div>
        <button class="btn btn-primary" @click="openCreate">
          <Icon name="plus" size="sm" />
          {{ t('admin.credits.grantEvents.create') }}
        </button>
      </div>

      <div class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
        <div class="overflow-x-auto">
          <table class="w-full min-w-[760px] text-left text-sm">
            <thead class="bg-gray-50 text-gray-500 dark:bg-dark-700 dark:text-dark-300">
              <tr>
                <th class="px-4 py-3">{{ t('admin.credits.grantEvents.name') }}</th>
                <th class="px-4 py-3">{{ t('admin.credits.grantEvents.type') }}</th>
                <th class="px-4 py-3">{{ t('admin.credits.amount') }}</th>
                <th class="px-4 py-3">{{ t('admin.credits.grantEvents.validity') }}</th>
                <th class="px-4 py-3">{{ t('admin.credits.grantEvents.updatedAt') }}</th>
                <th class="px-4 py-3 text-right">{{ t('common.actions') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr
                v-for="event in events"
                :key="event.id"
                data-test="credit-grant-event-row"
                class="hover:bg-gray-50 dark:hover:bg-dark-700/60"
              >
                <td class="px-4 py-3">
                  <p class="font-medium text-gray-900 dark:text-white">{{ event.name }}</p>
                  <p class="text-xs text-gray-500">#{{ event.id }}</p>
                </td>
                <td class="px-4 py-3">
                  <span
                    class="inline-flex rounded px-2 py-1 text-xs font-medium"
                    :class="event.credit_type === 'permanent'
                      ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
                      : 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300'"
                  >
                    {{ eventTypeText(event.credit_type) }}
                  </span>
                </td>
                <td class="px-4 py-3 font-medium">{{ '$' }}{{ precise(event.amount) }}</td>
                <td class="px-4 py-3 text-gray-600 dark:text-gray-300">
                  {{ event.validity_days ? t('admin.credits.grantEvents.daysValue', { days: event.validity_days }) : '-' }}
                </td>
                <td class="px-4 py-3 text-gray-500">{{ localDate(event.updated_at) }}</td>
                <td class="px-4 py-3">
                  <div class="flex justify-end gap-1">
                    <button
                      class="rounded p-2 text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-dark-700 dark:hover:text-white"
                      :title="t('common.edit')"
                      @click="openEdit(event)"
                    >
                      <Icon name="edit" size="sm" />
                    </button>
                    <button
                      class="rounded p-2 text-gray-500 hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                      :title="t('common.delete')"
                      @click="requestDelete(event)"
                    >
                      <Icon name="trash" size="sm" />
                    </button>
                  </div>
                </td>
              </tr>
              <tr v-if="!loading && events.length === 0">
                <td colspan="6" class="px-4 py-12 text-center text-gray-500">
                  {{ t('admin.credits.grantEvents.empty') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination
          v-if="total > 0"
          :page="page"
          :page-size="pageSize"
          :total="total"
          :show-page-size-selector="false"
          @update:page="loadEvents"
        />
      </div>
    </div>

    <BaseDialog
      :show="showEditor"
      :title="editingEvent ? t('admin.credits.grantEvents.edit') : t('admin.credits.grantEvents.create')"
      width="normal"
      @close="closeEditor"
    >
      <form id="credit-grant-event-form" class="space-y-4" @submit.prevent="submitEvent">
        <div>
          <label class="input-label">{{ t('admin.credits.grantEvents.name') }}</label>
          <input v-model="form.name" class="input" maxlength="100" required />
        </div>
        <div>
          <label class="input-label">{{ t('admin.credits.grantEvents.type') }}</label>
          <div class="grid grid-cols-2 rounded border border-gray-200 p-1 dark:border-dark-600">
            <button
              v-for="option in typeOptions"
              :key="option.value"
              type="button"
              class="h-9 px-3 text-sm font-medium transition-colors"
              :class="form.credit_type === option.value
                ? 'bg-gray-900 text-white dark:bg-white dark:text-gray-900'
                : 'text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700'"
              @click="selectType(option.value)"
            >
              {{ option.label }}
            </button>
          </div>
        </div>
        <div>
          <label class="input-label">{{ t('admin.credits.amount') }}</label>
          <input v-model.number="form.amount" class="input" type="number" min="0.00000001" max="999999999999.99999999" step="0.00000001" required />
        </div>
        <div v-if="form.credit_type === 'limited'">
          <label class="input-label">{{ t('admin.credits.days') }}</label>
          <input v-model.number="form.validity_days" class="input" type="number" min="1" max="36500" step="1" required />
        </div>
      </form>
      <template #footer>
        <button class="btn btn-secondary" @click="closeEditor">{{ t('common.cancel') }}</button>
        <button form="credit-grant-event-form" type="submit" class="btn btn-primary" :disabled="submitting">
          {{ t('common.confirm') }}
        </button>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="Boolean(deletingEvent)"
      :title="t('admin.credits.grantEvents.deleteTitle')"
      :message="t('admin.credits.grantEvents.deleteMessage', { name: deletingEvent?.name || '' })"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmDelete"
      @cancel="deletingEvent = null"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import { creditsAPI } from '@/api/admin'
import type { CreditGrantEvent, CreditGrantEventPayload, CreditGrantEventType } from '@/api/admin/credits'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()
const events = ref<CreditGrantEvent[]>([])
const loading = ref(false)
const submitting = ref(false)
const search = ref('')
const page = ref(1)
const pageSize = 20
const total = ref(0)
const showEditor = ref(false)
const editingEvent = ref<CreditGrantEvent | null>(null)
const deletingEvent = ref<CreditGrantEvent | null>(null)
const form = reactive({
  name: '',
  credit_type: 'permanent' as CreditGrantEventType,
  amount: 0,
  validity_days: 30
})

const typeOptions = [
  { value: 'permanent' as const, label: t('admin.credits.permanent') },
  { value: 'limited' as const, label: t('admin.credits.limited') }
]

const precise = (value: number) => Number(value || 0).toFixed(8).replace(/\.?(?:0+)$/, '')
const localDate = (value: string) => new Date(value).toLocaleString()
const eventTypeText = (type: CreditGrantEventType) => type === 'permanent' ? t('admin.credits.permanent') : t('admin.credits.limited')

function errorMessage(error: unknown): string {
  if (!error || typeof error !== 'object') return t('common.error')
  const apiError = error as { message?: unknown; response?: { data?: { message?: unknown } } }
  const message = apiError.response?.data?.message ?? apiError.message
  return typeof message === 'string' && message ? message : t('common.error')
}

async function loadEvents(targetPage = page.value) {
  loading.value = true
  try {
    const result = await creditsAPI.listCreditGrantEvents(targetPage, pageSize, search.value)
    events.value = result.items || []
    total.value = result.total || 0
    page.value = targetPage
  } catch (error: unknown) {
    appStore.showError(errorMessage(error))
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editingEvent.value = null
  Object.assign(form, { name: '', credit_type: 'permanent', amount: 0, validity_days: 30 })
  showEditor.value = true
}

function openEdit(event: CreditGrantEvent) {
  editingEvent.value = event
  Object.assign(form, {
    name: event.name,
    credit_type: event.credit_type,
    amount: event.amount,
    validity_days: event.validity_days || 30
  })
  showEditor.value = true
}

function closeEditor() {
  if (submitting.value) return
  showEditor.value = false
  editingEvent.value = null
}

function selectType(type: CreditGrantEventType) {
  form.credit_type = type
  if (type === 'limited' && form.validity_days < 1) form.validity_days = 30
}

async function submitEvent() {
  if (submitting.value) return
  submitting.value = true
  const currentEvent = editingEvent.value
  const payload: CreditGrantEventPayload = {
    name: form.name,
    credit_type: form.credit_type,
    amount: form.amount,
    ...(form.credit_type === 'limited' ? { validity_days: form.validity_days } : {})
  }
  try {
    if (currentEvent) {
      await creditsAPI.updateCreditGrantEvent(currentEvent.id, {
        ...payload,
        expected_updated_at: currentEvent.updated_at
      })
    } else {
      await creditsAPI.createCreditGrantEvent(payload)
    }
    showEditor.value = false
    editingEvent.value = null
    await loadEvents(currentEvent ? page.value : 1)
    appStore.showSuccess(t('common.success'))
  } catch (error: unknown) {
    appStore.showError(errorMessage(error))
  } finally {
    submitting.value = false
  }
}

function requestDelete(event: CreditGrantEvent) {
  deletingEvent.value = event
}

async function confirmDelete() {
  const event = deletingEvent.value
  if (!event || submitting.value) return
  submitting.value = true
  try {
    await creditsAPI.deleteCreditGrantEvent(event.id, event.updated_at)
    deletingEvent.value = null
    const nextPage = events.value.length === 1 && page.value > 1 ? page.value - 1 : page.value
    await loadEvents(nextPage)
    appStore.showSuccess(t('common.success'))
  } catch (error: unknown) {
    appStore.showError(errorMessage(error))
  } finally {
    submitting.value = false
  }
}

onMounted(() => loadEvents(1))
</script>
