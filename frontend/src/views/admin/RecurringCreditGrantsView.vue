<template>
  <AppLayout>
    <div class="space-y-5">
      <section class="rounded-lg border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 class="text-lg font-semibold">赠额任务</h2>
            <p class="mt-1 text-sm text-gray-500">立即、每月和每周任务统一向滚动 30 天内有 API 或站内活跃的用户发放限时额度。</p>
          </div>
          <button class="btn btn-primary" @click="openCreate">新建任务</button>
        </div>
        <div class="mb-4 flex flex-wrap gap-2">
          <input v-model="filters.search" class="input w-56" placeholder="搜索任务 ID / 名称" @keyup.enter="loadTasks(1)" />
          <select v-model="filters.status" class="input w-36"><option value="">全部状态</option><option value="active">运行中</option><option value="stopped">已停止</option><option value="completed">已完成</option></select>
          <select v-model="filters.mode" class="input w-36"><option value="">全部模式</option><option value="finite">有限</option><option value="permanent">永久</option></select>
          <select v-model="filters.scheduleType" class="input w-36"><option value="">全部类型</option><option value="immediate">立即执行</option><option value="monthly">月度</option><option value="weekly">周度</option></select>
          <button class="btn btn-secondary" @click="loadTasks(1)">筛选</button>
        </div>
        <div class="overflow-x-auto">
          <table class="min-w-full text-center text-sm">
            <thead class="bg-gray-50 dark:bg-dark-700"><tr><th class="table-header">任务</th><th class="table-header">赠额</th><th class="table-header">计划</th><th class="table-header">剩余/跳过</th><th class="table-header">下一次</th><th class="table-header">状态</th><th class="table-header">操作</th></tr></thead>
            <tbody>
              <tr v-for="task in tasks" :key="task.id">
                <td class="table-cell text-left"><button class="font-medium text-primary-600" @click="openTask(task)">{{ task.name }}</button><p class="text-xs text-gray-500">#{{ task.id }} · v{{ task.version }}</p></td>
                <td class="table-cell">{{ money(task.amount) }}</td>
                <td class="table-cell">{{ task.schedule_description }}<p v-if="task.schedule_type!=='immediate'" class="text-xs text-gray-500">{{ task.timezone }}</p></td>
                <td class="table-cell">{{ task.schedule_type==='immediate' ? '单次' : task.execution_mode === 'permanent' ? '永久' : `${task.remaining_runs} 次` }}<p v-if="task.schedule_type!=='immediate'" class="text-xs text-gray-500">跳过 {{ task.skip_count }} 次</p></td>
                <td class="table-cell whitespace-nowrap">{{ task.next_run_at ? localDate(task.next_run_at) : '-' }}</td>
                <td class="table-cell"><span class="badge" :class="statusClass(task.status)">{{ statusText(task.status) }}</span><p v-if="task.latest_batch_status" class="text-xs text-gray-500">最近：{{ batchStatusText(task.latest_batch_status) }}</p></td>
                <td class="table-cell"><div class="flex flex-wrap justify-center gap-1"><button v-if="task.schedule_type!=='immediate'" class="btn btn-secondary btn-sm" @click="openEdit(task)">编辑</button><button v-if="task.schedule_type!=='immediate' && task.status==='active'" class="btn btn-secondary btn-sm" @click="simpleAction(task,'stop')">停止</button><button v-if="task.schedule_type!=='immediate' && task.status==='stopped'" class="btn btn-secondary btn-sm" @click="previewAction(task,'resume')">恢复</button><button v-if="task.schedule_type!=='immediate' && task.status==='completed'" class="btn btn-secondary btn-sm" @click="openReactivate(task)">重新启用</button><button class="btn btn-secondary btn-sm" @click="openTask(task)">历史</button></div></td>
              </tr>
              <tr v-if="!tasks.length"><td colspan="7" class="table-cell py-8 text-gray-500">暂无赠额任务</td></tr>
            </tbody>
          </table>
        </div>
        <div class="mt-3 flex justify-end gap-2"><button class="btn btn-secondary" :disabled="taskPage<=1" @click="loadTasks(taskPage-1)">上一页</button><span class="px-3 py-2 text-sm">第 {{ taskPage }} 页 / 共 {{ taskTotal }} 条</span><button class="btn btn-secondary" :disabled="taskPage*20>=taskTotal" @click="loadTasks(taskPage+1)">下一页</button></div>
      </section>

      <section v-if="current" class="space-y-4 rounded-lg border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <div class="flex flex-wrap items-start justify-between gap-3"><div><h2 class="text-lg font-semibold">{{ current.name }} · 执行历史</h2><p class="text-sm text-gray-500">#{{ current.id }} · {{ current.schedule_description }}<span v-if="current.schedule_type!=='immediate'"> · {{ current.timezone }}</span></p></div><div class="flex flex-wrap gap-2"><button v-if="current.schedule_type!=='immediate' && current.status==='active' && current.execution_mode==='finite'" class="btn btn-secondary btn-sm" @click="previewAction(current,'make-permanent')">转为永久</button><button v-if="current.schedule_type!=='immediate' && current.status==='active' && current.execution_mode==='permanent'" class="btn btn-secondary btn-sm" @click="makeFinite(current)">转为有限</button><button v-if="current.schedule_type!=='immediate' && current.status==='active'" class="btn btn-secondary btn-sm" @click="setSkip(current)">设置跳过</button><button v-if="current.schedule_type!=='immediate' && current.status==='active' && current.skip_count" class="btn btn-secondary btn-sm" @click="simpleAction(current,'cancel-skip')">取消跳过</button><button class="btn btn-danger btn-sm" @click="removeTask(current)">删除</button></div></div>
        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="min-w-full text-center text-sm">
            <thead class="bg-gray-50 dark:bg-dark-700"><tr><th class="table-header">计划/资格窗口</th><th class="table-header">状态</th><th class="table-header">人数</th><th class="table-header">金额</th><th class="table-header">尝试</th><th class="table-header">操作</th></tr></thead>
            <tbody>
              <tr v-for="batch in batches" :key="batch.id">
                <td class="table-cell whitespace-nowrap">
                  {{ localDate(batch.scheduled_at) }}
                  <p class="text-xs text-gray-500">{{ batch.eligibility_policy==='rolling_30d_activity_v1' ? `${localDate(batch.qualification_start)} — ${localDate(batch.qualification_end)}` : current.schedule_type==='immediate' ? '全部未删除用户' : `${localDate(batch.qualification_start)} — ${localDate(batch.qualification_end)}` }}</p>
                  <p v-if="batch.snapshot_completed_at" class="text-xs text-gray-500">快照完成：{{ localDate(batch.snapshot_completed_at) }}</p>
                </td>
                <td class="table-cell">{{ batchStatusText(batch.status) }}<p v-if="batch.failure_message" class="max-w-xs text-xs text-red-600">{{ batch.failure_code }} · {{ batch.failure_message }}</p></td>
                <td class="table-cell">
                  合格 {{ batch.eligible_user_count }} / 发放 {{ batch.issued_user_count }}
                  <p v-if="batch.eligibility_policy==='rolling_30d_activity_v1'" class="text-xs text-gray-500">API {{ batch.api_active_count }} / 站内 {{ batch.site_active_count }} / 同时 {{ batch.both_active_count }}</p>
                  <p class="text-xs text-gray-500">排除 {{ batch.excluded_user_count }}</p>
                </td>
                <td class="table-cell">{{ money(batch.issued_amount) }}<p class="text-xs text-gray-500">单人 {{ money(batch.amount) }}</p></td>
                <td class="table-cell">{{ batch.attempt_count }}</td>
                <td class="table-cell"><div v-if="['succeeded','empty'].includes(batch.status)" class="flex justify-center gap-1"><button class="btn btn-secondary btn-sm" @click="openBatch(batch)">明细</button><button class="btn btn-secondary btn-sm" @click="downloadCSV(batch)">CSV</button></div></td>
              </tr>
              <tr v-if="!batches.length"><td colspan="6" class="table-cell py-6 text-gray-500">暂无执行历史</td></tr>
            </tbody>
          </table>
        </div>
        <div class="flex justify-end gap-2"><button class="btn btn-secondary" :disabled="batchPage<=1" @click="loadBatches(batchPage-1)">上一页</button><span class="px-3 py-2 text-sm">第 {{ batchPage }} 页 / 共 {{ batchTotal }} 条</span><button class="btn btn-secondary" :disabled="batchPage*20>=batchTotal" @click="loadBatches(batchPage+1)">下一页</button></div>

        <div v-if="selectedBatch" class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div>
              <h3 class="font-semibold">批次 #{{ selectedBatch.id }} 逐用户结果</h3>
              <p v-if="selectedBatch.eligibility_policy==='rolling_30d_activity_v1'" class="mt-1 text-xs text-gray-500">资格窗口：{{ localDate(selectedBatch.qualification_start) }} — {{ localDate(selectedBatch.qualification_end) }}；API {{ selectedBatch.api_active_count }} / 站内 {{ selectedBatch.site_active_count }} / 同时 {{ selectedBatch.both_active_count }} / 去重 {{ selectedBatch.eligible_user_count }}</p>
            </div>
            <div class="flex gap-2"><input v-model="userSearch" class="input w-60" placeholder="用户 ID / 邮箱 / 用户名" @keyup.enter="loadUsers(1)" /><button class="btn btn-secondary" @click="loadUsers(1)">搜索</button></div>
          </div>
          <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
            <table class="min-w-full text-center text-sm">
              <thead class="bg-gray-50 dark:bg-dark-700"><tr><th class="table-header">用户</th><th class="table-header">{{ selectedBatch.eligibility_policy==='rolling_30d_activity_v1' ? '最后 API 活跃' : '实际消耗' }}</th><th class="table-header">{{ selectedBatch.eligibility_policy==='rolling_30d_activity_v1' ? '最后站内活跃' : '净充值' }}</th><th class="table-header">命中原因</th><th class="table-header">发放</th><th class="table-header">结果</th></tr></thead>
              <tbody>
                <tr v-for="item in users" :key="item.id">
                  <td class="table-cell">{{ item.email || '-' }}<p class="text-xs text-gray-500">#{{ item.user_id }} · {{ item.username || '-' }}</p></td>
                  <td v-if="selectedBatch.eligibility_policy==='rolling_30d_activity_v1'" class="table-cell whitespace-nowrap">{{ item.api_last_used_at ? localDate(item.api_last_used_at) : '-' }}</td><td v-else class="table-cell">{{ money(item.actual_cost) }}</td>
                  <td v-if="selectedBatch.eligibility_policy==='rolling_30d_activity_v1'" class="table-cell whitespace-nowrap">{{ item.site_last_active_at ? localDate(item.site_last_active_at) : '-' }}</td><td v-else class="table-cell">{{ money(item.net_recharge) }}</td>
                  <td class="table-cell">{{ reasonText(item.qualification_reason) }}</td>
                  <td class="table-cell">{{ money(item.grant_amount) }}<p v-if="item.grant_id" class="text-xs text-gray-500">额度 #{{ item.grant_id }}</p></td>
                  <td class="table-cell">{{ item.result==='issued' ? '已发放' : item.result==='pending' ? '待发放' : exclusionText(item.exclusion_reason) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="flex justify-end gap-2"><button class="btn btn-secondary" :disabled="userPage<=1" @click="loadUsers(userPage-1)">上一页</button><span class="px-3 py-2 text-sm">第 {{ userPage }} 页 / 共 {{ userTotal }} 条</span><button class="btn btn-secondary" :disabled="userPage*50>=userTotal" @click="loadUsers(userPage+1)">下一页</button></div>
        </div>
      </section>
    </div>

    <BaseDialog :show="formOpen" :title="formTitle" size="lg" @close="formOpen=false">
      <div class="grid gap-4 md:grid-cols-2">
        <div><label class="input-label">任务名称</label><input v-model="form.name" maxlength="100" class="input" /></div>
        <div><label class="input-label">单用户赠额（$0.01–$10,000）</label><input v-model.number="form.amount" type="number" min="0.01" max="10000" step="0.00000001" class="input" /></div>
        <div><label class="input-label">任务类型</label><select v-model="form.schedule_type" class="input"><option value="immediate">立即执行</option><option value="monthly">每月循环</option><option value="weekly">每周循环</option></select></div>
        <div class="md:col-span-2 rounded-lg bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-700 dark:text-gray-300"><strong>发放对象：近 30 天活跃用户</strong><p class="mt-1 text-xs">API 鉴权活跃或站内活跃任一命中即可，实际名单以执行器领取任务时的快照为准。</p></div>
        <div v-if="form.schedule_type==='immediate'"><label class="input-label">有效期（天）</label><input v-model.number="form.validity_days" type="number" min="1" max="36500" step="1" class="input" /><p class="mt-1 text-xs text-gray-500">确认创建后立即生成活跃用户快照并发放，且只执行一次。</p></div>
        <template v-if="form.schedule_type!=='immediate'">
          <div v-if="form.schedule_type==='monthly'"><label class="input-label">每月日期（1–28）</label><input v-model.number="form.day_of_month" type="number" min="1" max="28" class="input" /></div>
          <div v-else><label class="input-label">星期</label><select v-model.number="form.day_of_week" class="input"><option v-for="(day,index) in weekdays" :key="day" :value="index+1">{{ day }}</option></select></div>
          <div><label class="input-label">发放时刻</label><input v-model="form.local_time" type="time" class="input" /></div>
          <div><label class="input-label">IANA 时区</label><input v-model="form.timezone" class="input" placeholder="Asia/Shanghai" /></div>
          <div><label class="input-label">执行模式</label><select v-model="form.execution_mode" class="input" :disabled="formMode==='edit'"><option value="finite">有限次数</option><option value="permanent">永久执行</option></select><p v-if="formMode==='edit'" class="mt-1 text-xs text-gray-500">模式切换请使用独立操作。</p></div>
          <div v-if="form.execution_mode==='finite'"><label class="input-label">剩余执行次数</label><input v-model.number="form.remaining_runs" type="number" min="1" step="1" class="input" /></div>
          <div v-if="formMode==='create'"><label class="input-label">创建状态</label><select v-model="form.initially_active" class="input"><option :value="true">创建并启用</option><option :value="false">保存为已停止</option></select></div>
        </template>
        <div class="md:col-span-2"><label class="input-label">管理员备注（最多 1000 字）</label><textarea v-model="form.admin_notes" maxlength="1000" rows="3" class="input" /></div>
      </div>
      <template #footer><button class="btn btn-secondary" @click="formOpen=false">取消</button><button class="btn btn-primary" :disabled="submitting" @click="previewForm">{{ submitting ? '正在计算…' : '预览并确认' }}</button></template>
    </BaseDialog>

    <BaseDialog :show="confirmOpen" title="确认赠额任务操作" @close="confirmOpen=false">
      <div v-if="preview" class="space-y-3 text-sm">
        <p>单用户金额：<strong>{{ money(preview.amount) }}</strong></p>
        <p v-if="form.schedule_type==='immediate'">执行方式：确认创建后立即执行一次</p>
        <p v-else>下一计划：{{ localDate(preview.next_run_at) }}（{{ preview.next_run_local }}）</p>
        <p>预计到期：{{ localDate(preview.expires_at) }}</p>
        <p>发放对象：近 30 天活跃用户</p>
        <p>资格窗口：[{{ localDate(preview.qualification_start) }}, {{ localDate(preview.qualification_end) }})</p>
        <p>API 活跃 {{ preview.api_active_count }} 人；站内活跃 {{ preview.site_active_count }} 人；两者命中 {{ preview.both_active_count }} 人；去重合格 {{ preview.reference_eligible_count }} 人。</p>
        <p>单批估算总额：<strong>{{ money(preview.estimated_total) }}</strong></p>
        <div v-if="preview.skipped_runs?.length" class="rounded-lg bg-amber-50 p-3 text-amber-800 dark:bg-amber-950/30 dark:text-amber-200"><p class="font-medium">将跳过以下计划时点：</p><p v-for="date in preview.skipped_runs" :key="date">{{ localDate(date) }}</p></div>
        <p class="rounded-lg bg-amber-50 p-3 font-medium text-amber-800 dark:bg-amber-950/30 dark:text-amber-200">{{ preview.disclaimer }}</p>
        <label class="flex items-start gap-2"><input v-model="confirmed" type="checkbox" class="mt-1" /><span>{{ form.schedule_type==='immediate' ? '我已核对发放范围、金额和有效期' : '我已核对计划、资格窗口、有效期和参考成本' }}</span></label>
      </div>
      <template #footer><button class="btn btn-secondary" @click="confirmOpen=false">取消</button><button class="btn btn-primary" :disabled="!confirmed||submitting" @click="commitPending">{{ submitting ? '正在提交…' : '确认提交' }}</button></template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { recurringCreditsAPI } from '@/api/admin'
import type { RecurringCreditBatch, RecurringCreditPreview, RecurringCreditTask, RecurringCreditTaskInput, RecurringCreditUserItem } from '@/api/admin/recurringCredits'
import { useAppStore } from '@/stores/app'

const appStore=useAppStore();const weekdays=['周一','周二','周三','周四','周五','周六','周日']
const tasks=ref<RecurringCreditTask[]>([]);const taskPage=ref(1);const taskTotal=ref(0);const filters=reactive({search:'',status:'',mode:'',scheduleType:''})
const current=ref<RecurringCreditTask|null>(null);const batches=ref<RecurringCreditBatch[]>([]);const batchPage=ref(1);const batchTotal=ref(0)
const selectedBatch=ref<RecurringCreditBatch|null>(null);const users=ref<RecurringCreditUserItem[]>([]);const userPage=ref(1);const userTotal=ref(0);const userSearch=ref('')
const formOpen=ref(false);const confirmOpen=ref(false);const preview=ref<RecurringCreditPreview|null>(null);const confirmed=ref(false);const submitting=ref(false);const formMode=ref<'create'|'edit'|'reactivate'>('create');const editing=ref<RecurringCreditTask|null>(null);const pendingAction=ref('')
const defaultTimezone=appStore.cachedPublicSettings?.server_timezone||''
const form=reactive<RecurringCreditTaskInput>({name:'',admin_notes:'',schedule_type:'immediate',day_of_month:5,day_of_week:1,validity_days:7,local_time:'08:00',timezone:defaultTimezone,amount:10,execution_mode:'finite',remaining_runs:1,initially_active:true})
const formTitle=computed(()=>formMode.value==='create'?'新建赠额任务':formMode.value==='reactivate'?'重新启用赠额任务':'编辑赠额任务')
const money=(value:number)=>`$${Number(value||0).toFixed(8).replace(/0+$/,'').replace(/\.$/,'')}`
const localDate=(value:string)=>new Date(value).toLocaleString()
const statusText=(status:string)=>({active:'运行中',stopped:'已停止',completed:'已完成',deleted:'已删除'}[status]||status)
const statusClass=(status:string)=>status==='active'?'badge-success':status==='completed'?'badge-info':'badge-secondary'
const batchStatusText=(status:string)=>({running:'执行中',succeeded:'成功',empty:'空批次',skipped:'已跳过',missed:'已错过',failed:'失败'}[status]||status)
const reasonText=(reason:string)=>({api_activity:'API 活跃',site_activity:'站内活跃',api_and_site_activity:'API + 站内活跃',all_users:'全部未删除用户',usage:'实际消耗',recharge:'现金充值',usage_and_recharge:'实际消耗 + 现金充值'}[reason]||reason)
const exclusionText=(reason?:string)=>({user_inactive:'用户已停用',user_deleted:'用户已删除',user_state_changed_after_snapshot:'快照后用户状态变化'}[reason||'']||reason||'已排除')

function payload():RecurringCreditTaskInput{const immediate=form.schedule_type==='immediate';return{name:form.name.trim(),admin_notes:form.admin_notes.trim(),schedule_type:form.schedule_type,day_of_month:form.schedule_type==='monthly'?Number(form.day_of_month):undefined,day_of_week:form.schedule_type==='weekly'?Number(form.day_of_week):undefined,validity_days:immediate?Number(form.validity_days):undefined,local_time:immediate?'':form.local_time,timezone:form.timezone.trim(),amount:Number(form.amount),execution_mode:immediate?'finite':form.execution_mode,remaining_runs:immediate?1:form.execution_mode==='finite'?Number(form.remaining_runs):undefined,initially_active:immediate?true:Boolean(form.initially_active)}}
function fillForm(task?:RecurringCreditTask){Object.assign(form,task?{name:task.name,admin_notes:task.admin_notes,schedule_type:task.schedule_type,day_of_month:task.day_of_month??5,day_of_week:task.day_of_week??1,validity_days:task.validity_days??7,local_time:task.local_time,timezone:task.timezone,amount:task.amount,execution_mode:task.execution_mode,remaining_runs:task.remaining_runs??1,initially_active:task.status==='active'}:{name:'',admin_notes:'',schedule_type:'immediate',day_of_month:5,day_of_week:1,validity_days:7,local_time:'08:00',timezone:defaultTimezone,amount:10,execution_mode:'finite',remaining_runs:1,initially_active:true})}
function openCreate(){formMode.value='create';editing.value=null;fillForm();formOpen.value=true}
function openEdit(task:RecurringCreditTask){formMode.value='edit';editing.value=task;fillForm(task);formOpen.value=true}
function openReactivate(task:RecurringCreditTask){formMode.value='reactivate';editing.value=task;fillForm(task);form.initially_active=true;formOpen.value=true}

async function loadTasks(page=1){const params:Record<string,unknown>={};if(filters.search)params.search=filters.search;if(filters.status)params.status=filters.status;if(filters.mode)params.mode=filters.mode;if(filters.scheduleType)params.schedule_type=filters.scheduleType;const result=await recurringCreditsAPI.listRecurringCredits(page,20,params);tasks.value=result.items||[];taskTotal.value=result.total||0;taskPage.value=page;if(current.value){const fresh=tasks.value.find(item=>item.id===current.value?.id);if(fresh)current.value=fresh}}
async function openTask(task:RecurringCreditTask){current.value=await recurringCreditsAPI.getRecurringCredit(task.id);selectedBatch.value=null;users.value=[];await loadBatches(1);window.scrollTo({top:document.body.scrollHeight,behavior:'smooth'})}
async function loadBatches(page=1){if(!current.value)return;const result=await recurringCreditsAPI.listRecurringCreditBatches(current.value.id,page,20);batches.value=result.items||[];batchTotal.value=result.total||0;batchPage.value=page}
async function openBatch(batch:RecurringCreditBatch){selectedBatch.value=batch;userSearch.value='';await loadUsers(1)}
async function loadUsers(page=1){if(!current.value||!selectedBatch.value)return;const result=await recurringCreditsAPI.listRecurringCreditUsers(current.value.id,selectedBatch.value.id,page,50,userSearch.value);users.value=result.items||[];userTotal.value=result.total||0;userPage.value=page}

async function previewForm(){submitting.value=true;try{preview.value=await recurringCreditsAPI.previewRecurringCredit(payload());pendingAction.value=formMode.value;confirmed.value=false;confirmOpen.value=true;formOpen.value=false}catch(error:any){appStore.showError(error.response?.data?.message||error.message||'成本预估失败')}finally{submitting.value=false}}
async function previewAction(task:RecurringCreditTask,action:string,count?:number){editing.value=task;fillForm(task);submitting.value=true;try{preview.value=await recurringCreditsAPI.previewRecurringCredit(payload(),action==='skip'?count||0:0);pendingAction.value=action+(count?`:${count}`:'');confirmed.value=false;confirmOpen.value=true}catch(error:any){appStore.showError(error.response?.data?.message||'预估失败')}finally{submitting.value=false}}
async function commitPending(){if(!confirmed.value)return;submitting.value=true;try{const action=pendingAction.value;let result:RecurringCreditTask;if(action==='create'){result=await recurringCreditsAPI.createRecurringCredit(payload(),crypto.randomUUID())}else if(action==='edit'&&editing.value){result=await recurringCreditsAPI.updateRecurringCredit(editing.value.id,payload(),editing.value.version)}else if(action==='reactivate'&&editing.value){result=await recurringCreditsAPI.recurringCreditAction(editing.value.id,'reactivate',editing.value.version,undefined,payload())}else if(editing.value){const [name,count]=action.split(':');result=await recurringCreditsAPI.recurringCreditAction(editing.value.id,name,editing.value.version,count?Number(count):undefined)}else{return}confirmOpen.value=false;confirmed.value=false;current.value=current.value?.id===result.id?result:current.value;await loadTasks(taskPage.value);if(current.value?.id===result.id)await loadBatches(1);appStore.showSuccess('操作成功')}catch(error:any){appStore.showError(error.response?.data?.message||error.message||'操作失败')}finally{submitting.value=false}}
async function simpleAction(task:RecurringCreditTask,action:string){if(!window.confirm(`确认${action==='stop'?'停止':action==='cancel-skip'?'取消跳过':'执行'}任务「${task.name}」？`))return;try{const result=await recurringCreditsAPI.recurringCreditAction(task.id,action,task.version);if(current.value?.id===result.id)current.value=result;await loadTasks(taskPage.value);appStore.showSuccess('操作成功')}catch(error:any){appStore.showError(error.response?.data?.message||'操作失败')}}
function makeFinite(task:RecurringCreditTask){const raw=window.prompt('请输入新的剩余执行次数（正整数）','1');if(raw==null)return;const count=Number(raw);if(!Number.isInteger(count)||count<1){appStore.showError('剩余次数必须是正整数');return}void recurringCreditsAPI.recurringCreditAction(task.id,'make-finite',task.version,count).then(async result=>{if(current.value?.id===result.id)current.value=result;await loadTasks(taskPage.value);appStore.showSuccess('已转为有限任务')}).catch((error:any)=>appStore.showError(error.response?.data?.message||'操作失败'))}
function setSkip(task:RecurringCreditTask){const raw=window.prompt('跳过接下来的几次（1–100，再次设置会替换旧值）',String(task.skip_count||1));if(raw==null)return;const count=Number(raw);if(!Number.isInteger(count)||count<1||count>100){appStore.showError('跳过次数必须是 1–100 的整数');return}void previewAction(task,'skip',count)}
async function removeTask(task:RecurringCreditTask){if(!window.confirm(`删除任务「${task.name}」后不可恢复；历史和已发额度会保留。确认删除？`))return;try{await recurringCreditsAPI.deleteRecurringCredit(task.id,task.version);if(current.value?.id===task.id)current.value=null;await loadTasks(1);appStore.showSuccess('任务已删除')}catch(error:any){appStore.showError(error.response?.data?.message||'删除失败')}}
async function downloadCSV(batch:RecurringCreditBatch){if(!current.value)return;const blob=await recurringCreditsAPI.exportRecurringCreditUsers(current.value.id,batch.id);const url=URL.createObjectURL(blob);const link=document.createElement('a');link.href=url;link.download=`recurring-credit-${batch.id}-users.csv`;link.click();URL.revokeObjectURL(url)}
onMounted(()=>loadTasks())
</script>
