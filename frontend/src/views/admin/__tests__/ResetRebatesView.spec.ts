import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../ResetRebatesView.vue'), 'utf8')

describe('ResetRebatesView workflow', () => {
  it('contains recoverable progress and all required statistics ratios', () => {
    expect(source).toContain("current.status === 'running'")
    expect(source).toContain('progress_completed')
    expect(source).toContain('weekly_usage_percent')
    expect(source).toContain('refundable_percent')
    expect(source).toContain('suggested_ratio')
  })

  it('hides stale details while a new task runs and synchronizes all views on completion', () => {
    const createStats = source.match(/async function createStats[\s\S]*?(?=\n\/\/ showCompletedBatch)/)?.[0]
    const showCompletedBatch = source.match(/async function showCompletedBatch[\s\S]*?(?=\n}\n\n\/\/ schedulePoll)/)?.[0]

    expect(createStats).toBeDefined()
    expect(createStats).toContain('current.value = null')
    expect(createStats).toContain('await loadHistory(1)')
    expect(createStats).toContain('schedulePoll(batch.id, true)')
    expect(showCompletedBatch).toBeDefined()
    expect(showCompletedBatch).toContain('current.value = batch')
    expect(showCompletedBatch).toContain('await loadAccounts(1)')
    expect(showCompletedBatch).toContain('await loadHistory(1)')
  })

  it('sets the next period start from the selected group latest executed batch', () => {
    const syncPeriodStart = source.match(/async function syncPeriodStart[\s\S]*?(?=\n}\n\nasync function createStats)/)?.[0]

    expect(source).toContain('getLatestExecutedPeriodEnd(groupID)')
    expect(source).toContain('creating || periodLoading || !form.groupId')
    expect(syncPeriodStart).toBeDefined()
    expect(syncPeriodStart).toContain('const start=periodEnd?new Date(periodEnd)')
    expect(syncPeriodStart).toContain('const latestAllowedEnd=new Date(start.getTime()+7*86400000)')
    expect(syncPeriodStart).toContain('const end=latestAllowedEnd<now?latestAllowedEnd:now')
  })

  it('contains account audit, paginated user preview, CSV and explicit confirmation', () => {
    expect(source).toContain('账号明细（包含影子账号排除记录）')
    expect(source).toContain('loadPreview(preview.page + 1)')
    expect(source).toContain('exportResetRebateUsers')
    expect(source).toContain('我已核对逐用户明细与发放总额')
    expect(source).toContain(':disabled="!checked || executing"')
  })

  it('shows non-executable states and permanent-success cleanup protection', () => {
    expect(source).toContain("not_eligible:'不可返利'")
    expect(source).toContain("incomplete:'统计不完整（可发放）'")
    expect(source).toContain("!['running','executed'].includes(status)")
  })

  it('allows forced execution for incomplete and zero-suggestion snapshots', () => {
    expect(source).toContain("['ready','incomplete','executed'].includes(current.status)")
    expect(source).toContain("incomplete:'统计不完整（可发放）'")
    expect(source).toContain("batch?.participant_count === 0 ? '可强制发放'")
    expect(source).toContain('current.failed_account_amount')
    expect(source).toContain('返还比例由管理员承担判断责任')
    expect(source).toContain('返还比例由管理员主动配置')
  })

  it('reloads account details when opening a historical batch', () => {
    const openBatch = source.match(/async function openBatch[\s\S]*?(?=\nasync function cleanBatch)/)?.[0]

    expect(openBatch).toBeDefined()
    expect(openBatch).toContain('accounts.value=[]')
    expect(openBatch).toContain('await loadAccounts(1)')
  })

  it('prefills only new batches and preserves an explicitly empty rebate reason', () => {
    expect(source).toContain("const defaultRebateReason = '官方重置！本站返利！'")
    expect(source).toContain('maxlength="100"')
    expect(source).toContain("batch.configured_ratio == null && batch.status !== 'executed' ? defaultRebateReason : ''")
    expect(source).toContain("preview.value.batch.rebate_reason||''")
  })

  it('centers all table content inside the current batch detail', () => {
    const currentBatch = source.slice(source.indexOf('<section v-if="current"'), source.indexOf('<section class="rounded-lg', source.indexOf('<section v-if="current"')))

    expect(currentBatch.match(/<table class="min-w-full text-center text-sm">/g)).toHaveLength(2)
  })

  it('centers table content and actions in the batch history', () => {
    const historyBatch = source.slice(source.indexOf('<h2 class="text-lg font-semibold">历史批次</h2>'))

    expect(historyBatch).toContain('<table class="min-w-full text-center text-sm">')
    expect(historyBatch).toContain('<div class="flex justify-center gap-2">')
  })
})
