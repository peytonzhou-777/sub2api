import { describe, expect, it, vi } from 'vitest'
import { accountsAPI } from '../accounts'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { post } }))

describe('OpenAI OAuth 建号 API', () => {
  it('发送配置至原子建号接口并返回账号', async () => {
    const account = { id: 42, platform: 'openai', type: 'oauth' }
    post.mockResolvedValue({ data: account })
    const payload = {
      name: 'OAuth account', session_id: 'session', code: 'code', state: 'state',
      concurrency: 3, priority: 50, credential_extras: { model_mapping: { a: 'b' } }
    }
    expect(await accountsAPI.createOpenAIOAuth(payload)).toEqual(account)
    expect(post).toHaveBeenCalledWith('/admin/openai/create-from-oauth', payload)
  })
})
