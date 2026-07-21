import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(__dirname, '../HomeView.vue'), 'utf8')

describe('HomeView 公共导航与页脚', () => {
  it('不在顶部导航重复展示站点品牌', () => {
    expect(source).not.toContain('class="codex-public-brand"')
  })

  it('删除品牌后仍将导航操作区保持在右侧', () => {
    const styles = readFileSync(resolve(__dirname, '../../styles/codex/public-shell.css'), 'utf8')
    expect(styles).toMatch(/\.codex-public-nav\s*\{[\s\S]*?justify-content:\s*flex-end;/)
  })

  it('不在页脚展示 GitHub 固定外链', () => {
    expect(source).not.toContain('https://github.com/Wei-Shaw/sub2api')
    expect(source).not.toContain('>GitHub</a>')
  })

  it('展示站点信息并重构首张信息卡', () => {
    expect(source).toContain('<p class="codex-section-kicker">站点信息</p>')
    expect(source).not.toContain('<h2>站点信息</h2>')
    expect(source).toContain("title: 'Codex 专营', description: '专注 ChatGPT 账号，营造 Codex 编程社区，随时分享实用经验。'")
    expect(source).not.toContain("t('home.features.unifiedGatewayDesc')")
    expect(source).toContain("title: '透明定价'")
    expect(source).toContain("description: '无任何计价套路，通过与官方接口的计价倍率即可一眼比价。'")
    expect(source).not.toContain("t('home.features.multiAccountDesc')")
    expect(source).toContain("title: '稳定响应'")
    expect(source).toContain("description: '美国独立服务器 + CloudFlare 优选线路，全球可达，保障响应质量。'")
    expect(source).not.toContain("t('home.features.balanceQuotaDesc')")
    const styles = readFileSync(resolve(__dirname, '../../styles/codex/public-shell.css'), 'utf8')
    expect(styles).toContain('.codex-feature h3 { margin: 0 0 12px; color: var(--codex-accent-blue);')
    expect(source).toContain("{ icon: 'openAI' as const, title: 'Codex 专营'")
    expect(source).toContain("{ icon: 'dollar' as const, title: '透明定价'")
    expect(source).toContain("{ icon: 'cloud' as const, title: '稳定响应'")
    expect(styles).toContain('margin-bottom: 52px;')
    expect(styles).toContain('margin-top: 40px;')
  })

  it('将重置返利主视觉前置并移除旧模型标签', () => {
    const showcaseIndex = source.indexOf('<ResetRebateShowcase />')
    const siteInfoIndex = source.indexOf('<p class="codex-section-kicker">站点信息</p>')

    expect(source).toContain("import ResetRebateShowcase from '@/components/public/ResetRebateShowcase.vue'")
    expect(showcaseIndex).toBeGreaterThan(-1)
    expect(siteInfoIndex).toBeGreaterThan(showcaseIndex)
    expect(source).not.toContain('codex-provider-list')
    expect(source).not.toContain('const providers = computed')
    expect(source).not.toContain("t('home.providers.title')")
  })
})
