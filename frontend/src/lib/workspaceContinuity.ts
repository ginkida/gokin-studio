export type ProjectWorkspaceView = 'chat' | 'files' | 'artifacts'

export type ProjectWorkspaceLocation = {
  view: ProjectWorkspaceView
  sessionID?: string
  updatedAt: number
}

export type WorkspaceContinuity = {
  version: 1
  activeProjectID?: string
  lastView: 'project' | 'settings'
  projects: Record<string, ProjectWorkspaceLocation>
}

const STORAGE_KEY = 'gokin:workspace-continuity-v1'
const MAX_PROJECTS = 100
const MAX_STORAGE_BYTES = 64 * 1024
const MAX_ID_LENGTH = 512

function emptyContinuity(): WorkspaceContinuity {
  return { version: 1, lastView: 'project', projects: {} }
}

function validID(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value.length <= MAX_ID_LENGTH && !value.includes('\0')
}

export function parseWorkspaceContinuity(raw: string | null): WorkspaceContinuity {
  if (!raw || raw.length > MAX_STORAGE_BYTES) return emptyContinuity()
  try {
    const source = JSON.parse(raw)
    const next = emptyContinuity()
    if (validID(source?.activeProjectID)) next.activeProjectID = source.activeProjectID
    if (source?.lastView === 'settings') next.lastView = 'settings'
    const entries = source?.projects && typeof source.projects === 'object'
      ? Object.entries(source.projects).slice(0, MAX_PROJECTS)
      : []
    for (const [projectID, value] of entries) {
      if (!validID(projectID) || !value || typeof value !== 'object') continue
      const record = value as Record<string, unknown>
      if (record.view !== 'chat' && record.view !== 'files' && record.view !== 'artifacts') continue
      const location: ProjectWorkspaceLocation = {
        view: record.view,
        updatedAt: typeof record.updatedAt === 'number' && Number.isFinite(record.updatedAt) ? record.updatedAt : 0,
      }
      if (validID(record.sessionID)) location.sessionID = record.sessionID
      next.projects[projectID] = location
    }
    return next
  } catch {
    return emptyContinuity()
  }
}

export function readWorkspaceContinuity(): WorkspaceContinuity {
  try { return parseWorkspaceContinuity(localStorage.getItem(STORAGE_KEY)) } catch { return emptyContinuity() }
}

function writeWorkspaceContinuity(state: WorkspaceContinuity) {
  try { localStorage.setItem(STORAGE_KEY, JSON.stringify(state)) } catch { /* unavailable or full */ }
}

function trimProjects(projects: Record<string, ProjectWorkspaceLocation>, keepProjectID?: string) {
  const entries = Object.entries(projects)
  if (entries.length <= MAX_PROJECTS) return projects
  entries.sort((left, right) => right[1].updatedAt - left[1].updatedAt)
  const kept = entries.slice(0, MAX_PROJECTS)
  if (keepProjectID && projects[keepProjectID] && !kept.some(([id]) => id === keepProjectID)) {
    kept[kept.length - 1] = [keepProjectID, projects[keepProjectID]]
  }
  return Object.fromEntries(kept)
}

export function persistActiveProject(projectID: string | null) {
  const state = readWorkspaceContinuity()
  if (validID(projectID)) state.activeProjectID = projectID
  else delete state.activeProjectID
  writeWorkspaceContinuity(state)
}

export function persistWorkspaceLocation(
  projectID: string | null,
  view: ProjectWorkspaceView | 'settings',
  sessionID?: string,
) {
  const state = readWorkspaceContinuity()
  if (view === 'settings') {
    state.lastView = 'settings'
    writeWorkspaceContinuity(state)
    return
  }
  if (!validID(projectID)) return
  state.activeProjectID = projectID
  state.lastView = 'project'
  state.projects[projectID] = {
    view,
    ...(validID(sessionID) ? { sessionID } : {}),
    updatedAt: Date.now(),
  }
  state.projects = trimProjects(state.projects, projectID)
  writeWorkspaceContinuity(state)
}

export function forgetWorkspaceProject(projectID: string) {
  if (!validID(projectID)) return
  const state = readWorkspaceContinuity()
  delete state.projects[projectID]
  if (state.activeProjectID === projectID) delete state.activeProjectID
  writeWorkspaceContinuity(state)
}
