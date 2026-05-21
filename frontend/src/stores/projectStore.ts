import { create } from 'zustand'

export interface ProjectInfo {
  id: string
  name: string
  directory: string
  directoryOK?: boolean
  provider: string
  model: string
  active: boolean
  gitBranch: string
  lastUsedAt?: number
  systemPrompt?: string
  temperature?: number
  maxTokens?: number
  thinkingMode?: string  // "" | "enabled" | "disabled"
  thinkingBudget?: number
  budgetUSD?: number
  enforceBudget?: boolean
  pinned?: boolean
  contextWindow?: number
  pinnedContext?: string
}

interface ProjectStore {
  projects: ProjectInfo[]
  activeProjectId: string | null
  setProjects: (projects: ProjectInfo[]) => void
  setActiveProject: (id: string) => void
  updateProject: (id: string, updates: Partial<ProjectInfo>) => void
  addProject: (project: ProjectInfo) => void
  removeProject: (id: string) => void
  bumpLastUsed: (id: string) => void
}

export const useProjectStore = create<ProjectStore>((set) => ({
  projects: [],
  activeProjectId: null,
  setProjects: (projects) => set({ projects }),
  setActiveProject: (id) => set({ activeProjectId: id }),
  updateProject: (id, updates) =>
    set((state) => ({
      projects: state.projects.map((p) =>
        p.id === id ? { ...p, ...updates } : p
      ),
    })),
  addProject: (project) =>
    set((state) => ({ projects: [...state.projects, project] })),
  removeProject: (id) =>
    set((state) => {
      const remaining = state.projects.filter((p) => p.id !== id)
      let nextActive = state.activeProjectId
      if (state.activeProjectId === id) {
        // Auto-select the most-recently-used remaining project so the user
        // isn't dumped into a blank "No project selected" state. Falls back
        // to null only when no projects remain.
        const sorted = [...remaining].sort((a, b) => (b.lastUsedAt || 0) - (a.lastUsedAt || 0))
        nextActive = sorted[0]?.id || null
      }
      return { projects: remaining, activeProjectId: nextActive }
    }),
  bumpLastUsed: (id) =>
    set((state) => ({
      projects: state.projects.map((p) =>
        p.id === id ? { ...p, lastUsedAt: Date.now() } : p
      ),
    })),
}))
