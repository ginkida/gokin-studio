const GLM_MODEL_ID = /^glm-[0-9]+(?:\.[0-9]+)*(?:-[a-z0-9]+)*$/
const KIMI_MODEL_ID = /^(?:k[0-9]+(?:-[a-z0-9]+)*|kimi-(?:for-coding|k[0-9]+)(?:[.-][a-z0-9]+)*)$/
const GLM_VERSION = /^glm-([0-9]+(?:\.[0-9]+)*)/
const KIMI_VERSION = /^(?:k|kimi-k)([0-9]+(?:\.[0-9]+)*)(?:-|$)/

// Keep dynamically discovered models inside the same strict product boundary
// as the backend. A provider response can introduce glm-5.3 or k4, but never
// use Studio as a path to expose an unrelated model family.
export function isStudioModelID(provider: string, model: string): boolean {
  const providerID = provider.trim().toLowerCase()
  const modelID = model.trim()
  if (providerID === 'glm') return GLM_MODEL_ID.test(modelID)
  if (providerID === 'kimi') return KIMI_MODEL_ID.test(modelID)
  return false
}

export function compareStudioModelVersions(provider: string, left: string, right: string): number {
  const matcher = provider.trim().toLowerCase() === 'kimi' ? KIMI_VERSION : GLM_VERSION
  const parse = (value: string) => {
    const match = value.trim().toLowerCase().match(matcher)
    return match ? match[1].split('.').map((part) => Number(part) || 0) : []
  }
  const a = parse(left)
  const b = parse(right)
  for (let index = 0; index < Math.max(a.length, b.length); index++) {
    const delta = (a[index] || 0) - (b[index] || 0)
    if (delta !== 0) return delta < 0 ? -1 : 1
  }
  return 0
}

export function isFutureStudioModelID(provider: string, model: string): boolean {
  const providerID = provider.trim().toLowerCase()
  const currentFlagship = providerID === 'kimi' ? 'k3' : 'glm-5.2'
  return isStudioModelID(providerID, model) &&
    compareStudioModelVersions(providerID, model, currentFlagship) >= 0
}

export type StudioReasoningControlKind = 'kimi-effort' | 'glm-effort' | 'budget'

// Drive the controls from backend model metadata, not a frozen list of model
// IDs. Exact flagship fallbacks avoid a one-frame UI downgrade while the
// provider catalog is still loading at startup.
export function studioReasoningControlKind(
  provider: string,
  model: string,
  reasoningControl?: string,
): StudioReasoningControlKind {
  const providerID = provider.trim().toLowerCase()
  const modelID = model.trim().toLowerCase()
  const control = (reasoningControl || '').trim().toLowerCase()
  if (providerID === 'kimi' && (
    (control.includes('low') && control.includes('high') && control.includes('max')) ||
    modelID === 'k3' || modelID === 'k3-256k'
  )) return 'kimi-effort'
  if (providerID === 'glm' && (
    (control.includes('high') && control.includes('max')) ||
    modelID === 'glm-5.2'
  )) return 'glm-effort'
  return 'budget'
}
