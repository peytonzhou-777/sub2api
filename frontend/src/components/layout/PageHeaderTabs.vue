<template>
  <nav class="page-header-tabs" role="tablist" :aria-label="tablistLabel">
    <div class="page-header-tabs-scroll">
      <button
        v-for="tab in tabs"
        :id="`${idPrefix}-${tab.key}`"
        :key="tab.key"
        type="button"
        role="tab"
        :data-test="`${testPrefix}-${tab.key}`"
        :aria-selected="activeKey === tab.key"
        :tabindex="activeKey === tab.key ? 0 : -1"
        class="page-header-tab"
        :class="{ 'page-header-tab-active': activeKey === tab.key }"
        @click="selectTab(tab.key)"
        @keydown="handleKeydown($event, tab.key)"
      >
        <Icon v-if="tab.icon" :name="tab.icon" size="sm" class="page-header-tab-icon" />
        <span class="page-header-tab-label">{{ tab.label }}</span>
      </button>
    </div>
  </nav>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'

type IconName = InstanceType<typeof Icon>['$props']['name']

export interface PageHeaderTab {
  key: string
  label: string
  icon?: IconName
}

const props = withDefaults(defineProps<{
  tabs: readonly PageHeaderTab[]
  activeKey: string
  tablistLabel: string
  idPrefix?: string
  testPrefix?: string
}>(), {
  idPrefix: 'page-header-tab',
  testPrefix: 'page-header-tab',
})

const emit = defineEmits<{ select: [key: string] }>()

const keyboardActions = {
  ArrowLeft: -1,
  ArrowUp: -1,
  ArrowRight: 1,
  ArrowDown: 1,
  Home: 'first',
  End: 'last',
} as const

// selectTab 统一派发标题栏标签切换，业务页只维护自身的活动状态。
function selectTab(key: string): void {
  emit('select', key)
}

function focusTab(key: string): void {
  window.requestAnimationFrame(() => {
    document.getElementById(`${props.idPrefix}-${key}`)?.focus()
  })
}

// handleKeydown 支持方向键、Home 和 End，保持标签组件可通过键盘完整操作。
function handleKeydown(event: KeyboardEvent, key: string): void {
  const action = keyboardActions[event.key as keyof typeof keyboardActions]
  if (action === undefined) return

  event.preventDefault()
  const currentIndex = props.tabs.findIndex(tab => tab.key === key)
  let nextIndex = currentIndex < 0 ? 0 : currentIndex

  if (action === 'first') {
    nextIndex = 0
  } else if (action === 'last') {
    nextIndex = props.tabs.length - 1
  } else {
    nextIndex = (nextIndex + action + props.tabs.length) % props.tabs.length
  }

  const nextTab = props.tabs[nextIndex]
  if (!nextTab) return
  selectTab(nextTab.key)
  focusTab(nextTab.key)
}
</script>

<style scoped>
.page-header-tabs {
  display: flex;
  min-width: 0;
  height: 100%;
  align-items: center;
  overflow: hidden;
}

.page-header-tabs-scroll {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 2px;
  overflow-x: auto;
  overscroll-behavior-inline: contain;
  scrollbar-width: none;
}

.page-header-tabs-scroll::-webkit-scrollbar { display: none; }

.page-header-tab {
  display: inline-flex;
  height: 32px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border: 1px solid transparent;
  border-radius: 7px;
  padding: 0 10px;
  color: #9b9b9b;
  font-size: 13px;
  font-weight: 450;
  line-height: 1;
  outline: none;
  transition: border-color var(--codex-fast), background-color var(--codex-fast), color var(--codex-fast);
}

.page-header-tab:hover { background: #1c1c1c; color: #e8e8e8; }
.page-header-tab:focus-visible { box-shadow: 0 0 0 2px rgb(255 255 255 / 0.18); }

.page-header-tab-active,
.page-header-tab-active:hover {
  border-color: #3a3a3a;
  background: #292929;
  color: #fff;
  box-shadow: inset 0 1px 0 rgb(255 255 255 / 0.055);
}

.page-header-tab-icon { flex: 0 0 auto; color: #777; }
.page-header-tab:hover .page-header-tab-icon,
.page-header-tab-active .page-header-tab-icon { color: currentColor; }

.page-header-tab-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 640px) {
  .page-header-tab { height: 34px; padding-inline: 9px; }
}
</style>
