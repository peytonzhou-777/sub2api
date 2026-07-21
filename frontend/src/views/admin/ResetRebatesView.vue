<template>
  <AppLayout>
    <div class="space-y-5">
      <section class="rounded-lg border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
          <div><h2 class="text-lg font-semibold">重置返利统计</h2><p class="mt-1 text-sm text-gray-500">统计周期采用浏览器时区 {{ browserTimezone }}，区间为 [开始, 结束)，最长 7 天。影子账号的额度和消费完全排除。</p></div>
          <button class="btn btn-primary" :disabled="creating || periodLoading || !form.groupId" @click="createStats"><Icon name="chart" size="sm" />{{ periodLoading ? '正在读取历史…' : creating ? '正在创建…' : '统计额度' }}</button>
        </div>
        <div class="grid gap-4 md:grid-cols-3">
          <div><label class="input-label">指定分组</label><select v-model.number="form.groupId" class="input"><option :value="0">请选择分组</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select></div>
          <div><label class="input-label">开始时间</label><input v-model="form.start" type="datetime-local" class="input" /></div>
          <div><label class="input-label">结束时间</label><input v-model="form.end" type="datetime-local" class="input" /></div>
        </div>
      </section>

      <section v-if="current" class="space-y-4 rounded-lg border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <div class="flex flex-wrap items-center justify-between gap-3"><div><h2 class="text-lg font-semibold">批次 #{{ current.id }} · {{ current.group_name }}</h2><p class="text-sm text-gray-500">{{ localDate(current.period_start) }} — {{ localDate(current.period_end) }}</p><p v-if="current.rebate_reason" class="mt-1 text-sm text-gray-500">返利原因：{{ current.rebate_reason }}</p></div><span class="badge" :class="statusClass(current.status)">{{ statusText(current.status, current) }}</span></div>
        <div v-if="current.status === 'running'" class="space-y-2"><div class="flex justify-between text-sm"><span>正在查询上游配额</span><span>{{ current.progress_completed }} / {{ current.progress_total }}（失败 {{ current.progress_failed }}）</span></div><div class="h-2 overflow-hidden rounded bg-gray-200 dark:bg-dark-700"><div class="h-full bg-primary-500 transition-all" :style="{width: `${progressPercent}%`}" /></div></div>
        <div v-if="current.failure_message" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-950/30 dark:text-amber-300">{{ current.failure_message }}<span v-if="current.failure_code">（{{ current.failure_code }}）</span></div>
        <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-5">
          <Metric label="实际消耗" :value="money(current.actual_amount)" />
          <Metric label="可返利消耗" :value="money(current.refundable_amount)" />
          <Metric label="当前周限使用" :value="percent(current.weekly_usage_percent)" />
          <Metric label="可返利/实际" :value="percent(current.refundable_percent)" />
          <Metric label="建议比例" :value="`${current.suggested_ratio}%`" />
        </div>
        <div v-if="['ready','incomplete','executed'].includes(current.status)" class="flex flex-wrap items-end gap-3">
          <div><label class="input-label">返还比例（1%–80%，整数）</label><input v-model.number="ratio" type="number" min="1" max="80" step="1" class="input w-48" :disabled="current.status === 'executed'" /><p v-if="current.status!=='executed' && ratio>current.suggested_ratio" class="mt-1 text-xs text-amber-600">当前配置高于建议比例，请重点核对逐用户明细。</p></div>
          <div class="min-w-[260px] flex-1"><label class="input-label">返利原因（可选，最多 100 字）</label><input v-model="rebateReason" type="text" maxlength="100" class="input w-full" placeholder="可填写本次返利说明" :disabled="current.status === 'executed'" /></div>
          <button class="btn btn-secondary" @click="loadPreview(1)">预览逐用户明细</button>
          <button class="btn btn-secondary" @click="downloadCSV">导出完整 CSV</button>
          <button v-if="current.status !== 'executed'" class="btn btn-primary" :disabled="!preview" @click="confirming = true">返还额度</button>
        </div>

        <details open class="rounded-lg border border-gray-200 dark:border-dark-700" @toggle="loadAccountsOnce"><summary class="cursor-pointer px-4 py-3 font-medium">账号明细（包含影子账号排除记录）</summary><div class="overflow-x-auto"><table class="min-w-full text-center text-sm"><thead class="bg-gray-50 dark:bg-dark-700"><tr><th class="table-header">账号</th><th class="table-header">类型</th><th class="table-header">周期消费</th><th class="table-header">重置次数</th><th class="table-header">周限使用</th><th class="table-header">统计结论</th></tr></thead><tbody><tr v-for="item in accounts" :key="item.id"><td class="table-cell">{{ item.account_name || '-' }}<p class="text-xs text-gray-500">#{{ item.account_id }}</p></td><td class="table-cell">{{ item.platform }}/{{ item.account_type }}<p v-if="item.is_shadow" class="text-xs text-amber-600">影子账号</p></td><td class="table-cell">{{ money(item.consumed_amount) }}</td><td class="table-cell">{{ item.available_count ?? '-' }}</td><td class="table-cell">{{ item.weekly_used_percent == null ? '-' : percent(item.weekly_used_percent) }}</td><td class="table-cell"><span :class="item.included ? 'text-green-600' : 'text-gray-500'">{{ item.included ? '已纳入' : (item.exclusion_reason || '已排除') }}</span><p v-if="item.error_code" class="text-xs text-red-600">{{ item.error_code }} · {{ item.error_message }}</p></td></tr></tbody></table></div><div class="flex justify-end gap-2 border-t border-gray-200 p-3 dark:border-dark-700"><button class="btn btn-secondary btn-sm" :disabled="accountPage<=1" @click.prevent="loadAccounts(accountPage-1)">上一页</button><span class="px-2 py-1 text-sm">第 {{ accountPage }} 页 / 共 {{ accountTotal }} 条</span><button class="btn btn-secondary btn-sm" :disabled="accountPage*50>=accountTotal" @click.prevent="loadAccounts(accountPage+1)">下一页</button></div></details>

        <div v-if="preview" class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-3"><div><h3 class="font-semibold">逐用户预览</h3><p class="text-sm text-gray-500">预计发放 {{ preview.issued_user_count }} 人，排除 {{ preview.excluded_user_count }} 人，总额 {{ money(preview.total_amount) }}，统一到期 {{ localDate(preview.expected_expires_at) }}</p><p v-if="preview.batch.rebate_reason" class="mt-1 text-sm text-gray-500">返利原因：{{ preview.batch.rebate_reason }}</p></div><div class="flex gap-2"><input v-model="userSearch" class="input w-64" placeholder="搜索用户 ID / 邮箱 / 用户名" @keyup.enter="loadPreview(1)" /><button class="btn btn-secondary" @click="loadPreview(1)">搜索</button></div></div>
          <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700"><table class="min-w-full text-center text-sm"><thead class="bg-gray-50 dark:bg-dark-700"><tr><th class="table-header">用户</th><th class="table-header">真实消耗</th><th class="table-header">比例</th><th class="table-header">发放额</th><th class="table-header">结果</th></tr></thead><tbody><tr v-for="item in preview.users" :key="item.id"><td class="table-cell">{{ item.email || '-' }}<p class="text-xs text-gray-500">#{{ item.user_id }} · {{ item.username || '-' }} · {{ item.user_status }}</p></td><td class="table-cell">{{ money(item.actual_amount) }}</td><td class="table-cell">{{ item.rebate_ratio }}%</td><td class="table-cell">{{ money(item.rebate_amount) }}</td><td class="table-cell">{{ item.issued ? `已发放${item.current_grant_status ? ` · 当前${item.current_grant_status}` : ''}` : (item.exclusion_reason || '预计发放') }}</td></tr></tbody></table></div>
          <div class="flex justify-end gap-2"><button class="btn btn-secondary" :disabled="preview.page <= 1" @click="loadPreview(preview.page - 1)">上一页</button><span class="px-3 py-2 text-sm">第 {{ preview.page }} 页 / 共 {{ preview.total }} 条</span><button class="btn btn-secondary" :disabled="preview.page * preview.page_size >= preview.total" @click="loadPreview(preview.page + 1)">下一页</button></div>
        </div>
      </section>

      <section class="rounded-lg border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <div class="mb-4 flex flex-wrap items-center justify-between gap-3"><h2 class="text-lg font-semibold">历史批次</h2><div class="flex flex-wrap gap-2"><select v-model.number="historyFilters.groupId" class="input w-40"><option :value="0">全部分组</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select><select v-model="historyFilters.status" class="input w-36"><option value="">全部状态</option><option v-for="status in statuses" :key="status" :value="status">{{ statusText(status) }}</option></select><input v-model.number="historyFilters.adminId" type="number" min="1" class="input w-32" placeholder="管理员 ID" /><input v-model="historyFilters.start" type="datetime-local" class="input w-48" title="周期筛选开始" /><input v-model="historyFilters.end" type="datetime-local" class="input w-48" title="周期筛选结束" /><button class="btn btn-secondary" @click="loadHistory(1)">筛选</button></div></div>
        <div class="overflow-x-auto"><table class="min-w-full text-center text-sm"><thead class="bg-gray-50 dark:bg-dark-700"><tr><th class="table-header">批次/分组</th><th class="table-header">统计周期</th><th class="table-header">状态</th><th class="table-header">建议/配置</th><th class="table-header">人数/总额</th><th class="table-header">创建人</th><th class="table-header">操作</th></tr></thead><tbody><tr v-for="item in history" :key="item.id"><td class="table-cell">#{{ item.id }} · {{ item.group_name }}<p v-if="item.rebate_reason" class="mt-1 text-xs text-gray-500">{{ item.rebate_reason }}</p></td><td class="table-cell whitespace-nowrap">{{ localDate(item.period_start) }}<br />{{ localDate(item.period_end) }}</td><td class="table-cell">{{ statusText(item.status, item) }}</td><td class="table-cell">{{ item.suggested_ratio }}% / {{ item.configured_ratio == null ? '-' : `${item.configured_ratio}%` }}</td><td class="table-cell">{{ item.issued_user_count }} / {{ money(item.issued_amount) }}</td><td class="table-cell">{{ item.admin_email || `#${item.admin_id}` }}</td><td class="table-cell"><div class="flex justify-center gap-2"><button class="btn btn-secondary btn-sm" @click="openBatch(item)">查看</button><button v-if="cleanable(item.status)" class="btn btn-danger btn-sm" @click="cleanBatch(item)">清理</button></div></td></tr></tbody></table></div><div class="mt-3 flex justify-end gap-2"><button class="btn btn-secondary" :disabled="historyPage<=1" @click="loadHistory(historyPage-1)">上一页</button><span class="px-3 py-2 text-sm">第 {{ historyPage }} 页 / 共 {{ historyTotal }} 条</span><button class="btn btn-secondary" :disabled="historyPage*20>=historyTotal" @click="loadHistory(historyPage+1)">下一页</button></div>
      </section>
    </div>

    <BaseDialog :show="confirming" title="确认返还额度" @close="confirming = false">
      <div v-if="preview" class="space-y-3 text-sm"><p><strong>{{ current?.group_name }}</strong> · {{ localDate(current!.period_start) }} — {{ localDate(current!.period_end) }}</p><p>快照剩余有效时间：{{ remainingTTL }}</p><p>配置比例 {{ ratio }}%，建议比例 {{ current?.suggested_ratio }}%；发放 {{ preview.issued_user_count }} 人，排除 {{ preview.excluded_user_count }} 人，总额 {{ money(preview.total_amount) }}。</p><p v-if="preview.batch.rebate_reason">返利原因：{{ preview.batch.rebate_reason }}</p><p>统一到期：{{ localDate(preview.expected_expires_at) }}</p><div v-if="current?.status === 'incomplete'" class="rounded-lg bg-amber-50 p-3 font-medium text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">统计不完整：{{ current.progress_failed }} 个账号查询失败，承载实际消费 {{ money(current.failed_account_amount) }}。当前返还依据包含未完成的上游统计，返还比例由管理员承担判断责任。</div><div v-else-if="current && (current.participant_count === 0 || current.suggested_ratio === 0)" class="rounded-lg bg-amber-50 p-3 font-medium text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">当前没有参与账号或建议比例为 0%，返还比例由管理员主动配置。</div><div class="rounded-lg bg-red-50 p-3 text-red-700 dark:bg-red-950/30 dark:text-red-300">发放立即生效且不可整体撤销。单份额度只能在“用户额度”中另行调整或作废。</div><label class="flex items-start gap-2"><input v-model="checked" type="checkbox" class="mt-1" /><span>我已核对逐用户明细与发放总额</span></label></div>
      <template #footer><button class="btn btn-secondary" @click="confirming = false">取消</button><button class="btn btn-primary" :disabled="!checked || executing" @click="execute">{{ executing ? '正在发放…' : '确认发放' }}</button></template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import Metric from '@/components/common/Metric.vue'
import { groupsAPI, resetRebatesAPI } from '@/api/admin'
import type { ResetRebateAccount, ResetRebateBatch, ResetRebatePreview, ResetRebateStatus } from '@/api/admin/resetRebates'
import { useAppStore } from '@/stores/app'
import type { AdminGroup } from '@/types'

const appStore = useAppStore()
const defaultRebateReason = '官方重置！本站返利！'
const groups = ref<AdminGroup[]>([])
const current = ref<ResetRebateBatch | null>(null)
const accounts = ref<ResetRebateAccount[]>([])
const preview = ref<ResetRebatePreview | null>(null)
const history = ref<ResetRebateBatch[]>([])
const creating = ref(false); const executing = ref(false); const confirming = ref(false); const checked = ref(false)
const periodLoading = ref(false)
const ratio = ref(1); const userSearch = ref(''); const rebateReason = ref(defaultRebateReason)
const accountPage = ref(1); const accountTotal = ref(0); const historyPage = ref(1); const historyTotal = ref(0)
const historyFilters = reactive({ groupId: 0, status: '', adminId: undefined as number | undefined, start: '', end: '' })
const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'Local'
const statuses: ResetRebateStatus[] = ['running','ready','incomplete','not_eligible','expired','executed','failed']
let pollTimer: ReturnType<typeof setTimeout> | undefined
let pollingBatchID: number | undefined
let periodRequestID = 0

function localInput(date: Date) { const shifted = new Date(date.getTime() - date.getTimezoneOffset()*60000); return shifted.toISOString().slice(0,16) }
const now = new Date()
const form = reactive({ groupId: 0, start: localInput(new Date(now.getTime()-7*86400000)), end: localInput(now) })
const money = (value: number) => `$${Number(value || 0).toFixed(8).replace(/0+$/,'').replace(/\.$/,'')}`
const percent = (value: number) => `${Number(value || 0).toFixed(2)}%`
const localDate = (value: string) => new Date(value).toLocaleString()
const progressPercent = computed(() => current.value?.progress_total ? Math.min(100, current.value.progress_completed/current.value.progress_total*100) : 0)
const remainingTTL = computed(() => { const end=current.value?.snapshot_expires_at ? new Date(current.value.snapshot_expires_at).getTime() : 0; const seconds=Math.max(0,Math.floor((end-Date.now())/1000)); return `${Math.floor(seconds/60)} 分 ${seconds%60} 秒` })
const statusText = (status: string, batch?: ResetRebateBatch) => status === 'ready' && (batch?.actual_amount ?? 0) > 0 && batch?.participant_count === 0 ? '可强制发放' : ({running:'统计中',ready:'可发放',incomplete:'统计不完整（可发放）',not_eligible:'不可返利',expired:'已过期',executed:'已发放',failed:'统计失败'}[status] || status)
const statusClass = (status: string) => status==='executed'?'badge-success':status==='ready'?'badge-info':status==='running'?'badge-warning':'badge-secondary'
const cleanable = (status: string) => !['running','executed'].includes(status)

// editableRebateReason 只为尚未预览的新批次填入前端默认文案，保留管理员已确认的空原因。
const editableRebateReason = (batch: ResetRebateBatch) => batch.rebate_reason || (batch.configured_ratio == null && batch.status !== 'executed' ? defaultRebateReason : '')

watch(ratio, () => { preview.value = null; checked.value = false })
watch(rebateReason, () => { preview.value = null; checked.value = false })
watch(() => form.groupId, groupID => {
  if (groupID > 0) void syncPeriodStart(groupID)
  else { periodRequestID++; periodLoading.value=false }
})

// syncPeriodStart 根据本组最近成功发放的统计截止时间设置下一周期起点。
async function syncPeriodStart(groupID: number) {
  const requestID=++periodRequestID
  periodLoading.value=true
  try {
    const periodEnd=await resetRebatesAPI.getLatestExecutedPeriodEnd(groupID)
    if(requestID!==periodRequestID||form.groupId!==groupID)return
    const now=new Date()
    const start=periodEnd?new Date(periodEnd):new Date(now.getTime()-7*86400000)
    const latestAllowedEnd=new Date(start.getTime()+7*86400000)
    const end=latestAllowedEnd<now?latestAllowedEnd:now
    form.start=localInput(start)
    form.end=localInput(end)
  } catch (error: any) {
    if(requestID===periodRequestID)appStore.showError(error.response?.data?.message||'读取历史发放周期失败')
  } finally {
    if(requestID===periodRequestID)periodLoading.value=false
  }
}

async function createStats() {
  creating.value = true
  try {
    const batch = await resetRebatesAPI.createResetRebate({ group_id: form.groupId, start: new Date(form.start).toISOString(), end: new Date(form.end).toISOString() })
    current.value = null
    accounts.value = []
    preview.value = null
    rebateReason.value = defaultRebateReason
    await loadHistory(1)
    if (batch.status === 'running') schedulePoll(batch.id, true)
    else await showCompletedBatch(batch)
  }
  catch (error: any) { appStore.showError(error.response?.data?.message || '创建统计失败') }
  finally { creating.value=false }
}

// showCompletedBatch 在任务进入终态后同步详情、账号明细和历史列表。
async function showCompletedBatch(batch: ResetRebateBatch) {
  pollingBatchID = undefined
  current.value = batch
  accounts.value = []
  preview.value = null
  rebateReason.value = editableRebateReason(batch)
  if (['ready','incomplete'].includes(batch.status)) ratio.value=Math.min(80,Math.max(1,batch.suggested_ratio))
  await loadAccounts(1)
  await loadHistory(1)
}

// schedulePoll 独立跟踪任务 ID；新建统计时隐藏运行中详情，历史查看时保留进度详情。
function schedulePoll(batchID: number, hideWhileRunning: boolean) {
  if (pollTimer) clearTimeout(pollTimer)
  pollingBatchID = batchID
  pollTimer=setTimeout(async()=>{
    try {
      const batch=await resetRebatesAPI.getResetRebate(batchID)
      const historyIndex=history.value.findIndex(item=>item.id===batch.id)
      if(historyIndex>=0)history.value[historyIndex]=batch
      if(batch.status==='running'){
        if(!hideWhileRunning&&current.value?.id===batch.id)current.value=batch
        schedulePoll(batchID,hideWhileRunning)
        return
      }
      await showCompletedBatch(batch)
    } catch {
      if(pollingBatchID===batchID)schedulePoll(batchID,hideWhileRunning)
    }
  },1200)
}
async function loadAccountsOnce(){ if(current.value&&accounts.value.length===0)await loadAccounts(1) }
async function loadAccounts(page=1){if(!current.value)return;const r=await resetRebatesAPI.listResetRebateAccounts(current.value.id,page,50);accounts.value=r.items||[];accountTotal.value=r.total||0;accountPage.value=page}
async function loadPreview(page=1){ if(!current.value)return; try{preview.value=await resetRebatesAPI.previewResetRebate(current.value.id,ratio.value,page,50,userSearch.value,rebateReason.value)}catch(error:any){appStore.showError(error.response?.data?.message||'加载预览失败')} }
async function execute(){ if(!current.value||!checked.value||!preview.value)return;executing.value=true;try{current.value=await resetRebatesAPI.executeResetRebate(current.value.id,ratio.value,preview.value.batch.rebate_reason||'');confirming.value=false;checked.value=false;await loadPreview(1);await loadHistory();appStore.showSuccess('重置返利已发放')}catch(error:any){appStore.showError(error.response?.data?.message||'发放失败')}finally{executing.value=false} }
async function downloadCSV(){if(!current.value)return;const blob=await resetRebatesAPI.exportResetRebateUsers(current.value.id,ratio.value);const url=URL.createObjectURL(blob);const a=document.createElement('a');a.href=url;a.download=`reset-rebate-${current.value.id}-users.csv`;a.click();URL.revokeObjectURL(url)}
async function loadHistory(page=1){const params:Record<string,unknown>={};if(historyFilters.groupId)params.group_id=historyFilters.groupId;if(historyFilters.status)params.status=historyFilters.status;if(historyFilters.adminId)params.admin_id=historyFilters.adminId;if(historyFilters.start)params.period_start=new Date(historyFilters.start).toISOString();if(historyFilters.end)params.period_end=new Date(historyFilters.end).toISOString();const r=await resetRebatesAPI.listResetRebates(page,20,params);history.value=r.items||[];historyTotal.value=r.total||0;historyPage.value=page}
// openBatch 切换历史批次后主动重载账号明细，避免已展开的 details 不再触发 toggle。
async function openBatch(item:ResetRebateBatch){if(pollTimer)clearTimeout(pollTimer);pollingBatchID=undefined;current.value=await resetRebatesAPI.getResetRebate(item.id);accounts.value=[];preview.value=null;rebateReason.value=editableRebateReason(current.value);ratio.value=current.value.configured_ratio??Math.min(80,Math.max(1,current.value.suggested_ratio));await loadAccounts(1);if(current.value.status==='running')schedulePoll(current.value.id,false);window.scrollTo({top:0,behavior:'smooth'})}
async function cleanBatch(item:ResetRebateBatch){if(!window.confirm(`确认清理批次 #${item.id}？`))return;await resetRebatesAPI.deleteResetRebate(item.id);if(current.value?.id===item.id)current.value=null;await loadHistory()}
onMounted(async()=>{groups.value=await groupsAPI.getAllIncludingInactive();await loadHistory()})
onBeforeUnmount(()=>{if(pollTimer)clearTimeout(pollTimer)})
</script>
