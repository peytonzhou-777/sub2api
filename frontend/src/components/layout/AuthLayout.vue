<template>
  <div class="codex-auth relative flex min-h-screen items-center justify-center overflow-hidden p-4">
    <CodexBackgroundVideo class="pointer-events-none absolute inset-0" />
    <div class="pointer-events-none absolute inset-0 bg-black/45 backdrop-blur-[2px]"></div>

    <!-- Content Container -->
    <div class="relative z-10 w-full max-w-md">
      <!-- Logo/Brand -->
      <div class="mb-7 text-center text-white">
        <!-- Custom Logo or Default Logo -->
        <template v-if="settingsLoaded">
          <div
            class="mb-4 inline-flex h-14 w-14 items-center justify-center overflow-hidden rounded-xl"
          >
            <img :src="siteLogo || '/logo.svg'" alt="Logo" class="h-full w-full object-contain" />
          </div>
          <h1 class="mb-2 text-3xl font-medium tracking-[-0.03em]">
            <SiteWordmark :name="siteName" />
          </h1>
          <p class="text-sm text-white/75">
            {{ siteSubtitle }}
          </p>
        </template>
      </div>

      <!-- Card Container -->
      <div class="rounded-[14px] border border-white/15 bg-[#151515]/95 p-8 text-white shadow-2xl shadow-black/40 backdrop-blur-xl">
        <slot />
      </div>

      <!-- Footer Links -->
      <div class="mt-6 text-center text-sm text-white/70">
        <slot name="footer" />
      </div>

      <!-- Copyright -->
      <div class="mt-8 text-center text-xs text-white/55">
        &copy; {{ currentYear }} <SiteWordmark :name="siteName" />. All rights reserved.
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useAppStore } from '@/stores'
import { sanitizeUrl } from '@/utils/url'
import CodexBackgroundVideo from '@/components/public/CodexBackgroundVideo.vue'
import SiteWordmark from '@/components/common/SiteWordmark.vue'

const appStore = useAppStore()

const siteName = computed(() => appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || 'Subscription to API Conversion Platform')
const settingsLoaded = computed(() => appStore.publicSettingsLoaded)

const currentYear = computed(() => new Date().getFullYear())

onMounted(() => {
  appStore.fetchPublicSettings()
})
</script>
