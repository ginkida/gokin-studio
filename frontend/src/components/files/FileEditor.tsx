import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { AlertTriangle, Check, Copy, FileText, Loader2, MessageSquare, RefreshCw, Save, Undo2, X } from 'lucide-react'
import { GetSessionFileSnapshot, SaveSessionFileContent } from '../../../wailsjs/go/studio/Studio'
import { ClipboardSetText, EventsOn } from '../../../wailsjs/runtime/runtime'
import { composeInChat, formatFileMention } from '../../lib/composeInChat'
import { requestFileContextMenu } from './FileContextMenu'

interface SessionFileSnapshot {
  path: string
  absolutePath: string
  content: string
  revision: string
  size: number
  modifiedAt: number
  readOnly: boolean
}

interface SessionFileSaveResult {
  saved: boolean
  conflict: boolean
  current: SessionFileSnapshot
}

interface CachedFileDraft {
  snapshot: SessionFileSnapshot
  draft: string
}

const sessionFileDraftCache = new Map<string, CachedFileDraft>()
const SESSION_FILE_DRAFT_CACHE_MAX = 8

function fileDraftKey(projectID: string, sessionID: string, path: string): string {
  return `${projectID.length}:${projectID}${sessionID.length}:${sessionID}${path}`
}

function cacheFileDraft(key: string, value: CachedFileDraft) {
  sessionFileDraftCache.delete(key)
  sessionFileDraftCache.set(key, value)
  while (sessionFileDraftCache.size > SESSION_FILE_DRAFT_CACHE_MAX) {
    const oldest = sessionFileDraftCache.keys().next().value
    if (typeof oldest !== 'string') break
    sessionFileDraftCache.delete(oldest)
  }
}

export function discardCachedSessionFileDraft(projectID: string, sessionID: string, path: string) {
  sessionFileDraftCache.delete(fileDraftKey(projectID, sessionID, path))
}

function lineCount(value: string): number {
  if (!value) return 1
  let lines = 1
  for (let index = 0; index < value.length; index += 1) if (value.charCodeAt(index) === 10) lines += 1
  return lines
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KiB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`
}

export function FileEditor({
  projectID,
  sessionID,
  path,
  active = true,
  onRequestClose,
  onDirtyChange,
}: {
  projectID: string
  sessionID: string
  path: string
  active?: boolean
  onRequestClose: () => void
  onDirtyChange?: (dirty: boolean) => void
}) {
  const [snapshot, setSnapshot] = useState<SessionFileSnapshot | null>(null)
  const [draft, setDraft] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [conflict, setConflict] = useState<SessionFileSnapshot | null>(null)
  const [copied, setCopied] = useState(false)
  const requestRef = useRef(0)
  const snapshotRef = useRef<SessionFileSnapshot | null>(null)
  const noticeTimerRef = useRef<number | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const dirty = !!snapshot && draft !== snapshot.content
  const draftKey = fileDraftKey(projectID, sessionID, path)
  snapshotRef.current = snapshot
  const lines = useMemo(() => lineCount(draft), [draft])

  const showNotice = useCallback((message: string) => {
    setNotice(message)
    if (noticeTimerRef.current !== null) window.clearTimeout(noticeTimerRef.current)
    noticeTimerRef.current = window.setTimeout(() => setNotice(null), 2200)
  }, [])

  const load = useCallback(async (preserveDraft = false) => {
    const request = ++requestRef.current
    setLoading(true)
    setError(null)
    try {
      const raw: any = await GetSessionFileSnapshot(projectID, sessionID, path)
      if (requestRef.current !== request) return
      const next = raw as SessionFileSnapshot
      const opened = snapshotRef.current
      if (preserveDraft && opened) {
        if (next.revision !== opened.revision) setConflict(next)
      } else {
        const cached = sessionFileDraftCache.get(draftKey)
        if (cached) {
          setSnapshot(cached.snapshot)
          setDraft(cached.draft)
          setConflict(next.revision !== cached.snapshot.revision ? next : null)
        } else {
          setSnapshot(next)
          setDraft(next.content)
          setConflict(null)
        }
      }
    } catch (reason: any) {
      if (requestRef.current === request) setError(String(reason?.message || reason))
    } finally {
      if (requestRef.current === request) setLoading(false)
    }
  }, [draftKey, path, projectID, sessionID])

  useEffect(() => {
    setSnapshot(null)
    setDraft('')
    setConflict(null)
    setNotice(null)
    void load(false)
    return () => { requestRef.current++ }
  }, [load])

  useEffect(() => {
    onDirtyChange?.(dirty)
    window.dispatchEvent(new CustomEvent('gokin:file-editor-dirty', {
      detail: { projectID, sessionID, path, dirty },
    }))
    return () => {
      onDirtyChange?.(false)
      window.dispatchEvent(new CustomEvent('gokin:file-editor-dirty', {
        detail: { projectID, sessionID, path, dirty: false },
      }))
    }
  }, [dirty, onDirtyChange, path, projectID, sessionID])

  useEffect(() => {
    if (!snapshot) return
    if (dirty) cacheFileDraft(draftKey, { snapshot, draft })
    else sessionFileDraftCache.delete(draftKey)
  }, [dirty, draft, draftKey, snapshot])

  useEffect(() => {
    const discard = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      if (detail.projectID !== projectID || detail.sessionID !== sessionID || detail.path !== path) return
      sessionFileDraftCache.delete(draftKey)
      if (snapshotRef.current) setDraft(snapshotRef.current.content)
    }
    window.addEventListener('gokin:discard-session-file-draft', discard)
    return () => window.removeEventListener('gokin:discard-session-file-draft', discard)
  }, [draftKey, path, projectID, sessionID])

  useEffect(() => () => {
    if (noticeTimerRef.current !== null) window.clearTimeout(noticeTimerRef.current)
  }, [])

  const save = useCallback(async (revision?: string) => {
    if (!snapshot || saving || snapshot.readOnly) return
    setSaving(true)
    setError(null)
    setNotice(null)
    try {
      const raw: any = await SaveSessionFileContent(projectID, sessionID, path, draft, revision || snapshot.revision)
      const result = raw as SessionFileSaveResult
      if (result.conflict) {
        setConflict(result.current)
        return
      }
      if (!result.saved || !result.current) throw new Error('The file save returned no updated snapshot.')
      setSnapshot(result.current)
      setDraft(result.current.content)
      setConflict(null)
      sessionFileDraftCache.delete(draftKey)
      showNotice('Saved')
      window.dispatchEvent(new CustomEvent('gokin:session-file-saved', { detail: { projectID, sessionID, path } }))
    } catch (reason: any) {
      setError(String(reason?.message || reason))
    } finally {
      setSaving(false)
    }
  }, [draft, draftKey, path, projectID, saving, sessionID, showNotice, snapshot])

  useEffect(() => {
    if (!active) return
    const onKey = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229 || event.altKey || event.shiftKey) return
      if (!(event.metaKey || event.ctrlKey) || event.key.toLowerCase() !== 's') return
      if (!dirty || saving || snapshot?.readOnly) return
      event.preventDefault()
      event.stopPropagation()
      void save()
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [active, dirty, save, saving, snapshot?.readOnly])

  useEffect(() => EventsOn('chat:complete', (payload: any) => {
    if (!active || payload?.projectID !== projectID || (payload?.sessionID && payload.sessionID !== sessionID)) return
    // Keep unsaved text intact while learning the latest disk revision. If the
    // editor is clean, a completed agent turn can refresh it directly.
    void load(dirty)
  }), [active, dirty, load, projectID, sessionID])

  const copyPath = async () => {
    if (!snapshot?.absolutePath) return
    try {
      await ClipboardSetText(snapshot.absolutePath)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch (reason: any) {
      setError(String(reason?.message || reason || 'Clipboard unavailable'))
    }
  }

  const onEditorKeyDown = (event: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    if (event.nativeEvent.isComposing || event.keyCode === 229) return
    if (event.key === 'Escape' && conflict) {
      event.preventDefault()
      setConflict(null)
    }
  }

  if (loading && !snapshot) {
    return <section className="session-file-editor"><div className="session-file-editor-state"><Loader2 size={18} className="spin" /> Opening {path}…</div></section>
  }

  if (!snapshot) {
    return (
      <section className="session-file-editor">
        <header className="session-file-editor-header"><FileText size={13} /><span>{path}</span><button onClick={onRequestClose} aria-label="Close file"><X size={13} /></button></header>
        <div className="session-file-editor-state error"><AlertTriangle size={18} /><strong>File cannot be edited</strong><span>{error || 'The file is unavailable.'}</span><button onClick={() => void load(false)}><RefreshCw size={12} /> Retry</button></div>
      </section>
    )
  }

  return (
    <section className={`session-file-editor ${dirty ? 'is-dirty' : ''}`} aria-label={`File editor: ${path}`}>
      <header className="session-file-editor-header">
        <FileText size={13} />
        <button
          className="session-file-editor-path"
          onClick={() => void copyPath()}
          onContextMenu={(event) => { event.preventDefault(); requestFileContextMenu(path, sessionID, event.clientX, event.clientY, event.currentTarget) }}
          title={`Copy absolute path: ${snapshot.absolutePath}`}
        >
          <span>{snapshot.path}</span>{copied && <small><Check size={10} /> Copied</small>}
        </button>
        {dirty && <span className="session-file-editor-dirty" title="Unsaved changes" aria-label="Unsaved changes" />}
        <button onClick={() => composeInChat(`${formatFileMention(path)} `, 'replace')} title="Add file to chat"><MessageSquare size={12} /></button>
        <button onClick={() => void copyPath()} title="Copy absolute path" aria-label="Copy absolute file path"><Copy size={12} /></button>
        <button onClick={onRequestClose} title="Close file" aria-label="Close file editor"><X size={13} /></button>
      </header>

      {snapshot.readOnly && <div className="session-file-editor-banner warning"><AlertTriangle size={12} /><span>This file is read-only. You can inspect and copy it, but Studio will not overwrite it.</span></div>}
      {conflict && (
        <div className="session-file-editor-conflict" role="alert">
          <AlertTriangle size={14} />
          <div><strong>File changed on disk</strong><span>Your edits are still here. Discard them to load the current file, or explicitly override that revision.</span></div>
          <button onClick={() => { sessionFileDraftCache.delete(draftKey); setSnapshot(conflict); setDraft(conflict.content); setConflict(null); showNotice('Loaded disk version') }}><Undo2 size={11} /> Discard mine</button>
          <button className="danger" onClick={() => void save(conflict.revision)} disabled={saving}><Save size={11} /> Override</button>
        </div>
      )}
      {error && <div className="session-file-editor-banner error" role="alert"><AlertTriangle size={12} /><span>{error}</span></div>}
      {notice && <div className="session-file-editor-banner success" role="status"><Check size={12} /><span>{notice}</span></div>}

      <textarea
        ref={textareaRef}
        className="session-file-editor-input"
        value={draft}
        onChange={(event) => { setDraft(event.target.value); setError(null); setNotice(null) }}
        onKeyDown={onEditorKeyDown}
        readOnly={snapshot.readOnly || saving}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        aria-label={`Edit ${snapshot.path}`}
      />

      <footer className="session-file-editor-footer">
        <span>{lines.toLocaleString()} line{lines === 1 ? '' : 's'} · {formatBytes(new TextEncoder().encode(draft).length)} · UTF-8</span>
        <span>{snapshot.readOnly ? 'Read-only' : dirty ? 'Unsaved changes' : 'Saved on disk'}</span>
        <button onClick={() => { sessionFileDraftCache.delete(draftKey); setDraft(snapshot.content); setConflict(null); setError(null) }} disabled={!dirty || saving}><Undo2 size={11} /> Discard</button>
        <button className="primary" onClick={() => void save()} disabled={!dirty || saving || snapshot.readOnly}>{saving ? <Loader2 size={11} className="spin" /> : <Save size={11} />} Save <kbd>{navigator.platform.includes('Mac') ? '⌘S' : 'Ctrl+S'}</kbd></button>
      </footer>
    </section>
  )
}
