<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div class="flex w-full flex-col gap-3 sm:flex-row sm:items-start">
            <div class="w-full sm:w-80">
              <label for="account-pool-id" class="sr-only">{{ t('accountPool.search') }}</label>
              <input
                id="account-pool-id"
                :value="searchInput"
                inputmode="numeric"
                pattern="[0-9]*"
                class="input"
                :class="{ 'border-red-500': invalidSearch }"
                :placeholder="t('accountPool.searchPlaceholder')"
                @input="handleInput"
                @keydown.enter.prevent="submitSearch"
              />
              <p v-if="invalidSearch" class="mt-1 text-sm text-red-600">{{ t('accountPool.invalidId') }}</p>
            </div>
            <Select
              :model-value="statusFilter"
              class="w-full sm:w-44"
              :options="statusOptions"
              :aria-label="t('accountPool.statusFilter')"
              @update:model-value="changeStatus"
            />
            <Select
              :model-value="relationFilter"
              class="w-full sm:w-48"
              data-test="relation-filter"
              :options="relationOptions"
              :aria-label="t('accountPool.relationFilter')"
              @update:model-value="changeRelation"
            />
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <button
              type="button"
              class="btn btn-secondary whitespace-nowrap"
              data-test="query-seven-day-usage"
              :disabled="loading || retryUntil > Date.now()"
              @click="querySevenDayUsage"
            >
              <Icon name="search" size="sm" />
              <span>{{ t('accountPool.querySevenDayUsage') }}</span>
            </button>
            <button class="btn btn-secondary" :disabled="loading || retryUntil > Date.now()" :title="t('common.refresh')" @click="load(true)">
              <Icon name="refresh" size="md" :class="{ 'animate-spin': loading }" />
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <div class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
            <thead class="bg-gray-50 dark:bg-dark-800">
              <tr>
                <th
                  v-for="column in columns"
                  :key="column.key"
                  class="whitespace-nowrap px-4 py-3 text-left text-xs font-semibold uppercase text-gray-500"
                  :aria-sort="column.sortable ? columnAriaSort(column.key) : undefined"
                >
                  <button
                    v-if="column.sortable"
                    :data-test="`sort-${column.key}`"
                    type="button"
                    class="flex items-center gap-1 font-semibold uppercase hover:text-gray-700 dark:hover:text-gray-300"
                    :title="sortTitle(column.key)"
                    @click="toggleSort(column.key)"
                  >
                    <span>{{ column.label }}</span>
                    <Icon v-if="sortBy === column.key" :name="sortOrder === 'asc' ? 'arrowUp' : 'arrowDown'" size="xs" />
                  </button>
                  <span v-else>{{ column.label }}</span>
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 bg-white dark:divide-dark-700 dark:bg-dark-900">
              <tr v-if="loading && !page.items.length"><td colspan="9" class="px-4 py-12 text-center text-gray-500">{{ t('common.loading') }}</td></tr>
              <tr v-else-if="!page.items.length"><td colspan="9" class="px-4 py-12 text-center text-gray-500">{{ submittedId ? t('accountPool.deletedOrMissing') : t('common.noData') }}</td></tr>
              <tr v-for="(account, accountIndex) in page.items" :key="account.id">
                <td class="whitespace-nowrap px-4 py-3 font-mono text-sm font-medium">#{{ account.id }}</td>
                <td class="min-w-56 px-4 py-3 text-sm">
                  <div class="flex min-w-0 flex-col gap-1">
                    <div class="flex flex-wrap items-center gap-1">
                      <PlatformTypeBadge
                        :platform="platformOf(account)"
                        :type="typeOf(account)"
                        :auth-mode="account.auth_mode"
                        :plan-type="account.plan_type"
                        :privacy-mode="account.privacy_mode"
                      />
                      <span v-if="antigravityTierLabel(account)" :class="['inline-block rounded px-1.5 py-0.5 text-[10px] font-medium', antigravityTierClass(account)]">
                        {{ antigravityTierLabel(account) }}
                      </span>
                    </div>
                  </div>
                </td>
                <td class="min-w-40 px-4 py-3 text-sm">
                  <div class="flex flex-wrap gap-1">
                    <span v-if="account.is_primary_residence" class="rounded bg-amber-100 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/30 dark:text-amber-300">
                      {{ t('accountPool.relations.primaryResidence') }}
                    </span>
                    <span v-else-if="account.is_current_residence" class="rounded bg-primary-100 px-1.5 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
                      {{ t('accountPool.relations.currentResidence') }}
                    </span>
                    <span v-if="account.is_seven_day_contact" class="rounded bg-emerald-100 px-1.5 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300">
                      {{ t('accountPool.relations.sevenDayContact') }}
                    </span>
                    <span v-else-if="account.is_historical_contact" class="rounded bg-gray-100 px-1.5 py-0.5 text-xs font-medium text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                      {{ t('accountPool.relations.historicalContact') }}
                    </span>
                    <span v-if="!account.is_current_residence && !account.is_historical_contact" class="text-gray-400 dark:text-gray-500">--</span>
                  </div>
                </td>
                <td class="min-w-28 px-4 py-3 text-sm">
                  <div v-if="account.residents?.applicable" class="flex flex-col gap-1">
                    <div data-test="resident-active" class="flex items-center justify-between gap-3 whitespace-nowrap">
                      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('accountPool.residents.active') }}</span>
                      <span class="font-semibold text-emerald-600 dark:text-emerald-400">{{ account.residents.active }}</span>
                    </div>
                    <div data-test="resident-total" class="flex items-center justify-between gap-3 whitespace-nowrap">
                      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('accountPool.residents.total') }}</span>
                      <span class="font-medium text-gray-700 dark:text-gray-200">{{ account.residents.total }}</span>
                    </div>
                    <div data-test="resident-draining" class="flex items-center justify-between gap-3 whitespace-nowrap">
                      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('accountPool.residents.draining') }}</span>
                      <span class="font-medium text-amber-600 dark:text-amber-400">{{ account.residents.draining_slots ?? 0 }}</span>
                    </div>
                    <div data-test="resident-conversations" class="flex items-center justify-between gap-3 whitespace-nowrap">
                      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('accountPool.residents.conversations') }}</span>
                      <span class="font-medium text-gray-700 dark:text-gray-200">{{ account.residents.active_conversations ?? 0 }}</span>
                    </div>
                    <div data-test="resident-contacted" class="flex items-center justify-between gap-3 whitespace-nowrap">
                      <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('accountPool.residents.contacted') }}</span>
                      <span class="font-medium text-gray-700 dark:text-gray-200">{{ account.residents.contacted_users ?? 0 }}</span>
                    </div>
                  </div>
                  <span v-else data-test="resident-not-applicable" class="text-gray-400 dark:text-gray-500">--</span>
                </td>
                <td class="whitespace-nowrap px-4 py-3 text-sm"><AccountPoolCapacityCell :capacity="account.capacity" /></td>
                <td class="min-w-64 px-4 py-3 text-sm"><AccountPoolUsageCell :windows="account.usage_windows" /></td>
                <td class="min-w-72 px-4 py-3 text-sm">
                  <AccountPoolPersonalUsageCell
                    :account="account"
                    :auto-query-token="personalUsageQueryToken"
                    :auto-query-delay-ms="accountIndex * 150"
                  />
                </td>
                <td class="whitespace-nowrap px-4 py-3 text-sm">{{ account.reset_count_state === 'fresh' && account.reset_count != null ? account.reset_count : '--' }}</td>
                <td class="min-w-36 px-4 py-3 text-sm"><AccountPoolStatusCell :status="account.status" /></td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
      <template #pagination>
        <Pagination
          v-if="!submittedId && page.total > 0"
          :page="page.page"
          :total="page.total"
          :page-size="page.page_size"
          @update:page="changePage"
          @update:pageSize="changePageSize"
        />
      </template>
    </TablePageLayout>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import PlatformTypeBadge from '@/components/common/PlatformTypeBadge.vue'
import AccountPoolCapacityCell from '@/components/account/AccountPoolCapacityCell.vue'
import AccountPoolStatusCell from '@/components/account/AccountPoolStatusCell.vue'
import AccountPoolUsageCell from '@/components/account/AccountPoolUsageCell.vue'
import AccountPoolPersonalUsageCell from '@/components/account/AccountPoolPersonalUsageCell.vue'
import accountPoolAPI, {
  type AccountPoolPage,
  type AccountPoolRelationFilter,
  type AccountPoolSortBy,
  type AccountPoolSortOrder,
  type AccountPoolStatusCode,
} from '@/api/accountPool'
import type { AccountPlatform, AccountType } from '@/types'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { getPersistedPageSize, setPersistedPageSize } from '@/composables/usePersistedPageSize'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const page = ref<AccountPoolPage>({ items: [], total: 0, page: 1, page_size: getPersistedPageSize(), pages: 1 })
const loading = ref(false)
const searchInput = ref('')
const submittedId = ref('')
const statusFilter = ref<AccountPoolStatusCode | ''>('')
const relationFilter = ref<AccountPoolRelationFilter | ''>('')
const sortBy = ref<AccountPoolSortBy>('id')
const sortOrder = ref<AccountPoolSortOrder>('desc')
const etag = ref('')
const lastLoadedAt = ref(0)
const retryUntil = ref(0)
const personalUsageQueryToken = ref(0)
let controller: AbortController | undefined
let debounceTimer: ReturnType<typeof setTimeout> | undefined
let pollTimer: ReturnType<typeof setInterval> | undefined
let retryTimer: ReturnType<typeof setTimeout> | undefined
let sequence = 0

const invalidSearch = computed(() => searchInput.value !== '' && /^0+$/.test(searchInput.value))
const columns = computed(() => [
  { key: 'id' as const, label: t('accountPool.columns.id'), sortable: true },
  { key: 'platformType' as const, label: t('accountPool.columns.platformType'), sortable: false },
  { key: 'relation' as const, label: t('accountPool.columns.relation'), sortable: false },
  { key: 'residents' as const, label: t('accountPool.columns.residents'), sortable: false },
  { key: 'capacity' as const, label: t('accountPool.columns.capacity'), sortable: false },
  { key: 'usageWindow' as const, label: t('accountPool.columns.usageWindow'), sortable: false },
  { key: 'personalUsage' as const, label: t('accountPool.columns.personalUsage'), sortable: false },
  { key: 'resetCount' as const, label: t('accountPool.columns.resetCount'), sortable: false },
  { key: 'status' as const, label: t('accountPool.columns.status'), sortable: true },
])
const statusOptions = computed(() => [
  { value: '', label: t('accountPool.allStatus') },
  ...(['active', 'disabled', 'error', 'temporarily_unavailable', 'overloaded', 'rate_limited', 'paused', 'quota_exceeded'] as AccountPoolStatusCode[])
    .map(status => ({ value: status, label: t(`accountPool.status.${status}`) })),
])
const relationOptions = computed(() => [
  { value: '', label: t('accountPool.relations.all') },
  { value: 'primary_residence', label: t('accountPool.relations.primaryResidence') },
  { value: 'current_residence', label: t('accountPool.relations.currentResidence') },
  { value: 'seven_day_contact', label: t('accountPool.relations.sevenDayContact') },
  { value: 'historical_contact', label: t('accountPool.relations.historicalContact') },
])

function platformOf(account: AccountPoolPage['items'][number]): AccountPlatform {
  return account.platform as AccountPlatform
}

function typeOf(account: AccountPoolPage['items'][number]): AccountType {
  return account.type as AccountType
}

function antigravityTierLabel(account: AccountPoolPage['items'][number]): string {
  switch (account.antigravity_tier) {
    case 'free-tier': return t('admin.accounts.tier.free')
    case 'g1-pro-tier': return t('admin.accounts.tier.pro')
    case 'g1-ultra-tier': return t('admin.accounts.tier.ultra')
    default: return ''
  }
}

function antigravityTierClass(account: AccountPoolPage['items'][number]): string {
  switch (account.antigravity_tier) {
    case 'free-tier': return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
    case 'g1-pro-tier': return 'bg-blue-100 text-blue-600 dark:bg-blue-900/40 dark:text-blue-300'
    case 'g1-ultra-tier': return 'bg-purple-100 text-purple-600 dark:bg-purple-900/40 dark:text-purple-300'
    default: return ''
  }
}

function normalizeId(value: unknown): string {
  const digits = String(value ?? '').replace(/[^0-9]/g, '')
  if (!digits || /^0+$/.test(digits)) return digits
  return digits.replace(/^0+/, '')
}

function handleInput(event: Event) {
  const input = event.target as HTMLInputElement
  const filtered = input.value.replace(/[^0-9]/g, '')
  input.value = filtered
  searchInput.value = filtered
  if (debounceTimer) clearTimeout(debounceTimer)
  if (filtered === '') {
    clearSearch()
    return
  }
  debounceTimer = setTimeout(submitSearch, 300)
}

// clearSearch 立即取消旧搜索并恢复完整列表第一页。
function clearSearch() {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = undefined
  controller?.abort()
  controller = undefined
  sequence++
  loading.value = false
  searchInput.value = ''
  submittedId.value = ''
  etag.value = ''
  page.value = { ...page.value, page: 1 }
  void router.replace({ query: { ...route.query, account_id: undefined } })
  void load(true, 1)
}

function submitSearch() {
  if (debounceTimer) clearTimeout(debounceTimer)
  const normalized = normalizeId(searchInput.value)
  if (normalized && /^0+$/.test(searchInput.value)) return
  searchInput.value = normalized
  submittedId.value = normalized
  etag.value = ''
  if (!normalized) {
    clearSearch()
    return
  }
  page.value = { ...page.value, page: 1 }
  void router.replace({ query: { ...route.query, account_id: normalized } })
  void load(true, 1)
}

async function load(force = false, targetPage = page.value.page) {
  if (document.hidden || invalidSearch.value || Date.now() < retryUntil.value) return false
  controller?.abort()
  controller = new AbortController()
  const current = ++sequence
  loading.value = true
  try {
    const result = await accountPoolAPI.list({
      page: submittedId.value ? 1 : targetPage,
      pageSize: page.value.page_size,
      accountId: submittedId.value || undefined,
      status: statusFilter.value,
      relation: relationFilter.value,
      sortBy: sortBy.value,
      sortOrder: sortOrder.value,
      etag: force ? undefined : etag.value || undefined,
      signal: controller.signal,
    })
    if (current !== sequence) return
    if (result.data) page.value = result.data
    if (result.etag) etag.value = result.etag
    lastLoadedAt.value = Date.now()
    return true
  } catch (error: unknown) {
    if (current !== sequence) return
    const apiError = error as { code?: string; status?: number; retryAfter?: number }
    if (apiError.status === 429) {
      scheduleRetry(apiError.retryAfter)
    } else if (apiError.code !== 'ERR_CANCELED') {
      appStore.showError(extractApiErrorMessage(error, t('common.error')))
    }
    return false
  } finally {
    if (current === sequence) loading.value = false
  }
}

function changePage(target: number) {
  etag.value = ''
  void load(true, target)
}

function changePageSize(size: number) {
  setPersistedPageSize(size)
  page.value = { ...page.value, page: 1, page_size: size }
  etag.value = ''
  void load(true, 1)
}

function changeStatus(value: string | number | boolean | null) {
  statusFilter.value = String(value ?? '') as AccountPoolStatusCode | ''
  page.value = { ...page.value, page: 1 }
  etag.value = ''
  void load(true, 1)
}

function changeRelation(value: string | number | boolean | null) {
  relationFilter.value = String(value ?? '') as AccountPoolRelationFilter | ''
  page.value = { ...page.value, page: 1 }
  etag.value = ''
  void load(true, 1)
}

// 一键查询先切换为七日触达筛选，列表成功返回后再错峰查询各账号个人用量。
async function querySevenDayUsage() {
  relationFilter.value = 'seven_day_contact'
  page.value = { ...page.value, page: 1 }
  etag.value = ''
  if (await load(true, 1)) personalUsageQueryToken.value += 1
}

function toggleSort(key: string) {
  if (key !== 'id' && key !== 'status') return
  if (sortBy.value === key) {
    sortOrder.value = sortOrder.value === 'asc' ? 'desc' : 'asc'
  } else {
    sortBy.value = key
    sortOrder.value = 'asc'
  }
  page.value = { ...page.value, page: 1 }
  etag.value = ''
  void load(true, 1)
}

function columnAriaSort(key: string): 'ascending' | 'descending' | 'none' {
  if (sortBy.value !== key) return 'none'
  return sortOrder.value === 'asc' ? 'ascending' : 'descending'
}

function sortTitle(key: string): string {
  const nextOrder = sortBy.value === key && sortOrder.value === 'asc' ? 'desc' : 'asc'
  return t(nextOrder === 'asc' ? 'accountPool.sortAscending' : 'accountPool.sortDescending')
}

// scheduleRetry 遵守服务端 Retry-After，并在到期后仅补发一次请求。
function scheduleRetry(retryAfter?: number) {
  const delay = Math.min(Math.max(1, retryAfter ?? 30) * 1000, 2_147_483_647)
  retryUntil.value = Date.now() + delay
  if (retryTimer) clearTimeout(retryTimer)
  retryTimer = setTimeout(() => {
    retryTimer = undefined
    retryUntil.value = 0
    if (!document.hidden) void load(true)
  }, delay)
}

function handleVisibility() {
  if (!document.hidden && Date.now() - lastLoadedAt.value >= 30_000) void load()
}

watch(() => route.query.account_id, (value) => {
  const normalized = normalizeId(Array.isArray(value) ? value[0] : value)
  if (normalized !== submittedId.value) {
    searchInput.value = normalized
    submittedId.value = normalized
    etag.value = ''
    void load(true, 1)
  }
})

onMounted(() => {
  const normalized = normalizeId(Array.isArray(route.query.account_id) ? route.query.account_id[0] : route.query.account_id)
  searchInput.value = normalized
  submittedId.value = normalized
  void load(true, 1)
  pollTimer = setInterval(() => { if (!document.hidden) void load() }, 30_000)
  document.addEventListener('visibilitychange', handleVisibility)
})

onBeforeUnmount(() => {
  controller?.abort()
  if (debounceTimer) clearTimeout(debounceTimer)
  if (pollTimer) clearInterval(pollTimer)
  if (retryTimer) clearTimeout(retryTimer)
  document.removeEventListener('visibilitychange', handleVisibility)
})
</script>
