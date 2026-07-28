import { beforeEach, describe, expect, it, vi } from 'vitest'
import { shallowMount } from '@vue/test-utils'
import AppHeader from '../AppHeader.vue'

const copyToClipboard = vi.hoisted(() => vi.fn().mockResolvedValue(true))
const appState = vi.hoisted(() => ({
  customerServiceGroupNumber: '',
  customerServiceGroupLink: '',
  contactInfo: '',
  docUrl: '',
  cachedPublicSettings: {},
}))
const authState = vi.hoisted(() => ({
  user: {
    id: 1,
    username: 'demo',
    email: 'demo@example.com',
    role: 'user',
    balance: 0,
    avatar_url: '',
  } as Record<string, unknown> | null,
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
  useRoute: () => ({ name: 'Dashboard', params: {}, meta: {} }),
}))

vi.mock('@/utils/featureFlags', () => ({
  FeatureFlags: { modelPlaza: 'model_plaza' },
  isFeatureFlagEnabled: () => false,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'common.customerServiceGroupNumber') {
          return `群号：${params?.number}`
        }
        if (key === 'common.customerServiceGroupAction') {
          return `复制群号 ${params?.number} 并打开客服群`
        }
        return key
      },
    }),
  }
})

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard }),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    ...appState,
    toggleMobileSidebar: vi.fn(),
  }),
  useAuthStore: () => ({
    user: authState.user,
    isAdmin: false,
    isSimpleMode: false,
    logout: vi.fn(),
  }),
  useLimitedCreditStore: () => ({
    remainingAmount: 0,
    activeCredits: [],
  }),
  useOnboardingStore: () => ({ replay: vi.fn() }),
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ customMenuItems: [] }),
}))

function mountHeader() {
  return shallowMount(AppHeader, {
    global: {
      stubs: {
        AnnouncementBell: true,
        LocaleSwitcher: true,
        Icon: true,
        Transition: false,
        RouterLink: true,
      },
    },
  })
}

describe('AppHeader customer service group', () => {
  beforeEach(() => {
    copyToClipboard.mockClear()
    appState.customerServiceGroupNumber = ''
    appState.customerServiceGroupLink = ''
    authState.user = {
      id: 1,
      username: 'demo',
      email: 'demo@example.com',
      role: 'user',
      balance: 0,
      avatar_url: '',
    }
  })

  it('renders the complete group entry before announcements', () => {
    appState.customerServiceGroupNumber = '001234567'
    appState.customerServiceGroupLink = 'https://qm.qq.com/q/example'

    const wrapper = mountHeader()
    const entry = wrapper.get('[data-test="customer-service-group"]')

    expect(entry.text()).toContain('群号：001234567')
    expect(entry.attributes('href')).toBe('https://qm.qq.com/q/example')
    expect(entry.attributes('target')).toBe('_blank')
    expect(entry.attributes('rel')).toBe('noopener noreferrer')
    expect(entry.element.nextElementSibling?.tagName).toBe('ANNOUNCEMENT-BELL-STUB')
  })

  it('copies the exact group number while leaving native link navigation intact', async () => {
    appState.customerServiceGroupNumber = '001234567'
    appState.customerServiceGroupLink = 'https://qm.qq.com/q/example'

    const wrapper = mountHeader()
    await wrapper.get('[data-test="customer-service-group"]').trigger('click')

    expect(copyToClipboard).toHaveBeenCalledWith(
      '001234567',
      'common.customerServiceGroupCopied',
    )
  })

  it.each([
    ['', 'https://qm.qq.com/q/example'],
    ['123456789', ''],
    ['123456789', 'javascript:alert(1)'],
  ])('hides incomplete or unsafe configuration', (number, link) => {
    appState.customerServiceGroupNumber = number
    appState.customerServiceGroupLink = link

    const wrapper = mountHeader()

    expect(wrapper.find('[data-test="customer-service-group"]').exists()).toBe(false)
  })

  it('hides the entry when no user is signed in', () => {
    appState.customerServiceGroupNumber = '123456789'
    appState.customerServiceGroupLink = 'https://qm.qq.com/q/example'
    authState.user = null

    const wrapper = mountHeader()

    expect(wrapper.find('[data-test="customer-service-group"]').exists()).toBe(false)
  })
})
