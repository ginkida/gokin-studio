import { useEffect, useState } from 'react'
import { PanelLeft, PanelRight, Folder, GitBranch } from 'lucide-react'
import { GetProjectGitContext } from '../../../wailsjs/go/studio/Studio'
import { EventsOn } from '../../../wailsjs/runtime/runtime'

interface TopBarProps {
  title: string
  projectName?: string
  projectId?: string | null
  isChat: boolean
  onToggleSidebar: () => void
}

// Full-width window top bar (sits BELOW the native OS titlebar — we did not go
// frameless, so there is no traffic-light region to clear). It shows the active
// view/session title, the active-project chip, the read-only current git
// branch, and panel toggles that drive the REAL sidebar-collapse + context-panel
// toggle. Back/forward navigation and an account avatar are deliberately omitted:
// studio has no view-history stack and no user identity, and dead chrome reads
// worse than honest absence.
export function TopBar({ title, projectName, projectId, isChat, onToggleSidebar }: TopBarProps) {
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
          title="Toggle sidebar (Ctrl+B)"
          aria-label="Toggle sidebar"
        >
          <PanelLeft size={15} />
        </button>
      </div>

      <div className="top-bar-center">
        <span className="top-bar-title" title={title}>{title}</span>
        {projectName && (
          <span className="top-bar-chip">
            <Folder size={11} />
            <span className="tbc-text">{projectName}</span>
          </span>
        )}
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
