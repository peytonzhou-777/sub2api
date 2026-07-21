import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AppSidebar.vue')
const componentSource = readFileSync(componentPath, 'utf8')
const stylePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../style.css')
const styleSource = readFileSync(stylePath, 'utf8')
const workbenchStylePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../styles/codex/workbench-shell.css')
const workbenchStyleSource = readFileSync(workbenchStylePath, 'utf8')

describe('AppSidebar custom SVG styles', () => {
  it('does not override uploaded SVG fill or stroke colors', () => {
    expect(componentSource).toContain('.sidebar-svg-icon {')
    expect(componentSource).toContain('color: currentColor;')
    expect(componentSource).toContain('display: block;')
    expect(componentSource).not.toContain('stroke: currentColor;')
    expect(componentSource).not.toContain('fill: none;')
  })
})

describe('AppSidebar scroll position persistence', () => {
  it('binds a template ref to the sidebar nav element', () => {
    expect(componentSource).toContain('ref="sidebarNavRef"')
    expect(componentSource).toContain('sidebar-nav')
  })

  it('declares sidebarNavRef in script setup', () => {
    expect(componentSource).toContain("const sidebarNavRef = ref<HTMLElement | null>(null)")
  })

  it('saves scroll position on beforeUnmount', () => {
    expect(componentSource).toContain('onBeforeUnmount')
    expect(componentSource).toContain('appStore.sidebarScrollTop')
    expect(componentSource).toContain('sidebarNavRef.value.scrollTop')
  })

  it('restores scroll position on mount', () => {
    expect(componentSource).toContain('onMounted')
    expect(componentSource).toContain('appStore.sidebarScrollTop')
    expect(componentSource).toContain('nextTick')
  })
})

describe('AppSidebar header styles', () => {
  it('does not clip the version badge dropdown', () => {
    const sidebarHeaderBlockMatch = styleSource.match(/\.sidebar-header\s*\{[\s\S]*?\n {2}\}/)
    const sidebarBrandBlockMatch = componentSource.match(/\.sidebar-brand\s*\{[\s\S]*?\n\}/)

    expect(sidebarHeaderBlockMatch).not.toBeNull()
    expect(sidebarBrandBlockMatch).not.toBeNull()
    expect(sidebarHeaderBlockMatch?.[0]).not.toContain('@apply overflow-hidden;')
    expect(sidebarBrandBlockMatch?.[0]).not.toContain('overflow: hidden;')
  })

  it('vertically centers the API suffix with the site name', () => {
    const suffixStyleMatch = componentSource.match(/\.sidebar-brand-suffix\s*\{[\s\S]*?\n\}/)

    expect(suffixStyleMatch).not.toBeNull()
    expect(suffixStyleMatch?.[0]).toContain('display: inline-flex;')
    expect(suffixStyleMatch?.[0]).toContain('height: 100%;')
    expect(suffixStyleMatch?.[0]).toContain('align-items: center;')
  })
})

describe('AppSidebar Codex background', () => {
  it('shares one smooth workspace glow across the sidebar and top status area', () => {
    expect(componentSource).toContain("'sidebar-collapsed': sidebarCollapsed")
    expect(workbenchStyleSource).toContain('.codex-workbench::before')
    expect(workbenchStyleSource).toContain('radial-gradient(ellipse 72% 54% at 78% -9%')
    expect(workbenchStyleSource).toContain('radial-gradient(ellipse 52% 78% at -8% 34%')
    expect(workbenchStyleSource).toContain('.codex-status-bar')
    expect(workbenchStyleSource).toContain('.codex-panel-titlebar')
    expect(workbenchStyleSource).toContain('border-top-left-radius: 16px')
    expect(workbenchStyleSource).toMatch(/\.codex-workbench-main::after\s*\{[\s\S]*?inset:\s*44px 0 0;[\s\S]*?border:\s*1px solid var\(--codex-line\);/)
    expect(workbenchStyleSource).toMatch(/\.codex-workbench-main::after\s*\{[\s\S]*?z-index:\s*50;/)
    expect(workbenchStyleSource).toContain('background: transparent;')
    expect(workbenchStyleSource).toMatch(/\.codex-status-bar\s*\{[\s\S]*?z-index:\s*2;/)
    expect(workbenchStyleSource).toMatch(/\.codex-panel-titlebar\s*\{[\s\S]*?z-index:\s*1;/)
  })
})

describe('AppSidebar user account navigation', () => {
  const routerPath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../router/index.ts')
  const appPath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../App.vue')
  const headerPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AppHeader.vue')
  const routerSource = readFileSync(routerPath, 'utf8')
  const appSource = readFileSync(appPath, 'utf8')
  const headerSource = readFileSync(headerPath, 'utf8')

  it('hides the user subscription entry and keeps My Account behind the payment flag', () => {
    expect(componentSource).not.toContain("{ path: '/subscriptions', label: t('nav.mySubscriptions')")
    expect(componentSource).toContain("{ path: '/purchase', label: t('nav.workbenchWallet')")
    expect(componentSource).toContain("featureFlag: flagPayment")
  })

  it('uses concise workbench labels and keeps profile access out of the sidebar list', () => {
    const selfNavBlock = componentSource.match(/function buildSelfNavItems[\s\S]*?return items\n}/)?.[0]

    expect(selfNavBlock).toBeDefined()
    expect(selfNavBlock).toContain("t('nav.workbenchOverview')")
    expect(selfNavBlock).toContain("t('nav.workbenchKeys')")
    expect(selfNavBlock).toContain("t('nav.workbenchRecords')")
    expect(selfNavBlock).toContain("t('nav.workbenchWallet')")
    expect(selfNavBlock).toContain("t('nav.workbenchOrders')")
    expect(selfNavBlock).not.toContain("path: '/profile'")
  })

  it('redirects the legacy subscription route to the account tab', () => {
    expect(routerSource).toContain("redirect: { path: '/purchase', query: { tab: 'account' } }")
    expect(routerSource).toContain("titleKey: 'nav.workbenchWallet'")
  })

  it('keeps workbench page titles aligned with their sidebar labels', () => {
    expect(routerSource).toContain("titleKey: 'nav.workbenchOverview'")
    expect(routerSource).toContain("titleKey: 'nav.workbenchKeys'")
    expect(routerSource).toContain("titleKey: 'nav.workbenchRecords'")
    expect(routerSource).toContain("titleKey: 'nav.workbenchWallet'")
    expect(routerSource).toContain("titleKey: 'nav.workbenchOrders'")
  })

  it('does not preload, poll, or render user subscriptions globally', () => {
    expect(appSource).not.toContain('fetchActiveSubscriptions')
    expect(appSource).not.toContain('subscriptionStore.startPolling')
    expect(headerSource).not.toContain('SubscriptionProgressMini')
  })

  it('does not render page subtitles in the global content title bar', () => {
    expect(headerSource).not.toContain('pageDescription')
    expect(headerSource).not.toContain('route.meta.descriptionKey')
  })
})
