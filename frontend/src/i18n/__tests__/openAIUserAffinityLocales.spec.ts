import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

describe('OpenAI 用户粘性调度翻译', () => {
  it('在中文运行时路径提供字段名称、选项与说明', () => {
    expect(zh.admin.settings.openAIUserAffinity).toMatchObject({
      title: 'OpenAI 用户粘性调度',
      modes: { enforce: '强制执行', shadow: '影子模式' },
      touchSuccessModes: {
        upstreamAccepted: '上游已接受请求',
        responseCompleted: '响应已完成'
      },
      maxContactUsersHint: expect.any(String),
      failureThresholdHint: expect.any(String),
      reserve7dHint: expect.any(String),
      resetExcludeSourceHint: expect.any(String)
    })
    expect(zh.admin).not.toHaveProperty('openAIUserAffinity')
  })

  it('在英文运行时路径提供对应字段说明', () => {
    expect(en.admin.settings.openAIUserAffinity).toMatchObject({
      title: 'OpenAI User Affinity Scheduling',
      modes: { enforce: 'Enforce', shadow: 'Shadow' },
      touchSuccessModes: {
        upstreamAccepted: 'Upstream accepted the request',
        responseCompleted: 'Response completed'
      },
      maxContactUsersHint: expect.any(String),
      failureThresholdHint: expect.any(String),
      reserve7dHint: expect.any(String),
      resetExcludeSourceHint: expect.any(String)
    })
    expect(en.admin).not.toHaveProperty('openAIUserAffinity')
  })
})
