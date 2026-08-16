import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UserSecurityDepositModal from '../UserSecurityDepositModal.vue'

const { getUser, credit, deduct, revokeLot, automaticallyRefundLot, reserveManualRefund, completeManualRefund, cancelRefund } = vi.hoisted(() => ({
  getUser: vi.fn(),
  credit: vi.fn(),
  deduct: vi.fn(),
  revokeLot: vi.fn(),
  automaticallyRefundLot: vi.fn(),
  reserveManualRefund: vi.fn(),
  completeManualRefund: vi.fn(),
  cancelRefund: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { securityDeposits: { getUser, credit, deduct, revokeLot, automaticallyRefundLot, reserveManualRefund, completeManualRefund, cancelRefund } },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

const detail = {
  user: { user_id: 7, email: 'user@test.com', username: 'user' },
  account: {
    total_balance_cents: 15000,
    paid_balance_cents: 10000,
    admin_grant_balance_cents: 5000,
    risk_multiplier: 2,
    cyber_strike_count: 1,
    lots: [
      { id: 1, bucket_type: 'paid', original_cents: 10000, remaining_cents: 10000, refund_reserved_cents: 0, provider_refund_enabled: true },
      { id: 2, bucket_type: 'admin_grant', original_cents: 5000, remaining_cents: 5000, refund_reserved_cents: 0 },
    ],
  },
  ledger: [],
  refunds: [],
  violations: [],
}

describe('UserSecurityDepositModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getUser.mockResolvedValue(detail)
    credit.mockResolvedValue({ action_id: 1 })
    deduct.mockResolvedValue({ action_id: 2 })
    revokeLot.mockResolvedValue({ action_id: 3 })
    automaticallyRefundLot.mockResolvedValue({ refund_id: 'sdref-1' })
    reserveManualRefund.mockResolvedValue({ refund_id: 'sdref-2' })
  })

  it('shows separated balances and submits exact integer cents', async () => {
    const wrapper = mount(UserSecurityDepositModal, {
      props: { show: false, user: { id: 7, email: 'user@test.com', username: 'user' } as any },
      global: {
        stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true },
      },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.get('[data-test="security-deposit-admin-total"]').text()).toContain('150')
    await wrapper.get('[data-test="security-deposit-admin-amount"]').setValue('100.25')
    await wrapper.get('[data-test="security-deposit-admin-submit"]').trigger('click')
    await flushPromises()

    expect(credit).toHaveBeenCalledWith(7, 10025, 'admin_add', '')
  })

  it('requires explicit confirmation before revoking a permanent lot', async () => {
    const wrapper = mount(UserSecurityDepositModal, {
      props: { show: false, user: { id: 7, email: 'user@test.com', username: 'user' } as any },
      global: {
        stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true },
      },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    const revokeButton = wrapper.findAll('button').find((button) => button.text() === 'admin.users.securityDeposit.revoke')
    expect(revokeButton).toBeTruthy()
    await revokeButton!.trigger('click')
    expect(revokeLot).not.toHaveBeenCalled()
  })

  it('requires explicit confirmation before refunding a paid lot', async () => {
    const wrapper = mount(UserSecurityDepositModal, {
      props: { show: false, user: { id: 7, email: 'user@test.com', username: 'user' } as any },
      global: {
        stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true },
      },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    const refundButton = wrapper.findAll('button').find((button) => button.text().includes('admin.users.securityDeposit.originalRefund'))
    expect(refundButton).toBeTruthy()
    await refundButton!.trigger('click')
    expect(automaticallyRefundLot).not.toHaveBeenCalled()
    const confirmButton = wrapper.findAll('button').find((button) => button.text() === 'common.confirm' && button.isVisible())
    await confirmButton!.trigger('click')
    await flushPromises()
    expect(automaticallyRefundLot).toHaveBeenCalledWith(7, 1, '')
  })
})
