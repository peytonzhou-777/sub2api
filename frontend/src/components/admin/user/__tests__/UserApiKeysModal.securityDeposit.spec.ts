import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UserApiKeysModal from '../UserApiKeysModal.vue'

const { getUserApiKeys, getAll, unlockApiKey } = vi.hoisted(() => ({
  getUserApiKeys: vi.fn(),
  getAll: vi.fn(),
  unlockApiKey: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { getUserApiKeys },
    groups: { getAll },
    apiKeys: { updateApiKeyGroup: vi.fn() },
    securityDeposits: { unlockApiKey },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('UserApiKeysModal security deposit unlock', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getAll.mockResolvedValue([])
    getUserApiKeys.mockResolvedValue({
      items: [{
        id: 12,
        user_id: 7,
        key: 'sk-security-locked-key-value',
        name: 'locked key',
        group_id: null,
        status: 'security_locked',
        created_at: '2026-08-16T00:00:00Z',
      }],
    })
    unlockApiKey.mockResolvedValue({ api_key_id: 12, status: 'disabled' })
  })

  it('requires an explicit confirmation and keeps the unlocked key disabled', async () => {
    const wrapper = mount(UserApiKeysModal, {
      props: { show: false, user: { id: 7, email: 'user@test.com', username: 'user' } as any },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /></div>' },
          GroupBadge: true,
          GroupOptionItem: true,
          Teleport: true,
        },
      },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    await wrapper.get('[data-test="security-deposit-unlock-key"]').trigger('click')
    expect(unlockApiKey).not.toHaveBeenCalled()
    await wrapper.get('input[maxlength="1000"]').setValue('appeal reviewed')
    const confirm = wrapper.findAll('button').find((button) => button.text() === 'common.confirm')
    await confirm!.trigger('click')
    await flushPromises()

    expect(unlockApiKey).toHaveBeenCalledWith(7, 12, 'appeal reviewed')
    expect(wrapper.text()).toContain('disabled')
    expect(wrapper.find('[data-test="security-deposit-unlock-key"]').exists()).toBe(false)
  })
})
