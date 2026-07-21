import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const root = dirname(fileURLToPath(import.meta.url))
const routerSource = readFileSync(resolve(root, '../index.ts'), 'utf8')
const sidebarSource = readFileSync(resolve(root, '../../components/layout/AppSidebar.vue'), 'utf8')
const creditsViewSource = readFileSync(resolve(root, '../../views/admin/CreditsView.vue'), 'utf8')
const rechargeActivitiesViewSource = readFileSync(resolve(root, '../../views/admin/orders/AdminRechargeActivitiesView.vue'), 'utf8')
const resetRebatesViewSource = readFileSync(resolve(root, '../../views/admin/ResetRebatesView.vue'), 'utf8')

describe('credit management routes', () => {
  it('places all three credit functions under credit management', () => {
    expect(routerSource).toContain("path: '/admin/credits'")
    expect(routerSource).toContain("path: '/admin/credits/recharge-activities'")
    expect(routerSource).toContain("path: '/admin/credits/reset-rebates'")
  })

  it('redirects the old recharge activity URL and removes it from order navigation', () => {
    expect(routerSource).toMatch(/path: '\/admin\/orders\/recharge-activities',[\s\S]*?redirect: '\/admin\/credits\/recharge-activities'/)
    expect(sidebarSource).not.toContain("{ path: '/admin/orders/recharge-activities'")
  })

  it('uses an expandable credit management sidebar group instead of page tabs', () => {
    expect(sidebarSource).toMatch(/path: '\/admin\/credits',[\s\S]*?expandOnly: true,[\s\S]*?children: \[/)
    expect(sidebarSource).toContain("{ path: '/admin/credits', label: t('admin.credits.tabs.users')")
    expect(sidebarSource).toContain("{ path: '/admin/credits/recharge-activities', label: t('admin.credits.tabs.rechargeActivities')")
    expect(sidebarSource).toContain("{ path: '/admin/credits/reset-rebates', label: t('admin.credits.tabs.resetRebates')")
    expect(creditsViewSource).not.toContain('CreditManagementTabs')
    expect(rechargeActivitiesViewSource).not.toContain('CreditManagementTabs')
    expect(resetRebatesViewSource).not.toContain('CreditManagementTabs')
  })
})
