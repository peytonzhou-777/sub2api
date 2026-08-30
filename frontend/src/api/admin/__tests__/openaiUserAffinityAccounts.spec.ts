import { beforeEach, describe, expect, it, vi } from 'vitest'

import { resetOpenAIUserAffinityPlacement } from '../openaiUserAffinityAccounts'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

describe('OpenAI 用户粘性账号管理 API', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: {} })
  })

  it('单 scope 重置保留 scope 并明确 all_scopes 为 false', async () => {
    await resetOpenAIUserAffinityPlacement(42, 'openai:v1:group:1:lane:general', true)

    expect(post).toHaveBeenCalledWith('/admin/accounts/user-affinity/42/reset', {
      scope_key: 'openai:v1:group:1:lane:general',
      all_scopes: false,
      exclude_source_account: true
    })
    expect(post.mock.calls[0][1]).not.toHaveProperty('reason')
  })

  it('全 scope 重置发送空 scope 和 all_scopes 标记', async () => {
    await resetOpenAIUserAffinityPlacement(42, '', false, true)

    expect(post).toHaveBeenCalledWith('/admin/accounts/user-affinity/42/reset', {
      scope_key: '',
      all_scopes: true,
      exclude_source_account: false
    })
  })
})
