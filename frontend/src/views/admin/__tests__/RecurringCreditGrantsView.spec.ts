import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../RecurringCreditGrantsView.vue'), 'utf8')
const apiSource = readFileSync(resolve(dirname(fileURLToPath(import.meta.url)), '../../../api/admin/recurringCredits.ts'), 'utf8')

describe('RecurringCreditGrantsView grant tasks', () => {
  it('renames the feature and keeps immediate execution as the default', () => {
    expect(source).toContain('<h2 class="text-lg font-semibold">赠额任务</h2>')
    expect(source).toContain("formMode.value==='create'?'新建赠额任务'")
    expect(source).toContain("schedule_type:'immediate'")
    expect(source).toContain('<option value="immediate">立即执行</option>')
  })

  it('uses rolling thirty-day activity for every task type', () => {
    expect(source).toContain('发放对象：近 30 天活跃用户')
    expect(source).toContain('API 活跃')
    expect(source).toContain('站内活跃')
    expect(source).toContain('两者命中')
    expect(source).not.toContain('确认创建后立即向全部未删除用户发放')
    expect(source).not.toContain('最近完整同类周期参考合格人数')
  })

  it('shows activity columns for new batches and legacy amounts for old batches', () => {
    expect(source).toContain("eligibility_policy==='rolling_30d_activity_v1'")
    expect(source).toContain('最后 API 活跃')
    expect(source).toContain('最后站内活跃')
    expect(source).toContain('实际消耗')
    expect(source).toContain('净充值')
  })

  it('shows all rolling activity reasons and snapshot exclusion states', () => {
    expect(source).toContain("api_activity:'API 活跃'")
    expect(source).toContain("site_activity:'站内活跃'")
    expect(source).toContain("api_and_site_activity:'API + 站内活跃'")
    expect(source).toContain("user_inactive:'用户已停用'")
    expect(source).toContain("user_deleted:'用户已删除'")
  })

  it('declares the backward-compatible activity fields in the admin API', () => {
    expect(apiSource).toContain('api_active_count: number')
    expect(apiSource).toContain('site_active_count: number')
    expect(apiSource).toContain('both_active_count: number')
    expect(apiSource).toContain('api_last_used_at?: string')
    expect(apiSource).toContain('site_last_active_at?: string')
  })
})
