import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AccountRouteDetectionModal from '../AccountRouteDetectionModal.vue'

const { detectCodexRoute } = vi.hoisted(() => ({
  detectCodexRoute: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { detectCodexRoute }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key === 'admin.accounts.routeDetectionNotReturned' ? '未返回' : key })
  }
})

const account = {
  id: 42,
  name: 'Codex Account',
  platform: 'openai',
  type: 'oauth',
  status: 'active'
}

const result = {
  account_id: 42,
  credential_account_id: 41,
  status: 'luna',
  checked_at: '2026-09-04T01:02:03Z',
  reason_code: 'luna_engine',
  requested_model: 'gpt-5.6-sol',
  reported_model: 'gpt-5.6-sol',
  response_headers: {
    'x-codex-primary-used-percent': '12',
    'x-codex-primary-window-minutes': '300',
    'x-codex-active-limit': 'primary',
    'x-codex-safety-buffering-faster-model': ''
  }
}

const mountModal = () => mount(AccountRouteDetectionModal, {
  props: { show: false, account } as any,
  global: {
    stubs: {
      BaseDialog: {
        props: ['show'],
        template: '<div v-if="show"><slot /><slot name="footer" /></div>'
      },
      Icon: true
    }
  }
})

describe('AccountRouteDetectionModal', () => {
  beforeEach(() => {
    detectCodexRoute.mockReset()
    detectCodexRoute.mockResolvedValue(result)
  })

  it('打开后自动检测并直接打印辅助响应头', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(detectCodexRoute).toHaveBeenCalledWith(42, expect.any(AbortSignal))
    expect(wrapper.text()).toContain('admin.accounts.routeDetectionStatusLuna')
    const headers = wrapper.get('[data-testid="route-detection-headers"]').text()
    expect(headers).toContain('x-codex-primary-used-percent: 12')
    expect(headers).toContain('x-codex-primary-window-minutes: 300')
    expect(headers).toContain('x-codex-active-limit: primary')
    expect(headers).toContain('x-codex-safety-buffering-faster-model: 未返回')
    expect(wrapper.emitted('account-updated')?.[0]?.[0]).toEqual(expect.objectContaining({
      id: 42,
      extra: expect.objectContaining({ codex_route_detection: result })
    }))
  })

  it('请求失败时显示错误且不覆盖账号快照', async () => {
    detectCodexRoute.mockRejectedValue(new Error('network failed'))
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.text()).toContain('network failed')
    expect(wrapper.emitted('account-updated')).toBeUndefined()
  })
})
