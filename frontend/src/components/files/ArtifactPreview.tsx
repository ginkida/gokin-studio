import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertTriangle,
  Check,
  Code2,
  Copy,
  Download,
  ExternalLink,
  History,
  Loader2,
  Monitor,
  RefreshCw,
  RotateCcw,
  Send,
  ShieldCheck,
  Sparkles,
  Smartphone,
  Tablet,
  Wifi,
  WifiOff,
  X,
  XCircle,
} from 'lucide-react'
import {
  ListArtifactVersions,
  ListSessionArtifactVersions,
  ReadArtifactContent,
  ReadArtifactVersion,
  ReadSessionArtifactContent,
  ReadSessionArtifactVersion,
  RestoreArtifactVersion,
  RestoreSessionArtifactVersion,
  SaveArtifactVersion,
  SaveSessionArtifactVersion,
} from '../../../wailsjs/go/studio/Studio'
import { ClipboardSetText } from '../../../wailsjs/runtime/runtime'
import { composeInChat, formatFileMention } from '../../lib/composeInChat'
import { useConfirmDialog } from '../common/AppDialog'
import { requestFileContextMenu } from './FileContextMenu'
import { downloadBlob } from '../../lib/download'

export type ArtifactDocument = {
  path: string
  name: string
  mimeType: string
  content: string
  dataBase64?: string
  previewKind: 'web' | 'document' | 'spreadsheet' | 'presentation' | 'pdf'
  size: number
  modifiedAt: number
}

type ArtifactVersion = {
  id: string
  digest: string
  path: string
  name: string
  mimeType: string
  size: number
  createdAt: number
  sourceModifiedAt: number
}

const OFFLINE_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline' blob:",
  "style-src 'unsafe-inline'",
  "img-src data: blob:",
  "font-src data:",
  "media-src data: blob:",
  "connect-src 'none'",
  "frame-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
].join('; ')

const NETWORK_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline' blob: https: http:",
  "style-src 'unsafe-inline' https: http:",
  "img-src data: blob: https: http:",
  "font-src data: https: http:",
  "media-src data: blob: https: http:",
  "connect-src https: http:",
  "frame-src https: http:",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
].join('; ')

export function buildSandboxedArtifactDocument(source: string, allowNetwork: boolean): string {
  const policy = allowNetwork ? NETWORK_CSP : OFFLINE_CSP
  // The policy is emitted before any untrusted source. Wrapping (instead of
  // string-inserting into the source's <head>) prevents a crafted early script
  // or malformed document from running before CSP becomes active.
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${policy}"><meta name="referrer" content="no-referrer"><base href="about:blank"><style>html,body{min-height:100%;margin:0}</style></head><body>${source}</body></html>`
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / 1024 / 1024).toFixed(2)} MB`
}

function formatVersionTime(value: number) {
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(value))
}

function isNativeArtifactPath(path: string) {
  return /\.(?:docx|xlsx|pptx|pdf)$/i.test(path)
}

function base64Bytes(value: string): Uint8Array {
  const binary = window.atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index)
  }
  return bytes
}

export function ArtifactPreview({
  projectID,
  sessionID,
  path,
  onClose,
  onAddToChat,
  active = true,
}: {
  projectID: string
  sessionID?: string
  path: string
  onClose: () => void
  onAddToChat: (path: string) => void
  active?: boolean
}) {
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const [artifact, setArtifact] = useState<ArtifactDocument | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [allowNetwork, setAllowNetwork] = useState(false)
  const [viewport, setViewport] = useState<'desktop' | 'tablet' | 'mobile'>('desktop')
  const [revision, setRevision] = useState(0)
  const [copyStatus, setCopyStatus] = useState<'idle' | 'copied' | 'error'>('idle')
  const [versions, setVersions] = useState<ArtifactVersion[]>([])
  const [historyOpen, setHistoryOpen] = useState(false)
  const [historyError, setHistoryError] = useState<string | null>(null)
  const [viewingVersion, setViewingVersion] = useState<ArtifactVersion | null>(null)
  const [versionBusy, setVersionBusy] = useState<string | null>(null)
  const latestRef = useRef<ArtifactDocument | null>(null)
  const viewingVersionRef = useRef<string | null>(null)
  const activeRef = useRef(active)
  const loadRequestRef = useRef(0)
  const loadInFlightRef = useRef(false)
  const versionsRequestRef = useRef(0)
  const snapshotRequestRef = useRef(0)
  const versionActionRequestRef = useRef(0)
  const restoreInFlightRef = useRef(false)
  const copyRequestRef = useRef(0)
  const copyTimerRef = useRef<number | null>(null)
  const scopeKey = `${projectID.length}:${projectID}${(sessionID || '').length}:${sessionID || ''}${path}`
  const scopeRef = useRef({ generation: 0, mounted: false })

  useEffect(() => {
    scopeRef.current.generation += 1
    scopeRef.current.mounted = true
    const generation = scopeRef.current.generation
    return () => {
      if (scopeRef.current.generation === generation) {
        scopeRef.current.mounted = false
        scopeRef.current.generation += 1
      }
      loadRequestRef.current += 1
      loadInFlightRef.current = false
      versionsRequestRef.current += 1
      snapshotRequestRef.current += 1
      versionActionRequestRef.current += 1
      restoreInFlightRef.current = false
      copyRequestRef.current += 1
      if (copyTimerRef.current !== null) {
        window.clearTimeout(copyTimerRef.current)
        copyTimerRef.current = null
      }
    }
  }, [scopeKey])

  const ownsScope = useCallback((generation: number) => (
    scopeRef.current.mounted && scopeRef.current.generation === generation
  ), [])

  const refreshVersions = useCallback(async (expectedScope = scopeRef.current.generation) => {
    const request = ++versionsRequestRef.current
    const result: any = sessionID
      ? await ListSessionArtifactVersions(projectID, sessionID, path)
      : await ListArtifactVersions(projectID, path)
    if (!ownsScope(expectedScope) || versionsRequestRef.current !== request) return false
    setVersions((result || []) as ArtifactVersion[])
    return true
  }, [ownsScope, path, projectID, sessionID])

  const snapshotVersion = useCallback(async (expectedScope: number) => {
    const request = ++snapshotRequestRef.current
    versionsRequestRef.current += 1
    try {
      if (sessionID) await SaveSessionArtifactVersion(projectID, sessionID, path)
      else await SaveArtifactVersion(projectID, path)
      if (!ownsScope(expectedScope) || snapshotRequestRef.current !== request) return
      await refreshVersions(expectedScope)
      if (ownsScope(expectedScope) && snapshotRequestRef.current === request) setHistoryError(null)
    } catch (e: any) {
      if (!ownsScope(expectedScope) || snapshotRequestRef.current !== request) return
      // Version history is additive: a storage failure must not blank or stop
      // the otherwise safe live preview.
      setHistoryError(String(e?.message || e || 'Failed to save artifact version'))
    }
  }, [ownsScope, path, projectID, refreshVersions, sessionID])

  const load = useCallback(async (quiet = false, expectedScope = scopeRef.current.generation) => {
    if (restoreInFlightRef.current) return false
    // A live poll must not supersede an explicit initial/retry/reload request;
    // the next sequential poll will observe its result instead.
    if (quiet && loadInFlightRef.current) return false
    const request = ++loadRequestRef.current
    loadInFlightRef.current = true
    if (!quiet) setLoading(true)
    try {
      const result: any = sessionID
        ? await ReadSessionArtifactContent(projectID, sessionID, path)
        : await ReadArtifactContent(projectID, path)
      if (!ownsScope(expectedScope) || loadRequestRef.current !== request) return false
      const next = result as ArtifactDocument
      const previous = latestRef.current
      latestRef.current = next
      if (!viewingVersionRef.current) setArtifact(next)
      setError(null)
      const changed = !previous || previous.modifiedAt !== next.modifiedAt || previous.content !== next.content
      if (previous && changed) {
        setRevision((value) => value + 1)
      }
      if (changed && next.previewKind === 'web') await snapshotVersion(expectedScope)
      return true
    } catch (e: any) {
      if (!ownsScope(expectedScope) || loadRequestRef.current !== request) return false
      // A transient polling failure should not blank a working preview. Only
      // an explicit/initial load replaces the document with an error state.
      if (!quiet) {
        setError(String(e?.message || e || 'Failed to load artifact'))
        setArtifact(null)
      }
      return false
    } finally {
      if (ownsScope(expectedScope) && loadRequestRef.current === request) {
        loadInFlightRef.current = false
        if (!quiet) setLoading(false)
      }
    }
  }, [ownsScope, path, projectID, sessionID, snapshotVersion])

  useEffect(() => {
    latestRef.current = null
    viewingVersionRef.current = null
    setArtifact(null)
    setError(null)
    setLoading(true)
    setViewingVersion(null)
    setVersionBusy(null)
    setVersions([])
    setHistoryError(null)
    setHistoryOpen(false)
    setAllowNetwork(false)
    setViewport('desktop')
    setRevision(0)
    setCopyStatus('idle')
    void load()
    return () => { loadRequestRef.current += 1 }
  }, [load])

  // Live refresh while GLM/Kimi edits the artifact. Polling the bounded,
  // worktree-rooted RPC avoids granting the iframe filesystem access.
  useEffect(() => {
    const becameActive = !activeRef.current && active
    activeRef.current = active
    if (!active || isNativeArtifactPath(path)) return
    const scope = scopeRef.current.generation
    let stopped = false
    let timer: number | undefined
    const poll = async () => {
      if (stopped || !ownsScope(scope)) return
      if (window.document.visibilityState !== 'hidden') await load(true, scope)
      if (stopped || !ownsScope(scope)) return
      timer = window.setTimeout(() => { void poll() }, 1500)
    }
    if (becameActive) void poll()
    else timer = window.setTimeout(() => { void poll() }, 1500)
    return () => {
      stopped = true
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [active, load, ownsScope, path])

  const srcDoc = useMemo(
    () => buildSandboxedArtifactDocument(
      artifact?.content || '',
      artifact?.previewKind === 'web' && allowNetwork,
    ),
    [artifact?.content, artifact?.previewKind, allowNetwork],
  )
  const pdfSource = artifact?.previewKind === 'pdf' && artifact.dataBase64
    ? `data:${artifact.mimeType};base64,${artifact.dataBase64}`
    : undefined
  const isWebPreview = artifact?.previewKind === 'web'
  const isNativePreview = Boolean(artifact && artifact.previewKind !== 'web')

  const download = () => {
    if (!artifact) return
    const content = artifact.dataBase64
      ? base64Bytes(artifact.dataBase64)
      : artifact.content
    const blob = new Blob([content], {
      type: artifact.dataBase64 ? artifact.mimeType : `${artifact.mimeType};charset=utf-8`,
    })
    downloadBlob(blob, artifact.name)
  }

  const copy = async () => {
    if (!artifact) return
    const scope = scopeRef.current.generation
    const request = ++copyRequestRef.current
    const content = artifact.content
    try {
      await ClipboardSetText(content)
      if (!ownsScope(scope) || copyRequestRef.current !== request) return
      setCopyStatus('copied')
    } catch {
      if (!ownsScope(scope) || copyRequestRef.current !== request) return
      setCopyStatus('error')
    }
    if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current)
    copyTimerRef.current = window.setTimeout(() => {
      if (ownsScope(scope) && copyRequestRef.current === request) setCopyStatus('idle')
      copyTimerRef.current = null
    }, 1800)
  }

  const viewVersion = async (version: ArtifactVersion) => {
    const scope = scopeRef.current.generation
    const request = ++versionActionRequestRef.current
    setVersionBusy(`view:${version.id}`)
    setHistoryError(null)
    try {
      const result: any = sessionID
        ? await ReadSessionArtifactVersion(projectID, sessionID, path, version.id)
        : await ReadArtifactVersion(projectID, path, version.id)
      if (!ownsScope(scope) || versionActionRequestRef.current !== request) return
      viewingVersionRef.current = version.id
      setViewingVersion(version)
      setArtifact(result as ArtifactDocument)
      setRevision((value) => value + 1)
    } catch (e: any) {
      if (ownsScope(scope) && versionActionRequestRef.current === request) {
        setHistoryError(String(e?.message || e || 'Failed to load artifact version'))
      }
    } finally {
      if (ownsScope(scope) && versionActionRequestRef.current === request) setVersionBusy(null)
    }
  }

  const showLiveVersion = () => {
    viewingVersionRef.current = null
    setViewingVersion(null)
    if (latestRef.current) setArtifact(latestRef.current)
    setRevision((value) => value + 1)
  }

  const restoreVersion = async (version: ArtifactVersion) => {
    const scope = scopeRef.current.generation
    const accepted = await requestConfirmation({
      title: 'Restore this version?',
      message: `Restore the version from ${formatVersionTime(version.createdAt)}. The current file is saved to history first, then the ${sessionID ? 'active chat worktree file' : 'project file'} is replaced.`,
      confirmLabel: 'Restore version',
      danger: true,
    })
    if (!accepted || !ownsScope(scope)) return
    const request = ++versionActionRequestRef.current
    restoreInFlightRef.current = true
    loadRequestRef.current += 1
    loadInFlightRef.current = false
    versionsRequestRef.current += 1
    setVersionBusy(`restore:${version.id}`)
    setHistoryError(null)
    try {
      const result: any = sessionID
        ? await RestoreSessionArtifactVersion(projectID, sessionID, path, version.id)
        : await RestoreArtifactVersion(projectID, path, version.id)
      if (!ownsScope(scope) || versionActionRequestRef.current !== request) return
      const next = result as ArtifactDocument
      latestRef.current = next
      viewingVersionRef.current = null
      setViewingVersion(null)
      setArtifact(next)
      setRevision((value) => value + 1)
      await refreshVersions(scope)
    } catch (e: any) {
      if (ownsScope(scope) && versionActionRequestRef.current === request) {
        setHistoryError(String(e?.message || e || 'Failed to restore artifact version'))
      }
    } finally {
      if (ownsScope(scope) && versionActionRequestRef.current === request) {
        restoreInFlightRef.current = false
        setVersionBusy(null)
      }
    }
  }

  return (
    <section className="artifact-preview" aria-label={`Artifact preview: ${path}`}>
      <header className="artifact-preview-header">
        <div
          className="artifact-title"
          onContextMenu={(event) => {
            if (!sessionID) return
            event.preventDefault()
            requestFileContextMenu(path, sessionID, event.clientX, event.clientY, event.currentTarget)
          }}
        >
          <ExternalLink size={14} />
          <div>
            <strong>{artifact?.name || path.split('/').pop()}</strong>
            <span>{path}{artifact ? ` · ${formatBytes(artifact.size)}` : ''}</span>
          </div>
        </div>
        <div className="artifact-actions">
          <div className="artifact-viewport-switch" role="group" aria-label="Preview width">
            <button className={viewport === 'desktop' ? 'active' : ''} onClick={() => setViewport('desktop')} title="Desktop width"><Monitor size={12} /></button>
            <button className={viewport === 'tablet' ? 'active' : ''} onClick={() => setViewport('tablet')} title="Tablet width"><Tablet size={12} /></button>
            <button className={viewport === 'mobile' ? 'active' : ''} onClick={() => setViewport('mobile')} title="Mobile width"><Smartphone size={12} /></button>
          </div>
          {isWebPreview && (
            <button
              className={`artifact-network ${allowNetwork ? 'enabled' : ''}`}
              onClick={() => setAllowNetwork((value) => !value)}
              title={allowNetwork ? 'Network enabled — click to isolate artifact' : 'Network blocked — click to allow external HTTPS/HTTP resources'}
            >
              {allowNetwork ? <Wifi size={12} /> : <WifiOff size={12} />}
              {allowNetwork ? 'Network on' : 'Isolated'}
            </button>
          )}
          <button onClick={() => { setRevision((value) => value + 1); void load() }} disabled={loading || versionBusy !== null} title="Reload preview"><RefreshCw size={12} /></button>
          {isWebPreview && (
            <>
              <button
                className={historyOpen ? 'active' : ''}
                onClick={() => setHistoryOpen((value) => !value)}
                title="Version history"
              >
                <History size={12} />
                {versions.length > 0 && <span>{versions.length}</span>}
              </button>
              <button
                className={copyStatus === 'error' ? 'is-error' : ''}
                onClick={() => void copy()}
                disabled={!artifact}
                title={copyStatus === 'copied' ? 'Source copied' : copyStatus === 'error' ? 'Clipboard unavailable' : 'Copy source'}
                aria-label={copyStatus === 'copied' ? 'Source copied' : copyStatus === 'error' ? 'Source copy failed' : 'Copy source'}
                aria-live="polite"
              >
                {copyStatus === 'copied' ? <Check size={12} /> : copyStatus === 'error' ? <XCircle size={12} /> : <Copy size={12} />}
              </button>
            </>
          )}
          <button onClick={download} disabled={!artifact} title="Download artifact"><Download size={12} /></button>
          <button
            className="artifact-chat-draft"
            onClick={() => onAddToChat(path)}
            title="Add this file to the current chat draft. Nothing is sent until you press Send."
          >
            <Send size={12} />
            <span>Add to chat</span>
          </button>
          <button
            className="artifact-agent-update"
            onClick={() => composeInChat(
              `Update the persistent artifact at ${formatFileMention(path)}. Inspect its current source first, then refresh it using the latest relevant project files and enabled MCP connectors. Preserve useful behavior and visual structure, validate the result, and keep direct artifact network access disabled unless I explicitly approve it.`,
            )}
            title="Draft an update task in the current GLM/Kimi chat"
          >
            <Sparkles size={12} />
            <span>Update</span>
          </button>
          <button onClick={onClose} title="Close preview"><X size={13} /></button>
        </div>
      </header>

      {isWebPreview && !allowNetwork && (
        <div className="artifact-security-bar">
          <ShieldCheck size={11} />
          Scripts run in an origin-isolated sandbox. Parent app, forms, popups, external assets, and API requests are blocked.
        </div>
      )}
      {isWebPreview && allowNetwork && (
        <div className="artifact-security-bar warning">
          <AlertTriangle size={11} />
          Network access is enabled for this preview. The iframe remains origin-isolated and cannot access Gokin Studio.
        </div>
      )}
      {isNativePreview && artifact?.previewKind !== 'pdf' && (
        <div className="artifact-security-bar">
          <ShieldCheck size={11} />
          Local preview generated from the native file. Macros, scripts, external links, and embedded objects are not executed.
        </div>
      )}
      {artifact?.previewKind === 'pdf' && (
        <div className="artifact-security-bar">
          <ShieldCheck size={11} />
          PDF is shown in the system viewer inside an isolated frame.
        </div>
      )}

      <div className="artifact-preview-body">
        <div className="artifact-stage">
          {loading && <div className="artifact-state"><Loader2 size={18} className="spin" /> Loading artifact…</div>}
          {!loading && error && (
            <div className="artifact-state error">
              <AlertTriangle size={20} />
              <strong>Preview unavailable</strong>
              <span>{error}</span>
              <button className="btn-secondary" onClick={() => void load()}><RefreshCw size={12} /> Retry</button>
            </div>
          )}
          {!loading && artifact && active && (
            <div className={`artifact-frame-shell ${viewport}`}>
              <iframe
                key={`${revision}-${allowNetwork ? 'online' : 'offline'}`}
                className="artifact-frame"
                src={artifact.previewKind === 'pdf' ? pdfSource : undefined}
                srcDoc={artifact.previewKind === 'pdf' ? undefined : srcDoc}
                sandbox={artifact.previewKind === 'web' ? 'allow-scripts' : ''}
                referrerPolicy="no-referrer"
                title={`Preview of ${artifact.name}`}
              />
            </div>
          )}
        </div>
        {isWebPreview && historyOpen && (
          <aside className="artifact-history" aria-label="Artifact version history">
            <div className="artifact-history-header">
              <div>
                <strong>Version history</strong>
                <span>Latest 20 · 128 MiB/project</span>
              </div>
              {viewingVersion && (
                <button onClick={showLiveVersion} title="Return to live file">
                  <RefreshCw size={11} /> Live
                </button>
              )}
            </div>
            {historyError && (
              <div className="artifact-history-error" role="alert">
                <AlertTriangle size={11} />
                <span>{historyError}</span>
              </div>
            )}
            {versions.length === 0 && !historyError && (
              <div className="artifact-history-empty">No saved versions yet.</div>
            )}
            <div className="artifact-version-list">
              {versions.map((version, index) => {
                const selected = viewingVersion?.id === version.id
                return (
                  <div className={`artifact-version ${selected ? 'selected' : ''}`} key={version.id}>
                    <button
                      className="artifact-version-main"
                      onClick={() => void viewVersion(version)}
                      disabled={versionBusy !== null}
                      title="Preview this version"
                    >
                      <span>{index === 0 ? 'Current saved' : formatVersionTime(version.createdAt)}</span>
                      <small>{formatBytes(version.size)} · {version.digest.slice(0, 8)}</small>
                    </button>
                    <button
                      className="artifact-version-restore"
                      onClick={() => void restoreVersion(version)}
                      disabled={versionBusy !== null || (index === 0 && !viewingVersion)}
                      title={`Restore this version to the ${sessionID ? 'active chat worktree' : 'project file'}`}
                    >
                      {versionBusy === `restore:${version.id}`
                        ? <Loader2 size={11} className="spin" />
                        : <RotateCcw size={11} />}
                    </button>
                  </div>
                )
              })}
            </div>
          </aside>
        )}
      </div>
      <footer className="artifact-footer">
        <Code2 size={11} />
        {isWebPreview
          ? (
            <>
              {viewingVersion
                ? `Viewing saved version from ${formatVersionTime(viewingVersion.createdAt)}`
                : 'Live preview refreshes when the file changes'}
              {' · '}{sessionID ? 'active chat worktree · ' : ''}HTML/SVG up to 2 MiB
            </>
          )
          : `${(artifact?.name.split('.').pop() || 'native').toUpperCase()} · native file up to 30 MiB · passive preview`}
      </footer>
      {confirmationDialog}
    </section>
  )
}
