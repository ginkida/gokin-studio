import { useState, useEffect, useRef } from 'react'
import { useProjectStore, ProjectInfo } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { ProviderSelect } from '../project/ProviderSelect'
import { Zap, FolderPlus, Trash2, GitBranch, Settings, FolderOpen, Folder, PanelLeftClose, PanelLeftOpen, Search, X, Pin, PinOff, Download, Upload, Volume2, VolumeX } from 'lucide-react'
import { AddProject, RemoveProject, BrowseDirectory, RenameProject, SetProjectPinned, ExportProjectJSON, ImportProjectJSON } from '../../../wailsjs/go/studio/Studio'
import { isProjectMuted, toggleProjectMute, unmuteProject, clearProjectLocalStorage } from '../../lib/mutedProjects'

interface SidebarProps {
  onOpenSettings?: () => void
  onToggleCollapse?: () => void
  collapsed?: boolean
}

export function Sidebar({ onOpenSettings, onToggleCollapse, collapsed }: SidebarProps) {
  const projects = useProjectStore((s) => s.projects)
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const setActiveProject = useProjectStore((s) => s.setActiveProject)
  const addProjectToStore = useProjectStore((s) => s.addProject)
  const removeProjectFromStore = useProjectStore((s) => s.removeProject)

  const [showAdd, setShowAdd] = useState(false)
  const [newName, setNewName] = useState('')
  const [newDir, setNewDir] = useState('')
  const [error, setError] = useState('')
  const [renameError, setRenameError] = useState<string | null>(null)
  const [removeError, setRemoveError] = useState<string | null>(null)
  const [confirmDelete, setConfirmDelete] = useState<string | null>(null)
  const [editingName, setEditingName] = useState<string | null>(null)
  const [editNameValue, setEditNameValue] = useState('')
  const [ctxMenu, setCtxMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  // Project import (iter 560+) — paste-textarea modal triggered from the
  // sidebar header. Same paste-flow pattern as session import (iter 550+)
  // because Wails desktop apps can't reliably pop a "browse for file"
  // dialog without OS plumbing.
  const [showImportProject, setShowImportProject] = useState(false)
  const [importJSON, setImportJSON] = useState('')
  const [importDir, setImportDir] = useState('')
  const [importError, setImportError] = useState<string | null>(null)
  const [importBusy, setImportBusy] = useState(false)
  // Sidebar project search. Only shown when there are 4+ projects so single-
  // project / few-project users don't see a redundant search input. Filters
  // by name, directory, and git branch (case-insensitive substring).
  const [searchQuery, setSearchQuery] = useState('')
  const searchInputRef = useRef<HTMLInputElement>(null)
  const updateProject = useProjectStore((s) => s.updateProject)

  // Ctrl+Shift+P / Cmd+Shift+P focuses the sidebar search. Distinct from
  // Ctrl+P (file picker) and Ctrl+K (command palette) — this one targets the
  // visual project list specifically. No-op when the search input isn't
  // rendered (fewer than 4 projects).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      const isSearchHotkey = (e.ctrlKey || e.metaKey) && e.shiftKey && (e.key === 'P' || e.key === 'p')
      if (!isSearchHotkey) return
      const el = searchInputRef.current
      if (!el) return // search not rendered (too few projects)
      e.preventDefault()
      el.focus()
      el.select()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

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

  // Per-project mute (iter 580+) — increment a tick whenever the muted set
  // changes so the project list re-renders with updated VolumeX icons.
  const [muteTick, setMuteTick] = useState(0)
  useEffect(() => {
    const refresh = () => setMuteTick((t) => t + 1)
    window.addEventListener('gokin:muted-changed', refresh)
    return () => window.removeEventListener('gokin:muted-changed', refresh)
  }, [])
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
    setRenameError(null)
    try {
      await RenameProject(id, trimmed)
      updateProject(id, { name: trimmed })
      setEditingName(null)
    } catch (e: any) {
      setRenameError(String(e?.message || e || 'Rename failed'))
      // Keep the rename input open so the user can correct the name.
    }
  }

  const handleAdd = async () => {
    if (!newName.trim() || !newDir.trim()) return
    setError('')
    try {
      const p = await AddProject(newName.trim(), newDir.trim()) as ProjectInfo
      addProjectToStore(p)
      setActiveProject(p.id)
      setShowAdd(false)
      setNewName('')
      setNewDir('')
    } catch (e: any) {
      setError(String(e))
    }
  }

  const handleRemove = async (id: string) => {
    setRemoveError(null)
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
      setConfirmDelete(null)
    } catch (err: any) {
      setRemoveError(String(err?.message || err || 'Delete failed'))
      setTimeout(() => setRemoveError(null), 4000)
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
    <div className={`sidebar ${collapsed ? 'collapsed' : ''}`}>
      <div className="sidebar-header">
        {!collapsed && (
          <div className="sidebar-brand">
            <Zap size={18} className="sidebar-brand-icon" />
            <span>Gokin Studio</span>
          </div>
        )}
        <div className="sidebar-header-actions">
          {onToggleCollapse && (
            <button className="icon-btn" onClick={onToggleCollapse} title={collapsed ? 'Expand sidebar (Ctrl+B)' : 'Collapse sidebar (Ctrl+B)'}>
              {collapsed ? <PanelLeftOpen size={15} /> : <PanelLeftClose size={15} />}
            </button>
          )}
          {onOpenSettings && (
            <button className="icon-btn" onClick={onOpenSettings} title="Settings (Ctrl+3)">
              <Settings size={15} />
            </button>
          )}
          <button
            className="icon-btn"
            onClick={() => {
              setImportJSON('')
              setImportDir('')
              setImportError(null)
              setShowImportProject(true)
            }}
            title="Import project from JSON"
          >
            <Upload size={15} />
          </button>
          <button className="icon-btn" onClick={() => setShowAdd(!showAdd)} title="Add project">
            <FolderPlus size={15} />
          </button>
        </div>
      </div>

      {showImportProject && (
        <>
          <div className="import-backdrop" onClick={() => { if (!importBusy) { setShowImportProject(false); setImportError(null) } }} />
          <div className="import-modal">
            <div className="import-header">
              <h3>Import project from JSON</h3>
              <button
                className="icon-btn"
                onClick={() => { if (!importBusy) { setShowImportProject(false); setImportError(null) } }}
                title="Close (Esc)"
              >
                <X size={14} />
              </button>
            </div>
            <p className="import-hint">
              Paste a project JSON blob produced by "Export project as JSON" and pick a target directory. The imported project lands as a fresh entry with "(imported)" suffix; existing files in the target dir are not modified.
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
                  if (e.key === 'Escape' && !importBusy) { e.preventDefault(); setShowImportProject(false); setImportError(null) }
                }}
              />
              <button
                className="icon-btn"
                onClick={async () => {
                  try {
                    const dir = await BrowseDirectory()
                    if (dir) setImportDir(dir)
                  } catch (e: any) {
                    setImportError(`Could not open directory picker: ${String(e?.message || e || 'unknown error')}`)
                  }
                }}
                title="Browse..."
              >
                <FolderOpen size={14} />
              </button>
            </div>
            {importError && <div className="import-error">{importError}</div>}
            <div className="import-actions">
              <span className="import-action-hint">Import preserves system prompt + provider/model + budget.</span>
              <button className="btn-secondary" onClick={() => { setShowImportProject(false); setImportError(null) }} disabled={importBusy}>
                Cancel
              </button>
              <button
                className="btn-primary"
                disabled={importBusy || !importJSON.trim() || !importDir.trim()}
                onClick={async () => {
                  setImportBusy(true)
                  setImportError(null)
                  try {
                    const info = await ImportProjectJSON(importJSON, importDir) as ProjectInfo
                    if (info?.id) {
                      addProjectToStore(info)
                      setActiveProject(info.id)
                    }
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

      {showAdd && (
        <div className="add-project-form">
          <input
            placeholder="Project name"
            value={newName}
            maxLength={60}
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) return
              if (e.key === 'Escape') { setShowAdd(false); setError('') }
              else if (e.key === 'Enter' && newDir.trim()) handleAdd()
            }}
            autoFocus
          />
          <div className="dir-input-row">
            <input
              placeholder="Select directory..."
              value={newDir}
              onChange={(e) => setNewDir(e.target.value)}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing || e.keyCode === 229) return
                if (e.key === 'Escape') { setShowAdd(false); setError('') }
                else if (e.key === 'Enter') handleAdd()
              }}
            />
            <button
              className="btn-secondary browse-btn"
              onClick={async () => {
                try {
                  const dir = await BrowseDirectory()
                  if (dir) {
                    setNewDir(dir)
                    if (!newName.trim()) {
                      const parts = dir.replace(/\/$/, '').split('/')
                      setNewName(parts[parts.length - 1] || '')
                    }
                  }
                } catch (e: any) {
                  console.error('BrowseDirectory error:', e)
                  setError(`Could not open directory picker: ${String(e?.message || e || 'unknown error')}`)
                }
              }}
              title="Browse..."
            >
              <FolderOpen size={14} />
            </button>
          </div>
          {error && <div className="form-error">{error}</div>}
          <div className="add-project-actions">
            <button className="btn-secondary" onClick={() => { setShowAdd(false); setError('') }}>Cancel</button>
            <button className="btn-primary" onClick={handleAdd} disabled={!newName.trim() || !newDir.trim()}>Add</button>
          </div>
        </div>
      )}

      {/* Sidebar search: hidden when there are <4 projects (clutter for solo
          users) or when sidebar is collapsed. */}
      {!collapsed && projects.length >= 4 && (
        <div className="sidebar-search">
          <Search size={12} className="sidebar-search-icon" />
          <input
            ref={searchInputRef}
            type="text"
            className="sidebar-search-input"
            value={searchQuery}
            placeholder="Search projects… (Ctrl+Shift+P)"
            maxLength={100}
            onChange={(e) => setSearchQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.nativeEvent.isComposing || e.keyCode === 229) return
              if (e.key === 'Escape') {
                e.preventDefault()
                setSearchQuery('')
                searchInputRef.current?.blur()
              }
            }}
          />
          {searchQuery && (
            <button
              className="sidebar-search-clear"
              onClick={() => { setSearchQuery(''); searchInputRef.current?.focus() }}
              title="Clear search (Esc)"
            >
              <X size={11} />
            </button>
          )}
        </div>
      )}

      <div className="project-list">
        {(() => {
          const sorted = [...projects].sort((a, b) => {
            // Pinned projects always sort above unpinned. Within each pin
            // group, recent-first ordering applies (lastUsedAt desc, alpha
            // tiebreaker for never-used projects).
            const ap = a.pinned ? 1 : 0
            const bp = b.pinned ? 1 : 0
            if (ap !== bp) return bp - ap
            const ai = a.lastUsedAt || 0
            const bi = b.lastUsedAt || 0
            if (ai !== bi) {
              if (ai === 0) return 1
              if (bi === 0) return -1
              return bi - ai
            }
            return a.name.localeCompare(b.name)
          })
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
            className={`project-item ${activeProjectId === p.id ? 'active' : ''}`}
            onClick={() => setActiveProject(p.id)}
            onContextMenu={(e) => {
              e.preventDefault()
              setCtxMenu({ id: p.id, x: e.clientX, y: e.clientY })
            }}
            title={p.directory}
          >
            <div className="project-icon-wrap">
              <Folder size={16} className="project-folder-icon" />
              <span className={`project-status-dot ${isProjectActive(p.id) ? 'active' : 'idle'}`} />
            </div>
            <div className="project-info">
              {editingName === p.id ? (
                <>
                  <input
                    className="rename-input"
                    value={editNameValue}
                    maxLength={60}
                    onChange={(e) => { setEditNameValue(e.target.value); setRenameError(null) }}
                    onBlur={() => handleRename(p.id)}
                    onKeyDown={(e) => { if (e.nativeEvent.isComposing || e.keyCode === 229) return; if (e.key === 'Enter') handleRename(p.id); if (e.key === 'Escape') { setEditingName(null); setRenameError(null) } }}
                    onClick={(e) => e.stopPropagation()}
                    autoFocus
                  />
                  {renameError && <div className="sidebar-inline-error">{renameError}</div>}
                </>
              ) : (
                <div
                  className="project-name"
                  onDoubleClick={(e) => { e.stopPropagation(); setEditingName(p.id); setEditNameValue(p.name) }}
                  title={
                    [
                      p.pinned ? 'Pinned to top' : '',
                      isProjectMuted(p.id) ? 'Notifications muted' : '',
                      'Double-click to rename',
                    ].filter(Boolean).join(' · ')
                  }
                >
                  {p.pinned && <Pin size={10} className="project-pin-icon" />}
                  {isProjectMuted(p.id) && <VolumeX size={10} className="project-mute-icon" />}
                  {p.name}
                </div>
              )}
              <div className="project-meta">
                {p.gitBranch && (
                  <span className="git-branch">
                    <GitBranch size={9} /> {p.gitBranch}
                  </span>
                )}
                <span className="project-path">{shortenPath(p.directory)}</span>
              </div>
              <div className="project-provider-row">
                <ProviderSelect
                  projectId={p.id}
                  currentProvider={p.provider || 'glm'}
                  currentModel={p.model || 'glm-5.1'}
                  currentTemperature={p.temperature}
                  currentMaxTokens={p.maxTokens}
                  currentThinkingMode={p.thinkingMode}
                  currentThinkingBudget={p.thinkingBudget}
                />
              </div>
              {removeError && confirmDelete === p.id && (
                <div className="sidebar-inline-error">{removeError}</div>
              )}
            </div>
            {confirmDelete === p.id ? (
              <div className="confirm-delete" onClick={(e) => e.stopPropagation()}>
                <button className="btn-danger-sm" onClick={() => handleRemove(p.id)}>Delete</button>
                <button className="btn-cancel-sm" onClick={() => { setConfirmDelete(null); setRemoveError(null) }}>No</button>
              </div>
            ) : (
              <button
                className="icon-btn remove-btn"
                onClick={(e) => { e.stopPropagation(); setConfirmDelete(p.id) }}
                title="Remove project"
              >
                <Trash2 size={12} />
              </button>
            )}
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
      {!collapsed && (
        <div className="sidebar-footer">
          <span className="sidebar-footer-version">Gokin Studio v1.0</span>
          <span className="sidebar-footer-count">
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
        const MENU_W = 180, MENU_H = 180
        const cx = Math.min(ctxMenu.x, window.innerWidth - MENU_W - 8)
        const cy = Math.min(ctxMenu.y, window.innerHeight - MENU_H - 8)
        const ctxProject = projects.find((pr) => pr.id === ctxMenu.id)
        const isPinned = !!ctxProject?.pinned
        return (
        <>
          <div className="ctx-backdrop" onClick={() => setCtxMenu(null)} />
          <div className="ctx-menu" style={{ top: cy, left: cx }}>
            <button onClick={() => {
              setActiveProject(ctxMenu.id)
              window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: 'chat' }))
              setCtxMenu(null)
            }}>Open Chat</button>
            <button onClick={() => {
              setActiveProject(ctxMenu.id)
              window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: 'files' }))
              setCtxMenu(null)
            }}>Browse Files</button>
            <button onClick={() => {
              const proj = projects.find((pr) => pr.id === ctxMenu.id)
              setEditingName(ctxMenu.id)
              setEditNameValue(proj?.name || '')
              setCtxMenu(null)
            }}>Rename</button>
            <div className="ctx-divider" />
            <button onClick={async () => {
              const targetId = ctxMenu.id
              const next = !isPinned
              setCtxMenu(null)
              // Optimistic local update so the sort runs immediately; if the
              // backend rejects we revert. Errors are silent (logged) since a
              // failure here is non-blocking — the state simply doesn't change.
              updateProject(targetId, { pinned: next })
              try {
                await SetProjectPinned(targetId, next)
              } catch (e) {
                console.error('SetProjectPinned error:', e)
                updateProject(targetId, { pinned: isPinned })
              }
            }}>
              {isPinned ? <><PinOff size={12} /> Unpin from top</> : <><Pin size={12} /> Pin to top</>}
            </button>
            {(() => {
              const muted = isProjectMuted(ctxMenu.id)
              return (
                <button onClick={() => {
                  toggleProjectMute(ctxMenu.id)
                  setCtxMenu(null)
                }}>
                  {muted ? <><Volume2 size={12} /> Unmute notifications</> : <><VolumeX size={12} /> Mute notifications</>}
                </button>
              )
            })()}
            <div className="ctx-divider" />
            <button onClick={async () => {
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
              } catch (e: any) {
                console.error('ExportProjectJSON failed:', e)
              }
            }}>
              <Download size={12} /> Export project as JSON
            </button>
            <div className="ctx-divider" />
            <button className="ctx-danger" onClick={() => {
              setConfirmDelete(ctxMenu.id)
              setCtxMenu(null)
            }}>Delete</button>
          </div>
        </>
        )
      })()}
    </div>
  )
}
