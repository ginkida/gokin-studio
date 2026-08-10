import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from 'react'
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
  const [pendingExternalApproval, setPendingExternalApproval] = useState<{ review: ExternalNavigationReview; tabID?: string } | null>(null)
  const frameRef = useRef<HTMLIFrameElement>(null)
  const externalFrameRef = useRef<HTMLIFrameElement>(null)
  const handledExternalNavigationRef = useRef(0)
  const pendingInspectionRef = useRef<Map<string, { resolve: (value: PreviewInspection) => void; reject: (reason: Error) => void; timer: number }>>(new Map())
  const pendingExternalAgentRef = useRef<Map<string, { resolve: (value: Record<string, unknown>) => void; reject: (reason: Error) => void; timer: number }>>(new Map())
  const activeExternalSyncRef = useRef<Promise<unknown>>(Promise.resolve())
  const selectingElementRef = useRef(false)
  const startElementSelectionRef = useRef<() => void>(() => {})
  const handledElementSelectRequestRef = useRef(0)
  const autoInspectRef = useRef(false)
  const persistenceRequestRef = useRef(0)
  const persistenceRefreshTimerRef = useRef<number | null>(null)
  const staticRequestRef = useRef(0)
  const staticPathRef = useRef(staticPath)
  staticPathRef.current = staticPath
  const [resizing, setResizing] = useState(false)
  const { width, widthRef, updateWidth } = usePersistentPanelWidth('gokin:live-preview-width', 560, 340, 900)

  const configurations = config?.configurations || []
  const selected = useMemo(
    () => configurations.find((item) => item.name === selectedName) || configurations[0],
    [configurations, selectedName],
  )
  const browserMode = activeBrowserTabID !== null
  const activeExternalTab = useMemo(
    () => externalTabs.find((tab) => tab.id === activeBrowserTabID) || null,
    [activeBrowserTabID, externalTabs],
  )

  const upsertExternalTab = useCallback((next: ExternalBrowserTab) => {
    setExternalTabs((current) => {
      const index = current.findIndex((tab) => tab.id === next.id)
      if (index < 0) return [...current, next]
      const copy = current.slice()
      copy[index] = next
      return copy
    })
    setActiveBrowserTabID(next.id)
    setBrowserAddress(next.url)
    setExternalFrameLoaded(false)
  }, [])

  useEffect(() => {
    let cancelled = false
    void ListExternalBrowserTabs(projectId, sessionId)
      .then((raw: any) => {
        if (cancelled) return
        const tabs = (raw || []) as ExternalBrowserTab[]
        setExternalTabs(tabs)
        const active = tabs.find((tab) => tab.active)
        if (active) {
          setActiveBrowserTabID(active.id)
          setBrowserAddress(active.url)
        }
      })
      .catch((reason: any) => { if (!cancelled) setError(String(reason?.message || reason)) })
    return () => { cancelled = true }
  }, [projectId, sessionId])

  useEffect(() => {
    const tabID = activeExternalTab?.id || ''
    activeExternalSyncRef.current = activeExternalSyncRef.current
      .catch(() => undefined)
      .then(() => SetActiveExternalBrowserTab(projectId, sessionId, tabID))
      .catch((reason: any) => { if (tabID) setError(String(reason?.message || reason)) })
  }, [activeExternalTab?.id, projectId, sessionId])

  useEffect(() => () => {
    activeExternalSyncRef.current = activeExternalSyncRef.current
      .catch(() => undefined)
      .then(() => SetActiveExternalBrowserTab(projectId, sessionId, ''))
      .catch(() => undefined)
  }, [projectId, sessionId])

  const commitExternalNavigation = useCallback(async (review: ExternalNavigationReview, approval: 'existing' | 'once' | 'always', tabID?: string) => {
    if (externalBusy) return
    setExternalBusy(true)
    setError(null)
    try {
      const raw: any = tabID
        ? await NavigateExternalBrowserTab(projectId, sessionId, tabID, review.url, approval)
        : await OpenExternalBrowserTab(projectId, sessionId, review.url, approval)
      upsertExternalTab(raw as ExternalBrowserTab)
      setPendingExternalApproval(null)
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      setExternalBusy(false)
    }
  }, [externalBusy, projectId, sessionId, upsertExternalTab])

  const requestExternalNavigation = useCallback(async (rawURL: string, tabID?: string) => {
    const trimmed = rawURL.trim()
    if (!trimmed || externalBusy) return
    const candidate = /^https?:\/\//i.test(trimmed) ? trimmed : `https://${trimmed}`
    setExternalBusy(true)
    setError(null)
    try {
      const raw: any = await ReviewExternalBrowserNavigation(candidate)
      const review = raw as ExternalNavigationReview
      setBrowserAddress(review.url)
      if (review.approved || (tabID && externalTabs.find((tab) => tab.id === tabID)?.origin === review.origin)) {
        setExternalBusy(false)
        await commitExternalNavigation(review, 'existing', tabID)
        return
      }
      setPendingExternalApproval({ review, tabID })
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      setExternalBusy(false)
    }
  }, [commitExternalNavigation, externalBusy, externalTabs])

  const closeExternalTab = useCallback(async (tabID: string) => {
    try {
      await CloseExternalBrowserTab(projectId, sessionId, tabID)
      setExternalTabs((current) => current.filter((tab) => tab.id !== tabID))
      setPendingExternalApproval((current) => current?.tabID === tabID ? null : current)
      setActiveBrowserTabID((current) => current === tabID ? null : current)
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    }
  }, [projectId, sessionId])

  useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (!activeExternalTab || event.source !== externalFrameRef.current?.contentWindow) return
      const data = event.data
      if (!data || data.token !== activeExternalTab.bridgeToken || data.tabID !== activeExternalTab.id) return
      if (data.type === 'gokin-external-navigation' && typeof data.url === 'string') {
        void requestExternalNavigation(data.url, activeExternalTab.id)
        return
      }
      if (data.type === 'gokin-external-ready') {
        const title = typeof data.title === 'string' && data.title.trim() ? data.title.trim().slice(0, 160) : activeExternalTab.title
        const nextURL = typeof data.url === 'string' ? data.url : activeExternalTab.url
        void UpdateExternalBrowserTabState(projectId, sessionId, activeExternalTab.id, activeExternalTab.bridgeToken, nextURL, title)
          .then(() => {
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
      window.removeEventListener('message', onMessage)
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
    setExternalAgentBusy({ requestID, action })
    void runExternalAgentCommand(requestID, request.args || { action: 'inspect' })
      .then((payload) => ResolveExternalBrowserAgentRequest(requestID, requestTabID, requestToken, JSON.stringify(payload)))
      .catch((reason) => ResolveExternalBrowserAgentRequest(requestID, requestTabID, requestToken, JSON.stringify({ error: String(reason?.message || reason), capturedAt: Date.now() })))
      .finally(() => setExternalAgentBusy((current) => current?.requestID === requestID ? null : current))
  }), [activeExternalTab, projectId, runExternalAgentCommand, sessionId])

  useEffect(() => {
    if (!externalNavigationRequest || externalNavigationRequest.key <= handledExternalNavigationRef.current) return
    handledExternalNavigationRef.current = externalNavigationRequest.key
    setActiveBrowserTabID('new')
    setBrowserAddress(externalNavigationRequest.url)
    setPendingExternalApproval(null)
    void requestExternalNavigation(externalNavigationRequest.url)
  }, [externalNavigationRequest, requestExternalNavigation])

  const loadPersistence = useCallback(async () => {
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
    if (!target || !token || status.state !== 'running') throw new Error('Preview diagnostics bridge is not ready')
    setInspecting(true)
    try {
      const requestId = suppliedRequestId || `inspect-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`
      const isElementSelection = args.action === 'select_element'
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
      setInspecting(false)
    }
  }, [status.bridgeToken, status.state])

  const inspectPreview = useCallback(() => runPreviewCommand({ action: 'inspect', screenshot: true }), [runPreviewCommand])

  useEffect(() => {
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
            if (persistenceRefreshTimerRef.current !== null) window.clearTimeout(persistenceRefreshTimerRef.current)
            persistenceRefreshTimerRef.current = window.setTimeout(() => { void loadPersistence() }, 400)
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
      window.removeEventListener('message', onMessage)
      for (const pending of pendingInspectionRef.current.values()) {
        window.clearTimeout(pending.timer)
        pending.reject(new Error('The preview bridge was replaced or closed.'))
      }
      pendingInspectionRef.current.clear()
      if (persistenceRefreshTimerRef.current !== null) window.clearTimeout(persistenceRefreshTimerRef.current)
    }
  }, [loadPersistence, projectId, selected?.name, sessionId, status.bridgeToken])

  const startElementSelection = useCallback(async () => {
    if (selectingElementRef.current) return
    selectingElementRef.current = true
    setSelectingElement(true)
    setError(null)
    try {
      const result = await runPreviewCommand({ action: 'select_element', screenshot: false })
      if (result.cancelled) return
      const selection = normalizePreviewElementSelection(result.selectedElement)
      if (!selection) throw new Error('The preview returned incomplete element metadata. Reload it and try again.')
      composeInChat(previewElementDraft(selection), 'replace')
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      selectingElementRef.current = false
      setSelectingElement(false)
    }
  }, [runPreviewCommand])
  startElementSelectionRef.current = () => { void startElementSelection() }

  const cancelElementSelection = useCallback(() => {
    const target = frameRef.current?.contentWindow
    const token = status.bridgeToken
    if (!target || !token) {
      selectingElementRef.current = false
      setSelectingElement(false)
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
    handledElementSelectRequestRef.current = elementSelectRequest
    void startElementSelection()
  }, [elementSelectRequest, frameLoaded, startElementSelection, status.bridgeToken, status.state])

  useEffect(() => EventsOn('preview:browser_request', (request: any) => {
    if (request?.projectID !== projectId || request?.sessionID !== sessionId) return
    const activeConfiguration = staticPath ? '__gokin_static_file__' : selected?.name
    if (request?.configuration !== activeConfiguration || request?.bridgeToken !== status.bridgeToken) {
      void ResolvePreviewBrowserRequest(request.requestID, JSON.stringify({ error: 'The active Preview configuration changed; inspect again after it finishes loading.', capturedAt: Date.now() }))
      return
    }
    void runPreviewCommand(request.args || { action: 'inspect' }, request.requestID)
      .then((payload) => ResolvePreviewBrowserRequest(request.requestID, JSON.stringify(payload)))
      .catch((reason) => ResolvePreviewBrowserRequest(request.requestID, JSON.stringify({ error: String(reason?.message || reason), capturedAt: Date.now() })))
  }), [projectId, runPreviewCommand, selected?.name, sessionId, staticPath, status.bridgeToken])

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
      if (stopped || inFlight || document.visibilityState === 'hidden') return
      inFlight = true
      try {
        const raw: any = await GetSessionPreviewServerStatus(projectId, sessionId, selected.name)
        if (!stopped) {
          const next = raw as PreviewStatus
          setStatus(next)
          if (next.state === 'failed') setLogsOpen(true)
        }
      } catch (reason: any) {
        if (!stopped) setError(String(reason?.message || reason))
      } finally {
        inFlight = false
      }
    }
    void poll()
    const timer = window.setInterval(poll, 900)
    return () => { stopped = true; window.clearInterval(timer) }
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
    if (!selected || busy) return
    const attachOnly = !selected.runtimeExecutable
    const previewTarget = selected.url || `http://127.0.0.1:${selected.port}`
    const accepted = await requestConfirmation({
      title: `${attachOnly ? 'Attach' : 'Start'} preview “${selected.name}”?`,
      message: attachOnly
        ? `Studio will attach the diagnostics proxy to this reviewed local origin:\n\n${selected.command}\n\nTarget: ${previewTarget}`
        : `This repository-defined command will run in the selected chat worktree with an isolated base environment. Any repository-defined env values are listed below.\n\n${selected.command}\n\nPreferred target: ${previewTarget}\n\nThe process can execute project code and access the network.`,
      confirmLabel: attachOnly ? 'Attach preview' : 'Start server',
      cancelLabel: 'Cancel',
    })
    if (!accepted) return
    setBusy(true)
    setError(null)
    setFrameLoaded(false)
    try {
      const raw: any = await StartSessionPreviewServer(projectId, sessionId, selected.name, selected.command)
      setStatus(raw as PreviewStatus)
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      setBusy(false)
    }
  }

  const stop = async () => {
    if (!selected || busy) return
    setBusy(true)
    setError(null)
    try {
      await StopSessionPreviewServer(projectId, sessionId, selected.name)
      setStatus((current) => ({ ...current, state: 'stopped', pid: undefined }))
      setFrameLoaded(false)
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      setBusy(false)
    }
  }

  const editConfiguration = () => {
    const prompt = config?.source === 'file'
      ? `${formatFileMention(config.path)} Open this preview-server configuration and help me edit it. Preserve version 0.0.1. Supported Claude Desktop fields are runtimeExecutable/runtimeArgs or program/args, port, cwd, env, autoPort, url, and top-level autoVerify. Keep url on localhost in this app. Do not start any server until I review the full launch summary.`
      : `Create ${formatFileMention('.claude/launch.json')} for this project using version 0.0.1. Add the correct dev server runtimeExecutable/runtimeArgs (or program/args), port, and any needed cwd, env, autoPort, loopback url, and autoVerify values. Do not start the server until I review the full launch summary.`
    composeInChat(prompt, 'replace')
    onClose()
  }

  const saveDetectedConfiguration = async () => {
    if (!selected || config?.source !== 'detected' || busy) return
    const accepted = await requestConfirmation({
      title: 'Save detected preview configuration?',
      message: `Studio will create .claude/launch.json in this session worktree with the reviewed command:\n\n${selected.command}\n\nPort: ${selected.port} · autoVerify: true`,
      confirmLabel: 'Save configuration',
      cancelLabel: 'Cancel',
    })
    if (!accepted) return
    setBusy(true)
    setError(null)
    try {
      const raw: any = await SaveDetectedSessionPreviewConfig(projectId, sessionId, selected.name, selected.command)
      setConfig(raw as PreviewConfig)
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      setBusy(false)
    }
  }

  const clearFrameStorage = () => {
    frameRef.current?.contentWindow?.postMessage({ type: 'gokin-preview-storage-clear', token: status.bridgeToken }, '*')
  }

  const togglePersistence = async () => {
    if (!selected || persistenceBusy) return
    const enabling = !persistence.enabled
    const accepted = await requestConfirmation({
      title: enabling ? 'Persist preview sessions?' : 'Turn off persisted preview sessions?',
      message: enabling
        ? 'Studio will store this preview configuration’s cookies and localStorage separately for this chat, so logins and app state survive dev-server and desktop restarts. The bounded profile is private app data (mode 0600) and may be included in Studio backups. It can contain authentication tokens.'
        : 'Studio will delete the saved cookies and localStorage for this chat and configuration. The currently open preview will also be signed out and cleared.',
      confirmLabel: enabling ? 'Persist sessions' : 'Disable and clear',
      cancelLabel: 'Cancel',
    })
    if (!accepted) return
    setPersistenceBusy(true)
    setError(null)
    try {
      const raw: any = await SetPreviewSessionPersistence(projectId, sessionId, selected.name, enabling)
      setPersistence(raw as PreviewSessionPersistence)
      if (!enabling) clearFrameStorage()
      setFrameLoaded(false)
      window.setTimeout(() => setRevision((value) => value + 1), 60)
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      setPersistenceBusy(false)
    }
  }

  const clearPersistence = async () => {
    if (!selected || persistenceBusy) return
    const accepted = await requestConfirmation({
      title: 'Clear saved preview session?',
      message: `Delete ${persistence.localStorageEntries} localStorage entr${persistence.localStorageEntries === 1 ? 'y' : 'ies'} and ${persistence.cookies} cookie${persistence.cookies === 1 ? '' : 's'} saved for “${selected.name}”? Persist sessions will remain enabled, but the preview will be signed out and reloaded.`,
      confirmLabel: 'Clear session data',
      cancelLabel: 'Cancel',
      danger: true,
    })
    if (!accepted) return
    setPersistenceBusy(true)
    setError(null)
    try {
      const raw: any = await ClearPreviewSessionData(projectId, sessionId, selected.name)
      setPersistence(raw as PreviewSessionPersistence)
      clearFrameStorage()
      setFrameLoaded(false)
      window.setTimeout(() => setRevision((value) => value + 1), 60)
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      setPersistenceBusy(false)
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
    <aside className={`live-preview-pane ${workspaceMode ? 'workspace-mode' : ''} ${resizing ? 'is-resizing' : ''}`} style={{ '--live-preview-width': `${width}px` } as CSSProperties} aria-label="Live app preview">
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
            onClick={() => browserMode
              ? (activeExternalTab && (setExternalFrameLoaded(false), setExternalRevision((value) => value + 1)))
              : staticPath
                ? (status.state === 'failed' ? setStaticOpenRevision((value) => value + 1) : setRevision((value) => value + 1))
                : void loadConfig()}
            disabled={browserMode && !activeExternalTab}
            title={browserMode ? 'Reload page' : staticPath ? 'Reload static file' : 'Reload configuration'}
            aria-label={browserMode ? 'Reload browser page' : staticPath ? 'Reload static preview file' : 'Reload preview configuration'}
          ><RefreshCw size={13} /></button>
          <button onClick={onClose} title="Close Browser / Preview (Cmd/Ctrl+Shift+B)" aria-label="Close Browser / Preview"><X size={14} /></button>
        </div>
      </header>

      <div className="external-browser-tabs" role="tablist" aria-label="Preview and browser tabs">
        <button type="button" role="tab" aria-selected={!browserMode} className={!browserMode ? 'active' : ''} onClick={() => { setActiveBrowserTabID(null); setPendingExternalApproval(null) }}><Monitor size={11} /><span>Preview</span></button>
        {externalTabs.map((tab) => <div key={tab.id} className={`external-browser-tab ${activeBrowserTabID === tab.id ? 'active' : ''}`}>
          <button type="button" role="tab" aria-selected={activeBrowserTabID === tab.id} title={tab.url} onClick={() => { setActiveBrowserTabID(tab.id); setBrowserAddress(tab.url); setPendingExternalApproval(null); setExternalFrameLoaded(false) }}><Globe2 size={11} /><span>{tab.title || tab.origin}</span></button>
          <button type="button" className="external-browser-tab-close" title={`Close ${tab.title || tab.origin}`} aria-label={`Close ${tab.title || tab.origin}`} onClick={() => { void closeExternalTab(tab.id) }}><X size={10} /></button>
        </div>)}
        <button type="button" className={activeBrowserTabID === 'new' ? 'active external-browser-new' : 'external-browser-new'} title="New browser tab" aria-label="New browser tab" onClick={() => { setActiveBrowserTabID('new'); setBrowserAddress(''); setPendingExternalApproval(null); setError(null) }}><Plus size={12} /></button>
      </div>

      {browserMode ? <form className="live-preview-toolbar external-browser-address" onSubmit={(event) => { event.preventDefault(); void requestExternalNavigation(browserAddress, activeExternalTab?.id) }}>
        <ShieldCheck size={13} aria-hidden="true" />
        <input value={browserAddress} onChange={(event) => setBrowserAddress(event.target.value)} placeholder="Enter a public http(s) address" aria-label="Browser address" autoFocus={activeBrowserTabID === 'new'} spellCheck={false} />
        <button type="submit" disabled={!browserAddress.trim() || externalBusy}>{externalBusy ? <Loader2 size={12} className="spin" /> : 'Go'}</button>
        <button type="button" onClick={() => activeExternalTab && BrowserOpenURL(activeExternalTab.url)} disabled={!activeExternalTab} title="Open in default browser" aria-label="Open in default browser"><ExternalLink size={12} /></button>
      </form> : <div className="live-preview-toolbar">
          {staticPath ? (
            <div className="live-preview-static-path" title={staticPath}><FileCode2 size={12} /><span>{staticFileName}</span></div>
          ) : (
            <select value={selected?.name || ''} onChange={(event) => { setSelectedName(event.target.value); setFrameLoaded(false); setError(null) }} disabled={configurations.length === 0 || starting || running} aria-label="Preview server configuration">
              {configurations.length === 0 && <option value="">No configuration</option>}
              {configurations.map((item) => <option key={item.name} value={item.name}>{item.name} · {item.url || `:${item.port}`}</option>)}
            </select>
          )}
          {staticPath ? (
            <button onClick={() => { setError(null); setInspection(null); onStaticPathChange?.(null) }} title="Return to preview servers"><Monitor size={11} /> Servers</button>
          ) : starting || running ? (
            <button className="live-preview-stop" onClick={() => { void stop() }} disabled={busy}><Square size={11} /> Stop</button>
          ) : (
            <button className="live-preview-start" onClick={() => { void start() }} disabled={!selected || busy || loading}><Play size={11} /> {selected && !selected.runtimeExecutable ? 'Attach' : 'Start'}</button>
          )}
          <button onClick={() => { setFrameLoaded(false); setRevision((value) => value + 1) }} disabled={!running} title="Reload page" aria-label="Reload preview page"><RefreshCw size={12} /></button>
          <button onClick={() => { void inspectPreview().catch((reason) => setError(String(reason?.message || reason))) }} disabled={!running || !frameLoaded || inspecting || !status.bridgeToken} title="Inspect DOM and runtime errors" aria-label="Inspect preview"><ScanSearch size={12} /></button>
          <button className={selectingElement ? 'live-preview-select active' : 'live-preview-select'} onClick={() => selectingElement ? cancelElementSelection() : void startElementSelection()} disabled={!running || !frameLoaded || !status.bridgeToken} title={selectingElement ? 'Cancel element selection (Esc)' : 'Select an element for the chat draft (Cmd/Ctrl+Shift+S)'} aria-label={selectingElement ? 'Cancel preview element selection' : 'Select preview element'} aria-pressed={selectingElement} aria-keyshortcuts="Control+Shift+S Meta+Shift+S"><Crosshair size={12} /></button>
          {!staticPath && <button className={`live-preview-persist ${persistence.enabled ? 'active' : ''}`} onClick={() => { void togglePersistence() }} disabled={!selected || persistenceBusy} title={persistence.enabled ? `Persist sessions on · ${persistence.localStorageEntries} localStorage · ${persistence.cookies} cookies` : 'Persist cookies and localStorage across preview restarts'} aria-pressed={persistence.enabled}><Database size={12} /><span>{persistence.enabled ? 'Persisting' : 'Ephemeral'}</span></button>}
          {!staticPath && persistence.enabled && persistence.hasData && <button onClick={() => { void clearPersistence() }} disabled={persistenceBusy} title="Clear saved preview session data" aria-label="Clear saved preview session data"><Trash2 size={12} /></button>}
          <button onClick={() => browserURL && BrowserOpenURL(browserURL)} disabled={!running || !browserURL} title="Open in browser" aria-label="Open preview in browser"><ExternalLink size={12} /></button>
        </div>}

      {pendingExternalApproval && <div className="external-browser-approval" role="alert">
        <ShieldCheck size={15} />
        <div><strong>Open this domain?</strong><span>{pendingExternalApproval.review.origin}</span><small>Public network only. Subdomains and other ports require separate approval.</small></div>
        <div><button type="button" onClick={() => setPendingExternalApproval(null)} disabled={externalBusy}>Cancel</button><button type="button" onClick={() => { void commitExternalNavigation(pendingExternalApproval.review, 'once', pendingExternalApproval.tabID) }} disabled={externalBusy}>Allow once</button><button type="button" className="primary" onClick={() => { void commitExternalNavigation(pendingExternalApproval.review, 'always', pendingExternalApproval.tabID) }} disabled={externalBusy}>Always allow</button></div>
      </div>}

      {!browserMode && (staticPath
        ? <div className="live-preview-notice static"><FileCode2 size={12} /><span>Read-only static preview from this chat worktree · local assets allowed · external network blocked.</span></div>
        : config?.source === 'detected' && <div className="live-preview-notice"><AlertTriangle size={12} /><span>Detected from package.json · default port 3000. Save a configuration to customize it.</span></div>)}
      {!browserMode && selectingElement && <div className="live-preview-selection-notice" role="status"><Crosshair size={12} /><span>Click an element in the preview · Esc cancels · the page click will not run.</span></div>}
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
          <div className="live-preview-empty"><Monitor size={25} /><strong>{status.state === 'failed' ? (staticPath ? 'Preview unavailable' : 'Server exited') : (staticPath ? staticFileName : selected?.name)}</strong><span>{status.error || (staticPath ? staticPath : selected?.command)}</span>{staticPath ? <button onClick={() => setStaticOpenRevision((value) => value + 1)}><RefreshCw size={12} /> Retry</button> : <button onClick={() => { void start() }} disabled={busy}><Play size={12} /> Review and start</button>}</div>
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
            : <button onClick={config?.source === 'detected' ? () => { void saveDetectedConfiguration() } : editConfiguration}><FileCode2 size={11} /> {config?.source === 'file' ? 'Edit configuration' : 'Save configuration'}</button>}
        </footer>}
      {confirmationDialog}
    </aside>
  )
}
