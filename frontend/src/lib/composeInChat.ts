export type ComposeInChatMode = 'replace' | 'append'

// Plain mentions stay compact. Paths containing spaces or punctuation are
// quoted so the composer can preserve them as one reference through send-time
// expansion. JSON-style escaping keeps quotes and backslashes unambiguous.
export function formatFileMention(path: string): string {
  const value = path.trim()
  if (/^[A-Za-z0-9._/\-]+\.[A-Za-z0-9]+$/.test(value)) return `@${value}`
  return `@${JSON.stringify(value)}`
}

// Open the active project's current chat and place a reviewable prompt in its
// composer. App.tsx owns the view transition; ChatPanel owns the draft. Keeping
// this as an event means workspace views do not need to know session internals.
export function composeInChat(text: string, mode: ComposeInChatMode = 'replace') {
  window.dispatchEvent(new CustomEvent('gokin:compose-in-chat', {
    detail: { text, mode },
  }))
}
