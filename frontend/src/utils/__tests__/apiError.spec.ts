import { describe, expect, it } from 'vitest'

import { extractApiErrorMessage, extractApiErrorStatus } from '@/utils/apiError'

describe('API 错误读取工具', () => {
  it.each([
    [
      '响应拦截器扁平错误',
      { status: 409, code: 'VERSION_CONFLICT', message: '扁平对象中的后端消息' },
      '扁平对象中的后端消息',
    ],
    [
      'Axios 原始错误',
      {
        response: {
          status: 409,
          data: { message: 'Axios 响应体中的后端消息' },
        },
        message: 'Request failed with status code 409',
      },
      'Axios 响应体中的后端消息',
    ],
  ])('%s 能读取 409 状态和后端消息', (_name, error, expectedMessage) => {
    expect(extractApiErrorStatus(error)).toBe(409)
    expect(extractApiErrorMessage(error, 'common.error')).toBe(expectedMessage)
  })

  it('Axios 原始错误优先显示响应体 message，而非 Axios 顶层通用消息', () => {
    const error = {
      response: {
        status: 422,
        data: { message: '金额必须大于零' },
      },
      message: 'Request failed with status code 422',
    }

    expect(extractApiErrorMessage(error, 'common.error')).toBe('金额必须大于零')
  })

  it('无可显示消息时回退到调用方提供的默认消息', () => {
    expect(extractApiErrorStatus({})).toBeUndefined()
    expect(extractApiErrorMessage({}, 'common.error')).toBe('common.error')
  })
})
