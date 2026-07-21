<template>
  <div v-if="hasEntitlements" class="relative" ref="containerRef">
    <button
      @click="toggleTooltip"
      class="flex cursor-pointer items-center gap-2 rounded-xl bg-purple-50 px-3 py-1.5 transition-colors hover:bg-purple-100 dark:bg-purple-900/20 dark:hover:bg-purple-900/30"
      :title="t('subscriptionProgress.viewDetails')"
    >
      <Icon name="creditCard" size="sm" class="text-purple-600 dark:text-purple-400" />
      <div class="flex items-center gap-1.5">
        <div class="flex items-center gap-0.5">
          <div
            v-for="dot in displayDots.slice(0, 4)"
            :key="dot.key"
            class="h-2 w-2 rounded-full"
            :class="dot.className"
          ></div>
        </div>
        <span class="text-xs font-medium text-purple-700 dark:text-purple-300">
          {{ totalActiveCount }}
        </span>
      </div>
    </button>

    <transition name="dropdown">
      <div
        v-if="tooltipOpen"
        class="absolute right-0 z-50 mt-2 w-[360px] overflow-hidden rounded-xl border border-gray-200 bg-white shadow-xl dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="border-b border-gray-100 p-3 dark:border-dark-700">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('subscriptionProgress.title') }}
          </h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
            {{ t('subscriptionProgress.activeCount', { count: totalActiveCount }) }}
          </p>
        </div>

        <div class="max-h-72 overflow-y-auto">
          <div v-if="displaySubscriptions.length > 0" class="border-b border-gray-100 dark:border-dark-700/60">
            <div class="px-3 pt-3 text-[11px] font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-400">
              {{ t('subscriptionProgress.subscriptions') }}
            </div>
            <div
              v-for="subscription in displaySubscriptions"
              :key="`subscription-${subscription.id}`"
              class="border-b border-gray-50 p-3 last:border-b-0 dark:border-dark-700/50"
            >
              <div class="mb-2 flex items-center justify-between gap-3">
                <span class="min-w-0 truncate text-sm font-medium text-gray-900 dark:text-white">
                  {{ subscription.group?.name || `Group #${subscription.group_id}` }}
                </span>
                <span
                  v-if="subscription.expires_at"
                  class="flex-shrink-0 text-xs"
                  :class="getDaysRemainingClass(subscription.expires_at)"
                >
                  {{ formatDaysRemaining(subscription.expires_at) }}
                </span>
              </div>

              <div class="space-y-1.5">
                <div
                  v-if="isUnlimited(subscription)"
                  class="flex items-center gap-2 rounded-lg bg-gradient-to-r from-emerald-50 to-teal-50 px-2.5 py-1.5 dark:from-emerald-900/20 dark:to-teal-900/20"
                >
                  <span class="text-lg text-emerald-600 dark:text-emerald-400">∞</span>
                  <span class="text-xs font-medium text-emerald-700 dark:text-emerald-300">
                    {{ t('subscriptionProgress.unlimited') }}
                  </span>
                </div>

                <template v-else>
                  <div v-if="subscription.group?.daily_limit_usd" class="flex items-center gap-2">
                    <span class="w-8 flex-shrink-0 text-[10px] text-gray-500">{{ t('subscriptionProgress.daily') }}</span>
                    <div class="h-1.5 min-w-0 flex-1 rounded-full bg-gray-200 dark:bg-dark-600">
                      <div
                        class="h-1.5 rounded-full transition-all"
                        :class="getProgressBarClass(subscription.daily_usage_usd, subscription.group?.daily_limit_usd)"
                        :style="{ width: getProgressWidth(subscription.daily_usage_usd, subscription.group?.daily_limit_usd) }"
                      ></div>
                    </div>
                    <span class="w-24 flex-shrink-0 text-right text-[10px] text-gray-500">
                      {{ formatUsage(subscription.daily_usage_usd, subscription.group?.daily_limit_usd) }}
                    </span>
                  </div>

                  <div v-if="subscription.group?.weekly_limit_usd" class="flex items-center gap-2">
                    <span class="w-8 flex-shrink-0 text-[10px] text-gray-500">{{ t('subscriptionProgress.weekly') }}</span>
                    <div class="h-1.5 min-w-0 flex-1 rounded-full bg-gray-200 dark:bg-dark-600">
                      <div
                        class="h-1.5 rounded-full transition-all"
                        :class="getProgressBarClass(subscription.weekly_usage_usd, subscription.group?.weekly_limit_usd)"
                        :style="{ width: getProgressWidth(subscription.weekly_usage_usd, subscription.group?.weekly_limit_usd) }"
                      ></div>
                    </div>
                    <span class="w-24 flex-shrink-0 text-right text-[10px] text-gray-500">
                      {{ formatUsage(subscription.weekly_usage_usd, subscription.group?.weekly_limit_usd) }}
                    </span>
                  </div>

                  <div v-if="subscription.group?.monthly_limit_usd" class="flex items-center gap-2">
                    <span class="w-8 flex-shrink-0 text-[10px] text-gray-500">{{ t('subscriptionProgress.monthly') }}</span>
                    <div class="h-1.5 min-w-0 flex-1 rounded-full bg-gray-200 dark:bg-dark-600">
                      <div
                        class="h-1.5 rounded-full transition-all"
                        :class="getProgressBarClass(subscription.monthly_usage_usd, subscription.group?.monthly_limit_usd)"
                        :style="{ width: getProgressWidth(subscription.monthly_usage_usd, subscription.group?.monthly_limit_usd) }"
                      ></div>
                    </div>
                    <span class="w-24 flex-shrink-0 text-right text-[10px] text-gray-500">
                      {{ formatUsage(subscription.monthly_usage_usd, subscription.group?.monthly_limit_usd) }}
                    </span>
                  </div>
                </template>
              </div>
            </div>
          </div>

          <div v-if="displayLimitedCredits.length > 0">
            <div class="px-3 pt-3 text-[11px] font-semibold uppercase tracking-wide text-gray-400 dark:text-dark-400">
              {{ t('subscriptionProgress.limitedCredits') }}
            </div>
            <div
              v-for="credit in displayLimitedCredits"
              :key="`limited-credit-${credit.id}`"
              class="border-b border-gray-50 p-3 last:border-b-0 dark:border-dark-700/50"
            >
              <div class="mb-2 flex items-center justify-between gap-3">
                <span class="min-w-0 truncate text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('subscriptionProgress.limitedCreditTitle', { id: credit.id }) }}
                </span>
                <span class="flex-shrink-0 text-xs" :class="getDaysRemainingClass(credit.expires_at)">
                  {{ formatDaysRemaining(credit.expires_at) }}
                </span>
              </div>

              <div class="flex items-center gap-2">
                <span class="w-8 flex-shrink-0 text-[10px] text-gray-500">{{ t('subscriptionProgress.used') }}</span>
                <div class="h-1.5 min-w-0 flex-1 rounded-full bg-gray-200 dark:bg-dark-600">
                  <div
                    class="h-1.5 rounded-full transition-all"
                    :class="getLimitedCreditBarClass(credit)"
                    :style="{ width: getProgressWidth(credit.used_amount, credit.initial_amount) }"
                  ></div>
                </div>
                <span class="w-24 flex-shrink-0 text-right text-[10px] text-gray-500">
                  {{ formatUsage(credit.used_amount, credit.initial_amount) }}
                </span>
              </div>

              <div class="mt-2 grid grid-cols-2 gap-2 text-[10px] text-gray-500 dark:text-dark-400">
                <span>{{ t('subscriptionProgress.remaining') }} {{ formatMoney(credit.remaining_amount) }}</span>
                <span class="text-right">{{ t('subscriptionProgress.frozen') }} {{ formatMoney(credit.frozen_amount) }}</span>
              </div>
            </div>
          </div>
        </div>

        <div v-if="displaySubscriptions.length > 0" class="border-t border-gray-100 p-2 dark:border-dark-700">
          <router-link
            to="/subscriptions"
            @click="closeTooltip"
            class="block w-full py-1 text-center text-xs text-primary-600 hover:underline dark:text-primary-400"
          >
            {{ t('subscriptionProgress.viewAll') }}
          </router-link>
        </div>
      </div>
    </transition>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useLimitedCreditStore, useSubscriptionStore } from '@/stores'
import type { LimitedCreditGrant, UserSubscription } from '@/types'

const { t } = useI18n()

const subscriptionStore = useSubscriptionStore()
const limitedCreditStore = useLimitedCreditStore()

const containerRef = ref<HTMLElement | null>(null)
const tooltipOpen = ref(false)

const activeSubscriptions = computed(() => subscriptionStore.activeSubscriptions)
const activeLimitedCredits = computed(() => limitedCreditStore.activeCredits)
const hasEntitlements = computed(() => subscriptionStore.hasActiveSubscriptions || limitedCreditStore.hasActiveLimitedCredits)
const totalActiveCount = computed(() => activeSubscriptions.value.length + activeLimitedCredits.value.length)

const displaySubscriptions = computed(() => {
  return [...activeSubscriptions.value].sort((a, b) => getMaxUsagePercentage(b) - getMaxUsagePercentage(a))
})

const displayLimitedCredits = computed(() => {
  return [...activeLimitedCredits.value].sort((a, b) => {
    const byExpiry = new Date(a.expires_at).getTime() - new Date(b.expires_at).getTime()
    return byExpiry || a.id - b.id
  })
})

const displayDots = computed(() => {
  const subscriptionDots = displaySubscriptions.value.map((sub) => ({
    key: `subscription-${sub.id}`,
    className: getProgressDotClass(sub)
  }))
  const limitedCreditDots = displayLimitedCredits.value.map((credit) => ({
    key: `limited-credit-${credit.id}`,
    className: getLimitedCreditDotClass(credit)
  }))
  return [...subscriptionDots, ...limitedCreditDots]
})

function getMaxUsagePercentage(sub: UserSubscription): number {
  const percentages: number[] = []
  if (sub.group?.daily_limit_usd) {
    percentages.push(((sub.daily_usage_usd || 0) / sub.group.daily_limit_usd) * 100)
  }
  if (sub.group?.weekly_limit_usd) {
    percentages.push(((sub.weekly_usage_usd || 0) / sub.group.weekly_limit_usd) * 100)
  }
  if (sub.group?.monthly_limit_usd) {
    percentages.push(((sub.monthly_usage_usd || 0) / sub.group.monthly_limit_usd) * 100)
  }
  return percentages.length > 0 ? Math.max(...percentages) : 0
}

function isUnlimited(sub: UserSubscription): boolean {
  return (
    !sub.group?.daily_limit_usd &&
    !sub.group?.weekly_limit_usd &&
    !sub.group?.monthly_limit_usd
  )
}

function getProgressDotClass(sub: UserSubscription): string {
  if (isUnlimited(sub)) {
    return 'bg-emerald-500'
  }
  const maxPercentage = getMaxUsagePercentage(sub)
  if (maxPercentage >= 90) return 'bg-red-500'
  if (maxPercentage >= 70) return 'bg-orange-500'
  return 'bg-green-500'
}

function getLimitedCreditDotClass(credit: LimitedCreditGrant): string {
  const days = getDaysUntil(credit.expires_at)
  const percentage = getLimitedCreditUsagePercentage(credit)
  if (days <= 3 || percentage >= 90) return 'bg-red-500'
  if (days <= 7 || percentage >= 70) return 'bg-orange-500'
  return 'bg-green-500'
}

function getProgressBarClass(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return 'bg-gray-400'
  const percentage = ((used || 0) / limit) * 100
  if (percentage >= 90) return 'bg-red-500'
  if (percentage >= 70) return 'bg-orange-500'
  return 'bg-green-500'
}

function getLimitedCreditBarClass(credit: LimitedCreditGrant): string {
  const percentage = getLimitedCreditUsagePercentage(credit)
  if (percentage >= 90) return 'bg-red-500'
  if (percentage >= 70) return 'bg-orange-500'
  return 'bg-emerald-500'
}

function getLimitedCreditUsagePercentage(credit: LimitedCreditGrant): number {
  if (!credit.initial_amount) return 0
  return (Number(credit.used_amount || 0) / credit.initial_amount) * 100
}

function getProgressWidth(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return '0%'
  const percentage = Math.min(((used || 0) / limit) * 100, 100)
  return `${percentage}%`
}

function formatUsage(used: number | undefined, limit: number | null | undefined): string {
  const usedValue = (used || 0).toFixed(2)
  const limitValue = limit?.toFixed(2) || '∞'
  return `$${usedValue}/$${limitValue}`
}

function formatMoney(value: number | undefined): string {
  return `$${Number(value || 0).toFixed(2)}`
}

function getDaysUntil(expiresAt: string): number {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diff = expires.getTime() - now.getTime()
  return Math.ceil(diff / (1000 * 60 * 60 * 24))
}

function formatDaysRemaining(expiresAt: string): string {
  const days = getDaysUntil(expiresAt)
  if (days < 0) return t('subscriptionProgress.expired')
  if (days === 0) return t('subscriptionProgress.expiresToday')
  if (days === 1) return t('subscriptionProgress.expiresTomorrow')
  return t('subscriptionProgress.daysRemaining', { days })
}

function getDaysRemainingClass(expiresAt: string): string {
  const days = getDaysUntil(expiresAt)
  if (days <= 3) return 'text-red-600 dark:text-red-400'
  if (days <= 7) return 'text-orange-600 dark:text-orange-400'
  return 'text-gray-500 dark:text-dark-400'
}

function toggleTooltip() {
  tooltipOpen.value = !tooltipOpen.value
}

function closeTooltip() {
  tooltipOpen.value = false
}

function handleClickOutside(event: MouseEvent) {
  if (containerRef.value && !containerRef.value.contains(event.target as Node)) {
    closeTooltip()
  }
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  subscriptionStore.fetchActiveSubscriptions().catch((error) => {
    console.error('Failed to load subscriptions in SubscriptionProgressMini:', error)
  })
  limitedCreditStore.fetchActiveLimitedCredits().catch((error) => {
    console.error('Failed to load limited credits in SubscriptionProgressMini:', error)
  })
})

onBeforeUnmount(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>

<style scoped>
.dropdown-enter-active,
.dropdown-leave-active {
  transition: all 0.2s ease;
}

.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
  transform: scale(0.95) translateY(-4px);
}
</style>