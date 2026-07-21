import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, post } }))

import { adjustBalance, adjustLimitedCredit, createLimitedCredit, getCreditUser, listCreditUsers, resetLimitedCredit, revokeLimitedCredit } from '@/api/admin/credits'

describe('admin credits api', () => {
  beforeEach(() => { get.mockReset(); post.mockReset(); get.mockResolvedValue({ data: {} }); post.mockResolvedValue({ data: {} }) })

  it('loads users and detail', async () => {
    await listCreditUsers(2, 10, 'user@example.com')
    await getCreditUser(7)
    expect(get).toHaveBeenNthCalledWith(1, '/admin/credits/users', { params: { page: 2, page_size: 10, search: 'user@example.com' } })
    expect(get).toHaveBeenNthCalledWith(2, '/admin/credits/users/7')
  })

  it('uses dedicated write endpoints', async () => {
    const expected = '2026-07-11T00:00:00Z'
    await adjustBalance(7, { operation: 'add', amount: 10, notes: '', expected_updated_at: expected })
    await createLimitedCredit(7, { amount: 5, validity_days: 30, notes: '' })
    await adjustLimitedCredit(7, 9, { amount_operation: 'subtract', amount: 1, expected_updated_at: expected })
    await resetLimitedCredit(7, 9, { expected_updated_at: expected, notes: '' })
    await revokeLimitedCredit(7, 9, { expected_updated_at: expected, notes: '' })
    expect(post.mock.calls.map(call => call[0])).toEqual([
      '/admin/credits/users/7/balance-adjustments',
      '/admin/credits/users/7/limited-credits',
      '/admin/credits/users/7/limited-credits/9/adjustments',
      '/admin/credits/users/7/limited-credits/9/reset',
      '/admin/credits/users/7/limited-credits/9/revoke'
    ])
  })
})
