import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const bellSource = readFileSync(
  resolve(process.cwd(), 'src/components/common/AnnouncementBell.vue'),
  'utf8',
)
const popupSource = readFileSync(
  resolve(process.cwd(), 'src/components/common/AnnouncementPopup.vue'),
  'utf8',
)

describe('公告弹窗主题表面', () => {
  it('公告列表与详情弹窗使用 Codex 明暗主题 token', () => {
    expect(bellSource).toContain('background: var(--codex-modal-backdrop);')
    expect(bellSource).toContain('background: var(--codex-overlay);')
    expect(bellSource).toContain('background: color-mix(in srgb, var(--codex-canvas) 86%, transparent);')
    expect(bellSource).toContain('background: color-mix(in srgb, var(--codex-panel-raised) 82%, var(--codex-panel));')
    expect(bellSource).not.toContain('.announcement-center-header > *')
    expect(bellSource).not.toContain('linear-gradient(145deg, rgb(45 45 49 / 0.92)')
  })

  it('主动公告内容弹窗不再固定使用暗色表面和正文颜色', () => {
    expect(popupSource).toContain('background: var(--codex-modal-backdrop);')
    expect(popupSource).toContain('background: var(--codex-overlay);')
    expect(popupSource).toContain('color: var(--codex-text-muted);')
    expect(popupSource).toContain('background: var(--codex-panel-hover);')
    expect(popupSource).not.toContain('linear-gradient(145deg, rgb(45 45 49 / 0.92)')
    expect(popupSource).not.toContain('color: #d1d1d5;')
  })
})
