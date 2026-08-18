import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const root = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(root, '../ResetRebatesView.vue'), 'utf8')

describe('ResetRebatesView v2 contract', () => {
  it('defaults to non-error OAuth accounts and server-owned windows', () => {
    expect(source).toContain("item.type === 'oauth' && item.status !== 'error'")
    expect(source).toContain('accountWindowDefaults')
    expect(source).toContain('修改账号统计设置')
  })

  it('exposes independent account and payout ratios', () => {
    expect(source).toContain('强制覆盖所有账号统计比例')
    expect(source).toContain("ratio_mode: 'auto'")
    expect(source).toContain('发放比例')
  })

  it('supports applying one statistics window to multiple selected accounts', () => {
    expect(source).toContain('批量设置统计窗口')
    expect(source).toContain('本次将修改已选择的 {{ selectedIds.size }} 个账号')
    expect(source).toContain('applyWindowToDraft')
    expect(source).toContain('账号原有的统计比例模式和手动比例保持不变')
  })

  it('requires risk confirmation and reports per-user failures', () => {
    expect(source).toContain('系统不提供周期防重')
    expect(source).toContain('确认选择错误状态账号')
    expect(source).toContain('单个用户失败会被跳过')
    expect(source).toContain('retryFailures')
    expect(source).toContain('官方重置！按账号重置天数返还消耗额度！')
  })
})
