import { useState, useEffect, useCallback } from 'react'
import type { CSSProperties } from 'react'
import { useProjectStore } from '../../stores/projectStore'
import { ListSessionDirectory } from '../../../wailsjs/go/studio/Studio'
import { Folder, FolderOpen, File, ChevronRight, FolderX, Loader, RotateCcw, PanelsTopLeft, MessageSquare } from 'lucide-react'
import { ArtifactPreview } from './ArtifactPreview'
import { FileEditor, discardCachedSessionFileDraft } from './FileEditor'
import { requestFileContextMenu } from './FileContextMenu'
import { composeInChat, formatFileMention } from '../../lib/composeInChat'
import { ProjectFolderRecovery } from '../project/ProjectFolderRecovery'
import { SplitViewResizer } from '../common/SplitViewResizer'
import { useConfirmDialog } from '../common/AppDialog'
import { usePersistentPanelWidth } from '../../hooks/usePersistentPanelWidth'
import { isPreviewableFilePath, isStaticPreviewFilePath } from '../../lib/previewFiles'

const FILE_LIST_DEFAULT_WIDTH = 280
const FILE_LIST_MIN_WIDTH = 200
const FILE_LIST_MAX_WIDTH = 520

interface FileEntry {
  name: string
  path: string
  isDir: boolean
  size: number
}

export function isArtifactPath(path: string) {
  return isPreviewableFilePath(path)
}

export function composeFileInChat(path: string) {
  // Native documents are read by the bounded document reader at tool time;
  // text/web files use the existing @-mention expansion flow. Both remain a
  // reviewable composer draft and are never sent from the file browser.
  if (/\.(?:docx|xlsx|pptx|pdf)$/i.test(path)) {
    composeInChat(`Read and analyze the native document at ${JSON.stringify(path)} using the read tool. `)
    return
  }
  composeInChat(`${formatFileMention(path)} `)
}

export function FileBrowser({
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
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const activeProject = useProjectStore((s) => s.projects.find((p) => p.id === s.activeProjectId))
  const [refreshKey, setRefreshKey] = useState(0)
  const [localArtifactPath, setLocalArtifactPath] = useState<string | null>(null)
  const [selectedFilePath, setSelectedFilePath] = useState<string | null>(null)
  const [fileDirty, setFileDirty] = useState(false)
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const refresh = useCallback(() => setRefreshKey((k) => k + 1), [])
  const selectedArtifact = artifactPath === undefined ? localArtifactPath : artifactPath
  const setSelectedArtifact = onArtifactPathChange || setLocalArtifactPath
  const { width: fileListWidth, updateWidth: setFileListWidth } = usePersistentPanelWidth(
    'gokin:file-preview-list-width',
    FILE_LIST_DEFAULT_WIDTH,
    FILE_LIST_MIN_WIDTH,
    FILE_LIST_MAX_WIDTH,
  )

  const confirmFileNavigation = useCallback(async () => {
    if (!fileDirty) return true
    return requestConfirmation({
      title: 'Discard unsaved file edits?',
      message: `The edits in ${selectedFilePath || 'the open file'} have not been saved. Discard them and open another file?`,
      confirmLabel: 'Discard edits',
      cancelLabel: 'Keep editing',
      danger: true,
    })
  }, [fileDirty, requestConfirmation, selectedFilePath])

  const openTextFile = useCallback(async (path: string | null) => {
    if (path === selectedFilePath) return
    if (!(await confirmFileNavigation())) return
    if (fileDirty && selectedFilePath && activeProjectId) discardCachedSessionFileDraft(activeProjectId, sessionID, selectedFilePath)
    setSelectedArtifact(null)
    setSelectedFilePath(path)
    setFileDirty(false)
  }, [activeProjectId, confirmFileNavigation, fileDirty, selectedFilePath, sessionID, setSelectedArtifact])

  const openOfficeArtifact = useCallback(async (path: string) => {
    if (selectedFilePath && !(await confirmFileNavigation())) return
    if (fileDirty && selectedFilePath && activeProjectId) discardCachedSessionFileDraft(activeProjectId, sessionID, selectedFilePath)
    setSelectedFilePath(null)
    setFileDirty(false)
    setSelectedArtifact(path)
  }, [activeProjectId, confirmFileNavigation, fileDirty, selectedFilePath, sessionID, setSelectedArtifact])

  useEffect(() => {
    const handler = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      if (detail.projectID && detail.projectID !== activeProjectId) return
      if (detail.sessionID && detail.sessionID !== sessionID) return
      if (typeof detail.path !== 'string' || !detail.path.trim()) return
      void openTextFile(detail.path)
    }
    window.addEventListener('gokin:file-browser-open', handler)
    return () => window.removeEventListener('gokin:file-browser-open', handler)
  }, [activeProjectId, openTextFile, sessionID])

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
        <ProjectFolderRecovery project={activeProject} className="file-browser-missing" />
      </div>
    )
  }

  return (
    <div
      className={`file-browser ${selectedArtifact || selectedFilePath ? 'has-artifact' : ''}`}
      style={{ '--preview-list-width': `${fileListWidth}px` } as CSSProperties}
    >
      <div className="file-browser-pane">
        <div className="file-browser-header">
          <Folder size={14} />
          <span>{activeProject.name}</span>
          <button className="icon-btn file-browser-refresh" onClick={refresh} title="Refresh directory tree" aria-label="Refresh directory tree">
            <RotateCcw size={13} />
          </button>
        </div>
        <div className="file-browser-guide">
          <span><PanelsTopLeft size={10} /> HTML, PDF, image, and video files open in Preview</span>
          <span><MessageSquare size={10} /> Text files open here and can be added to chat</span>
        </div>
        <div className="file-browser-tree">
          <DirectoryNode
            key={`${activeProjectId}-${sessionID}-${refreshKey}`}
            projectId={activeProjectId}
            sessionId={sessionID}
            path=""
            depth={0}
            selectedArtifact={selectedArtifact}
            selectedFile={selectedFilePath}
            onOpenArtifact={setSelectedArtifact}
            onOpenOfficeArtifact={openOfficeArtifact}
            onOpenFile={openTextFile}
          />
        </div>
      </div>
      {selectedArtifact && (
        <>
          <SplitViewResizer
            value={fileListWidth}
            min={FILE_LIST_MIN_WIDTH}
            max={FILE_LIST_MAX_WIDTH}
            minSecondary={280}
            defaultValue={FILE_LIST_DEFAULT_WIDTH}
            label="Resize file list and preview"
            onChange={setFileListWidth}
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
      {!selectedArtifact && selectedFilePath && (
        <>
          <SplitViewResizer
            value={fileListWidth}
            min={FILE_LIST_MIN_WIDTH}
            max={FILE_LIST_MAX_WIDTH}
            minSecondary={320}
            defaultValue={FILE_LIST_DEFAULT_WIDTH}
            label="Resize file list and editor"
            onChange={setFileListWidth}
          />
          <FileEditor
            projectID={activeProjectId}
            sessionID={sessionID}
            path={selectedFilePath}
            active={isActive}
            onDirtyChange={setFileDirty}
            onRequestClose={() => { void openTextFile(null) }}
          />
        </>
      )}
      {confirmationDialog}
    </div>
  )
}

function DirectoryNode({
  projectId,
  sessionId,
  path,
  depth,
  selectedArtifact,
  selectedFile,
  onOpenArtifact,
  onOpenOfficeArtifact,
  onOpenFile,
}: {
  projectId: string
  sessionId: string
  path: string
  depth: number
  selectedArtifact: string | null
  selectedFile: string | null
  onOpenArtifact: (path: string | null) => void
  onOpenOfficeArtifact: (path: string) => void | Promise<void>
  onOpenFile: (path: string | null) => void | Promise<void>
}) {
  const [entries, setEntries] = useState<FileEntry[]>([])
  const [expanded, setExpanded] = useState(depth === 0)
  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [focusedPath, setFocusedPath] = useState('')

  useEffect(() => {
    if (expanded && !loaded) {
      ListSessionDirectory(projectId, sessionId, path).then((result) => {
        if (result) setEntries(result as FileEntry[])
        setLoaded(true)
      }).catch(() => { setLoaded(true); setLoadError(true) })
    }
  }, [expanded, loaded, projectId, sessionId, path])

  useEffect(() => {
    if (!focusedPath && entries.length > 0) setFocusedPath(entries[0].path)
  }, [entries, focusedPath])

  // Root level auto-expands
  if (depth === 0) {
    if (loaded && loadError) {
      return (
        <div className="file-browser-missing" role="alert">
          <FolderX size={28} style={{ opacity: 0.3 }} />
          <p>Failed to load directory</p>
        </div>
      )
    }
    if (!loaded) {
      return <div className="file-browser-loading" role="status" aria-label="Loading project files"><Loader size={14} className="spin" /></div>
    }
    if (entries.length === 0) {
      return <div className="file-browser-loading" role="status" style={{ color: 'var(--text-muted)' }}>Empty directory</div>
    }
    return (
      <div
        className="file-tree-root"
        role="tree"
        aria-label="Project files"
        onKeyDown={(event) => {
          if (event.nativeEvent.isComposing || event.keyCode === 229) return
          const row = (event.target as HTMLElement).closest<HTMLButtonElement>('button.file-node-row')
          if (!row || !event.currentTarget.contains(row)) return
          const visibleRows = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>('button.file-node-row'))
          const current = visibleRows.indexOf(row)
          if (current < 0) return

          let next: HTMLButtonElement | undefined
          if (event.key === 'ArrowDown') next = visibleRows[(current + 1) % visibleRows.length]
          else if (event.key === 'ArrowUp') next = visibleRows[(current - 1 + visibleRows.length) % visibleRows.length]
          else if (event.key === 'Home') next = visibleRows[0]
          else if (event.key === 'End') next = visibleRows[visibleRows.length - 1]
          else if (event.key === 'ArrowRight' && row.dataset.isDir === 'true') {
            if (row.dataset.expanded !== 'true') row.click()
            else {
              const node = row.parentElement
              next = node?.querySelector<HTMLButtonElement>(':scope > .file-node-children > .file-node > button.file-node-row') || undefined
            }
          } else if (event.key === 'ArrowLeft') {
            if (row.dataset.isDir === 'true' && row.dataset.expanded === 'true') row.click()
            else {
              const parentNode = row.parentElement?.parentElement?.closest<HTMLDivElement>('.file-node')
              next = parentNode?.querySelector<HTMLButtonElement>(':scope > button.file-node-row') || undefined
            }
          } else if (!event.ctrlKey && !event.metaKey && !event.altKey && event.key.length === 1 && event.key.trim()) {
            const query = event.key.toLocaleLowerCase()
            for (let offset = 1; offset <= visibleRows.length; offset += 1) {
              const candidate = visibleRows[(current + offset) % visibleRows.length]
              if ((candidate.dataset.fileName || '').toLocaleLowerCase().startsWith(query)) {
                next = candidate
                break
              }
            }
          } else return

          event.preventDefault()
          next?.focus()
        }}
      >
        {entries.map((e) => (
          <FileNode
            key={e.path}
            entry={e}
            projectId={projectId}
            sessionId={sessionId}
            depth={depth}
            selectedArtifact={selectedArtifact}
            selectedFile={selectedFile}
            onOpenArtifact={onOpenArtifact}
            onOpenOfficeArtifact={onOpenOfficeArtifact}
            onOpenFile={onOpenFile}
            focusedPath={focusedPath}
            onFocusPath={setFocusedPath}
          />
        ))}
      </div>
    )
  }

  return null
}

function FileNode({
  entry,
  projectId,
  sessionId,
  depth,
  selectedArtifact,
  selectedFile,
  onOpenArtifact,
  onOpenOfficeArtifact,
  onOpenFile,
  focusedPath,
  onFocusPath,
}: {
  entry: FileEntry
  projectId: string
  sessionId: string
  depth: number
  selectedArtifact: string | null
  selectedFile: string | null
  onOpenArtifact: (path: string | null) => void
  onOpenOfficeArtifact: (path: string) => void | Promise<void>
  onOpenFile: (path: string | null) => void | Promise<void>
  focusedPath: string
  onFocusPath: (path: string) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const [children, setChildren] = useState<FileEntry[]>([])
  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)

  const handleToggle = () => {
    if (!entry.isDir) return
    // Retry the load when expanding after a previous failure. On a first expand
    // (!loaded) or after an error (loadError set), kick off a fresh fetch.
    // Collapsing never triggers a load.
    const willExpand = !expanded
    if (willExpand && (!loaded || loadError)) {
      setLoaded(false)
      setLoadError(null)
      setChildren([])
      ListSessionDirectory(projectId, sessionId, entry.path).then((result) => {
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
    if (entry.isDir) return
    if (isArtifactPath(entry.path)) {
      if (isStaticPreviewFilePath(entry.path)) {
        window.dispatchEvent(new CustomEvent('gokin:open-artifact', { detail: { path: entry.path, sessionID: sessionId } }))
        return
      }
      void onOpenOfficeArtifact(entry.path)
      return
    }
    void onOpenFile(entry.path)
  }

  const formatSize = (bytes: number): string => {
    if (bytes < 1024) return `${bytes}B`
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)}K`
    return `${(bytes / 1024 / 1024).toFixed(1)}M`
  }

  return (
    <div className="file-node" role="none">
      <button
        type="button"
        className={`file-node-row ${entry.isDir ? 'dir' : 'file'} ${selectedArtifact === entry.path || selectedFile === entry.path ? 'selected' : ''}`}
        style={{ paddingLeft: `${12 + depth * 16}px` }}
        onClick={entry.isDir ? handleToggle : handleFileClick}
        onContextMenu={(event) => {
          event.preventDefault()
          requestFileContextMenu(entry.path, sessionId, event.clientX, event.clientY, event.currentTarget)
        }}
        onKeyDown={(event) => {
          if (event.key !== 'ContextMenu' && !(event.shiftKey && event.key === 'F10')) return
          event.preventDefault()
          event.stopPropagation()
          const rect = event.currentTarget.getBoundingClientRect()
          requestFileContextMenu(entry.path, sessionId, rect.left + 18, rect.bottom, event.currentTarget)
        }}
        onFocus={() => onFocusPath(entry.path)}
        role="treeitem"
        aria-level={depth + 1}
        aria-expanded={entry.isDir ? expanded : undefined}
        aria-busy={entry.isDir && expanded && !loaded ? true : undefined}
        aria-selected={selectedArtifact === entry.path || selectedFile === entry.path}
        aria-label={`${entry.name}, ${entry.isDir ? 'folder' : 'file'}`}
        tabIndex={focusedPath === entry.path ? 0 : -1}
        data-file-name={entry.name}
        data-is-dir={entry.isDir ? 'true' : 'false'}
        data-expanded={entry.isDir ? (expanded ? 'true' : 'false') : undefined}
        title={entry.isDir
          ? `${expanded ? 'Collapse' : 'Expand'} ${entry.name}`
          : (isArtifactPath(entry.path) ? 'Open in Preview' : 'Open in the session file editor')}
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
            {isArtifactPath(entry.path) && <PanelsTopLeft size={10} className="artifact-file-mark" />}
          </>
        )}
        <span className="file-name">{entry.name}</span>
        {entry.isDir && expanded && !loaded && <Loader size={10} className="spin file-node-busy" aria-hidden />}
        {!entry.isDir && <span className="file-size">{formatSize(entry.size)}</span>}
      </button>
      {expanded && entry.isDir && (
        <div className="file-node-children" role="group">
          {loadError && (
            <div
              className="file-node-row file-node-error"
              style={{ paddingLeft: `${12 + (depth + 1) * 16}px` }}
              title={loadError}
              role="alert"
            >
              <span className="file-chevron-placeholder" />
              <span className="file-name">Failed to load: {loadError}</span>
            </div>
          )}
          {children.map((child) => (
            <FileNode
              key={child.path}
              entry={child}
              projectId={projectId}
              sessionId={sessionId}
              depth={depth + 1}
              selectedArtifact={selectedArtifact}
              selectedFile={selectedFile}
              onOpenArtifact={onOpenArtifact}
              onOpenOfficeArtifact={onOpenOfficeArtifact}
              onOpenFile={onOpenFile}
              focusedPath={focusedPath}
              onFocusPath={onFocusPath}
            />
          ))}
        </div>
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
  docx: '#2b579a', xlsx: '#217346', pptx: '#d24726', pdf: '#d32f2f',
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
