import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

describe('OpenAI 用户粘性调度翻译', () => {
  it('在中文运行时路径提供字段名称、选项与说明', () => {
    expect(zh.admin.settings.openAIUserAffinity).toMatchObject({
      title: 'OpenAI 用户粘性调度',
      modes: { enforce: '强制执行', shadow: '影子模式' },
      bestFitStrategy: '额度优先窗口',
      bestFitStrategyHint: expect.stringContaining('剩余额度更多'),
      closeTolerance: '主窗口接近容差',
      touchSuccessModes: {
        upstreamAccepted: '上游已接受请求',
        responseCompleted: '响应已完成'
      },
      maxResidentUsersHint: expect.stringContaining('跨分组和 Scope 按用户去重'),
      failureThresholdHint: expect.any(String),
      reserve7dHint: expect.any(String),
      resetExcludeSourceHint: expect.stringContaining('直接参与新居民 Best Fit')
    })
    expect(zh.admin).not.toHaveProperty('openAIUserAffinity')
  })

  it('在英文运行时路径提供对应字段说明', () => {
    expect(en.admin.settings.openAIUserAffinity).toMatchObject({
      title: 'OpenAI User Affinity Scheduling',
      modes: { enforce: 'Enforce', shadow: 'Shadow' },
      bestFitStrategy: 'Quota priority window',
      bestFitStrategyHint: expect.stringContaining('more projected remaining quota'),
      closeTolerance: 'Primary-window close tolerance',
      touchSuccessModes: {
        upstreamAccepted: 'Upstream accepted the request',
        responseCompleted: 'Response completed'
      },
      maxResidentUsersHint: expect.stringContaining('deduplicated across groups and scopes'),
      failureThresholdHint: expect.any(String),
      reserve7dHint: expect.any(String),
      resetExcludeSourceHint: expect.stringContaining('enter new-resident Best Fit directly')
    })
    expect(en.admin).not.toHaveProperty('openAIUserAffinity')
  })
})
