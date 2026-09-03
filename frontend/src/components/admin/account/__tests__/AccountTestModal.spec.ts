import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountTestModal from '../AccountTestModal.vue'

const { getAvailableModels, listOpenAIAccountPersonas, updateAccount, copyToClipboard } = vi.hoisted(() => ({
  getAvailableModels: vi.fn(),
  listOpenAIAccountPersonas: vi.fn(),
  updateAccount: vi.fn(),
  copyToClipboard: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getAvailableModels,
      listOpenAIAccountPersonas,
      update: updateAccount
    }
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'admin.accounts.imagePromptDefault': 'Generate a cute orange cat astronaut sticker on a clean pastel background.'
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.accounts.imageReceived' && params?.count) {
          return `received-${params.count}`
        }
        if (key === 'admin.accounts.imagePreviewAlt' && params?.index) {
          return `test-image-${params.index}`
        }
        return messages[key] || key
      }
    })
  }
})

function createStreamResponse(lines: string[]) {
  const encoder = new TextEncoder()
  const chunks = lines.map((line) => encoder.encode(line))
  let index = 0

  return {
    ok: true,
    body: {
      getReader: () => ({
        read: vi.fn().mockImplementation(async () => {
          if (index < chunks.length) {
            return { done: false, value: chunks[index++] }
          }
          return { done: true, value: undefined }
        })
      })
    }
  } as Response
}

function mountModal(account: Record<string, unknown> = {
  id: 42,
  name: 'Gemini Image Test',
  platform: 'gemini',
  type: 'apikey',
  status: 'active'
}, variant: 'connection' | 'intelligence' = 'connection') {
  return mount(AccountTestModal, {
    props: {
      show: false,
      account,
      variant
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Select: {
          props: ['options'],
          template: '<div class="select-stub">{{ (options || []).map((option) => option.label || option.display_name).join(" | ") }}</div>'
        },
        TextArea: {
          props: ['modelValue'],
          emits: ['update:modelValue'],
          template: '<textarea class="textarea-stub" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
        },
        Icon: true
      }
    }
  })
}

describe('AccountTestModal', () => {
  beforeEach(() => {
    getAvailableModels.mockResolvedValue([
      { id: 'gemini-2.0-flash', display_name: 'Gemini 2.0 Flash' },
      { id: 'gemini-2.5-flash-image', display_name: 'Gemini 2.5 Flash Image' },
      { id: 'gemini-3.1-flash-image', display_name: 'Gemini 3.1 Flash Image' }
    ])
    listOpenAIAccountPersonas.mockResolvedValue([{
      id: 4201,
      account_id: 42,
      position: 0,
      profile_id: 'codex_cli_strict',
      state: 'active',
      enabled: true,
      authorized: true
    }])
    updateAccount.mockReset()
    copyToClipboard.mockReset()
    Object.defineProperty(globalThis, 'localStorage', {
      value: {
        getItem: vi.fn((key: string) => (key === 'auth_token' ? 'test-token' : null)),
        setItem: vi.fn(),
        removeItem: vi.fn(),
        clear: vi.fn()
      },
      configurable: true
    })
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"gemini-2.5-flash-image"}\n',
        'data: {"type":"image","image_url":"data:image/png;base64,QUJD","mime_type":"image/png"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('gemini 图片模型测试会携带提示词并渲染图片预览', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const promptInput = wrapper.find('textarea.textarea-stub')
    expect(promptInput.exists()).toBe(true)
    await promptInput.setValue('draw a tiny orange cat astronaut')

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'gemini-3.1-flash-image',
      prompt: 'draw a tiny orange cat astronaut'
    })

    const preview = wrapper.find('img[alt="test-image-1"]')
    expect(preview.exists()).toBe(true)
    expect(preview.attributes('src')).toBe('data:image/png;base64,QUJD')
  })

  it('grok 账号测试默认选择 Grok 模型', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'grok-4.3', display_name: 'Grok 4.3' },
      { id: 'grok-build-0.1', display_name: 'Grok Build 0.1' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"grok-4.3"}\n',
        'data: {"type":"content","text":"ok"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 13,
      name: 'Grok Account',
      platform: 'grok',
      type: 'oauth',
      status: 'active'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'grok-4.3',
      prompt: '',
      mode: 'text'
    })
  })

  it('OpenAI Compact 探测会携带 compact 测试模式', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-5.4', display_name: 'GPT-5.4' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 42,
      name: 'OpenAI OAuth',
      platform: 'openai',
      type: 'oauth',
      status: 'active'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    ;(wrapper.vm as any).selectedModelId = 'gpt-5.4'
    ;(wrapper.vm as any).testMode = 'compact'
    await (wrapper.vm as any).startTest()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toMatchObject({
      model_id: 'gpt-5.4',
      prompt: '',
      mode: 'compact'
    })
  })

  it('OpenAI OAuth 降智检测仅提交模型并渲染流式答案', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-image-1', display_name: 'GPT Image 1' },
      { id: 'gpt-5.4', display_name: 'GPT-5.4' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"gpt-5.4"}\n',
        'data: {"type":"content","text":"29"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 42,
      name: 'OpenAI OAuth',
      platform: 'openai',
      type: 'oauth',
      status: 'active'
    }, 'intelligence')
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect((wrapper.vm as any).selectedModelId).toBe('gpt-5.4')
    expect((wrapper.vm as any).modelOptionsForMode.map((model: { id: string }) => model.id)).toEqual([
      'gpt-5.4'
    ])
    expect(wrapper.find('textarea.textarea-stub').exists()).toBe(false)

    const startButton = wrapper.findAll('button').find((button) =>
      button.text().includes('admin.accounts.startIntelligenceTest')
    )
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [url, request] = (global.fetch as any).mock.calls[0]
    expect(String(url)).toContain('/admin/accounts/42/intelligence-test')
    expect(JSON.parse(request.body)).toEqual({ model_id: 'gpt-5.4', account_persona_id: 4201 })
    expect(wrapper.text()).toContain('29')
    expect(wrapper.text()).toContain('admin.accounts.intelligenceTestCompleted')
  })

  it('OpenAI OAuth 降智检测允许管理员修改人工标记', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-5.4', display_name: 'GPT-5.4' }
    ])
    updateAccount.mockResolvedValue({
      id: 42,
      name: 'OpenAI OAuth',
      platform: 'openai',
      type: 'oauth',
      status: 'active',
      intelligence_test_status: 'passed'
    })

    const wrapper = mountModal({
      id: 42,
      name: 'OpenAI OAuth',
      platform: 'openai',
      type: 'oauth',
      status: 'active',
      intelligence_test_status: 'failed'
    }, 'intelligence')
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.get('[data-testid="intelligence-mark-failed"]').attributes('aria-pressed')).toBe('true')

    await wrapper.get('[data-testid="intelligence-mark-passed"]').trigger('click')
    await flushPromises()

    expect(updateAccount).toHaveBeenCalledWith(42, { intelligence_test_status: 'passed' })
    expect(wrapper.get('[data-testid="intelligence-mark-passed"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.emitted('account-updated')).toEqual([[
      expect.objectContaining({ id: 42, intelligence_test_status: 'passed' })
    ]])
  })

  it('动态 Persona 降智检测显示身份并提交稳定 ID', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-5.4', display_name: 'GPT-5.4' }
    ])
    listOpenAIAccountPersonas.mockResolvedValue([
        {
          id: 5101,
          position: 0,
          profile_id: 'codex_cli_strict',
          state: 'active',
          enabled: true,
          authorized: true
        },
        {
          id: 5102,
          position: 1,
          profile_id: 'opencode',
          state: 'active',
          enabled: true,
          authorized: true
        }
      ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 52,
      parent_account_id: 51,
      name: 'OpenAI Persona Shadow',
      platform: 'openai',
      type: 'oauth',
      status: 'active'
    }, 'intelligence')
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(listOpenAIAccountPersonas).toHaveBeenCalledWith(51)
    expect(wrapper.text()).toContain('Codex CLI Strict')
    expect(wrapper.text()).toContain('OpenCode')
    expect((wrapper.vm as any).selectedAccountPersonaId).toBe(5101)

    ;(wrapper.vm as any).selectedAccountPersonaId = 5102
    await (wrapper.vm as any).startTest()
    await flushPromises()

    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'gpt-5.4',
      account_persona_id: 5102
    })
  })

  it('没有可用动态 Persona 时禁止开始检测', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-5.4', display_name: 'GPT-5.4' }
    ])
    listOpenAIAccountPersonas.mockResolvedValue([
        {
          id: 5301,
          position: 0,
          profile_id: 'codex_cli_strict',
          state: 'draining',
          enabled: false,
          authorized: true
        },
        {
          id: 5302,
          position: 1,
          profile_id: 'opencode',
          state: 'draft',
          enabled: false,
          authorized: false
        }
      ])

    const wrapper = mountModal({
      id: 53,
      name: 'OpenAI Persona',
      platform: 'openai',
      type: 'oauth',
      status: 'active'
    }, 'intelligence')
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect((wrapper.vm as any).selectedAccountPersonaId).toBeNull()
    expect((wrapper.vm as any).canStartTest).toBe(false)
    expect(global.fetch).not.toHaveBeenCalled()
  })
})
