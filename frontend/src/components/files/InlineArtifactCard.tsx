import { useMemo, useState } from 'react'
import { AlertTriangle, ChevronRight, Download, ExternalLink, Loader2, PanelsTopLeft } from 'lucide-react'
import { ReadSessionArtifactContent } from '../../../wailsjs/go/studio/Studio'
import {
  buildSandboxedArtifactDocument,
  type ArtifactDocument,
} from './ArtifactPreview'
import { requestFileContextMenu } from './FileContextMenu'

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

  const load = async () => {
    if (artifact || loading) return
    setLoading(true)
    setError(null)
    try {
      const result: any = await ReadSessionArtifactContent(projectID, sessionID, path)
      setArtifact(result as ArtifactDocument)
    } catch (e: any) {
      setError(String(e?.message || e || 'Preview unavailable'))
    } finally {
      setLoading(false)
    }
  }

  const toggle = () => {
    const next = !expanded
    setExpanded(next)
    if (next) void load()
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
    const url = URL.createObjectURL(blob)
    const anchor = window.document.createElement('a')
    anchor.href = url
    anchor.download = artifact.name
    anchor.click()
    URL.revokeObjectURL(url)
  }

  return (
    <section className={`inline-artifact ${expanded ? 'expanded' : ''}`}>
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
