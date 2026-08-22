import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, post } }))

import { adminAccountRefundAPI } from '../accountRefunds'

describe('管理员余额清退 API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('摘要、列表和详情均使用只读接口', async () => {
    await adminAccountRefundAPI.getSummary()
    await adminAccountRefundAPI.getList({ tab: 'manual_review', page: 2, page_size: 20 })
    await adminAccountRefundAPI.getDetail(42)

    expect(get).toHaveBeenNthCalledWith(1, '/admin/payment/account-refunds/summary')
    expect(get).toHaveBeenNthCalledWith(2, '/admin/payment/account-refunds', { params: { tab: 'manual_review', page: 2, page_size: 20 } })
    expect(get).toHaveBeenNthCalledWith(3, '/admin/payment/account-refunds/42')
    expect(post).not.toHaveBeenCalled()
  })

  it('发起清退携带幂等键，后续动作携带状态版本', async () => {
    await adminAccountRefundAPI.start(42, { expected_state_revision: 7, quote_hash: 'quote-1' }, 'start-key-1')
    await adminAccountRefundAPI.action(42, 'confirm', { expected_state_revision: 8, quote_hash: 'quote-2' })

    expect(post).toHaveBeenNthCalledWith(1, '/admin/payment/account-refunds/42/start', { expected_state_revision: 7, quote_hash: 'quote-1' }, { headers: { 'Idempotency-Key': 'start-key-1' } })
    expect(post).toHaveBeenNthCalledWith(2, '/admin/payment/account-refunds/42/confirm', { expected_state_revision: 8, quote_hash: 'quote-2' })
  })

  it('人工核验提交原订单、外部结果和核验依据', async () => {
    const input = { order_id: 9, outcome: 'succeeded' as const, external_refund_id: 'refund-9', verified_at: '2026-08-22T08:00:00Z', evidence: 'gateway-console', note: '已核对', expected_state_revision: 12 }
    await adminAccountRefundAPI.reconcile(42, input)
    expect(post).toHaveBeenCalledWith('/admin/payment/account-refunds/42/reconcile', input)
  })
})
