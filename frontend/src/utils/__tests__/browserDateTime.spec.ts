import { afterAll, describe, expect, it } from 'vitest'
import { localDateTimeInputToUTC, utcToLocalDateTimeInput } from '../browserDateTime'

const originalTimezone = process.env.TZ

afterAll(() => {
  process.env.TZ = originalTimezone
})

describe('browserDateTime', () => {
  it('converts UTC across a calendar day in Asia/Shanghai without drift', () => {
    process.env.TZ = 'Asia/Shanghai'
    const utc = '2026-07-10T18:30:00.000Z'

    const local = utcToLocalDateTimeInput(utc)

    expect(local).toBe('2026-07-11T02:30')
    expect(localDateTimeInputToUTC(local)).toBe(utc)
  })

  it('uses the selected date DST offset in America/New_York', () => {
    process.env.TZ = 'America/New_York'
    const utc = '2026-03-08T07:30:00.000Z'

    const local = utcToLocalDateTimeInput(utc)

    expect(local).toBe('2026-03-08T03:30')
    expect(localDateTimeInputToUTC(local)).toBe(utc)
  })

  it('keeps winter and summer edits stable in a DST timezone', () => {
    process.env.TZ = 'America/New_York'
    const winter = '2026-01-15T17:00:00.000Z'
    const summer = '2026-07-15T16:00:00.000Z'

    expect(localDateTimeInputToUTC(utcToLocalDateTimeInput(winter))).toBe(winter)
    expect(localDateTimeInputToUTC(utcToLocalDateTimeInput(summer))).toBe(summer)
  })

  it('preserves the original instant and seconds during a DST fallback fold', () => {
    process.env.TZ = 'America/New_York'
    const utc = '2026-11-01T06:30:45.123Z'

    const local = utcToLocalDateTimeInput(utc)

    expect(local).toBe('2026-11-01T01:30:45.123')
    expect(localDateTimeInputToUTC(local, utc)).toBe(utc)
    expect(localDateTimeInputToUTC(local)).not.toBe(utc)
  })

  it('rejects empty or invalid local values', () => {
    expect(() => localDateTimeInputToUTC('')).toThrow()
    expect(() => localDateTimeInputToUTC('not-a-date')).toThrow()
    expect(() => utcToLocalDateTimeInput('not-a-date')).toThrow()
  })
})
