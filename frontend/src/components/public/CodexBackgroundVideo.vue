<template>
  <div ref="rootRef" class="absolute inset-0">
    <img
      v-if="showPoster"
      class="h-full w-full object-cover"
      src="/assets/codex/floral-a-poster.webp"
      alt=""
      aria-hidden="true"
    />
    <video
      v-else
      ref="videoRef"
      class="h-full w-full object-cover"
      src="/assets/codex/floral-a.mp4"
      poster="/assets/codex/floral-a-poster.webp"
      autoplay
      muted
      loop
      playsinline
      aria-hidden="true"
      @error="showPoster = true"
    />
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'

const rootRef = ref<HTMLElement | null>(null)
const videoRef = ref<HTMLVideoElement | null>(null)
const showPoster = ref(false)
let observer: IntersectionObserver | null = null

// 根据系统动效、节省流量和页面可见性自动控制视频播放。
function syncPlayback() {
  const video = videoRef.value
  if (!video || showPoster.value) return
  if (document.hidden) {
    video.pause()
    return
  }
  void video.play().catch(() => {
    showPoster.value = true
  })
}

onMounted(() => {
  const connection = (navigator as Navigator & { connection?: { saveData?: boolean } }).connection
  showPoster.value = window.matchMedia('(prefers-reduced-motion: reduce)').matches || Boolean(connection?.saveData)
  document.addEventListener('visibilitychange', syncPlayback)

  if (!showPoster.value && rootRef.value) {
    observer = new IntersectionObserver(([entry]) => {
      const video = videoRef.value
      if (!video) return
      if (entry.isIntersecting) syncPlayback()
      else video.pause()
    }, { threshold: 0.08 })
    observer.observe(rootRef.value)
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  document.removeEventListener('visibilitychange', syncPlayback)
})
</script>
