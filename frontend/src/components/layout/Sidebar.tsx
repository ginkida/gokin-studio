import { useState, useEffect, useRef, useCallback, useMemo } from 'react'
import { useProjectStore, ProjectInfo } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { ProviderSelect } from '../project/ProviderSelect'
import { Zap, FolderPlus, Trash2, GitBranch, Settings, FolderOpen, Folder, Search, X, Pin, PinOff, Download, Upload, Volume2, VolumeX, Archive, ArchiveRestore, Loader2, AlertTriangle, CheckCircle } from 'lucide-react'
import { AddProject, ArchiveProject, ListArchivedProjects, RestoreProject, RemoveProject, BrowseDirectory, RenameProject, SetProjectPinned, ExportProjectJSON, ImportProjectJSON, GetProjectOrder, ReorderProjects } from '../../../wailsjs/go/studio/Studio'
import { isProjectMuted, toggleProjectMute, unmuteProject, clearProjectLocalStorage } from '../../lib/mutedProjects'
import { hasOpenModal } from '../../hooks/useModalFocusManagement'
import { useConfirmDialog } from '../common/AppDialog'
import { useSettingsStore } from '../../stores/settingsStore'
import { formatContextWindow } from '../../lib/modelCapabilities'
import { formatProviderModelLabel } from '../../lib/providerCatalog'

interface SidebarProps {
  onOpenSettings?: () => void
  onToggleCollapse?: () => void
  onNavigate?: () => void
  collapsed?: boolean
  compactDrawer?: boolean
}

type ArchivedProjectInfo = {
  id: string
  name: string
  directory: string
  directoryOK: boolean
  provider: string
  model: string
  archivedAt: number
}

export function Sidebar({ onOpenSettings, onToggleCollapse, onNavigate, collapsed, compactDrawer }: SidebarProps) {
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const setActiveProject = useProjectStore((s) => s.setActiveProject)
  const addProjectToStore = useProjectStore((s) => s.addProject)
  const removeProjectFromStore = useProjectStore((s) => s.removeProject)
  const settings = useSettingsStore((s) => s.settings)
  const providers = useSettingsStore((s) => s.providers)
  const providerCapabilities = useSettingsStore((s) => s.providerCapabilities)
  const providerCredentialSources = useSettingsStore((s) => s.providerCredentialSources)

  const [showAdd, setShowAdd] = useState(false)
  const [newName, setNewName] = useState('')
  const [newDir, setNewDir] = useState('')
  const [addBusy, setAddBusy] = useState(false)
  const [browseBusy, setBrowseBusy] = useState(false)
  const [error, setError] = useState('')
  const [renameError, setRenameError] = useState<string | null>(null)
  const [removeError, setRemoveError] = useState<string | null>(null)
  const [removeErrorProjectId, setRemoveErrorProjectId] = useState<string | null>(null)
  const [editingName, setEditingName] = useState<string | null>(null)
  const [editNameValue, setEditNameValue] = useState('')
  const [ctxMenu, setCtxMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  const ctxMenuRef = useRef<HTMLDivElement>(null)
  const ctxMenuTriggerRef = useRef<HTMLButtonElement | null>(null)
  const ctxMenuWasOpenRef = useRef(false)
  const [showArchived, setShowArchived] = useState(false)
  const [archivedProjects, setArchivedProjects] = useState<ArchivedProjectInfo[] | null>(null)
  const [archiveBusy, setArchiveBusy] = useState<string | null>(null)
  const [archiveError, setArchiveError] = useState<string | null>(null)
  const [archiveLoadError, setArchiveLoadError] = useState<string | null>(null)
  // Project import (iter 560+) — paste-textarea modal triggered from the
  // sidebar header. Same paste-flow pattern as session import (iter 550+)
  // because Wails desktop apps can't reliably pop a "browse for file"
  // dialog without OS plumbing.
  const [showImportProject, setShowImportProject] = useState(false)
  const [importJSON, setImportJSON] = useState('')
  const [importDir, setImportDir] = useState('')
  const [importError, setImportError] = useState<string | null>(null)
  const [importBusy, setImportBusy] = useState(false)
  const [importBrowseBusy, setImportBrowseBusy] = useState(false)
  // Sidebar project search. Only shown when there are 4+ projects so single-
  // project / few-project users don't see a redundant search input. Filters
  // by name, directory, and git branch (case-insensitive substring).
  const [searchQuery, setSearchQuery] = useState('')
  const [searchForcedOpen, setSearchForcedOpen] = useState(false)
  const searchInputRef = useRef<HTMLInputElement>(null)
  const projectListRef = useRef<HTMLDivElement>(null)
  const searchFocusPendingRef = useRef(false)
  const [sidebarNotice, setSidebarNotice] = useState<{ kind: 'success' | 'error'; text: string } | null>(null)
  const noticeTimerRef = useRef<number | null>(null)
  const orderRequestRef = useRef(0)
  const archivedRequestRef = useRef(0)
  const renameInFlightRef = useRef(new Set<string>())
  const removeInFlightRef = useRef(new Set<string>())
  const removeFeedbackRef = useRef(0)
  const [renameBusyProjectId, setRenameBusyProjectId] = useState<string | null>(null)
  const [removeBusyProjectIds, setRemoveBusyProjectIds] = useState<Set<string>>(() => new Set())
  const [pinBusyProjectId, setPinBusyProjectId] = useState<string | null>(null)
  const updateProject = useProjectStore((s) => s.updateProject)
  const defaultProvider = settings.defaultProvider || 'glm'
  const defaultModel = settings.defaultModel || (defaultProvider === 'kimi' ? 'k3' : 'glm-5.2')
  const defaultProviderInfo = providers.find((item) => item.id === defaultProvider)
  const defaultModelInfo = defaultProviderInfo?.modelDetails?.find((item) => item.id === defaultModel)
  const defaultCapability = providerCapabilities[defaultProvider]
  const defaultModelUnavailable = !!defaultCapability?.availableModels.length && !defaultCapability.availableModels.includes(defaultModel)
  const defaultSavedKey = defaultProvider === 'kimi' ? settings.kimiKey : settings.glmKey
  const defaultCredentialSource = providerCredentialSources[defaultProvider]
  const defaultCredentialMissing = !defaultSavedKey.trim() && defaultCredentialSource !== undefined && defaultCredentialSource !== 'env'
  const defaultSetupBlocked = defaultModelUnavailable || defaultCredentialMissing
  const addFormDirty = newName.trim().length > 0 || newDir.trim().length > 0

  // Sidebar-owned dialogs must close from any focused control, not only from
  // their textarea. While an import/archive mutation is in flight, Escape is
  // consumed without dismissing the progress surface.
  useEffect(() => {
    if (!showImportProject && !showArchived) return
    const onKey = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229 || event.key !== 'Escape') return
      event.preventDefault()
      if (showImportProject) {
        if (importBusy || importBrowseBusy) return
        setShowImportProject(false)
        setImportError(null)
        return
      }
      if (showArchived) {
        if (archiveBusy) return
        setShowArchived(false)
        setArchiveError(null)
        setArchiveLoadError(null)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showImportProject, importBusy, importBrowseBusy, showArchived, archiveBusy])

  const showSidebarNotice = useCallback((kind: 'success' | 'error', text: string) => {
    if (noticeTimerRef.current !== null) window.clearTimeout(noticeTimerRef.current)
    setSidebarNotice({ kind, text })
    noticeTimerRef.current = window.setTimeout(() => {
      setSidebarNotice(null)
      noticeTimerRef.current = null
    }, kind === 'error' ? 6500 : 3500)
  }, [])

  useEffect(() => () => {
    if (noticeTimerRef.current !== null) window.clearTimeout(noticeTimerRef.current)
  }, [])

  const requestProjectSearchFocus = useCallback(() => {
    searchFocusPendingRef.current = true
    setSearchForcedOpen(true)
    if (collapsed) onToggleCollapse?.()
  }, [collapsed, onToggleCollapse])

  // Complete focus only after React has revealed the search and, when needed,
  // the parent has expanded the sidebar. A DOM timeout from the command
  // palette is not sufficient when both changes render in the same frame.
  useEffect(() => {
    if (!searchFocusPendingRef.current || collapsed || !searchInputRef.current) return
    searchFocusPendingRef.current = false
    searchInputRef.current.focus()
    searchInputRef.current.select()
  }, [collapsed, searchForcedOpen])

  // Ctrl+Shift+G / Cmd+Shift+G focuses the sidebar search. Distinct from
  // Ctrl+P (file picker) and Ctrl+K (command palette) — this one targets the
  // visual project list specifically. Cmd/Ctrl+Shift+B is reserved for the
  // Claude-compatible Browser / Preview pane. The custom event gives the command
  // palette the same reliable path.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      const isSearchHotkey = (e.ctrlKey || e.metaKey) && e.shiftKey && (e.key === 'G' || e.key === 'g')
      if (!isSearchHotkey) return
      if (hasOpenModal()) { e.preventDefault(); return }
      e.preventDefault()
      requestProjectSearchFocus()
    }
    const onFocusRequest = () => requestProjectSearchFocus()
    window.addEventListener('keydown', onKey)
    window.addEventListener('gokin:focus-project-search', onFocusRequest)
    return () => {
      window.removeEventListener('keydown', onKey)
      window.removeEventListener('gokin:focus-project-search', onFocusRequest)
    }
  }, [requestProjectSearchFocus])

  // Close context menu on Escape.
  useEffect(() => {
    if (!ctxMenu) return
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      if (e.key === 'Escape') { e.preventDefault(); setCtxMenu(null) }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [ctxMenu])

  useEffect(() => {
    if (ctxMenu) {
      ctxMenuWasOpenRef.current = true
      requestAnimationFrame(() => ctxMenuRef.current?.querySelector<HTMLButtonElement>('button:not([disabled])')?.focus())
      return
    }
    if (!ctxMenuWasOpenRef.current) return
    ctxMenuWasOpenRef.current = false
    requestAnimationFrame(() => ctxMenuTriggerRef.current?.focus())
  }, [ctxMenu])

  // Per-project mute (iter 580+) — increment a tick whenever the muted set
  // changes so the project list re-renders with updated VolumeX icons.
  const [muteTick, setMuteTick] = useState(0)
  useEffect(() => {
    const refresh = () => setMuteTick((t) => t + 1)
    window.addEventListener('gokin:muted-changed', refresh)
    return () => window.removeEventListener('gokin:muted-changed', refresh)
  }, [])

  // The no-project empty state can open this form without duplicating project
  // creation logic in ChatPanel. Expand the sidebar first if it is collapsed.
  useEffect(() => {
    const openAddProject = () => {
      if (collapsed) onToggleCollapse?.()
      setShowAdd(true)
      setError('')
    }
    window.addEventListener('gokin:add-project', openAddProject)
    return () => window.removeEventListener('gokin:add-project', openAddProject)
  }, [collapsed, onToggleCollapse])

  // iter 1060+: user-defined project order, loaded once on mount and
  // updated optimistically on each drop (then persisted server-side).
  // Empty array = "no custom order" → projects sort by lastUsedAt-default.
  // The order maps an ID to its index in this array; indices not in the
  // map fall to the end of the sort.
  const [projectOrder, setProjectOrder] = useState<string[]>([])
  const [projectOrderStatus, setProjectOrderStatus] = useState<'loading' | 'ready' | 'error'>('loading')
  const [projectOrderSaveBusy, setProjectOrderSaveBusy] = useState(false)
  const [draggingProjectId, setDraggingProjectId] = useState<string | null>(null)
  const [dropTargetId, setDropTargetId] = useState<string | null>(null)
  const loadProjectOrder = useCallback(async () => {
    const request = ++orderRequestRef.current
    setProjectOrderStatus('loading')
    try {
      const order = await GetProjectOrder()
      if (orderRequestRef.current !== request) return
      setProjectOrder(Array.isArray(order) ? order : [])
      setProjectOrderStatus('ready')
    } catch {
      if (orderRequestRef.current !== request) return
      setProjectOrderStatus('error')
    }
  }, [])
  useEffect(() => {
    void loadProjectOrder()
    return () => { orderRequestRef.current += 1 }
  }, [loadProjectOrder])

  const persistProjectOrder = useCallback(async (next: string[], previous: string[]) => {
    if (projectOrderSaveBusy || projectOrderStatus !== 'ready') return
    setProjectOrder(next)
    setProjectOrderSaveBusy(true)
    try {
      await ReorderProjects(next)
    } catch {
      setProjectOrder(previous)
      showSidebarNotice('error', 'Could not save project order. The previous order was restored.')
    } finally {
      setProjectOrderSaveBusy(false)
    }
  }, [projectOrderSaveBusy, projectOrderStatus, showSidebarNotice])

  const orderedProjects = useMemo(() => {
    const orderIndex = new Map(projectOrder.map((id, index) => [id, index] as const))
    return [...projects].sort((a, b) => {
      // Pinning is an anchor, not just a saved index: keyboard and pointer
      // reordering both operate within the matching pinned/unpinned group.
      if (!!a.pinned !== !!b.pinned) return a.pinned ? -1 : 1
      const aIndex = orderIndex.get(a.id)
      const bIndex = orderIndex.get(b.id)
      if (aIndex !== undefined && bIndex !== undefined) return aIndex - bIndex
      if (aIndex !== undefined) return -1
      if (bIndex !== undefined) return 1
      const aUsed = a.lastUsedAt || 0
      const bUsed = b.lastUsedAt || 0
      if (aUsed !== bUsed) {
        if (aUsed === 0) return 1
        if (bUsed === 0) return -1
        return bUsed - aUsed
      }
      return a.name.localeCompare(b.name)
    })
  }, [projects, projectOrder])

  const moveProjectInOrder = useCallback((projectID: string, delta: -1 | 1) => {
    if (searchQuery.trim() || projectOrderStatus !== 'ready' || projectOrderSaveBusy) return
    const target = orderedProjects.find((project) => project.id === projectID)
    if (!target) return
    const group = orderedProjects.filter((project) => !!project.pinned === !!target.pinned)
    const index = group.findIndex((project) => project.id === projectID)
    const neighbor = group[index + delta]
    if (index < 0 || !neighbor) return
    const next = orderedProjects.map((project) => project.id)
    const targetIndex = next.indexOf(projectID)
    const neighborIndex = next.indexOf(neighbor.id)
    if (targetIndex < 0 || neighborIndex < 0) return
    ;[next[targetIndex], next[neighborIndex]] = [next[neighborIndex], next[targetIndex]]
    setCtxMenu(null)
    void persistProjectOrder(next, projectOrder)
  }, [searchQuery, projectOrderStatus, projectOrderSaveBusy, orderedProjects, persistProjectOrder, projectOrder])
  // muteTick is read in the render path indirectly via isProjectMuted() —
  // this comment exists so a future refactor doesn't strip the seemingly-
  // unused state variable.
  void muteTick

  // Derive per-project "any session active" from the chat store so the
  // sidebar dot stays green as long as at least one session is running,
  // even when a sibling session just finished. project.active on its own
  // flip-flops with each session's start/stop.
  const sessionActive = useChatStore((s) => s.sessionActive)
  const isProjectActive = (projectID: string): boolean => {
    const prefix = projectID + '_'
    for (const [key, active] of Object.entries(sessionActive)) {
      if (active && key.startsWith(prefix)) return true
    }
    return false
  }

  const handleRename = async (id: string) => {
    const trimmed = editNameValue.trim()
    if (!trimmed) { setEditingName(null); return }
    if (renameInFlightRef.current.has(id)) return
    renameInFlightRef.current.add(id)
    setRenameBusyProjectId(id)
    setRenameError(null)
    try {
      await RenameProject(id, trimmed)
      updateProject(id, { name: trimmed })
      setEditingName(null)
    } catch (e: any) {
      setRenameError(String(e?.message || e || 'Rename failed'))
      // Keep the rename input open so the user can correct the name.
    } finally {
      renameInFlightRef.current.delete(id)
      setRenameBusyProjectId((current) => current === id ? null : current)
    }
  }

  const handleAdd = async () => {
    if (addBusy || browseBusy || !newName.trim() || !newDir.trim()) return
    if (defaultCredentialMissing) {
      setError(`Connect ${defaultProvider === 'kimi' ? 'Kimi' : 'GLM'} in Settings before creating this project.`)
      return
    }
    if (defaultModelUnavailable) {
      setError(`${defaultModel} is not available for the tested ${defaultProvider.toUpperCase()} key. Update the default model in Settings before creating this project.`)
      return
    }
    setAddBusy(true)
    setError('')
    try {
      const p = await AddProject(newName.trim(), newDir.trim()) as ProjectInfo
      if (!p?.id || !p.name || !p.directory) throw new Error('Backend returned an incomplete project record.')
      addProjectToStore(p)
      setActiveProject(p.id)
      onNavigate?.()
      setShowAdd(false)
      setNewName('')
      setNewDir('')
    } catch (e: any) {
      setError(String(e?.message || e || 'Could not add project'))
    } finally {
      setAddBusy(false)
    }
  }

  const requestCloseAdd = useCallback(async () => {
    if (addBusy || browseBusy) return
    if (addFormDirty && !(await requestConfirmation({
      title: 'Discard new project draft?',
      message: 'The unsaved project name and directory will be lost. No project has been created yet.',
      confirmLabel: 'Discard draft',
      cancelLabel: 'Keep editing',
      danger: true,
    }))) return
    setShowAdd(false)
    setNewName('')
    setNewDir('')
    setError('')
  }, [addBusy, browseBusy, addFormDirty, requestConfirmation])

  useEffect(() => {
    if (!showAdd) return
    const onKey = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229 || event.key !== 'Escape' || hasOpenModal()) return
      event.preventDefault()
      void requestCloseAdd()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [showAdd, requestCloseAdd])

  const handleRemove = async (id: string) => {
    if (removeInFlightRef.current.has(id)) return
    const feedback = ++removeFeedbackRef.current
    removeInFlightRef.current.add(id)
    setRemoveBusyProjectIds((current) => new Set(current).add(id))
    setRemoveError(null)
    setRemoveErrorProjectId(null)
    try {
      await RemoveProject(id)
      removeProjectFromStore(id)
      // Drop every chatStore entry keyed by this project so messages,
      // drafts, usage, etc. don't leak for the rest of the session.
      useChatStore.getState().dropProject(id)
      // Drop the mute flag so a re-added project at the same ID doesn't
      // inherit the prior session's mute state.
      unmuteProject(id)
      // Drop any other per-project frontend preferences (quiet mode,
      // budget-alert thresholds). Without this, localStorage accumulates
      // dead state for every removed project.
      clearProjectLocalStorage(id)
    } catch (err: any) {
      setRemoveError(String(err?.message || err || 'Delete failed'))
      setRemoveErrorProjectId(id)
      window.setTimeout(() => {
        if (removeFeedbackRef.current !== feedback) return
        setRemoveError(null)
        setRemoveErrorProjectId(null)
      }, 5000)
    } finally {
      removeInFlightRef.current.delete(id)
      setRemoveBusyProjectIds((current) => {
        const next = new Set(current)
        next.delete(id)
        return next
      })
    }
  }

  const requestProjectRemoval = async (project: ProjectInfo) => {
    const accepted = await requestConfirmation({
      title: `Delete “${project.name}” from Gokin Studio?`,
      message: 'This permanently removes its local chats, drafts, pins, recovery data, schedules, knowledge snapshots, and artifact versions. Files in the connected project folder are not deleted. Use Archive instead if you may need this Studio history later.',
      confirmLabel: 'Delete local data',
      danger: true,
    })
    if (accepted) await handleRemove(project.id)
  }

  const loadArchivedProjects = async () => {
    const request = ++archivedRequestRef.current
    setArchivedProjects(null)
    setArchiveLoadError(null)
    try {
      const records = await ListArchivedProjects()
      if (archivedRequestRef.current !== request) return
      if (!Array.isArray(records)) throw new Error('Backend returned an invalid archived-project list.')
      const typedRecords = records as ArchivedProjectInfo[]
      if (typedRecords.some((record) => !record?.id || !record.name || !record.directory)) {
        throw new Error('Backend returned an incomplete archived-project record.')
      }
      setArchivedProjects(typedRecords)
    } catch (err: any) {
      if (archivedRequestRef.current !== request) return
      setArchivedProjects([])
      setArchiveLoadError(String(err?.message || err || 'Could not load archived projects'))
    }
  }

  const openArchivedProjects = () => {
    setArchiveError(null)
    setShowArchived(true)
    void loadArchivedProjects()
  }

  const handleArchive = async (id: string) => {
    setArchiveBusy(`archive:${id}`)
    setArchiveError(null)
    try {
      await ArchiveProject(id)
      removeProjectFromStore(id)
      // The backend history remains intact; only discard the live frontend
      // snapshot so a hidden project cannot keep stale session UI mounted.
      useChatStore.getState().dropProject(id)
      setCtxMenu(null)
      if (showArchived) await loadArchivedProjects()
    } catch (err: any) {
      const message = String(err?.message || err || 'Archive failed')
      const feedback = ++removeFeedbackRef.current
      setArchiveError(message)
      setRemoveError(message)
      setRemoveErrorProjectId(id)
      window.setTimeout(() => {
        if (removeFeedbackRef.current !== feedback) return
        setRemoveError(null)
        setRemoveErrorProjectId(null)
      }, 5000)
    } finally {
      setArchiveBusy(null)
    }
  }

  const handleRestore = async (id: string) => {
    setArchiveBusy(`restore:${id}`)
    setArchiveError(null)
    try {
      const info = await RestoreProject(id) as ProjectInfo
      if (!info?.id || !info.name || !info.directory) throw new Error('Backend returned an incomplete restored project record.')
      addProjectToStore(info)
      setActiveProject(info.id)
      setShowArchived(false)
      onNavigate?.()
      setArchivedProjects((current) => (current || []).filter((record) => record.id !== id))
    } catch (err: any) {
      setArchiveError(String(err?.message || err || 'Restore failed'))
    } finally {
      setArchiveBusy(null)
    }
  }

  // Shorten path for display: /home/user/projects/foo -> ~/projects/foo
  const shortenPath = (dir: string) => {
    for (const prefix of ['/Users/', '/home/']) {
      if (dir.startsWith(prefix)) {
        const rest = dir.slice(prefix.length)
        const slashIdx = rest.indexOf('/')
        if (slashIdx !== -1) return '~' + rest.slice(slashIdx)
      }
    }
    return dir
  }

  return (
    <div
      id="project-sidebar"
      className={`sidebar ${collapsed ? 'collapsed' : ''}`}
      role={compactDrawer ? 'dialog' : undefined}
      aria-modal={compactDrawer ? 'true' : undefined}
      aria-label={compactDrawer ? 'Projects and workspace navigation' : undefined}
    >
      <div className="sidebar-header">
        {!collapsed && (
          <div className="sidebar-brand">
            <Zap size={18} className="sidebar-brand-icon" />
            <span>Gokin Studio</span>
          </div>
        )}
        <div className="sidebar-header-actions">
          {onOpenSettings && (
            <button className="icon-btn" onClick={onOpenSettings} title="Settings (Ctrl+3)" aria-label="Open settings">
              <Settings size={15} />
            </button>
          )}
          <button className="icon-btn" onClick={openArchivedProjects} title="Archived projects" aria-label="Open archived projects">
            <Archive size={15} />
          </button>
          <button
            className="icon-btn"
            onClick={() => {
              setImportJSON('')
              setImportDir('')
              setImportError(null)
              setShowImportProject(true)
            }}
            title="Import project from JSON"
            aria-label="Import project from JSON"
          >
            <Upload size={15} />
          </button>
          <button className="icon-btn" onClick={() => { if (showAdd) void requestCloseAdd(); else { setShowAdd(true); setError('') } }} title="Add project" aria-label="Add project">
            <FolderPlus size={15} />
          </button>
        </div>
      </div>

      {showImportProject && (
        <>
          <div className="import-backdrop" onClick={() => { if (!importBusy && !importBrowseBusy) { setShowImportProject(false); setImportError(null) } }} />
          <div className="import-modal" role="dialog" aria-modal="true" aria-label="Import project">
            <div className="import-header">
              <h3>Import project from JSON</h3>
              <button
                className="icon-btn"
                onClick={() => { if (!importBusy && !importBrowseBusy) { setShowImportProject(false); setImportError(null) } }}
                title="Close (Esc)"
              >
                <X size={14} />
              </button>
            </div>
            <p className="import-hint">
              Paste a project JSON blob produced by "Export project as JSON" and pick a target directory. The imported project lands as a fresh entry with "(imported)" suffix; existing files in the target dir are not modified. Only GLM/Kimi selections are preserved; legacy providers are safely mapped to the current Studio default.
            </p>
            <textarea
              className="import-textarea"
              placeholder='{"version":1,"name":"…","sessions":[…]}'
              value={importJSON}
              autoFocus
              maxLength={5_000_000}
              onChange={(e) => { setImportJSON(e.target.value); setImportError(null) }}
              rows={8}
            />
            <div className="dir-input-row">
              <input
                placeholder="Target directory (must exist + not already registered)"
                value={importDir}
                onChange={(e) => { setImportDir(e.target.value); setImportError(null) }}
                onKeyDown={(e) => {
                  if (e.nativeEvent.isComposing || e.keyCode === 229) return
                  if (e.key === 'Escape' && !importBusy && !importBrowseBusy) { e.preventDefault(); setShowImportProject(false); setImportError(null) }
                }}
              />
              <button
                className="icon-btn"
                disabled={importBusy || importBrowseBusy}
                onClick={async () => {
                  if (importBrowseBusy) return
                  setImportBrowseBusy(true)
                  setImportError(null)
                  try {
                    const dir = await BrowseDirectory()
                    if (dir) setImportDir(dir)
                  } catch (e: any) {
                    setImportError(`Could not open directory picker: ${String(e?.message || e || 'unknown error')}`)
                  } finally {
                    setImportBrowseBusy(false)
                  }
                }}
                title="Browse..."
              >
                {importBrowseBusy ? <Loader2 size={14} className="spin" /> : <FolderOpen size={14} />}
              </button>
            </div>
            {importError && <div className="import-error">{importError}</div>}
            <div className="import-actions">
              <span className="import-action-hint">Preserves prompts, budget, and supported GLM/Kimi models.</span>
              <button className="btn-secondary" onClick={() => { setShowImportProject(false); setImportError(null) }} disabled={importBusy || importBrowseBusy}>
                Cancel
              </button>
              <button
                className="btn-primary"
                disabled={importBusy || importBrowseBusy || !importJSON.trim() || !importDir.trim()}
                onClick={async () => {
                  setImportBusy(true)
                  setImportError(null)
                  try {
                    let sourceProvider = ''
                    let sourceModel = ''
                    try {
                      const source = JSON.parse(importJSON)
                      sourceProvider = typeof source?.provider === 'string' ? source.provider.trim().toLowerCase() : ''
                      sourceModel = typeof source?.model === 'string' ? source.model.trim() : ''
                    } catch { /* backend returns the authoritative parse error */ }
                    const info = await ImportProjectJSON(importJSON, importDir) as ProjectInfo
                    if (!info?.id || !info.name || !info.directory) throw new Error('Backend returned an incomplete imported project record.')
                    addProjectToStore(info)
                    setActiveProject(info.id)
                    onNavigate?.()
                    const selectionChanged = !!(sourceProvider || sourceModel) &&
                      (sourceProvider !== info.provider || sourceModel !== info.model)
                    showSidebarNotice(
                      'success',
                      selectionChanged
                        ? `Imported with ${formatProviderModelLabel(info.provider, info.model)}; unsupported source model/provider was migrated.`
                        : `Imported with ${formatProviderModelLabel(info.provider, info.model)}.`,
                    )
                    setShowImportProject(false)
                    setImportJSON('')
                    setImportDir('')
                  } catch (e: any) {
                    setImportError(String(e?.message || e || 'Import failed'))
                  } finally {
                    setImportBusy(false)
                  }
                }}
              >
                {importBusy ? 'Importing…' : 'Import'}
              </button>
            </div>
          </div>
        </>
      )}

      {showArchived && (
        <>
          <div className="import-backdrop" onClick={() => {
            if (!archiveBusy) { setShowArchived(false); setArchiveError(null); setArchiveLoadError(null) }
          }} />
          <div className="import-modal archived-projects-modal" role="dialog" aria-modal="true" aria-label="Archived projects">
            <div className="import-header">
              <h3><Archive size={15} /> Archived projects</h3>
              <button className="icon-btn" onClick={() => {
                setShowArchived(false); setArchiveError(null); setArchiveLoadError(null)
              }} disabled={archiveBusy !== null} title="Close" aria-label="Close archived projects">
                <X size={14} />
              </button>
            </div>
            <p className="import-hint">
              Archived projects are hidden and inactive. Their chats, memory, knowledge, artifacts, pins, drafts, and scheduled routines stay on this computer. Automatic routines resume from the next future occurrence after restore.
            </p>
            {archiveError && <div className="import-error">{archiveError}</div>}
            {archiveLoadError ? (
              <div className="archived-projects-empty" role="alert">
                <AlertTriangle size={22} />
                <span>{archiveLoadError}</span>
                <button className="btn-secondary" type="button" onClick={() => void loadArchivedProjects()}>Retry</button>
              </div>
            ) : archivedProjects === null ? (
              <div className="archived-projects-empty"><Loader2 size={15} className="spin" /> Loading…</div>
            ) : archivedProjects.length === 0 ? (
              <div className="archived-projects-empty"><Archive size={22} /> No archived projects</div>
            ) : (
              <div className="archived-projects-list">
                {archivedProjects.map((project) => (
                  <div className="archived-project-row" key={project.id}>
                    <div className="archived-project-icon"><Archive size={15} /></div>
                    <div className="archived-project-details">
                      <strong>{project.name}</strong>
                      <span title={project.directory}>{shortenPath(project.directory)}</span>
                      <small>
                        {formatProviderModelLabel(project.provider, project.model)} · archived {new Date(project.archivedAt).toLocaleDateString()}
                        {!project.directoryOK && ' · folder unavailable'}
                      </small>
                    </div>
                    <button
                      className="btn-secondary"
                      disabled={archiveBusy !== null}
                      onClick={() => void handleRestore(project.id)}
                    >
                      {archiveBusy === `restore:${project.id}` ? <Loader2 size={12} className="spin" /> : <ArchiveRestore size={12} />}
                      Restore
                    </button>
                  </div>
                ))}
              </div>
            )}
          </div>
        </>
      )}

      {showAdd && (
        <div className="add-project-form">
          <input
            placeholder="Project name"
            aria-label="Project name"
            value={newName}
            maxLength={60}
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) return
              if (e.key === 'Enter' && newDir.trim()) handleAdd()
            }}
            autoFocus
          />
          <div className="dir-input-row">
            <input
              placeholder="Select directory..."
              aria-label="Project directory"
              value={newDir}
              onChange={(e) => setNewDir(e.target.value)}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing || e.keyCode === 229) return
                if (e.key === 'Enter') handleAdd()
              }}
            />
            <button
              className="btn-secondary browse-btn"
              disabled={addBusy || browseBusy}
              onClick={async () => {
                setBrowseBusy(true)
                setError('')
                try {
                  const dir = await BrowseDirectory()
                  if (dir) {
                    setNewDir(dir)
                    if (!newName.trim()) {
                      const parts = dir.replace(/[\\/]+$/, '').split(/[\\/]/)
                      setNewName(parts[parts.length - 1] || '')
                    }
                  }
                } catch (e: any) {
                  console.error('BrowseDirectory error:', e)
                  setError(`Could not open directory picker: ${String(e?.message || e || 'unknown error')}`)
                } finally {
                  setBrowseBusy(false)
                }
              }}
              title="Browse..."
              aria-label="Browse for project directory"
            >
              {browseBusy ? <Loader2 size={14} className="spin" /> : <FolderOpen size={14} />}
            </button>
          </div>
          <div className={`add-project-model-preview ${defaultSetupBlocked ? 'unavailable' : ''}`}>
            <span className={`provider-dot ${defaultProvider}`} aria-hidden />
            <span className="add-project-model-copy">
              <strong>{defaultProviderInfo?.name || (defaultProvider === 'kimi' ? 'Kimi Code' : 'GLM (Z.AI)')} · <code>{defaultModel}</code></strong>
              <small>
                {defaultModelInfo
                  ? `${formatContextWindow(defaultModelInfo.contextWindow)} context · ${defaultModelInfo.inputModalities.join(' + ')} · ${defaultModelInfo.reasoningControl} reasoning`
                  : 'Inherited from Settings'}
              </small>
            </span>
            {defaultSetupBlocked
              ? <AlertTriangle size={13} aria-label={defaultCredentialMissing ? 'Provider connection required' : 'Unavailable for tested key'} />
              : defaultCapability?.availableModels.length
                ? <CheckCircle size={13} aria-label="Available for tested key" />
                : null}
          </div>
          {defaultCredentialMissing ? (
            <div className="add-project-model-warning" role="alert">
              <span>{defaultProvider === 'kimi' ? 'Kimi' : 'GLM'} is not connected.</span>
              <button
                type="button"
                onClick={() => window.dispatchEvent(new CustomEvent('gokin:open-settings', { detail: { section: 'settings-connections' } }))}
              >
                Connect in Settings
              </button>
            </div>
          ) : defaultModelUnavailable && (
            <div className="add-project-model-warning" role="alert">
              <span><code>{defaultModel}</code> is unavailable for the tested key.</span>
              <button
                type="button"
                onClick={() => window.dispatchEvent(new CustomEvent('gokin:open-settings', { detail: { section: 'settings-models' } }))}
              >
                Fix in Settings
              </button>
            </div>
          )}
          {error && <div className="form-error" role="alert">{error}</div>}
          <div className="add-project-actions">
            <button className="btn-secondary" onClick={() => void requestCloseAdd()} disabled={addBusy || browseBusy}>Cancel</button>
            <button className="btn-primary" onClick={handleAdd} disabled={addBusy || browseBusy || defaultSetupBlocked || !newName.trim() || !newDir.trim()}>
              {addBusy ? <><Loader2 size={12} className="spin" /> Creating…</> : 'Add'}
            </button>
          </div>
        </div>
      )}

      {/* Normally hidden for short project lists, but keyboard/palette access
          can reveal it on demand. */}
      {!collapsed && (projects.length >= 4 || searchForcedOpen) && (
        <div className="sidebar-search">
          <Search size={12} className="sidebar-search-icon" />
          <input
            ref={searchInputRef}
            type="search"
            className="sidebar-search-input"
            value={searchQuery}
            placeholder="Search projects… (Ctrl+Shift+G)"
            maxLength={100}
            aria-label="Search projects"
            aria-controls="sidebar-project-list"
            aria-keyshortcuts="Control+Shift+G Meta+Shift+G"
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) return
              if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                const items = Array.from(projectListRef.current?.querySelectorAll<HTMLButtonElement>('button.project-open-control:not([disabled])') || [])
                if (items.length === 0) return
                e.preventDefault()
                const target = e.key === 'ArrowUp' ? items[items.length - 1] : items[0]
                target?.focus()
                target?.scrollIntoView({ block: 'nearest' })
                return
              }
              if (e.key === 'Escape') {
                e.preventDefault()
                setSearchQuery('')
                if (projects.length < 4) setSearchForcedOpen(false)
                searchInputRef.current?.blur()
              }
            }}
          />
          {searchQuery && (
            <button
              className="sidebar-search-clear"
              onClick={() => { setSearchQuery(''); searchInputRef.current?.focus() }}
              title="Clear search (Esc)"
              aria-label="Clear project search"
            >
              <X size={11} />
            </button>
          )}
        </div>
      )}

      <div
        id="sidebar-project-list"
        ref={projectListRef}
        className="project-list"
        role="navigation"
        aria-label="Projects"
        onKeyDown={(event) => {
          if (event.nativeEvent.isComposing || event.keyCode === 229) return
          const target = event.target as HTMLElement
          if (!target.matches('button.project-open-control')) return
          if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
            const projectID = target.dataset.projectId
            if (!projectID) return
            event.preventDefault()
            const rect = target.getBoundingClientRect()
            ctxMenuTriggerRef.current = target as HTMLButtonElement
            setCtxMenu({ id: projectID, x: rect.left + 16, y: rect.bottom })
            return
          }
          if (event.key === 'Escape' && searchInputRef.current) {
            event.preventDefault()
            searchInputRef.current.focus()
            searchInputRef.current.select()
            return
          }
          if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
          const items = Array.from(projectListRef.current?.querySelectorAll<HTMLButtonElement>('button.project-open-control:not([disabled])') || [])
          if (items.length === 0) return
          event.preventDefault()
          const current = items.indexOf(target as HTMLButtonElement)
          const next = event.key === 'Home' ? 0
            : event.key === 'End' ? items.length - 1
              : event.key === 'ArrowDown' ? (current + 1 + items.length) % items.length
                : (current - 1 + items.length) % items.length
          items[next]?.focus()
          items[next]?.scrollIntoView({ block: 'nearest' })
        }}
      >
        {!collapsed && projectOrderSaveBusy && (
          <div className="sidebar-order-status" role="status">
            <Loader2 size={11} className="spin" /> Saving project order…
          </div>
        )}
        {!collapsed && projectOrderStatus === 'error' && (
          <div className="sidebar-order-error" role="alert">
            <span>Saved project order could not be loaded. Drag sorting is paused.</span>
            <button type="button" onClick={() => void loadProjectOrder()}>Retry</button>
          </div>
        )}
        {(() => {
          const sorted = orderedProjects
          // Filter by name + directory + git branch. Empty query → all.
          const q = searchQuery.trim().toLowerCase()
          const filtered = q === '' ? sorted : sorted.filter((p) => {
            return (
              p.name.toLowerCase().includes(q) ||
              p.directory.toLowerCase().includes(q) ||
              (p.gitBranch || '').toLowerCase().includes(q) ||
              // Iter 690+: include provider/model so users can find e.g.
              // "all kimi projects" or "all glm-5.1 projects" by typing
              // the provider/model substring.
              (p.provider || '').toLowerCase().includes(q) ||
              (p.model || '').toLowerCase().includes(q)
            )
          })
          // No-matches empty state when search has a query but yields nothing.
          if (q !== '' && filtered.length === 0) {
            return (
              <div className="sidebar-search-empty">
                No projects match "{searchQuery}"
              </div>
            )
          }
          return filtered.map((p) => (
          <div
            key={p.id}
            className={`project-item ${activeProjectId === p.id ? 'active' : ''} ${draggingProjectId === p.id ? 'is-dragging' : ''} ${dropTargetId === p.id ? 'drop-target' : ''}`}
            onContextMenu={(e) => {
              e.preventDefault()
              ctxMenuTriggerRef.current = e.currentTarget.querySelector<HTMLButtonElement>('.project-open-control')
              const rect = e.currentTarget.getBoundingClientRect()
              setCtxMenu({ id: p.id, x: e.clientX || rect.left, y: e.clientY || rect.bottom })
            }}
            title={p.directory}
            draggable={editingName !== p.id && q === '' && projectOrderStatus === 'ready' && !projectOrderSaveBusy}
            onDragStart={(e) => {
              // iter 1060+: only allow drag-reorder when search is empty —
              // reordering within a filtered view would persist a partial
              // order that doesn't reflect the real layout the user sees.
              setDraggingProjectId(p.id)
              try {
                e.dataTransfer.setData('text/plain', p.id)
                e.dataTransfer.effectAllowed = 'move'
              } catch { /* Firefox edge cases */ }
            }}
            onDragEnd={() => { setDraggingProjectId(null); setDropTargetId(null) }}
            onDragOver={(e) => {
              if (!draggingProjectId || draggingProjectId === p.id) return
              const source = projects.find((project) => project.id === draggingProjectId)
              if (!source || !!source.pinned !== !!p.pinned) return
              e.preventDefault()
              try { e.dataTransfer.dropEffect = 'move' } catch { /* ignore */ }
              if (dropTargetId !== p.id) setDropTargetId(p.id)
            }}
            onDragLeave={(e) => {
              // Only clear when leaving the project-item itself, not when
              // moving across a child element. Without this guard, hover
              // over the rename input or icon would flicker the indicator.
              if (!(e.currentTarget as HTMLElement).contains(e.relatedTarget as Node | null)) {
                if (dropTargetId === p.id) setDropTargetId(null)
              }
            }}
            onDrop={(e) => {
              e.preventDefault()
              const src = draggingProjectId
              setDraggingProjectId(null)
              setDropTargetId(null)
              if (!src || src === p.id) return
              const source = projects.find((project) => project.id === src)
              if (!source || !!source.pinned !== !!p.pinned) return
              // Compute the new order: insert src BEFORE p in the current
              // sorted list. Persist the full visible list as the new order
              // so the backend filters against live projects on save.
              const sortedIds = sorted.map((x) => x.id)
              const fromIdx = sortedIds.indexOf(src)
              const toIdx = sortedIds.indexOf(p.id)
              if (fromIdx < 0 || toIdx < 0) return
              const next = [...sortedIds]
              next.splice(fromIdx, 1)
              const insertAt = next.indexOf(p.id) // p's index shifts if src was before it
              next.splice(insertAt, 0, src)
              void persistProjectOrder(next, projectOrder)
            }}
          >
            {editingName === p.id ? (
              <div className="project-open-control">
                <div className="project-icon-wrap">
                  <Folder size={16} className="project-folder-icon" />
                  <span className={`project-status-dot ${isProjectActive(p.id) ? 'active' : 'idle'}`} />
                </div>
                <div className="project-info">
                  <input
                    className="rename-input"
                    value={editNameValue}
                    maxLength={60}
                    disabled={renameBusyProjectId === p.id}
                    aria-busy={renameBusyProjectId === p.id}
                    onChange={(e) => { setEditNameValue(e.target.value); setRenameError(null) }}
                    onBlur={() => handleRename(p.id)}
                    onKeyDown={(e) => { if (e.nativeEvent.isComposing || e.keyCode === 229) return; if (e.key === 'Enter') handleRename(p.id); if (e.key === 'Escape') { setEditingName(null); setRenameError(null) } }}
                    onClick={(e) => e.stopPropagation()}
                    autoFocus
                  />
                  <div className="project-meta">
                    {p.gitBranch && (
                      <span className="git-branch">
                        <GitBranch size={9} /> {p.gitBranch}
                      </span>
                    )}
                    <span className="project-path">{shortenPath(p.directory)}</span>
                  </div>
                </div>
              </div>
            ) : (
              <button
                type="button"
                className="project-open-control"
                onClick={() => { setActiveProject(p.id); onNavigate?.() }}
                aria-current={activeProjectId === p.id ? 'page' : undefined}
                aria-haspopup="menu"
                aria-expanded={ctxMenu?.id === p.id}
                aria-keyshortcuts="Shift+F10"
                aria-label={`${p.name}, ${p.provider || 'glm'} ${p.model || 'glm-5.2'}`}
                data-project-id={p.id}
              >
                <span className="project-icon-wrap">
                  <Folder size={16} className="project-folder-icon" />
                  <span className={`project-status-dot ${isProjectActive(p.id) ? 'active' : 'idle'}`} />
                </span>
                <span className="project-info">
                  <span
                    className="project-name"
                    onDoubleClick={(e) => { e.stopPropagation(); setEditingName(p.id); setEditNameValue(p.name) }}
                    title={
                      [
                        p.pinned ? 'Pinned to top' : '',
                        isProjectMuted(p.id) ? 'Notifications muted' : '',
                        'Double-click to rename',
                        'Right-click or Shift+F10 for menu',
                      ].filter(Boolean).join(' · ')
                    }
                  >
                    {p.pinned && <Pin size={10} className="project-pin-icon" />}
                    {isProjectMuted(p.id) && <VolumeX size={10} className="project-mute-icon" />}
                    {p.name}
                  </span>
                  <span className="project-meta">
                    {p.gitBranch && (
                      <span className="git-branch">
                        <GitBranch size={9} /> {p.gitBranch}
                      </span>
                    )}
                    <span className="project-path">{shortenPath(p.directory)}</span>
                  </span>
                </span>
              </button>
            )}
            {activeProjectId === p.id && (
              <div className="project-provider-row">
                <ProviderSelect
                  projectId={p.id}
                  currentProvider={p.provider || 'glm'}
                  currentModel={p.model || 'glm-5.2'}
                  currentTemperature={p.temperature}
                  currentMaxTokens={p.maxTokens}
                  currentThinkingMode={p.thinkingMode}
                  currentThinkingBudget={p.thinkingBudget}
                />
              </div>
            )}
            {renameError && editingName === p.id && <div className="sidebar-inline-error">{renameError}</div>}
            {removeError && removeErrorProjectId === p.id && (
              <div className="sidebar-inline-error">{removeError}</div>
            )}
            <button
              className="icon-btn remove-btn"
              disabled={removeBusyProjectIds.has(p.id)}
              onClick={(e) => { e.stopPropagation(); void requestProjectRemoval(p) }}
              title="Delete local project data…"
              aria-label={`Delete local data for ${p.name}`}
            >
              {removeBusyProjectIds.has(p.id) ? <Loader2 size={12} className="spin" /> : <Trash2 size={12} />}
            </button>
          </div>
        ))
        })()}
        {projects.length === 0 && !showAdd && (
          <div className="empty-state">
            <FolderPlus size={28} style={{ opacity: 0.3 }} />
            <p>No projects yet</p>
            <button className="btn-primary" onClick={() => setShowAdd(true)}>Add Project</button>
          </div>
        )}
      </div>
      {!collapsed && sidebarNotice && (
        <div
          className={`sidebar-notice ${sidebarNotice.kind}`}
          role={sidebarNotice.kind === 'error' ? 'alert' : 'status'}
        >
          <span>{sidebarNotice.text}</span>
          <button
            onClick={() => {
              if (noticeTimerRef.current !== null) window.clearTimeout(noticeTimerRef.current)
              noticeTimerRef.current = null
              setSidebarNotice(null)
            }}
            aria-label="Dismiss sidebar notification"
          >
            <X size={11} />
          </button>
        </div>
      )}
      {!collapsed && (
        <div className="sidebar-footer">
          <span className="sidebar-footer-version">Gokin Studio v1.0</span>
          <span className="sidebar-footer-count" role="status" aria-live="polite">
            {(() => {
              const q = searchQuery.trim().toLowerCase()
              if (q === '') return `${projects.length} project${projects.length !== 1 ? 's' : ''}`
              const matched = projects.filter((p) =>
                p.name.toLowerCase().includes(q) ||
                p.directory.toLowerCase().includes(q) ||
                (p.gitBranch || '').toLowerCase().includes(q) ||
                (p.provider || '').toLowerCase().includes(q) ||
                (p.model || '').toLowerCase().includes(q)
              ).length
              return `${matched} of ${projects.length}`
            })()}
          </span>
        </div>
      )}

      {ctxMenu && (() => {
        const MENU_W = 190, MENU_H = 340
        const cx = Math.max(8, Math.min(ctxMenu.x, window.innerWidth - MENU_W - 8))
        const cy = Math.max(8, Math.min(ctxMenu.y, window.innerHeight - MENU_H - 8))
        const ctxProject = projects.find((pr) => pr.id === ctxMenu.id)
        const isPinned = !!ctxProject?.pinned
        const pinGroup = orderedProjects.filter((project) => !!project.pinned === isPinned)
        const orderIndex = pinGroup.findIndex((project) => project.id === ctxMenu.id)
        const canReorder = searchQuery.trim() === '' && projectOrderStatus === 'ready' && !projectOrderSaveBusy
        return (
        <>
          <div className="ctx-backdrop" onClick={() => setCtxMenu(null)} />
          <div
            ref={ctxMenuRef}
            className="ctx-menu"
            style={{ top: cy, left: cx }}
            role="menu"
            aria-label={`Actions for ${ctxProject?.name || 'project'}`}
            onKeyDown={(event) => {
              if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
              const items = Array.from(ctxMenuRef.current?.querySelectorAll<HTMLButtonElement>('button:not([disabled])') || [])
              if (items.length === 0) return
              event.preventDefault()
              const current = items.indexOf(document.activeElement as HTMLButtonElement)
              const next = event.key === 'Home' ? 0
                : event.key === 'End' ? items.length - 1
                  : event.key === 'ArrowDown' ? (current + 1 + items.length) % items.length
                    : (current - 1 + items.length) % items.length
              items[next]?.focus()
            }}
          >
            <button role="menuitem" onClick={() => {
              setActiveProject(ctxMenu.id)
              window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: 'chat' }))
              onNavigate?.()
              setCtxMenu(null)
            }}>Open Chat</button>
            <button role="menuitem" onClick={() => {
              setActiveProject(ctxMenu.id)
              window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: 'files' }))
              onNavigate?.()
              setCtxMenu(null)
            }}>Browse Files</button>
            <button role="menuitem" disabled={renameBusyProjectId === ctxMenu.id} onClick={() => {
              const proj = projects.find((pr) => pr.id === ctxMenu.id)
              setEditingName(ctxMenu.id)
              setEditNameValue(proj?.name || '')
              setCtxMenu(null)
            }}>Rename</button>
            <div className="ctx-divider" role="separator" />
            <button role="menuitem" disabled={pinBusyProjectId === ctxMenu.id} onClick={async () => {
              const targetId = ctxMenu.id
              const next = !isPinned
              setCtxMenu(null)
              // Optimistic local update so the sort runs immediately; if the
              // backend rejects, revert and explain the failure in the sidebar.
              setPinBusyProjectId(targetId)
              updateProject(targetId, { pinned: next })
              try {
                await SetProjectPinned(targetId, next)
                showSidebarNotice('success', next ? 'Project pinned to the top.' : 'Project unpinned.')
              } catch (e: any) {
                console.error('SetProjectPinned error:', e)
                updateProject(targetId, { pinned: isPinned })
                showSidebarNotice('error', `Could not ${next ? 'pin' : 'unpin'} project: ${String(e?.message || e || 'unknown error')}`)
              } finally {
                setPinBusyProjectId((current) => current === targetId ? null : current)
              }
            }}>
              {isPinned ? <><PinOff size={12} /> Unpin from top</> : <><Pin size={12} /> Pin to top</>}
            </button>
            <div className="ctx-divider" role="separator" />
            <button
              role="menuitem"
              disabled={!canReorder || orderIndex <= 0}
              onClick={() => moveProjectInOrder(ctxMenu.id, -1)}
            >Move up</button>
            <button
              role="menuitem"
              disabled={!canReorder || orderIndex < 0 || orderIndex >= pinGroup.length - 1}
              onClick={() => moveProjectInOrder(ctxMenu.id, 1)}
            >Move down</button>
            {(() => {
              const muted = isProjectMuted(ctxMenu.id)
              return (
                <button role="menuitem" onClick={() => {
                  toggleProjectMute(ctxMenu.id)
                  setCtxMenu(null)
                }}>
                  {muted ? <><Volume2 size={12} /> Unmute notifications</> : <><VolumeX size={12} /> Mute notifications</>}
                </button>
              )
            })()}
            <div className="ctx-divider" role="separator" />
            <button role="menuitem" onClick={async () => {
              const targetId = ctxMenu.id
              const proj = projects.find((pr) => pr.id === targetId)
              setCtxMenu(null)
              if (!proj) return
              try {
                const json = await ExportProjectJSON(targetId)
                const blob = new Blob([json], { type: 'application/json' })
                const url = URL.createObjectURL(blob)
                const a = document.createElement('a')
                a.href = url
                a.download = `${proj.name.replace(/[^a-zA-Z0-9_\-]/g, '_')}-project.json`
                a.click()
                URL.revokeObjectURL(url)
                showSidebarNotice('success', `Exported “${proj.name}” as JSON.`)
              } catch (e: any) {
                console.error('ExportProjectJSON failed:', e)
                showSidebarNotice('error', `Could not export “${proj.name}”: ${String(e?.message || e || 'unknown error')}`)
              }
            }}>
              <Download size={12} /> Export project as JSON
            </button>
            <div className="ctx-divider" role="separator" />
            <button role="menuitem" disabled={archiveBusy !== null} onClick={() => void handleArchive(ctxMenu.id)}>
              {archiveBusy === `archive:${ctxMenu.id}` ? <Loader2 size={12} className="spin" /> : <Archive size={12} />}
              Archive project
            </button>
            <button role="menuitem" className="ctx-danger" disabled={removeBusyProjectIds.has(ctxMenu.id)} onClick={() => {
              const project = projects.find((item) => item.id === ctxMenu.id)
              setCtxMenu(null)
              if (project) void requestProjectRemoval(project)
            }}><Trash2 size={12} /> Delete local data…</button>
          </div>
        </>
        )
      })()}
      {confirmationDialog}
    </div>
  )
}
