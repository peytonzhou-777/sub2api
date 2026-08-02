<template>
  <AppLayout>
    <div class="space-y-4">
      <div class="flex flex-wrap items-center gap-3">
        <div class="relative w-full max-w-sm">
          <Icon name="search" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
          <input v-model="search" class="input pl-10" :placeholder="t('admin.credits.search')" @keyup.enter="loadUsers(1)" />
        </div>
        <button class="btn btn-secondary" :disabled="loading" @click="loadUsers(1)"><Icon name="refresh" size="sm" />{{ t('common.refresh') }}</button>
      </div>

      <div class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
        <table class="w-full text-left text-sm">
          <thead class="bg-gray-50 text-gray-500 dark:bg-dark-700 dark:text-dark-300"><tr><th class="px-4 py-3">{{ t('admin.credits.user') }}</th><th class="px-4 py-3">{{ t('admin.credits.permanent') }}</th><th class="px-4 py-3">{{ t('admin.credits.limited') }}</th><th class="px-4 py-3">{{ t('admin.credits.total') }}</th><th class="px-4 py-3">{{ t('admin.credits.eventProgress') }}</th><th class="px-4 py-3 text-right">{{ t('common.actions') }}</th></tr></thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="user in users" :key="user.id" class="hover:bg-gray-50 dark:hover:bg-dark-700/60">
              <td class="px-4 py-3"><p class="font-medium text-gray-900 dark:text-white">{{ user.email }}</p><p class="text-xs text-gray-500">#{{ user.id }} · {{ user.username || '-' }}</p></td>
              <td class="px-4 py-3 font-medium">${{ money(user.balance) }}</td>
              <td class="px-4 py-3"><span class="font-medium text-red-600 dark:text-red-400">${{ money(user.limited_remaining_amount) }}</span><span class="ml-2 text-xs text-gray-500">{{ user.limited_active_count }} {{ t('admin.credits.items') }}</span></td>
              <td class="px-4 py-3 font-semibold">${{ money(user.balance + user.limited_remaining_amount) }}</td>
              <td class="px-4 py-3 font-medium" data-test="grant-event-progress">{{ user.triggered_event_count }}/{{ user.active_event_count }}</td>
              <td class="px-4 py-3 text-right"><button class="btn btn-secondary btn-sm" @click="openUser(user.id)">{{ t('admin.credits.manage') }}</button></td>
            </tr>
            <tr v-if="!loading && users.length === 0"><td colspan="6" class="px-4 py-12 text-center text-gray-500">{{ t('admin.credits.empty') }}</td></tr>
          </tbody>
        </table>
      </div>
    </div>

    <Teleport to="body">
      <div v-if="detail" class="fixed inset-0 z-50 bg-black/35" @click.self="closeDetail">
        <aside class="ml-auto h-full w-full max-w-2xl overflow-y-auto bg-white p-5 shadow-xl dark:bg-dark-900">
          <div class="flex items-start justify-between"><div><h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ detail.email }}</h2><p class="text-sm text-gray-500">#{{ detail.id }} · {{ detail.username || '-' }}</p></div><button class="btn btn-secondary px-2" @click="closeDetail"><Icon name="x" /></button></div>
          <section class="mt-5 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <div class="flex items-center justify-between"><div><p class="text-sm text-gray-500">{{ t('admin.credits.permanent') }}</p><p class="text-2xl font-semibold">${{ money(detail.balance) }}</p><p v-if="detail.frozen_balance > 0" class="text-xs text-amber-600">{{ t('admin.credits.frozen') }} ${{ money(detail.frozen_balance) }}</p></div><div class="flex flex-wrap justify-end gap-2"><button class="btn btn-secondary" @click="toggleBalanceHistory">{{ t('admin.credits.balanceHistory') }}</button><button class="btn btn-secondary" @click="startAction('balance-add')">{{ t('admin.credits.add') }}</button><button class="btn btn-danger" @click="startAction('balance-subtract')">{{ t('admin.credits.subtract') }}</button></div></div>
            <div v-if="showBalanceHistory" class="mt-4 divide-y divide-gray-100 border-t border-gray-100 pt-2 text-xs dark:divide-dark-700 dark:border-dark-700"><div v-for="entry in balanceHistory" :key="entry.id" class="flex justify-between gap-3 py-2"><div><p>{{ t(`admin.credits.balanceEvents.${entry.type}`) }}</p><p v-if="entry.notes" class="text-gray-500">{{ entry.notes }}</p></div><div class="text-right"><p :class="entry.value >= 0 ? 'text-green-600' : 'text-red-600'">{{ entry.value >= 0 ? '+' : '' }}${{ precise(entry.value) }}</p><p class="text-gray-500">{{ localDate(entry.used_at || entry.created_at) }}</p></div></div><p v-if="balanceHistory.length === 0" class="py-3 text-center text-gray-500">{{ t('admin.credits.noHistory') }}</p></div>
          </section>
          <section class="mt-6" data-test="grant-event-details">
            <h3 class="font-semibold">{{ t('admin.credits.eventDetails') }}</h3>
            <div class="mt-3 space-y-3">
              <article v-for="event in detail.credit_grant_events" :key="event.id" class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
                <div class="flex items-start justify-between gap-3">
                  <div class="min-w-0">
                    <p class="break-words font-medium text-gray-900 dark:text-white">{{ event.name }}</p>
                    <p class="mt-1 text-xs text-gray-500">
                      {{ t('admin.credits.currentConfig') }}:
                      {{ eventTypeText(event.credit_type) }} · {{ '$' }}{{ precise(event.amount) }}
                      <template v-if="event.credit_type === 'limited'"> · {{ t('admin.credits.grantEvents.daysValue', { days: event.validity_days }) }}</template>
                    </p>
                  </div>
                  <span class="shrink-0 rounded-full px-2 py-1 text-xs font-medium" :class="event.triggered ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'">
                    {{ event.triggered ? t('admin.credits.triggered') : t('admin.credits.untriggered') }}
                  </span>
                </div>
                <div v-if="event.triggered" class="mt-3 border-t border-gray-100 pt-3 text-xs text-gray-500 dark:border-dark-700">
                  <p>
                    {{ t('admin.credits.actualGrant') }}:
                    {{ eventTypeText(event.actual_credit_type || event.credit_type) }} · {{ '$' }}{{ precise(event.actual_amount ?? event.amount) }}
                    <template v-if="(event.actual_credit_type || event.credit_type) === 'limited' && event.actual_validity_days"> · {{ t('admin.credits.grantEvents.daysValue', { days: event.actual_validity_days }) }}</template>
                  </p>
                  <p v-if="event.actual_expires_at" class="mt-1">{{ t('admin.credits.grantEvents.validity') }}: {{ localDate(event.actual_expires_at) }}</p>
                  <p v-if="event.triggered_at" class="mt-1">{{ t('admin.credits.triggeredAt') }}: {{ localDate(event.triggered_at) }}</p>
                </div>
                <div v-else class="mt-3 flex justify-end">
                  <button class="btn btn-primary btn-sm" :disabled="triggeringEventId !== null" :data-test="'trigger-grant-event-' + event.id" @click="triggerCreditEvent(event)">
                    {{ t('admin.credits.grantEvent') }}
                  </button>
                </div>
              </article>
              <p v-if="detail.credit_grant_events.length === 0" class="rounded-lg border border-dashed border-gray-200 px-4 py-8 text-center text-sm text-gray-500 dark:border-dark-700">
                {{ t('admin.credits.grantEvents.empty') }}
              </p>
            </div>
          </section>
          <div class="mt-6 flex flex-wrap items-center justify-between gap-2"><h3 class="font-semibold">{{ t('admin.credits.limitedDetails') }}</h3><div class="flex gap-2"><Select v-model="limitedFilter" class="w-32" :options="[{value:'active',label:t('admin.credits.activeOnly')},{value:'all',label:t('admin.credits.allRecords')}]" /><button class="btn btn-primary" @click="startAction('limited-create')"><Icon name="plus" size="sm" />{{ t('admin.credits.createLimited') }}</button></div></div>
          <div class="mt-3 space-y-3">
            <article v-for="grant in displayedCredits" :key="grant.id" class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
              <div class="flex items-start justify-between"><div><p class="font-medium">{{ t('admin.credits.limitedItem', { id: grant.id }) }}</p><p class="text-xs text-gray-500">{{ sourceText(grant.source_type) }} · {{ statusText(grant.status) }}</p></div><p class="font-semibold">${{ precise(grant.used_amount) }} / ${{ precise(grant.initial_amount) }}</p></div>
              <div class="mt-3 h-2 overflow-hidden rounded bg-gray-200 dark:bg-dark-700"><div class="h-full bg-primary-500" :style="{ width: `${Math.min(100, grant.initial_amount ? grant.used_amount / grant.initial_amount * 100 : 0)}%` }" /></div>
              <div class="mt-2 flex flex-wrap items-center justify-between gap-2 text-xs text-gray-500"><span>{{ localDate(grant.expires_at) }}</span><span v-if="grant.frozen_amount > 0">{{ t('admin.credits.frozen') }} ${{ precise(grant.frozen_amount) }}</span></div>
              <div class="mt-3 flex flex-wrap justify-end gap-2"><button class="btn btn-secondary btn-sm" @click="toggleLedger(grant)">{{ t('admin.credits.ledger') }}</button><template v-if="grant.status === 'active' && new Date(grant.expires_at) > new Date()"><button class="btn btn-secondary btn-sm" @click="startGrantAction('limited-used', grant)">{{ t('admin.credits.adjustUsed') }}</button><button class="btn btn-secondary btn-sm" @click="startGrantAction('limited-limit', grant)">{{ t('admin.credits.adjustLimit') }}</button><button class="btn btn-secondary btn-sm" @click="startGrantAction('limited-expiry', grant)">{{ t('admin.credits.adjustExpiry') }}</button></template><button v-if="grant.status !== 'revoked'" class="btn btn-primary btn-sm" :disabled="grant.frozen_amount > 0" @click="startGrantAction('limited-reset', grant)">{{ t('admin.credits.reset') }}</button><button v-if="grant.status === 'active'" class="btn btn-danger btn-sm" :disabled="grant.frozen_amount > 0" @click="startGrantAction('limited-revoke', grant)">{{ t('admin.credits.revoke') }}</button></div>
              <div v-if="expandedLedger === grant.id" class="mt-3 divide-y divide-gray-100 rounded border border-gray-100 px-3 text-xs dark:divide-dark-700 dark:border-dark-700"><div v-for="entry in ledger" :key="entry.id" class="flex items-start justify-between gap-3 py-2"><div><p>{{ t(`admin.credits.events.${entry.event_type}`) }}</p><p v-if="entry.notes" class="text-gray-500">{{ entry.notes }}</p></div><div class="text-right"><p class="font-medium">{{ formatLedgerAmount(entry) }}</p><p class="text-gray-500">{{ localDate(entry.created_at) }}</p></div></div></div>
            </article>
          </div>
        </aside>
      </div>
    </Teleport>

    <BaseDialog :show="action !== ''" :title="actionTitle" width="narrow" :close-on-escape="!showBalanceConflictConfirm" @close="resetAction">
      <form id="credit-action" class="space-y-4" @submit.prevent="submitAction">
        <div v-if="!['limited-revoke','limited-reset'].includes(action)"><label class="input-label">{{ action === 'limited-expiry' || action === 'limited-create' ? t('admin.credits.days') : t('admin.credits.amount') }}</label><input v-model.number="form.value" type="number" :step="action === 'limited-expiry' || action === 'limited-create' ? 1 : 0.00000001" min="0" class="input" required /></div>
        <div v-if="action === 'limited-create'"><label class="input-label">{{ t('admin.credits.amount') }}</label><input v-model.number="form.amount" type="number" step="0.00000001" min="0" class="input" required /></div>
        <Select v-if="['limited-used','limited-limit','limited-expiry'].includes(action)" v-model="form.operation" :options="[{value:'add',label:t('admin.credits.add')},{value:'subtract',label:t('admin.credits.subtract')}]" />
        <div><label class="input-label">{{ t('admin.credits.notes') }}</label><textarea v-model="form.notes" class="input" rows="3" /></div>
        <div class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">{{ previewText }}</div>
      </form>
      <template #footer><button class="btn btn-secondary" @click="resetAction">{{ t('common.cancel') }}</button><button form="credit-action" type="submit" class="btn" :class="action.includes('subtract') || action === 'limited-revoke' ? 'btn-danger' : 'btn-primary'" :disabled="submitting || showBalanceConflictConfirm || balanceConflictRefreshing">{{ t('common.confirm') }}</button></template>
    </BaseDialog>
    <ConfirmDialog
      :show="showBalanceConflictConfirm"
      :title="t('admin.credits.balanceConflict.title')"
      :message="t('admin.credits.balanceConflict.message')"
      :confirm-text="t('admin.credits.balanceConflict.retry')"
      :cancel-text="t('common.cancel')"
      @confirm="retryBalanceAdjustment"
      @cancel="cancelBalanceConflictRetry"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { adminAPI, creditsAPI } from '@/api/admin'
import type { BalanceHistoryItem } from '@/api/admin'
import type { AdminLimitedCredit, CreditGrantEventType, CreditGrantEventUserStatus, CreditUser, CreditUserDetail, LimitedCreditLedgerEntry } from '@/api/admin/credits'
import { extractApiErrorStatus } from '@/utils/apiError'

const { t } = useI18n(); const route = useRoute(); const router = useRouter(); const appStore = useAppStore()
const users = ref<CreditUser[]>([]); const detail = ref<CreditUserDetail | null>(null); const search = ref(''); const loading = ref(false); const submitting = ref(false)
const expandedLedger = ref<number | null>(null); const ledger = ref<LimitedCreditLedgerEntry[]>([])
const showBalanceHistory = ref(false); const balanceHistory = ref<BalanceHistoryItem[]>([])
const triggeringEventId = ref<number | null>(null)
const limitedFilter = ref('active')
const action = ref(''); const selectedGrant = ref<AdminLimitedCredit | null>(null); const form = reactive({ value: 0, amount: 0, operation: 'add', notes: '' })
interface PendingBalanceAdjustment {
  userId: number
  operation: 'add' | 'subtract'
  amount: number
  notes: string
}

const activeDetailId = ref<number | null>(null)
const showBalanceConflictConfirm = ref(false)
const balanceConflictRefreshing = ref(false)
const balanceConflictRetried = ref(false)
const balanceConflictSession = ref(0)
const pendingBalanceAdjustment = ref<PendingBalanceAdjustment | null>(null)
const money = (v:number) => v.toFixed(2); const precise=(v:number)=>v.toFixed(8).replace(/\.?(?:0+)$/,''); const localDate=(v:string)=>new Date(v).toLocaleString()
const sourceText=(v:string)=>t(`admin.credits.sources.${v}`); const statusText=(v:string)=>t(`admin.credits.statuses.${v}`)
const eventTypeText=(v:CreditGrantEventType)=>v==='permanent'?t('admin.credits.permanent'):t('admin.credits.limited')
const formatLedgerAmount=(entry:LimitedCreditLedgerEntry)=>['admin_extend_expiry','admin_reduce_expiry'].includes(entry.event_type)?`${precise(entry.amount)} ${t('admin.credits.dayUnit')}`:`$${precise(entry.amount)} ${t('admin.credits.creditUnit')}`
const actionTitle=computed(()=>t(`admin.credits.actions.${action.value || 'none'}`))
const displayedCredits=computed(()=>limitedFilter.value==='all'?(detail.value?.limited_credits||[]):(detail.value?.limited_credits||[]).filter(item=>item.status==='active'&&new Date(item.expires_at)>new Date()))
const previewText=computed(()=>{ if(!detail.value)return ''; if(action.value.startsWith('balance')) { const next=detail.value.balance+(action.value==='balance-add'?form.value:-form.value); return t('admin.credits.balancePreview',{before:precise(detail.value.balance),after:precise(next)}) } if(action.value==='limited-create')return t('admin.credits.createPreview',{amount:precise(form.amount),days:form.value}); if(!selectedGrant.value)return ''; if(action.value==='limited-limit'){const next=selectedGrant.value.initial_amount+(form.operation==='add'?form.value:-form.value);return t('admin.credits.limitPreview',{before:precise(selectedGrant.value.initial_amount),after:precise(next)})} if(action.value==='limited-used'){const next=selectedGrant.value.used_amount+(form.operation==='add'?form.value:-form.value);return t('admin.credits.usedPreview',{before:precise(selectedGrant.value.used_amount),after:precise(next)})} if(action.value==='limited-expiry'){const d=new Date(selectedGrant.value.expires_at);d.setDate(d.getDate()+(form.operation==='add'?form.value:-form.value));return t('admin.credits.expiryPreview',{date:d.toLocaleString()})} if(action.value==='limited-reset')return selectedGrant.value.validity_days?t('admin.credits.resetWithExpiryPreview',{days:selectedGrant.value.validity_days}):t('admin.credits.resetPreview'); return t('admin.credits.revokePreview') })
async function loadUsers(page=1){loading.value=true;try{const r=await creditsAPI.listCreditUsers(page,20,search.value);users.value=r.items||[]}finally{loading.value=false}}
async function openUser(id: number) {
  resetBalanceConflictState()
  activeDetailId.value = id
  const user = await creditsAPI.getCreditUser(id)
  if (activeDetailId.value !== id) return
  detail.value = user
  await router.replace({ query: { ...route.query, user: String(id) } })
}
function closeDetail() {
  resetBalanceConflictState()
  activeDetailId.value = null
  detail.value = null
  void router.replace({ query: {} })
}
function startAction(v: string) {
  resetBalanceConflictState()
  action.value = v
  form.value = v === 'limited-create' ? 30 : 0
  form.amount = 0
  form.operation = 'add'
  form.notes = ''
}
function startGrantAction(v:string,g:AdminLimitedCredit){selectedGrant.value=g;startAction(v)}
function resetAction() {
  resetBalanceConflictState()
  action.value = ''
  selectedGrant.value = null
}
async function toggleLedger(grant:AdminLimitedCredit){if(!detail.value)return;if(expandedLedger.value===grant.id){expandedLedger.value=null;return}ledger.value=await creditsAPI.listLimitedCreditLedger(detail.value.id,grant.id);expandedLedger.value=grant.id}
async function loadBalanceHistory(userId:number){const result=await adminAPI.users.getUserBalanceHistory(userId,1,50);balanceHistory.value=(result.items||[]).filter(item=>['balance','admin_balance','affiliate_balance'].includes(item.type))}
async function toggleBalanceHistory(){if(!detail.value)return;showBalanceHistory.value=!showBalanceHistory.value;if(showBalanceHistory.value&&balanceHistory.value.length===0)await loadBalanceHistory(detail.value.id)}

/** 发放事件后刷新用户额度、事件状态及已展开的永久额度流水。 */
async function triggerCreditEvent(event:CreditGrantEventUserStatus){
  const currentDetail=detail.value
  if(!currentDetail||event.triggered||triggeringEventId.value!==null)return
  triggeringEventId.value=event.id
  try{
    const triggered=await creditsAPI.triggerCreditGrantEvent(currentDetail.id,event.id)
    const eventIndex=currentDetail.credit_grant_events.findIndex(item=>item.id===event.id)
    if(eventIndex>=0)currentDetail.credit_grant_events[eventIndex]=triggered
    try{
      await refreshUserDetail(currentDetail.id)
      await loadUsers()
      if(showBalanceHistory.value)await loadBalanceHistory(currentDetail.id)
      appStore.showSuccess(t('admin.credits.grantEventSuccess'))
    }catch(refreshError:unknown){
      appStore.showSuccess(t('admin.credits.grantEventSuccess'))
      appStore.showError(extractCreditActionErrorMessage(refreshError))
    }
  }catch(error:unknown){
    if(extractApiErrorStatus(error)===409){
      try{
        await refreshUserDetail(currentDetail.id)
        await loadUsers()
        if(showBalanceHistory.value)await loadBalanceHistory(currentDetail.id)
      }catch(refreshError:unknown){
        appStore.showError(extractCreditActionErrorMessage(refreshError))
        return
      }
      appStore.showWarning(t('admin.credits.grantEventAlreadyTriggered'))
    }else{
      appStore.showError(extractCreditActionErrorMessage(error))
    }
  }finally{
    triggeringEventId.value=null
  }
}
/**
 * 清理当前额度冲突流程，避免状态带入下一个用户或下一次操作。
 */
function resetBalanceConflictState() {
  balanceConflictSession.value += 1
  showBalanceConflictConfirm.value = false
  balanceConflictRefreshing.value = false
  balanceConflictRetried.value = false
  pendingBalanceAdjustment.value = null
}

/**
 * 409 后必须读取详情接口；冲突刷新携带会话号时，忽略已失效的异步响应。
 */
async function refreshUserDetail(id: number, conflictSession?: number) {
  const refreshed = await creditsAPI.getCreditUser(id)
  if (
    activeDetailId.value === id &&
    (conflictSession === undefined || conflictSession === balanceConflictSession.value)
  ) {
    detail.value = refreshed
  }
  return refreshed
}

async function finishSuccessfulAction(userId: number) {
  await refreshUserDetail(userId)
  await loadUsers()
  resetAction()
  appStore.showSuccess(t('common.success'))
}
function extractCreditActionErrorMessage(error: unknown): string {
  if (!error || typeof error !== 'object') return t('common.error')

  const apiError = error as { message?: unknown; response?: { data?: { message?: unknown } } }
  const responseMessage = apiError.response?.data?.message
  if (typeof responseMessage === 'string' && responseMessage) return responseMessage
  if (!apiError.response && typeof apiError.message === 'string' && apiError.message) return apiError.message
  return t('common.error')
}

/**
 * 首次冲突只刷新详情并等待确认；确认重试仍冲突时停止重放。
 */
async function handleBalanceConflict(
  adjustment: PendingBalanceAdjustment,
  isConflictRetry: boolean,
) {
  if (balanceConflictRefreshing.value) return

  const session = balanceConflictSession.value
  balanceConflictRefreshing.value = true
  try {
    await refreshUserDetail(adjustment.userId, session)
    if (session !== balanceConflictSession.value) return
    if (activeDetailId.value !== adjustment.userId || detail.value?.id !== adjustment.userId) return

    if (isConflictRetry || balanceConflictRetried.value) {
      resetBalanceConflictState()
      appStore.showError(t('admin.credits.balanceConflict.retryExhausted'))
      return
    }

    pendingBalanceAdjustment.value = adjustment
    showBalanceConflictConfirm.value = true
  } catch (error: unknown) {
    if (session !== balanceConflictSession.value) return
    if (isConflictRetry) resetBalanceConflictState()
    appStore.showError(extractCreditActionErrorMessage(error))
  } finally {
    if (session === balanceConflictSession.value) {
      balanceConflictRefreshing.value = false
    }
  }
}
function cancelBalanceConflictRetry() {
  if (!showBalanceConflictConfirm.value) return
  resetBalanceConflictState()
}

async function retryBalanceAdjustment() {
  const adjustment = pendingBalanceAdjustment.value
  if (
    !showBalanceConflictConfirm.value ||
    !adjustment ||
    submitting.value ||
    balanceConflictRefreshing.value
  ) return

  if (activeDetailId.value !== adjustment.userId || detail.value?.id !== adjustment.userId) {
    resetBalanceConflictState()
    return
  }

  showBalanceConflictConfirm.value = false
  balanceConflictRetried.value = true
  await submitBalanceAdjustment(adjustment, true)
}
/**
 * 仅将永久额度 POST 的 409 作为冲突处理，成功后的刷新失败不得重放额度。
 */
async function submitBalanceAdjustment(
  adjustment: PendingBalanceAdjustment,
  isConflictRetry: boolean,
) {
  const currentDetail = detail.value
  if (activeDetailId.value !== adjustment.userId || !currentDetail || currentDetail.id !== adjustment.userId) return
  if (submitting.value) return

  submitting.value = true
  try {
    try {
      await creditsAPI.adjustBalance(adjustment.userId, {
        operation: adjustment.operation,
        amount: adjustment.amount,
        notes: adjustment.notes,
        expected_updated_at: currentDetail.updated_at,
      })
    } catch (error: unknown) {
      if (extractApiErrorStatus(error) === 409) {
        await handleBalanceConflict(adjustment, isConflictRetry)
      } else {
        if (isConflictRetry) resetBalanceConflictState()
        appStore.showError(extractCreditActionErrorMessage(error))
      }
      return
    }

    try {
      await finishSuccessfulAction(adjustment.userId)
    } catch (error: unknown) {
      // 额度调整已成功，关闭表单以防管理员因刷新失败重复提交，并保留成功反馈。
      resetAction()
      appStore.showSuccess(t('common.success'))
      appStore.showError(extractCreditActionErrorMessage(error))
    }
  } finally {
    submitting.value = false
  }
}



async function submitAction() {
  const currentDetail = detail.value
  if (
    !currentDetail ||
    submitting.value ||
    showBalanceConflictConfirm.value ||
    balanceConflictRefreshing.value
  ) return

  if (action.value.startsWith('balance')) {
    resetBalanceConflictState()
    const adjustment: PendingBalanceAdjustment = {
      userId: currentDetail.id,
      operation: action.value === 'balance-add' ? 'add' : 'subtract',
      amount: form.value,
      notes: form.notes,
    }
    await submitBalanceAdjustment(adjustment, false)
    return
  }

  submitting.value = true
  try {
    if (action.value === 'limited-create') {
      await creditsAPI.createLimitedCredit(currentDetail.id, { amount: form.amount, validity_days: form.value, notes: form.notes })
    } else if (selectedGrant.value && ['limited-used','limited-limit'].includes(action.value)) {
      await creditsAPI.adjustLimitedCredit(currentDetail.id, selectedGrant.value.id, { amount_target: action.value === 'limited-used' ? 'used' : 'initial', amount_operation: form.operation, amount: form.value, notes: form.notes, expected_updated_at: selectedGrant.value.updated_at })
    } else if (selectedGrant.value && action.value === 'limited-expiry') {
      await creditsAPI.adjustLimitedCredit(currentDetail.id, selectedGrant.value.id, { expiry_operation: form.operation, validity_days: form.value, notes: form.notes, expected_updated_at: selectedGrant.value.updated_at })
    } else if (selectedGrant.value && action.value === 'limited-reset') {
      await creditsAPI.resetLimitedCredit(currentDetail.id, selectedGrant.value.id, { expected_updated_at: selectedGrant.value.updated_at, notes: form.notes })
    } else if (selectedGrant.value) {
      await creditsAPI.revokeLimitedCredit(currentDetail.id, selectedGrant.value.id, { expected_updated_at: selectedGrant.value.updated_at, notes: form.notes })
    }
    await finishSuccessfulAction(currentDetail.id)
  } catch (error: unknown) {
    appStore.showError(extractCreditActionErrorMessage(error))
  } finally {
    submitting.value = false
  }
}
onMounted(async()=>{await loadUsers();const id=Number(route.query.user);if(id>0){await openUser(id);const requested=String(route.query.action||'');if(['balance-add','balance-subtract'].includes(requested))startAction(requested)}})
</script>
