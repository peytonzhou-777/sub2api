import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const settingsView = readFileSync(resolve(__dirname, '../SettingsView.vue'), 'utf8')
const settingsApi = readFileSync(resolve(__dirname, '../../../api/admin/settings.ts'), 'utf8')

describe('SettingsView 品牌后缀配置', () => {
  it('展示、初始化并提交品牌后缀', () => {
    expect(settingsView).toContain('v-model="form.site_wordmark_suffix"')
    expect(settingsView).toContain('maxlength="16"')
    expect(settingsView).toContain('site_wordmark_suffix: "API"')
    expect(settingsView).toContain('site_wordmark_suffix: form.site_wordmark_suffix')
  })

  it('在管理员设置读写协议中声明品牌后缀', () => {
    expect(settingsApi).toContain('site_wordmark_suffix: string;')
    expect(settingsApi).toContain('site_wordmark_suffix?: string;')
  })
})
