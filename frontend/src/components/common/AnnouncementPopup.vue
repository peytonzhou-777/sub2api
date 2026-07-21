<template>
  <Teleport to="body">
    <Transition name="announcement-popup">
      <div
        v-if="announcementStore.currentPopup"
        class="announcement-popup-overlay"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        @click.self="handleDismiss"
      >
        <section class="announcement-popup-panel" @click.stop>
          <div class="announcement-popup-glow" aria-hidden="true"></div>

          <header class="announcement-popup-header">
            <div class="announcement-popup-heading">
              <div class="announcement-popup-icon" aria-hidden="true">
                <Icon name="bell" size="md" />
              </div>
              <div class="min-w-0">
                <div class="announcement-popup-eyebrow">
                  <span class="announcement-popup-dot" aria-hidden="true"></span>
                  {{ t('announcements.title') }}
                </div>
                <h2 :id="titleId" class="announcement-popup-title">
                  {{ announcementStore.currentPopup.title }}
                </h2>
              </div>
            </div>

            <button
              type="button"
              class="announcement-popup-close"
              :aria-label="t('common.close')"
              @click="handleDismiss"
            >
              <Icon name="x" size="sm" />
            </button>
          </header>

          <div class="announcement-popup-meta">
            <Icon name="clock" size="sm" aria-hidden="true" />
            <time>{{ formatRelativeWithDateTime(announcementStore.currentPopup.created_at) }}</time>
            <span class="announcement-popup-meta-separator" aria-hidden="true"></span>
            <span class="announcement-popup-unread">{{ t('announcements.unread') }}</span>
          </div>

          <div class="announcement-popup-body">
            <div
              class="announcement-popup-content markdown-body"
              v-html="renderedContent"
            ></div>
          </div>

          <footer class="announcement-popup-footer">
            <p>{{ t('announcements.markReadHint') }}</p>
            <button type="button" class="btn btn-primary announcement-popup-action" @click="handleDismiss">
              <Icon name="check" size="sm" aria-hidden="true" />
              {{ t('announcements.markRead') }}
            </button>
          </footer>
        </section>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import Icon from '@/components/icons/Icon.vue'
import { useAnnouncementStore } from '@/stores/announcements'
import { formatRelativeWithDateTime } from '@/utils/format'

const { t } = useI18n()
const announcementStore = useAnnouncementStore()
const titleId = 'announcement-popup-title'

marked.setOptions({
  breaks: true,
  gfm: true,
})

const renderedContent = computed(() => {
  const content = announcementStore.currentPopup?.content
  if (!content) return ''
  const html = marked.parse(content) as string
  return DOMPurify.sanitize(html)
})

// 关闭公告时沿用公告队列逻辑，并将当前公告标记为已读。
function handleDismiss() {
  announcementStore.dismissPopup()
}

function handleEscape(event: KeyboardEvent) {
  if (event.key === 'Escape' && announcementStore.currentPopup) {
    handleDismiss()
  }
}

watch(
  () => announcementStore.currentPopup,
  (popup) => {
    document.body.classList.toggle('announcement-popup-open', Boolean(popup))
  },
  { immediate: true }
)

onMounted(() => document.addEventListener('keydown', handleEscape))

onBeforeUnmount(() => {
  document.removeEventListener('keydown', handleEscape)
  document.body.classList.remove('announcement-popup-open')
})
</script>

<style scoped>
.announcement-popup-overlay {
  position: fixed;
  inset: 0;
  z-index: 120;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow-y: auto;
  padding: 24px;
  background: rgb(0 0 0 / 0.68);
  backdrop-filter: blur(10px) saturate(0.9);
}

.announcement-popup-panel {
  position: relative;
  width: min(100%, 660px);
  max-height: min(760px, calc(100vh - 48px));
  overflow: hidden;
  border: 1px solid var(--codex-overlay-border);
  border-radius: 18px;
  background: linear-gradient(145deg, rgb(45 45 49 / 0.92), rgb(25 25 28 / 0.94));
  color: var(--codex-text);
  box-shadow: var(--codex-overlay-highlight), 0 28px 90px rgb(0 0 0 / 0.62);
  backdrop-filter: blur(28px) saturate(1.18);
}

.announcement-popup-glow {
  position: absolute;
  top: -160px;
  right: -120px;
  width: 390px;
  height: 300px;
  border-radius: 50%;
  background: radial-gradient(circle, rgb(181 140 255 / 0.2) 0%, rgb(120 184 255 / 0.1) 42%, transparent 72%);
  filter: blur(16px);
  pointer-events: none;
}

.announcement-popup-header {
  position: relative;
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 20px;
  padding: 26px 28px 16px;
}

.announcement-popup-heading { display: flex; min-width: 0; align-items: flex-start; gap: 14px; }

.announcement-popup-icon {
  display: flex;
  width: 40px;
  height: 40px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border: 1px solid rgb(120 184 255 / 0.22);
  border-radius: 11px;
  background: rgb(120 184 255 / 0.1);
  color: var(--codex-accent-blue);
}

.announcement-popup-eyebrow {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 7px;
  color: var(--codex-accent-purple);
  font-size: 12px;
  font-weight: 500;
  letter-spacing: 0.04em;
}

.announcement-popup-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--codex-accent-purple);
  box-shadow: 0 0 12px rgb(181 140 255 / 0.75);
}

.announcement-popup-title {
  margin: 0;
  color: var(--codex-text);
  font-size: clamp(20px, 3vw, 25px);
  font-weight: 600;
  line-height: 1.25;
  letter-spacing: -0.02em;
}

.announcement-popup-close {
  display: flex;
  width: 34px;
  height: 34px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  border-radius: 9px;
  color: var(--codex-text-muted);
  transition: background var(--codex-fast), color var(--codex-fast), transform var(--codex-fast);
}

.announcement-popup-close:hover { background: rgb(255 255 255 / 0.08); color: var(--codex-text); }
.announcement-popup-close:active { transform: scale(0.95); }

.announcement-popup-meta {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 0 28px 20px 82px;
  color: var(--codex-text-muted);
  font-size: 12px;
}

.announcement-popup-meta-separator { width: 1px; height: 12px; background: var(--codex-seam); }
.announcement-popup-unread { color: var(--codex-accent-blue); }

.announcement-popup-body {
  position: relative;
  max-height: min(50vh, 430px);
  overflow-y: auto;
  border-block: 1px solid var(--codex-seam);
  padding: 26px 28px;
  background: rgb(15 15 17 / 0.48);
}

.announcement-popup-content { color: #d1d1d5; font-size: 14px; line-height: 1.75; }
.announcement-popup-content :deep(> :first-child) { margin-top: 0; }
.announcement-popup-content :deep(> :last-child) { margin-bottom: 0; }
.announcement-popup-content :deep(a) { color: var(--codex-accent-blue); }
.announcement-popup-content :deep(strong),
.announcement-popup-content :deep(h1),
.announcement-popup-content :deep(h2),
.announcement-popup-content :deep(h3) { color: var(--codex-text); }
.announcement-popup-content :deep(blockquote) { border-left-color: var(--codex-accent-purple); background: rgb(181 140 255 / 0.07); }
.announcement-popup-content :deep(code) { background: rgb(255 255 255 / 0.08); color: #e5b6ff; }

.announcement-popup-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  padding: 16px 20px 16px 28px;
}

.announcement-popup-footer p { margin: 0; color: var(--codex-text-faint); font-size: 12px; }
.announcement-popup-action { display: inline-flex; min-width: 126px; align-items: center; justify-content: center; gap: 8px; }

.announcement-popup-body::-webkit-scrollbar { width: 6px; }
.announcement-popup-body::-webkit-scrollbar-track { background: transparent; }
.announcement-popup-body::-webkit-scrollbar-thumb { border-radius: 999px; background: rgb(255 255 255 / 0.18); }

.announcement-popup-enter-active,
.announcement-popup-leave-active { transition: opacity 180ms var(--codex-ease); }
.announcement-popup-enter-active .announcement-popup-panel,
.announcement-popup-leave-active .announcement-popup-panel { transition: transform 220ms var(--codex-ease), opacity 180ms var(--codex-ease); }
.announcement-popup-enter-from,
.announcement-popup-leave-to { opacity: 0; }
.announcement-popup-enter-from .announcement-popup-panel,
.announcement-popup-leave-to .announcement-popup-panel { opacity: 0; transform: translateY(12px) scale(0.975); }

@media (max-width: 640px) {
  .announcement-popup-overlay { align-items: flex-end; padding: 12px; }
  .announcement-popup-panel { max-height: calc(100vh - 24px); border-radius: 16px; }
  .announcement-popup-header { padding: 22px 20px 14px; }
  .announcement-popup-meta { padding: 0 20px 18px 74px; }
  .announcement-popup-body { padding: 22px 20px; }
  .announcement-popup-footer { align-items: stretch; flex-direction: column; padding: 14px 20px 18px; }
  .announcement-popup-action { width: 100%; }
}

@media (prefers-reduced-motion: reduce) {
  .announcement-popup-enter-active,
  .announcement-popup-leave-active,
  .announcement-popup-enter-active .announcement-popup-panel,
  .announcement-popup-leave-active .announcement-popup-panel { transition-duration: 1ms; }
}
</style>

<style>
body.announcement-popup-open { overflow: hidden; }
</style>
