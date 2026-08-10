export interface PreviewElementRect {
  x: number
  y: number
  width: number
  height: number
}

export interface PreviewElementDescription {
  tag: string
  role: string
  type: string
  name: string
  id: string
  testId: string
  text: string
  disabled: boolean
  rect: PreviewElementRect
}

export interface PreviewElementSelection {
  selector: string
  element: PreviewElementDescription
  ancestors: PreviewElementDescription[]
  url: string
  title: string
  capturedAt: number
}

function boundedString(value: unknown, max: number): string {
  return typeof value === 'string' ? Array.from(value).slice(0, max).join('') : ''
}

function finiteNumber(value: unknown): number {
  if (typeof value !== 'number' || !Number.isFinite(value)) return 0
  return Math.max(-100_000, Math.min(100_000, Math.round(value)))
}

function safePreviewURL(value: unknown): string {
  const raw = boundedString(value, 4096)
  if (!raw) return ''
  try {
    const parsed = new URL(raw)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return ''
    return boundedString(`${parsed.origin}${parsed.pathname}`, 2048)
  } catch {
    return ''
  }
}

function normalizeDescription(value: unknown): PreviewElementDescription {
  const item = value && typeof value === 'object' ? value as Record<string, unknown> : {}
  const rawRect = item.rect && typeof item.rect === 'object' ? item.rect as Record<string, unknown> : {}
  return {
    tag: boundedString(item.tag, 80),
    role: boundedString(item.role, 120),
    type: boundedString(item.type, 120),
    name: boundedString(item.name, 200),
    id: boundedString(item.id, 200),
    testId: boundedString(item.testId, 200),
    text: boundedString(item.text, 500),
    disabled: item.disabled === true,
    rect: {
      x: finiteNumber(rawRect.x),
      y: finiteNumber(rawRect.y),
      width: Math.max(0, finiteNumber(rawRect.width)),
      height: Math.max(0, finiteNumber(rawRect.height)),
    },
  }
}

// Preview content is untrusted even though its transport is token-bound. Keep
// only the fixed metadata schema and cap every field before it reaches a draft.
export function normalizePreviewElementSelection(value: unknown): PreviewElementSelection | null {
  if (!value || typeof value !== 'object') return null
  const raw = value as Record<string, unknown>
  const selector = boundedString(raw.selector, 512)
  const element = normalizeDescription(raw.element)
  if (!selector || !element.tag) return null
  return {
    selector,
    element,
    ancestors: Array.isArray(raw.ancestors) ? raw.ancestors.slice(0, 4).map(normalizeDescription) : [],
    // Query strings and fragments routinely contain session tokens. They do
    // not help locate a source component, so keep only origin + pathname.
    url: safePreviewURL(raw.url),
    title: boundedString(raw.title, 500),
    capturedAt: typeof raw.capturedAt === 'number' && Number.isFinite(raw.capturedAt) && raw.capturedAt > 0
      ? Math.min(Number.MAX_SAFE_INTEGER, Math.round(raw.capturedAt))
      : Date.now(),
  }
}

export function previewElementDraft(selection: PreviewElementSelection): string {
  return `Update the element I selected in the active app preview. Use this bounded DOM evidence to locate the relevant component and styles:\n\n${JSON.stringify(selection, null, 2)}\n\nAsk what visual or behavioral change I want if it is not already clear from my next instruction. Do not assume the DOM selector is a source-file path. Make the smallest correct change, run relevant tests, and inspect this element again before finishing.\n\nRequested change: `
}
