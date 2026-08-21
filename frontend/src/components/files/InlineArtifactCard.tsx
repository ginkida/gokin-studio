import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, ChevronRight, Download, ExternalLink, Loader2, PanelsTopLeft } from 'lucide-react'
import { ReadSessionArtifactContent } from '../../../wailsjs/go/studio/Studio'
import {
  buildSandboxedArtifactDocument,
  type ArtifactDocument,
} from './ArtifactPreview'
import { requestFileContextMenu } from './FileContextMenu'
import { downloadBlob } from '../../lib/download'

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`
  return `${(value / 1024 / 1024).toFixed(2)} MB`
}

function base64Bytes(value: string): Uint8Array {
  const binary = window.atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index += 1) {
    bytes[index] = binary.charCodeAt(index)
  }
  return bytes
}

function artifactLabel(path: string) {
  const extension = path.split('.').pop()?.toUpperCase() || 'ARTIFACT'
  return extension === 'HTM' ? 'HTML' : extension
}

export function InlineArtifactCard({
  projectID,
  sessionID,
  path,
}: {
  projectID: string
  sessionID: string
  path: string
}) {
  const [expanded, setExpanded] = useState(false)
  const [artifact, setArtifact] = useState<ArtifactDocument | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const loadRequestRef = useRef(0)
  const loadInFlightRef = useRef(false)
  const scopeKey = `${projectID.length}:${projectID}${sessionID.length}:${sessionID}${path}`
  const scopeRef = useRef({ generation: 0, mounted: false })

  useEffect(() => {
    scopeRef.current.generation += 1
    scopeRef.current.mounted = true
    const generation = scopeRef.current.generation
    setExpanded(false)
    setArtifact(null)
    setLoading(false)
    setError(null)
    return () => {
      if (scopeRef.current.generation === generation) {
        scopeRef.current.mounted = false
        scopeRef.current.generation += 1
      }
      loadRequestRef.current += 1
      loadInFlightRef.current = false
    }
  }, [scopeKey])

  const ownsScope = useCallback((generation: number) => (
    scopeRef.current.mounted && scopeRef.current.generation === generation
  ), [])

  const load = useCallback(async (quiet = false, expectedScope = scopeRef.current.generation) => {
    // Polls never supersede an explicit first load or retry. Scheduling the
    // next poll after this promise settles keeps backend reads sequential.
    if (quiet && loadInFlightRef.current) return false
    const request = ++loadRequestRef.current
    loadInFlightRef.current = true
    if (!quiet) {
      setLoading(true)
      setError(null)
    }
    try {
      const result: any = await ReadSessionArtifactContent(projectID, sessionID, path)
      if (!ownsScope(expectedScope) || loadRequestRef.current !== request) return false
      setArtifact(result as ArtifactDocument)
      setError(null)
      return true
    } catch (e: any) {
      if (!ownsScope(expectedScope) || loadRequestRef.current !== request) return false
      // A transient live-refresh failure should not blank a usable preview.
      if (!quiet) setError(String(e?.message || e || 'Preview unavailable'))
      return false
    } finally {
      if (ownsScope(expectedScope) && loadRequestRef.current === request) {
        loadInFlightRef.current = false
        if (!quiet) setLoading(false)
      }
    }
  }, [ownsScope, path, projectID, sessionID])

  useEffect(() => {
    if (!expanded) return
    const scope = scopeRef.current.generation
    let stopped = false
    let timer: number | undefined
    const poll = async () => {
      if (stopped || !ownsScope(scope)) return
      if (window.document.visibilityState !== 'hidden') await load(true, scope)
      if (stopped || !ownsScope(scope)) return
      timer = window.setTimeout(() => { void poll() }, 2500)
    }
    timer = window.setTimeout(() => { void poll() }, 2500)
    return () => {
      stopped = true
      if (timer !== undefined) window.clearTimeout(timer)
    }
  }, [expanded, load, ownsScope])

  const toggle = () => {
    const next = !expanded
    setExpanded(next)
    if (next) void load(Boolean(artifact))
  }

  const srcDoc = useMemo(
    () => buildSandboxedArtifactDocument(artifact?.content || '', false),
    [artifact?.content],
  )
  const pdfSource = artifact?.previewKind === 'pdf' && artifact.dataBase64
    ? `data:${artifact.mimeType};base64,${artifact.dataBase64}`
    : undefined

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

  return (
    <section className={`inline-artifact ${expanded ? 'expanded' : ''}`} aria-busy={expanded && loading}>
      <div
        className="inline-artifact-header"
        onContextMenu={(event) => {
          event.preventDefault()
          requestFileContextMenu(path, sessionID, event.clientX, event.clientY, event.currentTarget)
        }}
      >
        <button
          type="button"
          className="inline-artifact-toggle"
          onClick={toggle}
          aria-expanded={expanded}
          title={expanded ? 'Collapse inline preview' : 'Preview artifact inline'}
        >
          <PanelsTopLeft size={13} />
          <span className="inline-artifact-name">{path.split('/').pop()}</span>
          <span className="inline-artifact-meta">
            {artifact ? formatBytes(artifact.size) : artifactLabel(path)}
          </span>
          {loading
            ? <Loader2 size={12} className="spin" />
            : <ChevronRight size={12} className={`inline-artifact-chevron ${expanded ? 'expanded' : ''}`} />}
        </button>
        {artifact && (
          <button
            type="button"
            className="inline-artifact-action"
            onClick={download}
            title={`Download ${artifact.name}`}
          >
            <Download size={12} />
          </button>
        )}
        <button
          type="button"
          className="inline-artifact-action"
          onClick={() => window.dispatchEvent(new CustomEvent('gokin:open-artifact', { detail: { path, sessionID } }))}
          title="Open full artifact preview"
        >
          <ExternalLink size={12} />
        </button>
      </div>
      {expanded && (
        <div className="inline-artifact-body">
          {loading && (
            <div className="inline-artifact-state">
              <Loader2 size={15} className="spin" /> Loading preview…
            </div>
          )}
          {!loading && error && (
            <div className="inline-artifact-state error">
              <AlertTriangle size={14} />
              <span>{error}</span>
              <button type="button" onClick={() => void load()}>Retry</button>
            </div>
          )}
          {!loading && artifact && (
            <iframe
              className="inline-artifact-frame"
              src={artifact.previewKind === 'pdf' ? pdfSource : undefined}
              srcDoc={artifact.previewKind === 'pdf' ? undefined : srcDoc}
              sandbox={artifact.previewKind === 'web' ? 'allow-scripts' : ''}
              referrerPolicy="no-referrer"
              title={`Inline preview of ${artifact.name}`}
            />
          )}
        </div>
      )}
    </section>
  )
}
