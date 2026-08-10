import { useEffect, useState } from 'react'
import { ChevronLeft, ChevronRight, PanelLeft, PanelRight, GitBranch } from 'lucide-react'
import { GetProjectGitContext } from '../../../wailsjs/go/studio/Studio'
import { EventsOn } from '../../../wailsjs/runtime/runtime'

interface TopBarProps {
  title: string
  projectName?: string
  projectId?: string | null
  isChat: boolean
  onToggleSidebar: () => void
  sidebarExpanded: boolean
  canNavigateBack: boolean
  canNavigateForward: boolean
  onNavigateBack: () => void
  onNavigateForward: () => void
}

// Full-width window top bar (sits below the native OS titlebar). Keep it as a
// quiet breadcrumb rather than a second status dashboard: project and current
// view answer "where am I?", while the two edge buttons control the panels.
export function TopBar({
  title,
  projectName,
  projectId,
  isChat,
  onToggleSidebar,
  sidebarExpanded,
  canNavigateBack,
  canNavigateForward,
  onNavigateBack,
  onNavigateForward,
}: TopBarProps) {
  const [branch, setBranch] = useState('')

  // Read-only current branch. Loaded on project switch and refreshed when a turn
  // finishes (chat:complete) or the project goes idle — so a mid-session
  // `git checkout` updates the label, matching the context panel. A
  // stale-response guard prevents a slow switch from clobbering the new project.
  useEffect(() => {
    if (!projectId) { setBranch(''); return }
    let cancelled = false
    const load = () => GetProjectGitContext(projectId)
      .then((g: any) => { if (!cancelled) setBranch(g?.isRepo ? (g.branch || '') : '') })
      .catch(() => { if (!cancelled) setBranch('') })
    load()
    const off1 = EventsOn('chat:complete', (d: any) => { if (d?.projectID === projectId) load() })
    const off2 = EventsOn('project:status', (d: any) => { if (d?.projectID === projectId && d?.status === 'idle') load() })
    return () => { cancelled = true; off1(); off2() }
  }, [projectId])

  return (
    <div className="top-bar">
      <div className="top-bar-left">
        <button
          className="top-bar-btn"
          onClick={onToggleSidebar}
          title={`${sidebarExpanded ? 'Hide' : 'Show'} sidebar (Ctrl+B)`}
          aria-label={`${sidebarExpanded ? 'Hide' : 'Show'} sidebar`}
          aria-expanded={sidebarExpanded}
          aria-controls="project-sidebar"
        >
          <PanelLeft size={15} />
        </button>
        <span className="top-bar-nav-divider" aria-hidden />
        <button
          className="top-bar-btn top-bar-history-btn"
          type="button"
          onClick={onNavigateBack}
          disabled={!canNavigateBack}
          title="Back (Ctrl/Cmd+[ · Mouse Back)"
          aria-label="Go back"
        >
          <ChevronLeft size={15} />
        </button>
        <button
          className="top-bar-btn top-bar-history-btn"
          type="button"
          onClick={onNavigateForward}
          disabled={!canNavigateForward}
          title="Forward (Ctrl/Cmd+] · Mouse Forward)"
          aria-label="Go forward"
        >
          <ChevronRight size={15} />
        </button>
      </div>

      <div className="top-bar-center">
        <div className="top-bar-breadcrumb" aria-label="Current location">
          {projectName && <span className="top-bar-project" title={projectName}>{projectName}</span>}
          {projectName && title && <span className="top-bar-slash" aria-hidden>/</span>}
          <span className="top-bar-title" title={title}>{title}</span>
        </div>
        {branch && (
          <span className="top-bar-branch" title={`On branch ${branch}`}>
            <GitBranch size={11} />
            <span className="tbc-text">{branch}</span>
          </span>
        )}
      </div>

      <div className="top-bar-right">
        {isChat && (
          <button
            className="top-bar-btn"
            onClick={() => window.dispatchEvent(new CustomEvent('gokin:toggle-context'))}
            title="Toggle context panel (Ctrl+J)"
            aria-label="Toggle context panel"
          >
            <PanelRight size={15} />
          </button>
        )}
      </div>
    </div>
  )
}
