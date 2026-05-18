import { useEffect, useState, useRef } from 'react'
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
}

const MAX_VISIBLE = 3
const AUTO_DISMISS_MS = 5500

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
  // Track timer handles so we can clear them on manual dismiss.
  const timersRef = useRef<Map<string, number>>(new Map())

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

  const dismiss = (id: string) => {
    const t = timersRef.current.get(id)
    if (t) {
      clearTimeout(t)
      timersRef.current.delete(id)
    }
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }

  const push = (kind: ToastKind, data: any) => {
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
    setToasts((prev) => {
      // Cap at MAX_VISIBLE — drop the oldest if at limit. The stack grows
      // top-down (newest at top), so we slice from the end.
      const next = [{ id, kind, projectID: data.projectID, sessionID, projectName, preview }, ...prev]
      // Shed oldest if over capacity.
      if (next.length > MAX_VISIBLE) {
        const dropped = next.slice(MAX_VISIBLE)
        for (const d of dropped) {
          const t = timersRef.current.get(d.id)
          if (t) clearTimeout(t)
          timersRef.current.delete(d.id)
        }
        return next.slice(0, MAX_VISIBLE)
      }
      return next
    })
    const handle = window.setTimeout(() => dismiss(id), AUTO_DISMISS_MS)
    timersRef.current.set(id, handle)
  }

  useEffect(() => {
    const offComplete = EventsOn('chat:complete', (data: any) => push('success', data))
    const offError = EventsOn('chat:error', (data: any) => push('error', data))
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
      setToasts((prev) => {
        const next = [toast, ...prev]
        if (next.length > MAX_VISIBLE) {
          const dropped = next.slice(MAX_VISIBLE)
          for (const d of dropped) {
            const t2 = timersRef.current.get(d.id)
            if (t2) clearTimeout(t2)
            timersRef.current.delete(d.id)
          }
          return next.slice(0, MAX_VISIBLE)
        }
        return next
      })
      // Budget alerts dwell longer than completion toasts — they're more
      // actionable and the user might want to read the numbers carefully.
      const handle = window.setTimeout(() => dismiss(id), 9000)
      timersRef.current.set(id, handle)
    }
    window.addEventListener('gokin:budget-alert', onBudget)
    return () => {
      if (typeof offComplete === 'function') offComplete()
      if (typeof offError === 'function') offError()
      window.removeEventListener('gokin:budget-alert', onBudget)
      // Clean up any in-flight timers.
      for (const handle of timersRef.current.values()) clearTimeout(handle)
      timersRef.current.clear()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled])

  if (toasts.length === 0) return null

  const jumpTo = (toast: Toast) => {
    useProjectStore.getState().setActiveProject(toast.projectID)
    useChatStore.getState().setActiveSession(toast.projectID, toast.sessionID)
    // App.tsx's tab handler reacts to this event by switching the chat view.
    window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: toast.sessionID }))
    dismiss(toast.id)
  }

  return (
    <div className="toast-stack">
      {toasts.map((t) => (
        <button
          key={t.id}
          className={`toast toast-${t.kind}`}
          onClick={() => jumpTo(t)}
          title="Click to jump to this session"
        >
          <div className="toast-icon">
            {t.kind === 'success' ? <CheckCircle size={14} /> : t.kind === 'warning' ? <AlertTriangle size={14} /> : <XCircle size={14} />}
          </div>
          <div className="toast-body">
            <div className="toast-title">
              <span className="toast-project">{t.projectName}</span>
              <span className="toast-status">{t.kind === 'success' ? 'finished' : t.kind === 'warning' ? 'budget alert' : 'errored'}</span>
            </div>
            <div className="toast-preview">{t.preview}</div>
          </div>
          <span
            className="toast-close"
            role="button"
            onClick={(e) => { e.stopPropagation(); dismiss(t.id) }}
            title="Dismiss"
          >
            <X size={11} />
          </span>
        </button>
      ))}
    </div>
  )
}
