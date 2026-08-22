<template>
  <AppLayout>
    <div class="space-y-5">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('balanceRefunds.title') }}</h1>
          <p class="mt-0.5 text-sm text-gray-500">{{ t('balanceRefunds.subtitle') }}</p>
        </div>
        <button type="button" class="btn btn-secondary" :disabled="loading" :title="t('common.refresh')" @click="refreshAll">
          <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
        </button>
      </div>

      <BalanceRefundStats :summary="summary" />

      <section class="space-y-3">
        <div class="flex flex-wrap items-center gap-3 border-b border-gray-200 pb-3 dark:border-dark-600">
          <div class="flex overflow-x-auto">
            <button v-for="tab in tabs" :key="tab" type="button" class="border-b-2 px-3 py-2 text-sm font-medium" :class="filters.tab === tab ? 'border-primary-600 text-primary-600' : 'border-transparent text-gray-500 hover:text-gray-900 dark:hover:text-white'" @click="setTab(tab)">{{ t(`balanceRefunds.tabs.${tab}`) }}</button>
          </div>
          <div class="ml-auto flex min-w-0 flex-1 flex-wrap items-center justify-end gap-2 lg:max-w-3xl">
            <input v-model.trim="filters.keyword" class="input min-w-0" :placeholder="t('balanceRefunds.search')" @keyup.enter="applyFilters" />
            <select v-model="filters.currency" class="input w-28" @change="applyFilters"><option value="">{{ t('balanceRefunds.allCurrencies') }}</option><option v-for="currency in currencies" :key="currency" :value="currency">{{ currency }}</option></select>
            <select v-model="filters.status" class="input w-36" @change="applyFilters"><option value="">{{ t('balanceRefunds.allStatuses') }}</option><option v-for="state in flowStates" :key="state" :value="state">{{ t(`balanceRefunds.states.${state}`) }}</option></select>
            <select v-model="filters.sort_by" class="input w-36" @change="applyFilters"><option value="updated_at">{{ t('balanceRefunds.sort.updated') }}</option><option value="email">{{ t('balanceRefunds.sort.email') }}</option><option value="refund_amount" :disabled="!filters.currency">{{ t('balanceRefunds.sort.refund') }}</option></select>
            <button type="button" class="btn btn-ghost" :title="t(`balanceRefunds.sort.${filters.sort_order}`)" @click="toggleSortOrder"><Icon name="sort" size="sm" :class="filters.sort_order === 'asc' ? 'rotate-180' : ''" /></button>
          </div>
        </div>

        <BalanceRefundTable :items="items" :loading="listLoading" @select="openDetail" @action="prepareAction" />
        <Pagination v-if="pagination.total > 0" :page="pagination.page" :total="pagination.total" :page-size="pagination.page_size" @update:page="changePage" @update:pageSize="changePageSize" />
      </section>
    </div>

    <BalanceRefundDetailDrawer :show="detailOpen" :loading="detailLoading" :detail="detail" @close="detailOpen = false" @action="prepareAction" />
    <BalanceRefundActionDialog :show="actionOpen" :item="detail?.item || selectedItem" :action="selectedAction" :loading="actionLoading" @close="actionOpen = false" @confirm="submitAction" />
    <BalanceRefundReconcileDialog :show="reconcileOpen" :detail="detail" :loading="actionLoading" @close="reconcileOpen = false" @confirm="submitReconcile" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { adminAccountRefundAPI, type AdminAccountRefundDirectAction } from '@/api/admin/accountRefunds'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Pagination from '@/components/common/Pagination.vue'
import BalanceRefundStats from '@/components/admin/payment/refunds/BalanceRefundStats.vue'
import BalanceRefundTable from '@/components/admin/payment/refunds/BalanceRefundTable.vue'
import BalanceRefundDetailDrawer from '@/components/admin/payment/refunds/BalanceRefundDetailDrawer.vue'
import BalanceRefundActionDialog from '@/components/admin/payment/refunds/BalanceRefundActionDialog.vue'
import BalanceRefundReconcileDialog from '@/components/admin/payment/refunds/BalanceRefundReconcileDialog.vue'
import type {
  AccountRefundAction,
  AdminAccountRefundDetail,
  AdminAccountRefundListItem,
  AdminAccountRefundReconcileInput,
  AdminAccountRefundSummary,
} from '@/types/accountRefund'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const tabs = ['refundable', 'processing', 'manual_review', 'completed', 'all'] as const
const flowStates = ['draining', 'ready_to_confirm', 'submitting', 'pending', 'failed', 'partial_external_success', 'manual_review', 'succeeded', 'canceled', 'donated'] as const
const filters = reactive({
  tab: (tabs.includes(route.query.tab as typeof tabs[number]) ? route.query.tab : 'refundable') as typeof tabs[number],
  keyword: String(route.query.keyword || ''),
  currency: String(route.query.currency || ''),
  status: String(route.query.status || ''),
  sort_by: (['updated_at', 'email', 'refund_amount'].includes(String(route.query.sort_by)) ? route.query.sort_by : 'updated_at') as 'updated_at' | 'email' | 'refund_amount',
  sort_order: (route.query.sort_order === 'asc' ? 'asc' : 'desc') as 'asc' | 'desc',
})
const pagination = reactive({ page: Number(route.query.page || 1), page_size: Number(route.query.page_size || 20), total: 0 })
const summary = ref<AdminAccountRefundSummary | null>(null)
const items = ref<AdminAccountRefundListItem[]>([])
const detail = ref<AdminAccountRefundDetail | null>(null)
const selectedItem = ref<AdminAccountRefundListItem | null>(null)
const selectedAction = ref<AccountRefundAction>('start')
const loading = ref(false)
const listLoading = ref(false)
const detailLoading = ref(false)
const actionLoading = ref(false)
const detailOpen = ref(false)
const actionOpen = ref(false)
const reconcileOpen = ref(false)
let pollingTimer: ReturnType<typeof setInterval> | null = null

const currencies = computed(() => {
  const values = new Set<string>()
  for (const source of [summary.value?.refundable_totals, summary.value?.automatic_totals, summary.value?.manual_external_totals]) {
    Object.keys(source || {}).forEach(currency => values.add(currency))
  }
  return [...values].sort()
})

async function loadSummary() {
  const response = await adminAccountRefundAPI.getSummary()
  summary.value = response.data
}

async function loadList() {
  listLoading.value = true
  try {
    const response = await adminAccountRefundAPI.getList({ page: pagination.page, page_size: pagination.page_size, tab: filters.tab, keyword: filters.keyword || undefined, currency: filters.currency || undefined, status: filters.status || undefined, sort_by: filters.sort_by, sort_order: filters.sort_order })
    items.value = response.data.items
    pagination.total = response.data.total
  } finally {
    listLoading.value = false
  }
}

async function refreshAll() {
  loading.value = true
  try {
    await Promise.all([loadSummary(), loadList()])
  } catch (error: unknown) {
    summary.value = null
    appStore.showError(extractI18nErrorMessage(error, t, 'payment.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

async function openDetail(item: AdminAccountRefundListItem) {
  selectedItem.value = item
  detailOpen.value = true
  detailLoading.value = true
  if (detail.value?.item.user_id !== item.user_id) detail.value = null
  try {
    const response = await adminAccountRefundAPI.getDetail(item.user_id)
    detail.value = response.data
    return true
  } catch (error: unknown) {
    appStore.showError(extractI18nErrorMessage(error, t, 'payment.errors', t('common.error')))
    detailOpen.value = false
    return false
  } finally {
    detailLoading.value = false
  }
}

async function prepareAction(item: AdminAccountRefundListItem, action: AccountRefundAction) {
  if (!detail.value || detail.value.item.user_id !== item.user_id || detail.value.item.state_revision !== item.state_revision) {
    if (!await openDetail(item)) return
  }
  selectedItem.value = detail.value?.item || item
  selectedAction.value = action
  if (action === 'reconcile') reconcileOpen.value = true
  else actionOpen.value = true
}

async function submitAction() {
  if (!detail.value) return
  actionLoading.value = true
  try {
    const action = selectedAction.value
    const input = { expected_state_revision: detail.value.item.state_revision, quote_hash: detail.value.quote?.quote_hash }
    if (action === 'start') {
      await adminAccountRefundAPI.start(detail.value.item.user_id, input, createIdempotencyKey())
    } else {
      await adminAccountRefundAPI.action(detail.value.item.user_id, action as AdminAccountRefundDirectAction, input)
    }
    actionOpen.value = false
    await reloadAfterAction(detail.value.item.user_id)
  } catch (error: unknown) {
    appStore.showError(extractI18nErrorMessage(error, t, 'payment.errors', t('common.error')))
    await reloadAfterAction(detail.value.item.user_id)
  } finally {
    actionLoading.value = false
  }
}

async function submitReconcile(input: AdminAccountRefundReconcileInput) {
  if (!detail.value) return
  actionLoading.value = true
  try {
    await adminAccountRefundAPI.reconcile(detail.value.item.user_id, input)
    reconcileOpen.value = false
    await reloadAfterAction(detail.value.item.user_id)
  } catch (error: unknown) {
    appStore.showError(extractI18nErrorMessage(error, t, 'payment.errors', t('common.error')))
    await reloadAfterAction(detail.value.item.user_id)
  } finally {
    actionLoading.value = false
  }
}

async function reloadAfterAction(userId: number) {
  await Promise.all([loadSummary(), loadList()])
  const current = items.value.find(item => item.user_id === userId) || selectedItem.value
  if (detailOpen.value && current) await openDetail(current)
}

function setTab(tab: typeof tabs[number]) { filters.tab = tab; pagination.page = 1; applyFilters() }
function changePage(page: number) { pagination.page = page; applyFilters() }
function changePageSize(size: number) { pagination.page_size = size; pagination.page = 1; applyFilters() }
function toggleSortOrder() { filters.sort_order = filters.sort_order === 'asc' ? 'desc' : 'asc'; applyFilters() }
function applyFilters() {
  if (!filters.currency && filters.sort_by === 'refund_amount') filters.sort_by = 'updated_at'
  void router.replace({ query: { ...route.query, tab: filters.tab, keyword: filters.keyword || undefined, currency: filters.currency || undefined, status: filters.status || undefined, sort_by: filters.sort_by, sort_order: filters.sort_order, page: String(pagination.page), page_size: String(pagination.page_size) } })
  void loadList().catch(error => appStore.showError(extractI18nErrorMessage(error, t, 'payment.errors', t('common.error'))))
}
function createIdempotencyKey(): string { return globalThis.crypto?.randomUUID?.() || `refund-${Date.now()}-${Math.random().toString(16).slice(2)}` }

async function pollActiveRefunds() {
  const activeItems = items.value.filter(item => !['estimate', 'succeeded', 'canceled', 'donated'].includes(item.flow_state))
  if (activeItems.length === 0) return
  const settled = await Promise.allSettled(activeItems.map(item => adminAccountRefundAPI.getDetail(item.user_id)))
  for (let index = 0; index < settled.length; index++) {
    const result = settled[index]
    if (result.status !== 'fulfilled') continue
    const updated = result.value.data
    const rowIndex = items.value.findIndex(item => item.user_id === updated.item.user_id)
    if (rowIndex >= 0) items.value[rowIndex] = updated.item
    if (detailOpen.value && detail.value?.item.user_id === updated.item.user_id) detail.value = updated
  }
}

onMounted(() => {
  void refreshAll()
  pollingTimer = setInterval(() => { void pollActiveRefunds() }, 10000)
})
onBeforeUnmount(() => { if (pollingTimer) clearInterval(pollingTimer) })
</script>
