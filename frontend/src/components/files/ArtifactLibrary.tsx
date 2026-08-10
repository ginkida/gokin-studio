import { useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties } from 'react'
import {
  AlertTriangle,
  Clock3,
  FileSpreadsheet,
  FileText,
  Globe2,
  History,
  LayoutDashboard,
  Loader2,
  Plus,
  Presentation,
  RotateCcw,
  Search,
} from 'lucide-react'
import { ListSessionArtifacts } from '../../../wailsjs/go/studio/Studio'
import { useProjectStore } from '../../stores/projectStore'
import { ArtifactPreview } from './ArtifactPreview'
import { composeFileInChat } from './FileBrowser'
import { composeInChat } from '../../lib/composeInChat'
import { ProjectFolderRecovery } from '../project/ProjectFolderRecovery'
import { SplitViewResizer } from '../common/SplitViewResizer'
import { usePersistentPanelWidth } from '../../hooks/usePersistentPanelWidth'

const ARTIFACT_LIST_DEFAULT_WIDTH = 340
const ARTIFACT_LIST_MIN_WIDTH = 240
const ARTIFACT_LIST_MAX_WIDTH = 600

type ArtifactSummary = {
  path: string
  name: string
  directory?: string
  mimeType: string
  previewKind: 'web' | 'document' | 'spreadsheet' | 'presentation' | 'pdf'
  size: number
  modifiedAt: number
  versionCount?: number
  latestVersionAt?: number
  previewable: boolean
  issue?: string
}

type ArtifactFilter = 'all' | ArtifactSummary['previewKind']
type ArtifactSort = 'recent' | 'name' | 'size'

const FILTERS: { key: ArtifactFilter; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'web', label: 'Web & SVG' },
  { key: 'document', label: 'Documents' },
  { key: 'spreadsheet', label: 'Sheets' },
  { key: 'presentation', label: 'Slides' },
  { key: 'pdf', label: 'PDF' },
]
const ARTIFACT_KINDS = new Set<ArtifactSummary['previewKind']>([
  'web', 'document', 'spreadsheet', 'presentation', 'pdf',
])

export function ArtifactLibrary({
  sessionID,
  artifactPath,
  onArtifactPathChange,
  isActive = true,
}: {
  sessionID: string
  artifactPath?: string | null
  onArtifactPathChange?: (path: string | null) => void
  isActive?: boolean
}) {
  const activeProjectId = useProjectStore((state) => state.activeProjectId)
  const activeProject = useProjectStore((state) => state.projects.find((project) => project.id === state.activeProjectId))
  const [items, setItems] = useState<ArtifactSummary[] | null>(null)
  const [truncated, setTruncated] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<ArtifactFilter>('all')
  const [sort, setSort] = useState<ArtifactSort>('recent')
  const [refreshKey, setRefreshKey] = useState(0)
  const [localArtifactPath, setLocalArtifactPath] = useState<string | null>(null)
  const [focusedArtifactPath, setFocusedArtifactPath] = useState('')
  const searchRef = useRef<HTMLInputElement>(null)
  const gridRef = useRef<HTMLDivElement>(null)
  const selectedArtifact = artifactPath === undefined ? localArtifactPath : artifactPath
  const setSelectedArtifact = onArtifactPathChange || setLocalArtifactPath
  const { width: artifactListWidth, updateWidth: setArtifactListWidth } = usePersistentPanelWidth(
    'gokin:artifact-preview-list-width',
    ARTIFACT_LIST_DEFAULT_WIDTH,
    ARTIFACT_LIST_MIN_WIDTH,
    ARTIFACT_LIST_MAX_WIDTH,
  )

  useEffect(() => {
    if (!activeProjectId) {
      setItems([])
      return
    }
    const projectID = activeProjectId
    let cancelled = false
    setItems(null)
    setError(null)
    ListSessionArtifacts(projectID, sessionID).then((value) => {
      if (cancelled || useProjectStore.getState().activeProjectId !== projectID) return
      const artifacts = Array.isArray(value?.artifacts)
        ? value.artifacts.filter((item) => item && ARTIFACT_KINDS.has(item.previewKind as ArtifactSummary['previewKind'])) as ArtifactSummary[]
        : []
      setItems(artifacts)
      setTruncated(!!value?.truncated)
    }).catch((reason: any) => {
      if (cancelled) return
      setError(String(reason?.message || reason || 'Failed to scan active chat artifacts'))
      setItems([])
      setTruncated(false)
    })
    return () => { cancelled = true }
  }, [activeProjectId, activeProject?.directory, sessionID, refreshKey])

  const visible = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase()
    const filtered = (items || []).filter((item) => {
      if (filter !== 'all' && item.previewKind !== filter) return false
      if (!normalizedQuery) return true
      return item.name.toLocaleLowerCase().includes(normalizedQuery) ||
        item.path.toLocaleLowerCase().includes(normalizedQuery)
    })
    return [...filtered].sort((left, right) => {
      if (sort === 'name') return left.name.localeCompare(right.name)
      if (sort === 'size') return right.size - left.size || left.path.localeCompare(right.path)
      return right.modifiedAt - left.modifiedAt || left.path.localeCompare(right.path)
    })
  }, [items, query, filter, sort])

  useEffect(() => {
    const selectedVisible = selectedArtifact && visible.some((item) => item.path === selectedArtifact)
      ? selectedArtifact
      : null
    setFocusedArtifactPath((current) => visible.some((item) => item.path === current)
      ? current
      : (selectedVisible || visible[0]?.path || ''))
  }, [selectedArtifact, visible])

  useEffect(() => {
    if (!isActive) return
    const focusSearch = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229 || document.querySelector('[aria-modal="true"]')) return
      if (!(event.ctrlKey || event.metaKey) || event.altKey || event.shiftKey || event.key.toLowerCase() !== 'f') return
      event.preventDefault()
      searchRef.current?.focus()
      searchRef.current?.select()
    }
    window.addEventListener('keydown', focusSearch)
    return () => window.removeEventListener('keydown', focusSearch)
  }, [isActive])

  if (!activeProjectId || !activeProject) return null
  if (activeProject.directoryOK === false) {
    return <ProjectFolderRecovery project={activeProject} className="artifact-library-state" />
  }

  return (
    <div
      className={`artifact-library ${selectedArtifact ? 'has-preview' : ''}`}
      style={{ '--preview-list-width': `${artifactListWidth}px` } as CSSProperties}
    >
      <section className="artifact-library-pane">
        <header className="artifact-library-header">
          <div className="artifact-library-heading">
            <LayoutDashboard size={17} />
            <div>
              <strong>Artifacts</strong>
              <span>{items === null ? 'Scanning active chat worktree…' : `${items.length} supported file${items.length === 1 ? '' : 's'} in active chat`}</span>
            </div>
          </div>
          <div className="artifact-library-header-actions">
            <button
              className="artifact-library-new"
              onClick={() => composeInChat(
                'Create a persistent live HTML artifact for this project. Ask concise clarifying questions first, then save it under artifacts/ with a descriptive filename. Use relevant project files and enabled MCP connectors as data sources. Keep the artifact origin-isolated and do not require direct network access unless I explicitly request it.',
              )}
              title="Draft a new live-artifact task in the current GLM/Kimi chat"
            >
              <Plus size={13} />
              New artifact
            </button>
            <button
              className="icon-btn"
              onClick={() => setRefreshKey((value) => value + 1)}
              disabled={items === null}
              title="Rescan project artifacts"
              aria-label="Rescan project artifacts"
            >
              <RotateCcw size={13} />
            </button>
          </div>
        </header>

        <div className="artifact-library-controls">
          <label className="artifact-library-search">
            <Search size={13} />
            <input
              ref={searchRef}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === 'Escape' && query) {
                  event.preventDefault()
                  setQuery('')
                  return
                }
                if (event.key !== 'ArrowDown') return
                const first = gridRef.current?.querySelector<HTMLButtonElement>('.artifact-library-card')
                if (!first) return
                event.preventDefault()
                first.focus()
              }}
              placeholder="Search artifacts by name or path"
              maxLength={300}
              aria-label="Search artifacts by name or path"
              aria-controls="artifact-library-results"
              aria-keyshortcuts="Control+F Meta+F"
            />
          </label>
          <select value={sort} onChange={(event) => setSort(event.target.value as ArtifactSort)} aria-label="Sort artifacts">
            <option value="recent">Most recent</option>
            <option value="name">Name</option>
            <option value="size">Largest</option>
          </select>
        </div>
        <div className="artifact-library-filters" role="group" aria-label="Artifact type">
          {FILTERS.map((option) => (
            <button
              key={option.key}
              className={filter === option.key ? 'active' : ''}
              onClick={() => setFilter(option.key)}
              aria-pressed={filter === option.key}
            >
              {option.label}
            </button>
          ))}
        </div>

        {truncated && (
          <div className="artifact-library-notice" role="status">
            <AlertTriangle size={12} />
            Results are capped for responsiveness. Narrow the project or use search after removing generated noise.
          </div>
        )}
        {error && (
          <div className="artifact-library-error" role="alert">
            <AlertTriangle size={13} />
            <span>{error}</span>
            <button onClick={() => setRefreshKey((value) => value + 1)}>Retry</button>
          </div>
        )}

        <div className="artifact-library-content">
          {items === null ? (
            <div className="artifact-library-state" role="status" aria-live="polite">
              <Loader2 size={22} className="spin" />
              <span>Discovering HTML, SVG, Office, and PDF files…</span>
            </div>
          ) : visible.length === 0 ? (
            <div className="artifact-library-state" role="status">
              <LayoutDashboard size={30} />
              <strong>{items.length === 0 ? 'No artifacts yet' : 'No matching artifacts'}</strong>
              <span>
                {items.length === 0
                  ? 'Ask GLM 5.2 or Kimi K3 to create a dashboard, document, spreadsheet, presentation, or PDF.'
                  : 'Try another search or file type.'}
              </span>
            </div>
          ) : (
            <div
              id="artifact-library-results"
              ref={gridRef}
              className="artifact-library-grid"
              role="list"
              aria-label="Project artifacts"
              onKeyDown={(event) => {
                if (event.nativeEvent.isComposing || event.keyCode === 229) return
                const card = (event.target as HTMLElement).closest<HTMLButtonElement>('.artifact-library-card')
                if (!card || !event.currentTarget.contains(card)) return
                const cards = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('.artifact-library-card'))
                const current = cards.indexOf(card)
                if (current < 0) return

                let next: HTMLButtonElement | undefined
                if (event.key === 'ArrowLeft') next = cards[current - 1]
                else if (event.key === 'ArrowRight') next = cards[current + 1]
                else if (event.key === 'Home') next = cards[0]
                else if (event.key === 'End') next = cards[cards.length - 1]
                else if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
                  const direction = event.key === 'ArrowDown' ? 1 : -1
                  const currentRect = card.getBoundingClientRect()
                  const currentX = currentRect.left + currentRect.width / 2
                  next = cards
                    .filter((candidate) => direction * (candidate.getBoundingClientRect().top - currentRect.top) > 2)
                    .sort((left, right) => {
                      const leftRect = left.getBoundingClientRect()
                      const rightRect = right.getBoundingClientRect()
                      const leftScore = Math.abs(leftRect.top - currentRect.top) * 10_000 + Math.abs(leftRect.left + leftRect.width / 2 - currentX)
                      const rightScore = Math.abs(rightRect.top - currentRect.top) * 10_000 + Math.abs(rightRect.left + rightRect.width / 2 - currentX)
                      return leftScore - rightScore
                    })[0]
                } else if (!event.ctrlKey && !event.metaKey && !event.altKey && event.key.length === 1 && event.key.trim()) {
                  const queryLetter = event.key.toLocaleLowerCase()
                  for (let offset = 1; offset <= cards.length; offset += 1) {
                    const candidate = cards[(current + offset) % cards.length]
                    if ((candidate.dataset.artifactName || '').toLocaleLowerCase().startsWith(queryLetter)) {
                      next = candidate
                      break
                    }
                  }
                } else return

                event.preventDefault()
                next?.focus()
              }}
            >
              {visible.map((item) => (
                <div key={item.path} className="artifact-library-card-item" role="listitem">
                  <button
                    className={`artifact-library-card ${selectedArtifact === item.path ? 'selected' : ''} ${item.previewable ? '' : 'unavailable'}`}
                    onClick={() => { if (item.previewable) setSelectedArtifact(item.path) }}
                    onFocus={() => setFocusedArtifactPath(item.path)}
                    aria-current={selectedArtifact === item.path ? 'true' : undefined}
                    aria-disabled={!item.previewable}
                    tabIndex={focusedArtifactPath === item.path ? 0 : -1}
                    data-artifact-name={item.name}
                    title={item.previewable ? `Open ${item.path}` : `${item.path}: ${item.issue || 'preview unavailable'}`}
                  >
                    <div className={`artifact-library-card-icon ${item.previewKind}`}>
                      <ArtifactKindIcon kind={item.previewKind} />
                    </div>
                    <div className="artifact-library-card-copy">
                      <strong>{item.name}</strong>
                      <span>{item.directory || 'Project root'}</span>
                      <div className="artifact-library-card-meta">
                        <span><Clock3 size={10} />{formatArtifactDate(item.modifiedAt)}</span>
                        <span>{formatArtifactSize(item.size)}</span>
                        {!!item.versionCount && <span><History size={10} />{item.versionCount}</span>}
                      </div>
                      {!item.previewable && <small>{item.issue || 'Preview unavailable'}</small>}
                    </div>
                  </button>
                </div>
              ))}
            </div>
          )}
        </div>
      </section>

      {selectedArtifact && (
        <>
          <SplitViewResizer
            value={artifactListWidth}
            min={ARTIFACT_LIST_MIN_WIDTH}
            max={ARTIFACT_LIST_MAX_WIDTH}
            minSecondary={320}
            defaultValue={ARTIFACT_LIST_DEFAULT_WIDTH}
            label="Resize artifact library and preview"
            onChange={setArtifactListWidth}
          />
          <ArtifactPreview
            projectID={activeProjectId}
            sessionID={sessionID}
            path={selectedArtifact}
            onClose={() => setSelectedArtifact(null)}
            onAddToChat={composeFileInChat}
            active={isActive}
          />
        </>
      )}
    </div>
  )
}

function ArtifactKindIcon({ kind }: { kind: ArtifactSummary['previewKind'] }) {
  if (kind === 'web') return <Globe2 size={20} />
  if (kind === 'spreadsheet') return <FileSpreadsheet size={20} />
  if (kind === 'presentation') return <Presentation size={20} />
  return <FileText size={20} />
}

function formatArtifactSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} KiB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`
}

function formatArtifactDate(timestamp: number) {
  if (!timestamp) return 'Unknown'
  const delta = Date.now() - timestamp
  if (delta >= 0 && delta < 60_000) return 'Now'
  if (delta >= 0 && delta < 3_600_000) return `${Math.max(1, Math.floor(delta / 60_000))}m`
  if (delta >= 0 && delta < 86_400_000) return `${Math.max(1, Math.floor(delta / 3_600_000))}h`
  return new Date(timestamp).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}
