import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const source = readFileSync(resolve(__dirname, '../AmountInput.vue'), 'utf8')

describe('AmountInput theme styles', () => {
  it('uses Codex theme tokens for amount controls in both color schemes', () => {
    expect(source).toContain('background: var(--codex-panel-raised);')
    expect(source).toContain('background: var(--codex-panel-hover);')
    expect(source).toContain('background: var(--codex-line-strong);')
    expect(source).toContain('color: var(--codex-accent-blue);')
    expect(source).toContain('.amount-adjustment-button:focus-visible')
    expect(source).toMatch(/@media \(max-width: 767px\)[\s\S]*?min-height: var\(--codex-control\);/)

    expect(source).not.toContain('background: #202020;')
    expect(source).not.toContain('background: #444;')
    expect(source).not.toContain('color: #78baff;')
  })
})
