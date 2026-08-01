<template>
  <div
    v-if="user"
    ref="footerRef"
    class="sidebar-account-footer"
    @mouseenter="cancelCloseMenus"
    @mouseleave="scheduleCloseMenus"
  >
    <Transition name="sidebar-popover">
      <div
        v-if="accountOpen"
        class="sidebar-account-menu"
        data-test="account-popover"
        @mouseenter="cancelCloseMenus"
      >
        <div class="sidebar-account-menu-head">
          <span class="sidebar-avatar sidebar-avatar-small">{{ userInitials }}</span>
          <span class="truncate font-medium">{{ displayName }}</span>
        </div>
        <router-link to="/profile" class="sidebar-account-item" @click="closeMenus">
          <Icon name="user" size="sm" />
          {{ t('nav.profile') }}
        </router-link>
        <router-link to="/keys" class="sidebar-account-item" @click="closeMenus">
          <Icon name="key" size="sm" />
          {{ t('nav.apiKeys') }}
        </router-link>
        <button v-if="showOnboardingButton" class="sidebar-account-item w-full" @click="replayGuide">
          <Icon name="refresh" size="sm" />
          {{ t('onboarding.restartTour') }}
        </button>
        <button class="sidebar-account-item w-full" @click="logout">
          <Icon name="login" size="sm" />
          {{ t('nav.logout') }}
        </button>
      </div>
    </Transition>

    <div class="sidebar-account-row">
      <button
        class="sidebar-user-trigger"
        aria-label="User Menu"
        :aria-expanded="accountOpen"
        @click="openAccountMenu"
        @mouseenter="openAccountMenu"
      >
        <span class="sidebar-avatar">
          <img v-if="avatarUrl" :src="avatarUrl" :alt="displayName" />
          <span v-else>{{ userInitials }}</span>
        </span>
        <span class="truncate">{{ displayName }}</span>
      </button>

      <div class="sidebar-balance-wrap group">
        <button class="sidebar-balance-trigger" data-test="sidebar-balance" @click="openBalanceMenu" @mouseenter="openBalanceMenu">
          <Icon name="dollar" size="sm" />
          <span data-test="header-balance">{{ balanceDisplayText }}</span>
          <span v-if="limitedCreditSignal" data-test="limited-credit-signals" class="inline-flex">
            <span data-test="limited-credit-signal" class="h-1.5 w-1.5 rounded-full" :class="limitedCreditSignal" />
          </span>
        </button>
        <Transition name="sidebar-popover">
          <div
            v-show="balanceOpen"
            class="sidebar-balance-popover"
            data-test="balance-popover"
            @mouseenter="cancelCloseMenus"
          >
            <div class="flex justify-between gap-4"><span>{{ ordinaryBalanceText }}</span><strong>{{ formatMoney(ordinaryBalance) }}</strong></div>
            <div v-if="limitedBalance > 0" class="mt-2 flex justify-between gap-4" data-test="limited-credit-total">
              <span class="text-[var(--codex-accent-purple)]">{{ limitedCreditText }}</span>
              <strong class="text-[var(--codex-success)]">{{ formatMoney(limitedBalance) }}</strong>
            </div>
            <div class="mt-2 flex justify-between gap-4 border-t pt-2"><span>{{ balanceTotalText }}</span><strong>{{ balanceDisplayText }}</strong></div>
            <div v-if="earliestCredit" class="mt-2 border-t pt-2" data-test="earliest-limited-credit">
              <div class="flex justify-between gap-4"><span>{{ earliestLimitedCreditText }}</span><strong class="text-[var(--codex-danger)]">{{ formatMoney(earliestCredit.remaining_amount) }}</strong></div>
              <div class="mt-1 text-right text-[11px] text-[var(--codex-text-faint)]">{{ formatExpiration(earliestCredit.expires_at) }}</div>
            </div>
            <router-link v-if="limitedBalance > 0 && paymentEnabled" to="/purchase?tab=account" class="sidebar-balance-link" data-test="limited-credit-details-link" @click="closeMenus">
              {{ viewLimitedCreditDetailsText }}
            </router-link>
          </div>
        </Transition>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore, useAuthStore, useLimitedCreditStore, useOnboardingStore } from '@/stores'
import Icon from '@/components/icons/Icon.vue'
import { formatDateOnly } from '@/utils/format'
import { getLimitedCreditSignalLevel } from '@/utils/limitedCreditStatus'

const router = useRouter()
const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const limitedCreditStore = useLimitedCreditStore()
const onboardingStore = useOnboardingStore()
const footerRef = ref<HTMLElement | null>(null)
const accountOpen = ref(false)
const balanceOpen = ref(false)
let closeTimer: ReturnType<typeof setTimeout> | null = null

const user = computed(() => authStore.user)
const displayName = computed(() => user.value?.username || user.value?.email?.split('@')[0] || '')
const userInitials = computed(() => displayName.value.slice(0, 2).toUpperCase())
const avatarUrl = computed(() => user.value?.avatar_url?.trim() || '')
const ordinaryBalance = computed(() => Number(user.value?.balance || 0))
const limitedBalance = computed(() => limitedCreditStore.remainingAmount)
const totalBalance = computed(() => ordinaryBalance.value + limitedBalance.value)
const paymentEnabled = computed(() => appStore.cachedPublicSettings?.payment_enabled !== false)
const showOnboardingButton = computed(() => !authStore.isSimpleMode && user.value?.role === 'admin')
const ordinaryBalanceText = computed(() => t('common.ordinaryBalance'))
const limitedCreditText = computed(() => t('common.limitedBalance'))
const balanceTotalText = computed(() => t('common.totalBalance'))
const earliestLimitedCreditText = computed(() => t('common.earliestExpiringLimitedBalance'))
const viewLimitedCreditDetailsText = computed(() => t('common.viewLimitedBalanceDetails'))
const balanceDisplayText = computed(() => formatMoney(totalBalance.value))
const earliestCredit = computed(() => [...limitedCreditStore.activeCredits].sort((a, b) => new Date(a.expires_at).getTime() - new Date(b.expires_at).getTime() || a.id - b.id)[0] ?? null)
const limitedCreditSignal = computed(() => {
  if (!earliestCredit.value) return null
  return { red: 'bg-red-500', yellow: 'bg-yellow-400', green: 'bg-green-500' }[getLimitedCreditSignalLevel(earliestCredit.value)]
})

function formatMoney(value: number) {
  return Number.isFinite(value) ? `$${value.toFixed(2)}` : '$0.00'
}

// 按浏览器时区展示最早到期限时余额日期。
function formatExpiration(expiresAt: string) {
  return t('common.expiresOn', { date: formatDateOnly(expiresAt) })
}

function closeMenus() {
  cancelCloseMenus()
  accountOpen.value = false
  balanceOpen.value = false
}

// 为触发器与悬浮弹层之间的移动保留短暂容错时间。
function scheduleCloseMenus() {
  cancelCloseMenus()
  closeTimer = setTimeout(closeMenus, 180)
}

function cancelCloseMenus() {
  if (!closeTimer) return
  clearTimeout(closeTimer)
  closeTimer = null
}

function openAccountMenu() {
  accountOpen.value = true
  balanceOpen.value = false
}

function openBalanceMenu() {
  balanceOpen.value = true
  accountOpen.value = false
}

function replayGuide() {
  closeMenus()
  onboardingStore.replay()
}

async function logout() {
  closeMenus()
  try { await authStore.logout() } catch (error) { console.error('Logout error:', error) }
  await router.push('/login')
}

function handleClickOutside(event: MouseEvent) {
  if (footerRef.value && !footerRef.value.contains(event.target as Node)) closeMenus()
}

onMounted(() => document.addEventListener('click', handleClickOutside))
onBeforeUnmount(() => {
  cancelCloseMenus()
  document.removeEventListener('click', handleClickOutside)
})
</script>

<style scoped>
.sidebar-account-footer { position: relative; z-index: 5; border-top: 1px solid var(--codex-line); padding: 6px; }
.sidebar-account-row { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: stretch; gap: 4px; min-height: 42px; }
.sidebar-user-trigger, .sidebar-balance-trigger { display: flex; min-width: 0; align-items: center; gap: 7px; border-radius: 9px; color: var(--codex-text); transition: background var(--codex-fast); }
.sidebar-user-trigger { width: 100%; min-height: 100%; padding: 5px 7px; font-size: 13px; text-align: left; }
.sidebar-user-trigger:hover, .sidebar-user-trigger[aria-expanded='true'], .sidebar-balance-trigger:hover { background: rgb(255 255 255 / .08); }
.sidebar-avatar { display: inline-flex; width: 24px; height: 24px; flex: 0 0 auto; align-items: center; justify-content: center; overflow: hidden; border-radius: 999px; background: #29485b; color: #bde8ff; font-size: 9px; }
.sidebar-avatar img { width: 100%; height: 100%; object-fit: cover; }
.sidebar-avatar-small { width: 20px; height: 20px; }
.sidebar-balance-wrap { position: static; display: flex; min-width: 0; }
.sidebar-balance-trigger { width: 100%; min-height: 100%; justify-content: flex-end; padding: 5px 7px; color: var(--codex-accent-blue); font-size: 12px; font-weight: 600; white-space: nowrap; }
.sidebar-account-menu, .sidebar-balance-popover { position: absolute; right: 6px; bottom: calc(100% + 6px); left: 6px; border: 1px solid var(--codex-overlay-border); border-radius: 13px; background: var(--codex-overlay); box-shadow: var(--codex-overlay-highlight), var(--codex-overlay-shadow); backdrop-filter: blur(22px) saturate(1.16); }
.sidebar-account-menu::after, .sidebar-balance-popover::after { position: absolute; right: 0; bottom: -7px; left: 0; height: 7px; content: ''; }
.sidebar-account-menu { padding: 5px; }
.sidebar-account-menu-head { display: flex; align-items: center; gap: 8px; margin-bottom: 4px; border-bottom: 1px solid var(--codex-line); padding: 7px 8px 9px; font-size: 13px; }
.sidebar-account-item { display: flex; min-height: 34px; align-items: center; gap: 9px; border-radius: 8px; padding: 7px 9px; color: var(--codex-text); font-size: 13px; text-align: left; }
.sidebar-account-item:hover { background: var(--codex-panel-hover); }
.sidebar-balance-popover { padding: 12px; color: var(--codex-text-muted); font-size: 12px; }
.sidebar-balance-popover > div, .sidebar-balance-link { border-color: var(--codex-line); }
.sidebar-balance-link { display: block; margin-top: 8px; border-top: 1px solid; padding-top: 8px; color: var(--codex-accent-purple); text-align: center; }
.sidebar-popover-enter-active, .sidebar-popover-leave-active { transition: opacity 140ms ease, transform 140ms ease; transform-origin: bottom; }
.sidebar-popover-enter-from, .sidebar-popover-leave-to { opacity: 0; transform: translateY(5px) scale(.98); }
</style>
