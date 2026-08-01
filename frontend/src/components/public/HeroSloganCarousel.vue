<template>
  <section class="codex-slogan" aria-label="选择本站理由">
    <ul class="codex-slogan-list">
      <li
        v-for="(slogan, index) in slogans"
        :key="slogan.join('-')"
        class="codex-slogan-item"
        :style="{ '--slogan-delay': `${index * 180}ms` }"
        :aria-label="slogan.join('、')"
      >
        <span class="codex-slogan-stage" aria-hidden="true">
          <span class="codex-slogan-word codex-slogan-word-primary">
            {{ slogan[0] }}
          </span>
          <span class="codex-slogan-word codex-slogan-word-secondary">
            {{ slogan[1] }}
          </span>
        </span>
      </li>
    </ul>
  </section>
</template>

<script setup lang="ts">
// 每个固定槽位独立循环两条标语，通过错峰形成参考页的滚动节奏。
const slogans = [
  ['省心注册', '一键接入'],
  ['额度同享', '按量计费'],
  ['全球加速', '亚洲优选'],
  ['定价透明', '安心调用'],
] as const
</script>
