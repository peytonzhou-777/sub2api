import { describe, expect, it } from 'vitest'
import { calculateCreditedBalancePreview, calculateRechargeBonusPreview } from '../rechargeBonus'

describe('rechargeBonus preview', () => {
  it('uses decimal half-up rounding for the permanent credited balance', () => {
    expect(calculateCreditedBalancePreview(10, 0.1025)).toBe(1.03)
  })

  it('uses the later tier at a shared boundary', () => {
    expect(calculateRechargeBonusPreview(100, [
      { min_amount: 10, max_amount: 100, min_rate: 5, max_rate: 5 },
      { min_amount: 100, max_amount: 500, min_rate: 10, max_rate: 10 },
    ])).toBe(10)
  })

  it('includes the upper boundary of the final tier', () => {
    expect(calculateRechargeBonusPreview(500, [
      { min_amount: 100, max_amount: 500, min_rate: 5, max_rate: 10 },
    ])).toBe(50)
  })

  it('uses linear interpolation and rounds the bonus to eight places', () => {
    expect(calculateRechargeBonusPreview(1, [
      { min_amount: 0, max_amount: 3, min_rate: 0, max_rate: 100 },
    ])).toBe(0.33333333)
  })

  it('returns zero for gaps and invalid inputs', () => {
    const tiers = [
      { min_amount: 10, max_amount: 100, min_rate: 5, max_rate: 5 },
      { min_amount: 200, max_amount: 500, min_rate: 10, max_rate: 10 },
    ]
    expect(calculateRechargeBonusPreview(150, tiers)).toBe(0)
    expect(calculateCreditedBalancePreview(Number.NaN, 1)).toBe(0)
  })
})
