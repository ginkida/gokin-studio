import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ChevronRight, File, Folder, FolderOpen, RotateCcw, Search, X } from 'lucide-react'
import { ListSessionDirectory } from '../../../wailsjs/go/studio/Studio'

interface Entry {
  name: string
  path: string
  isDir: boolean
  size: number
}

type DirState = {
  loaded: boolean
  entries: Entry[]
  expanded: boolean
  error?: string
}

export function FilePicker({
  projectId,
  sessionId,
  onPick,
  onClose,
}: {
  projectId: string
  sessionId: string
  onPick: (path: string) => void
  onClose: () => void
}) {
  const [dirs, setDirs] = useState<Record<string, DirState>>({})
  const [query, setQuery] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)
  const requestSequenceRef = useRef(0)
  const directoryRequestsRef = useRef(new Map<string, number>())
  const scopeKey = `${projectId.length}:${projectId}${sessionId.length}:${sessionId}`
  const scopeRef = useRef({ generation: 0, mounted: false })

  const ownsScope = useCallback((generation: number) => (
    scopeRef.current.mounted && scopeRef.current.generation === generation
  ), [])

  const loadDirectory = useCallback((path: string, expectedScope = scopeRef.current.generation) => {
    const request = ++requestSequenceRef.current
    directoryRequestsRef.current.set(path, request)
    setDirs((current) => {
      const existing = current[path] || { loaded: false, entries: [], expanded: path === '' }
      return {
        ...current,
        [path]: { ...existing, loaded: false, expanded: path === '' ? true : existing.expanded, error: undefined },
      }
    })
    return ListSessionDirectory(projectId, sessionId, path).then((entries) => {
      if (!ownsScope(expectedScope) || directoryRequestsRef.current.get(path) !== request) return false
      setDirs((current) => {
        const existing = current[path] || { loaded: false, entries: [], expanded: path === '' }
        return {
          ...current,
          [path]: { ...existing, loaded: true, entries: (entries || []) as Entry[], error: undefined },
        }
      })
      return true
    }).catch((err) => {
      if (!ownsScope(expectedScope) || directoryRequestsRef.current.get(path) !== request) return false
      setDirs((current) => {
        const existing = current[path] || { loaded: false, entries: [], expanded: path === '' }
        return {
          ...current,
          [path]: { ...existing, loaded: true, entries: [], error: String(err?.message || err) },
        }
      })
      return false
    })
  }, [ownsScope, projectId, sessionId])

  useEffect(() => {
    scopeRef.current.generation += 1
    scopeRef.current.mounted = true
    const generation = scopeRef.current.generation
    directoryRequestsRef.current.clear()
    setDirs({})
    setQuery('')
    inputRef.current?.focus()
    void loadDirectory('', generation)
    return () => {
      if (scopeRef.current.generation === generation) {
        scopeRef.current.mounted = false
        scopeRef.current.generation += 1
      }
      requestSequenceRef.current += 1
      directoryRequestsRef.current.clear()
    }
  }, [loadDirectory, scopeKey])

  const toggle = (path: string) => {
    const current = dirs[path] || { loaded: false, entries: [], expanded: false }
    const expanded = !current.expanded
    setDirs((state) => ({ ...state, [path]: { ...(state[path] || current), expanded } }))
    if (expanded && (!current.loaded || current.error)) void loadDirectory(path)
  }

  const onKey = (e: React.KeyboardEvent) => {
    if (e.nativeEvent.isComposing || e.keyCode === 229) return
    if (e.key === 'Escape') { e.preventDefault(); onClose() }
  }

  // Flat filtered list when there's a query — performs shallow match on names
  // within already-loaded directories so we don't force-load the whole tree.
  const q = query.trim().toLowerCase()
  const filtered = useMemo(() => {
    if (!q) return null
    const matches: Entry[] = []
    for (const ds of Object.values(dirs)) {
      for (const e of ds.entries) {
        if (e.name.toLowerCase().includes(q) || e.path.toLowerCase().includes(q)) {
          matches.push(e)
        }
      }
    }
    // Dedupe by path
    const seen = new Set<string>()
    const unique = matches.filter((e) => {
      if (seen.has(e.path)) return false
      seen.add(e.path)
      return true
    })
    // Files first, then dirs; then alphabetical by path
    unique.sort((a, b) => {
      if (a.isDir !== b.isDir) return a.isDir ? 1 : -1
      return a.path.localeCompare(b.path)
    })
    return unique
  }, [q, dirs])

  const SEARCH_LIMIT = 80
  const visibleFiltered = filtered ? filtered.slice(0, SEARCH_LIMIT) : null
  const filteredOverflow = filtered ? Math.max(0, filtered.length - SEARCH_LIMIT) : 0

  const renderNode = (entry: Entry, depth: number) => {
    const state = dirs[entry.path]
    const expanded = !!state?.expanded
    return (
      <div key={entry.path}>
        <div
          className={`fp-row ${entry.isDir ? 'dir' : 'file'}`}
          style={{ paddingLeft: 10 + depth * 16 }}
          onClick={() => (entry.isDir ? toggle(entry.path) : onPick(entry.path))}
          title={entry.path}
        >
          {entry.isDir ? (
            <>
              <ChevronRight size={10} className={`fp-chev ${expanded ? 'expanded' : ''}`} />
              {expanded ? <FolderOpen size={14} className="fp-icon dir" /> : <Folder size={14} className="fp-icon dir" />}
            </>
          ) : (
            <>
              <span className="fp-chev-placeholder" />
              <File size={14} className="fp-icon" />
            </>
          )}
          <span className="fp-name">{entry.name}</span>
        </div>
        {entry.isDir && expanded && state?.loaded && (
          state.error ? (
            <div
              className="fp-row file"
              style={{ paddingLeft: 10 + (depth + 1) * 16, fontSize: 12, color: 'var(--error)', opacity: 0.8 }}
            >
              <span className="fp-chev-placeholder" />
              <span className="fp-name">Failed to load: {state.error}</span>
            </div>
          ) : (
            state.entries.map((c) => renderNode(c, depth + 1))
          )
        )}
      </div>
    )
  }

  const root = dirs['']

  return (
    <div className="fp-backdrop" onMouseDown={onClose}>
      <div
        className="fp-modal"
        onMouseDown={(e) => e.stopPropagation()}
        onKeyDown={onKey}
        role="dialog"
        aria-modal="true"
        aria-label="Browse project files"
      >
        <div className="fp-input-wrap">
          <Search size={14} className="fp-search-icon" />
          <input
            ref={inputRef}
            className="fp-input"
            value={query}
            placeholder="Filter by name or path…"
            onChange={(e) => setQuery(e.target.value)}
            maxLength={500}
          />
          <button className="icon-btn" onClick={onClose} title="Close" aria-label="Close file picker"><X size={12} /></button>
        </div>
        <div className="fp-body">
          {visibleFiltered ? (
            visibleFiltered.length === 0 ? (
              <div className="fp-empty">No matches. Try expanding folders first.</div>
            ) : (
              <>
                {visibleFiltered.map((e) => (
                  <div
                    key={e.path}
                    className={`fp-row ${e.isDir ? 'dir' : 'file'}`}
                    onClick={() => (e.isDir ? toggle(e.path) : onPick(e.path))}
                    title={e.path}
                  >
                    <span className="fp-chev-placeholder" />
                    {e.isDir ? <Folder size={14} className="fp-icon dir" /> : <File size={14} className="fp-icon" />}
                    <span className="fp-name">{e.name}</span>
                    <span className="fp-path">{e.path}</span>
                  </div>
                ))}
                {filteredOverflow > 0 && (
                  <div className="fp-overflow">+{filteredOverflow} more — refine your query to narrow results</div>
                )}
              </>
            )
          ) : root?.loaded ? (
            root.error ? (
              <div className="fp-empty" role="alert">
                <span>Failed to read project directory: {root.error}</span>
                <button type="button" className="btn-secondary" onClick={() => { void loadDirectory('') }}>
                  <RotateCcw size={12} /> Retry
                </button>
              </div>
            ) : root.entries.length === 0 ? (
              <div className="fp-empty">Project directory is empty.</div>
            ) : (
              root.entries.map((e) => renderNode(e, 0))
            )
          ) : (
            <div className="fp-empty" role="status">Loading…</div>
          )}
        </div>
        <div className="fp-footer">
          <span>Click a file to insert <span className="mono">@path</span> in the chat input</span>
          <kbd>Esc</kbd>
        </div>
      </div>
    </div>
  )
}
