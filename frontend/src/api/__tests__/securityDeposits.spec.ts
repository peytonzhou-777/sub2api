import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post },
}))

import { securityDepositsAPI } from '@/api/securityDeposits'

describe('security deposits api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('按分组获取服务端权威资格报价', async () => {
    await securityDepositsAPI.getEligibility(42)

    expect(get).toHaveBeenCalledWith('/security-deposits/eligibility', {
      params: { group_id: 42 },
    })
  })

  it('创建订单时只提交报价与协议事实，不提交客户端金额', async () => {
    await securityDepositsAPI.createOrder({
      group_id: 42,
      agreement_version: '2026-08-15',
      agreement_hash: 'agreement-hash',
      quote_hash: 'quote-hash',
      accepted: true,
      payment_type: 'alipay',
      is_mobile: false,
    })

    expect(post).toHaveBeenCalledWith('/security-deposits/orders', {
      group_id: 42,
      agreement_version: '2026-08-15',
      agreement_hash: 'agreement-hash',
      quote_hash: 'quote-hash',
      accepted: true,
      payment_type: 'alipay',
      is_mobile: false,
    })
    expect(post.mock.calls[0][1]).not.toHaveProperty('amount')
  })

  it('使用专用接口预览、确认并查询单批次退款', async () => {
    await securityDepositsAPI.previewRefund(9)
    await securityDepositsAPI.createRefund(9, 'refund-idempotency-1')
    await securityDepositsAPI.getRefund('sdref/unsafe')

    expect(post).toHaveBeenNthCalledWith(1, '/security-deposits/refunds/preview', { lot_id: 9 })
    expect(post).toHaveBeenNthCalledWith(
      2,
      '/security-deposits/refunds',
      { lot_id: 9 },
      { headers: { 'Idempotency-Key': 'refund-idempotency-1' } },
    )
    expect(get).toHaveBeenCalledWith('/security-deposits/refunds/sdref%2Funsafe')
  })
})
