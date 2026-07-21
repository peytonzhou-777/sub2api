<template>
  <div class="relative" ref="containerRef">
    <button
      type="button"
      @click="toggle"
      :class="['date-picker-trigger', isOpen && 'date-picker-trigger-open']"
    >
      <span class="date-picker-icon">
        <Icon name="calendar" size="sm" />
      </span>
      <span class="date-picker-value">
        {{ displayValue }}
      </span>
      <span class="date-picker-chevron">
        <Icon
          name="chevronDown"
          size="sm"
          :class="['transition-transform duration-200', isOpen && 'rotate-180']"
        />
      </span>
    </button>

    <!-- 浮层传送到 body，避免被后续卡片或局部 stacking context 遮挡。 -->
    <Teleport to="body">
      <Transition name="date-picker-dropdown">
        <div
          v-if="isOpen"
          ref="dropdownRef"
          class="date-picker-dropdown"
          :style="dropdownStyle"
          @click.stop
        >
          <!-- Quick presets -->
          <div class="date-picker-presets">
            <button
              v-for="preset in presets"
              :key="preset.value"
              @click="selectPreset(preset)"
              :class="['date-picker-preset', isPresetActive(preset) && 'date-picker-preset-active']"
            >
              {{ t(preset.labelKey) }}
            </button>
          </div>

          <div class="date-picker-divider"></div>

          <!-- Custom date range inputs -->
          <div class="date-picker-custom">
            <div class="date-picker-field">
              <label class="date-picker-label">{{ t('dates.startDate') }}</label>
              <input
                type="date"
                v-model="localStartDate"
                :max="localEndDate || tomorrow"
                class="date-picker-input"
                @change="onDateChange"
              />
            </div>
            <div class="date-picker-separator">
              <Icon name="arrowRight" size="sm" class="text-gray-400" />
            </div>
            <div class="date-picker-field">
              <label class="date-picker-label">{{ t('dates.endDate') }}</label>
              <input
                type="date"
                v-model="localEndDate"
                :min="localStartDate"
                :max="tomorrow"
                class="date-picker-input"
                @change="onDateChange"
              />
            </div>
          </div>

          <!-- Apply button -->
          <div class="date-picker-actions">
            <button @click="apply" class="date-picker-apply">
              {{ t('dates.apply') }}
            </button>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

interface DatePreset {
  labelKey: string
  value: string
  getRange: () => { start: string; end: string }
}

interface Props {
  startDate: string
  endDate: string
}

interface Emits {
  (e: 'update:startDate', value: string): void
  (e: 'update:endDate', value: string): void
  (e: 'change', range: { startDate: string; endDate: string; preset: string | null }): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

const { t, locale } = useI18n()

const isOpen = ref(false)
const containerRef = ref<HTMLElement | null>(null)
const dropdownRef = ref<HTMLElement | null>(null)
const triggerRect = ref<DOMRect | null>(null)
const dropdownPosition = ref<'bottom' | 'top'>('bottom')
const localStartDate = ref(props.startDate)
const localEndDate = ref(props.endDate)
const activePreset = ref<string | null>('last24Hours')

const dropdownStyle = computed(() => {
  if (!triggerRect.value) return {}

  const rect = triggerRect.value
  const style: Record<string, string> = {
    position: 'fixed',
    left: `${Math.min(rect.left, Math.max(8, window.innerWidth - 328))}px`,
    zIndex: '100000020'
  }

  if (dropdownPosition.value === 'top') {
    style.bottom = `${window.innerHeight - rect.top + 8}px`
  } else {
    style.top = `${rect.bottom + 8}px`
  }

  return style
})

const today = computed(() => {
  // Use local timezone to avoid UTC timezone issues
  const now = new Date()
  const year = now.getFullYear()
  const month = String(now.getMonth() + 1).padStart(2, '0')
  const day = String(now.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
})

// Tomorrow's date - used for max date to handle timezone differences
// When user is in a timezone behind the server, "today" on server might be "tomorrow" locally
const tomorrow = computed(() => {
  const d = new Date()
  d.setDate(d.getDate() + 1)
  return formatDateToString(d)
})

// Helper function to format date to YYYY-MM-DD using local timezone
const formatDateToString = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

const presets: DatePreset[] = [
  {
    labelKey: 'dates.today',
    value: 'today',
    getRange: () => {
      const t = today.value
      return { start: t, end: t }
    }
  },
  {
    labelKey: 'dates.yesterday',
    value: 'yesterday',
    getRange: () => {
      const d = new Date()
      d.setDate(d.getDate() - 1)
      const yesterday = formatDateToString(d)
      return { start: yesterday, end: yesterday }
    }
  },
  {
    labelKey: 'dates.last24Hours',
    value: 'last24Hours',
    getRange: () => {
      const end = new Date()
      const start = new Date(end.getTime() - 24 * 60 * 60 * 1000)
      return {
        start: formatDateToString(start),
        end: formatDateToString(end)
      }
    }
  },
  {
    labelKey: 'dates.last7Days',
    value: '7days',
    getRange: () => {
      const end = today.value
      const d = new Date()
      d.setDate(d.getDate() - 6)
      const start = formatDateToString(d)
      return { start, end }
    }
  },
  {
    labelKey: 'dates.last14Days',
    value: '14days',
    getRange: () => {
      const end = today.value
      const d = new Date()
      d.setDate(d.getDate() - 13)
      const start = formatDateToString(d)
      return { start, end }
    }
  },
  {
    labelKey: 'dates.last30Days',
    value: '30days',
    getRange: () => {
      const end = today.value
      const d = new Date()
      d.setDate(d.getDate() - 29)
      const start = formatDateToString(d)
      return { start, end }
    }
  },
  {
    labelKey: 'dates.thisMonth',
    value: 'thisMonth',
    getRange: () => {
      const now = new Date()
      const start = formatDateToString(new Date(now.getFullYear(), now.getMonth(), 1))
      return { start, end: today.value }
    }
  },
  {
    labelKey: 'dates.lastMonth',
    value: 'lastMonth',
    getRange: () => {
      const now = new Date()
      const start = formatDateToString(new Date(now.getFullYear(), now.getMonth() - 1, 1))
      const end = formatDateToString(new Date(now.getFullYear(), now.getMonth(), 0))
      return { start, end }
    }
  }
]

const displayValue = computed(() => {
  if (activePreset.value) {
    const preset = presets.find((p) => p.value === activePreset.value)
    if (preset) return t(preset.labelKey)
  }

  if (localStartDate.value && localEndDate.value) {
    if (localStartDate.value === localEndDate.value) {
      return formatDate(localStartDate.value)
    }
    return `${formatDate(localStartDate.value)} - ${formatDate(localEndDate.value)}`
  }

  return t('dates.selectDateRange')
})

const formatDate = (dateStr: string): string => {
  const date = new Date(dateStr + 'T00:00:00')
  const dateLocale = locale.value === 'zh' ? 'zh-CN' : 'en-US'
  return date.toLocaleDateString(dateLocale, { month: 'short', day: 'numeric' })
}

const isPresetActive = (preset: DatePreset): boolean => {
  return activePreset.value === preset.value
}

const selectPreset = (preset: DatePreset) => {
  const range = preset.getRange()
  localStartDate.value = range.start
  localEndDate.value = range.end
  activePreset.value = preset.value
}

const onDateChange = () => {
  // Check if current dates match any preset
  activePreset.value = null
  for (const preset of presets) {
    const range = preset.getRange()
    if (range.start === localStartDate.value && range.end === localEndDate.value) {
      activePreset.value = preset.value
      break
    }
  }
}

const toggle = () => {
  isOpen.value = !isOpen.value
}

// 跟随触发器更新浮层位置，并在空间不足时自动向上展开。
const updateDropdownPosition = () => {
  if (!containerRef.value) return
  triggerRect.value = containerRef.value.getBoundingClientRect()

  nextTick(() => {
    if (!dropdownRef.value || !triggerRect.value) return
    const dropdownHeight = dropdownRef.value.offsetHeight || 280
    const spaceBelow = window.innerHeight - triggerRect.value.bottom
    dropdownPosition.value = spaceBelow < dropdownHeight && triggerRect.value.top > spaceBelow ? 'top' : 'bottom'
  })
}

watch(isOpen, (open) => {
  if (open) {
    updateDropdownPosition()
    window.addEventListener('scroll', updateDropdownPosition, { capture: true, passive: true })
    window.addEventListener('resize', updateDropdownPosition)
  } else {
    window.removeEventListener('scroll', updateDropdownPosition, { capture: true })
    window.removeEventListener('resize', updateDropdownPosition)
  }
})

const apply = () => {
  emit('update:startDate', localStartDate.value)
  emit('update:endDate', localEndDate.value)
  emit('change', {
    startDate: localStartDate.value,
    endDate: localEndDate.value,
    preset: activePreset.value
  })
  isOpen.value = false
}

const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as Node
  if (
    containerRef.value &&
    !containerRef.value.contains(target) &&
    !dropdownRef.value?.contains(target)
  ) {
    isOpen.value = false
  }
}

const handleEscape = (event: KeyboardEvent) => {
  if (event.key === 'Escape' && isOpen.value) {
    isOpen.value = false
  }
}

// Sync local state with props
watch(
  () => props.startDate,
  (val) => {
    localStartDate.value = val
    onDateChange()
  }
)

watch(
  () => props.endDate,
  (val) => {
    localEndDate.value = val
    onDateChange()
  }
)

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  document.addEventListener('keydown', handleEscape)
  // Initialize active preset detection
  onDateChange()
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
  document.removeEventListener('keydown', handleEscape)
  window.removeEventListener('scroll', updateDropdownPosition, { capture: true })
  window.removeEventListener('resize', updateDropdownPosition)
})
</script>

<style scoped>
.date-picker-trigger {
  display: flex;
  min-height: var(--codex-control);
  cursor: pointer;
  align-items: center;
  gap: 0.5rem;
  border: 1px solid var(--codex-line);
  border-radius: var(--codex-radius);
  background: #121212;
  padding: 0.5rem 0.75rem;
  color: var(--codex-text);
  font-size: 0.875rem;
  transition: background var(--codex-fast), border-color var(--codex-fast), box-shadow var(--codex-fast);
}

.date-picker-trigger:hover {
  border-color: var(--codex-line-strong);
  background: #171717;
}

.date-picker-trigger:focus-visible {
  border-color: #666;
  outline: none;
  box-shadow: 0 0 0 2px rgb(255 255 255 / 0.14);
}

.date-picker-trigger-open {
  border-color: #666;
  background: #171717;
  box-shadow: 0 0 0 2px rgb(255 255 255 / 0.14);
}

.date-picker-icon,
.date-picker-chevron { color: var(--codex-text-faint); }

.date-picker-value {
  @apply font-medium;
}

.date-picker-dropdown {
  min-width: 320px;
  overflow: hidden;
  border: 1px solid var(--codex-overlay-border);
  border-radius: var(--codex-radius);
  background: var(--codex-overlay);
  color: var(--codex-text);
  box-shadow: var(--codex-overlay-highlight), var(--codex-overlay-shadow);
  backdrop-filter: blur(18px) saturate(1.18);
}

.date-picker-presets {
  @apply grid grid-cols-2 gap-1 p-2;
}

.date-picker-preset {
  border-radius: 6px;
  padding: 0.375rem 0.75rem;
  color: var(--codex-text-muted);
  font-size: 0.75rem;
  font-weight: 500;
  transition: background var(--codex-fast), color var(--codex-fast);
}

.date-picker-preset:hover {
  background: var(--codex-panel-hover);
  color: var(--codex-text);
}

.date-picker-preset-active {
  background: #303030;
  color: var(--codex-accent-blue);
}

.date-picker-divider {
  border-top: 1px solid var(--codex-line);
}

.date-picker-custom {
  @apply flex items-end gap-2 p-3;
}

.date-picker-field {
  @apply flex-1;
}

.date-picker-label {
  @apply mb-1 block text-xs font-medium;
  color: var(--codex-text-muted);
}

.date-picker-input {
  width: 100%;
  border: 1px solid var(--codex-line);
  border-radius: 6px;
  background: #121212;
  padding: 0.375rem 0.5rem;
  color: var(--codex-text);
  font-size: 0.875rem;
}

.date-picker-input:focus {
  border-color: #666;
  outline: none;
  box-shadow: 0 0 0 2px rgb(255 255 255 / 0.14);
}

.date-picker-input::-webkit-calendar-picker-indicator {
  @apply cursor-pointer opacity-60 hover:opacity-100;
  filter: invert(0.5);
}

.dark .date-picker-input::-webkit-calendar-picker-indicator {
  filter: invert(0.7);
}

.date-picker-separator {
  @apply flex items-center justify-center pb-1;
}

.date-picker-actions {
  @apply flex justify-end p-2 pt-0;
}

.date-picker-apply {
  min-height: 34px;
  border-radius: var(--codex-pill);
  background: #f4f4f4;
  padding: 0.375rem 1rem;
  color: #0a0a0a;
  font-size: 0.875rem;
  font-weight: 500;
  transition: background var(--codex-fast);
}

.date-picker-apply:hover {
  background: #dcdcdc;
}

/* Dropdown animation */
.date-picker-dropdown-enter-active,
.date-picker-dropdown-leave-active {
  transition: all 0.2s ease;
}

.date-picker-dropdown-enter-from,
.date-picker-dropdown-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
