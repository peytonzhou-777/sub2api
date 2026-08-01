import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(__dirname, '../ResetRebateShowcase.vue'), 'utf8')
const styles = readFileSync(resolve(__dirname, '../../../styles/codex/public-shell.css'), 'utf8')

describe('ResetRebateShowcase', () => {
  it('展示已确认的核心宣传文案与无金额流程', () => {
    expect(source).toContain('站点特色活动')
    expect(source).toContain('重置同享')
    expect(source).not.toContain('官方重置，额度同享')
    expect(source).toContain('最高')
    expect(source).toContain('80%')
    expect(source).toContain('当 Codex 官方提供额度重置机会，本站将依据本周期实际消耗，按活动比例返还调用额度。（指定分组）')
    expect(source).toContain('官方重置机会')
    expect(source).toContain('统计实际消耗')
    expect(source).toContain('返还限时额度')
    expect(source).toContain('根据实际消耗')
    expect(source).toContain('以调用记录为准')
    expect(source).toContain('按照活动比例折算')
    expect(source).not.toContain('以已完成的调用记录为准')
    expect(source).not.toContain('按当期活动比例自动计算')
    expect(source).not.toContain('可用 2 次')
    expect(source).toContain('<span>可用</span>')
    expect(source).not.toMatch(/\$\d+/)
  })

  it('不提供尚未上线的领取交互', () => {
    expect(source).not.toContain('<button')
    expect(source).not.toContain('<router-link')
    expect(source).not.toContain('一键领取')
  })

  it('提供桌面、窄屏与减少动态效果样式', () => {
    expect(styles).toContain('.codex-rebate-showcase {')
    expect(styles).toContain('grid-template-columns: minmax(0, .78fr) minmax(600px, 1.22fr);')
    expect(styles).toContain('.codex-rebate-stage-title { margin: 14px 0 0; color: #ededf0; font-size: 17px; font-weight: 500; line-height: 1.3; white-space: nowrap; }')
    expect(styles).toContain('.codex-rebate-reset-row strong { font-weight: 500; line-height: 1.3; white-space: nowrap; }')
    expect(styles).toContain('.codex-rebate-stage-official .codex-rebate-stage-label { text-transform: none; }')
    expect(styles).toContain('.codex-rebate-stage-official .codex-rebate-stage-title { text-align: left; }')
    expect(styles).toContain('padding-right: .12em;')
    expect(styles).toContain('inset: -18% 0 auto 12%;')
    expect(styles).toContain('.codex-rebate-showcase:hover .codex-rebate-flow { animation-play-state: paused; }')
    expect(styles).toContain('@keyframes codex-rebate-stage')
    expect(styles).toMatch(/@media \(max-width: 767px\)[\s\S]*?\.codex-rebate-showcase\s*\{[\s\S]*?grid-template-columns:\s*1fr;/)
    expect(styles).toMatch(/@media \(prefers-reduced-motion: reduce\)[\s\S]*?\.codex-rebate-flow[\s\S]*?animation:\s*none;/)
  })
})
