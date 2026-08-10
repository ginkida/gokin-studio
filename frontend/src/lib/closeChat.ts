export type CloseChatShortcutEvent = Pick<KeyboardEvent, 'altKey' | 'ctrlKey' | 'metaKey' | 'shiftKey' | 'key' | 'code'>

// Use the physical key as a fallback so the desktop shortcut keeps working on
// Russian and other non-Latin keyboard layouts.
export function isCloseChatShortcut(event: CloseChatShortcutEvent): boolean {
  return (event.ctrlKey || event.metaKey) &&
    !event.altKey &&
    !event.shiftKey &&
    (event.key.toLowerCase() === 'w' || event.code === 'KeyW')
}

// Standalone panes (Files, Artifacts, Settings) retain a selected chat in the
// store. Resolve only an exact currently loaded session, and never close the
// project's last chat.
export function resolveCloseChatSession(
  view: string,
  storedActiveSession: string | null | undefined,
  sessionIDs: readonly string[],
): string | null {
  if (sessionIDs.length <= 1) return null
  if (sessionIDs.includes(view)) return view
  if (storedActiveSession && sessionIDs.includes(storedActiveSession)) return storedActiveSession
  return null
}
