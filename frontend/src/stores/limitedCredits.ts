/**
 * 用户限时额度状态。
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import limitedCreditsAPI from '@/api/limitedCredits'
import type { LimitedCreditGrant } from '@/types'

const CACHE_TTL_MS = 60_000

let requestGeneration = 0

export const useLimitedCreditStore = defineStore('limitedCredits', () => {
  const activeCredits = ref<LimitedCreditGrant[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const lastFetchedAt = ref<number | null>(null)

  let activePromise: Promise<LimitedCreditGrant[]> | null = null
  let pollerInterval: ReturnType<typeof setInterval> | null = null

  const hasActiveLimitedCredits = computed(() => activeCredits.value.length > 0)
  const activeCount = computed(() => activeCredits.value.length)
  const initialAmount = computed(() => sumCredits('initial_amount'))
  const usedAmount = computed(() => sumCredits('used_amount'))
  const frozenAmount = computed(() => sumCredits('frozen_amount'))
  const remainingAmount = computed(() => sumCredits('remaining_amount'))
  const availableAmount = computed(() => sumCredits('available_amount'))

  // 按字段聚合当前仍有效的限时额度。
  function sumCredits(field: keyof Pick<LimitedCreditGrant, 'initial_amount' | 'used_amount' | 'frozen_amount' | 'remaining_amount' | 'available_amount'>): number {
    return activeCredits.value.reduce((sum, credit) => sum + Number(credit[field] || 0), 0)
  }

  // 拉取有效限时额度，带缓存和请求去重。
  async function fetchActiveLimitedCredits(force = false): Promise<LimitedCreditGrant[]> {
    const now = Date.now()
    if (!force && loaded.value && lastFetchedAt.value && now - lastFetchedAt.value < CACHE_TTL_MS) {
      return activeCredits.value
    }
    if (activePromise && !force) {
      return activePromise
    }

    const currentGeneration = ++requestGeneration
    loading.value = true

    const requestPromise = limitedCreditsAPI
      .getActiveLimitedCredits()
      .then((data) => {
        if (currentGeneration === requestGeneration) {
          activeCredits.value = data
          loaded.value = true
          lastFetchedAt.value = Date.now()
        }
        return data
      })
      .catch((error) => {
        console.error('Failed to fetch active limited credits:', error)
        throw error
      })
      .finally(() => {
        if (activePromise === requestPromise) {
          loading.value = false
          activePromise = null
        }
      })

    activePromise = requestPromise
    return activePromise
  }

  // 开启定时刷新，避免额度使用后长期停留在旧值。
  function startPolling() {
    if (pollerInterval) return

    pollerInterval = setInterval(() => {
      fetchActiveLimitedCredits(true).catch((error) => {
        console.error('Limited credit polling failed:', error)
      })
    }, 5 * 60 * 1000)
  }

  function stopPolling() {
    if (pollerInterval) {
      clearInterval(pollerInterval)
      pollerInterval = null
    }
  }

  function clear() {
    requestGeneration++
    activePromise = null
    activeCredits.value = []
    loaded.value = false
    lastFetchedAt.value = null
    stopPolling()
  }

  function invalidateCache() {
    lastFetchedAt.value = null
  }

  return {
    activeCredits,
    loading,
    hasActiveLimitedCredits,
    activeCount,
    initialAmount,
    usedAmount,
    frozenAmount,
    remainingAmount,
    availableAmount,
    fetchActiveLimitedCredits,
    startPolling,
    stopPolling,
    clear,
    invalidateCache
  }
})