import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from 'react'
import { AlertTriangle, CheckCircle2, Crosshair, Database, ExternalLink, FileCode2, Globe2, Loader2, Monitor, Play, Plus, RefreshCw, ScanSearch, ShieldCheck, Square, TerminalSquare, Trash2, X } from 'lucide-react'
import {
  CloseExternalBrowserTab,
  CloseSessionPreviewFile,
  ClearPreviewSessionData,
  GetPreviewSessionPersistence,
  GetSessionPreviewConfig,
  GetSessionPreviewServerStatus,
  ListExternalBrowserTabs,
  NavigateExternalBrowserTab,
  OpenExternalBrowserTab,
  OpenSessionPreviewFile,
  SavePreviewBrowserStorage,
  SetPreviewSessionPersistence,
  StartSessionPreviewServer,
  StopSessionPreviewServer,
  SaveDetectedSessionPreviewConfig,
  ResolvePreviewBrowserRequest,
  ResolveExternalBrowserAgentRequest,
  ReviewExternalBrowserNavigation,
  SetActiveExternalBrowserTab,
  UpdateExternalBrowserTabState,
} from '../../../wailsjs/go/studio/Studio'
import { BrowserOpenURL, EventsOn } from '../../../wailsjs/runtime/runtime'
import { composeInChat, formatFileMention } from '../../lib/composeInChat'
import { useConfirmDialog } from '../common/AppDialog'
import { usePersistentPanelWidth } from '../../hooks/usePersistentPanelWidth'
import { normalizePreviewElementSelection, previewElementDraft, type PreviewElementSelection } from '../../lib/previewSelection'

interface PreviewConfiguration {
  name: string
  runtimeExecutable: string
  runtimeArgs?: string[]
  port: number
  command: string
  cwd?: string
  env?: Record<string, string>
  autoPort?: boolean
  program?: string
  args?: string[]
  url?: string
}

interface PreviewConfig {
  version: string
  autoVerify: boolean
  configurations?: PreviewConfiguration[]
  source: 'file' | 'detected' | 'none'
  path: string
}

interface PreviewStatus {
  configuration: string
  state: 'stopped' | 'starting' | 'running' | 'failed'
  url?: string
  browserURL?: string
  port?: number
  pid?: number
  startedAt?: number
  logs?: string
  error?: string
  bridgeToken?: string
}

interface PreviewSessionPersistence {
  enabled: boolean
  hasData: boolean
  localStorageEntries: number
  cookies: number
  updatedAt?: number
}

interface ExternalBrowserTab {
  id: string
  projectID: string
  sessionID: string
  url: string
  origin: string
  browserURL: string
  bridgeToken: string
  title: string
  state: 'running' | 'failed'
  error?: string
  createdAt: number
  active?: boolean
  activeScripts?: boolean
}

interface ExternalNavigationReview {
  url: string
  origin: string
  hostname: string
  approved: boolean
}

interface PreviewInspection {
  url: string
  title: string
  readyState: string
  viewport: { width: number; height: number; devicePixelRatio: number }
  text: string
  headings: Array<Record<string, unknown>>
  controls: Array<Record<string, unknown>>
  issues: Array<{ kind: string; text: string; time: number }>
  capturedAt: number
  error?: string
  cancelled?: boolean
  selectedElement?: PreviewElementSelection
}

interface PreviewActionOwner {
  scope: number
  request: number
}

interface PreviewCommandOwner {
  scope: number
  requestID: string
  configuration: string
  bridgeToken: string
  action: string
  target: Window
}

interface ExternalNavigationOwner extends PreviewActionOwner {
  targetTabID: string
}

interface PendingExternalNavigationApproval {
  review: ExternalNavigationReview
  owner: ExternalNavigationOwner
  tabID?: string
}

interface ExternalAgentActionOwner {
  scope: number
  requestID: string
  tabID: string
  bridgeToken: string
}

export function LivePreviewPane({ projectId, sessionId, onClose, workspaceMode = false, staticPath = null, onStaticPathChange, elementSelectRequest = 0, externalNavigationRequest = null }: {
  projectId: string
  sessionId: string
  onClose: () => void
  workspaceMode?: boolean
  staticPath?: string | null
  onStaticPathChange?: (path: string | null) => void
  elementSelectRequest?: number
  externalNavigationRequest?: { url: string; key: number } | null
}) {
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const requestRef = useRef(0)
  const [config, setConfig] = useState<PreviewConfig | null>(null)
  const [selectedName, setSelectedName] = useState('')
  const [status, setStatus] = useState<PreviewStatus>({ configuration: '', state: 'stopped' })
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [revision, setRevision] = useState(0)
  const [staticOpenRevision, setStaticOpenRevision] = useState(0)
  const [logsOpen, setLogsOpen] = useState(false)
  const [frameLoaded, setFrameLoaded] = useState(false)
  const [inspection, setInspection] = useState<PreviewInspection | null>(null)
  const [inspecting, setInspecting] = useState(false)
  const [selectingElement, setSelectingElement] = useState(false)
  const [persistence, setPersistence] = useState<PreviewSessionPersistence>({ enabled: false, hasData: false, localStorageEntries: 0, cookies: 0 })
  const [persistenceBusy, setPersistenceBusy] = useState(false)
  const [externalTabs, setExternalTabs] = useState<ExternalBrowserTab[]>([])
  const [activeBrowserTabID, setActiveBrowserTabID] = useState<string | null>(null)
  const [browserAddress, setBrowserAddress] = useState('')
  const [externalBusy, setExternalBusy] = useState(false)
  const [externalRevision, setExternalRevision] = useState(0)
  const [externalFrameLoaded, setExternalFrameLoaded] = useState(false)
  const [externalAgentBusy, setExternalAgentBusy] = useState<{ requestID: string; action: string } | null>(null)
  const [closingExternalTabID, setClosingExternalTabID] = useState<string | null>(null)
  const [pendingExternalApproval, setPendingExternalApproval] = useState<PendingExternalNavigationApproval | null>(null)
  const frameRef = useRef<HTMLIFrameElement>(null)
  const externalFrameRef = useRef<HTMLIFrameElement>(null)
  const handledExternalNavigationRef = useRef(0)
  const externalTabsLoadRequestRef = useRef(0)
  const externalNavigationRequestRef = useRef(0)
  const externalNavigationInFlightRef = useRef(false)
  const activeBrowserTabIDRef = useRef<string | null>(null)
  const externalAgentInFlightRef = useRef<ExternalAgentActionOwner | null>(null)
  const closingExternalTabIDRef = useRef<string | null>(null)
  const previewCommandInFlightRef = useRef<PreviewCommandOwner | null>(null)
  const previewBridgeIdentityRef = useRef({ configuration: '', bridgeToken: '' })
  const elementSelectionRequestRef = useRef(0)
  const pendingInspectionRef = useRef<Map<string, { resolve: (value: PreviewInspection) => void; reject: (reason: Error) => void; timer: number }>>(new Map())
  const pendingExternalAgentRef = useRef<Map<string, { resolve: (value: Record<string, unknown>) => void; reject: (reason: Error) => void; timer: number }>>(new Map())
  const activeExternalSyncRef = useRef<Promise<unknown>>(Promise.resolve())
  const activeExternalSyncRequestRef = useRef(0)
  const selectingElementRef = useRef(false)
  const startElementSelectionRef = useRef<() => void>(() => {})
  const handledElementSelectRequestRef = useRef(0)
  const autoInspectRef = useRef(false)
  const persistenceRequestRef = useRef(0)
  const persistenceRefreshTimerRef = useRef<number | null>(null)
  const previewReloadTimerRef = useRef<number | null>(null)
  const serverStatusRequestRef = useRef(0)
  const staticRequestRef = useRef(0)
  const staticPathRef = useRef(staticPath)
  staticPathRef.current = staticPath
  const serverActionRequestRef = useRef(0)
  const serverActionInFlightRef = useRef(false)
  const persistenceActionRequestRef = useRef(0)
  const persistenceActionInFlightRef = useRef(false)
  const scopeKey = `${projectId.length}:${projectId}${sessionId.length}:${sessionId}`
  const scopeRef = useRef({ generation: 0, mounted: false })
  const selectedSnapshotRef = useRef({ name: '', command: '' })
  const [resizing, setResizing] = useState(false)
  const { width, widthRef, updateWidth } = usePersistentPanelWidth('gokin:live-preview-width', 560, 340, 900)

  const configurations = config?.configurations || []
  const selected = useMemo(
    () => configurations.find((item) => item.name === selectedName) || configurations[0],
    [configurations, selectedName],
  )
  const scopedExternalTabs = useMemo(
    () => externalTabs.filter((tab) => tab.projectID === projectId && tab.sessionID === sessionId),
    [externalTabs, projectId, sessionId],
  )
  const activeExternalTab = useMemo(
    () => scopedExternalTabs.find((tab) => tab.id === activeBrowserTabID) || null,
    [activeBrowserTabID, scopedExternalTabs],
  )
  const browserMode = activeBrowserTabID === 'new' || activeExternalTab !== null
  const externalInteractionBusy = busy || persistenceBusy || externalBusy || externalAgentBusy !== null || inspecting
  const activeExternalTabClosing = !!activeExternalTab && closingExternalTabID === activeExternalTab.id
  const browserNavigationBusy = externalInteractionBusy || activeExternalTabClosing
  const paneInteractionBusy = externalInteractionBusy || closingExternalTabID !== null

  useLayoutEffect(() => {
    selectedSnapshotRef.current = {
      name: selected?.name || '',
      command: selected?.command || '',
    }
  }, [selected?.command, selected?.name])

  useLayoutEffect(() => {
    activeBrowserTabIDRef.current = activeBrowserTabID
  }, [activeBrowserTabID])

  useLayoutEffect(() => {
    previewBridgeIdentityRef.current = {
      configuration: status.configuration,
      bridgeToken: status.bridgeToken || '',
    }
  }, [status.bridgeToken, status.configuration])

  useEffect(() => {
    scopeRef.current.generation += 1
    scopeRef.current.mounted = true
    const generation = scopeRef.current.generation
    serverActionInFlightRef.current = false
    persistenceActionInFlightRef.current = false
    externalNavigationInFlightRef.current = false
    externalAgentInFlightRef.current = null
    closingExternalTabIDRef.current = null
    previewCommandInFlightRef.current = null
    elementSelectionRequestRef.current += 1
    selectingElementRef.current = false
    activeExternalSyncRequestRef.current += 1
    activeBrowserTabIDRef.current = null
    setBusy(false)
    setPersistenceBusy(false)
    setExternalBusy(false)
    setPendingExternalApproval(null)
    setExternalTabs([])
    setActiveBrowserTabID(null)
    setBrowserAddress('')
    setExternalFrameLoaded(false)
    setExternalAgentBusy(null)
    setClosingExternalTabID(null)
    setInspecting(false)
    setSelectingElement(false)
    return () => {
      if (scopeRef.current.generation === generation) {
        scopeRef.current.mounted = false
        scopeRef.current.generation += 1
      }
      serverActionRequestRef.current += 1
      serverActionInFlightRef.current = false
      serverStatusRequestRef.current += 1
      persistenceActionRequestRef.current += 1
      persistenceActionInFlightRef.current = false
      externalNavigationRequestRef.current += 1
      externalNavigationInFlightRef.current = false
      externalAgentInFlightRef.current = null
      closingExternalTabIDRef.current = null
      previewCommandInFlightRef.current = null
      elementSelectionRequestRef.current += 1
      selectingElementRef.current = false
      externalTabsLoadRequestRef.current += 1
      activeExternalSyncRequestRef.current += 1
      if (previewReloadTimerRef.current !== null) {
        window.clearTimeout(previewReloadTimerRef.current)
        previewReloadTimerRef.current = null
      }
    }
  }, [scopeKey])

  const ownsScope = useCallback((generation: number) => (
    scopeRef.current.mounted && scopeRef.current.generation === generation
  ), [])

  const ownsPreviewCommand = useCallback((owner: PreviewCommandOwner) => (
    previewCommandInFlightRef.current === owner &&
    ownsScope(owner.scope) &&
    frameRef.current?.contentWindow === owner.target &&
    previewBridgeIdentityRef.current.configuration === owner.configuration &&
    previewBridgeIdentityRef.current.bridgeToken === owner.bridgeToken
  ), [ownsScope])

  const beginServerAction = (): PreviewActionOwner | null => {
    if (
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current ||
      previewCommandInFlightRef.current ||
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      closingExternalTabIDRef.current
    ) return null
    serverActionInFlightRef.current = true
    const owner = { scope: scopeRef.current.generation, request: ++serverActionRequestRef.current }
    serverStatusRequestRef.current += 1
    setBusy(true)
    setError(null)
    return owner
  }

  const ownsServerAction = (owner: PreviewActionOwner) => (
    ownsScope(owner.scope) && serverActionRequestRef.current === owner.request
  )

  const finishServerAction = (owner: PreviewActionOwner) => {
    if (!ownsServerAction(owner)) return
    serverActionInFlightRef.current = false
    setBusy(false)
  }

  const beginPersistenceAction = (): PreviewActionOwner | null => {
    if (
      persistenceActionInFlightRef.current ||
      serverActionInFlightRef.current ||
      previewCommandInFlightRef.current ||
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      closingExternalTabIDRef.current
    ) return null
    persistenceActionInFlightRef.current = true
    const owner = { scope: scopeRef.current.generation, request: ++persistenceActionRequestRef.current }
    persistenceRequestRef.current += 1
    if (persistenceRefreshTimerRef.current !== null) {
      window.clearTimeout(persistenceRefreshTimerRef.current)
      persistenceRefreshTimerRef.current = null
    }
    setPersistenceBusy(true)
    setError(null)
    return owner
  }

  const ownsPersistenceAction = (owner: PreviewActionOwner) => (
    ownsScope(owner.scope) && persistenceActionRequestRef.current === owner.request
  )

  const finishPersistenceAction = (owner: PreviewActionOwner) => {
    if (!ownsPersistenceAction(owner)) return
    persistenceActionInFlightRef.current = false
    setPersistenceBusy(false)
  }

  const schedulePreviewReload = (owner: PreviewActionOwner, configuration: string) => {
    if (previewReloadTimerRef.current !== null) window.clearTimeout(previewReloadTimerRef.current)
    const timer = window.setTimeout(() => {
      if (ownsPersistenceAction(owner) && selectedSnapshotRef.current.name === configuration) {
        setRevision((value) => value + 1)
      }
      if (previewReloadTimerRef.current === timer) previewReloadTimerRef.current = null
    }, 60)
    previewReloadTimerRef.current = timer
  }

  const beginExternalNavigation = useCallback((targetTabID: string): ExternalNavigationOwner | null => {
    if (
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      previewCommandInFlightRef.current ||
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current ||
      closingExternalTabIDRef.current
    ) return null
    externalNavigationInFlightRef.current = true
    const owner = {
      scope: scopeRef.current.generation,
      request: ++externalNavigationRequestRef.current,
      targetTabID,
    }
    setPendingExternalApproval(null)
    setExternalBusy(true)
    setError(null)
    return owner
  }, [])

  const ownsExternalNavigation = useCallback((owner: ExternalNavigationOwner) => (
    ownsScope(owner.scope) &&
    externalNavigationRequestRef.current === owner.request &&
    activeBrowserTabIDRef.current === owner.targetTabID
  ), [ownsScope])

  const finishExternalNavigation = useCallback((owner: ExternalNavigationOwner) => {
    if (!ownsScope(owner.scope) || externalNavigationRequestRef.current !== owner.request) return
    externalNavigationInFlightRef.current = false
    setExternalBusy(false)
  }, [ownsScope])

  const cancelExternalNavigation = useCallback(() => {
    if (
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      previewCommandInFlightRef.current ||
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current
    ) return false
    externalNavigationRequestRef.current += 1
    setPendingExternalApproval(null)
    setExternalBusy(false)
    return true
  }, [])

  const upsertExternalTab = useCallback((next: ExternalBrowserTab) => {
    if (next.projectID !== projectId || next.sessionID !== sessionId) return
    externalTabsLoadRequestRef.current += 1
    setExternalTabs((current) => {
      const index = current.findIndex((tab) => tab.id === next.id)
      if (index < 0) return [...current, next]
      const copy = current.slice()
      copy[index] = next
      return copy
    })
    activeBrowserTabIDRef.current = next.id
    setActiveBrowserTabID(next.id)
    setBrowserAddress(next.url)
    setExternalFrameLoaded(false)
  }, [projectId, sessionId])

  useEffect(() => {
    const request = ++externalTabsLoadRequestRef.current
    let cancelled = false
    void ListExternalBrowserTabs(projectId, sessionId)
      .then((raw: any) => {
        if (cancelled || externalTabsLoadRequestRef.current !== request) return
        const tabs = ((raw || []) as ExternalBrowserTab[]).filter((tab) => tab.projectID === projectId && tab.sessionID === sessionId)
        setExternalTabs(tabs)
        const active = tabs.find((tab) => tab.active)
        if (active) {
          activeBrowserTabIDRef.current = active.id
          setActiveBrowserTabID(active.id)
          setBrowserAddress(active.url)
        }
      })
      .catch((reason: any) => {
        if (!cancelled && externalTabsLoadRequestRef.current === request) setError(String(reason?.message || reason))
      })
    return () => {
      cancelled = true
      if (externalTabsLoadRequestRef.current === request) externalTabsLoadRequestRef.current += 1
    }
  }, [projectId, sessionId])

  useEffect(() => {
    const tabID = activeExternalTab?.id || ''
    const scope = scopeRef.current.generation
    const request = ++activeExternalSyncRequestRef.current
    const ownsSync = () => (
      ownsScope(scope) &&
      activeExternalSyncRequestRef.current === request &&
      (activeBrowserTabIDRef.current || '') === tabID
    )
    activeExternalSyncRef.current = activeExternalSyncRef.current
      .catch(() => undefined)
      .then(() => ownsSync() ? SetActiveExternalBrowserTab(projectId, sessionId, tabID) : undefined)
      .catch((reason: any) => { if (tabID && ownsSync()) setError(String(reason?.message || reason)) })
  }, [activeExternalTab?.id, ownsScope, projectId, sessionId])

  useEffect(() => () => {
    activeExternalSyncRequestRef.current += 1
    activeExternalSyncRef.current = activeExternalSyncRef.current
      .catch(() => undefined)
      .then(() => SetActiveExternalBrowserTab(projectId, sessionId, ''))
      .catch(() => undefined)
  }, [projectId, sessionId])

  const performExternalNavigation = useCallback(async (
    owner: ExternalNavigationOwner,
    review: ExternalNavigationReview,
    approval: 'existing' | 'once' | 'always',
    tabID?: string,
  ) => {
    if (!ownsExternalNavigation(owner)) return false
    const raw: any = tabID
      ? await NavigateExternalBrowserTab(projectId, sessionId, tabID, review.url, approval)
      : await OpenExternalBrowserTab(projectId, sessionId, review.url, approval)
    if (!ownsExternalNavigation(owner)) return false
    upsertExternalTab(raw as ExternalBrowserTab)
    setPendingExternalApproval(null)
    return true
  }, [ownsExternalNavigation, projectId, sessionId, upsertExternalTab])

  const commitExternalNavigation = useCallback(async (
    pending: PendingExternalNavigationApproval,
    approval: 'once' | 'always',
  ) => {
    if (externalNavigationInFlightRef.current || !ownsExternalNavigation(pending.owner)) return false
    externalNavigationInFlightRef.current = true
    setExternalBusy(true)
    setError(null)
    try {
      return await performExternalNavigation(pending.owner, pending.review, approval, pending.tabID)
    } catch (reason: any) {
      if (ownsExternalNavigation(pending.owner)) setError(String(reason?.message || reason))
      return false
    } finally {
      finishExternalNavigation(pending.owner)
    }
  }, [finishExternalNavigation, ownsExternalNavigation, performExternalNavigation])

  const requestExternalNavigation = useCallback(async (
    rawURL: string,
    tabID?: string,
    expectedOrigin?: string,
    activateNew = false,
  ): Promise<boolean> => {
    const trimmed = rawURL.trim()
    if (
      !trimmed ||
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      previewCommandInFlightRef.current ||
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current
    ) return false
    const targetTabID = tabID || 'new'
    if (closingExternalTabIDRef.current === targetTabID) return false
    if (activateNew) {
      externalTabsLoadRequestRef.current += 1
      activeBrowserTabIDRef.current = 'new'
      setActiveBrowserTabID('new')
      setBrowserAddress(trimmed)
      setPendingExternalApproval(null)
    } else if (activeBrowserTabIDRef.current !== targetTabID) {
      return false
    }
    const owner = beginExternalNavigation(targetTabID)
    if (!owner) return false
    const candidate = /^https?:\/\//i.test(trimmed) ? trimmed : `https://${trimmed}`
    try {
      const raw: any = await ReviewExternalBrowserNavigation(candidate)
      if (!ownsExternalNavigation(owner)) return true
      const review = raw as ExternalNavigationReview
      setBrowserAddress(review.url)
      if (review.approved || (tabID && expectedOrigin === review.origin)) {
        await performExternalNavigation(owner, review, 'existing', tabID)
      } else {
        setPendingExternalApproval({ review, owner, tabID })
      }
    } catch (reason: any) {
      if (ownsExternalNavigation(owner)) setError(String(reason?.message || reason))
    } finally {
      finishExternalNavigation(owner)
    }
    return true
  }, [beginExternalNavigation, finishExternalNavigation, ownsExternalNavigation, performExternalNavigation])

  const closeExternalTab = useCallback(async (tabID: string) => {
    if (
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      previewCommandInFlightRef.current ||
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current ||
      closingExternalTabIDRef.current
    ) return false
    closingExternalTabIDRef.current = tabID
    setClosingExternalTabID(tabID)
    setError(null)
    const scope = scopeRef.current.generation
    try {
      await CloseExternalBrowserTab(projectId, sessionId, tabID)
      if (!ownsScope(scope) || closingExternalTabIDRef.current !== tabID) return false
      externalTabsLoadRequestRef.current += 1
      setExternalTabs((current) => current.filter((tab) => tab.id !== tabID))
      setPendingExternalApproval((current) => current?.tabID === tabID ? null : current)
      if (activeBrowserTabIDRef.current === tabID) {
        activeBrowserTabIDRef.current = null
        setActiveBrowserTabID(null)
      }
      return true
    } catch (reason: any) {
      if (ownsScope(scope) && closingExternalTabIDRef.current === tabID) setError(String(reason?.message || reason))
      return false
    } finally {
      if (ownsScope(scope) && closingExternalTabIDRef.current === tabID) {
        closingExternalTabIDRef.current = null
        setClosingExternalTabID(null)
      }
    }
  }, [ownsScope, projectId, sessionId])

  useEffect(() => {
    let active = true
    const onMessage = (event: MessageEvent) => {
      if (!activeExternalTab || event.source !== externalFrameRef.current?.contentWindow) return
      const data = event.data
      if (!data || data.token !== activeExternalTab.bridgeToken || data.tabID !== activeExternalTab.id) return
      if (data.type === 'gokin-external-navigation' && typeof data.url === 'string') {
        void requestExternalNavigation(data.url, activeExternalTab.id, activeExternalTab.origin)
        return
      }
      if (data.type === 'gokin-external-ready') {
        const title = typeof data.title === 'string' && data.title.trim() ? data.title.trim().slice(0, 160) : activeExternalTab.title
        const nextURL = typeof data.url === 'string' ? data.url : activeExternalTab.url
        void UpdateExternalBrowserTabState(projectId, sessionId, activeExternalTab.id, activeExternalTab.bridgeToken, nextURL, title)
          .then(() => {
            if (!active) return
            setBrowserAddress(nextURL)
            setExternalTabs((current) => current.map((tab) => tab.id === activeExternalTab.id ? { ...tab, title, url: nextURL } : tab))
          })
          .catch(() => { /* a replaced or closed tab invalidates late page lifecycle messages */ })
        return
      }
      if (data.type !== 'gokin-external-result' || typeof data.requestId !== 'string') return
      const pending = pendingExternalAgentRef.current.get(data.requestId)
      if (!pending) return
      window.clearTimeout(pending.timer)
      pendingExternalAgentRef.current.delete(data.requestId)
      pending.resolve((data.payload || {}) as Record<string, unknown>)
    }
    window.addEventListener('message', onMessage)
    return () => {
      active = false
      window.removeEventListener('message', onMessage)
      const actionOwner = externalAgentInFlightRef.current
      if (actionOwner && actionOwner.tabID === activeExternalTab?.id) {
        const actionRequestID = actionOwner.requestID
        externalAgentInFlightRef.current = null
        setExternalAgentBusy((current) => current?.requestID === actionRequestID ? null : current)
      }
      for (const pending of pendingExternalAgentRef.current.values()) {
        window.clearTimeout(pending.timer)
        pending.reject(new Error('The active external Browser tab was replaced or closed.'))
      }
      pendingExternalAgentRef.current.clear()
    }
  }, [activeExternalTab, projectId, requestExternalNavigation, sessionId])

  const runExternalAgentCommand = useCallback(async (requestID: string, args: Record<string, unknown>) => {
    const target = externalFrameRef.current?.contentWindow
    const token = activeExternalTab?.bridgeToken
    if (!target || !token || !activeExternalTab || !externalFrameLoaded) throw new Error('Keep the active external Browser tab visible until this action finishes.')
    const result = await new Promise<Record<string, unknown>>((resolve, reject) => {
      const timer = window.setTimeout(() => {
        pendingExternalAgentRef.current.delete(requestID)
        reject(new Error('The external page did not answer the approved browser action. Inspect it again after it finishes loading.'))
      }, 10_000)
      pendingExternalAgentRef.current.set(requestID, { resolve, reject, timer })
      target.postMessage({ type: 'gokin-external-command', token, requestId: requestID, args }, '*')
    })
    if (typeof result.error === 'string' && result.error) throw new Error(result.error)
    return result
  }, [activeExternalTab, externalFrameLoaded])

  useEffect(() => EventsOn('external-browser:agent_request', (request: any) => {
    if (request?.projectID !== projectId || request?.sessionID !== sessionId) return
    const requestID = typeof request.requestID === 'string' ? request.requestID : ''
    const requestTabID = typeof request.tabID === 'string' ? request.tabID : ''
    const requestToken = typeof request.bridgeToken === 'string' ? request.bridgeToken : ''
    const action = typeof request?.args?.action === 'string' ? request.args.action : 'inspect'
    if (!requestID || !requestTabID || !requestToken) return
    if (!activeExternalTab || requestTabID !== activeExternalTab.id || requestToken !== activeExternalTab.bridgeToken) {
      void ResolveExternalBrowserAgentRequest(requestID, requestTabID, requestToken, JSON.stringify({ error: 'The active external Browser tab changed. List and inspect the visible tab again.', capturedAt: Date.now() }))
      return
    }
    const inFlight = externalAgentInFlightRef.current
    if (inFlight) {
      if (inFlight.requestID === requestID && inFlight.tabID === requestTabID && inFlight.bridgeToken === requestToken) return
      void ResolveExternalBrowserAgentRequest(requestID, requestTabID, requestToken, JSON.stringify({ error: 'Another approved model action is already running in this external Browser tab.', capturedAt: Date.now() }))
      return
    }
    if (externalNavigationInFlightRef.current || closingExternalTabIDRef.current === requestTabID) {
      void ResolveExternalBrowserAgentRequest(requestID, requestTabID, requestToken, JSON.stringify({ error: 'The external Browser tab is changing. Inspect it again after navigation finishes.', capturedAt: Date.now() }))
      return
    }
    const owner: ExternalAgentActionOwner = {
      scope: scopeRef.current.generation,
      requestID,
      tabID: requestTabID,
      bridgeToken: requestToken,
    }
    externalAgentInFlightRef.current = owner
    setExternalAgentBusy({ requestID, action })
    void runExternalAgentCommand(requestID, request.args || { action: 'inspect' })
      .then((payload) => ResolveExternalBrowserAgentRequest(requestID, requestTabID, requestToken, JSON.stringify(payload)))
      .catch((reason) => ResolveExternalBrowserAgentRequest(requestID, requestTabID, requestToken, JSON.stringify({ error: String(reason?.message || reason), capturedAt: Date.now() })))
      .finally(() => {
        const current = externalAgentInFlightRef.current
        if (current?.scope === owner.scope && current.requestID === owner.requestID && current.tabID === owner.tabID && current.bridgeToken === owner.bridgeToken) {
          externalAgentInFlightRef.current = null
        }
        if (ownsScope(owner.scope)) setExternalAgentBusy((value) => value?.requestID === requestID ? null : value)
      })
  }), [activeExternalTab, ownsScope, projectId, runExternalAgentCommand, sessionId])

  useEffect(() => {
    if (!externalNavigationRequest || externalNavigationRequest.key <= handledExternalNavigationRef.current) return
    if (externalBusy || externalAgentBusy || inspecting) return
    const { key, url } = externalNavigationRequest
    void requestExternalNavigation(url, undefined, undefined, true).then((started) => {
      if (started) handledExternalNavigationRef.current = Math.max(handledExternalNavigationRef.current, key)
    })
  }, [externalAgentBusy, externalBusy, externalNavigationRequest, inspecting, requestExternalNavigation])

  const loadPersistence = useCallback(async () => {
    if (persistenceActionInFlightRef.current) return
    const request = ++persistenceRequestRef.current
    if (!selected?.name) {
      setPersistence({ enabled: false, hasData: false, localStorageEntries: 0, cookies: 0 })
      return
    }
    try {
      const raw: any = await GetPreviewSessionPersistence(projectId, sessionId, selected.name)
      if (persistenceRequestRef.current === request) setPersistence(raw as PreviewSessionPersistence)
    } catch (reason: any) {
      if (persistenceRequestRef.current === request && !staticPathRef.current) setError(String(reason?.message || reason))
    }
  }, [projectId, selected?.name, sessionId])

  useEffect(() => {
    void loadPersistence()
    return () => { persistenceRequestRef.current++ }
  }, [loadPersistence])

  const runPreviewCommand = useCallback(async (args: Record<string, unknown>, suppliedRequestId?: string) => {
    const target = frameRef.current?.contentWindow
    const token = status.bridgeToken
    const configuration = status.configuration
    if (!target || !token || status.state !== 'running') throw new Error('Preview diagnostics bridge is not ready')
    if (
      previewCommandInFlightRef.current ||
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current ||
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      closingExternalTabIDRef.current
    ) {
      throw new Error('Another Preview interaction is already in progress. Wait for it to finish and inspect again.')
    }
    const requestId = suppliedRequestId || `inspect-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`
    const action = typeof args.action === 'string' ? args.action : 'inspect'
    const owner: PreviewCommandOwner = {
      scope: scopeRef.current.generation,
      requestID: requestId,
      configuration,
      bridgeToken: token,
      action,
      target,
    }
    previewCommandInFlightRef.current = owner
    setInspecting(true)
    try {
      const isElementSelection = action === 'select_element'
      const result = await new Promise<PreviewInspection>((resolve, reject) => {
        const timer = window.setTimeout(() => {
          pendingInspectionRef.current.delete(requestId)
          reject(new Error(isElementSelection
            ? 'Element selection timed out. Choose Select element again when the preview is ready.'
            : 'Preview did not answer the diagnostics request. Its CSP or page lifecycle may block the bridge.'))
        }, isElementSelection ? 60_000 : 5000)
        pendingInspectionRef.current.set(requestId, { resolve, reject, timer })
        target.postMessage({ type: 'gokin-preview-command', token, requestId, args }, '*')
      })
      if (!ownsPreviewCommand(owner)) throw new Error('The Preview frame changed while the interaction was running. Inspect the visible frame again.')
      if (result.error) throw new Error(result.error)
      if (isElementSelection) return result
      const normalized: PreviewInspection = {
        ...result,
        url: typeof result.url === 'string' ? result.url : '',
        title: typeof result.title === 'string' ? result.title : '',
        readyState: typeof result.readyState === 'string' ? result.readyState : '',
        viewport: result.viewport || { width: 0, height: 0, devicePixelRatio: 1 },
        text: typeof result.text === 'string' ? result.text : '',
        headings: Array.isArray(result.headings) ? result.headings : [],
        controls: Array.isArray(result.controls) ? result.controls : [],
        issues: Array.isArray(result.issues) ? result.issues : [],
        capturedAt: typeof result.capturedAt === 'number' ? result.capturedAt : Date.now(),
      }
      setInspection(normalized)
      return normalized
    } finally {
      if (!suppliedRequestId && previewCommandInFlightRef.current === owner) {
        previewCommandInFlightRef.current = null
        if (ownsScope(owner.scope)) setInspecting(false)
      }
    }
  }, [ownsPreviewCommand, ownsScope, status.bridgeToken, status.configuration, status.state])

  const inspectPreview = useCallback(() => {
    if (
      previewCommandInFlightRef.current ||
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current ||
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      closingExternalTabIDRef.current
    ) {
      return Promise.resolve<PreviewInspection | null>(null)
    }
    return runPreviewCommand({ action: 'inspect', screenshot: true })
  }, [runPreviewCommand])

  useEffect(() => {
    let active = true
    const onMessage = (event: MessageEvent) => {
      if (event.source !== frameRef.current?.contentWindow) return
      const data = event.data
      if (!data || data.token !== status.bridgeToken) return
      if (data.type === 'gokin-preview-select-request') {
        startElementSelectionRef.current()
        return
      }
      if (data.type === 'gokin-preview-storage' && selected?.name && data.payload) {
        void SavePreviewBrowserStorage(projectId, sessionId, selected.name, status.bridgeToken || '', JSON.stringify(data.payload))
          .then(() => {
            if (!active) return
            if (persistenceRefreshTimerRef.current !== null) window.clearTimeout(persistenceRefreshTimerRef.current)
            const timer = window.setTimeout(() => {
              if (persistenceRefreshTimerRef.current === timer) persistenceRefreshTimerRef.current = null
              if (active) void loadPersistence()
            }, 400)
            persistenceRefreshTimerRef.current = timer
          })
          .catch(() => { /* a stopped/replaced preview invalidates late pagehide snapshots */ })
        return
      }
      if (data.type !== 'gokin-preview-result' || typeof data.requestId !== 'string') return
      const pending = pendingInspectionRef.current.get(data.requestId)
      if (!pending) return
      window.clearTimeout(pending.timer)
      pendingInspectionRef.current.delete(data.requestId)
      pending.resolve(data.payload as PreviewInspection)
    }
    window.addEventListener('message', onMessage)
    return () => {
      active = false
      window.removeEventListener('message', onMessage)
      const commandOwner = previewCommandInFlightRef.current
      if (
        commandOwner &&
        commandOwner.configuration === status.configuration &&
        commandOwner.bridgeToken === (status.bridgeToken || '')
      ) {
        previewCommandInFlightRef.current = null
        if (ownsScope(commandOwner.scope)) setInspecting(false)
      }
      for (const pending of pendingInspectionRef.current.values()) {
        window.clearTimeout(pending.timer)
        pending.reject(new Error('The preview bridge was replaced or closed.'))
      }
      pendingInspectionRef.current.clear()
      if (persistenceRefreshTimerRef.current !== null) {
        window.clearTimeout(persistenceRefreshTimerRef.current)
        persistenceRefreshTimerRef.current = null
      }
    }
  }, [loadPersistence, ownsScope, projectId, selected?.name, sessionId, status.bridgeToken, status.configuration])

  const startElementSelection = useCallback(() => {
    if (
      selectingElementRef.current ||
      previewCommandInFlightRef.current ||
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current ||
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      closingExternalTabIDRef.current
    ) return false
    const scope = scopeRef.current.generation
    const request = ++elementSelectionRequestRef.current
    const ownsSelection = () => ownsScope(scope) && elementSelectionRequestRef.current === request
    selectingElementRef.current = true
    setSelectingElement(true)
    setError(null)
    void runPreviewCommand({ action: 'select_element', screenshot: false })
      .then((result) => {
        if (!ownsSelection() || result.cancelled) return
        const selection = normalizePreviewElementSelection(result.selectedElement)
        if (!selection) throw new Error('The preview returned incomplete element metadata. Reload it and try again.')
        composeInChat(previewElementDraft(selection), 'replace')
      })
      .catch((reason: any) => {
        if (ownsSelection()) setError(String(reason?.message || reason))
      })
      .finally(() => {
        if (!ownsSelection()) return
        selectingElementRef.current = false
        setSelectingElement(false)
      })
    return true
  }, [ownsScope, runPreviewCommand])
  startElementSelectionRef.current = () => { void startElementSelection() }

  const cancelElementSelection = useCallback(() => {
    const target = frameRef.current?.contentWindow
    const token = status.bridgeToken
    if (!target || !token) {
      elementSelectionRequestRef.current += 1
      selectingElementRef.current = false
      setSelectingElement(false)
      const owner = previewCommandInFlightRef.current
      if (owner?.action === 'select_element') {
        const pending = pendingInspectionRef.current.get(owner.requestID)
        if (pending) {
          window.clearTimeout(pending.timer)
          pendingInspectionRef.current.delete(owner.requestID)
          pending.reject(new Error('Element selection was cancelled because the Preview frame closed.'))
        }
      }
      return
    }
    target.postMessage({
      type: 'gokin-preview-command',
      token,
      requestId: `cancel-selection-${Date.now()}`,
      args: { action: 'cancel_element_selection' },
    }, '*')
  }, [status.bridgeToken])

  useEffect(() => {
    if (!selectingElement) return
    const onKey = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229 || event.key !== 'Escape') return
      event.preventDefault()
      event.stopImmediatePropagation()
      cancelElementSelection()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [cancelElementSelection, selectingElement])

  useEffect(() => {
    if (elementSelectRequest <= handledElementSelectRequestRef.current) return
    if (status.state !== 'running' || !frameLoaded || !status.bridgeToken) return
    if (startElementSelection()) handledElementSelectRequestRef.current = elementSelectRequest
  }, [elementSelectRequest, frameLoaded, inspecting, startElementSelection, status.bridgeToken, status.state])

  useEffect(() => EventsOn('preview:browser_request', (request: any) => {
    if (request?.projectID !== projectId || request?.sessionID !== sessionId) return
    const requestID = typeof request.requestID === 'string' ? request.requestID : ''
    if (!requestID) return
    const activeConfiguration = staticPath ? '__gokin_static_file__' : selected?.name
    if (request?.configuration !== activeConfiguration || request?.bridgeToken !== status.bridgeToken) {
      void ResolvePreviewBrowserRequest(requestID, JSON.stringify({ error: 'The active Preview configuration changed; inspect again after it finishes loading.', capturedAt: Date.now() }))
      return
    }
    const inFlight = previewCommandInFlightRef.current
    if (inFlight) {
      if (inFlight.requestID === requestID && inFlight.configuration === request.configuration && inFlight.bridgeToken === request.bridgeToken) return
      void ResolvePreviewBrowserRequest(requestID, JSON.stringify({ error: 'Another Preview interaction is already in progress. Inspect again after it finishes.', capturedAt: Date.now() }))
      return
    }
    if (serverActionInFlightRef.current || persistenceActionInFlightRef.current) {
      void ResolvePreviewBrowserRequest(requestID, JSON.stringify({ error: 'The Preview configuration is changing. Inspect again after it finishes loading.', capturedAt: Date.now() }))
      return
    }
    const command = runPreviewCommand(request.args || { action: 'inspect' }, requestID)
    const owner = previewCommandInFlightRef.current
    void command
      .then((payload) => ResolvePreviewBrowserRequest(requestID, JSON.stringify(payload)))
      .catch((reason) => ResolvePreviewBrowserRequest(requestID, JSON.stringify({ error: String(reason?.message || reason), capturedAt: Date.now() })))
      .catch(() => { /* a timed-out or cancelled backend request no longer owns a response */ })
      .finally(() => {
        if (owner && previewCommandInFlightRef.current === owner) {
          previewCommandInFlightRef.current = null
          if (ownsScope(owner.scope)) setInspecting(false)
        }
      })
  }), [ownsScope, projectId, runPreviewCommand, selected?.name, sessionId, staticPath, status.bridgeToken])

  useEffect(() => {
    if (!staticPath) return
    const request = ++staticRequestRef.current
    let openedToken = ''
    setError(null)
    setInspection(null)
    setFrameLoaded(false)
    setStatus({ configuration: '__gokin_static_file__', state: 'starting' })
    void OpenSessionPreviewFile(projectId, sessionId, staticPath)
      .then((raw: any) => {
        const next = raw as PreviewStatus
        openedToken = next.bridgeToken || ''
        if (staticRequestRef.current !== request) {
          if (openedToken) void CloseSessionPreviewFile(projectId, sessionId, openedToken).catch(() => { /* replacement/session cleanup already owns the server */ })
          return
        }
        setStatus(next)
      })
      .catch((reason: any) => {
        if (staticRequestRef.current !== request) return
        setStatus({ configuration: '__gokin_static_file__', state: 'failed', error: String(reason?.message || reason) })
        setError(String(reason?.message || reason))
      })
    return () => {
      if (staticRequestRef.current === request) staticRequestRef.current++
      if (openedToken) void CloseSessionPreviewFile(projectId, sessionId, openedToken).catch(() => { /* deletion/shutdown may have removed the session first */ })
    }
  }, [projectId, sessionId, staticOpenRevision, staticPath])

  const loadConfig = useCallback(async () => {
    const request = ++requestRef.current
    setLoading(true)
    setError(null)
    try {
      const raw: any = await GetSessionPreviewConfig(projectId, sessionId)
      if (requestRef.current !== request) return
      const next = (raw || { source: 'none', configurations: [] }) as PreviewConfig
      setConfig(next)
      setSelectedName((current) => next.configurations?.some((item) => item.name === current)
        ? current
        : (next.configurations?.[0]?.name || ''))
    } catch (reason: any) {
      if (requestRef.current === request && !staticPathRef.current) setError(String(reason?.message || reason))
    } finally {
      if (requestRef.current === request) setLoading(false)
    }
  }, [projectId, sessionId])

  useEffect(() => {
    void loadConfig()
    return () => { requestRef.current++ }
  }, [loadConfig])

  useEffect(() => {
    if (staticPath) return
    if (!selected?.name) {
      setStatus({ configuration: '', state: 'stopped' })
      return
    }
    let stopped = false
    let inFlight = false
    const poll = async () => {
      if (stopped || inFlight || serverActionInFlightRef.current || document.visibilityState === 'hidden') return
      inFlight = true
      const request = ++serverStatusRequestRef.current
      try {
        const raw: any = await GetSessionPreviewServerStatus(projectId, sessionId, selected.name)
        if (!stopped && !serverActionInFlightRef.current && serverStatusRequestRef.current === request) {
          const next = raw as PreviewStatus
          setStatus(next)
          if (next.state === 'failed') setLogsOpen(true)
        }
      } catch (reason: any) {
        if (!stopped && !serverActionInFlightRef.current && serverStatusRequestRef.current === request) {
          setError(String(reason?.message || reason))
        }
      } finally {
        inFlight = false
      }
    }
    void poll()
    const timer = window.setInterval(poll, 900)
    return () => { stopped = true; serverStatusRequestRef.current += 1; window.clearInterval(timer) }
  }, [projectId, selected?.name, sessionId, staticPath])

  // The Desktop autoVerify contract is richer (DOM inspection and agent
  // interaction). Studio at least keeps the visible app synchronized after a
  // completed editing turn; the footer labels this honestly as auto-refresh.
  useEffect(() => {
    if (!config?.autoVerify && !staticPath) return
    return EventsOn('chat:complete', (payload: any) => {
      if (payload?.projectID !== projectId || (payload?.sessionID && payload.sessionID !== sessionId)) return
      autoInspectRef.current = true
      setInspection(null)
      setFrameLoaded(false)
      setRevision((value) => value + 1)
    })
  }, [config?.autoVerify, projectId, sessionId, staticPath])

  const start = async () => {
    if (!selected) return
    const target = { ...selected }
    const owner = beginServerAction()
    if (!owner) return
    try {
      const attachOnly = !target.runtimeExecutable
      const previewTarget = target.url || `http://127.0.0.1:${target.port}`
      const accepted = await requestConfirmation({
        title: `${attachOnly ? 'Attach' : 'Start'} preview “${target.name}”?`,
        message: attachOnly
          ? `Studio will attach the diagnostics proxy to this reviewed local origin:\n\n${target.command}\n\nTarget: ${previewTarget}`
          : `This repository-defined command will run in the selected chat worktree with an isolated base environment. Any repository-defined env values are listed below.\n\n${target.command}\n\nPreferred target: ${previewTarget}\n\nThe process can execute project code and access the network.`,
        confirmLabel: attachOnly ? 'Attach preview' : 'Start server',
        cancelLabel: 'Cancel',
      })
      if (!accepted || !ownsServerAction(owner)) return
      if (selectedSnapshotRef.current.name !== target.name || selectedSnapshotRef.current.command !== target.command) return
      setFrameLoaded(false)
      const raw: any = await StartSessionPreviewServer(projectId, sessionId, target.name, target.command)
      if (!ownsServerAction(owner)) return
      setStatus(raw as PreviewStatus)
    } catch (reason: any) {
      if (ownsServerAction(owner)) setError(String(reason?.message || reason))
    } finally {
      finishServerAction(owner)
    }
  }

  const stop = async () => {
    if (!selected) return
    const targetName = selected.name
    const owner = beginServerAction()
    if (!owner) return
    try {
      await StopSessionPreviewServer(projectId, sessionId, targetName)
      if (!ownsServerAction(owner) || selectedSnapshotRef.current.name !== targetName) return
      setStatus((current) => ({ ...current, state: 'stopped', pid: undefined }))
      setFrameLoaded(false)
    } catch (reason: any) {
      if (ownsServerAction(owner)) setError(String(reason?.message || reason))
    } finally {
      finishServerAction(owner)
    }
  }

  const editConfiguration = () => {
    if (
      serverActionInFlightRef.current ||
      persistenceActionInFlightRef.current ||
      previewCommandInFlightRef.current ||
      externalNavigationInFlightRef.current ||
      externalAgentInFlightRef.current ||
      closingExternalTabIDRef.current
    ) return
    const prompt = config?.source === 'file'
      ? `${formatFileMention(config.path)} Open this preview-server configuration and help me edit it. Preserve version 0.0.1. Supported Claude Desktop fields are runtimeExecutable/runtimeArgs or program/args, port, cwd, env, autoPort, url, and top-level autoVerify. Keep url on localhost in this app. Do not start any server until I review the full launch summary.`
      : `Create ${formatFileMention('.claude/launch.json')} for this project using version 0.0.1. Add the correct dev server runtimeExecutable/runtimeArgs (or program/args), port, and any needed cwd, env, autoPort, loopback url, and autoVerify values. Do not start the server until I review the full launch summary.`
    composeInChat(prompt, 'replace')
    onClose()
  }

  const saveDetectedConfiguration = async () => {
    if (!selected || config?.source !== 'detected') return
    const target = { ...selected }
    const owner = beginServerAction()
    if (!owner) return
    try {
      const accepted = await requestConfirmation({
        title: 'Save detected preview configuration?',
        message: `Studio will create .claude/launch.json in this session worktree with the reviewed command:\n\n${target.command}\n\nPort: ${target.port} · autoVerify: true`,
        confirmLabel: 'Save configuration',
        cancelLabel: 'Cancel',
      })
      if (!accepted || !ownsServerAction(owner)) return
      if (selectedSnapshotRef.current.name !== target.name || selectedSnapshotRef.current.command !== target.command) return
      const raw: any = await SaveDetectedSessionPreviewConfig(projectId, sessionId, target.name, target.command)
      if (!ownsServerAction(owner)) return
      setConfig(raw as PreviewConfig)
    } catch (reason: any) {
      if (ownsServerAction(owner)) setError(String(reason?.message || reason))
    } finally {
      finishServerAction(owner)
    }
  }

  const clearFrameStorage = (target = frameRef.current?.contentWindow, token = status.bridgeToken) => {
    target?.postMessage({ type: 'gokin-preview-storage-clear', token }, '*')
  }

  const togglePersistence = async () => {
    if (!selected) return
    const targetName = selected.name
    const enabling = !persistence.enabled
    const frameTarget = frameRef.current?.contentWindow
    const bridgeToken = status.bridgeToken
    const owner = beginPersistenceAction()
    if (!owner) return
    try {
      const accepted = await requestConfirmation({
        title: enabling ? 'Persist preview sessions?' : 'Turn off persisted preview sessions?',
        message: enabling
          ? 'Studio will store this preview configuration’s cookies and localStorage separately for this chat, so logins and app state survive dev-server and desktop restarts. The bounded profile is private app data (mode 0600) and may be included in Studio backups. It can contain authentication tokens.'
          : 'Studio will delete the saved cookies and localStorage for this chat and configuration. The currently open preview will also be signed out and cleared.',
        confirmLabel: enabling ? 'Persist sessions' : 'Disable and clear',
        cancelLabel: 'Cancel',
      })
      if (!accepted || !ownsPersistenceAction(owner) || selectedSnapshotRef.current.name !== targetName) return
      const raw: any = await SetPreviewSessionPersistence(projectId, sessionId, targetName, enabling)
      if (!ownsPersistenceAction(owner) || selectedSnapshotRef.current.name !== targetName) return
      persistenceRequestRef.current += 1
      setPersistence(raw as PreviewSessionPersistence)
      if (!enabling) clearFrameStorage(frameTarget, bridgeToken)
      setFrameLoaded(false)
      schedulePreviewReload(owner, targetName)
    } catch (reason: any) {
      if (ownsPersistenceAction(owner)) setError(String(reason?.message || reason))
    } finally {
      finishPersistenceAction(owner)
    }
  }

  const clearPersistence = async () => {
    if (!selected) return
    const targetName = selected.name
    const frameTarget = frameRef.current?.contentWindow
    const bridgeToken = status.bridgeToken
    const owner = beginPersistenceAction()
    if (!owner) return
    try {
      const accepted = await requestConfirmation({
        title: 'Clear saved preview session?',
        message: `Delete ${persistence.localStorageEntries} localStorage entr${persistence.localStorageEntries === 1 ? 'y' : 'ies'} and ${persistence.cookies} cookie${persistence.cookies === 1 ? '' : 's'} saved for “${targetName}”? Persist sessions will remain enabled, but the preview will be signed out and reloaded.`,
        confirmLabel: 'Clear session data',
        cancelLabel: 'Cancel',
        danger: true,
      })
      if (!accepted || !ownsPersistenceAction(owner) || selectedSnapshotRef.current.name !== targetName) return
      const raw: any = await ClearPreviewSessionData(projectId, sessionId, targetName)
      if (!ownsPersistenceAction(owner) || selectedSnapshotRef.current.name !== targetName) return
      persistenceRequestRef.current += 1
      setPersistence(raw as PreviewSessionPersistence)
      clearFrameStorage(frameTarget, bridgeToken)
      setFrameLoaded(false)
      schedulePreviewReload(owner, targetName)
    } catch (reason: any) {
      if (ownsPersistenceAction(owner)) setError(String(reason?.message || reason))
    } finally {
      finishPersistenceAction(owner)
    }
  }

  const running = status.state === 'running'
  const starting = status.state === 'starting'
  const staticFileName = staticPath?.split('/').pop() || staticPath || ''
  const browserURL = status.browserURL || status.url
  const frameURL = running ? `${browserURL || `http://127.0.0.1:${selected?.port}`}${(browserURL || '').includes('?') ? '&' : '?'}gokin_preview_revision=${revision}` : undefined
  const externalFrameURL = activeExternalTab
    ? `${activeExternalTab.browserURL}${activeExternalTab.browserURL.includes('?') ? '&' : '?'}gokin_browser_revision=${externalRevision}`
    : undefined

  const beginResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    event.preventDefault()
    const startX = event.clientX
    const startWidth = widthRef.current
    const target = event.currentTarget
    target.setPointerCapture(event.pointerId)
    setResizing(true)
    const move = (next: PointerEvent) => updateWidth(startWidth + startX - next.clientX, false)
    const finish = () => {
      target.removeEventListener('pointermove', move)
      target.removeEventListener('pointerup', finish)
      target.removeEventListener('pointercancel', finish)
      updateWidth(widthRef.current, true)
      setResizing(false)
    }
    target.addEventListener('pointermove', move)
    target.addEventListener('pointerup', finish)
    target.addEventListener('pointercancel', finish)
  }

  return (
    <aside className={`live-preview-pane ${workspaceMode ? 'workspace-mode' : ''} ${resizing ? 'is-resizing' : ''}`} style={{ '--live-preview-width': `${width}px` } as CSSProperties} aria-label="Live app preview" aria-busy={paneInteractionBusy}>
      {!workspaceMode && <div
        className="live-preview-resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="Resize live preview"
        aria-valuemin={340}
        aria-valuemax={900}
        aria-valuenow={width}
        tabIndex={0}
        onPointerDown={beginResize}
        onDoubleClick={() => updateWidth(560)}
        onKeyDown={(event) => {
          if (event.key === 'ArrowLeft') { event.preventDefault(); updateWidth(widthRef.current + 24) }
          if (event.key === 'ArrowRight') { event.preventDefault(); updateWidth(widthRef.current - 24) }
          if (event.key === 'Home') { event.preventDefault(); updateWidth(340) }
          if (event.key === 'End') { event.preventDefault(); updateWidth(900) }
        }}
      />}
      <header className="live-preview-header">
        <div>{browserMode ? <Globe2 size={14} /> : <Monitor size={14} />}<strong>{browserMode ? 'Browser' : 'Preview'}</strong><span className={`live-preview-state ${browserMode ? (activeExternalTab?.state || 'stopped') : status.state}`}>{browserMode ? (activeExternalTab?.origin || 'new tab') : status.state}</span></div>
        <div>
          <button
            onClick={() => {
              if (
                serverActionInFlightRef.current ||
                persistenceActionInFlightRef.current ||
                previewCommandInFlightRef.current ||
                externalNavigationInFlightRef.current ||
                externalAgentInFlightRef.current ||
                closingExternalTabIDRef.current
              ) return
              if (browserMode) {
                if (activeExternalTab) {
                  setExternalFrameLoaded(false)
                  setExternalRevision((value) => value + 1)
                }
              } else if (staticPath) {
                if (status.state === 'failed') setStaticOpenRevision((value) => value + 1)
                else setRevision((value) => value + 1)
              } else {
                void loadConfig()
              }
            }}
            disabled={(browserMode && !activeExternalTab) || paneInteractionBusy}
            title={browserMode ? 'Reload page' : staticPath ? 'Reload static file' : 'Reload configuration'}
            aria-label={browserMode ? 'Reload browser page' : staticPath ? 'Reload static preview file' : 'Reload preview configuration'}
          ><RefreshCw size={13} /></button>
          <button onClick={() => {
            if (
              serverActionInFlightRef.current ||
              persistenceActionInFlightRef.current ||
              previewCommandInFlightRef.current ||
              externalNavigationInFlightRef.current ||
              externalAgentInFlightRef.current ||
              closingExternalTabIDRef.current
            ) return
            onClose()
          }} disabled={paneInteractionBusy} title="Close Browser / Preview (Cmd/Ctrl+Shift+B)" aria-label="Close Browser / Preview"><X size={14} /></button>
        </div>
      </header>

      <div className="external-browser-tabs" role="tablist" aria-label="Preview and browser tabs">
        <button type="button" role="tab" aria-selected={!browserMode} className={!browserMode ? 'active' : ''} disabled={externalInteractionBusy} onClick={() => {
          if (!cancelExternalNavigation()) return
          externalTabsLoadRequestRef.current += 1
          activeBrowserTabIDRef.current = null
          setActiveBrowserTabID(null)
        }}><Monitor size={11} /><span>Preview</span></button>
        {scopedExternalTabs.map((tab) => <div key={tab.id} className={`external-browser-tab ${activeBrowserTabID === tab.id ? 'active' : ''}`}>
          <button type="button" role="tab" aria-selected={activeBrowserTabID === tab.id} title={tab.url} disabled={externalInteractionBusy || closingExternalTabID === tab.id} onClick={() => {
            if (!cancelExternalNavigation()) return
            externalTabsLoadRequestRef.current += 1
            activeBrowserTabIDRef.current = tab.id
            setActiveBrowserTabID(tab.id)
            setBrowserAddress(tab.url)
            setExternalFrameLoaded(false)
          }}><Globe2 size={11} /><span>{tab.title || tab.origin}</span></button>
          <button type="button" className="external-browser-tab-close" title={`Close ${tab.title || tab.origin}`} aria-label={`Close ${tab.title || tab.origin}`} disabled={externalInteractionBusy || closingExternalTabID !== null} onClick={() => {
            if (cancelExternalNavigation()) void closeExternalTab(tab.id)
          }}>{closingExternalTabID === tab.id ? <Loader2 size={10} className="spin" /> : <X size={10} />}</button>
        </div>)}
        <button type="button" className={activeBrowserTabID === 'new' ? 'active external-browser-new' : 'external-browser-new'} title="New browser tab" aria-label="New browser tab" disabled={externalInteractionBusy} onClick={() => {
          if (!cancelExternalNavigation()) return
          externalTabsLoadRequestRef.current += 1
          activeBrowserTabIDRef.current = 'new'
          setActiveBrowserTabID('new')
          setBrowserAddress('')
          setError(null)
        }}><Plus size={12} /></button>
      </div>

      {browserMode ? <form className="live-preview-toolbar external-browser-address" onSubmit={(event) => { event.preventDefault(); void requestExternalNavigation(browserAddress, activeExternalTab?.id, activeExternalTab?.origin) }}>
        <ShieldCheck size={13} aria-hidden="true" />
        <input value={browserAddress} onChange={(event) => setBrowserAddress(event.target.value)} placeholder="Enter a public http(s) address" aria-label="Browser address" autoFocus={activeBrowserTabID === 'new'} spellCheck={false} disabled={browserNavigationBusy} />
        <button type="submit" disabled={!browserAddress.trim() || browserNavigationBusy}>{externalBusy ? <Loader2 size={12} className="spin" /> : 'Go'}</button>
        <button type="button" onClick={() => activeExternalTab && BrowserOpenURL(activeExternalTab.url)} disabled={!activeExternalTab} title="Open in default browser" aria-label="Open in default browser"><ExternalLink size={12} /></button>
      </form> : <div className="live-preview-toolbar">
          {staticPath ? (
            <div className="live-preview-static-path" title={staticPath}><FileCode2 size={12} /><span>{staticFileName}</span></div>
          ) : (
            <select value={selected?.name || ''} onChange={(event) => {
              if (previewCommandInFlightRef.current || serverActionInFlightRef.current || persistenceActionInFlightRef.current) return
              setSelectedName(event.target.value)
              setFrameLoaded(false)
              setError(null)
            }} disabled={configurations.length === 0 || starting || running || paneInteractionBusy} aria-label="Preview server configuration">
              {configurations.length === 0 && <option value="">No configuration</option>}
              {configurations.map((item) => <option key={item.name} value={item.name}>{item.name} · {item.url || `:${item.port}`}</option>)}
            </select>
          )}
          {staticPath ? (
            <button onClick={() => {
              if (previewCommandInFlightRef.current) return
              setError(null)
              setInspection(null)
              onStaticPathChange?.(null)
            }} disabled={paneInteractionBusy} title="Return to preview servers"><Monitor size={11} /> Servers</button>
          ) : starting || running ? (
            <button className="live-preview-stop" onClick={() => { void stop() }} disabled={paneInteractionBusy}><Square size={11} /> Stop</button>
          ) : (
            <button className="live-preview-start" onClick={() => { void start() }} disabled={!selected || paneInteractionBusy || loading}><Play size={11} /> {selected && !selected.runtimeExecutable ? 'Attach' : 'Start'}</button>
          )}
          <button onClick={() => {
            if (previewCommandInFlightRef.current || serverActionInFlightRef.current || persistenceActionInFlightRef.current) return
            setFrameLoaded(false)
            setRevision((value) => value + 1)
          }} disabled={!running || paneInteractionBusy} title="Reload page" aria-label="Reload preview page"><RefreshCw size={12} /></button>
          <button onClick={() => { void inspectPreview().catch((reason) => setError(String(reason?.message || reason))) }} disabled={!running || !frameLoaded || paneInteractionBusy || !status.bridgeToken} title="Inspect DOM and runtime errors" aria-label="Inspect preview"><ScanSearch size={12} /></button>
          <button className={selectingElement ? 'live-preview-select active' : 'live-preview-select'} onClick={() => selectingElement ? cancelElementSelection() : void startElementSelection()} disabled={!running || !frameLoaded || !status.bridgeToken || (paneInteractionBusy && !selectingElement)} title={selectingElement ? 'Cancel element selection (Esc)' : 'Select an element for the chat draft (Cmd/Ctrl+Shift+S)'} aria-label={selectingElement ? 'Cancel preview element selection' : 'Select preview element'} aria-pressed={selectingElement} aria-keyshortcuts="Control+Shift+S Meta+Shift+S"><Crosshair size={12} /></button>
          {!staticPath && <button className={`live-preview-persist ${persistence.enabled ? 'active' : ''}`} onClick={() => { void togglePersistence() }} disabled={!selected || paneInteractionBusy} title={persistence.enabled ? `Persist sessions on · ${persistence.localStorageEntries} localStorage · ${persistence.cookies} cookies` : 'Persist cookies and localStorage across preview restarts'} aria-pressed={persistence.enabled}><Database size={12} /><span>{persistence.enabled ? 'Persisting' : 'Ephemeral'}</span></button>}
          {!staticPath && persistence.enabled && persistence.hasData && <button onClick={() => { void clearPersistence() }} disabled={paneInteractionBusy} title="Clear saved preview session data" aria-label="Clear saved preview session data"><Trash2 size={12} /></button>}
          <button onClick={() => browserURL && BrowserOpenURL(browserURL)} disabled={!running || !browserURL} title="Open in browser" aria-label="Open preview in browser"><ExternalLink size={12} /></button>
        </div>}

      {pendingExternalApproval && <div className="external-browser-approval" role="alert">
        <ShieldCheck size={15} />
        <div><strong>Open this domain?</strong><span>{pendingExternalApproval.review.origin}</span><small>Public network only. Subdomains and other ports require separate approval.</small></div>
        <div><button type="button" onClick={() => { void cancelExternalNavigation() }} disabled={externalInteractionBusy}>Cancel</button><button type="button" onClick={() => { void commitExternalNavigation(pendingExternalApproval, 'once') }} disabled={externalInteractionBusy}>Allow once</button><button type="button" className="primary" onClick={() => { void commitExternalNavigation(pendingExternalApproval, 'always') }} disabled={externalInteractionBusy}>Always allow</button></div>
      </div>}

      {!browserMode && (staticPath
        ? <div className="live-preview-notice static"><FileCode2 size={12} /><span>Read-only static preview from this chat worktree · local assets allowed · external network blocked.</span></div>
        : config?.source === 'detected' && <div className="live-preview-notice"><AlertTriangle size={12} /><span>Detected from package.json · default port 3000. Save a configuration to customize it.</span></div>)}
      {!browserMode && selectingElement && <div className="live-preview-selection-notice" role="status"><Crosshair size={12} /><span>Click an element in the preview · Esc cancels · the page click will not run.</span></div>}
      {!browserMode && (busy || persistenceBusy) && <div className="live-preview-notice action" role="status" aria-live="polite">
        <Loader2 size={12} className="spin" />
        <span>{persistenceBusy ? 'Updating this configuration’s isolated preview session data…' : 'Updating the selected preview server…'}</span>
      </div>}
      {!browserMode && inspecting && !selectingElement && <div className="live-preview-notice action" role="status" aria-live="polite">
        <Loader2 size={12} className="spin" />
        <span>Running one interaction in the exact visible Preview frame…</span>
      </div>}
      {browserMode && externalAgentBusy && <div className="external-browser-agent-notice" role="status" aria-live="polite">
        <ScanSearch size={13} className="pulse" />
        <div><strong>Approved model action in progress</strong><span>{externalAgentBusy.action === 'inspect' ? 'Reading the visible page and capturing bounded diagnostics' : `Running one reviewed ${externalAgentBusy.action} action in the active tab`}</span></div>
      </div>}
      {error && <div className="live-preview-error"><AlertTriangle size={13} /><span>{error}</span></div>}
      {!browserMode && inspection && (
        <div className={`live-preview-inspection ${inspection.issues.length > 0 ? 'warning' : 'ok'}`}>
          {inspection.issues.length > 0 ? <AlertTriangle size={13} /> : <CheckCircle2 size={13} />}
          <div><strong>{inspection.issues.length > 0 ? `${inspection.issues.length} runtime issue${inspection.issues.length === 1 ? '' : 's'}` : 'Runtime inspection passed'}</strong><span>{inspection.title || inspection.url} · {inspection.controls.length} visible controls · {inspection.headings.length} headings</span></div>
          <button onClick={() => composeInChat(`Verify and fix the running app preview at ${inspection.url}. Use this captured DOM/runtime evidence from the active chat worktree:\n\n${JSON.stringify({ title: inspection.title, viewport: inspection.viewport, headings: inspection.headings, controls: inspection.controls, issues: inspection.issues, visibleText: inspection.text.slice(0, 12000) }, null, 2)}\n\nReproduce each issue, make the smallest correct fix, run relevant tests, then inspect the preview again before finishing.`, 'replace')}>Send to agent</button>
        </div>
      )}

      <div className="live-preview-content">
        {browserMode ? (activeExternalTab && externalFrameURL ? (
          <div className="live-preview-frame-shell external-browser-frame">
            {!externalFrameLoaded && <div className="live-preview-frame-loading"><Loader2 size={18} className="spin" /> Loading isolated page…</div>}
            <iframe ref={externalFrameRef} key={externalFrameURL} src={externalFrameURL} title={`Browser: ${activeExternalTab.title || activeExternalTab.origin}`} sandbox="allow-scripts allow-same-origin allow-forms allow-modals allow-downloads" referrerPolicy="no-referrer" onLoad={() => setExternalFrameLoaded(true)} />
          </div>
        ) : (
          <div className="live-preview-empty external-browser-empty"><Globe2 size={27} /><strong>Browse a public site</strong><span>Enter an http(s) address above. Each domain is reviewed before it opens, and private or local network addresses are blocked.</span></div>
        )) : !staticPath && loading ? (
          <div className="live-preview-empty"><Loader2 size={20} className="spin" /><span>Reading preview configuration…</span></div>
        ) : !staticPath && configurations.length === 0 ? (
          <div className="live-preview-empty"><FileCode2 size={25} /><strong>No preview server configured</strong><span>Add `.claude/launch.json`, or a package.json dev/start script for detection.</span><button onClick={editConfiguration}><FileCode2 size={12} /> Create configuration</button></div>
        ) : starting ? (
          <div className="live-preview-empty"><Loader2 size={22} className="spin" /><strong>Opening {staticPath ? staticFileName : selected?.name}</strong><span>{staticPath ? 'Preparing isolated static preview…' : `Waiting for http://127.0.0.1:${selected?.port}`}</span></div>
        ) : running && frameURL ? (
          <div className="live-preview-frame-shell">
            {!frameLoaded && <div className="live-preview-frame-loading"><Loader2 size={18} className="spin" /> Loading app…</div>}
            <iframe
              ref={frameRef}
              key={frameURL}
              src={frameURL}
              title={`Live preview: ${selected?.name}`}
              sandbox="allow-scripts allow-same-origin allow-forms allow-modals allow-popups allow-downloads"
              referrerPolicy="no-referrer"
              onLoad={() => {
                setFrameLoaded(true)
                if (autoInspectRef.current) {
                  autoInspectRef.current = false
                  window.setTimeout(() => { void inspectPreview().catch((reason) => setError(String(reason?.message || reason))) }, 500)
                }
              }}
            />
          </div>
        ) : (
          <div className="live-preview-empty"><Monitor size={25} /><strong>{status.state === 'failed' ? (staticPath ? 'Preview unavailable' : 'Server exited') : (staticPath ? staticFileName : selected?.name)}</strong><span>{status.error || (staticPath ? staticPath : selected?.command)}</span>{staticPath ? <button onClick={() => { if (!previewCommandInFlightRef.current) setStaticOpenRevision((value) => value + 1) }} disabled={paneInteractionBusy}><RefreshCw size={12} /> Retry</button> : <button onClick={() => { void start() }} disabled={paneInteractionBusy}><Play size={12} /> Review and start</button>}</div>
        )}
      </div>

      {!browserMode && !staticPath && <div className={`live-preview-logs ${logsOpen ? 'open' : ''}`}>
        <button className="live-preview-logs-toggle" onClick={() => setLogsOpen((value) => !value)} aria-expanded={logsOpen}>
          <TerminalSquare size={12} /> Server logs {status.pid ? `· PID ${status.pid}` : ''}
        </button>
        {logsOpen && <pre>{status.logs || 'No server output yet.'}</pre>}
      </div>}

      {browserMode ? <footer className="live-preview-footer external-browser-footer"><span><ShieldCheck size={10} /> Isolated proxy · {activeExternalTab?.activeScripts ? 'native navigation guard' : 'active page scripts blocked'} · exact-action review</span><span>{activeExternalTab?.origin || 'No domain open'}</span></footer> : <footer className="live-preview-footer">
          <span>{staticPath ? 'Auto-reload and DOM/screenshot inspection use the selected chat worktree' : config?.autoVerify ? 'Auto-inspect DOM and runtime errors after completed turns' : 'Auto-verify disabled by configuration'}</span>
          {staticPath
            ? <button onClick={() => composeInChat(`${formatFileMention(staticPath)} `, 'replace')}><FileCode2 size={11} /> Add to chat</button>
            : <button onClick={config?.source === 'detected' ? () => { void saveDetectedConfiguration() } : editConfiguration} disabled={paneInteractionBusy}><FileCode2 size={11} /> {config?.source === 'file' ? 'Edit configuration' : 'Save configuration'}</button>}
        </footer>}
      {confirmationDialog}
    </aside>
  )
}
