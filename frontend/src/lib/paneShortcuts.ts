export type PaneShortcutAction = 'diff' | 'preview' | 'select-preview-element' | 'terminal'
export type PaneShortcutEvent = Pick<KeyboardEvent, 'altKey' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'key' | 'code'>

// Pane shortcuts follow Claude Desktop's platform contract. Terminal is Ctrl
// on every platform; the other actions use the platform's primary modifier.
export function paneShortcutAction(event: PaneShortcutEvent): PaneShortcutAction | null {
  if (event.altKey) return null
  if (event.ctrlKey && !event.metaKey && !event.shiftKey && (event.key === '`' || event.code === 'Backquote')) {
    return 'terminal'
  }
  if (!(event.ctrlKey || event.metaKey) || !event.shiftKey) return null
  if (event.code === 'KeyD' || event.key.toLowerCase() === 'd') return 'diff'
  if (event.code === 'KeyB' || event.key.toLowerCase() === 'b') return 'preview'
  if (event.code === 'KeyP' || event.key.toLowerCase() === 'p') return 'preview'
  if (event.code === 'KeyS' || event.key.toLowerCase() === 's') return 'select-preview-element'
  return null
}
