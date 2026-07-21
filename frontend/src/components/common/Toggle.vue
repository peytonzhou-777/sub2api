<template>
  <button
    type="button"
    @click="toggle"
    class="codex-switch"
    :class="modelValue ? 'codex-switch-on' : 'codex-switch-off'"
    role="switch"
    :aria-checked="modelValue"
  >
    <span
      class="codex-switch-thumb pointer-events-none"
      :class="{ 'codex-switch-thumb-on': modelValue }"
    />
  </button>
</template>

<script setup lang="ts">
const props = defineProps<{
  modelValue: boolean
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
}>()

function toggle() {
  emit('update:modelValue', !props.modelValue)
}
</script>
