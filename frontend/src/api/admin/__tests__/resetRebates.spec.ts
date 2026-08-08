import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, remove } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post, delete: remove } }))

import { accountWindowDefaults, create, execute, preview, retryFailures } from '@/api/admin/resetRebates'

describe('reset rebates v2 api', () => {
  beforeEach(() => {
    for (const fn of [get, post, remove]) {
      fn.mockReset()
      fn.mockResolvedValue({ data: {} })
    }
  })

  it('creates an account-scoped batch with server window defaults', async () => {
    await accountWindowDefaults([11, 12])
    await create({
      mechanism_version: 2,
      force_stat_ratio_enabled: true,
      force_stat_ratio: '100',
      acknowledged_error_account_ids: [12],
      accounts: [{ account_id: 11, period_start: '2026-08-01T00:00:00Z', period_end: '2026-08-05T00:00:00Z', ratio_mode: 'manual', manual_ratio: '80' }]
    })

    expect(post).toHaveBeenNthCalledWith(1, '/admin/reset-rebates/account-window-defaults', { account_ids: [11, 12] })
    expect(post.mock.calls[1][0]).toBe('/admin/reset-rebates')
    expect(post.mock.calls[1][1]).toMatchObject({ mechanism_version: 2, force_stat_ratio: '100', acknowledged_error_account_ids: [12] })
  })

  it('freezes preview version and retries only failed users', async () => {
    await preview(9, 90, '官方重置！按账号重置天数返还消耗额度！')
    await execute(9, 4)
    await retryFailures(9)

    expect(post).toHaveBeenNthCalledWith(1, '/admin/reset-rebates/9/preview', { payout_ratio: 90, reason: '官方重置！按账号重置天数返还消耗额度！' }, { params: { page: 1, page_size: 50, search: '' } })
    expect(post).toHaveBeenNthCalledWith(2, '/admin/reset-rebates/9/execute', { preview_version: 4, confirmed: true })
    expect(post).toHaveBeenNthCalledWith(3, '/admin/reset-rebates/9/retry-failures', { confirmed: true })
  })
})
