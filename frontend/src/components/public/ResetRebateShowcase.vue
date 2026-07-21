<template>
  <section class="codex-section codex-rebate-section" aria-labelledby="codex-rebate-title">
    <div class="codex-section-inner codex-rebate-showcase">
      <div class="codex-rebate-copy">
        <p class="codex-section-kicker">站点特色活动</p>
        <h2 id="codex-rebate-title">重置同享</h2>
        <p class="codex-rebate-description">
          当 Codex 官方提供额度重置机会，本站将依据本周期实际消耗，按活动比例返还调用额度。（指定分组）
        </p>

        <div class="codex-rebate-value" aria-label="返还额度最高百分之八十">
          <span>最高</span>
          <strong>80%</strong>
          <small>返还额度</small>
        </div>

        <ul class="codex-rebate-benefits" aria-label="活动特点">
          <li><Icon name="refresh" size="sm" />官方机会触发</li>
          <li><Icon name="chart" size="sm" />根据实际消耗</li>
          <li><Icon name="gift" size="sm" />返还限时额度</li>
        </ul>
      </div>

      <div class="codex-rebate-visual">
        <div class="codex-rebate-orbit codex-rebate-orbit-blue" aria-hidden="true" />
        <div class="codex-rebate-orbit codex-rebate-orbit-purple" aria-hidden="true" />

        <!-- 三阶段动画仅解释活动机制，当前不提供领取交互。 -->
        <div
          class="codex-rebate-flow"
          role="img"
          aria-label="官方重置机会，统计实际消耗，返还限时额度"
        >
          <article class="codex-rebate-stage codex-rebate-stage-official">
            <div class="codex-rebate-stage-icon"><Icon name="refresh" size="md" /></div>
            <p class="codex-rebate-stage-label">Codex 官方</p>
            <div class="codex-rebate-reset-row">
              <strong>使用限额重置</strong>
              <span>可用</span>
              <Icon name="chevronDown" size="sm" />
            </div>
            <p class="codex-rebate-stage-title">官方重置机会</p>
          </article>

          <div class="codex-rebate-connector codex-rebate-connector-first" aria-hidden="true">
            <span class="codex-rebate-signal" />
          </div>

          <article class="codex-rebate-stage codex-rebate-stage-process">
            <div class="codex-rebate-stage-icon"><Icon name="chart" size="md" /></div>
            <p class="codex-rebate-stage-label">本周期用量</p>
            <p class="codex-rebate-stage-title">统计实际消耗</p>
            <p class="codex-rebate-stage-note">以调用记录为准</p>
          </article>

          <div class="codex-rebate-connector codex-rebate-connector-second" aria-hidden="true">
            <span class="codex-rebate-signal" />
          </div>

          <article class="codex-rebate-stage codex-rebate-stage-reward">
            <div class="codex-rebate-stage-icon"><Icon name="gift" size="md" /></div>
            <p class="codex-rebate-stage-label">活动权益</p>
            <p class="codex-rebate-stage-title">返还限时额度</p>
            <p class="codex-rebate-stage-note">按照活动比例折算</p>
          </article>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
</script>
