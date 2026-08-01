<template>
  <span class="site-wordmark">
    <span class="site-wordmark-name"><slot>{{ name }}</slot></span>
    <span class="site-wordmark-suffix" aria-label="API">API</span>
  </span>
</template>

<script setup lang="ts">
defineProps<{
  name?: string
}>()
</script>

<style scoped>
.site-wordmark {
  display: inline-flex;
  align-items: center;
  max-width: 100%;
  line-height: inherit;
  vertical-align: baseline;
}

.site-wordmark-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.site-wordmark-suffix {
  display: inline-flex;
  align-items: center;
  align-self: center;
  flex: 0 0 auto;
  margin-left: 0.18em;
  color: var(--codex-accent-purple, #a970ff);
  font-size: 0.875em;
  font-weight: 500;
  letter-spacing: -0.02em;
}
</style>
