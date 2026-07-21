import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RechargeBonusCampaign } from '@/types/payment'
import AdminRechargeActivitiesView from '../AdminRechargeActivitiesView.vue'

const api = vi.hoisted(() => ({
  getRechargeBonusCampaigns: vi.fn(),
  createRechargeBonusCampaign: vi.fn(),
  updateRechargeBonusCampaign: vi.fn(),
  deleteRechargeBonusCampaign: vi.fn(),
}))

const appStore = vi.hoisted(() => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: api,
  default: api,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const scheduledCampaign: RechargeBonusCampaign = {
  id: 1,
  name: '暑期充值礼遇',
  description: '充值越多，赠送比例越高\n名额有限',
  start_at: '2026-07-15T00:00:00.000Z',
  end_at: '2026-07-31T00:00:00.000Z',
  participation_limit: 2,
  tiers: [{ min_amount: 10, max_amount: 100, min_rate: 5, max_rate: 10 }],
  status: 'scheduled',
  created_at: '2026-07-01T00:00:00.000Z',
  updated_at: '2026-07-01T00:00:00.000Z',
}

function mountView() {
  return mount(AdminRechargeActivitiesView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: { template: '<span />' },
      },
    },
  })
}

describe('AdminRechargeActivitiesView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    process.env.TZ = 'Asia/Shanghai'
    api.getRechargeBonusCampaigns.mockResolvedValue({ data: [scheduledCampaign] })
    api.createRechargeBonusCampaign.mockResolvedValue({ data: scheduledCampaign })
    api.updateRechargeBonusCampaign.mockResolvedValue({ data: scheduledCampaign })
    api.deleteRechargeBonusCampaign.mockResolvedValue({})
  })

  it('loads campaign name and preserves description line breaks', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="campaign-name"]').text()).toBe('暑期充值礼遇')
    expect(wrapper.get('[data-test="campaign-description"]').text()).toContain('名额有限')
    expect(wrapper.text()).toContain('2026/07/15')
  })

  it('creates a campaign with browser-local times converted to UTC', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="create-campaign"]').trigger('click')

    await wrapper.get('[data-test="campaign-name-input"]').setValue('国庆活动')
    await wrapper.get('[data-test="campaign-description-input"]').setValue('第一行\n第二行')
    await wrapper.get('[data-test="campaign-start-input"]').setValue('2026-10-01T08:00')
    await wrapper.get('[data-test="campaign-end-input"]').setValue('2026-10-08T08:00')
    await wrapper.get('[data-test="campaign-limit-input"]').setValue('3')
    await wrapper.get('[data-test="tier-min-amount-0"]').setValue('10')
    await wrapper.get('[data-test="tier-max-amount-0"]').setValue('100')
    await wrapper.get('[data-test="tier-min-rate-0"]').setValue('5')
    await wrapper.get('[data-test="tier-max-rate-0"]').setValue('10')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(api.createRechargeBonusCampaign).toHaveBeenCalledWith({
      name: '国庆活动',
      description: '第一行\n第二行',
      start_at: '2026-10-01T00:00:00.000Z',
      end_at: '2026-10-08T00:00:00.000Z',
      participation_limit: 3,
      tiers: [{ min_amount: 10, max_amount: 100, min_rate: 5, max_rate: 10 }],
    })
  })

  it('supports adding and deleting tier rows', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="create-campaign"]').trigger('click')

    expect(wrapper.findAll('[data-test="tier-row"]')).toHaveLength(1)
    await wrapper.get('[data-test="add-tier"]').trigger('click')
    expect(wrapper.findAll('[data-test="tier-row"]')).toHaveLength(2)
    await wrapper.get('[data-test="delete-tier-1"]').trigger('click')
    expect(wrapper.findAll('[data-test="tier-row"]')).toHaveLength(1)
  })

  it('only exposes early ending for an active campaign', async () => {
    api.getRechargeBonusCampaigns.mockResolvedValue({
      data: [{ ...scheduledCampaign, status: 'active' }],
    })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-test="edit-campaign"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="delete-campaign"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="end-campaign"]').exists()).toBe(true)
  })

  it('renders an explicit empty state', async () => {
    api.getRechargeBonusCampaigns.mockResolvedValue({ data: [] })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="campaign-empty"]').text()).toContain('payment.rechargeActivities.empty')
  })
})
