<template>
  <AppLayout>
    <div class="space-y-5 pb-8">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white">重置返利</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">按账号用量窗口统计并向用户发放限时额度</p>
        </div>
        <div class="grid grid-cols-2 rounded border border-gray-200 p-1 dark:border-dark-600">
          <button type="button" class="h-9 px-4 text-sm font-medium" :class="viewMode === 'create' ? activeSegmentClass : inactiveSegmentClass" @click="viewMode = 'create'">新建返利</button>
          <button type="button" class="h-9 px-4 text-sm font-medium" :class="viewMode === 'history' ? activeSegmentClass : inactiveSegmentClass" @click="openHistory">历史批次</button>
        </div>
      </div>

      <template v-if="viewMode === 'create' && !activeBatch">
        <section class="border-y border-gray-200 bg-white py-4 dark:border-dark-700 dark:bg-dark-800">
          <div class="flex flex-wrap items-center justify-between gap-3 px-4">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">1. 选择统计账号</h2>
              <p class="mt-1 text-sm text-gray-500">已选择 {{ selectedIds.size }} / {{ accounts.length }} 个账号，其中错误 {{ selectedErrorAccounts.length }} 个、风险窗口 {{ selectedRiskCount }} 个</p>
            </div>
            <button class="btn btn-secondary" :disabled="accountsLoading" @click="loadAccounts(true)">
              <Icon name="refresh" size="sm" />刷新账号
            </button>
          </div>

          <div class="mt-4 grid gap-3 border-y border-gray-100 bg-gray-50 px-4 py-3 sm:grid-cols-2 lg:grid-cols-6 dark:border-dark-700 dark:bg-dark-900/40">
            <div class="relative sm:col-span-2">
              <Icon name="search" size="sm" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
              <input v-model="accountFilters.search" class="input pl-9" placeholder="搜索账号名称或 ID" @input="accountPage = 1" />
            </div>
            <select v-model="accountFilters.platform" class="input" @change="accountPage = 1">
              <option value="">全部平台</option>
              <option v-for="platform in platformOptions" :key="platform" :value="platform">{{ platform }}</option>
            </select>
            <select v-model="accountFilters.type" class="input" @change="accountPage = 1">
              <option value="">全部认证类型</option>
              <option v-for="type in typeOptions" :key="type" :value="type">{{ type }}</option>
            </select>
            <select v-model="accountFilters.status" class="input" @change="accountPage = 1">
              <option value="">全部状态</option>
              <option value="active">正常</option>
              <option value="inactive">停用</option>
              <option value="error">错误</option>
            </select>
            <select v-model="accountFilters.runtime" class="input" @change="accountPage = 1">
              <option value="">全部运行状态</option>
              <option value="schedulable">可调度</option>
              <option value="unschedulable">不可调度</option>
            </select>
          </div>

          <div class="flex flex-wrap items-center gap-2 px-4 py-3 text-sm">
            <button class="btn btn-secondary" @click="selectCurrentPage">选择当前页</button>
            <button class="btn btn-secondary" @click="selectAllFiltered">选择全部筛选结果</button>
            <button class="btn btn-secondary" @click="clearSelection">清空选择</button>
            <button class="btn btn-secondary" :disabled="selectedIds.size < 2 || defaultsLoading" @click="openBulkWindowEditor">
              <Icon name="edit" size="sm" />批量设置统计窗口
            </button>
            <span v-if="defaultsLoading" class="text-gray-500">正在读取账号窗口...</span>
          </div>

          <div class="overflow-x-auto border-t border-gray-100 dark:border-dark-700">
            <table class="w-full min-w-[1080px] text-left text-sm">
              <thead class="bg-gray-50 text-gray-500 dark:bg-dark-700 dark:text-gray-300">
                <tr>
                  <th class="w-12 px-4 py-3"><input type="checkbox" :checked="currentPageAllSelected" @change="toggleCurrentPage" /></th>
                  <th class="px-4 py-3">账号</th>
                  <th class="px-4 py-3">平台 / 类型</th>
                  <th class="px-4 py-3">状态</th>
                  <th class="px-4 py-3">统计时间窗口</th>
                  <th class="px-4 py-3">统计比例</th>
                  <th class="px-4 py-3 text-right">操作</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="account in pagedAccounts" :key="account.id" :class="selectedIds.has(account.id) ? 'bg-emerald-50/50 dark:bg-emerald-900/10' : ''">
                  <td class="px-4 py-3"><input type="checkbox" :checked="selectedIds.has(account.id)" @change="toggleAccount(account)" /></td>
                  <td class="px-4 py-3">
                    <p class="font-medium text-gray-900 dark:text-white">{{ account.name }}</p>
                    <p class="text-xs text-gray-500">#{{ account.id }}</p>
                  </td>
                  <td class="px-4 py-3 text-gray-600 dark:text-gray-300">{{ account.platform }} / {{ account.type }}</td>
                  <td class="px-4 py-3"><AccountStatusIndicator :account="account" /></td>
                  <td class="px-4 py-3">
                    <template v-if="selectedIds.has(account.id)">
                      <p class="whitespace-nowrap text-xs text-gray-700 dark:text-gray-200">{{ windowText(account.id) }}</p>
                      <p class="mt-1 text-xs text-gray-500">{{ windowDurationText(account.id) }}</p>
                      <p v-if="draftFor(account.id)?.risk" class="mt-1 text-xs text-amber-700 dark:text-amber-300">{{ riskText(draftFor(account.id)?.risk) }}</p>
                    </template>
                    <span v-else class="text-gray-400">选择后加载</span>
                  </td>
                  <td class="px-4 py-3">
                    <template v-if="selectedIds.has(account.id)">
                      <span class="font-medium">{{ effectiveRatioText(account.id) }}%</span>
                      <span class="ml-1 text-xs text-gray-500">{{ draftFor(account.id)?.ratio_mode === 'manual' ? '手动' : '自动' }}</span>
                    </template>
                    <span v-else class="text-gray-400">-</span>
                  </td>
                  <td class="px-4 py-3 text-right">
                    <button v-if="selectedIds.has(account.id)" class="rounded p-2 text-gray-500 hover:bg-gray-100 hover:text-gray-900 dark:hover:bg-dark-700 dark:hover:text-white" title="修改统计窗口和比例" @click="openAccountEditor(account)">
                      <Icon name="edit" size="sm" />
                    </button>
                  </td>
                </tr>
                <tr v-if="!accountsLoading && pagedAccounts.length === 0"><td colspan="7" class="px-4 py-12 text-center text-gray-500">没有匹配账号</td></tr>
                <tr v-if="accountsLoading"><td colspan="7" class="px-4 py-12 text-center text-gray-500">正在加载账号...</td></tr>
              </tbody>
            </table>
          </div>
          <div class="flex items-center justify-between border-t border-gray-100 px-4 py-3 text-sm dark:border-dark-700">
            <span class="text-gray-500">共 {{ filteredAccounts.length }} 个结果</span>
            <div class="flex items-center gap-2">
              <button class="btn btn-secondary" :disabled="accountPage <= 1" @click="accountPage--">上一页</button>
              <span>{{ accountPage }} / {{ accountPageCount }}</span>
              <button class="btn btn-secondary" :disabled="accountPage >= accountPageCount" @click="accountPage++">下一页</button>
            </div>
          </div>
        </section>

        <section class="border-y border-gray-200 bg-white px-4 py-4 dark:border-dark-700 dark:bg-dark-800">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">2. 配置统计规则</h2>
          <div class="mt-4 flex flex-wrap items-center gap-4">
            <label class="flex items-center gap-2 text-sm font-medium text-gray-700 dark:text-gray-200">
              <input v-model="forceRatioEnabled" type="checkbox" />强制覆盖所有账号统计比例
            </label>
            <label v-if="forceRatioEnabled" class="flex items-center gap-2 text-sm">
              <input v-model="forceRatio" type="number" min="0" max="100" step="0.00000001" class="input w-36" />
              <span>%</span>
            </label>
          </div>
          <p class="mt-2 text-sm text-gray-500">未覆盖时，账号默认统计比例 = (7 天 - 统计窗口时长) / 7 天；可在账号行中单独改为手动比例。</p>
          <div class="mt-5 flex justify-end">
            <button class="btn btn-primary" :disabled="creating || selectedIds.size === 0 || defaultsLoading" @click="requestCreate">
              <Icon name="play" size="sm" />生成统计批次
            </button>
          </div>
        </section>
      </template>

      <template v-if="activeBatch">
        <div class="flex flex-wrap items-center justify-between gap-3 border-y border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-800">
          <div>
            <div class="flex items-center gap-2">
              <h2 class="font-semibold text-gray-900 dark:text-white">批次 #{{ activeBatch.id }}</h2>
              <span :class="statusClass(activeBatch.status)" class="rounded px-2 py-1 text-xs font-medium">{{ statusText(activeBatch) }}</span>
              <span v-if="activeBatch.mechanism_version === 1" class="rounded bg-gray-100 px-2 py-1 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">旧版只读</span>
            </div>
            <p class="mt-1 text-sm text-gray-500">创建于 {{ localDate(activeBatch.created_at) }}，{{ activeBatch.account_count }} 个账号</p>
          </div>
          <div class="flex flex-wrap gap-2">
            <button v-if="activeBatch.mechanism_version === 2" class="btn btn-secondary" @click="downloadExport('users')"><Icon name="download" size="sm" />用户汇总</button>
            <button v-if="activeBatch.mechanism_version === 2" class="btn btn-secondary" @click="downloadExport('user-account-contributions')"><Icon name="download" size="sm" />账号贡献</button>
            <button class="btn btn-secondary" @click="closeBatch">返回</button>
          </div>
        </div>

        <div v-if="['running', 'executing'].includes(activeBatch.status)" class="border-y border-gray-200 bg-white px-4 py-10 text-center dark:border-dark-700 dark:bg-dark-800">
          <Icon name="refresh" class="mx-auto animate-spin text-gray-500" />
          <p class="mt-3 font-medium">{{ activeBatch.status === 'running' ? '正在本地统计账号用量' : '正在后台发放返利' }}</p>
          <p v-if="activeBatch.status === 'running'" class="mt-1 text-sm text-gray-500">{{ activeBatch.progress_completed }} / {{ activeBatch.progress_total }}</p>
          <p v-else class="mt-1 text-sm text-gray-500">可离开本页面，任务将在后台继续</p>
        </div>

        <template v-else>
          <div v-if="activeBatch.mechanism_version === 1" class="border-y border-gray-200 bg-white px-4 py-3 text-sm text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300">
            该批次由旧版按分组机制生成，仅供历史审计，不可预览、执行或重试。
          </div>

          <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <div class="card p-4"><p class="text-xs text-gray-500">原始消耗</p><p class="mt-1 text-lg font-semibold">${{ money(activeBatch.raw_amount) }}</p></div>
            <div class="card p-4"><p class="text-xs text-gray-500">计入统计额度</p><p class="mt-1 text-lg font-semibold">${{ money(activeBatch.weighted_amount) }}</p></div>
            <div class="card p-4"><p class="text-xs text-gray-500">预计发放</p><p class="mt-1 text-lg font-semibold">${{ money(activeBatch.expected_amount) }}</p></div>
            <div class="card p-4"><p class="text-xs text-gray-500">实际成功发放</p><p class="mt-1 text-lg font-semibold">${{ money(activeBatch.successful_amount) }}</p></div>
          </div>

          <section class="border-y border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
            <div class="px-4 py-4"><h3 class="font-semibold text-gray-900 dark:text-white">账号统计快照</h3><p class="mt-1 text-sm text-gray-500">账号状态、窗口和比例均为创建批次时冻结的值</p></div>
            <div class="overflow-x-auto border-t border-gray-100 dark:border-dark-700"><table class="w-full min-w-[980px] text-left text-sm"><thead class="bg-gray-50 text-gray-500 dark:bg-dark-700 dark:text-gray-300"><tr><th class="px-4 py-3">账号</th><th class="px-4 py-3">状态</th><th class="px-4 py-3">统计窗口</th><th class="px-4 py-3">比例模式 / 有效比例</th><th class="px-4 py-3">原始消耗</th><th class="px-4 py-3">计入统计</th></tr></thead><tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="account in batchAccounts" :key="account.account_id"><td class="px-4 py-3"><p class="font-medium">{{ account.account_name }}</p><p class="text-xs text-gray-500">#{{ account.account_id }} · {{ account.platform }} / {{ account.account_type }}</p></td><td class="px-4 py-3"><p>{{ account.account_status }}</p><p v-if="account.account_error_message" class="max-w-[260px] break-words text-xs text-red-600">{{ account.account_error_message }}</p></td><td class="px-4 py-3 text-xs"><p>{{ localDate(account.period_start) }} 至 {{ localDate(account.period_end) }}</p><p v-if="account.window_risk" class="mt-1 text-amber-700">{{ riskText(account.window_risk) }}</p></td><td class="px-4 py-3">{{ account.ratio_mode === 'manual' ? '手动' : '自动' }} / {{ account.effective_stat_ratio }}%</td><td class="px-4 py-3">${{ money(account.raw_amount) }}</td><td class="px-4 py-3">${{ money(account.weighted_amount) }}</td></tr><tr v-if="batchAccounts.length === 0"><td colspan="6" class="px-4 py-8 text-center text-gray-500">暂无账号快照</td></tr></tbody></table></div>
          </section>

          <div v-if="activeBatch.failure_message" class="border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">
            {{ activeBatch.failure_message }}
          </div>

          <section v-if="activeBatch.mechanism_version === 2 && activeBatch.status === 'ready'" class="border-y border-gray-200 bg-white px-4 py-4 dark:border-dark-700 dark:bg-dark-800">
            <div class="flex flex-wrap items-end gap-4">
              <label class="block"><span class="input-label">发放比例</span><span class="flex items-center gap-2"><input v-model.number="payoutRatio" class="input w-32" type="number" min="1" max="100" step="1" /><span>%</span></span></label>
              <label class="min-w-[280px] flex-1"><span class="input-label">发放原因</span><input v-model="rebateReason" class="input" maxlength="100" /></label>
              <button class="btn btn-primary" :disabled="previewing" @click="loadPreview">生成发放预览</button>
            </div>
          </section>

          <section v-if="previewLoaded || ['partial', 'failed', 'executed'].includes(activeBatch.status)" class="border-y border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
            <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-4">
              <div>
                <h3 class="font-semibold text-gray-900 dark:text-white">用户发放明细</h3>
                <p class="mt-1 text-sm text-gray-500">预计 {{ activeBatch.expected_user_count }} 人，成功 {{ activeBatch.successful_user_count }} 人，失败 {{ activeBatch.failed_user_count }} 人，排除 {{ activeBatch.excluded_user_count }} 人</p>
              </div>
              <div class="flex flex-wrap gap-2">
                <button v-if="activeBatch.status === 'ready' && previewLoaded" class="btn btn-primary" @click="showExecuteConfirm = true">确认发放</button>
                <button v-if="canRetry" class="btn btn-primary" :disabled="executing" @click="showRetryConfirm = true"><Icon name="refresh" size="sm" />重试失败用户</button>
                <button v-if="activeBatch.failed_user_count > 0" class="btn btn-secondary" @click="downloadExport('failed-users')"><Icon name="download" size="sm" />失败名单</button>
              </div>
            </div>
            <div class="grid gap-3 border-t border-gray-100 bg-gray-50 px-4 py-3 sm:grid-cols-[minmax(0,1fr)_180px_auto] dark:border-dark-700 dark:bg-dark-900/40">
              <input v-model="userSearch" class="input" placeholder="搜索用户 ID、邮箱或用户名" @keyup.enter="changeUserPage(1)" />
              <select v-model="userResult" class="input" @change="changeUserPage(1)"><option value="">全部结果</option><option value="pending">待发放</option><option value="succeeded">成功</option><option value="failed">失败</option><option value="excluded">排除</option></select>
              <button class="btn btn-secondary" @click="changeUserPage(1)"><Icon name="search" size="sm" />查询</button>
            </div>
            <div class="overflow-x-auto border-t border-gray-100 dark:border-dark-700">
              <table class="w-full min-w-[1050px] text-left text-sm">
                <thead class="bg-gray-50 text-gray-500 dark:bg-dark-700 dark:text-gray-300"><tr><th class="px-4 py-3">用户</th><th class="px-4 py-3">原始 / 统计额度</th><th class="px-4 py-3">预计 / 实际</th><th class="px-4 py-3">结果</th><th class="px-4 py-3">失败原因</th><th class="px-4 py-3 text-right">贡献</th></tr></thead>
                <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                  <template v-for="user in users" :key="user.user_id">
                    <tr>
                      <td class="px-4 py-3"><p class="font-medium">{{ user.email || user.username || `用户 #${user.user_id}` }}</p><p class="text-xs text-gray-500">#{{ user.user_id }}</p></td>
                      <td class="px-4 py-3">${{ money(user.raw_amount) }} / ${{ money(user.weighted_amount) }}</td>
                      <td class="px-4 py-3">${{ money(user.expected_amount) }} / ${{ money(user.actual_issued_amount) }}</td>
                      <td class="px-4 py-3"><span :class="resultClass(user.result)" class="rounded px-2 py-1 text-xs font-medium">{{ resultText(user.result) }}</span></td>
                      <td class="max-w-[300px] break-words px-4 py-3 text-xs text-red-600 dark:text-red-400">{{ user.error_message || user.exclusion_reason || '-' }}</td>
                      <td class="px-4 py-3 text-right"><button v-if="activeBatch.mechanism_version === 2" class="rounded p-2 text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-700" title="查看账号贡献" @click="toggleContributions(user)"><Icon :name="expandedUserId === user.user_id ? 'chevronUp' : 'chevronDown'" size="sm" /></button><span v-else>-</span></td>
                    </tr>
                    <tr v-if="expandedUserId === user.user_id"><td colspan="6" class="bg-gray-50 px-6 py-3 dark:bg-dark-900/40">
                      <div v-if="contributionsLoading" class="text-xs text-gray-500">正在加载贡献明细...</div>
                      <div v-else class="grid gap-2 md:grid-cols-2 xl:grid-cols-3">
                        <div v-for="item in contributions" :key="item.account_id" class="rounded border border-gray-200 bg-white p-3 text-xs dark:border-dark-700 dark:bg-dark-800">
                          <p class="font-medium">{{ item.account_name }} (#{{ item.account_id }})</p><p class="mt-1 text-gray-500">${{ money(item.raw_amount) }} × {{ item.effective_stat_ratio }}% = ${{ money(item.weighted_amount) }}</p>
                        </div>
                        <p v-if="contributions.length === 0" class="text-gray-500">无账号贡献</p>
                      </div>
                    </td></tr>
                  </template>
                  <tr v-if="users.length === 0"><td colspan="6" class="px-4 py-10 text-center text-gray-500">暂无用户明细</td></tr>
                </tbody>
              </table>
            </div>
            <div class="flex items-center justify-between border-t border-gray-100 px-4 py-3 text-sm dark:border-dark-700"><span class="text-gray-500">共 {{ userTotal }} 人</span><div class="flex gap-2"><button class="btn btn-secondary" :disabled="userPage <= 1" @click="changeUserPage(userPage - 1)">上一页</button><button class="btn btn-secondary" :disabled="userPage * userPageSize >= userTotal" @click="changeUserPage(userPage + 1)">下一页</button></div></div>
          </section>
        </template>
      </template>

      <section v-if="viewMode === 'history' && !activeBatch" class="border-y border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
        <div class="flex flex-wrap items-center justify-between gap-3 px-4 py-4"><div><h2 class="font-semibold text-gray-900 dark:text-white">历史批次</h2><p class="mt-1 text-sm text-gray-500">新版批次可查看明细，旧版批次仅供审计</p></div><button class="btn btn-secondary" :disabled="historyLoading" @click="loadHistory"><Icon name="refresh" size="sm" />刷新</button></div>
        <div class="grid gap-3 border-t border-gray-100 bg-gray-50 px-4 py-3 sm:grid-cols-2 lg:grid-cols-6 dark:border-dark-700 dark:bg-dark-900/40">
          <input v-model="historyFilters.account" class="input" placeholder="账号 ID 或名称" @keyup.enter="applyHistoryFilters" />
          <select v-model="historyFilters.status" class="input" @change="applyHistoryFilters"><option value="">全部状态</option><option v-for="status in historyStatuses" :key="status" :value="status">{{ statusText({ status }) }}</option></select>
          <input v-model="historyFilters.admin_id" class="input" type="number" min="1" placeholder="创建管理员 ID" @keyup.enter="applyHistoryFilters" />
          <input v-model="historyFilters.executed_admin_id" class="input" type="number" min="1" placeholder="执行管理员 ID" @keyup.enter="applyHistoryFilters" />
          <input v-model="historyFilters.created_start" class="input" type="datetime-local" title="创建开始时间" @change="applyHistoryFilters" />
          <input v-model="historyFilters.created_end" class="input" type="datetime-local" title="创建结束时间" @change="applyHistoryFilters" />
        </div>
        <div class="overflow-x-auto border-t border-gray-100 dark:border-dark-700">
          <table class="w-full min-w-[1120px] text-left text-sm">
            <thead class="bg-gray-50 text-gray-500 dark:bg-dark-700 dark:text-gray-300"><tr><th class="px-4 py-3">批次</th><th class="px-4 py-3">机制 / 状态</th><th class="px-4 py-3">账号 / 用户</th><th class="px-4 py-3">时间范围</th><th class="px-4 py-3">预计 / 实际</th><th class="px-4 py-3">管理员审计</th><th class="px-4 py-3 text-right">操作</th></tr></thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="batch in history" :key="batch.id">
                <td class="px-4 py-3 font-medium">#{{ batch.id }}</td>
                <td class="px-4 py-3"><p>v{{ batch.mechanism_version }}</p><span :class="statusClass(batch.status)" class="mt-1 inline-flex rounded px-2 py-1 text-xs font-medium">{{ statusText(batch) }}</span></td>
                <td class="px-4 py-3">{{ batch.account_count }} / {{ batch.expected_user_count }}<p class="text-xs text-gray-500">成功 {{ batch.successful_user_count }} · 排除 {{ batch.excluded_user_count }} · 失败 {{ batch.failed_user_count }}</p></td>
                <td class="px-4 py-3 text-xs">{{ batch.period_start ? localDate(batch.period_start) : '-' }}<br />至 {{ batch.period_end ? localDate(batch.period_end) : '-' }}</td>
                <td class="px-4 py-3">${{ money(batch.expected_amount) }} / ${{ money(batch.successful_amount) }}<p class="text-xs text-gray-500">发放比例 {{ batch.payout_ratio ?? '-' }}%</p></td>
                <td class="px-4 py-3 text-xs"><p>创建：{{ batch.admin_email || `#${batch.admin_id}` }}</p><p>执行：{{ batch.executed_by_admin_email || (batch.executed_by_admin_id ? `#${batch.executed_by_admin_id}` : '-') }}</p><p>重试：{{ batch.last_retry_at ? localDate(batch.last_retry_at) : '-' }}</p><p class="text-gray-500">{{ localDate(batch.created_at) }}</p></td>
                <td class="px-4 py-3"><div class="flex justify-end gap-2"><button class="btn btn-secondary" @click="openBatch(batch.id)">查看</button><button v-if="canDeleteBatch(batch)" class="rounded p-2 text-red-600 hover:bg-red-50 dark:hover:bg-red-900/20" title="删除批次" @click="deletingBatch = batch"><Icon name="trash" size="sm" /></button></div></td>
              </tr>
              <tr v-if="!historyLoading && history.length === 0"><td colspan="7" class="px-4 py-12 text-center text-gray-500">暂无历史批次</td></tr>
            </tbody>
          </table>
        </div>
        <div class="flex items-center justify-between border-t border-gray-100 px-4 py-3 text-sm dark:border-dark-700"><span class="text-gray-500">共 {{ historyTotal }} 个批次</span><div class="flex gap-2"><button class="btn btn-secondary" :disabled="historyPage <= 1" @click="changeHistoryPage(historyPage - 1)">上一页</button><button class="btn btn-secondary" :disabled="historyPage * historyPageSize >= historyTotal" @click="changeHistoryPage(historyPage + 1)">下一页</button></div></div>
      </section>
    </div>

    <BaseDialog :show="showBulkWindowEditor" title="批量设置统计窗口" width="normal" @close="showBulkWindowEditor = false">
      <div class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-gray-300">本次将修改已选择的 {{ selectedIds.size }} 个账号。账号原有的统计比例模式和手动比例保持不变。</p>
        <p v-if="!bulkWindowDraft.period_start && !bulkWindowDraft.period_end" class="rounded border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300">所选账号当前窗口不一致，请明确设置统一的开始和结束时间。</p>
        <label class="block"><span class="input-label">开始时间</span><input v-model="bulkWindowDraft.period_start" type="datetime-local" class="input" /></label>
        <label class="block"><span class="input-label">结束时间</span><input v-model="bulkWindowDraft.period_end" type="datetime-local" class="input" /></label>
        <p class="text-sm text-gray-500">窗口时长：{{ bulkWindowDuration }}</p>
      </div>
      <template #footer><button class="btn btn-secondary" @click="showBulkWindowEditor = false">取消</button><button class="btn btn-primary" @click="saveBulkWindowEditor">应用到所选账号</button></template>
    </BaseDialog>

    <BaseDialog :show="Boolean(editingAccount)" title="修改账号统计设置" width="normal" @close="editingAccount = null">
      <div v-if="editingAccount && editDraft" class="space-y-4">
        <div><p class="font-medium">{{ editingAccount.name }}</p><p class="text-xs text-gray-500">#{{ editingAccount.id }} · {{ editingAccount.status }}</p></div>
        <label class="block"><span class="input-label">开始时间</span><input v-model="editDraft.period_start" type="datetime-local" class="input" /></label>
        <label class="block"><span class="input-label">结束时间</span><input v-model="editDraft.period_end" type="datetime-local" class="input" /></label>
        <p class="text-sm text-gray-500">窗口时长：{{ editWindowDuration }}</p>
        <div><span class="input-label">统计比例</span><div class="grid grid-cols-2 rounded border border-gray-200 p-1 dark:border-dark-600"><button type="button" class="h-9 text-sm" :class="editDraft.ratio_mode === 'auto' ? activeSegmentClass : inactiveSegmentClass" @click="editDraft.ratio_mode = 'auto'">自动计算</button><button type="button" class="h-9 text-sm" :class="editDraft.ratio_mode === 'manual' ? activeSegmentClass : inactiveSegmentClass" @click="editDraft.ratio_mode = 'manual'">手动设置</button></div></div>
        <label v-if="editDraft.ratio_mode === 'manual'" class="block"><span class="input-label">手动统计比例（%）</span><input v-model="editDraft.manual_ratio" type="number" min="0" max="100" step="0.00000001" class="input" /></label>
        <p v-else class="text-sm text-gray-500">保存后由服务端根据新窗口时长重新计算默认比例。</p>
      </div>
      <template #footer><button class="btn btn-secondary" @click="editingAccount = null">取消</button><button class="btn btn-primary" @click="saveAccountEditor">保存</button></template>
    </BaseDialog>

    <BaseDialog :show="showCreateConfirm" title="确认生成统计批次" width="normal" @close="showCreateConfirm = false">
      <div class="space-y-4 text-sm"><p>本次将统计 {{ selectedIds.size }} 个账号。账号窗口、统计比例和账号快照在创建后不可修改。</p><div v-if="selectedRiskCount > 0" class="rounded border border-amber-200 bg-amber-50 p-3 text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300">其中 {{ selectedRiskCount }} 个账号使用风险默认窗口，请确认已经人工核对。</div><div class="rounded border border-amber-200 bg-amber-50 p-3 text-amber-800 dark:border-amber-800 dark:bg-amber-900/20 dark:text-amber-300">系统不提供周期防重。重复创建和重复发放风险由管理员负责。</div><label class="flex items-start gap-2"><input v-model="createResponsibilityConfirmed" class="mt-1" type="checkbox" /><span>我已核对账号、时间窗口和比例，并确认自行负责周期防重。</span></label></div>
      <template #footer><button class="btn btn-secondary" @click="showCreateConfirm = false">取消</button><button class="btn btn-primary" :disabled="!createResponsibilityConfirmed" @click="continueCreate">继续</button></template>
    </BaseDialog>

    <BaseDialog :show="showErrorAccountConfirm" title="确认选择错误状态账号" width="normal" @close="showErrorAccountConfirm = false">
      <div class="space-y-4 text-sm"><p>以下 {{ selectedErrorAccounts.length }} 个错误状态账号仍将进入统计：</p><div class="max-h-56 overflow-y-auto rounded border border-red-200 bg-red-50 p-3 dark:border-red-800 dark:bg-red-900/20"><p v-for="account in selectedErrorAccounts" :key="account.id" class="py-1 text-red-800 dark:text-red-300">{{ account.name }} (#{{ account.id }})：{{ account.error_message || '未提供错误原因' }}</p></div><label class="flex items-start gap-2"><input v-model="errorAccountsConfirmed" class="mt-1" type="checkbox" /><span>我确认仍要统计以上错误账号。</span></label></div>
      <template #footer><button class="btn btn-secondary" @click="showErrorAccountConfirm = false">取消</button><button class="btn btn-primary" :disabled="!errorAccountsConfirmed || creating" @click="confirmErrorAccounts">继续</button></template>
    </BaseDialog>

    <BaseDialog :show="showExecuteConfirm" title="确认发放返利" width="normal" @close="showExecuteConfirm = false">
      <div class="space-y-4 text-sm"><div class="rounded border border-red-200 bg-red-50 p-3 text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300">发放后不可撤销。单个用户失败会被跳过，其余用户继续发放。</div><p>预览版本 {{ activeBatch?.preview_version }}，预计向 {{ activeBatch?.expected_user_count }} 个用户发放 ${{ money(activeBatch?.expected_amount || '0') }}。</p><label class="flex items-start gap-2"><input v-model="executeConfirmed" class="mt-1" type="checkbox" /><span>我已复核预览金额并确认执行。</span></label></div>
      <template #footer><button class="btn btn-secondary" @click="showExecuteConfirm = false">取消</button><button class="btn btn-primary" :disabled="!executeConfirmed || executing" @click="executeBatch">确认发放</button></template>
    </BaseDialog>

    <BaseDialog :show="showRetryConfirm" title="重试失败用户" width="narrow" @close="showRetryConfirm = false"><p class="text-sm">只重试当前仍为失败状态的 {{ activeBatch?.failed_user_count }} 个用户；已成功用户不会重复发放。</p><template #footer><button class="btn btn-secondary" @click="showRetryConfirm = false">取消</button><button class="btn btn-primary" :disabled="executing" @click="retryFailedUsers">确认重试</button></template></BaseDialog>

    <BaseDialog :show="Boolean(deletingBatch)" title="删除返利批次" width="narrow" @close="deletingBatch = null"><p class="text-sm">确认删除批次 #{{ deletingBatch?.id }}？仅未成功发放过额度的非运行批次允许删除。</p><template #footer><button class="btn btn-secondary" @click="deletingBatch = null">取消</button><button class="btn btn-danger" @click="deleteBatch">删除</button></template></BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { saveAs } from 'file-saver'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AccountStatusIndicator from '@/components/account/AccountStatusIndicator.vue'
import Icon from '@/components/icons/Icon.vue'
import { accountsAPI, resetRebatesAPI } from '@/api/admin'
import type { Account } from '@/types'
import type { ResetRebateAccount, ResetRebateAccountDraft, ResetRebateBatch, ResetRebateContribution, ResetRebateUser, ResetRebateWindowDefault } from '@/api/admin/resetRebates'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode } from '@/utils/apiError'

type EditableDraft = ResetRebateAccountDraft & Pick<ResetRebateWindowDefault, 'auto_stat_ratio' | 'window_source' | 'risk'>

const appStore = useAppStore()
const viewMode = ref<'create' | 'history'>('create')
const accounts = ref<Account[]>([])
const accountsLoading = ref(false)
const defaultsLoading = ref(false)
const selectedIds = ref(new Set<number>())
const drafts = reactive(new Map<number, EditableDraft>())
const accountFilters = reactive({ search: '', platform: '', type: '', status: '', runtime: '' })
const accountPage = ref(1)
const accountPageSize = 20
const forceRatioEnabled = ref(false)
const forceRatio = ref('100')
const creating = ref(false)
const activeBatch = ref<ResetRebateBatch | null>(null)
const batchAccounts = ref<ResetRebateAccount[]>([])
const previewLoaded = ref(false)
const previewing = ref(false)
const payoutRatio = ref(90)
const rebateReason = ref('官方重置！按账号重置天数返还消耗额度！')
const users = ref<ResetRebateUser[]>([])
const userTotal = ref(0)
const userPage = ref(1)
const userPageSize = 50
const userSearch = ref('')
const userResult = ref('')
const executing = ref(false)
const expandedUserId = ref<number | null>(null)
const contributions = ref<ResetRebateContribution[]>([])
const contributionsLoading = ref(false)
const history = ref<ResetRebateBatch[]>([])
const historyLoading = ref(false)
const historyTotal = ref(0)
const historyPage = ref(1)
const historyPageSize = 20
const historyFilters = reactive({ account: '', status: '', admin_id: '', executed_admin_id: '', created_start: '', created_end: '' })
const historyStatuses = ['running', 'executing', 'ready', 'not_eligible', 'partial', 'failed', 'executed', 'incomplete', 'expired']
const editingAccount = ref<Account | null>(null)
const editDraft = ref<EditableDraft | null>(null)
const showBulkWindowEditor = ref(false)
const bulkWindowDraft = reactive({ period_start: '', period_end: '' })
const showCreateConfirm = ref(false)
const createResponsibilityConfirmed = ref(false)
const showErrorAccountConfirm = ref(false)
const errorAccountsConfirmed = ref(false)
const showExecuteConfirm = ref(false)
const executeConfirmed = ref(false)
const showRetryConfirm = ref(false)
const deletingBatch = ref<ResetRebateBatch | null>(null)
let pollTimer: ReturnType<typeof setTimeout> | null = null

const activeSegmentClass = 'bg-gray-900 text-white dark:bg-white dark:text-gray-900'
const inactiveSegmentClass = 'text-gray-600 hover:bg-gray-100 dark:text-gray-300 dark:hover:bg-dark-700'

const filteredAccounts = computed(() => {
  const search = accountFilters.search.trim().toLowerCase()
  return accounts.value.filter((account) => {
    if (accountFilters.platform && account.platform !== accountFilters.platform) return false
    if (accountFilters.type && account.type !== accountFilters.type) return false
    if (accountFilters.status && account.status !== accountFilters.status) return false
    if (accountFilters.runtime === 'schedulable' && !account.schedulable) return false
    if (accountFilters.runtime === 'unschedulable' && account.schedulable) return false
    return !search || account.name.toLowerCase().includes(search) || String(account.id).includes(search)
  })
})
const accountPageCount = computed(() => Math.max(1, Math.ceil(filteredAccounts.value.length / accountPageSize)))
const pagedAccounts = computed(() => filteredAccounts.value.slice((accountPage.value - 1) * accountPageSize, accountPage.value * accountPageSize))
const platformOptions = computed(() => [...new Set(accounts.value.map((item) => item.platform))].sort())
const typeOptions = computed(() => [...new Set(accounts.value.map((item) => item.type))].sort())
const currentPageAllSelected = computed(() => pagedAccounts.value.length > 0 && pagedAccounts.value.every((item) => selectedIds.value.has(item.id)))
const selectedErrorAccounts = computed(() => accounts.value.filter((item) => selectedIds.value.has(item.id) && item.status === 'error'))
const selectedRiskCount = computed(() => [...selectedIds.value].filter((id) => Boolean(drafts.get(id)?.risk)).length)
const canRetry = computed(() => Boolean(activeBatch.value && activeBatch.value.failed_user_count > 0 && (activeBatch.value.status === 'partial' || (activeBatch.value.status === 'failed' && activeBatch.value.failure_stage === 'execution'))))
const editWindowDuration = computed(() => editDraft.value ? durationText(editDraft.value.period_start, editDraft.value.period_end) : '-')
const bulkWindowDuration = computed(() => bulkWindowDraft.period_start && bulkWindowDraft.period_end ? durationText(bulkWindowDraft.period_start, bulkWindowDraft.period_end) : '-')

function errorMessage(error: unknown): string {
  if (typeof error === 'object' && error && 'message' in error) return String((error as { message?: unknown }).message || '操作失败')
  return '操作失败'
}

async function loadAccounts(preserveState = false) {
  accountsLoading.value = true
  try {
    const previousSelection = new Set(selectedIds.value)
    const first = await accountsAPI.list(1, 100)
    const items = [...first.items]
    const pages = Math.ceil(first.total / 100)
    for (let page = 2; page <= pages; page++) {
      const response = await accountsAPI.list(page, 100)
      items.push(...response.items)
    }
    accounts.value = items
    if (preserveState) {
      const availableIds = new Set(items.map((item) => item.id))
      selectedIds.value = new Set([...previousSelection].filter((id) => availableIds.has(id)))
      for (const id of drafts.keys()) {
        if (!availableIds.has(id)) drafts.delete(id)
      }
      const removedCount = previousSelection.size - selectedIds.value.size
      if (removedCount > 0) appStore.showWarning(`${removedCount} 个已选账号不再可用，已从本次统计中移除`)
      errorAccountsConfirmed.value = false
    } else {
      selectedIds.value = new Set(items.filter((item) => item.type === 'oauth' && item.status !== 'error').map((item) => item.id))
      drafts.clear()
    }
    await ensureDefaults([...selectedIds.value], preserveState)
  } catch (error) {
    appStore.showError(errorMessage(error))
  } finally {
    accountsLoading.value = false
  }
}

async function ensureDefaults(ids: number[], refreshUnmodified = false) {
  const targets = ids.filter((id) => !drafts.has(id) || (refreshUnmodified && !drafts.get(id)?.window_modified))
  const missing = targets.filter((id) => !drafts.has(id))
  if (targets.length === 0) return
  defaultsLoading.value = true
  try {
    const defaults = await resetRebatesAPI.accountWindowDefaults(targets)
    for (const item of defaults) {
      const previous = drafts.get(item.account_id)
      const nextDraft: EditableDraft = {
        account_id: item.account_id,
        period_start: item.period_start,
        period_end: item.period_end,
        ratio_mode: 'auto',
        default_window_version: item.window_version,
        window_modified: false,
        auto_stat_ratio: item.auto_stat_ratio,
        window_source: item.window_source,
        risk: item.risk
      }
      if (previous) {
        nextDraft.ratio_mode = previous.ratio_mode
        nextDraft.manual_ratio = previous.manual_ratio
      }
      drafts.set(item.account_id, nextDraft)
    }
  } catch (error) {
    for (const id of missing) selectedIds.value.delete(id)
    selectedIds.value = new Set(selectedIds.value)
    appStore.showError(errorMessage(error))
  } finally {
    defaultsLoading.value = false
  }
}

async function toggleAccount(account: Account) {
  const next = new Set(selectedIds.value)
  if (next.has(account.id)) next.delete(account.id)
  else next.add(account.id)
  selectedIds.value = next
  if (next.has(account.id)) await ensureDefaults([account.id])
}

async function selectAccounts(items: Account[]) {
  const next = new Set(selectedIds.value)
  items.forEach((item) => next.add(item.id))
  selectedIds.value = next
  await ensureDefaults(items.map((item) => item.id))
}
const selectCurrentPage = () => selectAccounts(pagedAccounts.value)
const selectAllFiltered = () => selectAccounts(filteredAccounts.value)
function clearSelection() { selectedIds.value = new Set() }
function toggleCurrentPage() {
  if (!currentPageAllSelected.value) void selectCurrentPage()
  else {
    const next = new Set(selectedIds.value)
    pagedAccounts.value.forEach((item) => next.delete(item.id))
    selectedIds.value = next
  }
}

function draftFor(id: number) { return drafts.get(id) }
function localDate(value: string) { return value ? new Date(value).toLocaleString() : '-' }
function localInput(value: string) {
  const date = new Date(value)
  const offset = date.getTimezoneOffset() * 60000
  return new Date(date.getTime() - offset).toISOString().slice(0, 16)
}
function inputToISO(value: string) { return new Date(value).toISOString() }
function windowText(id: number) {
  const draft = drafts.get(id)
  return draft ? `${localDate(draft.period_start)} 至 ${localDate(draft.period_end)}` : '正在加载...'
}
function durationText(startValue: string, endValue: string) {
  const duration = new Date(endValue).getTime() - new Date(startValue).getTime()
  if (!Number.isFinite(duration) || duration <= 0) return '无效窗口'
  const hours = duration / 3600000
  const days = Math.floor(hours / 24)
  const remainingHours = Math.round((hours - days * 24) * 100) / 100
  return days > 0 ? `${days} 天 ${remainingHours} 小时` : `${remainingHours} 小时`
}
function windowDurationText(id: number) {
  const draft = drafts.get(id)
  return draft ? `时长 ${durationText(draft.period_start, draft.period_end)}` : '-'
}
function riskText(risk?: string) {
  const labels: Record<string, string> = { no_history: '无窗口历史，需人工核对', single_history: '仅有一条窗口历史，结束时间取服务器当前时间', missing_history: '缺少历史窗口，已使用保守默认值', insufficient_history: '历史窗口不足，默认时间可能不完整', error_account: '错误状态账号' }
  return risk ? labels[risk] || risk : ''
}
function effectiveRatioText(id: number) {
  if (forceRatioEnabled.value) return forceRatio.value
  const draft = drafts.get(id)
  return draft?.ratio_mode === 'manual' ? draft.manual_ratio || '0' : draft?.auto_stat_ratio || '0'
}

// applyWindowToDraft 统一更新单账号和批量编辑后的窗口派生字段。
function applyWindowToDraft(draft: EditableDraft, periodStart: string, periodEnd: string): EditableDraft {
  const durationMs = new Date(periodEnd).getTime() - new Date(periodStart).getTime()
  const autoRatio = Math.max(0, Math.min(100, ((7 * 86400000 - durationMs) / (7 * 86400000)) * 100))
  const windowChanged = draft.period_start !== periodStart || draft.period_end !== periodEnd
  return {
    ...draft,
    period_start: periodStart,
    period_end: periodEnd,
    auto_stat_ratio: autoRatio.toFixed(8),
    window_source: windowChanged ? 'manual' : draft.window_source,
    risk: windowChanged ? '' : draft.risk,
    window_modified: draft.window_modified || windowChanged
  }
}

function openBulkWindowEditor() {
  if (selectedIds.value.size < 2) return
  const selectedDrafts = [...selectedIds.value].map((id) => drafts.get(id))
  if (selectedDrafts.some((draft) => !draft)) {
    appStore.showError('仍有账号未加载默认窗口')
    return
  }
  const first = selectedDrafts[0]!
  const haveSameWindow = selectedDrafts.every((draft) => draft?.period_start === first.period_start && draft.period_end === first.period_end)
  bulkWindowDraft.period_start = haveSameWindow ? localInput(first.period_start) : ''
  bulkWindowDraft.period_end = haveSameWindow ? localInput(first.period_end) : ''
  showBulkWindowEditor.value = true
}

function saveBulkWindowEditor() {
  const start = new Date(bulkWindowDraft.period_start)
  const end = new Date(bulkWindowDraft.period_end)
  if (!Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || end <= start) {
    appStore.showError('结束时间必须晚于开始时间')
    return
  }
  const periodStart = inputToISO(bulkWindowDraft.period_start)
  const periodEnd = inputToISO(bulkWindowDraft.period_end)
  for (const id of selectedIds.value) {
    const draft = drafts.get(id)
    if (draft) drafts.set(id, applyWindowToDraft(draft, periodStart, periodEnd))
  }
  showBulkWindowEditor.value = false
  appStore.showSuccess(`已为 ${selectedIds.value.size} 个账号设置统一统计窗口`)
}

function openAccountEditor(account: Account) {
  const draft = drafts.get(account.id)
  if (!draft) return
  editingAccount.value = account
  editDraft.value = { ...draft, period_start: localInput(draft.period_start), period_end: localInput(draft.period_end) }
}
function saveAccountEditor() {
  if (!editingAccount.value || !editDraft.value) return
  const start = new Date(editDraft.value.period_start)
  const end = new Date(editDraft.value.period_end)
  if (!Number.isFinite(start.getTime()) || !Number.isFinite(end.getTime()) || end <= start) {
    appStore.showError('结束时间必须晚于开始时间')
    return
  }
  const manual = Number(editDraft.value.manual_ratio || 0)
  if (editDraft.value.ratio_mode === 'manual' && (!Number.isFinite(manual) || manual < 0 || manual > 100)) {
    appStore.showError('统计比例必须在 0% 到 100% 之间')
    return
  }
  const previous = drafts.get(editingAccount.value.id)
  if (!previous) return
  const startChanged = !previous || editDraft.value.period_start !== localInput(previous.period_start)
  const endChanged = !previous || editDraft.value.period_end !== localInput(previous.period_end)
  const periodStart = startChanged ? inputToISO(editDraft.value.period_start) : previous.period_start
  const periodEnd = endChanged ? inputToISO(editDraft.value.period_end) : previous.period_end
  drafts.set(editingAccount.value.id, {
    ...applyWindowToDraft(previous, periodStart, periodEnd),
    ratio_mode: editDraft.value.ratio_mode,
    manual_ratio: editDraft.value.manual_ratio
  })
  editingAccount.value = null
}

function requestCreate() {
  const ratio = Number(forceRatio.value)
  if (forceRatioEnabled.value && (!Number.isFinite(ratio) || ratio < 0 || ratio > 100)) {
    appStore.showError('强制统计比例必须在 0% 到 100% 之间')
    return
  }
  if ([...selectedIds.value].some((id) => !drafts.has(id))) {
    appStore.showError('仍有账号未加载默认窗口')
    return
  }
  if (selectedErrorAccounts.value.length > 0) {
    errorAccountsConfirmed.value = false
    showErrorAccountConfirm.value = true
  } else {
    createResponsibilityConfirmed.value = false
    showCreateConfirm.value = true
  }
}
function confirmErrorAccounts() {
  showErrorAccountConfirm.value = false
  createResponsibilityConfirmed.value = false
  showCreateConfirm.value = true
}
function continueCreate() { showCreateConfirm.value = false; void submitCreate() }
async function submitCreate() {
  creating.value = true
  try {
    const batch = await resetRebatesAPI.create({
      mechanism_version: 2,
      force_stat_ratio_enabled: forceRatioEnabled.value,
      force_stat_ratio: forceRatio.value,
      acknowledged_error_account_ids: selectedErrorAccounts.value.map((item) => item.id),
      accounts: [...selectedIds.value].map((id) => {
        const draft = drafts.get(id)!
        return {
          account_id: id, period_start: draft.period_start, period_end: draft.period_end, ratio_mode: draft.ratio_mode,
          default_window_version: draft.default_window_version, window_modified: draft.window_modified,
          ...(draft.ratio_mode === 'manual' ? { manual_ratio: draft.manual_ratio } : {})
        }
      })
    })
    showErrorAccountConfirm.value = false
    activeBatch.value = batch
    previewLoaded.value = false
    schedulePoll()
  } catch (error) {
    if (extractApiErrorCode(error) === 'RESET_REBATE_ERROR_ACCOUNTS_CHANGED') {
      await loadAccounts(true)
      if (selectedErrorAccounts.value.length > 0) {
        errorAccountsConfirmed.value = false
        showErrorAccountConfirm.value = true
      }
      appStore.showWarning('账号状态已变化，请核对并重新确认错误状态账号')
      return
    }
    appStore.showError(errorMessage(error))
  } finally {
    creating.value = false
  }
}

function schedulePoll() {
  if (!activeBatch.value || !['running', 'executing'].includes(activeBatch.value.status)) return
  if (pollTimer) clearTimeout(pollTimer)
  const batchID = activeBatch.value.id
  pollTimer = setTimeout(async () => {
    try {
      const batch = await resetRebatesAPI.get(batchID)
      if (activeBatch.value?.id !== batchID) return
      activeBatch.value = batch
      if (['running', 'executing'].includes(batch.status)) schedulePoll()
      else {
        await Promise.all([loadBatchAccounts(), loadBatchUsers()])
      }
    } catch (error) {
      if (activeBatch.value?.id !== batchID) return
      appStore.showError(errorMessage(error))
      schedulePoll()
    }
  }, 1500)
}
async function loadBatchUsers(result = '') {
  if (!activeBatch.value) return
  try {
    const response = await resetRebatesAPI.listUsers(activeBatch.value.id, userPage.value, userPageSize, userSearch.value, result || userResult.value)
    users.value = response.items
    userTotal.value = response.total
  } catch (error) { appStore.showError(errorMessage(error)) }
}
async function loadBatchAccounts() {
  if (!activeBatch.value) return
  try {
    const first = await resetRebatesAPI.listAccounts(activeBatch.value.id, 1, 100)
    const items = [...first.items]
    const pages = Math.ceil(first.total / 100)
    for (let page = 2; page <= pages; page++) {
      const response = await resetRebatesAPI.listAccounts(activeBatch.value.id, page, 100)
      items.push(...response.items)
    }
    batchAccounts.value = items
  } catch (error) { appStore.showError(errorMessage(error)) }
}
async function loadPreview() {
  if (!activeBatch.value) return
  previewing.value = true
  try {
    const response = await resetRebatesAPI.preview(activeBatch.value.id, payoutRatio.value, rebateReason.value, userPage.value, userPageSize, userSearch.value)
    activeBatch.value = response.batch
    users.value = response.users.items
    userTotal.value = response.users.total
    previewLoaded.value = true
  } catch (error) { appStore.showError(errorMessage(error)) } finally { previewing.value = false }
}
async function executeBatch() {
  if (!activeBatch.value) return
  executing.value = true
  try {
    activeBatch.value = await resetRebatesAPI.execute(activeBatch.value.id, activeBatch.value.preview_version)
    showExecuteConfirm.value = false
    executeConfirmed.value = false
    if (activeBatch.value.status === 'executing') {
      schedulePoll()
      appStore.showSuccess('返利发放任务已启动')
    } else {
      await loadBatchUsers()
      appStore.showSuccess('返利发放完成')
    }
  } catch (error) { appStore.showError(errorMessage(error)) } finally { executing.value = false }
}
async function retryFailedUsers() {
  if (!activeBatch.value) return
  executing.value = true
  try {
    activeBatch.value = await resetRebatesAPI.retryFailures(activeBatch.value.id)
    showRetryConfirm.value = false
    if (activeBatch.value.status === 'executing') {
      schedulePoll()
      appStore.showSuccess('失败用户重试任务已启动')
    } else {
      await loadBatchUsers()
      appStore.showSuccess('失败用户重试完成')
    }
  } catch (error) { appStore.showError(errorMessage(error)) } finally { executing.value = false }
}
async function toggleContributions(user: ResetRebateUser) {
  if (!activeBatch.value) return
  if (expandedUserId.value === user.user_id) { expandedUserId.value = null; return }
  expandedUserId.value = user.user_id
  contributionsLoading.value = true
  try { contributions.value = await resetRebatesAPI.listContributions(activeBatch.value.id, user.user_id) }
  catch (error) { appStore.showError(errorMessage(error)) }
  finally { contributionsLoading.value = false }
}
async function changeUserPage(page: number) { userPage.value = page; expandedUserId.value = null; await loadBatchUsers() }

async function loadHistory() {
  historyLoading.value = true
  try {
    const params: Record<string, unknown> = {}
    if (historyFilters.account.trim()) params.account = historyFilters.account.trim()
    if (historyFilters.status) params.status = historyFilters.status
    if (Number(historyFilters.admin_id) > 0) params.admin_id = Number(historyFilters.admin_id)
    if (Number(historyFilters.executed_admin_id) > 0) params.executed_admin_id = Number(historyFilters.executed_admin_id)
    if (historyFilters.created_start) params.created_start = inputToISO(historyFilters.created_start)
    if (historyFilters.created_end) params.created_end = inputToISO(historyFilters.created_end)
    const response = await resetRebatesAPI.list(historyPage.value, historyPageSize, params)
    history.value = response.items
    historyTotal.value = response.total
  }
  catch (error) { appStore.showError(errorMessage(error)) }
  finally { historyLoading.value = false }
}
function applyHistoryFilters() { historyPage.value = 1; void loadHistory() }
function openHistory() { viewMode.value = 'history'; activeBatch.value = null; void loadHistory() }
async function changeHistoryPage(page: number) { historyPage.value = page; await loadHistory() }
async function openBatch(id: number) {
  try {
    activeBatch.value = await resetRebatesAPI.get(id)
    previewLoaded.value = Boolean(activeBatch.value.preview_version)
    userPage.value = 1
    if (['running', 'executing'].includes(activeBatch.value.status)) schedulePoll()
    else await Promise.all([loadBatchAccounts(), loadBatchUsers()])
  } catch (error) { appStore.showError(errorMessage(error)) }
}
function closeBatch() {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
  activeBatch.value = null
  users.value = []
  batchAccounts.value = []
  if (viewMode.value === 'history') void loadHistory()
}
async function downloadExport(kind: 'users' | 'user-account-contributions' | 'failed-users') {
  if (!activeBatch.value) return
  try { const blob = await resetRebatesAPI.exportCSV(activeBatch.value.id, kind); saveAs(blob, `reset-rebate-${activeBatch.value.id}-${kind}.csv`) }
  catch (error) { appStore.showError(errorMessage(error)) }
}
function canDeleteBatch(batch: ResetRebateBatch) { return batch.status !== 'running' && batch.status !== 'executing' && batch.status !== 'partial' && batch.status !== 'executed' && batch.successful_user_count === 0 }
async function deleteBatch() {
  if (!deletingBatch.value) return
  try {
    await resetRebatesAPI.remove(deletingBatch.value.id)
    deletingBatch.value = null
    await loadHistory()
    appStore.showSuccess('批次已删除')
  } catch (error) { appStore.showError(errorMessage(error)) }
}

function money(value: string) { const number = Number(value || 0); return Number.isFinite(number) ? number.toFixed(8).replace(/\.?0+$/, '') || '0' : value }
function statusText(batch: { status: string; failure_stage?: string }) { const text: Record<string, string> = { running: '统计中', executing: '发放中', ready: '待预览/发放', not_eligible: '无可返额度', partial: '部分成功', failed: batch.failure_stage === 'execution' ? '发放失败' : '统计失败', executed: '已完成', incomplete: '不完整', expired: '已过期' }; return text[batch.status] || batch.status }
function statusClass(status: string) { if (status === 'executed') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'; if (status === 'partial' || status === 'running' || status === 'executing' || status === 'ready') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'; return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300' }
function resultText(result: string) { return ({ pending: '待发放', succeeded: '成功', failed: '失败', excluded: '排除' } as Record<string, string>)[result] || result }
function resultClass(result: string) { if (result === 'succeeded') return 'bg-emerald-100 text-emerald-700'; if (result === 'failed') return 'bg-red-100 text-red-700'; return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300' }

onMounted(() => { void loadAccounts(false) })
onBeforeUnmount(() => { if (pollTimer) clearTimeout(pollTimer) })
watch([payoutRatio, rebateReason], () => {
  if (activeBatch.value?.status === 'ready' && previewLoaded.value) {
    previewLoaded.value = false
    executeConfirmed.value = false
  }
})
</script>
