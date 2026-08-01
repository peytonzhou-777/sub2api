import { describe, expect, it } from 'vitest'

import { parseRetryAfter } from '../client'

describe('parseRetryAfter', () => {
  it('支持秒数和 HTTP 日期', () => {
    const now = Date.UTC(2026, 7, 1, 12, 0, 0)
    expect(parseRetryAfter('17', now)).toBe(17)
    expect(parseRetryAfter(new Date(now + 12_500).toUTCString(), now)).toBe(12)
  })
})
