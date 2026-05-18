import { useState, useEffect, useCallback } from 'react'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'
import { ListDirectory, ReadFileContent, SendMessage } from '../../../wailsjs/go/studio/Studio'
import { Folder, FolderOpen, File, ChevronRight, FolderX, Loader, RotateCcw } from 'lucide-react'

interface FileEntry {
  name: string
  path: string
  isDir: boolean
  size: number
}

export function FileBrowser() {
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const activeProject = useProjectStore((s) => s.projects.find((p) => p.id === s.activeProjectId))
  const [refreshKey, setRefreshKey] = useState(0)
  const refresh = useCallback(() => setRefreshKey((k) => k + 1), [])

  if (!activeProjectId || !activeProject) {
    return null
  }

  if (activeProject.directoryOK === false) {
    return (
      <div className="file-browser">
        <div className="file-browser-header">
          <FolderX size={14} style={{ color: 'var(--error)' }} />
          <span>{activeProject.name}</span>
        </div>
        <div className="file-browser-missing">
          <FolderX size={28} style={{ opacity: 0.3 }} />
          <p>Directory missing</p>
          <p className="mono" style={{ fontSize: 11, opacity: 0.6 }}>{activeProject.directory}</p>
        </div>
      </div>
    )
  }

  return (
    <div className="file-browser">
      <div className="file-browser-header">
        <Folder size={14} />
        <span>{activeProject.name}</span>
        <button className="icon-btn file-browser-refresh" onClick={refresh} title="Refresh directory tree">
          <RotateCcw size={13} />
        </button>
      </div>
      <div className="file-browser-tree">
        <DirectoryNode key={`${activeProjectId}-${refreshKey}`} projectId={activeProjectId} path="" depth={0} />
      </div>
    </div>
  )
}

function DirectoryNode({ projectId, path, depth }: { projectId: string; path: string; depth: number }) {
  const [entries, setEntries] = useState<FileEntry[]>([])
  const [expanded, setExpanded] = useState(depth === 0)
  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState(false)

  useEffect(() => {
    if (expanded && !loaded) {
      ListDirectory(projectId, path).then((result) => {
        if (result) setEntries(result as FileEntry[])
        setLoaded(true)
      }).catch(() => { setLoaded(true); setLoadError(true) })
    }
  }, [expanded, loaded, projectId, path])

  // Root level auto-expands
  if (depth === 0) {
    if (loaded && loadError) {
      return (
        <div className="file-browser-missing">
          <FolderX size={28} style={{ opacity: 0.3 }} />
          <p>Failed to load directory</p>
        </div>
      )
    }
    if (!loaded) {
      return <div className="file-browser-loading"><Loader size={14} className="spin" /></div>
    }
    if (entries.length === 0) {
      return <div className="file-browser-loading" style={{ color: 'var(--text-muted)' }}>Empty directory</div>
    }
    return (
      <div className="file-tree-root">
        {entries.map((e) => (
          <FileNode key={e.path} entry={e} projectId={projectId} depth={depth} />
        ))}
      </div>
    )
  }

  return null
}

function FileNode({ entry, projectId, depth }: { entry: FileEntry; projectId: string; depth: number }) {
  const [expanded, setExpanded] = useState(false)
  const [children, setChildren] = useState<FileEntry[]>([])
  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [sending, setSending] = useState(false)

  const handleToggle = () => {
    if (!entry.isDir) return
    // Retry the load when expanding after a previous failure. On a first expand
    // (!loaded) or after an error (loadError set), kick off a fresh fetch.
    // Collapsing never triggers a load.
    const willExpand = !expanded
    if (willExpand && (!loaded || loadError)) {
      setLoadError(null)
      setChildren([])
      ListDirectory(projectId, entry.path).then((result) => {
        if (result) setChildren(result as FileEntry[])
        setLoaded(true)
      }).catch((err) => {
        setLoaded(true)
        setLoadError(String(err?.message || err || 'Failed to read directory'))
      })
    }
    setExpanded(!expanded)
  }

  const handleFileClick = () => {
    if (entry.isDir || sending) return
    setSending(true)
    const sessionId = useChatStore.getState().activeSession[projectId] || 'default'
    const chatKey = projectId + '_' + sessionId
    ReadFileContent(projectId, entry.path).then((content) => {
      // content is "" for empty files — treat that as valid (not null/undefined).
      if (content !== null && content !== undefined) {
        const body = content.length > 0 ? content : '(empty file)'
        const msg = `Read file: ${entry.path}\n\n\`\`\`\n${body}\n\`\`\``
        useChatStore.getState().addUserMessage(chatKey, `[File: ${entry.path}]`)
        SendMessage(projectId, msg, sessionId).catch((err) => {
          console.error('SendMessage from file browser failed:', err)
          // Surface the failure so the user doesn't think the agent is
          // processing the file when nothing was actually sent.
          useChatStore.getState().finalizeAssistant(chatKey, `Error: failed to send file — ${String(err?.message || err)}`)
        })
        // Switch to chat tab so the user sees the message.
        window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: sessionId }))
      }
    }).catch((err) => {
      console.error('ReadFileContent error:', err)
      // Show the read failure in chat so the user knows their click registered
      // but the file couldn't be loaded (permission denied, gone, too large).
      useChatStore.getState().addUserMessage(chatKey, `[File: ${entry.path}]`)
      useChatStore.getState().finalizeAssistant(chatKey, `Error: failed to read file — ${String(err?.message || err)}`)
      window.dispatchEvent(new CustomEvent('gokin:switch-tab', { detail: sessionId }))
    }).finally(() => setSending(false))
  }

  const formatSize = (bytes: number): string => {
    if (bytes < 1024) return `${bytes}B`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)}K`
    return `${(bytes / 1024 / 1024).toFixed(1)}M`
  }

  return (
    <div className="file-node">
      <div
        className={`file-node-row ${entry.isDir ? 'dir' : 'file'} ${sending ? 'sending' : ''}`}
        style={{ paddingLeft: `${12 + depth * 16}px` }}
        onClick={entry.isDir ? handleToggle : handleFileClick}
        title={entry.isDir ? undefined : 'Click to send file to chat'}
      >
        {entry.isDir ? (
          <>
            <ChevronRight size={10} className={`file-chevron ${expanded ? 'expanded' : ''}`} />
            {expanded ? <FolderOpen size={14} className="file-icon folder" /> : <Folder size={14} className="file-icon folder" />}
          </>
        ) : (
          <>
            <span className="file-chevron-placeholder" />
            <FileTypeIcon name={entry.name} />
          </>
        )}
        <span className="file-name">{entry.name}</span>
        {!entry.isDir && <span className="file-size">{formatSize(entry.size)}</span>}
      </div>
      {expanded && entry.isDir && (
        <>
          {loadError && (
            <div
              className="file-node-row file-node-error"
              style={{ paddingLeft: `${12 + (depth + 1) * 16}px` }}
              title={loadError}
            >
              <span className="file-chevron-placeholder" />
              <span className="file-name">Failed to load: {loadError}</span>
            </div>
          )}
          {children.map((child) => (
            <FileNode key={child.path} entry={child} projectId={projectId} depth={depth + 1} />
          ))}
        </>
      )}
    </div>
  )
}

const extColors: Record<string, string> = {
  ts: '#3178c6', tsx: '#3178c6',
  js: '#f7df1e', jsx: '#f7df1e',
  go: '#00add8',
  css: '#264de4', scss: '#c76494',
  json: '#a0a0ab', yaml: '#a0a0ab', yml: '#a0a0ab', toml: '#a0a0ab',
  md: '#519aba', mdx: '#519aba',
  html: '#e44d26',
  py: '#3776ab',
  rs: '#dea584',
  sh: '#89e051', bash: '#89e051',
  sql: '#e38c00',
  svg: '#ffb13b', png: '#a87cec', jpg: '#a87cec',
  mod: '#00add8', sum: '#00add8',
}

function FileTypeIcon({ name }: { name: string }) {
  const ext = name.includes('.') ? name.split('.').pop()?.toLowerCase() || '' : ''
  const color = extColors[ext]

  if (color) {
    return <span className="file-ext-badge" style={{ color }}>{ext}</span>
  }
  return <File size={14} className="file-icon" />
}
