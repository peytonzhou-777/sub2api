<template>
  <div v-if="homeContent" class="min-h-screen">
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    />
    <div v-else v-html="homeContent" />
  </div>

  <div v-else class="codex-public">
    <header class="codex-public-nav" :class="{ 'is-solid': navSolid }">
      <nav class="codex-public-actions" aria-label="Main navigation">
        <LocaleSwitcher />
        <a
          v-if="docUrl"
          :href="docUrl"
          target="_blank"
          rel="noopener noreferrer"
          class="codex-public-icon-button codex-desktop-only"
          :title="t('home.viewDocs')"
        >
          <Icon name="book" size="md" />
        </a>
        <router-link :to="isAuthenticated ? dashboardPath : '/login'" class="codex-public-pill">
          {{ isAuthenticated ? t('home.dashboard') : t('home.login') }}
          <Icon name="arrowRight" size="sm" />
        </router-link>
      </nav>
    </header>

    <main>
      <section class="codex-hero">
        <CodexBackgroundVideo class="codex-hero-media" />
        <div class="codex-hero-content codex-reveal">
          <div class="codex-hero-brand-block">
            <div class="codex-hero-logo">
              <img :src="siteLogo || '/logo.svg'" alt="" class="h-full w-full object-contain" />
            </div>
            <h1><SiteWordmark :name="siteName" /></h1>
            <p>{{ siteSubtitle }}</p>
            <router-link :to="isAuthenticated ? dashboardPath : '/login'" class="codex-public-pill codex-reveal-delay">
              {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
              <Icon name="arrowRight" size="sm" />
            </router-link>
          </div>
          <HeroSloganCarousel />
        </div>
      </section>

      <ResetRebateShowcase />

      <section class="codex-section">
        <div class="codex-section-inner">
          <p class="codex-section-kicker">站点信息</p>

          <div class="codex-feature-grid">
            <article v-for="feature in features" :key="feature.title" class="codex-feature">
              <div class="codex-feature-icon"><Icon :name="feature.icon" size="md" /></div>
              <h3>{{ feature.title }}</h3>
              <p v-if="feature.description">{{ feature.description }}</p>
            </article>
          </div>
        </div>
      </section>



    </main>

    <footer class="codex-footer">
      <div class="codex-footer-inner">
        <p>&copy; {{ currentYear }} <SiteWordmark :name="siteName" />. {{ t('home.footer.allRightsReserved') }}</p>
        <div v-if="docUrl" class="codex-footer-links">
          <a :href="docUrl" target="_blank" rel="noopener noreferrer">{{ t('home.docs') }}</a>
        </div>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'
import CodexBackgroundVideo from '@/components/public/CodexBackgroundVideo.vue'
import HeroSloganCarousel from '@/components/public/HeroSloganCarousel.vue'
import ResetRebateShowcase from '@/components/public/ResetRebateShowcase.vue'
import SiteWordmark from '@/components/common/SiteWordmark.vue'
import { sanitizeUrl } from '@/utils/url'

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const navSolid = ref(false)

const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || 'AI API Gateway Platform')
const docUrl = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.doc_url || appStore.docUrl || ''))
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')
const isHomeContentUrl = computed(() => /^https?:\/\//i.test(homeContent.value.trim()))
const isAuthenticated = computed(() => authStore.isAuthenticated)
const dashboardPath = computed(() => authStore.isAdmin ? '/admin/dashboard' : '/dashboard')
const currentYear = new Date().getFullYear()

const features = computed(() => [
  { icon: 'openAI' as const, title: 'Codex 专营', description: '专注 ChatGPT 账号，营造 Codex 编程社区，随时分享实用经验。' },
  { icon: 'dollar' as const, title: '透明定价', description: '无任何计价套路，通过与官方接口的计价倍率即可一眼比价。' },
  { icon: 'cloud' as const, title: '稳定响应', description: '美国独立服务器 + CloudFlare 优选线路，全球可达，保障响应质量。' },
])

function updateNavSurface() {
  navSolid.value = window.scrollY > 36
}

onMounted(() => {
  appStore.fetchPublicSettings()
  updateNavSurface()
  window.addEventListener('scroll', updateNavSurface, { passive: true })
})

onBeforeUnmount(() => window.removeEventListener('scroll', updateNavSurface))
</script>
