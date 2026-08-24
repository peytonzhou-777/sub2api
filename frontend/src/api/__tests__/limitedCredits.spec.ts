import { beforeEach, describe, expect, expectTypeOf, it, vi } from 'vitest'
import type { LimitedCreditSummary } from '@/types'

const { get } = vi.hoisted(() => ({
  get: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get
  }
}))

import { getDepletedLimitedCredits, getLimitedCreditSummary } from '@/api/limitedCredits'

describe('limited credits api', () => {
  beforeEach(() => {
    get.mockReset()
  })

  it('keeps the summary type aligned with the backend response', async () => {
    const summary = {
      active_count: 1,
      available_amount: 2.5,
      frozen_amount: 0.5,
      remaining_amount: 3,
      grants: []
    } satisfies LimitedCreditSummary
    get.mockResolvedValue({ data: summary })

    const result = await getLimitedCreditSummary()

    expect(get).toHaveBeenCalledWith('/limited-credits/summary')
    expect(result).toEqual(summary)
    expectTypeOf(result).toEqualTypeOf<LimitedCreditSummary>()
  })

  it('queries depleted credits with an explicit time window', async () => {
    get.mockResolvedValue({ data: [] })

    const result = await getDepletedLimitedCredits(
      '2026-08-01T00:00:00.000Z',
      '2026-09-01T00:00:00.000Z',
    )

    expect(get).toHaveBeenCalledWith('/limited-credits/depleted', {
      params: {
        start_time: '2026-08-01T00:00:00.000Z',
        end_time: '2026-09-01T00:00:00.000Z',
      },
    })
    expect(result).toEqual([])
  })
})
