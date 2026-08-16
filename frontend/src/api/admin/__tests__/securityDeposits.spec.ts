import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('../../client', () => ({ apiClient: { get, post } }))

import {
  automaticallyRefundSecurityDepositLot,
  cancelSecurityDepositRefund,
  completeManualSecurityDepositRefund,
  creditSecurityDeposit,
  deductSecurityDeposit,
  failAutomaticSecurityDepositRefundReview,
  getSecurityDepositUser,
  querySecurityDepositRefund,
  reserveManualSecurityDepositRefund,
  revokeSecurityDepositLot,
  unlockSecurityDepositApiKey,
} from '../securityDeposits'

describe('admin security deposits API', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111')
    get.mockResolvedValue({ data: { user: { user_id: 7 } } })
    post.mockResolvedValue({ data: { action_id: 1 } })
  })

  it('loads the full user deposit evidence chain', async () => {
    await getSecurityDepositUser(7)
    expect(get).toHaveBeenCalledWith('/admin/security-deposits/users/7')
  })

  it('sends integer cents and an idempotency key for financial mutations', async () => {
    await creditSecurityDeposit(7, 10025, 'admin_add')
    await deductSecurityDeposit(7, 2500, ' optional ')
    await revokeSecurityDepositLot(7, 9)

    expect(post).toHaveBeenNthCalledWith(
      1,
      '/admin/security-deposits/users/7/credits',
      { amount_cents: 10025, action_type: 'admin_add', reason: undefined },
      { headers: { 'Idempotency-Key': '11111111-1111-4111-8111-111111111111' } },
    )
    expect(post.mock.calls[1][1]).toEqual({ amount_cents: 2500, reason: ' optional ' })
    expect(post.mock.calls[2][0]).toBe('/admin/security-deposits/users/7/lots/9/revoke')
  })

  it('uses the dedicated security unlock endpoint', async () => {
    await unlockSecurityDepositApiKey(7, 12, 'reviewed')
    expect(post).toHaveBeenCalledWith(
      '/admin/security-deposits/users/7/api-keys/12/unlock',
      { reason: 'reviewed' },
      { headers: { 'Idempotency-Key': '11111111-1111-4111-8111-111111111111' } },
    )
  })

  it('uses dedicated audited endpoints for automatic and manual refunds', async () => {
    await automaticallyRefundSecurityDepositLot(7, 9, 'automatic')
    await reserveManualSecurityDepositRefund(7, 10)
    await completeManualSecurityDepositRefund(7, 'sdref-1', {
      external_refund_id: 'external-1',
      external_amount_cents: 10000,
      external_refunded_at: '2026-08-16T12:00:00.000Z',
      external_evidence: { reference: 'voucher-1' },
    })
    await cancelSecurityDepositRefund(7, 'sdref-2')
	await querySecurityDepositRefund(7, 'sdref/3')
	await failAutomaticSecurityDepositRefundReview(7, 'sdref/4', { reference: 'provider-console' })

    expect(post.mock.calls[0][0]).toBe('/admin/security-deposits/users/7/lots/9/refunds/automatic')
    expect(post.mock.calls[1][0]).toBe('/admin/security-deposits/users/7/lots/10/refunds/manual')
    expect(post.mock.calls[2][0]).toBe('/admin/security-deposits/users/7/refunds/sdref-1/complete-manual')
    expect(post.mock.calls[2][1]).toMatchObject({ external_amount_cents: 10000 })
    expect(post.mock.calls[3][0]).toBe('/admin/security-deposits/users/7/refunds/sdref-2/cancel')
    expect(post.mock.calls[4][0]).toBe('/admin/security-deposits/users/7/refunds/sdref%2F3/query')
	expect(post.mock.calls[5][0]).toBe('/admin/security-deposits/users/7/refunds/sdref%2F4/review-failed')
	expect(post.mock.calls[5][2].headers['Idempotency-Key']).toBeTruthy()
    expect(post.mock.calls.slice(0, 4).every((call) => call[2].headers['Idempotency-Key'])).toBe(true)
  })
})
