import { useEffect, useState } from 'react'
import { Brain, AlertOctagon } from 'lucide-react'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { RecentErrorCount, RecentErrors } from '../../../wailsjs/go/studio/Studio'

// iter 990+: window in which recent backend errors light up the status bar.
// 5 minutes is long enough that a single error stays visible across a quick
// task switch (so the user can come back and notice it), short enough that
// yesterday's resolved hiccup doesn't permanently glow the bar red.
const ERROR_WINDOW_MS = 5 * 60 * 1000
// Poll cadence. 30s keeps perceived latency low without spamming the bridge.
const ERROR_POLL_MS = 30 * 1000

export function StatusBar() {
  const activeProject = useProjectStore((s) =>
    s.projects.find((p) => p.id === s.activeProjectId)
  )
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projectCount = useProjectStore((s) => s.projects.length)
  // Chat store is keyed by `projectID_sessionID`, so aggregate across every
  // session that belongs to the active project.
  const msgCount = useChatStore((s) => {
    if (!activeProjectId) return 0
    const prefix = activeProjectId + '_'
    let total = 0
    for (const [key, msgs] of Object.entries(s.messages)) {
      if (key.startsWith(prefix) && Array.isArray(msgs)) total += msgs.length
    }
    return total
  })
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

  // iter 990+: error indicator. Polls the backend's RecentErrorCount every
  // 30s. When > 0, the badge appears; click opens Settings → Logs scoped to
  // error level so the user can see what went wrong. Tooltip previews the
  // first few error messages so a glance answers "what's broken?".
  const [errorCount, setErrorCount] = useState(0)
  const [errorPreview, setErrorPreview] = useState<string>('')
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

  return (
    <div className="status-bar">
      <div className="status-left">
        <span className={`status-dot ${anyActive ? 'active' : activeProject ? 'connected' : ''}`} />
        {activeProject ? (
          <>
            <span className="status-project">{activeProject.name}</span>
            <span className="status-sep" />
            <span className="status-provider">{activeProject.provider || 'glm'}</span>
            <span className="status-model">{activeProject.model || 'glm-5.1'}</span>
            {(activeProject.thinkingMode === 'enabled' || (activeProject.thinkingMode !== 'disabled' && activeProject.provider === 'kimi')) && (
              <span className="status-thinking" title="Extended thinking enabled">
                <Brain size={11} />
              </span>
            )}
          </>
        ) : (
          <span>No project selected</span>
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
        {msgCount > 0 && <span className="status-info">{msgCount} msg{msgCount === 1 ? '' : 's'}</span>}
        <span className="status-info">{projectCount} project{projectCount !== 1 ? 's' : ''}</span>
        <span className="status-version">v1.0</span>
      </div>
    </div>
  )
}
