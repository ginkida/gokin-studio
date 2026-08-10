import { forgetWorkspaceProject } from './workspaceContinuity'

// Per-project notification mute (iter 580+) — frontend-only setting that
// silences unread-badge bumps and completion toasts for specific projects.
// Useful when a user has many projects and only wants notifications from a
// few "active" ones (e.g. archived/experimental projects shouldn't ping).
//
// Storage: a single localStorage key holding a JSON array of project IDs.
// We cache the set at module level and invalidate on the
// `gokin:muted-changed` window event so subscribers (ToastStack, the
// useWailsEvents unread handler) can read the live state without
// re-parsing JSON on every push.

const STORAGE_KEY = 'gokin:muted-projects'

// Module-level cache. Lazy-loaded on first access.
let cache: Set<string> | null = null

function loadFromStorage(): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return new Set()
    const parsed = JSON.parse(raw)
    if (Array.isArray(parsed)) {
      const out = new Set<string>()
      for (const id of parsed) {
        if (typeof id === 'string' && id !== '') out.add(id)
      }
      return out
    }
  } catch {
    // Corrupt or unavailable — treat as empty.
  }
  return new Set()
}

function ensureCache(): Set<string> {
  if (cache === null) cache = loadFromStorage()
  return cache
}

// Browsers may write to localStorage from another tab/process; we listen
// to the storage event for cross-window sync, plus our own custom event
// for same-window updates that don't fire `storage`. Setup is idempotent.
let listenerInstalled = false
function ensureListener() {
  if (listenerInstalled) return
  listenerInstalled = true
  const refresh = () => { cache = loadFromStorage() }
  window.addEventListener('storage', (e) => {
    if (e.key === STORAGE_KEY) refresh()
  })
  window.addEventListener('gokin:muted-changed', refresh as EventListener)
}

export function getMutedProjects(): Set<string> {
  ensureListener()
  return ensureCache()
}

export function isProjectMuted(projectID: string): boolean {
  if (!projectID) return false
  return getMutedProjects().has(projectID)
}

export function toggleProjectMute(projectID: string): boolean {
  if (!projectID) return false
  const current = ensureCache()
  const next = new Set(current)
  let nowMuted: boolean
  if (next.has(projectID)) {
    next.delete(projectID)
    nowMuted = false
  } else {
    next.add(projectID)
    nowMuted = true
  }
  // Persist back. Empty set removes the key so we don't keep stale storage.
  try {
    if (next.size === 0) {
      localStorage.removeItem(STORAGE_KEY)
    } else {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(Array.from(next)))
    }
  } catch {
    /* localStorage unavailable */
  }
  cache = next
  // Notify same-window subscribers; cross-window gets the native storage event.
  window.dispatchEvent(new CustomEvent('gokin:muted-changed'))
  return nowMuted
}

// Drop a project from the muted set when it's removed entirely. Best-effort —
// silently no-ops if the project wasn't muted.
export function unmuteProject(projectID: string): void {
  if (!projectID) return
  const current = ensureCache()
  if (!current.has(projectID)) return
  current.delete(projectID)
  try {
    if (current.size === 0) {
      localStorage.removeItem(STORAGE_KEY)
    } else {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(Array.from(current)))
    }
  } catch { /* unavailable */ }
  window.dispatchEvent(new CustomEvent('gokin:muted-changed'))
}

// clearProjectLocalStorage drops every per-project frontend preference
// when the project is deleted. Without this, keys like
// `gokin:quietmode:<pid>` and `gokin:budget-alerts-<pid>` accumulate
// forever — a long-running install with many added/removed projects can
// fill up localStorage with dead state.
//
// Keys cleaned up:
//   - gokin:quietmode:<pid>           (iter 510+ quiet mode)
//   - gokin:budget-alerts-<pid>       (iter 610+ budget threshold alerts)
//
// The mute-set entry is handled by `unmuteProject` (which also fires the
// change event); callers should invoke both around the same time.
export function clearProjectLocalStorage(projectID: string): void {
  if (!projectID) return
  const keys = [
    `gokin:quietmode:${projectID}`,
    `gokin:budget-alerts-${projectID}`,
  ]
  for (const k of keys) {
    try { localStorage.removeItem(k) } catch { /* unavailable */ }
  }
  forgetWorkspaceProject(projectID)
}

// resetAllPreferences (iter 630+) wipes every frontend-only preference key
// the app has ever stored in localStorage. Used by the Settings "Reset
// preferences" button to give users a clean recovery from any bad state.
//
// What gets cleared:
//   - Global toggles:
//       gokin:toasts-enabled, gokin:sound-on-complete
//   - Project sets:
//       gokin:muted-projects (full list)
//   - Per-project keys (enumerated by prefix scan):
//       gokin:quietmode:<pid>, gokin:budget-alerts-<pid>
//   - Workspace continuity:
//       active project, session, and Files/Artifacts/Settings location
//
// Important: backend-persisted state (project config, sessions, history)
// is NOT touched — this is purely frontend preferences. After clearing,
// dispatches all the change events so live components re-read their
// initial state. Returns the number of keys actually removed.
export function resetAllPreferences(): number {
  let removed = 0
  // Collect keys first since localStorage mutates during the loop.
  const toRemove: string[] = []
  try {
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k && k.startsWith('gokin:')) toRemove.push(k)
    }
  } catch { return 0 }
  for (const k of toRemove) {
    try {
      localStorage.removeItem(k)
      removed++
    } catch { /* skip */ }
  }
  // Reset the in-memory mute cache so the next isProjectMuted call doesn't
  // return stale entries.
  cache = new Set()
  // Notify all known live subscribers so they re-read defaults without
  // requiring a window reload. Each component listens for its own event
  // and re-initializes its state ref / variable.
  const events = [
    'gokin:toasts-toggled',
    'gokin:sound-toggled',
    'gokin:muted-changed',
    'gokin:layout-reset',
  ]
  for (const ev of events) {
    try { window.dispatchEvent(new CustomEvent(ev)) } catch { /* unavailable */ }
  }
  return removed
}
