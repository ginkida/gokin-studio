import type { ProviderInfo, ProviderModelInfo } from '../stores/settingsStore'
import { compareStudioModelVersions, isFutureStudioModelID } from './studioModelIds'

type BackendProvider = {
  id?: string
  name?: string
  models?: string[]
  modelDetails?: Partial<ProviderModelInfo>[]
}

// Product boundary mirrored defensively in the frontend. The backend already
// exposes only these two providers, but every picker consumes this normalized
// list, so an old/generated bridge or malformed response cannot reintroduce a
// generic third-party provider into the public Studio UI.
const STUDIO_PROVIDER_ORDER = ['glm', 'kimi'] as const
const STUDIO_PROVIDER_IDS = new Set<string>(STUDIO_PROVIDER_ORDER)
const STUDIO_PROVIDER_NAMES: Record<(typeof STUDIO_PROVIDER_ORDER)[number], string> = {
  glm: 'GLM (Z.AI)',
  kimi: 'Kimi Code',
}
const STUDIO_MODEL_IDS: Record<(typeof STUDIO_PROVIDER_ORDER)[number], readonly string[]> = {
  glm: ['glm-5.2', 'glm-5.1', 'glm-5', 'glm-5-turbo', 'glm-4.7'],
  kimi: ['k3', 'k3-256k', 'kimi-for-coding', 'kimi-for-coding-highspeed'],
}

const STUDIO_PROVIDER_LABELS: Record<string, string> = {
  glm: 'GLM',
  kimi: 'Kimi',
}

const STUDIO_PROVIDER_ACCOUNT_URLS: Record<string, string> = {
  glm: 'https://z.ai/manage-apikey/apikey-list',
  kimi: 'https://www.kimi.com/code/console',
}

const STUDIO_MODEL_LABELS: Record<string, string> = {
  'glm-5.2': '5.2',
  'glm-5.1': '5.1',
  'glm-5': '5',
  'glm-5-turbo': '5 Turbo',
  'glm-4.7': '4.7',
  'k3': 'K3',
  'k3-256k': 'K3 256K',
  'kimi-for-coding': 'for Coding',
  'kimi-for-coding-highspeed': 'for Coding Highspeed',
}

// Product-facing labels for high-frequency UI. Configuration screens still
// expose the exact API model ID where precision matters, while chat chrome can
// read like a desktop product instead of a transport protocol.
export function formatProviderLabel(provider?: string): string {
  const id = (provider || 'glm').trim().toLowerCase()
  return STUDIO_PROVIDER_LABELS[id] || id.toUpperCase()
}

export function formatModelLabel(model?: string): string {
  const id = (model || '').trim().toLowerCase()
  if (STUDIO_MODEL_LABELS[id]) return STUDIO_MODEL_LABELS[id]
  if (id.startsWith('glm-')) return id.slice(4).replace(/-/g, ' ')
  if (/^k[0-9]/.test(id)) return id.toUpperCase()
  if (id.startsWith('kimi-')) return id.slice(5).replace(/(^|[.-])k([0-9])/g, '$1K$2')
  return model || 'Default'
}

export function formatProviderModelLabel(provider?: string, model?: string): string {
  return `${formatProviderLabel(provider)} ${formatModelLabel(model)}`
}

export function getProviderAccountURL(provider?: string): string {
  return STUDIO_PROVIDER_ACCOUNT_URLS[(provider || '').trim().toLowerCase()] || ''
}

// Wails returns generated classes, while the settings store deliberately uses
// plain serializable objects. Copy every capability field here so adding a
// backend model contract cannot silently disappear between RPC and the picker.
export function normalizeProviderCatalog(providers: BackendProvider[]): ProviderInfo[] {
  return providers
    .map((provider) => ({
      provider,
      id: typeof provider?.id === 'string' ? provider.id.trim().toLowerCase() : '',
    }))
    .filter(({ id }) => STUDIO_PROVIDER_IDS.has(id))
    .sort((a, b) => STUDIO_PROVIDER_ORDER.indexOf(a.id as typeof STUDIO_PROVIDER_ORDER[number]) -
      STUDIO_PROVIDER_ORDER.indexOf(b.id as typeof STUDIO_PROVIDER_ORDER[number]))
    .map(({ provider, id }) => {
      const studioID = id as typeof STUDIO_PROVIDER_ORDER[number]
      const allowedModels = new Set(STUDIO_MODEL_IDS[studioID])
      const advertisedModels = new Set(
        Array.isArray(provider.models)
          ? provider.models.filter((model): model is string => typeof model === 'string' && (
            allowedModels.has(model) || isFutureStudioModelID(studioID, model)
          ))
          : [],
      )
      // Product order is authoritative: flagship first, no duplicates, and a
      // malformed backend cannot promote a legacy model above the current one.
      const advertisedExtras = [...advertisedModels]
        .filter((model) => !allowedModels.has(model))
        .sort((left, right) => compareStudioModelVersions(studioID, right, left) || left.localeCompare(right))
      const newerExtras = advertisedExtras.filter((model) => (
        compareStudioModelVersions(studioID, model, STUDIO_MODEL_IDS[studioID][0]) > 0
      ))
      const otherExtras = advertisedExtras.filter((model) => !newerExtras.includes(model))
      const models = [
        ...newerExtras,
        ...STUDIO_MODEL_IDS[studioID].filter((model) => advertisedModels.has(model)),
        ...otherExtras,
      ] as string[]
      const declaredModels = new Set(models)
      return {
        id,
        name: STUDIO_PROVIDER_NAMES[studioID],
        models,
        modelDetails: Array.isArray(provider.modelDetails)
          ? provider.modelDetails
          .filter((model): model is Partial<ProviderModelInfo> & { id: string } =>
            typeof model?.id === 'string' && declaredModels.has(model.id)
          )
          .map((model) => ({
            id: model.id,
            contextWindow: Number(model.contextWindow) || 0,
            defaultMaxOutputTokens: Number(model.defaultMaxOutputTokens) || 0,
            maxOutputTokens: Number(model.maxOutputTokens) || 0,
            inputModalities: Array.isArray(model.inputModalities) ? [...model.inputModalities] : [],
            reasoningControl: model.reasoningControl || '',
            description: model.description || '',
            latest: !!model.latest,
            recommended: !!model.recommended,
          }))
          : [],
      }
    })
}
