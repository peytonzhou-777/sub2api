/**
 * 用户限时额度 API
 */

import { apiClient } from './client'
import type { LimitedCreditGrant, LimitedCreditSummary } from '@/types'

// 获取当前用户有效限时额度明细。
export async function getActiveLimitedCredits(): Promise<LimitedCreditGrant[]> {
  const response = await apiClient.get<LimitedCreditGrant[]>('/limited-credits/active')
  return response.data
}

// 获取当前用户限时额度汇总。
export async function getLimitedCreditSummary(): Promise<LimitedCreditSummary> {
  const response = await apiClient.get<LimitedCreditSummary>('/limited-credits/summary')
  return response.data
}

export default {
  getActiveLimitedCredits,
  getLimitedCreditSummary
}