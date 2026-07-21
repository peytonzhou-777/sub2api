/**
 * 用户兑换码 API
 */

import { apiClient } from './client'
import type { RedeemCodeRequest } from '@/types'

export interface RedeemHistoryItem {
  id: number
  code: string
  type: string
  value: number
  status: string
  used_at: string
  created_at: string
  // 管理员调整备注，仅 admin_balance/admin_concurrency 类型返回。
  notes?: string
  // 订阅和限时额度相关字段。
  group_id?: number
  validity_days?: number
  group?: {
    id: number
    name: string
  }
}

export interface RedeemResult {
  message: string
  type: string
  value: number
  new_balance?: number
  new_concurrency?: number
  group_name?: string
  validity_days?: number
}

// 兑换卡密并返回兑换结果。
export async function redeem(code: string): Promise<RedeemResult> {
  const payload: RedeemCodeRequest = { code }
  const { data } = await apiClient.post<RedeemResult>('/redeem', payload)
  return data
}

// 获取当前用户兑换历史。
export async function getHistory(): Promise<RedeemHistoryItem[]> {
  const { data } = await apiClient.get<RedeemHistoryItem[]>('/redeem/history')
  return data
}

export const redeemAPI = {
  redeem,
  getHistory
}

export default redeemAPI