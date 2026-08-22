import { beforeEach, describe, expect, it, vi } from 'vitest'

import { resetOpenAIUserAffinityPlacement } from '../openaiUserAffinityAccounts'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

describe('OpenAI 用户粘性账号管理 API', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: {} })
  })

  it('整组重置只发送 scope 和原账号排除开关', async () => {
    await resetOpenAIUserAffinityPlacement(42, 'openai:v1:group:1:lane:general', true)

    expect(post).toHaveBeenCalledWith('/admin/accounts/user-affinity/42/reset', {
      scope_key: 'openai:v1:group:1:lane:general',
      exclude_source_account: true
    })
    expect(post.mock.calls[0][1]).not.toHaveProperty('reason')
  })
})
