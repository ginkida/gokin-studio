import { create } from 'zustand'

export interface Settings {
  theme: string
  defaultProvider: string
  defaultModel: string
  glmKey: string
  minimaxKey: string
  kimiKey: string
  deepseekKey: string
  ollamaUrl: string
  defaultThinkingMode: string   // "" | "enabled" | "disabled"
  defaultThinkingBudget: number // 0 = use provider default (4096)
  defaultBudgetUSD: number      // 0 = no budget for new projects
  autoCleanupDisabled: boolean  // iter 790+ opt-out of once-per-24h background cleanup
  autoBackupEnabled: boolean    // iter 840+ opt-in to once-per-24h tar.gz auto-backup
}

export interface ProviderInfo {
  id: string
  name: string
  models: string[]
}

interface SettingsStore {
  settings: Settings
  providers: ProviderInfo[]
  setSettings: (s: Settings) => void
  setProviders: (p: ProviderInfo[]) => void
  updateField: (key: keyof Settings, value: string | number | boolean) => void
}

export const useSettingsStore = create<SettingsStore>((set) => ({
  settings: {
    theme: 'dark',
    defaultProvider: 'glm',
    defaultModel: 'glm-5.2',
    glmKey: '',
    minimaxKey: '',
    kimiKey: '',
    deepseekKey: '',
    ollamaUrl: 'http://localhost:11434',
    defaultThinkingMode: '',
    defaultThinkingBudget: 0,
    defaultBudgetUSD: 0,
    autoCleanupDisabled: false,
    autoBackupEnabled: false,
  },
  providers: [],
  setSettings: (settings) => set({ settings }),
  setProviders: (providers) => set({ providers }),
  updateField: (key, value) =>
    set((s) => ({ settings: { ...s.settings, [key]: value } })),
}))
