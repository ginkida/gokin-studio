import { create } from 'zustand'
import { compareStudioModelVersions, isFutureStudioModelID } from '../lib/studioModelIds'

export interface Settings {
  theme: string
  defaultProvider: string
  defaultModel: string
  glmKey: string
  kimiKey: string
  globalInstructions: string
  defaultThinkingMode: string   // "" | "enabled" | "disabled"
  defaultThinkingBudget: number // 0 = use the selected model's tuned default
  defaultBudgetUSD: number      // 0 = no budget for new projects
  autoCleanupDisabled: boolean  // iter 790+ opt-out of once-per-24h background cleanup
  autoBackupEnabled: boolean    // iter 840+ opt-in to once-per-24h tar.gz auto-backup
  quickEntryEnabled: boolean    // opt-in native global shortcut while desktop app runs
  quickEntryShortcut: string    // Double-tap Option on macOS or a native chord
  voiceShortcutEnabled: boolean // independent opt-in global dictation toggle
  voiceShortcut: string         // Caps Lock on macOS or a native chord
  keepAwakeEnabled: boolean     // opt-in OS sleep inhibitor for active/scheduled runs
  autoUpdateCheckDisabled: boolean // opt out of the daily notify-only release check
  autoArchivePRAfterClose: boolean // opt-in archive of idle clean chats after PR merge/close
}

export const SETTINGS_FIELD_KEYS: (keyof Settings)[] = [
  'theme',
  'defaultProvider',
  'defaultModel',
  'glmKey',
  'kimiKey',
  'globalInstructions',
  'defaultThinkingMode',
  'defaultThinkingBudget',
  'defaultBudgetUSD',
  'autoCleanupDisabled',
  'autoBackupEnabled',
  'quickEntryEnabled',
  'quickEntryShortcut',
  'voiceShortcutEnabled',
  'voiceShortcut',
  'keepAwakeEnabled',
  'autoUpdateCheckDisabled',
  'autoArchivePRAfterClose',
]

export function settingsEqual(a: Settings, b: Settings): boolean {
  return SETTINGS_FIELD_KEYS.every((key) => a[key] === b[key])
}

export interface ProviderInfo {
  id: string
  name: string
  models: string[]
  modelDetails?: ProviderModelInfo[]
}

export interface ProviderModelInfo {
  id: string
  contextWindow: number
  defaultMaxOutputTokens: number
  maxOutputTokens: number
  inputModalities: string[]
  reasoningControl: string
  description: string
  latest: boolean
  recommended: boolean
}

export interface ProviderCapabilitySnapshot {
  availableModels: string[]
  recommendedModel?: string
  checkedAt: number
}

// Model availability belongs to one credential/account at one moment in
// time. Keeping it forever makes pickers claim a model is available long
// after account access or provider rollout changed. A short process-local TTL
// keeps the UI trustworthy without persisting account metadata to disk.
export const PROVIDER_CAPABILITY_TTL_MS = 15 * 60 * 1000
const providerCapabilityExpiryTimers = new Map<string, ReturnType<typeof setTimeout>>()

function clearProviderCapabilityTimer(provider: string) {
  const timer = providerCapabilityExpiryTimers.get(provider)
  if (timer !== undefined) clearTimeout(timer)
  providerCapabilityExpiryTimers.delete(provider)
}

function clearAllProviderCapabilityTimers() {
  for (const timer of providerCapabilityExpiryTimers.values()) clearTimeout(timer)
  providerCapabilityExpiryTimers.clear()
}

function mergeProviderCapabilityCatalog(
  providers: ProviderInfo[],
  provider: string,
  capability: ProviderCapabilitySnapshot,
): ProviderInfo[] {
  return providers.map((item) => {
    if (item.id !== provider) return item
    const discovered = capability.availableModels.filter((model) => (
      item.models.includes(model) || isFutureStudioModelID(provider, model)
    ))
    const models = [...discovered, ...item.models.filter((model) => !discovered.includes(model))]
    const previousFlagship = item.modelDetails?.find((model) => model.latest)
      || item.modelDetails?.[0]
    const futureFlagship = previousFlagship && discovered.find((model) => (
      !item.models.includes(model) && compareStudioModelVersions(provider, model, previousFlagship.id) > 0
    ))
    const detailsByID = new Map((item.modelDetails || []).map((detail) => [detail.id, detail]))
    const modelDetails = models.map((model) => {
      const existing = detailsByID.get(model)
      const inferred = existing || (previousFlagship ? {
        ...previousFlagship,
        id: model,
        description: `Account-advertised ${provider.toUpperCase()} model · inferred flagship capabilities`,
      } : {
        id: model,
        contextWindow: provider === 'kimi' ? 1_048_576 : 1_000_000,
        defaultMaxOutputTokens: 32_768,
        maxOutputTokens: 131_072,
        inputModalities: provider === 'kimi' ? ['text', 'image'] : ['text'],
        reasoningControl: provider === 'kimi' ? 'low / high / max' : 'high / max',
        description: `Account-advertised ${provider.toUpperCase()} model`,
        latest: false,
        recommended: false,
      })
      return {
        ...inferred,
        latest: futureFlagship ? model === futureFlagship : !!existing?.latest,
        recommended: capability.recommendedModel
          ? model === capability.recommendedModel
          : !!existing?.recommended,
      }
    })
    return { ...item, models, modelDetails }
  })
}

function applyProviderCapabilities(
  baseProviders: ProviderInfo[],
  capabilities: Record<string, ProviderCapabilitySnapshot | undefined>,
): ProviderInfo[] {
  return Object.entries(capabilities).reduce(
    (catalog, [provider, capability]) => capability
      ? mergeProviderCapabilityCatalog(catalog, provider, capability)
      : catalog,
    baseProviders,
  )
}

interface SettingsStore {
  settings: Settings
  // Unsaved SettingsPage edits survive tab/project navigation for the life of
  // the process. Deliberately memory-only: API keys must not be copied into
  // localStorage/sessionStorage merely to preserve a form draft.
  settingsDraft: Settings | null
  providerCapabilities: Record<string, ProviderCapabilitySnapshot | undefined>
  providerCredentialSources: Record<string, string | undefined>
  // Catalog returned by GetProviders before account-specific discovery is
  // merged in. Dynamic future IDs must be rebuildable from this baseline so
  // changing credentials or expiring a probe removes stale account models.
  baseProviders: ProviderInfo[]
  providers: ProviderInfo[]
  setSettings: (s: Settings) => void
  setProviders: (p: ProviderInfo[]) => void
  updateField: (key: keyof Settings, value: string | number | boolean) => void
  setSettingsDraft: (draft: Settings | null) => void
  setProviderCapability: (provider: string, capability: ProviderCapabilitySnapshot | null) => void
  setProviderCredentialSources: (sources: Record<string, string>) => void
}

export const useSettingsStore = create<SettingsStore>((set) => ({
  settings: {
    theme: 'dark',
    defaultProvider: 'glm',
    defaultModel: 'glm-5.2',
    glmKey: '',
    kimiKey: '',
    globalInstructions: '',
    defaultThinkingMode: '',
    defaultThinkingBudget: 0,
    defaultBudgetUSD: 0,
    autoCleanupDisabled: false,
    autoBackupEnabled: false,
    quickEntryEnabled: false,
    quickEntryShortcut: 'Alt+Space',
    voiceShortcutEnabled: false,
    voiceShortcut: 'Alt+Shift+D',
    keepAwakeEnabled: false,
    autoUpdateCheckDisabled: false,
    autoArchivePRAfterClose: false,
  },
  settingsDraft: null,
  providerCapabilities: {},
  providerCredentialSources: {},
  baseProviders: [],
  providers: [],
  // An authoritative settings reload can replace credentials (for example
  // after restore/import), so any account capability probe is stale. Resolved
  // credential sources arrive through their own backend RPC and are not
  // cleared here, avoiding a Promise.all race during application startup.
  setSettings: (settings) => {
    clearAllProviderCapabilityTimers()
    set((state) => ({
      settings,
      providerCapabilities: {},
      providers: state.baseProviders,
    }))
  },
  setProviders: (providers) => set((state) => ({
    baseProviders: providers,
    providers: applyProviderCapabilities(providers, state.providerCapabilities),
  })),
  updateField: (key, value) =>
    set((s) => ({ settings: { ...s.settings, [key]: value } })),
  setSettingsDraft: (settingsDraft) => set({ settingsDraft }),
  setProviderCapability: (provider, capability) => {
    clearProviderCapabilityTimer(provider)
    if (capability) {
      const checkedAt = Number(capability.checkedAt) || Date.now()
      const normalized = { ...capability, checkedAt }
      const delay = Math.max(0, checkedAt + PROVIDER_CAPABILITY_TTL_MS - Date.now())
      const timer = setTimeout(() => {
        providerCapabilityExpiryTimers.delete(provider)
        set((state) => {
          if (state.providerCapabilities[provider]?.checkedAt !== checkedAt) return state
          const next = { ...state.providerCapabilities }
          delete next[provider]
          return {
            providerCapabilities: next,
            providers: applyProviderCapabilities(state.baseProviders, next),
          }
        })
      }, delay)
      providerCapabilityExpiryTimers.set(provider, timer)
      set((state) => {
        const next = { ...state.providerCapabilities, [provider]: normalized }
        return {
          providerCapabilities: next,
          providers: applyProviderCapabilities(state.baseProviders, next),
        }
      })
      return
    }
    set((state) => {
      if (!(provider in state.providerCapabilities)) return state
      const next = { ...state.providerCapabilities }
      delete next[provider]
      return {
        providerCapabilities: next,
        providers: applyProviderCapabilities(state.baseProviders, next),
      }
    })
  },
  setProviderCredentialSources: (providerCredentialSources) => set({ providerCredentialSources }),
}))
