import { useEffect, useMemo, useRef, useState } from 'react'
import { ChevronRight, File, Folder, FolderOpen, Search, X } from 'lucide-react'
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

  useEffect(() => {
    inputRef.current?.focus()
    // Load root directory immediately
    ListSessionDirectory(projectId, sessionId, '').then((entries) => {
      setDirs((d) => ({ ...d, '': { loaded: true, entries: (entries || []) as Entry[], expanded: true } }))
    }).catch((err) => {
      setDirs((d) => ({ ...d, '': { loaded: true, entries: [], expanded: true, error: String(err?.message || err) } }))
    })
  }, [projectId, sessionId])

  const toggle = (path: string) => {
    setDirs((d) => {
      const cur = d[path] || { loaded: false, entries: [], expanded: false }
      const willExpand = !cur.expanded
      const next = { ...cur, expanded: willExpand }
      if (willExpand && (!cur.loaded || cur.error)) {
        // Use the current state (dd) at resolve-time, not the captured `next`,
        // so rapid double-clicks can't overwrite an intermediate expand/collapse.
        // Also retry when a previous attempt left an error (cur.error set).
        ListSessionDirectory(projectId, sessionId, path).then((entries) => {
          setDirs((dd) => ({ ...dd, [path]: { ...dd[path], loaded: true, entries: (entries || []) as Entry[], error: undefined } }))
        }).catch((err) => {
          setDirs((dd) => ({ ...dd, [path]: { ...dd[path], loaded: true, entries: [], error: String(err?.message || err) } }))
        })
        // Reset to loading state so the subtree shows nothing while the fetch is in flight.
        return { ...d, [path]: { ...next, loaded: false, error: undefined } }
      }
      return { ...d, [path]: next }
    })
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
              <div className="fp-empty">Failed to read project directory: {root.error}</div>
            ) : root.entries.length === 0 ? (
              <div className="fp-empty">Project directory is empty.</div>
            ) : (
              root.entries.map((e) => renderNode(e, 0))
            )
          ) : (
            <div className="fp-empty">Loading…</div>
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
