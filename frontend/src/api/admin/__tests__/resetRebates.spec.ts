import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, remove } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), remove: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, post, delete: remove } }))

import { createResetRebate, executeResetRebate, getLatestExecutedPeriodEnd, previewResetRebate } from '@/api/admin/resetRebates'

describe('reset rebates api', () => {
  beforeEach(() => { get.mockReset(); post.mockReset(); remove.mockReset() })

  it('creates a timezone-aware statistics task', async () => {
    post.mockResolvedValue({ data: { id: 7, status: 'running' } })
    const payload = { group_id: 3, start: '2026-07-01T00:00:00+08:00', end: '2026-07-08T00:00:00+08:00' }
    await createResetRebate(payload)
    expect(post).toHaveBeenCalledWith('/admin/reset-rebates', payload)
  })

  it('previews before sending the explicit execution confirmation', async () => {
    get.mockResolvedValue({ data: { users: [] } })
    post.mockResolvedValue({ data: { id: 7, status: 'executed' } })
    await previewResetRebate(7, 23, 2, 50, 'user@example.com')
    await executeResetRebate(7, 23)
    expect(get).toHaveBeenCalledWith('/admin/reset-rebates/7/preview', { params: { ratio: 23, page: 2, page_size: 50, search: 'user@example.com' } })
    expect(post).toHaveBeenCalledWith('/admin/reset-rebates/7/execute', { ratio: 23, confirmed: true })
  })

  it('carries the batch rebate reason through preview and execution', async () => {
    get.mockResolvedValue({ data: { batch: { rebate_reason: '本周活动返利' }, users: [] } })
    post.mockResolvedValue({ data: { id: 7, status: 'executed' } })

    await previewResetRebate(7, 23, 1, 50, '', '本周活动返利')
    await executeResetRebate(7, 23, '本周活动返利')

    expect(get).toHaveBeenCalledWith('/admin/reset-rebates/7/preview', { params: { ratio: 23, page: 1, page_size: 50, search: '', reason: '本周活动返利' } })
    expect(post).toHaveBeenCalledWith('/admin/reset-rebates/7/execute', { ratio: 23, confirmed: true, reason: '本周活动返利' })
  })

  it('loads the latest executed period end for a group', async () => {
    get.mockResolvedValueOnce({ data: { period_end: '2026-07-12T16:00:00Z' } })

    await expect(getLatestExecutedPeriodEnd(9)).resolves.toBe('2026-07-12T16:00:00Z')
    expect(get).toHaveBeenCalledWith('/admin/reset-rebates/latest-executed-period-end', { params: { group_id: 9 } })
  })
})
