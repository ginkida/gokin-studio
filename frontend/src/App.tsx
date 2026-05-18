import { useCallback, useEffect, useRef, useState } from 'react'
import { Sidebar } from './components/layout/Sidebar'
import { ChatPanel } from './components/chat/ChatPanel'
import { StatusBar } from './components/layout/StatusBar'
import { ToastStack } from './components/layout/ToastStack'
import { ErrorBoundary, installGlobalErrorHandlers } from './components/layout/ErrorBoundary'
import { OnboardingWizard, shouldShowOnboarding } from './components/onboarding/OnboardingWizard'
import { SettingsPage } from './components/settings/SettingsPage'
import { FileBrowser } from './components/files/FileBrowser'
import { CommandPalette } from './components/palette/CommandPalette'
import { useWailsEvents } from './hooks/useWailsEvents'
import { useProjectStore, ProjectInfo } from './stores/projectStore'
import { useSettingsStore } from './stores/settingsStore'
import { useChatStore } from './stores/chatStore'
import { ListProjects, GetSettings, GetProviders, CreateChatSession, ListChatSessions, DeleteChatSession, RenameChatSession, SetSessionPinned, ReorderChatSessions } from '../wailsjs/go/studio/Studio'
import { MessageSquare, Settings, FolderTree, Plus, X, GitFork, Pin, PinOff } from 'lucide-react'
import './App.css'

// Install once at module load — before any component mounts — so even
// errors during initial bootstrap land in the event log.
installGlobalErrorHandlers()

interface SessionTab {
  id: string
  name: string
  // Lineage indicator: when this session was forked, parentName names the
  // session it came from (or "(deleted)" if that source has been removed).
  // Empty for top-level / non-forked sessions. Used to render the "↳ name"
  // chip in the tab list so users can trace fork lineage at a glance.
  parentName?: string
  // Pinned anchors this tab to the top of the tab list (iter 480+).
  pinned?: boolean
}

function AppContent() {
  const setProjects = useProjectStore((s) => s.setProjects)
  const setSettings = useSettingsStore((s) => s.setSettings)
  const setProviders = useSettingsStore((s) => s.setProviders)
  const theme = useSettingsStore((s) => s.settings.theme)

  // Onboarding wizard (iter 730+). Shown once on a fresh install — when no
  // projects are configured AND the user hasn't explicitly skipped before.
  // Becomes false on any of: project added, "Start chatting" clicked, Skip clicked.
  const [showOnboarding, setShowOnboarding] = useState(false)

  // view: session ID for chat tabs, or 'files'/'settings'
  const [view, setView] = useState<string>('default')
  const [ready, setReady] = useState(false)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false)
  const [sessions, setSessions] = useState<SessionTab[]>([{ id: 'default', name: 'Chat 1' }])
  const [showPalette, setShowPalette] = useState(false)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [renameError, setRenameError] = useState<string | null>(null)
  const [closeError, setCloseError] = useState<string | null>(null)
  const [newChatError, setNewChatError] = useState<string | null>(null)
  const creatingChatRef = useRef(false)
  // Tab right-click context menu (iter 480+). Holds the target session id +
  // viewport coords. Closes on Escape, click outside, or any tab action.
  const [tabCtxMenu, setTabCtxMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  // Drag-to-reorder state (iter 540+). draggingId tracks which tab the user
  // is currently dragging; dropTargetId is the tab the cursor is hovering
  // over so we can render a drop indicator.
  const [draggingTabId, setDraggingTabId] = useState<string | null>(null)
  const [dropTargetId, setDropTargetId] = useState<string | null>(null)
  // Iter 650+: a separate "drop at end" zone after the new-chat (+) button
  // because dropping on tab N puts the dragged BEFORE N — there was no way
  // to drop at the very end of the list. true while user drags over the
  // dedicated end-zone.
  const [dropAtEnd, setDropAtEnd] = useState(false)

  useWailsEvents()

  // Apply theme
  useEffect(() => {
    const t = theme || 'dark'
    document.documentElement.setAttribute('data-theme', t)
    document.documentElement.style.colorScheme = t
  }, [theme])

  // Keyboard shortcuts
  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    // Don't intercept anything while an IME (Russian/CJK/etc.) is composing —
    // preventDefault during composition can eat characters the user is typing.
    if (e.isComposing || e.keyCode === 229) return

    const target = e.target as HTMLElement
    const isInput = target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT'

    if (e.ctrlKey || e.metaKey) {
      if (e.key === 'k') {
        // Ctrl+K works even when input is focused — palette is the point.
        e.preventDefault()
        setShowPalette((s) => !s)
      }
      if (e.key === 'l' && !isInput) {
        e.preventDefault()
        window.dispatchEvent(new CustomEvent('gokin:clear-chat'))
      }
      if (e.key === 't' && !isInput) {
        e.preventDefault()
        handleNewChat()
      }
      // Ctrl+1 → chat (restore the last session the user was on, or first in list)
      if (e.key === '1' && !isInput) {
        e.preventDefault()
        const pid = useProjectStore.getState().activeProjectId
        const lastSession = pid ? useChatStore.getState().activeSession[pid] : null
        setSessions((list) => {
          const exists = lastSession && list.some((t) => t.id === lastSession)
          setView(exists ? lastSession! : (list[0]?.id || 'default'))
          return list
        })
      }
      if (e.key === '2' && !isInput) { e.preventDefault(); setView('files') }
      if (e.key === '3' && !isInput) { e.preventDefault(); setView('settings') }
      // Ctrl+B toggles the project sidebar (VS Code convention).
      if (e.key === 'b' && !isInput) { e.preventDefault(); setSidebarCollapsed((c) => !c) }
      // Ctrl+PageUp/PageDown cycles through open chat sessions (wraps around).
      if (e.key === 'PageUp' || e.key === 'PageDown') {
        e.preventDefault()
        window.dispatchEvent(new CustomEvent('gokin:cycle-session', { detail: { direction: e.key === 'PageUp' ? -1 : 1 } }))
      }
    }
    // Alt+1..9 (iter 660+): jump directly to session N in the current tab
    // order. Fast navigation when a user has many tabs open and remembers
    // the position of their main one. No-op outside chat (no sessions list)
    // and when the input is focused (Alt+number can be a system shortcut).
    if (e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && !isInput) {
      const n = parseInt(e.key, 10)
      if (n >= 1 && n <= 9) {
        e.preventDefault()
        setSessions((list) => {
          const target = list[n - 1]
          if (target) setView(target.id)
          return list
        })
      }
    }
  }, [setView])

  useEffect(() => {
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [handleKeyDown])

  // Tab switch events
  useEffect(() => {
    const switchHandler = (e: Event) => {
      const tab = (e as CustomEvent).detail
      if (!tab) return
      if (tab === 'chat') {
        // Restore last-used session, validating it still exists in the tab list.
        const pid = useProjectStore.getState().activeProjectId
        const lastSession = pid ? useChatStore.getState().activeSession[pid] : null
        setSessions((list) => {
          const exists = lastSession && list.some((t) => t.id === lastSession)
          setView(exists ? lastSession! : (list[0]?.id || 'default'))
          return list
        })
      } else {
        setView(tab)
      }
    }
    const settingsHandler = () => setView('settings')
    const renamedHandler = (e: Event) => {
      const { sessionID, name } = (e as CustomEvent).detail || {}
      if (!sessionID || !name) return
      setSessions((s) => s.map((t) => (t.id === sessionID ? { ...t, name } : t)))
    }
    const cycleHandler = (e: Event) => {
      const direction: number = (e as CustomEvent).detail?.direction || 1
      // Use state updaters so we read the latest list/view without stale closure.
      setSessions((list) => {
        if (list.length === 0) return list
        setView((current) => {
          const idx = list.findIndex((t) => t.id === current)
          if (idx < 0) {
            // Not currently on a chat tab (e.g. Files/Settings) — jump into
            // chat at the first session instead of silently doing nothing.
            return list[0].id
          }
          const next = (idx + direction + list.length) % list.length
          return list[next].id
        })
        return list
      })
    }
    // Reload session list from backend; called after fork/rename/etc.
    // when a new session has appeared and we need it to show in the tab bar
    // without waiting for the next project switch or full reload.
    const sessionsChangedHandler = () => {
      const pid = useProjectStore.getState().activeProjectId
      if (!pid) return
      ListChatSessions(pid).then((list) => {
        if (list && list.length > 0) {
          setSessions(list.map((s: any) => ({ id: s.id, name: s.name, parentName: s.parentName, pinned: !!s.pinned })))
        }
      }).catch(() => {})
    }
    window.addEventListener('gokin:open-settings', settingsHandler)
    window.addEventListener('gokin:switch-tab', switchHandler)
    window.addEventListener('gokin:session-renamed', renamedHandler)
    window.addEventListener('gokin:cycle-session', cycleHandler)
    window.addEventListener('gokin:sessions-changed', sessionsChangedHandler)
    return () => {
      window.removeEventListener('gokin:open-settings', settingsHandler)
      window.removeEventListener('gokin:switch-tab', switchHandler)
      window.removeEventListener('gokin:session-renamed', renamedHandler)
      window.removeEventListener('gokin:cycle-session', cycleHandler)
      window.removeEventListener('gokin:sessions-changed', sessionsChangedHandler)
    }
  }, [])

  // Load data
  useEffect(() => {
    Promise.all([
      ListProjects().then((projects) => {
        if (projects) {
          const typed = projects as ProjectInfo[]
          setProjects(typed)
          if (typed.length > 0 && !useProjectStore.getState().activeProjectId) {
            useProjectStore.getState().setActiveProject(typed[0].id)
          }
        }
      }).catch(() => {}),
      GetSettings().then((config) => {
        if (config?.settings) {
          const s = config.settings
          setSettings({
            theme: s.theme || 'dark',
            defaultProvider: s.defaultProvider || 'glm',
            defaultModel: s.defaultModel || 'glm-5.1',
            glmKey: s.glmKey || '',
            minimaxKey: s.minimaxKey || '',
            kimiKey: s.kimiKey || '',
            deepseekKey: s.deepseekKey || '',
            ollamaUrl: s.ollamaUrl || 'http://localhost:11434',
            defaultThinkingMode: s.defaultThinkingMode || '',
            defaultThinkingBudget: s.defaultThinkingBudget || 0,
            defaultBudgetUSD: s.defaultBudgetUSD || 0,
            autoCleanupDisabled: !!s.autoCleanupDisabled,
            autoBackupEnabled: !!s.autoBackupEnabled,
          })
        }
      }).catch(() => {}),
      GetProviders().then((providers) => {
        if (providers) setProviders(providers.map((p) => ({ id: p.id, name: p.name, models: p.models || [] })))
      }).catch(() => {}),
    ]).finally(() => {
      setReady(true)
      // Decide whether to show the onboarding wizard after the data loads.
      // We have to wait for ready because shouldShowOnboarding reads project
      // count from the freshly-populated store.
      if (shouldShowOnboarding(useProjectStore.getState().projects.length)) {
        setShowOnboarding(true)
      }
    })
    // Allow the user to re-trigger the wizard from the command palette.
    const reshowHandler = () => setShowOnboarding(true)
    window.addEventListener('gokin:show-onboarding', reshowHandler)
    return () => window.removeEventListener('gokin:show-onboarding', reshowHandler)
  }, [setProjects, setSettings, setProviders])

  // Hooks before early return
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const sessionActive = useChatStore((s) => s.sessionActive)
  const unread = useChatStore((s) => s.unread)
  const clearUnread = useChatStore((s) => s.clearUnread)

  // Clear the unread badge whenever the user lands on a chat tab. Keyed on
  // (activeProjectId, view) so switching projects, switching tabs, OR newly
  // mounting all clear the corresponding badge. We use the chatStore's
  // setActiveSession to also keep the per-project active session in sync,
  // which ChatPanel and FileBrowser both read.
  useEffect(() => {
    if (!activeProjectId) return
    // 'files' / 'settings' aren't sessions, so don't clear/track for them.
    if (view === 'files' || view === 'settings') return
    const key = activeProjectId + '_' + view
    clearUnread(key)
  }, [activeProjectId, view, clearUnread])

  // Tab context menu close-on-Escape. Tracking with the ctx menu state so
  // we don't keep a stale listener when the menu is closed.
  useEffect(() => {
    if (!tabCtxMenu) return
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      if (e.key === 'Escape') { e.preventDefault(); setTabCtxMenu(null) }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [tabCtxMenu])

  // Load sessions when project changes
  useEffect(() => {
    if (!activeProjectId) return
    let cancelled = false
    ListChatSessions(activeProjectId).then((list) => {
      if (cancelled) return
      if (list && list.length > 0) {
        const tabs = list.map((s: any) => ({ id: s.id, name: s.name, parentName: s.parentName, pinned: !!s.pinned }))
        setSessions(tabs)
        // Navigate to the first session (most recently used); never hardcode
        // 'default' since the default session might have been deleted.
        setView(tabs[0].id)
      } else {
        setSessions([{ id: 'default', name: 'Chat 1' }])
        setView('default')
      }
    }).catch(() => {})
    return () => { cancelled = true }
  }, [activeProjectId])

  // Track the active session per project so components like FileBrowser can route to the right session.
  useEffect(() => {
    if (!activeProjectId) return
    const isChat = view !== 'files' && view !== 'settings'
    if (isChat) {
      useChatStore.getState().setActiveSession(activeProjectId, view)
    }
  }, [activeProjectId, view])

  const handleNewChat = useCallback(async () => {
    // Read from store instead of closure so the callback stays stable and
    // keyboard shortcuts (Ctrl+T) work even after the active project changes.
    const id = useProjectStore.getState().activeProjectId
    if (!id || creatingChatRef.current) return
    creatingChatRef.current = true
    try {
      const info = await CreateChatSession(id) as any
      if (info) {
        setSessions((prev) => [...prev, { id: info.id, name: info.name }])
        setView(info.id)
      }
    } catch (e: any) {
      console.error('CreateChatSession error:', e)
      setNewChatError(String(e?.message || e || 'Failed to create chat'))
      setTimeout(() => setNewChatError(null), 3000)
    } finally {
      creatingChatRef.current = false
    }
  }, [])

  const commitRename = async (sessionId: string, name: string) => {
    const trimmed = name.trim()
    setRenamingId(null)
    if (!trimmed || !activeProjectId) return
    const prev = sessions.find((s) => s.id === sessionId)?.name
    if (trimmed === prev) return
    // Optimistic update
    setSessions((s) => s.map((t) => (t.id === sessionId ? { ...t, name: trimmed } : t)))
    try {
      await RenameChatSession(activeProjectId, sessionId, trimmed)
    } catch (e) {
      console.error('RenameChatSession error:', e)
      // Revert on failure and briefly show error badge on the tab.
      if (prev) {
        setSessions((s) => s.map((t) => (t.id === sessionId ? { ...t, name: prev } : t)))
      }
      setRenameError(sessionId)
      setTimeout(() => setRenameError((cur) => (cur === sessionId ? null : cur)), 3000)
    }
  }

  const handleCloseChat = async (sessionId: string) => {
    if (!activeProjectId) return
    // Guard at the UI layer too — backend rejects deleting the last session,
    // but erroring out here gives the user immediate feedback.
    if (sessions.length <= 1) return
    try {
      await DeleteChatSession(activeProjectId, sessionId)
      const chatKey = activeProjectId + '_' + sessionId
      useChatStore.getState().dropSession(chatKey)

      setSessions((prev) => {
        const remaining = prev.filter((s) => s.id !== sessionId)
        // Navigate to the adjacent tab (left neighbor, or first if closing the
        // leftmost) so the view doesn't jump to the end of the list.
        if (view === sessionId && remaining.length > 0) {
          const closedIdx = prev.findIndex((s) => s.id === sessionId)
          const nextIdx = Math.max(0, Math.min(closedIdx - 1, remaining.length - 1))
          setView(remaining[nextIdx].id)
        }
        return remaining
      })
    } catch (e: any) {
      console.error('DeleteChatSession error:', e)
      setCloseError(sessionId)
      setTimeout(() => setCloseError((cur) => (cur === sessionId ? null : cur)), 3000)
    }
  }

  if (!ready) {
    return (
      <div className="app loading">
        <div className="loading-content">
          <div className="loading-brand">Gokin Studio</div>
          <div className="loading-text">Starting up...</div>
        </div>
      </div>
    )
  }

  const isChat = view !== 'files' && view !== 'settings'

  return (
    <div className={`app ${sidebarCollapsed ? 'sidebar-collapsed' : ''}`}>
      <Sidebar
        onOpenSettings={() => setView('settings')}
        onToggleCollapse={() => setSidebarCollapsed(!sidebarCollapsed)}
        collapsed={sidebarCollapsed}
      />
      <div className="main-content">
        <div className="view-tabs">
          <div className="tabs-left">
            {sessions.map((s) => {
              const sessionKey = activeProjectId ? activeProjectId + '_' + s.id : ''
              const isActive = sessionKey ? !!sessionActive[sessionKey] : false
              // Per-tab unread count: bumped when chat:complete or chat:error
              // fires while this session isn't being viewed. Suppressed for the
              // currently-viewed tab (it'd be cleared immediately anyway).
              const isCurrentTab = view === s.id
              const unreadCount = sessionKey && !isCurrentTab ? (unread[sessionKey] || 0) : 0
              const isRenaming = renamingId === s.id
              return (
                <button
                  key={s.id}
                  draggable={!isRenaming}
                  onDragStart={(e) => {
                    if (isRenaming) { e.preventDefault(); return }
                    setDraggingTabId(s.id)
                    e.dataTransfer.effectAllowed = 'move'
                    // Firefox requires non-empty data for drag to work.
                    e.dataTransfer.setData('text/plain', s.id)
                  }}
                  onDragEnd={() => { setDraggingTabId(null); setDropTargetId(null); setDropAtEnd(false) }}
                  onDragOver={(e) => {
                    if (!draggingTabId || draggingTabId === s.id) return
                    e.preventDefault()
                    e.dataTransfer.dropEffect = 'move'
                    if (dropTargetId !== s.id) setDropTargetId(s.id)
                  }}
                  onDragLeave={(e) => {
                    // Only clear if leaving the tab itself, not just a child.
                    if (e.currentTarget.contains(e.relatedTarget as Node)) return
                    if (dropTargetId === s.id) setDropTargetId(null)
                  }}
                  onDrop={async (e) => {
                    e.preventDefault()
                    const sourceId = draggingTabId
                    setDraggingTabId(null)
                    setDropTargetId(null)
                    if (!sourceId || sourceId === s.id || !activeProjectId) return
                    setSessions((prev) => {
                      const fromIdx = prev.findIndex((t) => t.id === sourceId)
                      const toIdx = prev.findIndex((t) => t.id === s.id)
                      if (fromIdx < 0 || toIdx < 0) return prev
                      const next = [...prev]
                      const [moved] = next.splice(fromIdx, 1)
                      // Drop the dragged tab BEFORE the target — matches the
                      // "drop indicator on left edge" mental model.
                      next.splice(toIdx, 0, moved)
                      // Persist asynchronously. Failure is logged but the
                      // local order stays — backend will catch up on next save.
                      const ids = next.map((t) => t.id)
                      ReorderChatSessions(activeProjectId, ids).catch((err) => {
                        console.error('ReorderChatSessions failed:', err)
                      })
                      return next
                    })
                  }}
                  className={`view-tab ${view === s.id ? 'active' : ''} ${s.parentName ? 'is-fork' : ''} ${unreadCount > 0 ? 'has-unread' : ''} ${s.pinned ? 'is-pinned' : ''} ${draggingTabId === s.id ? 'is-dragging' : ''} ${dropTargetId === s.id ? 'drop-target' : ''}`}
                  onClick={() => { if (!isRenaming) setView(s.id) }}
                  onDoubleClick={(e) => {
                    e.stopPropagation()
                    setRenameValue(s.name)
                    setRenamingId(s.id)
                  }}
                  onContextMenu={(e) => {
                    e.preventDefault()
                    setTabCtxMenu({ id: s.id, x: e.clientX, y: e.clientY })
                  }}
                  title={
                    (s.pinned ? 'Pinned to top of tab list\n' : '') +
                    (s.parentName ? `Forked from: ${s.parentName}\n` : '') +
                    (unreadCount > 0 ? `${unreadCount} new turn${unreadCount === 1 ? '' : 's'} since last viewed\n` : '') +
                    'Double-click to rename · Right-click for menu'
                  }
                >
                  {s.pinned && <Pin size={9} className="tab-pin-icon" />}
                  {s.parentName
                    ? <GitFork size={11} className="tab-fork-icon" />
                    : <MessageSquare size={13} />}
                  {isRenaming ? (
                    <input
                      className="tab-rename-input"
                      value={renameValue}
                      autoFocus
                      maxLength={60}
                      onChange={(e) => setRenameValue(e.target.value)}
                      onClick={(e) => e.stopPropagation()}
                      onBlur={() => commitRename(s.id, renameValue)}
                      onKeyDown={(e) => {
                        if (e.nativeEvent.isComposing || e.keyCode === 229) return
                        if (e.key === 'Enter') { e.preventDefault(); commitRename(s.id, renameValue) }
                        if (e.key === 'Escape') { e.preventDefault(); setRenamingId(null) }
                      }}
                    />
                  ) : (
                    <span className="tab-label">{s.name}</span>
                  )}
                  {isActive && <span className="tab-badge live">live</span>}
                  {!isActive && unreadCount > 0 && (
                    <span className="tab-badge unread" aria-label={`${unreadCount} unread`}>
                      {unreadCount > 9 ? '9+' : unreadCount}
                    </span>
                  )}
                  {renameError === s.id && <span className="tab-badge err">rename failed</span>}
                  {closeError === s.id && <span className="tab-badge err">close failed</span>}
                  {!isRenaming && sessions.length > 1 && (
                    <span
                      className="tab-close"
                      onClick={(e) => { e.stopPropagation(); handleCloseChat(s.id) }}
                      role="button"
                      title="Close tab"
                    >
                      <X size={12} />
                    </span>
                  )}
                </button>
              )
            })}
            <button
              className="view-tab tab-new"
              onClick={handleNewChat}
              title={newChatError ? `Failed: ${newChatError}` : 'New chat (Ctrl+T)'}
            >
              <Plus size={14} />
              {newChatError && <span className="tab-badge err">failed</span>}
            </button>
            {/* Drop-at-end zone (iter 650+). Catches drops past the last tab
                so users can move a tab to the very end of the list — the
                per-tab onDrop only supports "drop before target". Visible as
                a thin drop indicator only while a drag is in progress. */}
            {draggingTabId && (
              <div
                className={`tab-drop-end ${dropAtEnd ? 'active' : ''}`}
                onDragOver={(e) => {
                  if (!draggingTabId) return
                  e.preventDefault()
                  e.dataTransfer.dropEffect = 'move'
                  if (!dropAtEnd) setDropAtEnd(true)
                }}
                onDragLeave={(e) => {
                  if (e.currentTarget.contains(e.relatedTarget as Node)) return
                  setDropAtEnd(false)
                }}
                onDrop={async (e) => {
                  e.preventDefault()
                  const sourceId = draggingTabId
                  setDraggingTabId(null)
                  setDropTargetId(null)
                  setDropAtEnd(false)
                  if (!sourceId || !activeProjectId) return
                  setSessions((prev) => {
                    const fromIdx = prev.findIndex((t) => t.id === sourceId)
                    if (fromIdx < 0 || fromIdx === prev.length - 1) return prev
                    const next = [...prev]
                    const [moved] = next.splice(fromIdx, 1)
                    next.push(moved)
                    const ids = next.map((t) => t.id)
                    ReorderChatSessions(activeProjectId, ids).catch((err) => {
                      console.error('ReorderChatSessions failed:', err)
                    })
                    return next
                  })
                }}
                aria-hidden
              />
            )}
          </div>
          <div className="tabs-right">
            <button className={`view-tab ${view === 'files' ? 'active' : ''}`} onClick={() => setView('files')} title="Files (Ctrl+2)">
              <FolderTree size={14} />
              <span className="tab-label">Files</span>
            </button>
            <button className={`view-tab ${view === 'settings' ? 'active' : ''}`} onClick={() => setView('settings')} title="Settings (Ctrl+3)">
              <Settings size={14} />
              <span className="tab-label">Settings</span>
            </button>
          </div>
        </div>
        <div className="content-area">
          {view === 'settings' ? <SettingsPage />
            : view === 'files' ? <FileBrowser />
            : <ChatPanel sessionId={view} sessionName={sessions.find((s) => s.id === view)?.name} />}
        </div>
      </div>
      <StatusBar />
      <ToastStack />
      {showOnboarding && (
        <OnboardingWizard
          onComplete={(project) => {
            setShowOnboarding(false)
            if (project) {
              // Project was created by the wizard — refresh sessions for the
              // new active project so the tab bar shows its default session.
              ListChatSessions(project.id).then((list) => {
                if (list && Array.isArray(list)) {
                  setSessions(
                    list.map((s: any) => ({
                      id: s.id, name: s.name, live: false, parentName: s.parentName, pinned: !!s.pinned,
                    })),
                  )
                  if (list.length > 0) setView(list[0].id)
                }
              }).catch(() => { /* fallback to default session */ })
            }
          }}
          onSkip={() => setShowOnboarding(false)}
        />
      )}
      {tabCtxMenu && (() => {
        const MENU_W = 160, MENU_H = 80
        const cx = Math.min(tabCtxMenu.x, window.innerWidth - MENU_W - 8)
        const cy = Math.min(tabCtxMenu.y, window.innerHeight - MENU_H - 8)
        const tab = sessions.find((t) => t.id === tabCtxMenu.id)
        const isPinned = !!tab?.pinned
        return (
          <>
            <div className="ctx-backdrop" onClick={() => setTabCtxMenu(null)} />
            <div className="ctx-menu" style={{ top: cy, left: cx }}>
              <button onClick={async () => {
                if (!activeProjectId) { setTabCtxMenu(null); return }
                const targetId = tabCtxMenu.id
                const next = !isPinned
                setTabCtxMenu(null)
                // Optimistic local update so the sort runs immediately; if
                // the backend rejects we revert. Errors logged silently —
                // a failure here is non-blocking.
                setSessions((prev) => prev.map((t) => (t.id === targetId ? { ...t, pinned: next } : t)))
                try {
                  await SetSessionPinned(activeProjectId, targetId, next)
                } catch (e) {
                  console.error('SetSessionPinned error:', e)
                  setSessions((prev) => prev.map((t) => (t.id === targetId ? { ...t, pinned: isPinned } : t)))
                }
                // Reload from backend to get the canonical sorted order so the
                // pinned tab actually moves to the top of the list.
                if (activeProjectId) {
                  ListChatSessions(activeProjectId).then((list) => {
                    if (list) setSessions(list.map((sx: any) => ({ id: sx.id, name: sx.name, parentName: sx.parentName, pinned: !!sx.pinned })))
                  }).catch(() => {})
                }
              }}>
                {isPinned ? <><PinOff size={12} /> Unpin from top</> : <><Pin size={12} /> Pin to top</>}
              </button>
              <div className="ctx-divider" />
              <button onClick={() => {
                const targetTab = sessions.find((t) => t.id === tabCtxMenu.id)
                if (targetTab) {
                  setRenameValue(targetTab.name)
                  setRenamingId(targetTab.id)
                }
                setTabCtxMenu(null)
              }}>Rename</button>
            </div>
          </>
        )
      })()}
      {showPalette && (
        <CommandPalette
          onClose={() => setShowPalette(false)}
          onSwitchProject={(id) => useProjectStore.getState().setActiveProject(id)}
          onOpenSettings={() => setView('settings')}
          onOpenFiles={() => setView('files')}
          onNewChat={handleNewChat}
          onClearChat={() => window.dispatchEvent(new CustomEvent('gokin:clear-chat'))}
          onOpenMemory={() => window.dispatchEvent(new CustomEvent('gokin:open-memory'))}
          onToggleSidebar={() => setSidebarCollapsed((c) => !c)}
        />
      )}
    </div>
  )
}

function App() {
  return (
    <ErrorBoundary>
      <AppContent />
    </ErrorBoundary>
  )
}

export default App
