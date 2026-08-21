export const ONBOARDING_DISMISSED_KEY = 'gokin:onboarding-dismissed'

type ReadableStorage = Pick<Storage, 'getItem'>

function browserStorage(): ReadableStorage | null {
  try {
    return globalThis.localStorage
  } catch {
    return null
  }
}

// Keep the startup decision outside the wizard component so the relatively
// heavy onboarding UI can stay in its own lazy-loaded chunk.
export function shouldShowOnboarding(
  projectCount: number,
  storage?: ReadableStorage | null,
): boolean {
  if (projectCount > 0) return false
  const readableStorage = storage === undefined ? browserStorage() : storage
  if (!readableStorage) return true
  try {
    return readableStorage.getItem(ONBOARDING_DISMISSED_KEY) !== '1'
  } catch {
    // A fresh install should remain usable when WebView storage is blocked.
    return true
  }
}
