import { useCallback, useEffect, useRef, useState } from 'react'
import { Brain, AlertOctagon, AlertTriangle, Loader2, WifiOff, RefreshCw, KeyRound } from 'lucide-react'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { useSettingsStore } from '../../stores/settingsStore'
import { CheckProviderHealth, RecentErrorCount, RecentErrors } from '../../../wailsjs/go/studio/Studio'
import { formatProviderModelLabel } from '../../lib/providerCatalog'
import { scrollIntoViewWithMotion } from '../../lib/motion'

// iter 990+: window in which recent backend errors light up the status bar.
// 5 minutes is long enough that a single error stays visible across a quick
// task switch (so the user can come back and notice it), short enough that
// yesterday's resolved hiccup doesn't permanently glow the bar red.
const ERROR_WINDOW_MS = 5 * 60 * 1000
// Poll cadence. 30s keeps perceived latency low without spamming the bridge.
const ERROR_POLL_MS = 30 * 1000
// Provider readiness is refreshed much less often than local error state.
// This endpoint lists models and consumes no completion tokens.
const PROVIDER_HEALTH_REFRESH_MS = 5 * 60 * 1000

type ProviderHealth = {
  ok: boolean
  latencyMs?: number
  statusCode?: number
  error?: string
  description?: string
  availableModels?: string[]
  recommendedModel?: string
}

export function StatusBar() {
  const activeProject = useProjectStore((s) =>
    s.projects.find((p) => p.id === s.activeProjectId)
  )
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const settings = useSettingsStore((s) => s.settings)
  const setProviderCapability = useSettingsStore((s) => s.setProviderCapability)
  // Derive "any session active" from the chat store instead of the stored
  // project.active flag — the latter flips with each per-session start/stop,
  // so a finished sibling session would grey the status dot while another
  // session was still running.
  const anyActive = useChatStore((s) => {
    if (!activeProjectId) return false
    const prefix = activeProjectId + '_'
    for (const [key, active] of Object.entries(s.sessionActive)) {
      if (active && key.startsWith(prefix)) return true
    }
    return false
  })
  const pendingQuestions = useChatStore((s) => s.askUser)
  const pendingAttention = (() => {
    if (!activeProjectId) return null
    const prefix = activeProjectId + '_'
    for (const [key, question] of Object.entries(pendingQuestions)) {
      if (question && key.startsWith(prefix)) {
        return { sessionID: key.slice(prefix.length) || 'default', question }
      }
    }
    return null
  })()

  // iter 990+: error indicator. Polls the backend's RecentErrorCount every
  // 30s. When > 0, the badge appears; click opens Settings → Logs scoped to
  // error level so the user can see what went wrong. Tooltip previews the
  // first few error messages so a glance answers "what's broken?".
  const [errorCount, setErrorCount] = useState(0)
  const [errorPreview, setErrorPreview] = useState<string>('')
  const [online, setOnline] = useState(() => typeof navigator === 'undefined' || navigator.onLine)
  const [providerHealth, setProviderHealth] = useState<ProviderHealth | null>(null)
  const [healthChecking, setHealthChecking] = useState(false)
  const healthRequestID = useRef(0)
  const healthCheckedAt = useRef(0)
  const activeProvider = activeProject?.provider || 'glm'
  const activeModel = activeProject?.model || 'glm-5.2'
  const activeCredential = activeProvider === 'kimi' ? settings.kimiKey : settings.glmKey

  useEffect(() => {
    const markOnline = () => setOnline(true)
    const markOffline = () => setOnline(false)
    window.addEventListener('online', markOnline)
    window.addEventListener('offline', markOffline)
    return () => {
      window.removeEventListener('online', markOnline)
      window.removeEventListener('offline', markOffline)
    }
  }, [])

  const checkActiveProvider = useCallback(async (silent = false) => {
    const requestID = ++healthRequestID.current
    if (!activeProjectId || !online) {
      setProviderHealth(null)
      setHealthChecking(false)
      return
    }
    const provider = activeProvider
    if (!silent) setHealthChecking(true)
    try {
      const result = await CheckProviderHealth(provider) as ProviderHealth
      if (requestID !== healthRequestID.current) return
      setProviderHealth(result)
      if (result.ok && result.availableModels && result.availableModels.length > 0) {
        setProviderCapability(provider, {
          availableModels: [...result.availableModels],
          recommendedModel: result.recommendedModel,
          checkedAt: Date.now(),
        })
      } else if (!result.ok) {
        setProviderCapability(provider, null)
      }
    } catch (error: any) {
      if (requestID !== healthRequestID.current) return
      setProviderHealth({ ok: false, error: String(error?.message || error || 'Connection check failed') })
      setProviderCapability(provider, null)
    } finally {
      if (requestID === healthRequestID.current) {
        healthCheckedAt.current = Date.now()
        // A quiet refresh can supersede an initial visible check after a
        // focus event; always settle the spinner for the newest request.
        setHealthChecking(false)
      }
    }
  }, [activeProjectId, activeProvider, online, setProviderCapability])

  // Re-check when the selected project/provider or its saved credential
  // changes. This is authentication/model discovery only and consumes no
  // completion tokens.
  useEffect(() => {
    setProviderHealth(null)
    healthCheckedAt.current = 0
    void checkActiveProvider(false)
  }, [activeProjectId, activeProvider, activeCredential, online, checkActiveProvider])

  // Refresh quietly while the app stays open and whenever the user returns
  // after the previous result aged out. Silent checks keep a healthy status
  // from flickering to "Checking…" every five minutes.
  useEffect(() => {
    if (!activeProjectId || !online) return
    const refreshIfStale = () => {
      if (Date.now() - healthCheckedAt.current >= PROVIDER_HEALTH_REFRESH_MS) {
        void checkActiveProvider(true)
      }
    }
    const timer = window.setInterval(refreshIfStale, PROVIDER_HEALTH_REFRESH_MS)
    window.addEventListener('focus', refreshIfStale)
    return () => {
      window.clearInterval(timer)
      window.removeEventListener('focus', refreshIfStale)
    }
  }, [activeProjectId, online, checkActiveProvider])
  useEffect(() => {
    let cancelled = false
    const tick = async () => {
      try {
        const n = await RecentErrorCount(ERROR_WINDOW_MS)
        if (cancelled) return
        setErrorCount(typeof n === 'number' ? n : 0)
        if (n > 0) {
          const recent = await RecentErrors(3)
          if (cancelled) return
          const lines = (recent || []).map((e: any) =>
            `${e.source}: ${(e.message || '').slice(0, 140)}`
          )
          setErrorPreview(lines.join('\n'))
        } else {
          setErrorPreview('')
        }
      } catch {
        // Backend down / Wails bridge mid-startup — silently keep previous count.
      }
    }
    void tick()
    const id = window.setInterval(tick, ERROR_POLL_MS)
    return () => {
      cancelled = true
      window.clearInterval(id)
    }
  }, [])
  const handleOpenErrors = () => {
    window.dispatchEvent(new CustomEvent('gokin:open-logs', { detail: { level: 'error' } }))
  }

  const currentModelUnavailable = !!(
    activeProject && providerHealth?.ok && providerHealth.availableModels?.length &&
    !providerHealth.availableModels.includes(activeModel)
  )
  const authRejected = !providerHealth?.ok && (
    providerHealth?.statusCode === 401 || providerHealth?.error?.toLowerCase().includes('api key')
  )
  const providerIssue = !!activeProject && !healthChecking && !online
    ? 'offline'
    : !!activeProject && !healthChecking && providerHealth !== null && !providerHealth.ok
      ? authRejected ? 'auth' : 'provider'
      : !healthChecking && currentModelUnavailable ? 'model' : null
  const stateLabel = pendingAttention
    ? pendingAttention.question.kind === 'tool_approval' ? 'Approval needed' : 'Input needed'
    : anyActive
      ? 'Working'
    : !activeProject
      ? ''
      : !online
        ? 'Offline'
        : healthChecking || providerHealth === null
          ? 'Checking…'
          : authRejected
            ? 'Key rejected'
            : currentModelUnavailable
              ? 'Model unavailable'
              : providerHealth.ok
                ? 'Ready'
                : 'Provider unavailable'
  const stateTitle = !activeProject
    ? ''
    : !online
      ? 'No network connection detected. Your drafts remain saved.'
      : healthChecking || providerHealth === null
        ? `Checking ${activeProject.provider?.toUpperCase() || 'GLM'} authentication and model availability…`
        : currentModelUnavailable
          ? `${activeModel} was not advertised for this account. Click to choose an available model.`
          : providerHealth.description || providerHealth.error || 'Provider status unavailable'

  const handleProviderState = () => {
    if (providerIssue === 'auth') {
      window.dispatchEvent(new CustomEvent('gokin:open-settings', { detail: { section: 'settings-connections' } }))
    } else if (providerIssue === 'model') {
      window.dispatchEvent(new CustomEvent('gokin:open-model-switcher'))
    } else if (providerIssue === 'provider' || providerIssue === 'offline') {
      void checkActiveProvider()
    }
  }

  const handlePendingAttention = () => {
    if (!activeProjectId || !pendingAttention) return
    useChatStore.getState().setActiveSession(activeProjectId, pendingAttention.sessionID)
    window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: pendingAttention.sessionID }))
    // Let the target ChatPanel mount/restore before moving focus to the safe
    // default approval choice (or free-form input).
    window.setTimeout(() => {
      const card = document.querySelector<HTMLElement>(`[data-question-id="${pendingAttention.question.questionID}"]`)
      scrollIntoViewWithMotion(card, { block: 'center' })
      card?.querySelector<HTMLElement>('.askuser-option.default, .askuser-input')?.focus()
    }, 80)
  }

  return (
    <div className="status-bar" role="status" aria-live="polite">
      <div className="status-left">
        <span className={`status-dot ${anyActive ? 'active' : activeProject && !providerIssue && providerHealth?.ok ? 'connected' : ''} ${providerIssue ? 'issue' : ''} ${pendingAttention ? 'attention' : ''}`} />
        {activeProject ? (
          <>
            {pendingAttention ? (
              <button
                type="button"
                className="status-state-action is-attention"
                onClick={handlePendingAttention}
                title={`Open the session waiting for ${pendingAttention.question.kind === 'tool_approval' ? 'approval' : 'input'}`}
              >
                <AlertTriangle size={10} />
                {stateLabel}
              </button>
            ) : providerIssue && providerIssue !== 'offline' && !anyActive ? (
              <button type="button" className={`status-state-action is-${providerIssue}`} onClick={handleProviderState} title={stateTitle}>
                {providerIssue === 'auth' ? <KeyRound size={10} /> : <RefreshCw size={10} />}
                {stateLabel}
              </button>
            ) : (
              <span className="status-state" title={stateTitle}>
                {providerIssue === 'offline' && <WifiOff size={10} />}
                {healthChecking && !anyActive && <Loader2 size={10} className="tool-spinner" />}
                {stateLabel}
              </span>
            )}
            <button
              type="button"
              className="status-model"
              onClick={() => window.dispatchEvent(new CustomEvent('gokin:open-model-switcher'))}
              title={`Switch model · ${activeProject.provider || 'glm'}/${activeProject.model || 'glm-5.2'}`}
            >
              {formatProviderModelLabel(activeProject.provider, activeProject.model || 'glm-5.2')}
            </button>
            {activeProject.thinkingActive && (
              <span
                className="status-thinking"
                title={`Extended thinking enabled${activeProject.thinkingBudgetEffective ? ` (${activeProject.thinkingBudgetEffective} tokens)` : ''}`}
              >
                <Brain size={11} />
              </span>
            )}
          </>
        ) : (
          <span>Choose a project to begin</span>
        )}
      </div>
      <div className="status-right">
        {errorCount > 0 && (
          <button
            type="button"
            className="status-error-badge"
            onClick={handleOpenErrors}
            title={`${errorCount} error${errorCount === 1 ? '' : 's'} in the last 5 minutes — click to view logs\n\n${errorPreview}`}
          >
            <AlertOctagon size={11} />
            <span>{errorCount > 99 ? '99+' : errorCount}</span>
          </button>
        )}
      </div>
    </div>
  )
}
