import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type DragEvent as ReactDragEvent, type PointerEvent as ReactPointerEvent } from 'react'
import {
  CalendarClock, ChevronDown, ChevronLeft, ChevronRight, ChevronUp, FileBox, FileDiff, FolderTree, GripVertical,
  LayoutDashboard, ListChecks, MessageSquare, Monitor, PanelsTopLeft, RotateCcw, TerminalSquare, X,
} from 'lucide-react'
import { ChatPanel } from '../chat/ChatPanel'
import { FileBrowser } from '../files/FileBrowser'
import { ArtifactLibrary } from '../files/ArtifactLibrary'
import { LivePreviewPane } from '../preview/LivePreviewPane'
import { TerminalPanel } from '../terminal/Terminal'
import { ContextPanel } from './ContextPanel'
import { GitReviewModal } from './GitReviewModal'
import { PlanPane } from './PlanPane'
import { ScheduledTasksModal } from '../chat/ScheduledTasksModal'
import { useSettingsStore } from '../../stores/settingsStore'
import { hasOpenModal } from '../../hooks/useModalFocusManagement'
import { useChoiceDialog, useConfirmDialog } from '../common/AppDialog'
import { BrowserOpenURL } from '../../../wailsjs/runtime/runtime'
import { normalizeExternalHTTPLink } from '../../lib/externalLinks'
import {
  WORKSPACE_SPLIT_MAX_RATIO,
  WORKSPACE_SPLIT_MIN_RATIO,
  defaultWorkspaceLayout,
  moveWorkspacePane,
  readWorkspaceLayout,
  resizeWorkspaceSplit,
  setWorkspacePaneOpen,
  writeWorkspaceLayout,
  type WorkspaceDropEdge,
  type WorkspaceLayout,
  type WorkspaceLayoutNode,
  type WorkspacePaneID,
  type WorkspaceSplitAxis,
  type WorkspaceSplitNode,
} from '../../lib/workspaceLayout'
import type { PendingQuickEntryComposerAction } from '../../lib/quickEntry'

const PANE_META: Record<WorkspacePaneID, { label: string; icon: typeof MessageSquare }> = {
  chat: { label: 'Chat', icon: MessageSquare },
  diff: { label: 'Diff', icon: FileDiff },
  preview: { label: 'Browser', icon: Monitor },
  terminal: { label: 'Terminal', icon: TerminalSquare },
  files: { label: 'Files', icon: FolderTree },
  artifacts: { label: 'Artifacts', icon: FileBox },
  plan: { label: 'Plan', icon: ListChecks },
  tasks: { label: 'Tasks', icon: CalendarClock },
  context: { label: 'Context', icon: PanelsTopLeft },
}

interface SessionWorkspaceProps {
  projectId: string
  sessionId: string
  sessionName?: string
  sessionPermissionMode?: string
  sessionWorktreeIsolated?: boolean
  sessionWorktreePath?: string
  sessionWorktreeBranch?: string
  sessionWorktreeError?: string
  quickEntryAction?: PendingQuickEntryComposerAction | null
  onQuickEntryActionHandled?: (id: string) => void
}

interface DropTarget {
  pane: WorkspacePaneID
  edge: WorkspaceDropEdge
}

interface ResizeState {
  path: number[]
  axis: WorkspaceSplitAxis
  startPosition: number
  startRatio: number
  containerSize: number
}

interface WorkspaceRect {
  x: number
  y: number
  width: number
  height: number
}

interface PanePlacement {
  pane: WorkspacePaneID
  rect: WorkspaceRect
}

interface SplitPlacement {
  path: number[]
  split: WorkspaceSplitNode
  rect: WorkspaceRect
}

function workspacePlacements(root: WorkspaceLayoutNode): { panes: PanePlacement[]; splits: SplitPlacement[] } {
  const panes: PanePlacement[] = []
  const splits: SplitPlacement[] = []
  const visit = (node: WorkspaceLayoutNode, rect: WorkspaceRect, path: number[]) => {
    if (node.kind === 'pane') {
      panes.push({ pane: node.pane, rect })
      return
    }
    splits.push({ path, split: node, rect })
    if (node.axis === 'horizontal') {
      const firstWidth = rect.width * node.ratio
      visit(node.first, { ...rect, width: firstWidth }, [...path, 0])
      visit(node.second, { x: rect.x + firstWidth, y: rect.y, width: rect.width - firstWidth, height: rect.height }, [...path, 1])
    } else {
      const firstHeight = rect.height * node.ratio
      visit(node.first, { ...rect, height: firstHeight }, [...path, 0])
      visit(node.second, { x: rect.x, y: rect.y + firstHeight, width: rect.width, height: rect.height - firstHeight }, [...path, 1])
    }
  }
  visit(root, { x: 0, y: 0, width: 1, height: 1 }, [])
  return { panes, splits }
}

function percent(value: number): string {
  return `${Math.round(value * 100000) / 1000}%`
}

function dropEdge(event: ReactDragEvent<HTMLElement>): WorkspaceDropEdge {
  const rect = event.currentTarget.getBoundingClientRect()
  const x = Math.max(0, Math.min(1, (event.clientX - rect.left) / Math.max(1, rect.width))) - 0.5
  const y = Math.max(0, Math.min(1, (event.clientY - rect.top) / Math.max(1, rect.height))) - 0.5
  if (Math.abs(x) >= Math.abs(y)) return x < 0 ? 'left' : 'right'
  return y < 0 ? 'top' : 'bottom'
}

export function SessionWorkspace(props: SessionWorkspaceProps) {
  const { projectId, sessionId } = props
  const defaultProvider = useSettingsStore((state) => state.settings.defaultProvider)
  const defaultModel = useSettingsStore((state) => state.settings.defaultModel)
  const [layout, setLayout] = useState<WorkspaceLayout>(() => readWorkspaceLayout(projectId, sessionId))
  const layoutRef = useRef(layout)
  layoutRef.current = layout
  const workspaceRef = useRef<HTMLElement>(null)
  const layoutTreeRef = useRef<HTMLDivElement>(null)
  const focusedPaneRef = useRef<WorkspacePaneID>('chat')
  const [viewsOpen, setViewsOpen] = useState(false)
  const [dragging, setDragging] = useState<WorkspacePaneID | null>(null)
  const [dropTarget, setDropTarget] = useState<DropTarget | null>(null)
  const [resizing, setResizing] = useState<WorkspaceSplitAxis | null>(null)
  const [previewFilePath, setPreviewFilePath] = useState<string | null>(null)
  const [previewElementSelectRequest, setPreviewElementSelectRequest] = useState(0)
  const [browserNavigationRequest, setBrowserNavigationRequest] = useState<{ url: string; key: number } | null>(null)
  const [terminalRequest, setTerminalRequest] = useState<{ path: string; key: number }>({ path: '', key: 0 })
  const [filesDirty, setFilesDirty] = useState(false)
  const [dirtyFilePath, setDirtyFilePath] = useState<string | null>(null)
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const [requestChoice, choiceDialog] = useChoiceDialog()
  const resizeRef = useRef<ResizeState | null>(null)
  const viewsRef = useRef<HTMLDivElement>(null)
  const placements = useMemo(() => workspacePlacements(layout.root), [layout.root])
  const visible = useMemo(() => placements.panes.map(({ pane }) => pane), [placements.panes])

  const commit = useCallback((next: WorkspaceLayout) => {
    layoutRef.current = next
    setLayout(next)
    writeWorkspaceLayout(projectId, sessionId, next)
  }, [projectId, sessionId])

  const focusPane = useCallback((id: WorkspacePaneID) => {
    focusedPaneRef.current = id
    requestAnimationFrame(() => workspaceRef.current?.querySelector<HTMLElement>(`[data-workspace-pane="${id}"]`)?.focus())
  }, [])

  const applyOpen = useCallback((id: WorkspacePaneID, open: boolean) => {
    const current = layoutRef.current
    const target = focusedPaneRef.current !== id ? focusedPaneRef.current : 'chat'
    const next = setWorkspacePaneOpen(current, id, open, target)
    commit(next)
    setViewsOpen(false)
    if (open) {
      focusPane(id)
    } else {
      const oldIndex = current.open.indexOf(id)
      const fallback = next.open[Math.max(0, Math.min(oldIndex, next.open.length - 1))] || 'chat'
      focusPane(fallback)
    }
  }, [commit, focusPane])

  const setOpen = useCallback((id: WorkspacePaneID, open: boolean) => {
    if (id === 'tasks' && !open) {
      window.dispatchEvent(new CustomEvent('gokin:close-scheduled-tasks'))
      return
    }
    if (id === 'files' && !open && filesDirty) {
      void requestConfirmation({
        title: 'Discard unsaved file edits?',
        message: 'The Files pane contains an unsaved edit. Discard it and close the pane?',
        confirmLabel: 'Discard and close',
        cancelLabel: 'Keep editing',
        danger: true,
      }).then((accepted) => {
        if (!accepted) return
        if (dirtyFilePath) window.dispatchEvent(new CustomEvent('gokin:discard-session-file-draft', { detail: { projectID: projectId, sessionID: sessionId, path: dirtyFilePath } }))
        applyOpen(id, false)
      })
      return
    }
    applyOpen(id, open)
  }, [applyOpen, dirtyFilePath, filesDirty, projectId, requestConfirmation, sessionId])

  const resetWorkspace = useCallback(() => {
    const perform = () => {
      commit(defaultWorkspaceLayout())
      setViewsOpen(false)
      focusPane('chat')
    }
    if (!filesDirty || !layoutRef.current.open.includes('files')) {
      perform()
      return
    }
    void requestConfirmation({
      title: 'Discard unsaved file edits?',
      message: 'Resetting the layout closes the Files pane and discards its unsaved edit.',
      confirmLabel: 'Discard and reset',
      cancelLabel: 'Keep layout',
      danger: true,
    }).then((accepted) => {
      if (!accepted) return
      if (dirtyFilePath) window.dispatchEvent(new CustomEvent('gokin:discard-session-file-draft', { detail: { projectID: projectId, sessionID: sessionId, path: dirtyFilePath } }))
      perform()
    })
  }, [commit, dirtyFilePath, filesDirty, focusPane, projectId, requestConfirmation, sessionId])

  const toggle = useCallback((id: WorkspacePaneID) => {
    setOpen(id, !layoutRef.current.open.includes(id))
  }, [setOpen])

  const move = useCallback((source: WorkspacePaneID, target: WorkspacePaneID, edge: WorkspaceDropEdge) => {
    commit(moveWorkspacePane(layoutRef.current, source, target, edge))
    setDragging(null)
    setDropTarget(null)
    focusPane(source)
  }, [commit, focusPane])

  useEffect(() => {
    const closeMenu = (event: PointerEvent) => {
      if (viewsRef.current?.contains(event.target as Node)) return
      setViewsOpen(false)
    }
    window.addEventListener('pointerdown', closeMenu)
    return () => window.removeEventListener('pointerdown', closeMenu)
  }, [])

  useEffect(() => {
    window.addEventListener('gokin:layout-reset', resetWorkspace)
    return () => window.removeEventListener('gokin:layout-reset', resetWorkspace)
  }, [resetWorkspace])

  useEffect(() => {
    const toggleDiff = () => toggle('diff')
    const togglePreview = () => toggle('preview')
    const selectPreviewElement = () => {
      setOpen('preview', true)
      setPreviewElementSelectRequest((value) => value + 1)
    }
    const toggleTerminal = () => toggle('terminal')
    const toggleContext = () => toggle('context')
    const openPane = (event: Event) => {
      const id = (event as CustomEvent).detail as WorkspacePaneID
      if (id && id in PANE_META) setOpen(id, true)
    }
    const routeReview = (event: Event) => {
      event.stopImmediatePropagation()
      setOpen('diff', true)
    }
    const openPreviewFile = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      const path = typeof detail === 'string' ? detail : detail.path
      if (typeof path !== 'string' || !path.trim()) return
      if (detail.projectID && detail.projectID !== projectId) return
      if (detail.sessionID && detail.sessionID !== sessionId) return
      setPreviewFilePath(path)
      setOpen('preview', true)
    }
    const openExternalBrowser = async (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      if (!(event.target instanceof Node) || !workspaceRef.current?.contains(event.target)) return
      if (detail.projectID && detail.projectID !== projectId) return
      if (detail.sessionID && detail.sessionID !== sessionId) return
      if (hasOpenModal()) return
      const link = normalizeExternalHTTPLink(detail.url)
      if (!link) return
      const destination = await requestChoice({
        title: 'Open external link',
        message: link.display,
        choices: [
          { value: 'app', label: 'Open in app', description: 'Use the isolated Browser pane; a new origin is reviewed before navigation.', primary: true },
          { value: 'system', label: 'Default browser', description: 'Leave Gokin Studio and open this link in your system browser.' },
        ],
        cancelLabel: 'Cancel',
      })
      if (destination === 'system') {
        BrowserOpenURL(link.url)
        return
      }
      if (destination !== 'app') return
      setPreviewFilePath(null)
      setBrowserNavigationRequest((current) => ({ url: link.url, key: (current?.key || 0) + 1 }))
      setOpen('preview', true)
    }
    const openWorkspaceFile = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      if (detail.projectID && detail.projectID !== projectId) return
      if (detail.sessionID && detail.sessionID !== sessionId) return
      if (typeof detail.path !== 'string' || !detail.path.trim()) return
      setOpen('files', true)
      requestAnimationFrame(() => requestAnimationFrame(() => {
        window.dispatchEvent(new CustomEvent('gokin:file-browser-open', { detail }))
      }))
    }
    const openSessionTerminal = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      if (detail.projectID && detail.projectID !== projectId) return
      if (detail.sessionID && detail.sessionID !== sessionId) return
      const path = typeof detail.path === 'string' ? detail.path : ''
      setTerminalRequest((current) => ({ path, key: current.key + 1 }))
      setOpen('terminal', true)
    }
    const fileDirtyChanged = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      if (detail.projectID !== projectId || detail.sessionID !== sessionId) return
      setFilesDirty(detail.dirty === true)
      setDirtyFilePath(detail.dirty === true && typeof detail.path === 'string' ? detail.path : null)
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.isComposing || event.keyCode === 229) return
      const modifier = event.metaKey || event.ctrlKey
      if (modifier && !event.shiftKey && !event.altKey && event.key.toLowerCase() === 'j') {
        event.preventDefault()
        if (!hasOpenModal()) toggleContext()
        return
      }
      if (modifier && event.key === '\\') {
        const pane = (event.target as HTMLElement | null)?.closest<HTMLElement>('[data-workspace-pane]')
        const id = pane?.dataset.workspacePane as WorkspacePaneID | undefined
        if (!id || id === 'chat' || hasOpenModal()) return
        event.preventDefault()
        setOpen(id, false)
      }
    }
    window.addEventListener('keydown', onKey, true)
    window.addEventListener('gokin:toggle-diff-pane', toggleDiff)
    window.addEventListener('gokin:toggle-live-preview', togglePreview)
    window.addEventListener('gokin:select-preview-element', selectPreviewElement)
    window.addEventListener('gokin:toggle-workspace-terminal', toggleTerminal)
    window.addEventListener('gokin:toggle-context', toggleContext)
    window.addEventListener('gokin:open-workspace-pane', openPane)
    window.addEventListener('gokin:open-git-review', routeReview)
    window.addEventListener('gokin:open-preview-file', openPreviewFile)
    window.addEventListener('gokin:open-external-browser', openExternalBrowser)
    window.addEventListener('gokin:open-workspace-file', openWorkspaceFile)
    window.addEventListener('gokin:open-session-terminal', openSessionTerminal)
    window.addEventListener('gokin:file-editor-dirty', fileDirtyChanged)
    return () => {
      window.removeEventListener('keydown', onKey, true)
      window.removeEventListener('gokin:toggle-diff-pane', toggleDiff)
      window.removeEventListener('gokin:toggle-live-preview', togglePreview)
      window.removeEventListener('gokin:select-preview-element', selectPreviewElement)
      window.removeEventListener('gokin:toggle-workspace-terminal', toggleTerminal)
      window.removeEventListener('gokin:toggle-context', toggleContext)
      window.removeEventListener('gokin:open-workspace-pane', openPane)
      window.removeEventListener('gokin:open-git-review', routeReview)
      window.removeEventListener('gokin:open-preview-file', openPreviewFile)
      window.removeEventListener('gokin:open-external-browser', openExternalBrowser)
      window.removeEventListener('gokin:open-workspace-file', openWorkspaceFile)
      window.removeEventListener('gokin:open-session-terminal', openSessionTerminal)
      window.removeEventListener('gokin:file-editor-dirty', fileDirtyChanged)
    }
  }, [projectId, sessionId, setOpen, toggle, requestChoice])

  const beginResize = (event: ReactPointerEvent<HTMLDivElement>, path: number[], split: WorkspaceSplitNode, rect: WorkspaceRect) => {
    if (event.button !== 0) return
    const container = layoutTreeRef.current?.getBoundingClientRect()
    if (!container) return
    event.preventDefault()
    resizeRef.current = {
      path,
      axis: split.axis,
      startPosition: split.axis === 'horizontal' ? event.clientX : event.clientY,
      startRatio: split.ratio,
      containerSize: split.axis === 'horizontal' ? container.width * rect.width : container.height * rect.height,
    }
    setResizing(split.axis)
    event.currentTarget.setPointerCapture(event.pointerId)
  }

  const resizeFromPointer = (event: ReactPointerEvent<HTMLDivElement>) => {
    const active = resizeRef.current
    if (!active) return
    const position = active.axis === 'horizontal' ? event.clientX : event.clientY
    const ratio = active.startRatio + (position - active.startPosition) / Math.max(1, active.containerSize)
    const next = resizeWorkspaceSplit(layoutRef.current, active.path, ratio)
    layoutRef.current = next
    setLayout(next)
  }

  const finishResize = (event?: ReactPointerEvent<HTMLDivElement>) => {
    if (!resizeRef.current) return
    resizeRef.current = null
    setResizing(null)
    writeWorkspaceLayout(projectId, sessionId, layoutRef.current)
    if (event?.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
  }

  const renderContent = (id: WorkspacePaneID) => {
    switch (id) {
      case 'chat':
        return <ChatPanel
          sessionId={sessionId}
          sessionName={props.sessionName}
          sessionPermissionMode={props.sessionPermissionMode}
          sessionWorktreeIsolated={props.sessionWorktreeIsolated}
          sessionWorktreePath={props.sessionWorktreePath}
          sessionWorktreeBranch={props.sessionWorktreeBranch}
          sessionWorktreeError={props.sessionWorktreeError}
          quickEntryAction={props.quickEntryAction}
          onQuickEntryActionHandled={props.onQuickEntryActionHandled}
          terminalOpen={layout.open.includes('terminal')}
          onToggleTerminal={() => toggle('terminal')}
        />
      case 'diff':
        return <GitReviewModal projectId={projectId} sessionId={sessionId} onClose={() => setOpen('diff', false)} embedded />
      case 'preview':
        return <LivePreviewPane
          projectId={projectId}
          sessionId={sessionId}
          onClose={() => setOpen('preview', false)}
          workspaceMode
          staticPath={previewFilePath}
          onStaticPathChange={setPreviewFilePath}
          elementSelectRequest={previewElementSelectRequest}
          externalNavigationRequest={browserNavigationRequest}
        />
      case 'terminal':
        return <TerminalPanel
          sessionId={sessionId}
          worktreePath={props.sessionWorktreePath}
          requestedPath={terminalRequest.path}
          requestKey={terminalRequest.key}
          onClose={() => setOpen('terminal', false)}
        />
      case 'files':
        return <FileBrowser sessionID={sessionId} isActive />
      case 'artifacts':
        return <ArtifactLibrary sessionID={sessionId} isActive />
      case 'plan':
        return <PlanPane projectId={projectId} sessionId={sessionId} />
      case 'tasks':
        return <ScheduledTasksModal projectID={projectId} sessionID={sessionId} provider={defaultProvider} model={defaultModel} onClose={() => applyOpen('tasks', false)} embedded />
      case 'context':
        return <ContextPanel projectId={projectId} sessionId={sessionId} workspaceMode />
    }
  }

  const renderPane = (id: WorkspacePaneID, rect: WorkspaceRect) => {
    const Icon = PANE_META[id].icon
    const index = visible.indexOf(id)
    const previous = visible[index - 1]
    const next = visible[index + 1]
    const verticalTarget = previous || next
    const target = dropTarget?.pane === id ? dropTarget : null
    const placementStyle = { left: percent(rect.x), top: percent(rect.y), width: percent(rect.width), height: percent(rect.height) }
    return <div key={id} className="workspace-pane-slot" style={placementStyle}>
      <section
        className={`workspace-pane-frame pane-${id} ${target ? `drop-target drop-${target.edge}` : ''} ${dragging === id ? 'is-dragging' : ''}`}
        data-workspace-pane={id}
        tabIndex={-1}
        aria-label={`${PANE_META[id].label} pane`}
        onFocusCapture={() => { focusedPaneRef.current = id }}
        onDragOver={(event) => {
          if (!dragging || dragging === id) return
          event.preventDefault()
          const edge = dropEdge(event)
          if (dropTarget?.pane !== id || dropTarget.edge !== edge) setDropTarget({ pane: id, edge })
        }}
        onDrop={(event) => {
          event.preventDefault()
          if (dragging && target) move(dragging, id, target.edge)
        }}
      >
        <header className="workspace-pane-frame-header">
          <button
            type="button"
            className="workspace-pane-drag"
            draggable
            onDragStart={(event) => {
              setDragging(id)
              event.dataTransfer.effectAllowed = 'move'
              event.dataTransfer.setData('text/plain', id)
            }}
            onDragEnd={() => { setDragging(null); setDropTarget(null) }}
            title={`Drag ${PANE_META[id].label} to any edge of another pane`}
            aria-label={`Drag ${PANE_META[id].label} pane to dock it left, right, above, or below another pane`}
          ><GripVertical size={12} /><Icon size={12} /><span>{PANE_META[id].label}</span></button>
          <div className="workspace-pane-actions">
            <button type="button" disabled={!previous} onClick={() => previous && move(id, previous, 'left')} title="Dock left of previous pane" aria-label={`Dock ${PANE_META[id].label} left`}><ChevronLeft size={11} /></button>
            <button type="button" disabled={!verticalTarget} onClick={() => verticalTarget && move(id, verticalTarget, 'top')} title="Dock above adjacent pane" aria-label={`Dock ${PANE_META[id].label} above`}><ChevronUp size={11} /></button>
            <button type="button" disabled={!verticalTarget} onClick={() => verticalTarget && move(id, verticalTarget, 'bottom')} title="Dock below adjacent pane" aria-label={`Dock ${PANE_META[id].label} below`}><ChevronDown size={11} /></button>
            <button type="button" disabled={!next} onClick={() => next && move(id, next, 'right')} title="Dock right of next pane" aria-label={`Dock ${PANE_META[id].label} right`}><ChevronRight size={11} /></button>
            {id !== 'chat' && <button type="button" onClick={() => setOpen(id, false)} title="Close pane (Cmd/Ctrl+\\)" aria-label={`Close ${PANE_META[id].label} pane`}><X size={11} /></button>}
          </div>
        </header>
        <div className="workspace-pane-frame-content">{renderContent(id)}</div>
      </section>
    </div>
  }

  const renderSplit = ({ path, split, rect }: SplitPlacement) => {
    const horizontal = split.axis === 'horizontal'
    const boundary = horizontal ? rect.x + rect.width * split.ratio : rect.y + rect.height * split.ratio
    const style = horizontal
      ? { left: percent(boundary), top: percent(rect.y), height: percent(rect.height) }
      : { left: percent(rect.x), top: percent(boundary), width: percent(rect.width) }
    return <div
      key={path.length ? path.join('-') : 'root'}
      className={`workspace-split-resizer axis-${split.axis}`}
      style={style as CSSProperties}
      data-split-path={path.length ? path.join('-') : 'root'}
      role="separator"
      aria-label={`Resize ${horizontal ? 'left and right' : 'top and bottom'} workspace panes`}
      aria-orientation={horizontal ? 'vertical' : 'horizontal'}
      aria-valuemin={Math.round(WORKSPACE_SPLIT_MIN_RATIO * 100)}
      aria-valuemax={Math.round(WORKSPACE_SPLIT_MAX_RATIO * 100)}
      aria-valuenow={Math.round(split.ratio * 100)}
      tabIndex={0}
      onPointerDown={(event) => beginResize(event, path, split, rect)}
      onPointerMove={resizeFromPointer}
      onPointerUp={finishResize}
      onPointerCancel={() => finishResize()}
      onDoubleClick={() => commit(resizeWorkspaceSplit(layoutRef.current, path, 0.5))}
      onKeyDown={(event) => {
        const decrease = horizontal ? 'ArrowLeft' : 'ArrowUp'
        const increase = horizontal ? 'ArrowRight' : 'ArrowDown'
        if (![decrease, increase, 'Home', 'End'].includes(event.key)) return
        event.preventDefault()
        const ratio = event.key === 'Home'
          ? WORKSPACE_SPLIT_MIN_RATIO
          : event.key === 'End'
            ? WORKSPACE_SPLIT_MAX_RATIO
            : split.ratio + (event.key === decrease ? -0.025 : 0.025)
        commit(resizeWorkspaceSplit(layoutRef.current, path, ratio))
      }}
    />
  }

  return (
    <section ref={workspaceRef} className={`session-workspace ${resizing ? `is-resizing is-resizing-${resizing}` : ''}`} aria-label="Session workspace">
      <div className="workspace-viewbar">
        <div className="workspace-viewbar-title"><LayoutDashboard size={13} /><span>Workspace</span></div>
        <div className="workspace-open-panes" aria-label="Open panes">
          {visible.map((id) => {
            const Icon = PANE_META[id].icon
            return <button key={id} type="button" onClick={() => focusPane(id)} className="workspace-pane-chip"><Icon size={11} />{PANE_META[id].label}</button>
          })}
        </div>
        <div className="workspace-views-menu" ref={viewsRef}>
          <button type="button" className="workspace-views-trigger" onClick={() => setViewsOpen((open) => !open)} aria-expanded={viewsOpen} aria-haspopup="menu">
            <PanelsTopLeft size={12} /> Views
          </button>
          {viewsOpen && <div className="workspace-views-popover" role="menu" aria-label="Workspace views">
            {layout.order.map((id) => {
              const Icon = PANE_META[id].icon
              const open = layout.open.includes(id)
              return <button key={id} type="button" role="menuitemcheckbox" aria-checked={open} disabled={id === 'chat'} onClick={() => setOpen(id, !open)}>
                <Icon size={12} /><span>{PANE_META[id].label}</span><span className={`workspace-view-check ${open ? 'checked' : ''}`}>{open ? '✓' : ''}</span>
              </button>
            })}
            <div className="workspace-views-divider" />
            <button type="button" role="menuitem" onClick={resetWorkspace}>
              <RotateCcw size={12} /><span>Reset layout</span>
            </button>
          </div>}
        </div>
      </div>

      <div ref={layoutTreeRef} className="workspace-layout-tree">
        {placements.panes.map(({ pane, rect }) => renderPane(pane, rect))}
        {placements.splits.map(renderSplit)}
      </div>
      {confirmationDialog}
      {choiceDialog}
    </section>
  )
}
