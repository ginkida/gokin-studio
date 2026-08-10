export type ThemePreference = 'dark' | 'light' | 'system'
export type ResolvedTheme = 'dark' | 'light'

const SYSTEM_DARK_QUERY = '(prefers-color-scheme: dark)'

export function normalizeThemePreference(value: string | null | undefined): ThemePreference {
  return value === 'light' || value === 'system' ? value : 'dark'
}

export function resolveThemePreference(preference: string | null | undefined): ResolvedTheme {
  const normalized = normalizeThemePreference(preference)
  if (normalized !== 'system') return normalized
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return 'dark'
  return window.matchMedia(SYSTEM_DARK_QUERY).matches ? 'dark' : 'light'
}

export function appliedTheme(): ResolvedTheme {
  return document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark'
}

export function applyThemePreference(preference: string | null | undefined): ResolvedTheme {
  const normalized = normalizeThemePreference(preference)
  const resolved = resolveThemePreference(normalized)
  const root = document.documentElement
  root.setAttribute('data-theme-preference', normalized)
  root.setAttribute('data-theme', resolved)
  root.style.colorScheme = resolved
  return resolved
}

// Applies a persisted preference and keeps the resolved palette synchronized
// with macOS/Windows while "System" is selected. The listener belongs to App,
// so previewing and saving Settings never creates duplicate subscriptions.
export function observeThemePreference(preference: string | null | undefined): () => void {
  const normalized = normalizeThemePreference(preference)
  applyThemePreference(normalized)
  if (normalized !== 'system' || typeof window.matchMedia !== 'function') return () => {}

  const query = window.matchMedia(SYSTEM_DARK_QUERY)
  const update = () => applyThemePreference('system')
  query.addEventListener('change', update)
  return () => query.removeEventListener('change', update)
}
