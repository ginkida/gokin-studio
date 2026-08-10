// Standard DOM MouseEvent.button values for dedicated browser-history
// buttons. Keeping this mapping pure makes the desktop navigation contract
// testable without manufacturing trusted OS mouse events.
export function workspaceMouseHistoryDirection(button: number): -1 | 1 | null {
  if (button === 3) return -1
  if (button === 4) return 1
  return null
}
