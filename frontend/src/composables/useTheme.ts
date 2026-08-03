import { computed, readonly, ref } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'
export type ResolvedTheme = Exclude<ThemeMode, 'system'>

const THEME_STORAGE_KEY = 'theme'
const SYSTEM_THEME_QUERY = '(prefers-color-scheme: dark)'

const themeMode = ref<ThemeMode>('system')
const resolvedTheme = ref<ResolvedTheme>('light')

let systemThemeQuery: MediaQueryList | null = null
let initialized = false

function isThemeMode(value: string | null): value is ThemeMode {
  return value === 'light' || value === 'dark' || value === 'system'
}

function readStoredTheme(): ThemeMode {
  try {
    const storedTheme = window.localStorage.getItem(THEME_STORAGE_KEY)
    return isThemeMode(storedTheme) ? storedTheme : 'system'
  } catch {
    return 'system'
  }
}

function resolveTheme(mode: ThemeMode): ResolvedTheme {
  if (mode !== 'system') return mode
  return systemThemeQuery?.matches ? 'dark' : 'light'
}

function applyTheme(mode: ThemeMode): void {
  const nextTheme = resolveTheme(mode)
  resolvedTheme.value = nextTheme

  const root = document.documentElement
  root.classList.toggle('dark', nextTheme === 'dark')
  root.dataset.theme = nextTheme
  root.dataset.themeMode = mode
  root.dataset.visualTheme = 'codex'
  root.style.colorScheme = nextTheme
}

function handleSystemThemeChange(): void {
  if (themeMode.value === 'system') applyTheme('system')
}

function handleStorageChange(event: StorageEvent): void {
  if (event.key !== THEME_STORAGE_KEY) return
  const nextMode = isThemeMode(event.newValue) ? event.newValue : 'system'
  themeMode.value = nextMode
  applyTheme(nextMode)
}

/** 在应用挂载前初始化主题，并监听系统与跨标签页变化。 */
export function initTheme(): void {
  if (typeof window === 'undefined' || typeof document === 'undefined') return

  systemThemeQuery = window.matchMedia(SYSTEM_THEME_QUERY)
  themeMode.value = readStoredTheme()
  applyTheme(themeMode.value)

  if (initialized) return
  systemThemeQuery.addEventListener('change', handleSystemThemeChange)
  window.addEventListener('storage', handleStorageChange)
  initialized = true
}

/** 设置并持久化用户选择的主题模式。 */
export function setThemeMode(mode: ThemeMode): void {
  if (typeof window === 'undefined' || typeof document === 'undefined') return

  themeMode.value = mode
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, mode)
  } catch {
    // 存储不可用时仍允许当前会话切换主题。
  }
  applyTheme(mode)
}

/** 在当前解析出的亮暗主题之间快速切换。 */
export function toggleTheme(): void {
  setThemeMode(resolvedTheme.value === 'dark' ? 'light' : 'dark')
}

/** 清理全局监听，供测试与应用卸载场景使用。 */
export function disposeTheme(): void {
  if (systemThemeQuery) {
    systemThemeQuery.removeEventListener('change', handleSystemThemeChange)
  }
  if (typeof window !== 'undefined') {
    window.removeEventListener('storage', handleStorageChange)
  }
  systemThemeQuery = null
  initialized = false
}

export function useTheme() {
  return {
    themeMode: readonly(themeMode),
    resolvedTheme: readonly(resolvedTheme),
    isDark: computed(() => resolvedTheme.value === 'dark'),
    setThemeMode,
    toggleTheme,
  }
}
