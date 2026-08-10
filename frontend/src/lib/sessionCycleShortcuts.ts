export type SessionCycleShortcutEvent = Pick<KeyboardEvent, 'altKey' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'key' | 'code'>

// Claude Desktop uses Ctrl+Tab on every platform and Cmd/Ctrl+Shift+bracket
// as an alternate chord. Keep the older PageUp/PageDown chord as a local
// compatibility alias. Physical codes preserve behavior on non-Latin layouts.
export function sessionCycleShortcutDirection(event: SessionCycleShortcutEvent): -1 | 1 | null {
  if (event.altKey) return null
  if (event.ctrlKey && !event.metaKey && (event.key === 'Tab' || event.code === 'Tab')) {
    return event.shiftKey ? -1 : 1
  }
  if ((event.ctrlKey || event.metaKey) && event.shiftKey) {
    if (event.code === 'BracketLeft' || event.key === '[' || event.key === '{') return -1
    if (event.code === 'BracketRight' || event.key === ']' || event.key === '}') return 1
  }
  if ((event.ctrlKey || event.metaKey) && !event.shiftKey) {
    if (event.key === 'PageUp' || event.code === 'PageUp') return -1
    if (event.key === 'PageDown' || event.code === 'PageDown') return 1
  }
  return null
}
