import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ArrowUpRight, Clock3, CornerDownLeft, Crop, Loader2, MessageSquare, Mic, MicOff, Monitor, Plus, Sparkles, X } from 'lucide-react'
import { GetDraft, SaveDraft } from '../../../wailsjs/go/studio/Studio'
import { useChatStore } from '../../stores/chatStore'
import { useSpeechDictation } from '../../hooks/useSpeechDictation'
import { composeDictationDraft } from '../../lib/dictation'
import type { QuickEntryComposerAction } from '../../lib/quickEntry'

export interface QuickEntrySession {
  id: string
  name: string
  parentName?: string
  pinned?: boolean
  createdAt?: number
  lastUsedAt?: number
  messages?: number
}

interface QuickEntryOverlayProps {
  projectID: string | null
  projectName?: string
  sessions: QuickEntrySession[]
  activeSessionID?: string | null
  voiceActivationID?: number | null
  onVoiceActivationHandled?: (id: number) => void
  onDismiss: () => void
  onOpenSession: (sessionID: string, action?: QuickEntryComposerAction) => void
  onCreateSession: (draft: string, sourceSessionID: string) => Promise<void>
  onOpenStudio: () => void
}

const QUICK_ENTRY_DRAFT_MAX = 100_000

function sessionRecency(session: QuickEntrySession) {
  return session.lastUsedAt || session.createdAt || 0
}

function formatRecency(timestamp?: number) {
  if (!timestamp) return 'New chat'
  const delta = Date.now() - timestamp
  if (delta < 60_000) return 'Just now'
  if (delta < 3_600_000) return `${Math.max(1, Math.floor(delta / 60_000))}m ago`
  if (delta < 86_400_000) return `${Math.floor(delta / 3_600_000)}h ago`
  if (delta < 7 * 86_400_000) return `${Math.floor(delta / 86_400_000)}d ago`
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(new Date(timestamp))
}

export function QuickEntryOverlay({
  projectID,
  projectName,
  sessions,
  activeSessionID,
  voiceActivationID,
  onVoiceActivationHandled,
  onDismiss,
  onOpenSession,
  onCreateSession,
  onOpenStudio,
}: QuickEntryOverlayProps) {
  const recentSessions = useMemo(() => [...sessions]
    .sort((left, right) => sessionRecency(right) - sessionRecency(left))
    .slice(0, 5), [sessions])
  const initialSession = activeSessionID && recentSessions.some((session) => session.id === activeSessionID)
    ? activeSessionID
    : recentSessions[0]?.id || ''
  const [selectedSessionID, setSelectedSessionID] = useState(initialSession)
  const [draft, setDraft] = useState('')
  const [loadingDraft, setLoadingDraft] = useState(false)
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const draftRef = useRef(draft)
  const dictationBaseRef = useRef('')
  const hydratedSessionRef = useRef('')
  const loadRequestRef = useRef(0)
  const handledVoiceActivationRef = useRef(0)
  draftRef.current = draft

  const dictation = useSpeechDictation({
    language: typeof navigator !== 'undefined' ? navigator.language : 'en-US',
    onTranscript: (finalTranscript, interimTranscript) => {
      setDraft(composeDictationDraft(
        dictationBaseRef.current,
        finalTranscript,
        interimTranscript,
        QUICK_ENTRY_DRAFT_MAX,
      ))
    },
  })
  const dictationBusy = dictation.phase !== 'idle'

  const persistDraft = useCallback((sessionID?: string, text?: string) => {
    const targetSessionID = sessionID || hydratedSessionRef.current
    if (!projectID || !targetSessionID) return Promise.resolve()
    const targetText = text ?? draftRef.current
    const chatKey = `${projectID}_${targetSessionID}`
    useChatStore.getState().setDraft(chatKey, targetText)
    return SaveDraft(projectID, targetSessionID, targetText).catch((reason: unknown) => {
      setError(`Draft is kept in memory, but could not be saved to disk: ${String((reason as any)?.message || reason)}`)
    })
  }, [projectID])

  useEffect(() => {
    const available = recentSessions.some((session) => session.id === selectedSessionID)
    if (available) return
    setSelectedSessionID(initialSession)
  }, [initialSession, recentSessions, selectedSessionID])

  useEffect(() => {
    const requestID = ++loadRequestRef.current
    hydratedSessionRef.current = ''
    setError('')
    if (!projectID || !selectedSessionID) {
      setDraft('')
      setLoadingDraft(false)
      return
    }
    const chatKey = `${projectID}_${selectedSessionID}`
    const drafts = useChatStore.getState().drafts
    if (Object.prototype.hasOwnProperty.call(drafts, chatKey)) {
      hydratedSessionRef.current = selectedSessionID
      setDraft(drafts[chatKey] || '')
      setLoadingDraft(false)
      requestAnimationFrame(() => textareaRef.current?.focus())
      return
    }
    setDraft('')
    setLoadingDraft(true)
    GetDraft(projectID, selectedSessionID).then((text) => {
      if (requestID !== loadRequestRef.current) return
      const latestDrafts = useChatStore.getState().drafts
      const hasLiveDraft = Object.prototype.hasOwnProperty.call(latestDrafts, chatKey)
      const next = hasLiveDraft ? latestDrafts[chatKey] || '' : text || ''
      hydratedSessionRef.current = selectedSessionID
      setDraft(next)
      if (next) useChatStore.getState().setDraft(chatKey, next)
    }).catch((reason: unknown) => {
      if (requestID === loadRequestRef.current) {
        setError(`Could not load this chat’s saved draft: ${String((reason as any)?.message || reason)}`)
      }
    }).finally(() => {
      if (requestID !== loadRequestRef.current) return
      if (!hydratedSessionRef.current) hydratedSessionRef.current = selectedSessionID
      setLoadingDraft(false)
      requestAnimationFrame(() => textareaRef.current?.focus())
    })
  }, [projectID, selectedSessionID])

  useEffect(() => {
    if (!projectID || !selectedSessionID || loadingDraft || hydratedSessionRef.current !== selectedSessionID) return
    const chatKey = `${projectID}_${selectedSessionID}`
    useChatStore.getState().setDraft(chatKey, draft)
    const timer = window.setTimeout(() => { void persistDraft(selectedSessionID, draft) }, 400)
    return () => window.clearTimeout(timer)
  }, [draft, loadingDraft, persistDraft, projectID, selectedSessionID])

  useEffect(() => () => {
    const sessionID = hydratedSessionRef.current
    const text = draftRef.current
    if (projectID && sessionID) void SaveDraft(projectID, sessionID, text)
  }, [projectID])

  useEffect(() => {
    const focus = () => textareaRef.current?.focus()
    window.addEventListener('gokin:focus-quick-entry', focus)
    requestAnimationFrame(focus)
    return () => window.removeEventListener('gokin:focus-quick-entry', focus)
  }, [])

  const toggleDictation = () => {
    if (dictation.phase === 'stopping') return
    if (dictation.listening) {
      dictation.stop()
      requestAnimationFrame(() => textareaRef.current?.focus())
      return
    }
    if (draft.length >= QUICK_ENTRY_DRAFT_MAX) return
    dictationBaseRef.current = draft
    dictation.start()
    requestAnimationFrame(() => textareaRef.current?.focus())
  }

  useEffect(() => {
    if (!voiceActivationID || handledVoiceActivationRef.current === voiceActivationID) return
    if (loadingDraft || !selectedSessionID || hydratedSessionRef.current !== selectedSessionID) return
    handledVoiceActivationRef.current = voiceActivationID
    onVoiceActivationHandled?.(voiceActivationID)
    if (dictation.phase === 'stopping') return
    if (dictation.listening) {
      dictation.stop()
      requestAnimationFrame(() => textareaRef.current?.focus())
      return
    }
    if (draftRef.current.length >= QUICK_ENTRY_DRAFT_MAX) {
      setError('The draft is full. Remove some text before starting voice dictation.')
      return
    }
    dictationBaseRef.current = draftRef.current
    dictation.start()
    requestAnimationFrame(() => textareaRef.current?.focus())
  }, [dictation.listening, dictation.phase, loadingDraft, onVoiceActivationHandled, selectedSessionID, voiceActivationID])

  const stopDictationForReview = () => {
    if (dictation.phase === 'stopping') return true
    if (!dictation.listening) return false
    dictation.stop()
    requestAnimationFrame(() => textareaRef.current?.focus())
    return true
  }

  const requestDismiss = () => {
    if (stopDictationForReview()) return
    void persistDraft().finally(onDismiss)
  }

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229) return
      if (event.key === 'Escape') {
        event.preventDefault()
        requestDismiss()
      }
    }
    window.addEventListener('keydown', onKeyDown, true)
    return () => window.removeEventListener('keydown', onKeyDown, true)
  }, [dictation.listening, dictation.phase, draft, onDismiss, persistDraft])

  const chooseSession = (sessionID: string) => {
    if (stopDictationForReview()) return
    if (sessionID === selectedSessionID) {
      textareaRef.current?.focus()
      return
    }
    void persistDraft(selectedSessionID, draft)
    hydratedSessionRef.current = ''
    setSelectedSessionID(sessionID)
  }

  const openSelected = async () => {
    if (stopDictationForReview()) return
    if (!selectedSessionID) return
    await persistDraft()
    onOpenSession(selectedSessionID)
  }

  const openSession = async (sessionID: string) => {
    if (stopDictationForReview()) return
    await persistDraft()
    onOpenSession(sessionID)
  }

  const openComposerAction = async (action: QuickEntryComposerAction) => {
    if (stopDictationForReview() || !selectedSessionID) return
    await persistDraft()
    onOpenSession(selectedSessionID, action)
  }

  const createSession = async () => {
    if (creating || stopDictationForReview()) return
    setCreating(true)
    setError('')
    const sourceSessionID = hydratedSessionRef.current || selectedSessionID
    // The parent moves this draft into the newly-created session. Clear the
    // unmount flush marker before it closes the compact surface, otherwise
    // cleanup would write the same text back into the source chat.
    hydratedSessionRef.current = ''
    try {
      await onCreateSession(draft, sourceSessionID)
    } catch (reason: unknown) {
      hydratedSessionRef.current = sourceSessionID
      setError(String((reason as any)?.message || reason || 'Could not create a chat'))
      setCreating(false)
    }
  }

  if (!projectID) {
    return (
      <main className="quick-entry-shell" role="dialog" aria-modal="true" aria-labelledby="quick-entry-title">
        <div className="quick-entry-empty">
          <span className="quick-entry-mark"><Sparkles size={20} /></span>
          <h1 id="quick-entry-title">Gokin Quick Entry</h1>
          <p>Connect a project folder before starting a GLM or Kimi draft.</p>
          <button type="button" className="quick-entry-primary" onClick={onOpenStudio}>Open Gokin Studio</button>
          <button type="button" className="quick-entry-close" onClick={onDismiss} aria-label="Close Quick Entry"><X size={16} /></button>
        </div>
      </main>
    )
  }

  return (
    <main className="quick-entry-shell" role="dialog" aria-modal="true" aria-labelledby="quick-entry-title">
      <header className="quick-entry-header">
        <div className="quick-entry-heading">
          <span className="quick-entry-mark"><Sparkles size={15} /></span>
          <div>
            <h1 id="quick-entry-title">Quick Entry</h1>
            <p>{projectName || 'Current project'} · draft stays local until you review and send</p>
          </div>
        </div>
        <button type="button" className="quick-entry-close" onClick={requestDismiss} aria-label="Close Quick Entry"><X size={16} /></button>
      </header>

      <section className="quick-entry-composer" aria-label="Message draft">
        <div className="quick-entry-composer-topline">
          <span><MessageSquare size={12} /> {recentSessions.find((session) => session.id === selectedSessionID)?.name || 'New chat'}</span>
          {loadingDraft
            ? <span className="quick-entry-loading"><Loader2 size={11} className="spin" /> Loading saved draft</span>
            : dictationBusy
              ? <span className="quick-entry-listening"><span className="quick-entry-listening-dot" /> {dictation.phase === 'stopping' ? 'Finishing transcript' : dictation.phase === 'authorizing' ? 'Waiting for macOS permission' : dictation.engine === 'native' ? 'Apple Dictation' : 'Listening'}{dictation.interimTranscript ? ` · ${dictation.interimTranscript.slice(0, 44)}` : '…'}</span>
              : null}
        </div>
        <textarea
          ref={textareaRef}
          value={draft}
          maxLength={QUICK_ENTRY_DRAFT_MAX}
          disabled={loadingDraft || creating}
          placeholder="Ask GLM or Kimi anything…"
          aria-label="Quick Entry message draft"
          onChange={(event) => {
            if (dictationBusy) dictation.cancel()
            setDraft(event.target.value)
          }}
          onKeyDown={(event) => {
            if ((event.ctrlKey || event.metaKey) && event.key === 'Enter' && selectedSessionID) {
              event.preventDefault()
              void openSelected()
            }
          }}
        />
        <div className="quick-entry-context-actions" aria-label="Voice and visual context">
          <button
            type="button"
            className={dictationBusy ? 'active' : ''}
            onClick={toggleDictation}
            disabled={!dictation.supported || loadingDraft || creating || dictation.phase === 'stopping' || (!dictationBusy && draft.length >= QUICK_ENTRY_DRAFT_MAX)}
            title={dictation.supported ? 'Dictate into this reviewable draft' : 'Speech recognition is unavailable in this desktop runtime'}
          >
            {dictationBusy ? <MicOff size={12} /> : <Mic size={12} />}
            {dictation.phase === 'stopping' ? 'Finishing…' : dictationBusy ? 'Stop dictation' : 'Dictate'}
          </button>
          <button type="button" onClick={() => { void openComposerAction('capture-desktop') }} disabled={!selectedSessionID || loadingDraft || creating}>
            <Monitor size={12} /> Desktop
          </button>
          <button type="button" onClick={() => { void openComposerAction('capture-selection') }} disabled={!selectedSessionID || loadingDraft || creating}>
            <Crop size={12} /> Window or region
          </button>
          <span>Captures open in the full composer for permission review</span>
        </div>
        <div className="quick-entry-composer-actions">
          <span className="quick-entry-review-note">Nothing is sent automatically</span>
          <div>
            <button type="button" className="quick-entry-secondary" onClick={() => { void createSession() }} disabled={creating || loadingDraft || !selectedSessionID}>
              {creating ? <Loader2 size={13} className="spin" /> : <Plus size={13} />} New chat
            </button>
            <button type="button" className="quick-entry-primary" onClick={() => { void openSelected() }} disabled={!selectedSessionID || loadingDraft || creating}>
              Review in chat <ArrowUpRight size={13} />
            </button>
          </div>
        </div>
      </section>

      {(error || dictation.error) && (
        <div className="quick-entry-error" role="alert">
          <span>{error || dictation.error}</span>
          {dictation.error && <button type="button" onClick={dictation.clearError} aria-label="Dismiss dictation error"><X size={11} /></button>}
        </div>
      )}

      <section className="quick-entry-recent" aria-labelledby="quick-entry-recent-title">
        <div className="quick-entry-section-title" id="quick-entry-recent-title"><Clock3 size={12} /> Recent chats</div>
        <div className="quick-entry-session-list" aria-label="Recent chats">
          {recentSessions.map((session) => (
            <button
              type="button"
              key={session.id}
              aria-current={session.id === selectedSessionID ? 'true' : undefined}
              className={`quick-entry-session ${session.id === selectedSessionID ? 'selected' : ''}`}
              onClick={() => chooseSession(session.id)}
              onDoubleClick={() => { void openSession(session.id) }}
            >
              <span className="quick-entry-session-icon"><MessageSquare size={13} /></span>
              <span className="quick-entry-session-copy">
                <strong>{session.name}</strong>
                <small>{session.parentName ? `Forked from ${session.parentName}` : `${session.messages || 0} message${session.messages === 1 ? '' : 's'}`}</small>
              </span>
              <time>{formatRecency(sessionRecency(session))}</time>
            </button>
          ))}
          {recentSessions.length === 0 && (
            <div className="quick-entry-no-sessions" role="status">Chats are still loading. Open Studio to inspect the workspace.</div>
          )}
        </div>
      </section>

      <footer className="quick-entry-footer">
        <span><kbd>⌘/Ctrl</kbd><kbd>Enter</kbd> review draft</span>
        <span><kbd>Esc</kbd> close</span>
        <span><CornerDownLeft size={11} /> Enter adds a new line</span>
      </footer>
    </main>
  )
}
