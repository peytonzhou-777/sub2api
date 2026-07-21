import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put, remove } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), remove: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post, put, delete: remove } }))

import { createRecurringCredit, previewRecurringCredit, recurringCreditAction } from '@/api/admin/recurringCredits'

const payload = { name: '月度赠额', admin_notes: '', schedule_type: 'monthly' as const, day_of_month: 5, local_time: '08:00', timezone: 'Asia/Shanghai', amount: 10, execution_mode: 'finite' as const, remaining_runs: 3, initially_active: true }

describe('recurring credits api', () => {
  beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset(); remove.mockReset() })

  it('previews cost before creating with an idempotency key', async () => {
    post.mockResolvedValue({ data: {} })
    await previewRecurringCredit(payload)
    await createRecurringCredit(payload, 'request-1')
    expect(post).toHaveBeenNthCalledWith(1, '/admin/credits/recurring-grants/preview', payload, { params: undefined })
    expect(post).toHaveBeenNthCalledWith(2, '/admin/credits/recurring-grants', payload, { headers: { 'Idempotency-Key': 'request-1' } })
  })

  it('uses explicit endpoints for mode and lifecycle actions', async () => {
    post.mockResolvedValue({ data: {} })
    await recurringCreditAction(7, 'make-finite', 4, 6)
    expect(post).toHaveBeenCalledWith('/admin/credits/recurring-grants/7/make-finite', { expected_version: 4, count: 6, configuration: undefined })
  })
})
