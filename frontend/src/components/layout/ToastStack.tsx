import { useCallback, useEffect, useState, useRef } from 'react'
import { CheckCircle, XCircle, X, AlertTriangle } from 'lucide-react'
import { EventsOn } from '../../../wailsjs/runtime/runtime'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { isProjectMuted } from '../../lib/mutedProjects'

// ToastStack (iter 530+) — bottom-right transient notifications when a turn
// finishes (or errors) in a session the user isn't currently viewing. The
// existing unread-badge feature (iter 410+) already says "something happened
// over there"; toasts add the "what happened" + click-to-jump so users
// don't have to manually find the right tab.
//
// Gating:
//  - localStorage `gokin:toasts-enabled` (default '1') controls the feature
//  - We don't push a toast if the user is actively viewing the matching
//    project + session (it'd be redundant — they can see the result inline)
//  - We DON'T fire when the window is unfocused either, because the OS-level
//    notification (notifyIfBlurred in useWailsEvents.ts) already handles
//    that case. Toasts are for the focused-but-elsewhere case specifically.

type ToastKind = 'success' | 'error' | 'warning'

interface Toast {
  id: string
  kind: ToastKind
  projectID: string
  sessionID: string
  projectName: string
  preview: string
  status?: string
  action?: 'session' | 'archived-chats'
}

const MAX_VISIBLE = 3
const AUTO_DISMISS_MS = 5500

type TimerPauseReason = 'hover' | 'focus' | 'window'
type ToastTimer = {
  handle: number | null
  remainingMs: number
  startedAt: number
  pauseReasons: Set<TimerPauseReason>
}

// Lazy-init the AudioContext so we don't claim audio resources until the
// first sound is actually requested. Modern browsers also require a user
// gesture before audio can play — by the time a toast fires, the user
// has clicked + interacted with the app multiple times, so this works.
let audioCtx: AudioContext | null = null
function playCompletionSound(kind: ToastKind) {
  try {
    const Ctor: typeof AudioContext = (window as any).AudioContext || (window as any).webkitAudioContext
    if (!Ctor) return
    if (!audioCtx) audioCtx = new Ctor()
    const ctx = audioCtx
    if (ctx.state === 'suspended') {
      // resume() returns a promise; we don't await — fire-and-forget.
      ctx.resume().catch(() => {})
    }
    // Two-note chime: a short upward arpeggio for success, a downward
    // dip for error. Synthesized with simple sine oscillators + a short
    // exponential gain envelope so the tone doesn't pop at start/end.
    const now = ctx.currentTime
    const notes = kind === 'success'
      ? [
          { freq: 660, t: 0, dur: 0.12 },
          { freq: 880, t: 0.1, dur: 0.18 },
        ]
      : [
          { freq: 440, t: 0, dur: 0.14 },
          { freq: 330, t: 0.12, dur: 0.22 },
        ]
    for (const n of notes) {
      const osc = ctx.createOscillator()
      const gain = ctx.createGain()
      osc.type = 'sine'
      osc.frequency.value = n.freq
      osc.connect(gain)
      gain.connect(ctx.destination)
      // Envelope: quick attack, exponential decay. Peak gain low enough
      // (0.12) that the chime is noticeable but not jarring even with
      // headphones at moderate volume.
      const start = now + n.t
      const end = start + n.dur
      gain.gain.setValueAtTime(0.0001, start)
      gain.gain.exponentialRampToValueAtTime(0.12, start + 0.01)
      gain.gain.exponentialRampToValueAtTime(0.0001, end)
      osc.start(start)
      osc.stop(end + 0.05)
    }
  } catch {
    // Audio playback failures are silent — sound is a "nice to have".
  }
}

export function ToastStack() {
  const [toasts, setToasts] = useState<Toast[]>([])
  const timersRef = useRef<Map<string, ToastTimer>>(new Map())

  // We listen for the toggle-changed event so the user can flip the
  // setting without a refresh. Initial value reads localStorage once.
  const [enabled, setEnabled] = useState<boolean>(() => {
    try { return localStorage.getItem('gokin:toasts-enabled') !== '0' } catch { return true }
  })
  // Sound preference (iter 570+). Off by default — sounds are intrusive.
  const soundEnabledRef = useRef<boolean>(false)
  useEffect(() => {
    const readSound = () => {
      try { soundEnabledRef.current = localStorage.getItem('gokin:sound-on-complete') === '1' } catch { soundEnabledRef.current = false }
    }
    readSound()
    window.addEventListener('gokin:sound-toggled', readSound)
    return () => window.removeEventListener('gokin:sound-toggled', readSound)
  }, [])
  useEffect(() => {
    const handler = () => {
      try { setEnabled(localStorage.getItem('gokin:toasts-enabled') !== '0') } catch { setEnabled(true) }
    }
    window.addEventListener('gokin:toasts-toggled', handler)
    return () => window.removeEventListener('gokin:toasts-toggled', handler)
  }, [])

  const clearToastTimer = useCallback((id: string) => {
    const timer = timersRef.current.get(id)
    if (timer?.handle !== null && timer?.handle !== undefined) window.clearTimeout(timer.handle)
    timersRef.current.delete(id)
  }, [])

  const dismiss = useCallback((id: string) => {
    clearToastTimer(id)
    setToasts((previous) => previous.filter((toast) => toast.id !== id))
  }, [clearToastTimer])

  const scheduleTimer = useCallback((id: string, delayMs: number) => {
    const previous = timersRef.current.get(id)
    if (previous?.handle !== null && previous?.handle !== undefined) window.clearTimeout(previous.handle)
    const remainingMs = Math.max(0, delayMs)
    const pauseReasons = previous?.pauseReasons || new Set<TimerPauseReason>()
    const timer: ToastTimer = {
      handle: null,
      remainingMs,
      startedAt: Date.now(),
      pauseReasons,
    }
    if (pauseReasons.size === 0) {
      timer.handle = window.setTimeout(() => dismiss(id), remainingMs)
    }
    timersRef.current.set(id, timer)
  }, [dismiss])

  const pauseTimer = useCallback((id: string, reason: TimerPauseReason) => {
    const timer = timersRef.current.get(id)
    if (!timer) return
    timer.pauseReasons.add(reason)
    if (timer.handle === null) return
    window.clearTimeout(timer.handle)
    timer.handle = null
    timer.remainingMs = Math.max(0, timer.remainingMs - (Date.now() - timer.startedAt))
  }, [])

  const resumeTimer = useCallback((id: string, reason: TimerPauseReason) => {
    const timer = timersRef.current.get(id)
    if (!timer) return
    timer.pauseReasons.delete(reason)
    if (timer.pauseReasons.size > 0 || timer.handle !== null) return
    scheduleTimer(id, Math.max(250, timer.remainingMs))
  }, [scheduleTimer])

  const addToast = useCallback((toast: Toast, dismissAfter: number) => {
    setToasts((previous) => {
      const next = [toast, ...previous]
      for (const dropped of next.slice(MAX_VISIBLE)) clearToastTimer(dropped.id)
      return next.slice(0, MAX_VISIBLE)
    })
    scheduleTimer(toast.id, dismissAfter)
  }, [clearToastTimer, scheduleTimer])

  useEffect(() => {
    const pauseAll = () => {
      for (const id of timersRef.current.keys()) pauseTimer(id, 'window')
    }
    const resumeAll = () => {
      for (const id of timersRef.current.keys()) resumeTimer(id, 'window')
    }
    window.addEventListener('blur', pauseAll)
    window.addEventListener('focus', resumeAll)
    return () => {
      window.removeEventListener('blur', pauseAll)
      window.removeEventListener('focus', resumeAll)
    }
  }, [pauseTimer, resumeTimer])

  useEffect(() => {
    if (enabled) return
    for (const id of timersRef.current.keys()) clearToastTimer(id)
    setToasts([])
  }, [enabled, clearToastTimer])

  useEffect(() => () => {
    for (const id of timersRef.current.keys()) clearToastTimer(id)
  }, [clearToastTimer])

  const push = useCallback((kind: ToastKind, data: any, status?: string, dismissAfter = AUTO_DISMISS_MS) => {
    if (!enabled) return
    if (!data?.projectID) return
    // Skip if the user is currently viewing this exact project + session AND
    // the window is focused. In any other state (different tab, different
    // project, unfocused window), surface the toast so they know.
    const proj = useProjectStore.getState()
    const chat = useChatStore.getState()
    const sessionID = data.sessionID || 'default'
    const isViewing =
      document.hasFocus() &&
      proj.activeProjectId === data.projectID &&
      chat.activeSession[data.projectID] === sessionID
    if (isViewing) return
    // OS-level notifyIfBlurred handles the unfocused case — skip the toast
    // there to avoid double notifications.
    if (!document.hasFocus()) return
    // Per-project mute (iter 580+) — silence both the toast AND the sound
    // for projects the user has explicitly muted via the sidebar context menu.
    if (isProjectMuted(data.projectID)) return
    // Play the chime if the user opted in. Independent of toast enabled
    // is intentional — but since the toast is shown anyway in this code
    // path, sound only fires when toasts are also on.
    if (soundEnabledRef.current) playCompletionSound(kind)
    const project = proj.projects.find((p) => p.id === data.projectID)
    const projectName = project?.name || 'Project'
    const previewRaw = String(data.text || '').replace(/\s+/g, ' ').trim()
    const preview = previewRaw.length > 100 ? previewRaw.slice(0, 100) + '…' : (previewRaw || (kind === 'error' ? 'Agent errored' : 'Agent finished.'))
    const id = `toast-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    addToast({ id, kind, projectID: data.projectID, sessionID, projectName, preview, status }, dismissAfter)
  }, [enabled, addToast])

  useEffect(() => {
    const offComplete = EventsOn('chat:complete', (data: any) => push('success', data))
    const offError = EventsOn('chat:error', (data: any) => push('error', data))
    const offAskUser = EventsOn('chat:ask_user', (data: any) => {
      const approval = data?.kind === 'tool_approval'
      push(
        'warning',
        {
          ...data,
          text: approval
            ? `GLM/Kimi is waiting for permission${data.tool ? ` to use ${String(data.tool).replace(/_/g, ' ')}` : ''}.`
            : 'GLM/Kimi is waiting for your answer.',
        },
        approval ? 'approval required' : 'input needed',
        15_000,
      )
    })
    // Budget-alert custom event (iter 610+). Ignores the focused-but-elsewhere
    // gating because the user explicitly opted into budget tracking — we
    // want them to see the alert immediately, even on the source project.
    // We bypass push() and inline-construct so the gating differs.
    //
    // Iter 670+: respect per-project mute (iter 580+) — if the user muted
    // the project explicitly, budget alerts should be silent too. They're
    // saying "I don't want anything from this project right now."
    const onBudget = (e: Event) => {
      const detail = (e as CustomEvent).detail
      if (!detail?.projectID || !enabled) return
      if (isProjectMuted(detail.projectID)) return
      const id = `toast-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
      const tot = Number(detail.total || 0)
      const bud = Number(detail.budget || 0)
      const t = Number(detail.threshold || 0)
      const preview = `Spent ${tot.toFixed(2)} of ${bud.toFixed(2)} budget — ${t}% reached.`
      const projectName = String(detail.projectName || 'Project')
      const toast: Toast = {
        id,
        kind: 'warning',
        projectID: detail.projectID,
        sessionID: String(detail.sessionID || 'default'),
        projectName,
        preview,
      }
      // Budget alerts dwell longer than completion toasts — they're more
      // actionable and the user might want to read the numbers carefully.
      addToast(toast, 9000)
    }
    const onSessionAutoArchived = (event: Event) => {
      const detail = (event as CustomEvent).detail
      if (!detail?.projectID || !enabled || !document.hasFocus()) return
      if (isProjectMuted(detail.projectID)) return
      const project = useProjectStore.getState().projects.find((item) => item.id === detail.projectID)
      const number = Number(detail.pullRequestNumber || 0)
      const state = String(detail.pullRequestState || '').toLowerCase()
      const prefix = number > 0 ? `PR #${number}` : 'Pull request'
      addToast({
        id: `toast-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
        kind: 'success',
        projectID: detail.projectID,
        sessionID: String(detail.sessionID || 'default'),
        projectName: project?.name || 'Project',
        preview: `${prefix} ${state || 'finished'} — clean idle chat moved to Archived chats.`,
        status: 'archived',
        action: 'archived-chats',
      }, 9000)
    }
    window.addEventListener('gokin:budget-alert', onBudget)
    window.addEventListener('gokin:session-auto-archived', onSessionAutoArchived)
    return () => {
      if (typeof offComplete === 'function') offComplete()
      if (typeof offError === 'function') offError()
      if (typeof offAskUser === 'function') offAskUser()
      window.removeEventListener('gokin:budget-alert', onBudget)
      window.removeEventListener('gokin:session-auto-archived', onSessionAutoArchived)
    }
  }, [enabled, push, addToast])

  if (toasts.length === 0) return null

  const jumpTo = (toast: Toast) => {
    useProjectStore.getState().setActiveProject(toast.projectID)
    if (toast.action === 'archived-chats') {
      window.dispatchEvent(new CustomEvent('gokin:open-archived-chats', { detail: { projectID: toast.projectID } }))
      dismiss(toast.id)
      return
    }
    useChatStore.getState().setActiveSession(toast.projectID, toast.sessionID)
    // App.tsx's tab handler reacts to this event by switching the chat view.
    window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: toast.sessionID }))
    dismiss(toast.id)
  }

  return (
    <div className="toast-stack" role="region" aria-label="Notifications" aria-live="polite" aria-atomic="false">
      {toasts.map((t) => (
        <div
          key={t.id}
          className={`toast toast-${t.kind}`}
          onMouseEnter={() => pauseTimer(t.id, 'hover')}
          onMouseLeave={() => resumeTimer(t.id, 'hover')}
          onFocusCapture={() => pauseTimer(t.id, 'focus')}
          onBlurCapture={(event) => {
            if (!event.currentTarget.contains(event.relatedTarget as Node | null)) resumeTimer(t.id, 'focus')
          }}
          onKeyDown={(event) => {
            if (event.key !== 'Escape') return
            event.preventDefault()
            event.stopPropagation()
            const items = Array.from(document.querySelectorAll<HTMLElement>('.toast'))
            const index = items.indexOf(event.currentTarget)
            const next = items[index + 1] || items[index - 1]
            dismiss(t.id)
            requestAnimationFrame(() => next?.querySelector<HTMLElement>('button')?.focus())
          }}
        >
          <button
            className="toast-jump"
            onClick={() => jumpTo(t)}
            title={t.action === 'archived-chats' ? 'Open archived chats' : 'Click to jump to this session'}
            aria-label={`${t.projectName}: ${t.preview}. ${t.action === 'archived-chats' ? 'Open archived chats' : 'Jump to session'}`}
          >
            <div className="toast-icon" aria-hidden="true">
              {t.kind === 'success' ? <CheckCircle size={14} /> : t.kind === 'warning' ? <AlertTriangle size={14} /> : <XCircle size={14} />}
            </div>
            <div className="toast-body">
              <div className="toast-title">
                <span className="toast-project">{t.projectName}</span>
                <span className="toast-status">{t.status || (t.kind === 'success' ? 'finished' : t.kind === 'warning' ? 'attention' : 'errored')}</span>
              </div>
              <div className="toast-preview">{t.preview}</div>
            </div>
          </button>
          <button
            className="toast-close"
            onClick={(e) => { e.stopPropagation(); dismiss(t.id) }}
            title="Dismiss"
            aria-label={`Dismiss notification from ${t.projectName}`}
          >
            <X size={11} aria-hidden="true" />
          </button>
        </div>
      ))}
    </div>
  )
}
