import { useEffect, useRef, useState } from 'react'
import { AlertTriangle, Copy, ExternalLink, FolderOpen, Loader2, MessageSquare, TerminalSquare, X } from 'lucide-react'
import { ListSessionPathActions, OpenSessionFileInApplication, ShowSessionFileInFileManager } from '../../../wailsjs/go/studio/Studio'
import { ClipboardSetText } from '../../../wailsjs/runtime/runtime'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { formatFileMention } from '../../lib/composeInChat'

interface FileApplication {
  id: string
  name: string
}

interface FileActions {
  path: string
  absolutePath: string
  isDirectory: boolean
  applications: FileApplication[]
}

interface MenuRequest {
  projectID: string
  sessionID: string
  path: string
  x: number
  y: number
  trigger?: HTMLElement | null
}

export function requestFileContextMenu(path: string, sessionID: string | undefined, x: number, y: number, trigger?: HTMLElement | null) {
  window.dispatchEvent(new CustomEvent('gokin:file-context-menu', {
    detail: { path, sessionID, x, y, trigger },
  }))
}

function fileManagerLabel(): string {
  const platform = navigator.platform.toLowerCase()
  if (platform.includes('mac')) return 'Show in Finder'
  if (platform.includes('win')) return 'Show in Explorer'
  return 'Show in file manager'
}

export function FileContextMenuHost() {
  const activeProjectID = useProjectStore((state) => state.activeProjectId)
  const [request, setRequest] = useState<MenuRequest | null>(null)
  const [actions, setActions] = useState<FileActions | null>(null)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState<string | null>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const requestSequenceRef = useRef(0)

  const close = (restoreFocus = true) => {
    const trigger = request?.trigger
    requestSequenceRef.current += 1
    setRequest(null)
    setActions(null)
    setError(null)
    setBusy('')
    if (restoreFocus && trigger?.isConnected) requestAnimationFrame(() => trigger.focus())
  }

  useEffect(() => {
    const open = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      const projectID = detail.projectID || useProjectStore.getState().activeProjectId
      if (!projectID || typeof detail.path !== 'string' || !detail.path.trim()) return
      const sessionID = detail.sessionID || useChatStore.getState().activeSession[projectID] || 'default'
      setRequest({
        projectID,
        sessionID,
        path: detail.path,
        x: Number.isFinite(detail.x) ? detail.x : window.innerWidth / 2,
        y: Number.isFinite(detail.y) ? detail.y : window.innerHeight / 2,
        trigger: detail.trigger instanceof HTMLElement ? detail.trigger : null,
      })
      setActions(null)
      setError(null)
      setBusy('')
    }
    window.addEventListener('gokin:file-context-menu', open)
    return () => window.removeEventListener('gokin:file-context-menu', open)
  }, [])

  useEffect(() => {
    if (!request) return
    const sequence = ++requestSequenceRef.current
    setLoading(true)
    ListSessionPathActions(request.projectID, request.sessionID, request.path)
      .then((raw: any) => {
        if (sequence !== requestSequenceRef.current) return
        setActions(raw as FileActions)
        requestAnimationFrame(() => menuRef.current?.querySelector<HTMLButtonElement>('button:not([disabled])')?.focus())
      })
      .catch((reason: any) => {
        if (sequence === requestSequenceRef.current) setError(String(reason?.message || reason))
      })
      .finally(() => { if (sequence === requestSequenceRef.current) setLoading(false) })
  }, [request])

  useEffect(() => {
    if (!request || request.projectID === activeProjectID) return
    requestSequenceRef.current += 1
    setRequest(null)
    setActions(null)
    setLoading(false)
    setBusy('')
    setError(null)
  }, [activeProjectID, request])

  useEffect(() => {
    if (!request) return
    const pointer = (event: PointerEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) close(false)
    }
    const key = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        close(true)
      }
    }
    window.addEventListener('pointerdown', pointer, true)
    window.addEventListener('keydown', key, true)
    return () => {
      window.removeEventListener('pointerdown', pointer, true)
      window.removeEventListener('keydown', key, true)
    }
  }, [request])

  if (!request || request.projectID !== activeProjectID) return null
  const width = 248
  const estimatedHeight = 142 + (actions?.applications.length || 0) * 30 + (error ? 44 : 0)
  const left = Math.max(8, Math.min(request.x, window.innerWidth - width - 8))
  const top = Math.max(8, Math.min(request.y, window.innerHeight - estimatedHeight - 8))

  const run = async (key: string, action: () => Promise<unknown>, closeAfter = true) => {
    if (busy) return
    setBusy(key)
    setError(null)
    try {
      await action()
      if (closeAfter) close(false)
    } catch (reason: any) {
      setError(String(reason?.message || reason))
      setBusy('')
    }
  }

  return (
    <div
      ref={menuRef}
      className="file-context-menu"
      style={{ left, top }}
      role="menu"
      aria-label={`Path actions for ${request.path}`}
      onContextMenu={(event) => { event.preventDefault(); close(true) }}
      onKeyDown={(event) => {
        if (event.key === 'Tab') {
          close(true)
          return
        }
        if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
        const items = Array.from(menuRef.current?.querySelectorAll<HTMLButtonElement>('button:not([disabled])') || [])
        if (!items.length) return
        event.preventDefault()
        const current = items.indexOf(document.activeElement as HTMLButtonElement)
        const next = event.key === 'Home' ? 0
          : event.key === 'End' ? items.length - 1
            : event.key === 'ArrowDown' ? (current + 1 + items.length) % items.length
              : (current - 1 + items.length) % items.length
        items[next]?.focus()
      }}
    >
      <div className="file-context-menu-title"><span title={request.path}>{request.path}</span><button onClick={() => close(true)} aria-label="Close file actions"><X size={11} /></button></div>
      {loading ? <div className="file-context-menu-status"><Loader2 size={12} className="spin" /> Resolving session path…</div> : actions && <>
        {actions.isDirectory ? (
          <button role="menuitem" onClick={() => {
            window.dispatchEvent(new CustomEvent('gokin:open-session-terminal', {
              detail: { projectID: request.projectID, sessionID: request.sessionID, path: actions.path },
            }))
            close(false)
          }}><TerminalSquare size={12} /><span>Open in terminal</span></button>
        ) : <button role="menuitem" onClick={() => {
          window.dispatchEvent(new CustomEvent('gokin:compose-in-chat', {
            detail: { text: `${formatFileMention(actions.path)} `, mode: 'append', sessionID: request.sessionID },
          }))
          close(false)
        }}><MessageSquare size={12} /><span>Attach as context</span></button>}
        {!actions.isDirectory && actions.applications.length > 0 && <div className="file-context-menu-label">Open in</div>}
        {!actions.isDirectory && actions.applications.map((application) => <button
          key={application.id}
          role="menuitem"
          disabled={!!busy}
          onClick={() => void run(`app:${application.id}`, () => OpenSessionFileInApplication(request.projectID, request.sessionID, actions.path, application.id))}
        >{busy === `app:${application.id}` ? <Loader2 size={12} className="spin" /> : <ExternalLink size={12} />}<span>{application.name}</span></button>)}
        <div className="file-context-menu-separator" />
        {!actions.isDirectory && <button role="menuitem" disabled={!!busy} onClick={() => void run('reveal', () => ShowSessionFileInFileManager(request.projectID, request.sessionID, actions.path))}>{busy === 'reveal' ? <Loader2 size={12} className="spin" /> : <FolderOpen size={12} />}<span>{fileManagerLabel()}</span></button>}
        <button role="menuitem" disabled={!!busy} onClick={() => void run('copy', () => ClipboardSetText(actions.absolutePath))}>{busy === 'copy' ? <Loader2 size={12} className="spin" /> : <Copy size={12} />}<span>Copy path</span></button>
      </>}
      {error && <div className="file-context-menu-error" role="alert"><AlertTriangle size={12} /><span>{error}</span></div>}
    </div>
  )
}
