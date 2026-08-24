import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useLimitedCreditStore } from '@/stores/limitedCredits'
import type { LimitedCreditGrant } from '@/types'

const getDepletedLimitedCredits = vi.hoisted(() => vi.fn())

vi.mock('@/api/limitedCredits', () => ({
  default: {
    getActiveLimitedCredits: vi.fn(),
    getDepletedLimitedCredits,
    getLimitedCreditSummary: vi.fn(),
  },
}))

const depletedGrant: LimitedCreditGrant = {
  id: 9,
  source_type: 'redeem_code',
  initial_amount: 5,
  used_amount: 5,
  frozen_amount: 0,
  remaining_amount: 0,
  available_amount: 0,
  expires_at: '2026-09-01T00:00:00Z',
  status: 'depleted',
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-20T00:00:00Z',
}

describe('useLimitedCreditStore depleted history', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getDepletedLimitedCredits.mockReset()
  })

  it('stores depleted credits returned for the requested time window', async () => {
    getDepletedLimitedCredits.mockResolvedValue([depletedGrant])
    const store = useLimitedCreditStore()

    const result = await store.fetchDepletedLimitedCredits('2026-08-01T00:00:00Z', '2026-09-01T00:00:00Z')

    expect(getDepletedLimitedCredits).toHaveBeenCalledWith('2026-08-01T00:00:00Z', '2026-09-01T00:00:00Z')
    expect(result).toEqual([depletedGrant])
    expect(store.depletedCredits).toEqual([depletedGrant])
    expect(store.historyLoading).toBe(false)
  })

  it('clears depleted history together with the active credit cache', async () => {
    getDepletedLimitedCredits.mockResolvedValue([depletedGrant])
    const store = useLimitedCreditStore()
    await store.fetchDepletedLimitedCredits('2026-08-01T00:00:00Z', '2026-09-01T00:00:00Z')

    store.clear()

    expect(store.depletedCredits).toEqual([])
    expect(store.historyLoading).toBe(false)
  })
})
