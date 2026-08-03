import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  disposeTheme,
  initTheme,
  setThemeMode,
  useTheme,
} from '@/composables/useTheme'

type MediaChangeListener = (event: MediaQueryListEvent) => void

describe('useTheme', () => {
  let prefersDark = false
  let mediaChangeListener: MediaChangeListener | null = null

  beforeEach(() => {
    disposeTheme()
    window.localStorage.clear()
    document.documentElement.className = ''
    document.documentElement.removeAttribute('data-theme')
    document.documentElement.removeAttribute('data-theme-mode')
    document.documentElement.style.colorScheme = ''

    vi.stubGlobal('matchMedia', vi.fn(() => ({
      get matches() {
        return prefersDark
      },
      media: '(prefers-color-scheme: dark)',
      onchange: null,
      addEventListener: (_type: string, listener: MediaChangeListener) => {
        mediaChangeListener = listener
      },
      removeEventListener: () => {
        mediaChangeListener = null
      },
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })))
  })

  afterEach(() => {
    disposeTheme()
    vi.unstubAllGlobals()
  })

  it('默认跟随系统主题并写入文档状态', () => {
    prefersDark = true

    initTheme()

    const { themeMode, resolvedTheme } = useTheme()
    expect(themeMode.value).toBe('system')
    expect(resolvedTheme.value).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(document.documentElement.dataset.themeMode).toBe('system')
  })

  it('持久化主题优先于系统主题', () => {
    prefersDark = true
    window.localStorage.setItem('theme', 'light')

    initTheme()

    expect(useTheme().resolvedTheme.value).toBe('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('切换主题后立即应用并持久化', () => {
    initTheme()

    setThemeMode('dark')

    expect(window.localStorage.getItem('theme')).toBe('dark')
    expect(useTheme().resolvedTheme.value).toBe('dark')
    expect(document.documentElement.style.colorScheme).toBe('dark')
  })

  it('系统模式会响应系统主题变化', () => {
    initTheme()
    prefersDark = true

    mediaChangeListener?.({ matches: true } as MediaQueryListEvent)

    expect(useTheme().resolvedTheme.value).toBe('dark')
  })

  it('其他标签页更新主题时同步当前页面', () => {
    initTheme()

    window.dispatchEvent(new StorageEvent('storage', {
      key: 'theme',
      newValue: 'dark',
    }))

    expect(useTheme().themeMode.value).toBe('dark')
    expect(useTheme().resolvedTheme.value).toBe('dark')
  })
})
