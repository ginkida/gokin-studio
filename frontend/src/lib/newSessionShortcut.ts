export type NewSessionShortcutEvent = Pick<KeyboardEvent, 'altKey' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'key' | 'code'>

export function isNewSessionShortcut(event: NewSessionShortcutEvent): boolean {
  return (event.ctrlKey || event.metaKey) &&
    !event.altKey &&
    !event.shiftKey &&
    (event.key.toLowerCase() === 'n' || event.code === 'KeyN')
}
