import Decimal from 'decimal.js'
import type { RechargeBonusTier } from '@/types/payment'

const MoneyDecimal = Decimal.clone({
  precision: 64,
  rounding: Decimal.ROUND_HALF_UP,
})

/** 计算应用充值倍率后的永久到账额度，并按后端规则保留两位小数。 */
export function calculateCreditedBalancePreview(amount: number, multiplier: number): number {
  if (!Number.isFinite(amount) || !Number.isFinite(multiplier) || amount <= 0 || multiplier <= 0) {
    return 0
  }
  return new MoneyDecimal(amount)
    .mul(multiplier)
    .toDecimalPlaces(2, MoneyDecimal.ROUND_HALF_UP)
    .toNumber()
}

/** 按后端阶梯边界、插值精度和八位小数规则试算充值赠送额度。 */
export function calculateRechargeBonusPreview(creditedAmount: number, tiers: RechargeBonusTier[]): number {
  if (!Number.isFinite(creditedAmount) || creditedAmount <= 0 || tiers.length === 0) {
    return 0
  }

  const amount = new MoneyDecimal(creditedAmount)
  const sorted = [...tiers].sort((left, right) => left.min_amount - right.min_amount)
  for (let index = 0; index < sorted.length; index += 1) {
    const tier = sorted[index]
    const minAmount = new MoneyDecimal(tier.min_amount)
    const maxAmount = new MoneyDecimal(tier.max_amount)
    const isLast = index === sorted.length - 1
    if (amount.lt(minAmount) || amount.gt(maxAmount) || (!isLast && amount.eq(maxAmount))) {
      continue
    }

    const minRate = new MoneyDecimal(tier.min_rate)
    const maxRate = new MoneyDecimal(tier.max_rate)
    const position = amount
      .minus(minAmount)
      .div(maxAmount.minus(minAmount))
      .toDecimalPlaces(16, MoneyDecimal.ROUND_HALF_UP)
    const rate = minRate.plus(position.mul(maxRate.minus(minRate)))
    return amount.mul(rate).div(100).toDecimalPlaces(8, MoneyDecimal.ROUND_HALF_UP).toNumber()
  }

  return 0
}
