<template>
  <div class="rounded-lg bg-gray-50 p-3 dark:bg-dark-700/60">
    <p class="text-xs text-gray-500 dark:text-dark-300">{{ label }}</p>
    <p class="mt-1 text-lg font-semibold text-gray-900 dark:text-white">{{ value }}</p>
  </div>
</template>

<script setup lang="ts">
defineProps<{ label: string; value: string }>()
</script>
