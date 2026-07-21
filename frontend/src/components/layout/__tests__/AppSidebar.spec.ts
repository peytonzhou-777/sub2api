import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const componentPath = resolve(dirname(fileURLToPath(import.meta.url)), '../AppSidebar.vue')
const componentSource = readFileSync(componentPath, 'utf8')
const stylePath = resolve(dirname(fileURLToPath(import.meta.url)), '../../../style.css')
const styleSource = readFileSync(stylePath, 'utf8')

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
    expect(componentSource).toContain("{ path: '/purchase', label: t('nav.myAccount')")
    expect(componentSource).toContain("featureFlag: flagPayment")
  })

  it('redirects the legacy subscription route to the account tab', () => {
    expect(routerSource).toContain("redirect: { path: '/purchase', query: { tab: 'account' } }")
    expect(routerSource).toContain("titleKey: 'nav.myAccount'")
  })

  it('does not preload, poll, or render user subscriptions globally', () => {
    expect(appSource).not.toContain('fetchActiveSubscriptions')
    expect(appSource).not.toContain('subscriptionStore.startPolling')
    expect(headerSource).not.toContain('SubscriptionProgressMini')
  })
})
