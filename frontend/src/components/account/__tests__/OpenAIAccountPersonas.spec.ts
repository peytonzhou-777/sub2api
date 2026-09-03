import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

const apiMocks = vi.hoisted(() => ({
  list: vi.fn(),
  profiles: vi.fn(),
  update: vi.fn(),
  rotate: vi.fn(),
  revoke: vi.fn(),
  refresh: vi.fn(),
  create: vi.fn(),
  retire: vi.fn()
}))

vi.mock('@/api', () => ({
  adminAPI: {
    accounts: {
      listOpenAIAccountPersonas: apiMocks.list,
      listOpenAIAccountPersonaProfiles: apiMocks.profiles,
      updateOpenAIAccountPersona: apiMocks.update,
      rotateOpenAIAccountPersonaSession: apiMocks.rotate,
      revokeOpenAIAccountPersona: apiMocks.revoke,
      refreshOpenAIAccountPersona: apiMocks.refresh,
      createOpenAIAccountPersona: apiMocks.create,
      retireOpenAIAccountPersona: apiMocks.retire
    }
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

import OpenAIAccountPersonas from '../OpenAIAccountPersonas.vue'

function persona(id: number, position: number, protectedPersona: boolean) {
  return {
    id,
    account_id: 7,
    position,
    profile_id: protectedPersona ? 'codex_cli_strict' : 'opencode',
    profile_version: 'test',
    credential_owner: protectedPersona ? 'account_primary' : 'persona_independent',
    state: 'active',
    enabled: true,
    authorized: true,
    persona_generation: 1,
    current_session_epoch: 1,
    row_version: 3,
    default_protected: protectedPersona,
    credential_state: 'ready',
    installation_summary: 'abcd1234',
    active_client_sessions: 0,
    effective_max_client_sessions: 1,
    effective_max_concurrency: 2,
    effective_max_websockets: 1,
    proxy_inherited: true,
    created_at: '2026-09-03T00:00:00Z',
    updated_at: '2026-09-03T00:00:00Z'
  }
}

describe('OpenAIAccountPersonas', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    apiMocks.list.mockResolvedValue([persona(10, 0, true), persona(11, 1, false)])
    apiMocks.profiles.mockResolvedValue([
      { id: 'codex_cli_strict', version: 'test', supported_transports: ['http', 'ws'], compression: 'native' },
      { id: 'opencode', version: 'test', supported_transports: ['http'], compression: 'adapted' }
    ])
    apiMocks.update.mockResolvedValue(persona(11, 1, false))
  })

  it('protects position 0 while exposing independent OAuth actions for later Personas', async () => {
    const wrapper = mount(OpenAIAccountPersonas, {
      props: { account: { id: 7 } as any, proxies: [] },
      global: { stubs: { Icon: true } }
    })
    await vi.waitFor(() => expect(wrapper.find('[data-testid="openai-account-persona-11"]').exists()).toBe(true))

    expect(wrapper.find('[data-testid="persona-authorize-10"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="persona-revoke-10"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="persona-authorize-11"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="persona-revoke-11"]').exists()).toBe(true)
  })

  it('reloads authoritative state after a CAS conflict', async () => {
    apiMocks.update.mockRejectedValue({ response: { status: 409 }, message: 'conflict' })
    const wrapper = mount(OpenAIAccountPersonas, {
      props: { account: { id: 7 } as any, proxies: [] },
      global: { stubs: { Icon: true } }
    })
    await vi.waitFor(() => expect(wrapper.find('[data-testid="persona-save-11"]').exists()).toBe(true))
    await wrapper.get('[data-testid="persona-save-11"]').trigger('click')
    await vi.waitFor(() => expect(apiMocks.list).toHaveBeenCalledTimes(2))
  })

  it('requires confirmation and sends the explicit force-rotation contract', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    apiMocks.rotate.mockResolvedValue(persona(11, 1, false))
    const wrapper = mount(OpenAIAccountPersonas, {
      props: { account: { id: 7 } as any, proxies: [] },
      global: { stubs: { Icon: true } }
    })
    await vi.waitFor(() => expect(wrapper.find('[data-testid="persona-force-rotate-11"]').exists()).toBe(true))
    await wrapper.get('[data-testid="persona-force-rotate-11"]').trigger('click')
    await vi.waitFor(() => expect(apiMocks.rotate).toHaveBeenCalledWith(7, expect.objectContaining({ id: 11 }), true))
  })
})
