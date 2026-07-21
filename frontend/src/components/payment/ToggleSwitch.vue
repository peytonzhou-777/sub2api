<template>
  <label class="flex flex-col items-center gap-0.5 cursor-pointer">
    <span class="text-xs text-gray-500 dark:text-gray-400 whitespace-nowrap">{{ label }}</span>
    <button
      type="button"
      role="switch"
      :aria-checked="checked"
      @click="emit('toggle')"
      class="codex-switch"
      :class="checked ? 'codex-switch-on' : 'codex-switch-off'"
    >
      <span
        class="codex-switch-thumb pointer-events-none"
        :class="{ 'codex-switch-thumb-on': checked }"
      />
    </button>
  </label>
</template>

<script setup lang="ts">
defineProps<{ label: string; checked: boolean }>()
const emit = defineEmits<{ toggle: [] }>()
</script>
