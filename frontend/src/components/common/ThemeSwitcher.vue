<template>
  <div ref="dropdownRef" class="theme-switcher">
    <button
      type="button"
      class="theme-switcher-trigger"
      :aria-label="t('common.appearance')"
      :aria-expanded="isOpen"
      aria-haspopup="menu"
      :title="t('common.appearance')"
      @click="isOpen = !isOpen"
    >
      <Icon :name="resolvedTheme === 'dark' ? 'moon' : 'sun'" size="md" />
    </button>

    <Transition name="theme-menu">
      <div
        v-if="isOpen"
        class="theme-switcher-menu"
        role="radiogroup"
        :aria-label="t('common.appearance')"
      >
        <button
          v-for="option in options"
          :key="option.value"
          type="button"
          class="theme-switcher-option"
          :class="{ 'is-selected': themeMode === option.value }"
          role="radio"
          :aria-checked="themeMode === option.value"
          @click="selectTheme(option.value)"
        >
          <Icon :name="option.icon" size="sm" />
          <span>{{ t(option.label) }}</span>
          <Icon
            v-if="themeMode === option.value"
            name="check"
            size="sm"
            class="theme-switcher-check"
          />
        </button>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { type ThemeMode, useTheme } from '@/composables/useTheme'

const { t } = useI18n()
const { themeMode, resolvedTheme, setThemeMode } = useTheme()
const dropdownRef = ref<HTMLElement | null>(null)
const isOpen = ref(false)

const options = [
  { value: 'light', label: 'common.themeLight', icon: 'sun' },
  { value: 'dark', label: 'common.themeDark', icon: 'moon' },
  { value: 'system', label: 'common.themeSystem', icon: 'monitor' },
] as const

/** 应用主题选择并关闭弹层。 */
function selectTheme(mode: ThemeMode): void {
  setThemeMode(mode)
  isOpen.value = false
}

function handleClickOutside(event: MouseEvent): void {
  if (dropdownRef.value && !dropdownRef.value.contains(event.target as Node)) {
    isOpen.value = false
  }
}

function handleEscape(event: KeyboardEvent): void {
  if (event.key === 'Escape') isOpen.value = false
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  document.addEventListener('keydown', handleEscape)
})

onBeforeUnmount(() => {
  document.removeEventListener('click', handleClickOutside)
  document.removeEventListener('keydown', handleEscape)
})
</script>

<style scoped>
.theme-switcher {
  position: relative;
  flex: none;
}

.theme-switcher-trigger {
  display: inline-flex;
  width: var(--codex-control);
  height: var(--codex-control);
  align-items: center;
  justify-content: center;
  border-radius: var(--codex-radius);
  color: inherit;
  transition: background var(--codex-fast), color var(--codex-fast);
}

.theme-switcher-trigger:hover {
  background: color-mix(in srgb, currentColor 9%, transparent);
}

.theme-switcher-menu {
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  z-index: 70;
  width: 168px;
  overflow: hidden;
  border: 1px solid var(--codex-line);
  border-radius: var(--codex-radius);
  padding: 4px;
  background: var(--codex-panel);
  box-shadow: var(--codex-overlay-shadow);
  color: var(--codex-text);
}

.theme-switcher-option {
  display: flex;
  width: 100%;
  min-height: 36px;
  align-items: center;
  gap: 10px;
  border-radius: var(--codex-radius-sm);
  padding: 0 10px;
  color: var(--codex-text-muted);
  font-size: 13px;
  font-weight: 500;
  text-align: left;
  transition: background var(--codex-fast), color var(--codex-fast);
}

.theme-switcher-option:hover,
.theme-switcher-option.is-selected {
  background: var(--codex-panel-hover);
  color: var(--codex-text);
}

.theme-switcher-option.is-selected {
  color: var(--codex-accent-blue);
}

.theme-switcher-check {
  margin-left: auto;
}

.theme-menu-enter-active,
.theme-menu-leave-active {
  transition: opacity var(--codex-fast), transform var(--codex-fast);
}

.theme-menu-enter-from,
.theme-menu-leave-to {
  opacity: 0;
  transform: translateY(-4px) scale(.98);
}
</style>
