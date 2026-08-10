export const MAX_EXTERNAL_CHAT_LINK_BYTES = 8 * 1024

export type ExternalHTTPLink = {
  url: string
  display: string
  origin: string
}

function byteLength(value: string) {
  return new TextEncoder().encode(value).length
}

export function normalizeExternalHTTPLink(value: unknown): ExternalHTTPLink | null {
  const raw = typeof value === 'string' ? value.trim() : ''
  if (!raw || byteLength(raw) > MAX_EXTERNAL_CHAT_LINK_BYTES || /[\u0000-\u001f\u007f]/.test(raw)) return null
  let parsed: URL
  try {
    parsed = new URL(raw)
  } catch {
    return null
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null
  // Credentials embedded in a chat link are easy to overlook and can leak to
  // the destination or system browser. Require a credential-free URL.
  if (parsed.username || parsed.password) return null
  const canonical = parsed.href
  const display = canonical.length <= 240 ? canonical : `${canonical.slice(0, 236)}…`
  return { url: canonical, display, origin: parsed.origin }
}
