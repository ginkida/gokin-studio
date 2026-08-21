import { lazy, Suspense, useCallback, useEffect, useRef, useState, type CSSProperties } from 'react'
import { Sidebar } from './components/layout/Sidebar'
import { StatusBar } from './components/layout/StatusBar'
import { TopBar } from './components/layout/TopBar'
import { ToastStack } from './components/layout/ToastStack'
import { SessionWorkspace } from './components/layout/SessionWorkspace'
import { ErrorBoundary, installGlobalErrorHandlers } from './components/layout/ErrorBoundary'
import { FileBrowser } from './components/files/FileBrowser'
import { FileContextMenuHost } from './components/files/FileContextMenu'
import { ArtifactLibrary } from './components/files/ArtifactLibrary'
import { useConfirmDialog } from './components/common/AppDialog'
import { useWailsEvents } from './hooks/useWailsEvents'
import { hasOpenModal, useModalFocusManagement } from './hooks/useModalFocusManagement'
import { useProjectStore, ProjectInfo } from './stores/projectStore'
import { useSettingsStore } from './stores/settingsStore'
import { useChatStore } from './stores/chatStore'
import { normalizeProviderCatalog } from './lib/providerCatalog'
import { composeInChat } from './lib/composeInChat'
import { persistActiveProject, persistWorkspaceLocation, readWorkspaceContinuity } from './lib/workspaceContinuity'
import { observeThemePreference } from './lib/theme'
import { isStaticPreviewFilePath } from './lib/previewFiles'
import { workspaceMouseHistoryDirection } from './lib/workspaceMouseNavigation'
import { isCloseChatShortcut, resolveCloseChatSession } from './lib/closeChat'
import { sessionCycleShortcutDirection } from './lib/sessionCycleShortcuts'
import { isNewSessionShortcut } from './lib/newSessionShortcut'
import { paneShortcutAction, type PaneShortcutAction } from './lib/paneShortcuts'
import type { PendingQuickEntryComposerAction, QuickEntryComposerAction } from './lib/quickEntry'
import { shouldShowOnboarding } from './lib/onboarding'
import { ListProjects, GetSettings, GetProviders, GetProviderCredentialSources, CreateChatSession, ListChatSessions, ListArchivedChatSessions, ArchiveChatSession, RestoreChatSession, DeleteChatSession, RenameChatSession, SetSessionPinned, ReorderChatSessions, SaveDraft, ShowQuickEntryWindow, HideQuickEntryWindow, StartDeepLinkEvents, StartNativeMenuEvents, CheckForUpdatesIfDue, GetSessionWorktreeStatus } from '../wailsjs/go/studio/Studio'
import { BrowserOpenURL, EventsOn, WindowCenter, WindowFullscreen, WindowGetPosition, WindowGetSize, WindowIsFullscreen, WindowIsMaximised, WindowMaximise, WindowSetAlwaysOnTop, WindowSetPosition, WindowSetSize, WindowSetTitle, WindowUnfullscreen, WindowUnmaximise } from '../wailsjs/runtime/runtime'
import { MessageSquare, Settings, FolderTree, Plus, X, GitFork, GitBranch, Pin, PinOff, PanelsTopLeft, Trash2, ListFilter, Search, Loader2, RefreshCw, AlertTriangle, Download, Archive, ArchiveRestore } from 'lucide-react'
import './App.css'

const DelegationsPanel = lazy(() => import('./components/dispatch/DelegationsPanel'))
const SettingsPage = lazy(() => import('./components/settings/SettingsPage').then((module) => ({ default: module.SettingsPage })))
const OnboardingWizard = lazy(() => import('./components/onboarding/OnboardingWizard').then((module) => ({ default: module.OnboardingWizard })))
const CommandPalette = lazy(() => import('./components/palette/CommandPalette').then((module) => ({ default: module.CommandPalette })))
const QuickEntryOverlay = lazy(() => import('./components/quickentry/QuickEntryOverlay').then((module) => ({ default: module.QuickEntryOverlay })))

// Install once at module load — before any component mounts — so even
// errors during initial bootstrap land in the event log.
installGlobalErrorHandlers()

function LazyViewFallback({ label }: { label: string }) {
  return (
    <div className="session-load-state" role="status" aria-live="polite">
      <div className="session-load-state-icon"><Loader2 size={22} className="spin" /></div>
      <h2>{label}</h2>
    </div>
  )
}

function LazyModalFallback({ label }: { label: string }) {
  return (
    <div className="app-dialog-backdrop">
      <div
        className="app-dialog lazy-modal-loading"
        role="dialog"
        aria-modal="true"
        aria-label={label}
        aria-busy="true"
      >
        <Loader2 size={18} className="spin" />
        <span role="status" aria-live="polite">{label}</span>
      </div>
    </div>
  )
}

interface SessionTab {
  id: string
  name: string
  // Plan is intentionally session-scoped and ephemeral. Empty means this
  // chat inherits the project's durable Manual / Accept edits / Auto / Skip default.
  permissionMode?: string
  // Lineage indicator: when this session was forked, parentName names the
  // session it came from (or "(deleted)" if that source has been removed).
  // Empty for top-level / non-forked sessions. Used to render the "↳ name"
  // chip in the tab list so users can trace fork lineage at a glance.
  parentName?: string
  // Pinned anchors this tab to the top of the tab list (iter 480+).
  pinned?: boolean
  createdAt?: number
  lastUsedAt?: number
  messages?: number
  worktreeIsolated?: boolean
  isolationSkippedReason?: string
  worktreePath?: string
  worktreeBranch?: string
  worktreeError?: string
  archived?: boolean
  archivedAt?: number
}

interface DesktopUpdateStatus {
  currentVersion: string
  latestVersion?: string
  available: boolean
  checkedAt?: number
  publishedAt?: number
  releaseURL?: string
}

function toSessionTab(session: any): SessionTab {
  return {
    id: String(session?.id || ''),
    name: String(session?.name || 'Chat'),
    permissionMode: session?.permissionMode ? String(session.permissionMode) : undefined,
    parentName: session?.parentName ? String(session.parentName) : undefined,
    pinned: !!session?.pinned,
    createdAt: Number(session?.createdAt) || 0,
    lastUsedAt: Number(session?.lastUsedAt) || 0,
    messages: Number(session?.messages) || 0,
    worktreeIsolated: !!session?.worktreeIsolated,
    isolationSkippedReason: session?.isolationSkippedReason || '',
    worktreePath: session?.worktreePath ? String(session.worktreePath) : undefined,
    worktreeBranch: session?.worktreeBranch ? String(session.worktreeBranch) : undefined,
    worktreeError: session?.worktreeError ? String(session.worktreeError) : undefined,
    archived: !!session?.archived,
    archivedAt: Number(session?.archivedAt) || 0,
  }
}

interface QuickEntryWindowSnapshot {
  width: number
  height: number
  x: number
  y: number
  maximised: boolean
  fullscreen: boolean
}

interface StudioDeepLink {
  sequence: number
  action: 'new' | 'chat' | 'project'
  projectID?: string
  sessionID?: string
  prompt?: string
  view?: 'chat' | 'files' | 'artifacts'
}

interface WorkspaceNavigationHistory {
  projectKey: string
  entries: string[]
  index: number
}

interface ProjectArtifactSelection {
  projectID: string
  sessionID: string
  path: string
}

const SIDEBAR_WIDTH_KEY = 'gokin:sidebar-width'
const SIDEBAR_WIDTH_MIN = 220
const SIDEBAR_WIDTH_MAX = 420
const SIDEBAR_WIDTH_DEFAULT = 272

function clampSidebarWidth(value: number) {
  return Math.max(SIDEBAR_WIDTH_MIN, Math.min(SIDEBAR_WIDTH_MAX, Math.round(value)))
}

function initialSidebarWidth() {
  try {
    const stored = Number(localStorage.getItem(SIDEBAR_WIDTH_KEY))
    return Number.isFinite(stored) && stored > 0 ? clampSidebarWidth(stored) : SIDEBAR_WIDTH_DEFAULT
  } catch {
    return SIDEBAR_WIDTH_DEFAULT
  }
}

function AppContent() {
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  useModalFocusManagement()
  const setProjects = useProjectStore((s) => s.setProjects)
  const setSettings = useSettingsStore((s) => s.setSettings)
  const setProviders = useSettingsStore((s) => s.setProviders)
  const setProviderCredentialSources = useSettingsStore((s) => s.setProviderCredentialSources)
  const theme = useSettingsStore((s) => s.settings.theme)
  const settingsDraft = useSettingsStore((s) => s.settingsDraft)

  // Onboarding wizard (iter 730+). Shown once on a fresh install — when no
  // projects are configured AND the user hasn't explicitly skipped before.
  // Becomes false on any of: project added, "Start chatting" clicked, Skip clicked.
  const [showOnboarding, setShowOnboarding] = useState(false)
  const [showDelegations, setShowDelegations] = useState(false)

  // view: session ID for chat tabs, or one of the standalone workspace views.
  const [view, setView] = useState<string>('default')
  const viewRef = useRef(view)
  viewRef.current = view
  const startupContinuityRef = useRef(readWorkspaceContinuity())
  const startupRestorePendingRef = useRef(true)
  const pendingWorkspaceViewRef = useRef<{ projectID: string; view: 'chat' | 'files' | 'artifacts' } | null>(null)
  const [settingsMounted, setSettingsMounted] = useState(false)
  const [filesMounted, setFilesMounted] = useState(false)
  const [artifactsMounted, setArtifactsMounted] = useState(false)
  const [fileArtifactSelection, setFileArtifactSelection] = useState<ProjectArtifactSelection | null>(null)
  const [libraryArtifactSelection, setLibraryArtifactSelection] = useState<ProjectArtifactSelection | null>(null)
  const [ready, setReady] = useState(false)
  const [bootstrapError, setBootstrapError] = useState<string | null>(null)
  const [bootstrapAttempt, setBootstrapAttempt] = useState(0)
  const [quickEntryOpen, setQuickEntryOpen] = useState(false)
  const quickEntryOpenRef = useRef(false)
  const quickEntryTransitionRef = useRef(false)
  const quickEntryWindowRef = useRef<QuickEntryWindowSnapshot | null>(null)
  const quickEntryNativeRef = useRef(false)
	const deepLinkChainRef = useRef<Promise<void>>(Promise.resolve())
  const quickEntryVoiceSequenceRef = useRef(0)
  const [pendingQuickEntryVoiceActivations, setPendingQuickEntryVoiceActivations] = useState<number[]>([])
  const quickEntryActionSequenceRef = useRef(0)
  const [pendingQuickEntryAction, setPendingQuickEntryAction] = useState<PendingQuickEntryComposerAction | null>(null)
  const [sidebarCollapsed, setSidebarCollapsed] = useState(() => {
    try { return localStorage.getItem('gokin:sidebar-collapsed') === '1' } catch { return false }
  })
  const [sidebarWidth, setSidebarWidth] = useState(initialSidebarWidth)
  const sidebarWidthRef = useRef(sidebarWidth)
  const sidebarResizeActiveRef = useRef(false)
  const [sidebarResizing, setSidebarResizing] = useState(false)
  const [compactViewport, setCompactViewport] = useState(() => (
    typeof window !== 'undefined' && window.matchMedia('(max-width: 760px)').matches
  ))
  const [sidebarDrawerOpen, setSidebarDrawerOpen] = useState(false)
  const [sessions, setSessions] = useState<SessionTab[]>([])
  const sessionsRef = useRef<SessionTab[]>([])
  sessionsRef.current = sessions
  const [sessionsProjectID, setSessionsProjectID] = useState<string | null>(null)
  const sessionsProjectIDRef = useRef<string | null>(null)
  sessionsProjectIDRef.current = sessionsProjectID
  const pendingNativeSessionCommandsRef = useRef<Array<{ projectID: string; command: string }>>([])
  const nativeCommandHandlerRef = useRef<(payload?: string | { command?: string }) => void>(() => {})
  const nativeFrontendReadyRef = useRef(ready)
  nativeFrontendReadyRef.current = ready
  const nativeEventsStartedRef = useRef(false)
  const nativeEventsStartingRef = useRef(false)
  const pendingNativeBootstrapCommandsRef = useRef<Array<string | { command?: string }>>([])
  const navigationHistoryRef = useRef<WorkspaceNavigationHistory>({ projectKey: '', entries: [], index: -1 })
  const navigationReplayRef = useRef(false)
  const [, setNavigationRevision] = useState(0)
  const [sessionsLoading, setSessionsLoading] = useState(false)
  const [sessionLoadError, setSessionLoadError] = useState<string | null>(null)
  const [sessionReloadNonce, setSessionReloadNonce] = useState(0)
  const sessionListRequestRef = useRef(0)
  const pendingComposeRef = useRef<{
    projectID: string
    text: string
    mode: 'replace' | 'append'
    sessionID?: string
  } | null>(null)
  const [showSessionSwitcher, setShowSessionSwitcher] = useState(false)
  const [sessionSwitcherQuery, setSessionSwitcherQuery] = useState('')
  const sessionSwitcherRef = useRef<HTMLDivElement>(null)
  const sessionSwitcherTriggerRef = useRef<HTMLButtonElement>(null)
  const sessionSwitcherWasOpenRef = useRef(false)
  const tabsLeftRef = useRef<HTMLDivElement>(null)
  const [showPalette, setShowPalette] = useState(false)
  const [renamingId, setRenamingId] = useState<string | null>(null)
  const [renameValue, setRenameValue] = useState('')
  const [renameError, setRenameError] = useState<string | null>(null)
  const [closeError, setCloseError] = useState<{ id: string; message: string } | null>(null)
  const [showArchivedSessions, setShowArchivedSessions] = useState(false)
  const [archivedSessions, setArchivedSessions] = useState<SessionTab[]>([])
  const [archivedSessionsLoading, setArchivedSessionsLoading] = useState(false)
  const [archivedSessionsError, setArchivedSessionsError] = useState<string | null>(null)
  const [archivedSessionBusy, setArchivedSessionBusy] = useState<string | null>(null)
  const [newChatError, setNewChatError] = useState<string | null>(null)
  const [deepLinkError, setDeepLinkError] = useState<string | null>(null)
  const [desktopUpdate, setDesktopUpdate] = useState<DesktopUpdateStatus | null>(null)
  const [dismissedUpdateVersion, setDismissedUpdateVersion] = useState<string | null>(null)
  const creatingChatRef = useRef(false)
  const archivingSessionIDsRef = useRef(new Set<string>())
  // Tab right-click context menu (iter 480+). Holds the target session id +
  // viewport coords. Closes on Escape, click outside, or any tab action.
  const [tabCtxMenu, setTabCtxMenu] = useState<{ id: string; x: number; y: number } | null>(null)
  const tabCtxMenuRef = useRef<HTMLDivElement>(null)
  const tabCtxMenuTriggerRef = useRef<HTMLButtonElement | null>(null)
  const tabCtxMenuWasOpenRef = useRef(false)
  // Drag-to-reorder state (iter 540+). draggingId tracks which tab the user
  // is currently dragging; dropTargetId is the tab the cursor is hovering
  // over so we can render a drop indicator.
  const [draggingTabId, setDraggingTabId] = useState<string | null>(null)
  const [dropTargetId, setDropTargetId] = useState<string | null>(null)
  const [tabOrderSaving, setTabOrderSaving] = useState(false)
  const [tabOrderError, setTabOrderError] = useState<string | null>(null)
  // Iter 650+: a separate "drop at end" zone after the new-chat (+) button
  // because dropping on tab N puts the dragged BEFORE N — there was no way
  // to drop at the very end of the list. true while user drags over the
  // dedicated end-zone.
  const [dropAtEnd, setDropAtEnd] = useState(false)

  useWailsEvents()

  const toggleSidebar = useCallback(() => {
    if (compactViewport) {
      setSidebarDrawerOpen((open) => !open)
      return
    }
    setSidebarCollapsed((collapsed) => {
      const next = !collapsed
      try { localStorage.setItem('gokin:sidebar-collapsed', next ? '1' : '0') } catch { /* storage unavailable */ }
      return next
    })
  }, [compactViewport])

  const updateSidebarWidth = useCallback((value: number, persist = false) => {
    const next = clampSidebarWidth(value)
    sidebarWidthRef.current = next
    setSidebarWidth(next)
    if (persist) {
      try { localStorage.setItem(SIDEBAR_WIDTH_KEY, String(next)) } catch { /* storage unavailable */ }
    }
  }, [])

  const isNavigationTargetAvailable = useCallback((target: string) => {
    if (target === 'files' || target === 'artifacts' || target === 'settings') return true
    const projectID = useProjectStore.getState().activeProjectId
    return !!projectID && sessionsProjectIDRef.current === projectID && sessionsRef.current.some((session) => session.id === target)
  }, [])

  const findNavigationTarget = useCallback((direction: -1 | 1) => {
    const history = navigationHistoryRef.current
    for (let index = history.index + direction; index >= 0 && index < history.entries.length; index += direction) {
      if (isNavigationTargetAvailable(history.entries[index])) return index
    }
    return -1
  }, [isNavigationTargetAvailable])

  const navigateHistory = useCallback((direction: -1 | 1) => {
    const targetIndex = findNavigationTarget(direction)
    if (targetIndex < 0) return
    const history = navigationHistoryRef.current
    const target = history.entries[targetIndex]
    history.index = targetIndex
    navigationReplayRef.current = true
    setNavigationRevision((revision) => revision + 1)
    setView(target)
  }, [findNavigationTarget])

  useEffect(() => {
    const resetLayout = () => {
      sidebarResizeActiveRef.current = false
      setSidebarResizing(false)
      setSidebarCollapsed(false)
      setSidebarDrawerOpen(false)
      updateSidebarWidth(SIDEBAR_WIDTH_DEFAULT)
    }
    window.addEventListener('gokin:layout-reset', resetLayout)
    return () => window.removeEventListener('gokin:layout-reset', resetLayout)
  }, [updateSidebarWidth])

  useEffect(() => {
    const query = window.matchMedia('(max-width: 760px)')
    const update = (event: MediaQueryListEvent | MediaQueryList) => {
      setCompactViewport(event.matches)
      if (!event.matches) setSidebarDrawerOpen(false)
    }
    update(query)
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [])

  useEffect(() => {
    if (!sidebarDrawerOpen) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || hasOpenModal()) return
      event.preventDefault()
      setSidebarDrawerOpen(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [sidebarDrawerOpen])

  // Apply theme
  useEffect(() => {
    return observeThemePreference(theme)
  }, [theme])

  const enterQuickEntry = useCallback(async (mode: 'text' | 'voice' = 'text') => {
    if (mode === 'voice') {
      const activationID = ++quickEntryVoiceSequenceRef.current
      setPendingQuickEntryVoiceActivations((pending) => [...pending, activationID])
    }
    if (quickEntryOpenRef.current) {
      window.dispatchEvent(new CustomEvent('gokin:focus-quick-entry'))
      return
    }
    if (quickEntryTransitionRef.current) return
    quickEntryTransitionRef.current = true
    try {
      const nativeStatus = await ShowQuickEntryWindow()
      if (nativeStatus?.native && nativeStatus?.open) {
        quickEntryNativeRef.current = true
        quickEntryOpenRef.current = true
        setQuickEntryOpen(true)
        requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:focus-quick-entry')))
        return
      }
      const [size, position, maximised, fullscreen] = await Promise.all([
        WindowGetSize(),
        WindowGetPosition(),
        WindowIsMaximised(),
        WindowIsFullscreen(),
      ])
      quickEntryWindowRef.current = {
        width: size.w,
        height: size.h,
        x: position.x,
        y: position.y,
        maximised,
        fullscreen,
      }
      if (fullscreen) WindowUnfullscreen()
      if (maximised) WindowUnmaximise()
      quickEntryOpenRef.current = true
      setQuickEntryOpen(true)
      WindowSetTitle('Quick Entry — Gokin Studio')
      WindowSetAlwaysOnTop(true)
      requestAnimationFrame(() => {
        WindowSetSize(720, 560)
        WindowCenter()
        requestAnimationFrame(() => window.dispatchEvent(new CustomEvent('gokin:focus-quick-entry')))
      })
    } catch (error) {
      console.error('Could not enter compact Quick Entry:', error)
      // The native shortcut must remain useful even if a platform cannot
      // report geometry. Render the surface in the current window.
      quickEntryOpenRef.current = true
      setQuickEntryOpen(true)
      WindowSetAlwaysOnTop(true)
    } finally {
      quickEntryTransitionRef.current = false
    }
  }, [])

  const exitQuickEntry = useCallback(async (activateStudio = false) => {
    if (!quickEntryOpenRef.current && !quickEntryWindowRef.current) return
    setPendingQuickEntryVoiceActivations([])
    quickEntryOpenRef.current = false
    if (quickEntryNativeRef.current) {
      quickEntryNativeRef.current = false
      try {
        await HideQuickEntryWindow(activateStudio)
      } catch (error) {
        console.error('Could not restore the native Quick Entry window:', error)
      }
      setQuickEntryOpen(false)
      return
    }
    setQuickEntryOpen(false)
    WindowSetAlwaysOnTop(false)
    WindowSetTitle('Gokin Studio')
    const snapshot = quickEntryWindowRef.current
    quickEntryWindowRef.current = null
    if (!snapshot) return
    requestAnimationFrame(() => {
      if (snapshot.fullscreen) {
        WindowFullscreen()
      } else if (snapshot.maximised) {
        WindowMaximise()
      } else {
        WindowSetSize(snapshot.width, snapshot.height)
        WindowSetPosition(snapshot.x, snapshot.y)
      }
    })
  }, [])

  useEffect(() => () => {
    if (quickEntryNativeRef.current) {
      void HideQuickEntryWindow(false)
    } else if (quickEntryOpenRef.current) {
      WindowSetAlwaysOnTop(false)
    }
  }, [])

  // Lazy-mount Settings on first visit, then keep it alive while hidden. This
  // preserves connector forms, plugin reviews, diagnostics, scroll position,
  // and the memory-only global settings draft across workspace navigation
  // without paying the Settings startup calls before the user opens it.
  useEffect(() => {
    if (view === 'settings') setSettingsMounted(true)
    if (view === 'files') setFilesMounted(true)
    if (view === 'artifacts') setArtifactsMounted(true)
  }, [view])

  // Browser reloads and ordinary window-close paths should not silently drop
  // an in-memory Settings draft. Wails/WebView support for the native prompt
  // varies, but preventDefault also protects accidental browser reloads.
  useEffect(() => {
    if (!settingsDraft) return
    const beforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', beforeUnload)
    return () => window.removeEventListener('beforeunload', beforeUnload)
  }, [settingsDraft])

  const routeLoadedChatTool = useCallback((eventName: string) => {
    const projectID = useProjectStore.getState().activeProjectId
    if (!projectID || sessionsProjectIDRef.current !== projectID || sessionsRef.current.length === 0) return false
    window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: 'chat' }))
    requestAnimationFrame(() => requestAnimationFrame(() => {
      window.dispatchEvent(new CustomEvent(eventName))
    }))
    return true
  }, [])

  useEffect(() => {
    const events: Record<PaneShortcutAction, string> = {
      diff: 'gokin:toggle-diff-pane',
      preview: 'gokin:toggle-live-preview',
      'select-preview-element': 'gokin:select-preview-element',
      terminal: 'gokin:toggle-workspace-terminal',
    }
    const handlePaneShortcut = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229 || quickEntryOpenRef.current) return
      const action = paneShortcutAction(event)
      if (!action) return
      event.preventDefault()
      event.stopImmediatePropagation()
      if (hasOpenModal()) {
        document.querySelector<HTMLElement>('[aria-modal="true"]')?.focus()
        return
      }
      routeLoadedChatTool(events[action])
    }
    window.addEventListener('keydown', handlePaneShortcut, true)
    return () => window.removeEventListener('keydown', handlePaneShortcut, true)
  }, [routeLoadedChatTool])

  // Keyboard shortcuts
  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    // Don't intercept anything while an IME (Russian/CJK/etc.) is composing —
    // preventDefault during composition can eat characters the user is typing.
    if (e.isComposing || e.keyCode === 229 || quickEntryOpenRef.current) return

    const target = e.target as HTMLElement
    const isInput = target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.tagName === 'SELECT'

    // The active modal owns the keyboard until it closes. This prevents
    // global shortcuts from navigating the workspace or stacking a second
    // overlay behind the current task.
    const activeModal = document.querySelector<HTMLElement>('[aria-modal="true"]')
    if (hasOpenModal() && activeModal) {
      const key = e.key.toLowerCase()
      const isGlobalShortcut = (e.ctrlKey || e.metaKey) && (
        ['k', 'l', 't', 'n', 'o', 'w', 'd', '1', '2', '3', '4', 'b', '[', ']'].includes(key) ||
        e.code === 'Comma' ||
        e.code === 'KeyW' || e.code === 'KeyD' || e.code === 'KeyN' ||
        e.code === 'BracketLeft' || e.code === 'BracketRight' ||
        e.key === 'PageUp' || e.key === 'PageDown'
      )
      const sessionCycleDirection = sessionCycleShortcutDirection(e)
      const isSessionShortcut = e.altKey && !e.ctrlKey && !e.metaKey && /^[1-9]$/.test(e.key)
      if (isGlobalShortcut || sessionCycleDirection !== null || isSessionShortcut) e.preventDefault()
      if (activeModal.id === 'project-sidebar' && (e.ctrlKey || e.metaKey) && key === 'b') {
        setSidebarDrawerOpen(false)
        return
      }
      // Preserve the palette's toggle shortcut without letting any other
      // global action leak through an active modal.
      if (activeModal.classList.contains('palette') && (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault()
        setShowPalette(false)
      }
      return
    }

    if (e.ctrlKey || e.metaKey) {
      if (isNewSessionShortcut(e)) {
        e.preventDefault()
        if (useProjectStore.getState().activeProjectId) void handleNewChat()
        else window.dispatchEvent(new CustomEvent('gokin:add-project'))
        return
      }
      if (isCloseChatShortcut(e)) {
        e.preventDefault()
        window.dispatchEvent(new CustomEvent('gokin:archive-active-session'))
        return
      }
      const sessionCycleDirection = sessionCycleShortcutDirection(e)
      if (sessionCycleDirection !== null) {
        e.preventDefault()
        window.dispatchEvent(new CustomEvent('gokin:cycle-session', { detail: { direction: sessionCycleDirection } }))
        return
      }
      // Claude Desktop uses Ctrl+O on every platform for transcript density,
      // leaving macOS Cmd+O available for the native Connect Folder action.
      if (e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && (e.key.toLowerCase() === 'o' || e.code === 'KeyO')) {
        e.preventDefault()
        window.dispatchEvent(new CustomEvent('gokin:cycle-transcript-mode'))
        return
      }
      if (!e.altKey && !e.shiftKey && !isInput && (e.key === '[' || e.code === 'BracketLeft')) {
        e.preventDefault()
        navigateHistory(-1)
        return
      }
      if (!e.altKey && !e.shiftKey && !isInput && (e.key === ']' || e.code === 'BracketRight')) {
        e.preventDefault()
        navigateHistory(1)
        return
      }
      // Conventional desktop preference shortcut. Use the physical key too,
      // so Cmd+, keeps working under Russian and other keyboard layouts.
      if (!e.altKey && !e.shiftKey && (e.key === ',' || e.code === 'Comma')) {
        e.preventDefault()
        setView('settings')
        return
      }
      if (e.key === 'k') {
        // Ctrl+K works even when input is focused — palette is the point.
        e.preventDefault()
        setShowPalette((s) => !s)
      }
      if (e.key === 'l' && !isInput) {
        e.preventDefault()
        window.dispatchEvent(new CustomEvent('gokin:clear-chat'))
      }
      if (!e.altKey && !e.shiftKey && !isInput && (e.key.toLowerCase() === 't' || e.code === 'KeyT')) {
        e.preventDefault()
        void handleNewChat()
      }
      // Ctrl+1 → chat (restore the last session the user was on, or first in list)
      if (e.key === '1' && !isInput) {
        e.preventDefault()
        const pid = useProjectStore.getState().activeProjectId
        if (pid && sessionsProjectIDRef.current !== pid) {
          pendingWorkspaceViewRef.current = { projectID: pid, view: 'chat' }
          setView('default')
          return
        }
        const lastSession = pid ? useChatStore.getState().activeSession[pid] : null
        setSessions((list) => {
          const exists = lastSession && list.some((t) => t.id === lastSession)
          setView(exists ? lastSession! : (list[0]?.id || 'default'))
          return list
        })
      }
      if (e.key === '2' && !isInput) { e.preventDefault(); setView('files') }
      if (e.key === '3' && !isInput) { e.preventDefault(); setView('settings') }
      if (e.key === '4' && !isInput) { e.preventDefault(); setView('artifacts') }
      // Ctrl+B toggles the project sidebar (VS Code convention).
      if (e.key === 'b' && !isInput) { e.preventDefault(); toggleSidebar() }
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
  }, [navigateHistory, setView, toggleSidebar])

  useEffect(() => {
    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [handleKeyDown])

  // Dedicated mouse Back/Forward buttons use the same validated, project-local
  // trail as the title-bar buttons and Ctrl/Cmd+[ ]. Consume the WebView's
  // default navigation even when there is no target so a side-button press can
  // never replace the Wails document with an internal browser-history entry.
  // Some WebViews expose the sequence as mouseup, others only as auxclick;
  // suppress the latter when it immediately follows the handled mouseup.
  useEffect(() => {
    let suppressAuxButton: number | null = null
    let clearSuppressionTimer = 0

    const navigateFromMouse = (event: MouseEvent) => {
      const direction = workspaceMouseHistoryDirection(event.button)
      if (direction === null) return false
      event.preventDefault()
      event.stopPropagation()
      if (quickEntryOpenRef.current || hasOpenModal()) {
        document.querySelector<HTMLElement>('[aria-modal="true"]')?.focus()
        return true
      }
      navigateHistory(direction)
      return true
    }

    const handleMouseUp = (event: MouseEvent) => {
      if (!navigateFromMouse(event)) return
      suppressAuxButton = event.button
      window.clearTimeout(clearSuppressionTimer)
      clearSuppressionTimer = window.setTimeout(() => { suppressAuxButton = null }, 0)
    }
    const handleAuxClick = (event: MouseEvent) => {
      const direction = workspaceMouseHistoryDirection(event.button)
      if (direction === null) return
      event.preventDefault()
      event.stopPropagation()
      if (suppressAuxButton === event.button) return
      if (quickEntryOpenRef.current || hasOpenModal()) {
        document.querySelector<HTMLElement>('[aria-modal="true"]')?.focus()
        return
      }
      navigateHistory(direction)
    }

    window.addEventListener('mouseup', handleMouseUp, true)
    window.addEventListener('auxclick', handleAuxClick, true)
    return () => {
      window.clearTimeout(clearSuppressionTimer)
      window.removeEventListener('mouseup', handleMouseUp, true)
      window.removeEventListener('auxclick', handleAuxClick, true)
    }
  }, [navigateHistory])

  // Tab switch events
  useEffect(() => {
    const switchHandler = (e: Event) => {
      const tab = (e as CustomEvent).detail
      if (!tab) return
      if (tab === 'chat') {
        // Restore last-used session, validating it still exists in the tab list.
        const pid = useProjectStore.getState().activeProjectId
        if (pid && sessionsProjectIDRef.current !== pid) {
          pendingWorkspaceViewRef.current = { projectID: pid, view: 'chat' }
          setView('default')
          return
        }
        const lastSession = pid ? useChatStore.getState().activeSession[pid] : null
        setSessions((list) => {
          const exists = lastSession && list.some((t) => t.id === lastSession)
          setView(exists ? lastSession! : (list[0]?.id || 'default'))
          return list
        })
      } else {
        const pid = useProjectStore.getState().activeProjectId
        if (pid && sessionsProjectIDRef.current !== pid && (tab === 'files' || tab === 'artifacts')) {
          pendingWorkspaceViewRef.current = { projectID: pid, view: tab }
        }
        setView(tab)
      }
    }
    const settingsHandler = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      setView('settings')
      if (detail.section) {
        requestAnimationFrame(() => requestAnimationFrame(() => {
          window.dispatchEvent(new CustomEvent('gokin:show-settings-section', { detail }))
        }))
      }
    }
    const artifactHandler = (e: Event) => {
      const detail = (e as CustomEvent).detail
      const path = typeof detail === 'string' ? detail : detail?.path
      const projectID = useProjectStore.getState().activeProjectId
      if (!path || !projectID) return
      const sessionID = (typeof detail === 'object' && detail?.sessionID) || useChatStore.getState().activeSession[projectID] || 'default'

      if (isStaticPreviewFilePath(path)) {
        const available = sessionsProjectIDRef.current === projectID ? sessionsRef.current : []
        const targetSession = available.some((session) => session.id === sessionID)
          ? sessionID
          : (available[0]?.id || sessionID || 'default')
        setView(targetSession)
        requestAnimationFrame(() => requestAnimationFrame(() => {
          window.dispatchEvent(new CustomEvent('gokin:open-preview-file', {
            detail: { projectID, sessionID: targetSession, path },
          }))
        }))
        return
      }
      setLibraryArtifactSelection({ projectID, sessionID, path })
      setView('artifacts')
    }
    const fileHandler = (e: Event) => {
      const detail = (e as CustomEvent).detail || {}
      const path = typeof detail === 'string' ? detail : detail.path
      const projectID = useProjectStore.getState().activeProjectId
      if (typeof path !== 'string' || !path.trim() || !projectID) return
      const requestedSession = (typeof detail === 'object' && detail?.sessionID) || useChatStore.getState().activeSession[projectID] || 'default'
      const available = sessionsProjectIDRef.current === projectID ? sessionsRef.current : []
      const targetSession = available.some((session) => session.id === requestedSession)
        ? requestedSession
        : (available[0]?.id || requestedSession || 'default')
      setView(targetSession)
      requestAnimationFrame(() => requestAnimationFrame(() => {
        window.dispatchEvent(new CustomEvent('gokin:open-workspace-file', {
          detail: { projectID, sessionID: targetSession, path },
        }))
      }))
    }
    const composeInChatHandler = (e: Event) => {
      const detail = (e as CustomEvent).detail || {}
      const text = typeof detail === 'string' ? detail : detail.text
      if (typeof text !== 'string' || !text.trim()) return
      const mode = detail.mode === 'append' ? 'append' : 'replace'
      const projectID = useProjectStore.getState().activeProjectId
      if (!projectID) return
      const availableSessions = sessionsProjectIDRef.current === projectID ? sessionsRef.current : []
      if (availableSessions.length === 0) {
        // A workspace action can arrive while project A's tabs are being
        // replaced with project B's. Queue the draft against the project ID;
        // the authoritative session load below will deliver it exactly once.
        pendingComposeRef.current = {
          projectID,
          text,
          mode,
          sessionID: typeof detail.sessionID === 'string' ? detail.sessionID : undefined,
        }
        return
      }
      const requestedSession = typeof detail.sessionID === 'string' ? detail.sessionID : null
      const activeSession = projectID
        ? useChatStore.getState().activeSession[projectID]
        : null
      const target = requestedSession && availableSessions.some((session) => session.id === requestedSession)
        ? requestedSession
        : activeSession && availableSessions.some((session) => session.id === activeSession)
          ? activeSession
          : availableSessions[0].id
      setView(target)
      // The chat view may be unmounted while Files/Artifacts/Settings is open.
      // Two animation frames let React commit ChatPanel before delivering the
      // draft event. Nothing is sent automatically.
      requestAnimationFrame(() => requestAnimationFrame(() => {
        window.dispatchEvent(new CustomEvent('gokin:compose-prompt', {
          detail: { text, mode },
        }))
      }))
    }
    // iter 990+: open Settings then defer the open-logs dispatch via rAF so
    // SettingsPage has time to mount and register its listener. detail.level
    // (e.g. 'error') is forwarded so the modal can pre-filter to errors when
    // opened from the status-bar indicator.
    const openLogsHandler = (e: Event) => {
      const detail = (e as CustomEvent).detail || {}
      setView('settings')
      requestAnimationFrame(() => {
        window.dispatchEvent(new CustomEvent('gokin:show-logs', { detail }))
      })
    }
    const renamedHandler = (e: Event) => {
      const { projectID, sessionID, name } = (e as CustomEvent).detail || {}
      if (!sessionID || !name) return
      if (projectID && projectID !== useProjectStore.getState().activeProjectId) return
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
      const requestID = ++sessionListRequestRef.current
      ListChatSessions(pid).then((list) => {
        // Stale-response guard: if the user switched projects while this async
        // call was in flight, drop the result rather than write project A's
        // tabs into project B's tab bar.
        if (requestID !== sessionListRequestRef.current || useProjectStore.getState().activeProjectId !== pid) return
        if (!list || list.length === 0) throw new Error('No chat sessions were returned for this project.')
        const tabs = list.map(toSessionTab)
        const lastSession = useChatStore.getState().activeSession[pid]
        const pending = pendingComposeRef.current
        const target = pending?.projectID === pid && pending.sessionID && tabs.some((tab: SessionTab) => tab.id === pending.sessionID)
          ? pending.sessionID
          : lastSession && tabs.some((tab: SessionTab) => tab.id === lastSession)
            ? lastSession
            : tabs[0].id
        setSessions(tabs)
        setSessionsProjectID(pid)
        setSessionsLoading(false)
        setSessionLoadError(null)
        setView((current) => {
          if (pending?.projectID === pid) return target
          if (current === 'files' || current === 'artifacts' || current === 'settings') return current
          return tabs.some((tab: SessionTab) => tab.id === current) ? current : target
        })
        if (pending?.projectID === pid) {
          pendingComposeRef.current = null
          requestAnimationFrame(() => requestAnimationFrame(() => {
            window.dispatchEvent(new CustomEvent('gokin:compose-prompt', {
              detail: { text: pending.text, mode: pending.mode },
            }))
          }))
        }
      }).catch((error: any) => {
        if (requestID !== sessionListRequestRef.current || useProjectStore.getState().activeProjectId !== pid) return
        const message = String(error?.message || error || 'unknown error')
        if (sessionsProjectIDRef.current !== pid || sessionsRef.current.length === 0) {
          setSessions([])
          setSessionsProjectID(pid)
          setSessionsLoading(false)
          setSessionLoadError(message)
        } else {
          setTabOrderError(`Could not refresh chats: ${message}`)
          setTimeout(() => setTabOrderError(null), 5000)
        }
      })
    }
    window.addEventListener('gokin:open-settings', settingsHandler)
    window.addEventListener('gokin:open-artifact', artifactHandler)
    window.addEventListener('gokin:open-file', fileHandler)
    window.addEventListener('gokin:compose-in-chat', composeInChatHandler)
    window.addEventListener('gokin:open-logs', openLogsHandler)
    window.addEventListener('gokin:switch-tab', switchHandler)
    window.addEventListener('gokin:session-renamed', renamedHandler)
    window.addEventListener('gokin:cycle-session', cycleHandler)
    window.addEventListener('gokin:sessions-changed', sessionsChangedHandler)
    return () => {
      window.removeEventListener('gokin:open-settings', settingsHandler)
      window.removeEventListener('gokin:open-artifact', artifactHandler)
      window.removeEventListener('gokin:open-file', fileHandler)
      window.removeEventListener('gokin:compose-in-chat', composeInChatHandler)
      window.removeEventListener('gokin:open-logs', openLogsHandler)
      window.removeEventListener('gokin:switch-tab', switchHandler)
      window.removeEventListener('gokin:session-renamed', renamedHandler)
      window.removeEventListener('gokin:cycle-session', cycleHandler)
      window.removeEventListener('gokin:sessions-changed', sessionsChangedHandler)
    }
  }, [])

  // Load data
  useEffect(() => {
    let cancelled = false
    setReady(false)
    setBootstrapError(null)

    const required = async <T,>(label: string, load: () => Promise<T>): Promise<T> => {
      try {
        return await load()
      } catch (error: any) {
        const message = String(error?.message || error || 'unknown error').replace(/\s+/g, ' ').trim().slice(0, 400)
        throw new Error(`${label}: ${message}`)
      }
    }

    Promise.all([
      required('Projects', () => ListProjects()),
      required('Settings', () => GetSettings()),
      required('GLM/Kimi model catalog', () => GetProviders()),
      required('Credential sources', () => GetProviderCredentialSources()),
    ]).then(([projectList, config, providerList, credentialSources]) => {
      if (cancelled) return
      const typedProjects = (Array.isArray(projectList) ? projectList : []) as ProjectInfo[]
      if (typedProjects.some((project) => !project?.id || !project?.name || !project?.directory)) {
        throw new Error('Projects: backend returned an incomplete project record.')
      }
      if (!config?.settings) {
        throw new Error('Settings: backend returned no settings payload.')
      }
      const normalizedProviders = normalizeProviderCatalog(Array.isArray(providerList) ? providerList : [])
      const providerIDs = new Set(normalizedProviders.map((provider) => provider.id))
      if (!providerIDs.has('glm') || !providerIDs.has('kimi') || normalizedProviders.some((provider) => provider.models.length === 0)) {
        throw new Error('GLM/Kimi model catalog: required provider models are missing.')
      }

      // Commit the snapshot only after every required RPC and structural gate
      // succeeds. A partial startup can never overwrite the UI with defaults
      // or trigger fresh-install onboarding over existing data.
      const s: any = config.settings
      setProjects(typedProjects)
      setSettings({
        theme: s.theme || 'dark',
        defaultProvider: s.defaultProvider || 'glm',
        defaultModel: s.defaultModel || 'glm-5.2',
        glmKey: s.glmKey || '',
        kimiKey: s.kimiKey || '',
        globalInstructions: s.globalInstructions || '',
        defaultThinkingMode: s.defaultThinkingMode || '',
        defaultThinkingBudget: s.defaultThinkingBudget || 0,
        defaultBudgetUSD: s.defaultBudgetUSD || 0,
        autoCleanupDisabled: !!s.autoCleanupDisabled,
        autoBackupEnabled: !!s.autoBackupEnabled,
        quickEntryEnabled: !!s.quickEntryEnabled,
        quickEntryShortcut: s.quickEntryShortcut || (navigator.platform.toLowerCase().includes('mac') ? 'Double-tap Option' : 'Ctrl+Alt+Space'),
        voiceShortcutEnabled: !!s.voiceShortcutEnabled,
        voiceShortcut: s.voiceShortcut || (navigator.platform.toLowerCase().includes('mac') ? 'Caps Lock' : 'Ctrl+Alt+D'),
        keepAwakeEnabled: !!s.keepAwakeEnabled,
        autoUpdateCheckDisabled: !!s.autoUpdateCheckDisabled,
        autoArchivePRAfterClose: !!s.autoArchivePRAfterClose,
      })
      setProviders(normalizedProviders)
      setProviderCredentialSources((credentialSources || {}) as Record<string, string>)
      if (typedProjects.length > 0 && !useProjectStore.getState().activeProjectId) {
        const savedProjectID = startupContinuityRef.current.activeProjectID
        const restoredProject = savedProjectID && typedProjects.some((project) => project.id === savedProjectID)
          ? savedProjectID
          : typedProjects[0].id
        useProjectStore.getState().setActiveProject(restoredProject)
      } else if (typedProjects.length === 0 && startupContinuityRef.current.lastView === 'settings') {
        setView('settings')
        startupRestorePendingRef.current = false
      }
      setReady(true)
      if (shouldShowOnboarding(typedProjects.length)) setShowOnboarding(true)
    }).catch((error: any) => {
      if (cancelled) return
      setBootstrapError(String(error?.message || error || 'Gokin Studio could not load its local data.'))
      setReady(false)
    })
    return () => { cancelled = true }
  }, [bootstrapAttempt, setProjects, setSettings, setProviders, setProviderCredentialSources])

  // Allow the user to re-trigger the wizard from the command palette only
  // after a trustworthy bootstrap has mounted the workspace.
  useEffect(() => {
    const reshowHandler = () => { if (ready) setShowOnboarding(true) }
    window.addEventListener('gokin:show-onboarding', reshowHandler)
    return () => window.removeEventListener('gokin:show-onboarding', reshowHandler)
  }, [ready])

  // Hooks before early return
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projects = useProjectStore((s) => s.projects)
  const sessionActive = useChatStore((s) => s.sessionActive)
  const unread = useChatStore((s) => s.unread)
  const clearUnread = useChatStore((s) => s.clearUnread)

  useEffect(() => {
    if (pendingQuickEntryAction && pendingQuickEntryAction.projectID !== activeProjectId) {
      setPendingQuickEntryAction(null)
    }
  }, [activeProjectId, pendingQuickEntryAction])

  useEffect(() => {
    if (!ready) return
    persistActiveProject(activeProjectId)
  }, [activeProjectId, ready])

  useEffect(() => {
    if (!ready || view === 'default') return
    if (view === 'settings') {
      persistWorkspaceLocation(activeProjectId, 'settings')
      return
    }
    if (!activeProjectId) return
    if (view === 'files' || view === 'artifacts') {
      persistWorkspaceLocation(activeProjectId, view)
      return
    }
    if (sessionsProjectID === activeProjectId && sessions.some((session) => session.id === view)) {
      persistWorkspaceLocation(activeProjectId, 'chat', view)
    }
  }, [activeProjectId, ready, sessions, sessionsProjectID, view])

  // Keep a bounded, project-local navigation trail across chat sessions and
  // standalone workspace views. Project switches clear the trail so Back can
  // never surface a chat ID from another workspace. History replay mutates the
  // cursor without appending another entry, matching native desktop behavior.
  useEffect(() => {
    const projectKey = activeProjectId || '__no_project__'
    const history = navigationHistoryRef.current
    if (history.projectKey !== projectKey) {
      navigationHistoryRef.current = { projectKey, entries: [], index: -1 }
      navigationReplayRef.current = false
      setNavigationRevision((revision) => revision + 1)
      return
    }
    if (view === 'default') return
    if (navigationReplayRef.current) {
      navigationReplayRef.current = false
      return
    }
    if (history.entries[history.index] === view) return
    const entries = [...history.entries.slice(0, history.index + 1), view].slice(-50)
    navigationHistoryRef.current = { projectKey, entries, index: entries.length - 1 }
    setNavigationRevision((revision) => revision + 1)
  }, [activeProjectId, view])

  useEffect(() => {
    const off = EventsOn('quick-entry:show', (payload?: { mode?: string }) => {
      void enterQuickEntry(payload?.mode === 'voice' ? 'voice' : 'text')
    })
    return off
  }, [enterQuickEntry])

  const applyDeepLink = useCallback(async (link: StudioDeepLink) => {
    const fail = (message: string) => {
      setDeepLinkError(message)
      window.setTimeout(() => setDeepLinkError(null), 6000)
    }
    const availableProjects = useProjectStore.getState().projects
    const requestedProjectID = link.projectID || useProjectStore.getState().activeProjectId || availableProjects[0]?.id
    if (!requestedProjectID || !availableProjects.some((project) => project.id === requestedProjectID)) {
      fail(link.projectID ? 'The project in this gokin:// link is not available.' : 'Add a project before opening this gokin:// link.')
      return
    }

    await exitQuickEntry(true)
    const switchProject = useProjectStore.getState().activeProjectId !== requestedProjectID
    useProjectStore.getState().setActiveProject(requestedProjectID)

    const deliverPrompt = (sessionID: string, prompt?: string, mode: 'replace' | 'append' = 'replace') => {
      useChatStore.getState().setActiveSession(requestedProjectID, sessionID)
      setView(sessionID)
      if (!prompt?.trim()) {
        requestAnimationFrame(() => requestAnimationFrame(() => {
          window.dispatchEvent(new CustomEvent('gokin:focus-composer'))
        }))
        return
      }
      requestAnimationFrame(() => requestAnimationFrame(() => {
        window.dispatchEvent(new CustomEvent('gokin:compose-prompt', {
          detail: { text: prompt, mode },
        }))
      }))
    }

    if (link.action === 'project') {
      const targetView = link.view === 'files' || link.view === 'artifacts' ? link.view : 'chat'
      if (switchProject || sessionsProjectIDRef.current !== requestedProjectID) {
        pendingWorkspaceViewRef.current = { projectID: requestedProjectID, view: targetView }
        setView('default')
      } else if (targetView === 'chat') {
        const activeSession = useChatStore.getState().activeSession[requestedProjectID]
        const target = activeSession && sessionsRef.current.some((session) => session.id === activeSession)
          ? activeSession
          : sessionsRef.current[0]?.id
        if (target) deliverPrompt(target)
      } else {
        setView(targetView)
      }
      return
    }

    if (link.action === 'new') {
      let info: any
      try {
        info = await CreateChatSession(requestedProjectID)
      } catch (error: any) {
        fail(`Could not create the linked chat: ${String(error?.message || error || 'unknown error')}`)
        return
      }
      if (!info?.id || useProjectStore.getState().activeProjectId !== requestedProjectID) return
      const session = toSessionTab(info)
      useChatStore.getState().setActiveSession(requestedProjectID, session.id)
      if (!switchProject && sessionsProjectIDRef.current === requestedProjectID && sessionsRef.current.length > 0) {
        setSessions((previous) => previous.some((item) => item.id === session.id) ? previous : [...previous, session])
        deliverPrompt(session.id, link.prompt)
      } else {
        if (link.prompt?.trim()) pendingComposeRef.current = { projectID: requestedProjectID, text: link.prompt, mode: 'replace' }
        pendingWorkspaceViewRef.current = { projectID: requestedProjectID, view: 'chat' }
        setView('default')
        setSessionReloadNonce((nonce) => nonce + 1)
      }
      return
    }

    if (link.action === 'chat' && link.sessionID) {
      let catalog: any[]
      try {
        catalog = await ListChatSessions(requestedProjectID) as any[]
      } catch (error: any) {
        fail(`Could not open the linked chat: ${String(error?.message || error || 'unknown error')}`)
        return
      }
      if (!catalog.some((session) => String(session?.id || '') === link.sessionID)) {
        fail('The chat in this gokin:// link no longer exists.')
        return
      }
      useChatStore.getState().setActiveSession(requestedProjectID, link.sessionID)
      if (!switchProject && sessionsProjectIDRef.current === requestedProjectID && sessionsRef.current.length > 0) {
        setSessions(catalog.map(toSessionTab))
        // Existing chats may already have an unsent draft. Append linked text
        // instead of destroying that local work; new-chat links still replace
        // because their composer is newly created and empty.
        deliverPrompt(link.sessionID, link.prompt, 'append')
      } else {
        if (link.prompt?.trim()) pendingComposeRef.current = { projectID: requestedProjectID, text: link.prompt, mode: 'append' }
        pendingWorkspaceViewRef.current = { projectID: requestedProjectID, view: 'chat' }
        setView('default')
        setSessionReloadNonce((nonce) => nonce + 1)
      }
    }
  }, [exitQuickEntry])

  useEffect(() => {
    if (!ready) return
    let disposed = false
    const enqueue = (payload?: StudioDeepLink) => {
      if (disposed || !payload || !Number.isFinite(payload.sequence)) return
      deepLinkChainRef.current = deepLinkChainRef.current
        .then(() => applyDeepLink(payload))
        .catch((error) => {
          console.error('Could not apply gokin:// navigation:', error)
          setDeepLinkError('Could not open the gokin:// link.')
          window.setTimeout(() => setDeepLinkError(null), 6000)
        })
    }
    const off = EventsOn('deep-link:open', enqueue)
    StartDeepLinkEvents()
      .then((pending) => { for (const link of pending || []) enqueue(link as StudioDeepLink) })
      .catch((error) => console.error('Could not start gokin:// navigation:', error))
    return () => {
      disposed = true
      off()
    }
  }, [applyDeepLink, ready])

  useEffect(() => {
    if (!ready) return
    let disposed = false
    const apply = (status?: DesktopUpdateStatus) => {
      if (disposed || !status?.available || !status.latestVersion || !status.releaseURL) return
      setDesktopUpdate(status)
    }
    const off = EventsOn('app:update_available', apply)
    CheckForUpdatesIfDue()
      .then((status) => apply(status as DesktopUpdateStatus))
      .catch((error) => console.warn('Automatic update check failed:', error))
    return () => {
      disposed = true
      off()
    }
  }, [ready])

  useEffect(() => {
    if (!ready) return
    const off = EventsOn('app:open_update_center', () => {
      setView('settings')
      requestAnimationFrame(() => requestAnimationFrame(() => {
        document.getElementById('settings-about')?.scrollIntoView({ block: 'start' })
        window.dispatchEvent(new CustomEvent('gokin:check-updates'))
      }))
    })
    return off
  }, [ready])

  // Clear the unread badge whenever the user lands on a chat tab. Keyed on
  // (activeProjectId, view) so switching projects, switching tabs, OR newly
  // mounting all clear the corresponding badge. We use the chatStore's
  // setActiveSession to also keep the per-project active session in sync,
  // which ChatPanel and FileBrowser both read.
  useEffect(() => {
    if (!activeProjectId) return
    // Standalone workspace views aren't chat sessions.
    if (view === 'files' || view === 'artifacts' || view === 'settings') return
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

  useEffect(() => {
    if (tabCtxMenu) {
      tabCtxMenuWasOpenRef.current = true
      requestAnimationFrame(() => tabCtxMenuRef.current?.querySelector<HTMLButtonElement>('button:not([disabled])')?.focus())
      return
    }
    if (!tabCtxMenuWasOpenRef.current) return
    tabCtxMenuWasOpenRef.current = false
    requestAnimationFrame(() => tabCtxMenuTriggerRef.current?.focus())
  }, [tabCtxMenu])

  useEffect(() => {
    if (showSessionSwitcher) {
      sessionSwitcherWasOpenRef.current = true
      requestAnimationFrame(() => sessionSwitcherRef.current?.querySelector<HTMLInputElement>('input')?.focus())
      return
    }
    if (!sessionSwitcherWasOpenRef.current) return
    sessionSwitcherWasOpenRef.current = false
    requestAnimationFrame(() => sessionSwitcherTriggerRef.current?.focus())
  }, [showSessionSwitcher])

  useEffect(() => {
    if (!showSessionSwitcher) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229 || event.key !== 'Escape') return
      event.preventDefault()
      setShowSessionSwitcher(false)
      setSessionSwitcherQuery('')
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [showSessionSwitcher])

  useEffect(() => {
    if (!showArchivedSessions) return
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229 || event.key !== 'Escape') return
      event.preventDefault()
      setShowArchivedSessions(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [showArchivedSessions])

  useEffect(() => {
    if (!showSessionSwitcher) return
    if (sessions.length <= 1 || showPalette) {
      setShowSessionSwitcher(false)
      setSessionSwitcherQuery('')
    }
  }, [sessions.length, showPalette, showSessionSwitcher])

  useEffect(() => {
    if (!showSessionSwitcher) return
    setShowSessionSwitcher(false)
    setSessionSwitcherQuery('')
    // Deliberately keyed only to workspace navigation. Opening the switcher
    // itself does not change view, while global shortcuts do.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view])

  // A selected chat may be outside the invisible horizontal overflow after a
  // palette/search/shortcut switch. Reveal it without moving the page, so the
  // strip always communicates which conversation is active.
  useEffect(() => {
    if (view === 'files' || view === 'artifacts' || view === 'settings' || view === 'default') return
    const frame = requestAnimationFrame(() => {
      const tabs = Array.from(tabsLeftRef.current?.querySelectorAll<HTMLButtonElement>('button.view-tab-main') || [])
      tabs.find((tab) => tab.dataset.sessionId === view)?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    })
    return () => cancelAnimationFrame(frame)
  }, [sessions, view])

  // Load sessions when project changes
  useEffect(() => {
    const requestID = ++sessionListRequestRef.current
    setShowSessionSwitcher(false)
    setSessionSwitcherQuery('')
    setShowArchivedSessions(false)
    setArchivedSessions([])
    setArchivedSessionsError(null)
    setArchivedSessionBusy(null)
    setFileArtifactSelection(null)
    setLibraryArtifactSelection(null)
    setTabCtxMenu(null)
    setSessions([])
    setSessionsProjectID(activeProjectId || null)
    setSessionLoadError(null)
    if (!activeProjectId) {
      setSessionsLoading(false)
      pendingComposeRef.current = null
      pendingWorkspaceViewRef.current = null
      startupRestorePendingRef.current = false
      return
    }
    if (pendingComposeRef.current && pendingComposeRef.current.projectID !== activeProjectId) {
      pendingComposeRef.current = null
    }
    if (pendingWorkspaceViewRef.current && pendingWorkspaceViewRef.current.projectID !== activeProjectId) {
      pendingWorkspaceViewRef.current = null
    }
    const projectID = activeProjectId
    setSessionsLoading(true)
    let cancelled = false
    ListChatSessions(projectID).then((list) => {
      if (cancelled || requestID !== sessionListRequestRef.current || useProjectStore.getState().activeProjectId !== projectID) return
      if (!list || list.length === 0) {
        throw new Error('No chat sessions were returned for this project.')
      }
      const tabs = list.map(toSessionTab)
      const continuity = readWorkspaceContinuity()
      const savedLocation = continuity.projects[projectID]
      const lastSession = useChatStore.getState().activeSession[projectID]
      const pending = pendingComposeRef.current
      const target = pending?.projectID === projectID && pending.sessionID && tabs.some((tab: SessionTab) => tab.id === pending.sessionID)
        ? pending.sessionID
        : lastSession && tabs.some((tab: SessionTab) => tab.id === lastSession)
          ? lastSession
          : savedLocation?.sessionID && tabs.some((tab: SessionTab) => tab.id === savedLocation.sessionID)
            ? savedLocation.sessionID
            : tabs[0].id
      const requestedView = pendingWorkspaceViewRef.current?.projectID === projectID
        ? pendingWorkspaceViewRef.current.view
        : null
      const restoreStartupView = startupRestorePendingRef.current
      startupRestorePendingRef.current = false
      pendingWorkspaceViewRef.current = null
      setSessions(tabs)
      setSessionsProjectID(projectID)
      setSessionLoadError(null)
      setView((current) => {
        if (pending?.projectID === projectID) return target
        if (requestedView === 'chat') return target
        if (requestedView === 'files' || requestedView === 'artifacts') return requestedView
        if (restoreStartupView && continuity.lastView === 'settings') return 'settings'
        if (current === 'files' || current === 'artifacts' || current === 'settings') return current
        if (savedLocation?.view === 'files' || savedLocation?.view === 'artifacts') return savedLocation.view
        return target
      })

      if (pending?.projectID === projectID) {
        pendingComposeRef.current = null
        requestAnimationFrame(() => requestAnimationFrame(() => {
          window.dispatchEvent(new CustomEvent('gokin:compose-prompt', {
            detail: { text: pending.text, mode: pending.mode },
          }))
        }))
      }
    }).catch((error: any) => {
      if (cancelled || requestID !== sessionListRequestRef.current || useProjectStore.getState().activeProjectId !== projectID) return
      setSessions([])
      setSessionLoadError(String(error?.message || error || 'Could not load chats'))
    }).finally(() => {
      if (!cancelled && requestID === sessionListRequestRef.current && useProjectStore.getState().activeProjectId === projectID) {
        setSessionsLoading(false)
      }
    })
    return () => { cancelled = true }
  }, [activeProjectId, sessionReloadNonce])

  // Track the active session per project so components like FileBrowser can route to the right session.
  useEffect(() => {
    if (!activeProjectId) return
    const isChat = view !== 'files' && view !== 'artifacts' && view !== 'settings'
    if (isChat && sessionsProjectID === activeProjectId && sessions.some((session) => session.id === view)) {
      useChatStore.getState().setActiveSession(activeProjectId, view)
    }
  }, [activeProjectId, sessions, sessionsProjectID, view])

  const handleNewChat = useCallback(async () => {
    // Read from store instead of closure so the callback stays stable and
    // keyboard shortcuts (Cmd/Ctrl+N and the Ctrl+T alias) work even after the active project changes.
    const id = useProjectStore.getState().activeProjectId
    if (!id || creatingChatRef.current) return
    if (sessionsProjectIDRef.current !== id || sessionsRef.current.length === 0) {
      setNewChatError('Wait for this project’s chats to finish loading.')
      setTimeout(() => setNewChatError(null), 3000)
      return
    }
    creatingChatRef.current = true
    try {
      const info = await CreateChatSession(id) as any
      if (info && useProjectStore.getState().activeProjectId === id && sessionsProjectIDRef.current === id) {
        setSessions((prev) => [...prev, toSessionTab(info)])
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

  const openQuickEntrySession = useCallback((sessionID: string, action?: QuickEntryComposerAction) => {
    const projectID = useProjectStore.getState().activeProjectId
    if (!projectID || !sessionsRef.current.some((session) => session.id === sessionID)) return
    const animatedWindowRestore = !quickEntryNativeRef.current && (!!quickEntryWindowRef.current?.fullscreen || !!quickEntryWindowRef.current?.maximised)
    useChatStore.getState().setActiveSession(projectID, sessionID)
    setView(sessionID)
    const restored = exitQuickEntry(true)
    const deliverAction = () => {
      if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) return
      if (action) {
        const sequence = ++quickEntryActionSequenceRef.current
        setPendingQuickEntryAction({
          id: `${projectID}:${sessionID}:${sequence}`,
          projectID,
          sessionID,
          action,
        })
      } else {
        window.dispatchEvent(new CustomEvent('gokin:focus-composer'))
      }
    }
    void restored.finally(() => requestAnimationFrame(() => requestAnimationFrame(() => {
      if (animatedWindowRestore) window.setTimeout(deliverAction, 450)
      else deliverAction()
    })))
  }, [exitQuickEntry])

  const handleQuickEntryActionHandled = useCallback((id: string) => {
    setPendingQuickEntryAction((current) => current?.id === id ? null : current)
  }, [])

  const createQuickEntrySession = useCallback(async (draft: string, sourceSessionID: string) => {
    const projectID = useProjectStore.getState().activeProjectId
    if (!projectID) throw new Error('No active project')
    if (sessionsProjectIDRef.current !== projectID || sessionsRef.current.length === 0) {
      throw new Error('Wait for this project’s chats to finish loading.')
    }
    const info = await CreateChatSession(projectID) as any
    if (!info?.id) throw new Error('The new chat did not return an ID.')
    if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) {
      throw new Error('The active project changed while creating the chat.')
    }
    const session = toSessionTab(info)
    setSessions((previous) => [...previous, session])
    if (sourceSessionID && sourceSessionID !== session.id) {
      useChatStore.getState().setDraft(`${projectID}_${sourceSessionID}`, '')
      await SaveDraft(projectID, sourceSessionID, '').catch((error) => {
        console.warn('Quick Entry source draft could not be cleared from disk:', error)
      })
    }
    const chatKey = `${projectID}_${session.id}`
    useChatStore.getState().setDraft(chatKey, draft)
    if (draft.trim()) {
      await SaveDraft(projectID, session.id, draft).catch((error) => {
        console.warn('Quick Entry draft remains in memory after disk save failed:', error)
      })
    }
    useChatStore.getState().setActiveSession(projectID, session.id)
    setView(session.id)
    await exitQuickEntry(true)
    requestAnimationFrame(() => requestAnimationFrame(() => {
      window.dispatchEvent(new CustomEvent('gokin:focus-composer'))
    }))
  }, [exitQuickEntry])

  useEffect(() => {
    const handler = () => { void handleNewChat() }
    window.addEventListener('gokin:new-chat', handler)
    return () => window.removeEventListener('gokin:new-chat', handler)
  }, [handleNewChat])

  const applyNativeCommand = useCallback((payload?: string | { command?: string }) => {
    const command = typeof payload === 'string' ? payload : payload?.command
    if (!command) return

    // Native menu actions must not navigate behind an approval or another
    // modal task. Bring the existing dialog back into focus instead.
    if (hasOpenModal()) {
      document.querySelector<HTMLElement>('[aria-modal="true"]')?.focus()
      return
    }

    const projectID = useProjectStore.getState().activeProjectId
    const needsLoadedChat = command === 'new-chat' || command === 'close-chat' || command === 'chat' || command === 'find-chat' || command === 'search-all' || command === 'cycle-transcript-mode' || command === 'side-chat' || command === 'diff' || command === 'preview' || command === 'select-preview-element'
    if (needsLoadedChat && projectID && (sessionsProjectIDRef.current !== projectID || sessionsRef.current.length === 0)) {
      const pending = pendingNativeSessionCommandsRef.current
      if (!pending.some((entry) => entry.projectID === projectID && entry.command === command)) {
        pending.push({ projectID, command })
        if (pending.length > 16) pending.splice(0, pending.length - 16)
      }
      return
    }

    switch (command) {
      case 'new-chat':
        if (projectID) void handleNewChat()
        else window.dispatchEvent(new CustomEvent('gokin:add-project'))
        break
      case 'close-chat':
        if (projectID) window.dispatchEvent(new CustomEvent('gokin:archive-active-session'))
        break
      case 'add-project':
        window.dispatchEvent(new CustomEvent('gokin:add-project'))
        break
      case 'command-palette':
        setShowPalette(true)
        break
      case 'chat':
      case 'files':
      case 'artifacts':
        window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: command }))
        break
      case 'settings':
        window.dispatchEvent(new CustomEvent('gokin:open-settings'))
        break
      case 'find-chat':
        if (projectID) routeLoadedChatTool('gokin:open-chat-search')
        break
      case 'search-all':
        if (projectID) routeLoadedChatTool('gokin:open-global-search')
        break
      case 'cycle-transcript-mode':
        if (projectID) routeLoadedChatTool('gokin:cycle-transcript-mode')
        break
      case 'side-chat':
        if (projectID) routeLoadedChatTool('gokin:toggle-side-chat')
        break
      case 'diff':
        if (projectID) routeLoadedChatTool('gokin:toggle-diff-pane')
        break
      case 'preview':
        if (projectID) routeLoadedChatTool('gokin:toggle-live-preview')
        break
      case 'select-preview-element':
        if (projectID) routeLoadedChatTool('gokin:select-preview-element')
        break
      case 'back':
        navigateHistory(-1)
        break
      case 'forward':
        navigateHistory(1)
        break
      case 'toggle-sidebar':
        toggleSidebar()
        break
      case 'help':
        window.dispatchEvent(new CustomEvent('gokin:open-settings', { detail: { section: 'settings-about' } }))
        break
    }
  }, [handleNewChat, navigateHistory, routeLoadedChatTool, toggleSidebar])
  nativeCommandHandlerRef.current = applyNativeCommand

  useEffect(() => {
    const projectID = useProjectStore.getState().activeProjectId
    if (!projectID || sessionsProjectID !== projectID || sessions.length === 0) return
    const deliver = pendingNativeSessionCommandsRef.current.filter((entry) => entry.projectID === projectID)
    // Commands belong to the project that was active when the menu item was
    // chosen; switching projects cancels stale queued navigation.
    pendingNativeSessionCommandsRef.current = []
    for (const entry of deliver) applyNativeCommand(entry.command)
  }, [activeProjectId, applyNativeCommand, sessions.length, sessionsProjectID])

  useEffect(() => {
    const receive = (payload?: string | { command?: string }) => {
      if (!nativeFrontendReadyRef.current || nativeEventsStartingRef.current) {
        const pending = pendingNativeBootstrapCommandsRef.current
        pending.push(payload || '')
        if (pending.length > 32) pending.splice(0, pending.length - 32)
        return
      }
      nativeCommandHandlerRef.current(payload)
    }
    const off = EventsOn('app:native_command', receive)
    return off
  }, [])

  useEffect(() => {
    if (!ready) return
    const flushFrontendQueue = () => {
      const pending = pendingNativeBootstrapCommandsRef.current.splice(0)
      for (const command of pending) nativeCommandHandlerRef.current(command)
    }
    if (nativeEventsStartedRef.current) {
      flushFrontendQueue()
      return
    }
    if (nativeEventsStartingRef.current) return
    nativeEventsStartingRef.current = true
    StartNativeMenuEvents()
      .then((pending) => {
        // Backend cold-start commands predate any live events buffered by the
        // permanent listener, so preserve that order during the hand-off.
        for (const command of pending || []) nativeCommandHandlerRef.current(command)
        flushFrontendQueue()
        nativeEventsStartedRef.current = true
      })
      .catch((error) => console.error('Could not start native menu commands:', error))
      .finally(() => { nativeEventsStartingRef.current = false })
  }, [ready])

  const commitRename = async (sessionId: string, name: string) => {
    const trimmed = name.trim()
    setRenamingId(null)
    if (!trimmed || !activeProjectId) return
    const projectID = activeProjectId
    const prev = sessions.find((s) => s.id === sessionId)?.name
    if (trimmed === prev) return
    // Optimistic update
    setSessions((s) => s.map((t) => (t.id === sessionId ? { ...t, name: trimmed } : t)))
    try {
      await RenameChatSession(projectID, sessionId, trimmed)
    } catch (e) {
      console.error('RenameChatSession error:', e)
      if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) return
      // Revert on failure and briefly show error badge on the tab.
      if (prev) {
        setSessions((s) => s.map((t) => (t.id === sessionId ? { ...t, name: prev } : t)))
      }
      setRenameError(sessionId)
      setTimeout(() => setRenameError((cur) => (cur === sessionId ? null : cur)), 3000)
    }
  }

  const handleArchiveChat = useCallback(async (sessionId: string) => {
    const projectID = useProjectStore.getState().activeProjectId
    const currentSessions = sessionsRef.current
    if (!projectID || sessionsProjectIDRef.current !== projectID || currentSessions.length <= 1 || !currentSessions.some((session) => session.id === sessionId)) return
    const operationKey = `${projectID}:${sessionId}`
    if (archivingSessionIDsRef.current.has(operationKey)) return
    archivingSessionIDsRef.current.add(operationKey)
    setCloseError((current) => current?.id === sessionId ? null : current)
    try {
      await ArchiveChatSession(projectID, sessionId)
      if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) return
      setSessions((previous) => {
        const remaining = previous.filter((session) => session.id !== sessionId)
        if (viewRef.current === sessionId && remaining.length > 0) {
          const archivedIndex = previous.findIndex((session) => session.id === sessionId)
          const nextIndex = Math.max(0, Math.min(archivedIndex - 1, remaining.length - 1))
          setView(remaining[nextIndex].id)
        }
        return remaining
      })
    } catch (error: any) {
      console.error('ArchiveChatSession error:', error)
      if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) return
      setCloseError({ id: sessionId, message: String(error?.message || error || 'Could not archive chat') })
      setTimeout(() => setCloseError((current) => current?.id === sessionId ? null : current), 6000)
    } finally {
      archivingSessionIDsRef.current.delete(operationKey)
    }
  }, [])

  useEffect(() => {
    const archiveActiveSession = () => {
      if (quickEntryOpenRef.current || hasOpenModal()) return
      const projectID = useProjectStore.getState().activeProjectId
      if (!projectID || sessionsProjectIDRef.current !== projectID) return
      const currentSessions = sessionsRef.current
      const target = resolveCloseChatSession(
        viewRef.current,
        useChatStore.getState().activeSession[projectID],
        currentSessions.map((session) => session.id),
      )
      if (target) void handleArchiveChat(target)
    }
    window.addEventListener('gokin:archive-active-session', archiveActiveSession)
    return () => window.removeEventListener('gokin:archive-active-session', archiveActiveSession)
  }, [handleArchiveChat])

  const openArchivedSessions = useCallback(async (requestedProjectID?: string) => {
    const projectID = requestedProjectID || activeProjectId
    if (!projectID) return
    if (projectID !== useProjectStore.getState().activeProjectId) {
      useProjectStore.getState().setActiveProject(projectID)
    }
    setShowArchivedSessions(true)
    setArchivedSessionsLoading(true)
    setArchivedSessionsError(null)
    try {
      const list = await ListArchivedChatSessions(projectID)
      if (useProjectStore.getState().activeProjectId !== projectID) return
      setArchivedSessions((list || []).map(toSessionTab))
    } catch (error: any) {
      if (useProjectStore.getState().activeProjectId !== projectID) return
      setArchivedSessionsError(String(error?.message || error || 'Could not load archived chats'))
    } finally {
      if (useProjectStore.getState().activeProjectId === projectID) setArchivedSessionsLoading(false)
    }
  }, [activeProjectId])

  useEffect(() => {
    const onOpenArchivedChats = (event: Event) => {
      const projectID = String((event as CustomEvent).detail?.projectID || '')
      void openArchivedSessions(projectID || undefined)
    }
    window.addEventListener('gokin:open-archived-chats', onOpenArchivedChats)
    return () => window.removeEventListener('gokin:open-archived-chats', onOpenArchivedChats)
  }, [openArchivedSessions])

  const handleRestoreArchivedChat = async (sessionId: string) => {
    if (!activeProjectId || archivedSessionBusy) return
    const projectID = activeProjectId
    setArchivedSessionBusy(sessionId)
    setArchivedSessionsError(null)
    try {
      await RestoreChatSession(projectID, sessionId)
      const [activeList, archivedList] = await Promise.all([
        ListChatSessions(projectID),
        ListArchivedChatSessions(projectID),
      ])
      if (useProjectStore.getState().activeProjectId !== projectID) return
      setSessions((activeList || []).map(toSessionTab))
      setArchivedSessions((archivedList || []).map(toSessionTab))
      setShowArchivedSessions(false)
      setView(sessionId)
    } catch (error: any) {
      if (useProjectStore.getState().activeProjectId !== projectID) return
      setArchivedSessionsError(String(error?.message || error || 'Could not restore chat'))
    } finally {
      if (useProjectStore.getState().activeProjectId === projectID) setArchivedSessionBusy(null)
    }
  }

  const handleDeleteArchivedChat = async (session: SessionTab) => {
    if (!activeProjectId || archivedSessionBusy) return
    const projectID = activeProjectId
    let deleteMessage = 'This permanently removes the archived conversation, its draft, and local recovery data. This cannot be undone.'
    if (session.worktreeIsolated || session.worktreeError) {
      try {
        const status = await GetSessionWorktreeStatus(projectID, session.id) as any
        if (status?.error) throw new Error(`Worktree unavailable: ${status.error}`)
        if (status?.dirty) {
          const count = Number(status.changedFiles) || 0
          throw new Error(`Commit or discard ${count} uncommitted worktree change${count === 1 ? '' : 's'} before deleting this chat.`)
        }
        const commits = Number(status?.commitsAhead) || 0
        if (commits > 0) {
          deleteMessage = `This permanently removes the archived conversation and its isolated checkout. The branch “${status.branch || session.worktreeBranch || 'session branch'}” is kept because it has ${commits} new commit${commits === 1 ? '' : 's'}.`
        }
      } catch (error: any) {
        setArchivedSessionsError(String(error?.message || error || 'Could not inspect the session worktree'))
        return
      }
    }
    const accepted = await requestConfirmation({
      title: `Permanently delete “${session.name}”?`,
      message: deleteMessage,
      confirmLabel: 'Delete permanently',
      danger: true,
    })
    if (!accepted) return
    setArchivedSessionBusy(session.id)
    setArchivedSessionsError(null)
    try {
      await DeleteChatSession(projectID, session.id)
      useChatStore.getState().dropSession(projectID + '_' + session.id)
      if (useProjectStore.getState().activeProjectId !== projectID) return
      setArchivedSessions((current) => current.filter((item) => item.id !== session.id))
    } catch (error: any) {
      if (useProjectStore.getState().activeProjectId !== projectID) return
      setArchivedSessionsError(String(error?.message || error || 'Could not permanently delete chat'))
    } finally {
      if (useProjectStore.getState().activeProjectId === projectID) setArchivedSessionBusy(null)
    }
  }

  const handleDeleteChat = async (sessionId: string) => {
    if (!activeProjectId) return
	setCloseError((current) => current?.id === sessionId ? null : current)
    // Guard at the UI layer too — backend rejects deleting the last session,
    // but erroring out here gives the user immediate feedback.
    if (sessions.length <= 1) return
    const projectID = activeProjectId
    const session = sessions.find((item) => item.id === sessionId)
    const sessionName = session?.name || 'this chat'
    let deleteMessage = 'This permanently removes the conversation, its draft, and local recovery data. Project files are not affected.'
    if (session?.worktreeIsolated || session?.worktreeError) {
      try {
        const status = await GetSessionWorktreeStatus(projectID, sessionId) as any
        if (status?.error) {
          setCloseError({ id: sessionId, message: `Worktree unavailable: ${status.error}` })
          return
        }
        if (status?.dirty) {
          const count = Number(status.changedFiles) || 0
          setCloseError({
            id: sessionId,
            message: `Commit or discard ${count} uncommitted worktree change${count === 1 ? '' : 's'} before deleting this chat.`,
          })
          return
        }
        const commits = Number(status?.commitsAhead) || 0
        deleteMessage = commits > 0
          ? `This removes the conversation and its isolated checkout. The branch “${status.branch || session.worktreeBranch || 'session branch'}” is kept because it has ${commits} new commit${commits === 1 ? '' : 's'}.`
          : 'This removes the conversation, its clean isolated checkout, and its temporary branch. Project files in other chats are not affected.'
      } catch (error: any) {
        setCloseError({ id: sessionId, message: String(error?.message || error || 'Could not inspect the session worktree.') })
        return
      }
    }
    const accepted = await requestConfirmation({
      title: `Delete “${sessionName}”?`,
      message: deleteMessage,
      confirmLabel: 'Delete chat',
      danger: true,
    })
    if (!accepted) return
    try {
      await DeleteChatSession(projectID, sessionId)
      const chatKey = projectID + '_' + sessionId
      useChatStore.getState().dropSession(chatKey)

      if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) return

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
      if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) return
      setCloseError({ id: sessionId, message: String(e?.message || e || 'Could not delete chat') })
      setTimeout(() => setCloseError((cur) => (cur?.id === sessionId ? null : cur)), 6000)
    }
  }

  const reorderSessionTabs = async (sourceID: string, beforeID?: string) => {
    if (!activeProjectId || tabOrderSaving) return
    const projectID = activeProjectId
    const previous = sessions
    const fromIndex = previous.findIndex((session) => session.id === sourceID)
    if (fromIndex < 0) return
    const moved = previous[fromIndex]
    const next = previous.filter((session) => session.id !== sourceID)
    if (beforeID) {
      const targetIndex = next.findIndex((session) => session.id === beforeID)
      if (targetIndex < 0) return
      next.splice(targetIndex, 0, moved)
    } else {
      next.push(moved)
    }
    if (next.every((session, index) => session.id === previous[index]?.id)) return

    setTabOrderError(null)
    setTabOrderSaving(true)
    setSessions(next)
    try {
      await ReorderChatSessions(projectID, next.map((session) => session.id))
    } catch (error: any) {
      console.error('ReorderChatSessions failed:', error)
      // Do not overwrite a project/session refresh that happened while the
      // RPC was in flight. Roll back only the exact optimistic snapshot.
      if (useProjectStore.getState().activeProjectId === projectID) {
        setSessions((current) => current.length === next.length && current.every((session, index) => session.id === next[index].id)
          ? previous
          : current)
        setTabOrderError(String(error?.message || error || 'Could not save tab order'))
        setTimeout(() => setTabOrderError(null), 5000)
      }
    } finally {
      setTabOrderSaving(false)
    }
  }

  if (!ready) {
    return (
      <div className="app loading">
        <div className={`loading-content ${bootstrapError ? 'has-error' : ''}`} role={bootstrapError ? 'alert' : 'status'} aria-live="polite">
          {bootstrapError ? (
            <>
              <div className="loading-error-icon"><AlertTriangle size={22} /></div>
              <div className="loading-brand">Couldn’t open Gokin Studio</div>
              <p className="loading-error-message">{bootstrapError}</p>
              <p className="loading-error-hint">No projects or settings were replaced. Retry after checking local storage access and the application logs.</p>
              <button className="btn-primary loading-retry" type="button" onClick={() => setBootstrapAttempt((attempt) => attempt + 1)}>
                <RefreshCw size={13} /> Retry startup
              </button>
            </>
          ) : (
            <>
              <div className="loading-brand">Gokin Studio</div>
              <div className="loading-text">Loading projects, settings, and GLM/Kimi models…</div>
            </>
          )}
        </div>
      </div>
    )
  }

  const isChat = view !== 'files' && view !== 'artifacts' && view !== 'settings'
  const effectiveSidebarCollapsed = compactViewport ? !sidebarDrawerOpen : sidebarCollapsed
  const activeProject = projects.find((p) => p.id === activeProjectId)
  const activeSessionTab = sessions.find((session) => session.id === view)
  const sessionCatalogReady = !!activeProjectId &&
    sessionsProjectID === activeProjectId &&
    !sessionsLoading &&
    !sessionLoadError &&
    sessions.length > 0
  const normalizedSessionQuery = sessionSwitcherQuery.trim().toLowerCase()
  const switcherSessions = normalizedSessionQuery
    ? sessions.filter((session) => (
      session.name.toLowerCase().includes(normalizedSessionQuery) ||
      (session.parentName || '').toLowerCase().includes(normalizedSessionQuery)
    ))
    : sessions
  const topBarTitle = !activeProjectId
    ? '' // welcome/empty state — don't imply an active chat
    : view === 'files'
    ? 'Files'
    : view === 'artifacts'
    ? 'Artifacts'
    : view === 'settings'
    ? 'Settings'
    : activeProjectId && !sessionCatalogReady
    ? sessionsLoading ? 'Loading chats…' : 'Chats unavailable'
    : (sessions.find((s) => s.id === view)?.name || 'Chat')
  const canNavigateBack = findNavigationTarget(-1) >= 0
  const canNavigateForward = findNavigationTarget(1) >= 0
  const storedActiveSession = activeProjectId ? useChatStore.getState().activeSession[activeProjectId] : null
  const chatTabStop = sessions.some((session) => session.id === view)
    ? view
    : storedActiveSession && sessions.some((session) => session.id === storedActiveSession)
      ? storedActiveSession
      : sessions[0]?.id

  if (quickEntryOpen) {
    const quickEntrySessions = sessionsProjectID === activeProjectId ? sessions : []
    return (
      <Suspense fallback={<div className="app loading"><div className="loading-content" role="status"><div className="loading-brand">Gokin Studio</div><div className="loading-text">Opening Quick Entry…</div></div></div>}>
        <QuickEntryOverlay
          projectID={activeProjectId}
          projectName={activeProject?.name}
          sessions={quickEntrySessions}
          activeSessionID={storedActiveSession || chatTabStop}
          voiceActivationID={pendingQuickEntryVoiceActivations[0] ?? null}
          onVoiceActivationHandled={(id) => setPendingQuickEntryVoiceActivations((pending) => pending.filter((item) => item !== id))}
          onDismiss={() => { void exitQuickEntry(false) }}
          onOpenStudio={() => { void exitQuickEntry(true) }}
          onOpenSession={openQuickEntrySession}
          onCreateSession={createQuickEntrySession}
        />
      </Suspense>
    )
  }

  return (
    <div
      className={`app ${effectiveSidebarCollapsed ? 'sidebar-collapsed' : ''} ${compactViewport ? 'sidebar-compact' : ''} ${sidebarDrawerOpen ? 'sidebar-drawer-open' : ''} ${sidebarResizing ? 'sidebar-resizing' : ''}`}
      style={{ '--sidebar-width': `${sidebarWidth}px` } as CSSProperties}
    >
      {deepLinkError && (
        <div className="deep-link-error" role="alert">
          <AlertTriangle size={14} />
          <span>{deepLinkError}</span>
          <button type="button" onClick={() => setDeepLinkError(null)} aria-label="Dismiss link error"><X size={13} /></button>
        </div>
      )}
      {desktopUpdate?.available && desktopUpdate.latestVersion !== dismissedUpdateVersion && desktopUpdate.releaseURL && (
        <div className="desktop-update-banner" role="status" aria-live="polite">
          <Download size={14} aria-hidden />
          <span><strong>Gokin Studio {desktopUpdate.latestVersion}</strong> is available.</span>
          <button type="button" className="desktop-update-open" onClick={() => BrowserOpenURL(desktopUpdate.releaseURL!)}>
            Release page
          </button>
          <button
            type="button"
            className="desktop-update-dismiss"
            onClick={() => setDismissedUpdateVersion(desktopUpdate.latestVersion || null)}
            aria-label="Dismiss update notice"
          >
            <X size={13} />
          </button>
        </div>
      )}
      <TopBar
        title={topBarTitle}
        projectName={activeProject?.name}
        projectId={activeProjectId}
        isChat={isChat}
        onToggleSidebar={toggleSidebar}
        sidebarExpanded={!effectiveSidebarCollapsed}
        canNavigateBack={canNavigateBack}
        canNavigateForward={canNavigateForward}
        onNavigateBack={() => navigateHistory(-1)}
        onNavigateForward={() => navigateHistory(1)}
      />
      <Sidebar
        onOpenSettings={() => { setView('settings'); setSidebarDrawerOpen(false) }}
        onToggleCollapse={toggleSidebar}
        onNavigate={() => setSidebarDrawerOpen(false)}
        collapsed={effectiveSidebarCollapsed}
        compactDrawer={compactViewport && sidebarDrawerOpen}
      />
      {!compactViewport && !effectiveSidebarCollapsed && (
        <div
          className="sidebar-resizer"
          role="separator"
          aria-label="Resize project sidebar"
          aria-orientation="vertical"
          aria-valuemin={SIDEBAR_WIDTH_MIN}
          aria-valuemax={SIDEBAR_WIDTH_MAX}
          aria-valuenow={sidebarWidth}
          tabIndex={0}
          title="Drag to resize · double-click to reset"
          onPointerDown={(event) => {
            if (event.button !== 0) return
            event.preventDefault()
            sidebarResizeActiveRef.current = true
            setSidebarResizing(true)
            event.currentTarget.setPointerCapture(event.pointerId)
          }}
          onPointerMove={(event) => {
            if (!sidebarResizeActiveRef.current) return
            updateSidebarWidth(event.clientX)
          }}
          onPointerUp={(event) => {
            if (!sidebarResizeActiveRef.current) return
            sidebarResizeActiveRef.current = false
            setSidebarResizing(false)
            updateSidebarWidth(sidebarWidthRef.current, true)
            if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
          }}
          onPointerCancel={() => {
            sidebarResizeActiveRef.current = false
            setSidebarResizing(false)
            updateSidebarWidth(sidebarWidthRef.current, true)
          }}
          onDoubleClick={() => updateSidebarWidth(SIDEBAR_WIDTH_DEFAULT, true)}
          onKeyDown={(event) => {
            if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
            event.preventDefault()
            const step = event.shiftKey ? 24 : 8
            const next = event.key === 'Home' ? SIDEBAR_WIDTH_MIN
              : event.key === 'End' ? SIDEBAR_WIDTH_MAX
                : sidebarWidthRef.current + (event.key === 'ArrowLeft' ? -step : step)
            updateSidebarWidth(next, true)
          }}
        />
      )}
      {compactViewport && sidebarDrawerOpen && (
        <button
          type="button"
          className="sidebar-drawer-backdrop"
          onClick={() => setSidebarDrawerOpen(false)}
          aria-label="Close project sidebar"
          tabIndex={-1}
        />
      )}
      <div className="main-content">
        <nav className="view-tabs" aria-label="Workspace views and chat sessions">
          {sessionCatalogReady && sessions.length > 1 && (
            <div className="session-switcher-wrap">
              <button
                ref={sessionSwitcherTriggerRef}
                type="button"
                className={`session-switcher-trigger ${showSessionSwitcher ? 'active' : ''}`}
                onClick={() => setShowSessionSwitcher((open) => !open)}
                title={`All chats (${sessions.length})`}
                aria-label={`All chats, ${sessions.length}`}
                aria-haspopup="dialog"
                aria-expanded={showSessionSwitcher}
                aria-controls="session-switcher-dialog"
              >
                <ListFilter size={13} />
                <span>{sessions.length}</span>
              </button>
              {showSessionSwitcher && (
                <>
                  <div className="session-switcher-backdrop" onClick={() => { setShowSessionSwitcher(false); setSessionSwitcherQuery('') }} />
                  <div
                    id="session-switcher-dialog"
                    ref={sessionSwitcherRef}
                    className="session-switcher"
                    role="dialog"
                    aria-label="All chats"
                    onKeyDown={(event) => {
                      if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp' && event.key !== 'Home' && event.key !== 'End') return
                      const options = Array.from(sessionSwitcherRef.current?.querySelectorAll<HTMLButtonElement>('.session-switcher-item') || [])
                      if (options.length === 0) return
                      event.preventDefault()
                      const current = options.indexOf(document.activeElement as HTMLButtonElement)
                      const next = event.key === 'Home'
                        ? 0
                        : event.key === 'End'
                          ? options.length - 1
                          : event.key === 'ArrowDown'
                            ? (current + 1 + options.length) % options.length
                            : (current - 1 + options.length) % options.length
                      options[next]?.focus()
                    }}
                  >
                    <div className="session-switcher-head">
                      <div>
                        <strong>Chats</strong>
                        <span>{sessions.length} in this project</span>
                      </div>
                      <button
                        type="button"
                        className="session-switcher-new"
                        onClick={() => {
                          setShowSessionSwitcher(false)
                          setSessionSwitcherQuery('')
                          void handleNewChat()
                        }}
                        title="New chat (Cmd/Ctrl+N)"
                      >
                        <Plus size={13} /> New
                      </button>
                    </div>
                    <label className="session-switcher-search">
                      <Search size={12} aria-hidden />
                      <input
                        value={sessionSwitcherQuery}
                        onChange={(event) => setSessionSwitcherQuery(event.target.value)}
                        placeholder="Search chats…"
                        aria-label="Search chats"
                      />
                      {sessionSwitcherQuery && (
                        <button type="button" onClick={() => setSessionSwitcherQuery('')} title="Clear search" aria-label="Clear chat search">
                          <X size={11} />
                        </button>
                      )}
                    </label>
                    <div className="session-switcher-list" role="listbox" aria-label="Project chats">
                      {switcherSessions.length === 0 ? (
                        <div className="session-switcher-empty">No chats match “{sessionSwitcherQuery}”.</div>
                      ) : switcherSessions.map((session) => {
                        const sessionKey = activeProjectId ? activeProjectId + '_' + session.id : ''
                        const live = !!sessionActive[sessionKey]
                        const unreadCount = view === session.id ? 0 : (unread[sessionKey] || 0)
                        return (
                          <button
                            key={session.id}
                            type="button"
                            role="option"
                            aria-selected={view === session.id}
                            className={`session-switcher-item ${view === session.id ? 'current' : ''}`}
                            onClick={() => {
                              setView(session.id)
                              setShowSessionSwitcher(false)
                              setSessionSwitcherQuery('')
                            }}
                          >
                            <span className="session-switcher-icon">
                              {session.pinned ? <Pin size={11} /> : session.parentName ? <GitFork size={12} /> : session.worktreeIsolated ? <GitBranch size={12} /> : <MessageSquare size={12} />}
                            </span>
                            <span className="session-switcher-copy">
                              <strong>{session.name}</strong>
                              {session.parentName && <small>Forked from {session.parentName}</small>}
                              {session.worktreeError
                                ? <small className="worktree-error">Worktree unavailable</small>
                                : session.worktreeIsolated && <small>Isolated · {session.worktreeBranch}</small>}
                            </span>
                            {live ? <span className="session-switcher-state live">Working</span>
                              : unreadCount > 0 ? <span className="session-switcher-state unread">{unreadCount > 99 ? '99+' : unreadCount} new</span>
                                : view === session.id ? <span className="session-switcher-state current">Current</span> : null}
                          </button>
                        )
                      })}
                    </div>
                    <div className="session-switcher-foot">↑↓ navigate · Enter open · Esc close</div>
                  </div>
                </>
              )}
            </div>
          )}
          <div
            ref={tabsLeftRef}
            className="tabs-left"
            role="tablist"
            aria-label="Project chat sessions"
            onWheel={(event) => {
              const strip = event.currentTarget
              if (strip.scrollWidth <= strip.clientWidth || Math.abs(event.deltaY) <= Math.abs(event.deltaX)) return
              strip.scrollLeft += event.deltaY
              event.preventDefault()
            }}
            onKeyDown={(event) => {
              if (event.nativeEvent.isComposing || event.keyCode === 229) return
              const target = (event.target as HTMLElement).closest<HTMLButtonElement>('button.view-tab-main')
              if (!target || !tabsLeftRef.current?.contains(target)) return
              const tabs = Array.from(tabsLeftRef.current.querySelectorAll<HTMLButtonElement>('button.view-tab-main:not([disabled])'))
              const current = tabs.indexOf(target)
              if (current < 0) return
              if (event.key === 'ContextMenu' || (event.shiftKey && event.key === 'F10')) {
                const sessionID = target.dataset.sessionId
                if (!sessionID) return
                event.preventDefault()
                const rect = target.getBoundingClientRect()
                tabCtxMenuTriggerRef.current = target
                setTabCtxMenu({ id: sessionID, x: rect.left + 8, y: rect.bottom })
                return
              }
              if (event.key === 'Delete' && sessions.length > 1) {
                const sessionID = target.dataset.sessionId
                if (!sessionID) return
                event.preventDefault()
                void handleArchiveChat(sessionID)
                return
              }
              if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
              event.preventDefault()
              const next = event.key === 'Home' ? 0
                : event.key === 'End' ? tabs.length - 1
                  : event.key === 'ArrowRight' ? (current + 1) % tabs.length
                    : (current - 1 + tabs.length) % tabs.length
              const nextTab = tabs[next]
              const sessionID = nextTab?.dataset.sessionId
              if (!sessionID) return
              nextTab.focus()
              setView(sessionID)
            }}
          >
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
                <div
                  key={s.id}
                  draggable={!isRenaming && !tabOrderSaving}
                  onDragStart={(e) => {
                    if (isRenaming || tabOrderSaving) { e.preventDefault(); return }
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
                    void reorderSessionTabs(sourceId, s.id)
                  }}
                  className={`view-tab ${view === s.id ? 'active' : ''} ${s.parentName ? 'is-fork' : ''} ${unreadCount > 0 ? 'has-unread' : ''} ${s.pinned ? 'is-pinned' : ''} ${draggingTabId === s.id ? 'is-dragging' : ''} ${dropTargetId === s.id ? 'drop-target' : ''}`}
                  onContextMenu={(e) => {
                    e.preventDefault()
                    tabCtxMenuTriggerRef.current = e.currentTarget.querySelector<HTMLButtonElement>('.view-tab-main')
                    const rect = e.currentTarget.getBoundingClientRect()
                    setTabCtxMenu({
                      id: s.id,
                      x: e.clientX || rect.left,
                      y: e.clientY || rect.bottom,
                    })
                  }}
                  title={
                    (s.pinned ? 'Pinned to top of tab list\n' : '') +
                    (s.parentName ? `Forked from: ${s.parentName}\n` : '') +
                    (s.worktreeError ? `Worktree unavailable: ${s.worktreeError}\n` : s.worktreeIsolated ? `Isolated worktree: ${s.worktreeBranch}\n${s.worktreePath || ''}\n` : 'Shared project checkout\n') +
                    (unreadCount > 0 ? `${unreadCount} new turn${unreadCount === 1 ? '' : 's'} since last viewed\n` : '') +
                    'Double-click to rename · Middle-click to archive · Right-click or Shift+F10 for menu'
                  }
                >
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
                    <button
                      type="button"
                      className="view-tab-main"
                      role="tab"
                      aria-selected={isCurrentTab}
                      aria-current={isCurrentTab ? 'page' : undefined}
                      aria-haspopup="menu"
                      aria-expanded={tabCtxMenu?.id === s.id}
                      aria-keyshortcuts="Shift+F10"
                      aria-label={`${s.name}${isActive ? ', running' : ''}${unreadCount > 0 ? `, ${unreadCount} unread` : ''}`}
                      data-session-id={s.id}
                      tabIndex={chatTabStop === s.id ? 0 : -1}
                      onClick={() => setView(s.id)}
                      onAuxClick={(event) => {
                        if (event.button !== 1 || sessions.length <= 1) return
                        event.preventDefault()
                        void handleArchiveChat(s.id)
                      }}
                      onDoubleClick={(e) => {
                        e.stopPropagation()
                        setRenameValue(s.name)
                        setRenamingId(s.id)
                      }}
                    >
                      {s.pinned && <Pin size={9} className="tab-pin-icon" aria-hidden />}
                      {s.parentName
                        ? <GitFork size={11} className="tab-fork-icon" aria-hidden />
                        : s.worktreeIsolated
                          ? <GitBranch size={12} className={s.worktreeError ? 'tab-worktree-icon error' : 'tab-worktree-icon'} aria-hidden />
                          : <MessageSquare size={13} aria-hidden />}
                      <span className="tab-label">{s.name}</span>
                      {isActive && <span className="tab-badge live" aria-hidden>live</span>}
                      {!isActive && unreadCount > 0 && (
                        <span className="tab-badge unread" aria-hidden>
                          {unreadCount > 9 ? '9+' : unreadCount}
                        </span>
                      )}
                      {renameError === s.id && <span className="tab-badge err" aria-hidden>rename failed</span>}
                      {closeError?.id === s.id && <span className="tab-badge err" title={closeError.message}>blocked</span>}
                    </button>
                  )}
                  {!isRenaming && sessions.length > 1 && (
                    <button
                      type="button"
                      className="tab-close"
                      onClick={() => { void handleArchiveChat(s.id) }}
                      title="Archive chat"
                      aria-label={`Archive chat ${s.name}`}
                    >
                      <Archive size={11} />
                    </button>
                  )}
                </div>
              )
            })}
            {sessionCatalogReady ? (
              <>
                <button
                  className="view-tab tab-new"
                  onClick={handleNewChat}
                  title={newChatError ? `Failed: ${newChatError}` : 'New chat (Cmd/Ctrl+N)'}
                  aria-label="New chat"
                >
                  <Plus size={14} />
                  {newChatError && <span className="tab-badge err">failed</span>}
                </button>
                <button
                  className="view-tab tab-archives"
                  onClick={() => { void openArchivedSessions() }}
                  title="Archived chats"
                  aria-label="Open archived chats"
                >
                  <Archive size={13} />
                </button>
              </>
            ) : activeProjectId ? (
              <span className={`session-catalog-tab-status ${sessionLoadError ? 'error' : ''}`} role="status">
                {sessionsLoading ? <><Loader2 size={11} className="spin" /> Loading chats…</> : 'Chats unavailable'}
              </span>
            ) : null}
            {(tabOrderSaving || tabOrderError) && (
              <span
                className={`tab-order-status ${tabOrderError ? 'error' : ''}`}
                role="status"
                title={tabOrderError || 'Saving tab order'}
              >
                {tabOrderError ? 'Order not saved' : 'Saving order…'}
              </span>
            )}
            {/* Drop-at-end zone (iter 650+). Catches drops past the last tab
                so users can move a tab to the very end of the list — the
                per-tab onDrop only supports "drop before target". Visible as
                a thin drop indicator only while a drag is in progress. */}
            {sessionCatalogReady && draggingTabId && (
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
                  void reorderSessionTabs(sourceId)
                }}
                aria-hidden
              />
            )}
          </div>
          <div className="tabs-right">
            <button className={`view-tab ${view === 'files' ? 'active' : ''}`} aria-current={view === 'files' ? 'page' : undefined} onClick={() => setView('files')} title="Files (Ctrl+2)">
              <FolderTree size={14} />
              <span className="tab-label">Files</span>
            </button>
            <button className={`view-tab ${view === 'artifacts' ? 'active' : ''}`} aria-current={view === 'artifacts' ? 'page' : undefined} onClick={() => setView('artifacts')} title="Artifacts (Ctrl+4)">
              <PanelsTopLeft size={14} />
              <span className="tab-label">Artifacts</span>
            </button>
            <button className={`view-tab ${view === 'settings' ? 'active' : ''}`} aria-current={view === 'settings' ? 'page' : undefined} onClick={() => setView('settings')} title="Settings (Ctrl+3)">
              <Settings size={14} />
              <span className="tab-label">Settings</span>
              {settingsDraft && <span className="tab-badge unsaved" aria-label="Unsaved settings">unsaved</span>}
            </button>
          </div>
        </nav>
        <div className={`content-area ${view === 'files' || view === 'settings' ? view : ''}`}>
          {(settingsMounted || view === 'settings') && (
            <div className={`settings-view-host ${view === 'settings' ? 'active' : 'hidden'}`} aria-hidden={view === 'settings' ? undefined : true}>
              <Suspense fallback={<LazyViewFallback label="Loading settings…" />}>
                <SettingsPage isActive={view === 'settings'} />
              </Suspense>
            </div>
          )}
          {(filesMounted || view === 'files') && (
            <div className={`workspace-view-host ${view === 'files' ? 'active' : 'hidden'}`} aria-hidden={view === 'files' ? undefined : true}>
              <FileBrowser
                key={`${activeProjectId || 'no-project'}:${chatTabStop || 'default'}`}
                sessionID={chatTabStop || 'default'}
                artifactPath={fileArtifactSelection?.projectID === activeProjectId && fileArtifactSelection.sessionID === (chatTabStop || 'default') ? fileArtifactSelection.path : null}
                onArtifactPathChange={(path) => setFileArtifactSelection(path && activeProjectId ? { projectID: activeProjectId, sessionID: chatTabStop || 'default', path } : null)}
                isActive={view === 'files'}
              />
            </div>
          )}
          {(artifactsMounted || view === 'artifacts') && (
            <div className={`workspace-view-host ${view === 'artifacts' ? 'active' : 'hidden'}`} aria-hidden={view === 'artifacts' ? undefined : true}>
              <ArtifactLibrary
                key={`${activeProjectId || 'no-project'}:${chatTabStop || 'default'}`}
                sessionID={chatTabStop || 'default'}
                artifactPath={libraryArtifactSelection?.projectID === activeProjectId && libraryArtifactSelection.sessionID === (chatTabStop || 'default') ? libraryArtifactSelection.path : null}
                onArtifactPathChange={(path) => setLibraryArtifactSelection(path && activeProjectId ? { projectID: activeProjectId, sessionID: chatTabStop || 'default', path } : null)}
                isActive={view === 'artifacts'}
              />
            </div>
          )}
          {view === 'settings' || view === 'files' || view === 'artifacts' ? null
            : activeProjectId && !sessionCatalogReady ? (
                  <div className={`session-load-state ${sessionLoadError ? 'error' : ''}`} role={sessionLoadError ? 'alert' : 'status'}>
                    <div className="session-load-state-icon">
                      {sessionsLoading ? <Loader2 size={22} className="spin" /> : <MessageSquare size={22} />}
                    </div>
                    <h2>{sessionsLoading ? 'Loading chats' : 'Chats could not be loaded'}</h2>
                    <p>{sessionsLoading
                      ? `Opening ${activeProject?.name || 'this project'} without carrying over tabs from another workspace.`
                      : sessionLoadError || 'The chat catalog is unavailable.'}</p>
                    {sessionLoadError && (
                      <button className="btn-primary" type="button" onClick={() => setSessionReloadNonce((value) => value + 1)}>
                        <RefreshCw size={13} /> Retry
                      </button>
                    )}
                  </div>
                )
                : (
              <SessionWorkspace
                  key={`${activeProjectId || 'no-project'}:${view}`}
                  projectId={activeProjectId!}
                  sessionId={view}
                  sessionName={activeSessionTab?.name}
                  sessionPermissionMode={activeSessionTab?.permissionMode}
                  sessionWorktreeIsolated={activeSessionTab?.worktreeIsolated}
                  sessionWorktreePath={activeSessionTab?.worktreePath}
                  sessionWorktreeBranch={activeSessionTab?.worktreeBranch}
                  sessionWorktreeError={activeSessionTab?.worktreeError}
                  quickEntryAction={pendingQuickEntryAction}
                  onQuickEntryActionHandled={handleQuickEntryActionHandled}
                />
            )}
        </div>
      </div>
      <StatusBar onOpenDelegations={() => setShowDelegations(true)} />
      {showDelegations && (
        <Suspense fallback={<LazyModalFallback label="Loading delegations…" />}>
          <DelegationsPanel
            onClose={() => setShowDelegations(false)}
            onOpenTarget={(projectID, sessionID) => {
              setShowDelegations(false)
              // Jump to the delegated chat in the target project so the user can
              // read the full transcript, not just the summarised answer.
              // `view` is what actually selects the rendered session, so setting
              // only the two stores would leave the UI on whatever was open.
              useProjectStore.getState().setActiveProject(projectID)
              useChatStore.getState().setActiveSession(projectID, sessionID)
              setView(sessionID)
            }}
          />
        </Suspense>
      )}
      <ToastStack />
      <FileContextMenuHost />
      {confirmationDialog}
      {showArchivedSessions && (
        <>
          <div className="sessions-mgr-backdrop" onClick={() => setShowArchivedSessions(false)} />
          <div className="sessions-mgr-modal archived-sessions-modal" role="dialog" aria-modal="true" aria-labelledby="archived-sessions-title">
            <div className="sessions-mgr-header">
              <h3 id="archived-sessions-title"><Archive size={15} /> Archived chats</h3>
              <button type="button" className="icon-btn" onClick={() => setShowArchivedSessions(false)} aria-label="Close archived chats"><X size={14} /></button>
            </div>
            <p className="archived-sessions-intro">Archived chats are hidden from the tab bar, but keep their conversation, draft, and isolated worktree until you permanently delete them.</p>
            {archivedSessionsError && <div className="sessions-mgr-error" role="alert">{archivedSessionsError}</div>}
            {archivedSessionsLoading ? (
              <div className="sessions-mgr-empty"><Loader2 size={14} className="spin" /> Loading archived chats…</div>
            ) : archivedSessions.length === 0 ? (
              <div className="sessions-mgr-empty">No archived chats in this project.</div>
            ) : (
              <div className="sessions-mgr-list archived-sessions-list">
                {archivedSessions.map((session) => {
                  const busy = archivedSessionBusy === session.id
                  const archivedLabel = session.archivedAt
                    ? new Date(session.archivedAt).toLocaleString()
                    : 'Archived'
                  return (
                    <div className="archived-session-row" key={session.id}>
                      <div className="archived-session-icon"><Archive size={14} /></div>
                      <div className="sessions-mgr-row-main">
                        <div className="sessions-mgr-row-name">{session.name}</div>
                        <div className="sessions-mgr-row-meta">
                          <span>{session.messages || 0} message{session.messages === 1 ? '' : 's'}</span>
                          <span>·</span>
                          <span>{archivedLabel}</span>
                          {session.worktreeIsolated && <><span>·</span><span>{session.worktreeBranch || 'isolated worktree'}</span></>}
                        </div>
                      </div>
                      <div className="archived-session-actions">
                        <button type="button" className="btn-secondary" disabled={!!archivedSessionBusy} onClick={() => { void handleRestoreArchivedChat(session.id) }}>
                          {busy ? <Loader2 size={12} className="spin" /> : <ArchiveRestore size={12} />} Restore
                        </button>
                        <button type="button" className="archived-session-delete" disabled={!!archivedSessionBusy} onClick={() => { void handleDeleteArchivedChat(session) }} title="Permanently delete chat" aria-label={`Permanently delete ${session.name}`}>
                          <Trash2 size={13} />
                        </button>
                      </div>
                    </div>
                  )
                })}
              </div>
            )}
            <div className="sessions-mgr-footer">
              <span className="sessions-mgr-count">{archivedSessions.length} archived</span>
              <button type="button" className="btn-secondary" onClick={() => setShowArchivedSessions(false)}>Done</button>
            </div>
          </div>
        </>
      )}
      {showOnboarding && (
        <Suspense fallback={<LazyModalFallback label="Preparing setup…" />}>
          <OnboardingWizard
            onComplete={(project, starterPrompt) => {
              setShowOnboarding(false)
              if (!starterPrompt) return
              const projectID = project?.id || useProjectStore.getState().activeProjectId
              if (!projectID) return
              if (sessionsProjectIDRef.current === projectID && sessionsRef.current.length > 0) {
                requestAnimationFrame(() => composeInChat(starterPrompt, 'replace'))
              } else {
                pendingComposeRef.current = { projectID, text: starterPrompt, mode: 'replace' }
              }
            }}
            onSkip={() => setShowOnboarding(false)}
          />
        </Suspense>
      )}
      {tabCtxMenu && (() => {
        const MENU_W = 180, MENU_H = sessions.length > 1 ? 242 : 80
        const cx = Math.min(tabCtxMenu.x, window.innerWidth - MENU_W - 8)
        const cy = Math.min(tabCtxMenu.y, window.innerHeight - MENU_H - 8)
        const tab = sessions.find((t) => t.id === tabCtxMenu.id)
        const isPinned = !!tab?.pinned
        const tabIndex = sessions.findIndex((session) => session.id === tabCtxMenu.id)
        return (
          <>
            <div className="ctx-backdrop" onClick={() => setTabCtxMenu(null)} />
            <div
              ref={tabCtxMenuRef}
              className="ctx-menu"
              style={{ top: cy, left: cx }}
              role="menu"
              aria-label={`Actions for ${tab?.name || 'chat'}`}
              onKeyDown={(event) => {
                if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
                const items = Array.from(tabCtxMenuRef.current?.querySelectorAll<HTMLButtonElement>('button:not([disabled])') || [])
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
              <button role="menuitem" onClick={async () => {
                if (!activeProjectId) { setTabCtxMenu(null); return }
                const projectID = activeProjectId
                const targetId = tabCtxMenu.id
                const next = !isPinned
                setTabCtxMenu(null)
                // Optimistic local update so the sort runs immediately; if
                // the backend rejects we revert. Errors logged silently —
                // a failure here is non-blocking.
                setSessions((prev) => prev.map((t) => (t.id === targetId ? { ...t, pinned: next } : t)))
                try {
                  await SetSessionPinned(projectID, targetId, next)
                } catch (e) {
                  console.error('SetSessionPinned error:', e)
                  if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) return
                  setSessions((prev) => prev.map((t) => (t.id === targetId ? { ...t, pinned: isPinned } : t)))
                  setTabOrderError(`Could not ${next ? 'pin' : 'unpin'} chat`)
                  setTimeout(() => setTabOrderError(null), 5000)
                }
                // Reload from backend to get the canonical sorted order so the
                // pinned tab actually moves to the top of the list.
                if (useProjectStore.getState().activeProjectId === projectID) {
                  ListChatSessions(projectID).then((list) => {
                    if (useProjectStore.getState().activeProjectId !== projectID || sessionsProjectIDRef.current !== projectID) return
                    if (list) setSessions(list.map(toSessionTab))
                  }).catch(() => {})
                }
              }}>
                {isPinned ? <><PinOff size={12} /> Unpin from top</> : <><Pin size={12} /> Pin to top</>}
              </button>
              {sessions.length > 1 && (
                <>
                  <div className="ctx-divider" />
                  <button
                    role="menuitem"
                    disabled={tabIndex <= 0 || tabOrderSaving}
                    onClick={() => {
                      const targetID = tabCtxMenu.id
                      const beforeID = sessions[tabIndex - 1]?.id
                      setTabCtxMenu(null)
                      if (beforeID) void reorderSessionTabs(targetID, beforeID)
                    }}
                  >Move left</button>
                  <button
                    role="menuitem"
                    disabled={tabIndex < 0 || tabIndex >= sessions.length - 1 || tabOrderSaving}
                    onClick={() => {
                      const targetID = tabCtxMenu.id
                      const beforeID = sessions[tabIndex + 2]?.id
                      setTabCtxMenu(null)
                      void reorderSessionTabs(targetID, beforeID)
                    }}
                  >Move right</button>
                </>
              )}
              <div className="ctx-divider" />
              <button role="menuitem" onClick={() => {
                const targetTab = sessions.find((t) => t.id === tabCtxMenu.id)
                if (targetTab) {
                  setRenameValue(targetTab.name)
                  setRenamingId(targetTab.id)
                }
                setTabCtxMenu(null)
              }}>Rename</button>
              {sessions.length > 1 && (
                <>
                  <div className="ctx-divider" />
                  <button role="menuitem" onClick={() => {
                    const targetId = tabCtxMenu.id
                    setTabCtxMenu(null)
                    void handleArchiveChat(targetId)
                  }}>
                    <Archive size={12} /> Archive chat
                  </button>
                  <div className="ctx-divider" />
                  <button role="menuitem" className="ctx-danger" onClick={() => {
                    const targetId = tabCtxMenu.id
                    setTabCtxMenu(null)
                    void handleDeleteChat(targetId)
                  }}>
                    <Trash2 size={12} /> Delete chat…
                  </button>
                </>
              )}
            </div>
          </>
        )
      })()}
      {showPalette && (
        <Suspense fallback={<LazyModalFallback label="Opening commands…" />}>
          <CommandPalette
            onClose={() => setShowPalette(false)}
            onSwitchProject={(id) => useProjectStore.getState().setActiveProject(id)}
            onOpenSettings={() => setView('settings')}
            onOpenFiles={() => setView('files')}
            onOpenArtifacts={() => setView('artifacts')}
            onNewChat={handleNewChat}
            onClearChat={() => window.dispatchEvent(new CustomEvent('gokin:clear-chat'))}
            onOpenMemory={() => window.dispatchEvent(new CustomEvent('gokin:open-memory'))}
            onToggleSidebar={toggleSidebar}
          />
        </Suspense>
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
