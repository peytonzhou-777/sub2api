import type { LimitedCreditGrant } from '@/types'

export type LimitedCreditSignalLevel = 'green' | 'yellow' | 'red'

// getLimitedCreditSignalLevel 根据剩余有效期和使用率计算单份限时额度信号等级。
export function getLimitedCreditSignalLevel(credit: LimitedCreditGrant): LimitedCreditSignalLevel {
  const daysRemaining = Math.ceil((new Date(credit.expires_at).getTime() - Date.now()) / 86_400_000)
  const initialAmount = Number(credit.initial_amount || 0)
  const usagePercentage = initialAmount > 0
    ? (Number(credit.used_amount || 0) / initialAmount) * 100
    : 0

  if (daysRemaining <= 3 || usagePercentage >= 90) return 'red'
  if (daysRemaining <= 7 || usagePercentage >= 70) return 'yellow'
  return 'green'
}

// getLimitedCreditAggregateSignalLevel 按红、黄、绿优先级汇总多份限时额度状态。
export function getLimitedCreditAggregateSignalLevel(
  credits: LimitedCreditGrant[],
): LimitedCreditSignalLevel | null {
  if (credits.length === 0) return null

  let aggregate: LimitedCreditSignalLevel = 'green'
  for (const credit of credits) {
    const level = getLimitedCreditSignalLevel(credit)
    if (level === 'red') return 'red'
    if (level === 'yellow') aggregate = 'yellow'
  }
  return aggregate
}
