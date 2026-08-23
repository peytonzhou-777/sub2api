import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const { accountsAPI, resetRebatesAPI, showError, showSuccess, showWarning } = vi.hoisted(() => ({
  accountsAPI: { list: vi.fn() },
  resetRebatesAPI: {
    accountWindowDefaults: vi.fn(),
    create: vi.fn()
  },
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api/admin', () => ({ accountsAPI, resetRebatesAPI }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess, showWarning }) }))
vi.mock('file-saver', () => ({ saveAs: vi.fn() }))

import ResetRebatesView from '../ResetRebatesView.vue'

const root = dirname(fileURLToPath(import.meta.url))
const source = readFileSync(resolve(root, '../ResetRebatesView.vue'), 'utf8')

function findByText<T extends { text: () => string }>(items: T[], text: string): T {
  const item = items.find((candidate) => candidate.text().includes(text))
  if (!item) throw new Error(`未找到包含文本“${text}”的元素`)
  return item
}

function mountView() {
  return mount(ResetRebatesView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        BaseDialog: {
          props: ['show'],
          template: '<section v-if="show"><slot /><slot name="footer" /></section>'
        },
        AccountStatusIndicator: true,
        Icon: true
      }
    }
  })
}

describe('ResetRebatesView v3 contract', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    accountsAPI.list.mockResolvedValue({
      items: [{ id: 11, name: 'OAuth A', platform: 'openai', type: 'oauth', status: 'active', schedulable: true, error_message: '' }],
      total: 1
    })
    resetRebatesAPI.accountWindowDefaults.mockResolvedValue([{
      account_id: 11,
      period_start: '2026-08-01T00:00:00Z',
      period_end: '2026-08-05T00:00:00Z',
      history_count: 2,
      window_source: 'history',
      window_version: 'v1',
      risk: '',
      auto_stat_ratio: '42.85714286',
      account_status: 'active',
      error_message: ''
    }])
    resetRebatesAPI.create.mockResolvedValue({ id: 99, status: 'ready' })
  })

  it('defaults to non-error OAuth accounts and server-owned windows', () => {
    expect(source).toContain("item.type === 'oauth' && item.status !== 'error'")
    expect(source).toContain('accountWindowDefaults')
    expect(source).toContain('修改账号统计设置')
  })

  it('exposes independent account and payout ratios', () => {
    expect(source).toContain('全账号平均受益周期和比例')
    expect(source).toContain('统一结束时间')
    expect(source).toContain('强制覆盖所有账号统计比例')
    expect(source).toContain("ratio_mode: 'auto'")
    expect(source).toContain('发放比例')
  })

  it('supports applying one statistics window to multiple selected accounts', () => {
    expect(source).toContain('批量设置开始时间')
    expect(source).toContain('本次将修改已选择的 {{ selectedIds.size }} 个账号')
    expect(source).toContain('applyStartToDraft')
    expect(source).toContain('账号原有的统计比例模式和手动比例保持不变')
  })

  it('requires risk confirmation and reports per-user failures', () => {
    expect(source).toContain('系统不提供周期防重')
    expect(source).toContain('确认选择错误状态账号')
    expect(source).toContain('单个用户失败会被跳过')
    expect(source).toContain('retryFailures')
    expect(source).toContain('官方重置！按账号重置天数返还消耗额度！')
  })

  it('serializes edited account and override ratios as decimal strings', async () => {
    const wrapper = mountView()
    await flushPromises()

    const forceLabel = findByText(wrapper.findAll('label'), '强制覆盖所有账号统计比例')
    await forceLabel.find('input').setValue(true)
    await findByText(wrapper.findAll('label'), '%').find('input[type="number"]').setValue('90.25')

    await wrapper.find('button[title="修改开始时间和比例"]').trigger('click')
    await findByText(wrapper.findAll('button'), '手动设置').trigger('click')
    await findByText(wrapper.findAll('label'), '手动统计比例').find('input').setValue('80.125')
    await findByText(wrapper.findAll('button'), '保存').trigger('click')

    await findByText(wrapper.findAll('button'), '生成统计批次').trigger('click')
    await findByText(wrapper.findAll('label'), '自行负责周期防重').find('input').setValue(true)
    await findByText(wrapper.findAll('button'), '继续').trigger('click')
    await flushPromises()

    const payload = resetRebatesAPI.create.mock.calls[0][0]
    expect(payload.force_stat_ratio).toBe('90.25')
    expect(payload.mechanism_version).toBe(3)
    expect(payload.period_end).toEqual(expect.any(String))
    expect(payload.accounts[0]).not.toHaveProperty('period_end')
    expect(payload.accounts[0].manual_ratio).toBe('80.125')
    wrapper.unmount()
  })

  it('rejects empty ratio inputs instead of treating them as zero', async () => {
    const wrapper = mountView()
    await flushPromises()

    const forceLabel = findByText(wrapper.findAll('label'), '强制覆盖所有账号统计比例')
    await forceLabel.find('input').setValue(true)
    await findByText(wrapper.findAll('label'), '%').find('input[type="number"]').setValue('')
    await findByText(wrapper.findAll('button'), '生成统计批次').trigger('click')

    expect(showError).toHaveBeenCalledWith('强制统计比例必须在 0% 到 100% 之间')
    expect(resetRebatesAPI.create).not.toHaveBeenCalled()

    await wrapper.find('button[title="修改开始时间和比例"]').trigger('click')
    await findByText(wrapper.findAll('button'), '手动设置').trigger('click')
    await findByText(wrapper.findAll('button'), '保存').trigger('click')

    expect(showError).toHaveBeenCalledWith('统计比例必须在 0% 到 100% 之间')
    wrapper.unmount()
  })
})
