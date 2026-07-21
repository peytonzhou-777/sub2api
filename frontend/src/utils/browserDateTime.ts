function padDatePart(value: number): string {
  return String(value).padStart(2, '0')
}

function assertValidDate(date: Date): void {
  if (Number.isNaN(date.getTime())) {
    throw new Error('INVALID_DATE_TIME')
  }
}

/** 将 UTC 时间转换为浏览器本地 datetime-local 输入值。 */
export function utcToLocalDateTimeInput(value: string): string {
  const date = new Date(value)
  assertValidDate(date)
  let timePart = padDatePart(date.getHours()) + ':' + padDatePart(date.getMinutes())
  if (date.getSeconds() !== 0 || date.getMilliseconds() !== 0) {
    timePart += ':' + padDatePart(date.getSeconds())
  }
  if (date.getMilliseconds() !== 0) {
    timePart += '.' + String(date.getMilliseconds()).padStart(3, '0')
  }
  return [
    date.getFullYear() + '-' + padDatePart(date.getMonth() + 1) + '-' + padDatePart(date.getDate()),
    timePart,
  ].join('T')
}

/** 将浏览器本地 datetime-local 输入值转换为 RFC3339 UTC 时间。 */
export function localDateTimeInputToUTC(value: string, originalUTC?: string | null): string {
  if (!value) throw new Error('INVALID_DATE_TIME')
  if (originalUTC) {
    const original = new Date(originalUTC)
    assertValidDate(original)
    if (utcToLocalDateTimeInput(originalUTC) === value) {
      return original.toISOString()
    }
  }
  const date = new Date(value)
  assertValidDate(date)
  return date.toISOString()
}

/** 使用浏览器时区格式化 UTC 时间。 */
export function formatBrowserDateTime(value: string): string {
  const date = new Date(value)
  assertValidDate(date)
  return date.getFullYear() + '/' + padDatePart(date.getMonth() + 1) + '/' + padDatePart(date.getDate())
    + ' ' + padDatePart(date.getHours()) + ':' + padDatePart(date.getMinutes())
}
