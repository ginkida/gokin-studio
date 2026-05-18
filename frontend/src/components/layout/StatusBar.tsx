import { Brain } from 'lucide-react'
import { useProjectStore } from '../../stores/projectStore'
import { useChatStore } from '../../stores/chatStore'

export function StatusBar() {
  const activeProject = useProjectStore((s) =>
    s.projects.find((p) => p.id === s.activeProjectId)
  )
  const activeProjectId = useProjectStore((s) => s.activeProjectId)
  const projectCount = useProjectStore((s) => s.projects.length)
  // Chat store is keyed by `projectID_sessionID`, so aggregate across every
  // session that belongs to the active project.
  const msgCount = useChatStore((s) => {
    if (!activeProjectId) return 0
    const prefix = activeProjectId + '_'
    let total = 0
    for (const [key, msgs] of Object.entries(s.messages)) {
      if (key.startsWith(prefix) && Array.isArray(msgs)) total += msgs.length
    }
    return total
  })
  // Derive "any session active" from the chat store instead of the stored
  // project.active flag — the latter flips with each per-session start/stop,
  // so a finished sibling session would grey the status dot while another
  // session was still running.
  const anyActive = useChatStore((s) => {
    if (!activeProjectId) return false
    const prefix = activeProjectId + '_'
    for (const [key, active] of Object.entries(s.sessionActive)) {
      if (active && key.startsWith(prefix)) return true
    }
    return false
  })

  return (
    <div className="status-bar">
      <div className="status-left">
        <span className={`status-dot ${anyActive ? 'active' : activeProject ? 'connected' : ''}`} />
        {activeProject ? (
          <>
            <span className="status-project">{activeProject.name}</span>
            <span className="status-sep" />
            <span className="status-provider">{activeProject.provider || 'glm'}</span>
            <span className="status-model">{activeProject.model || 'glm-5.1'}</span>
            {(activeProject.thinkingMode === 'enabled' || (activeProject.thinkingMode !== 'disabled' && activeProject.provider === 'kimi')) && (
              <span className="status-thinking" title="Extended thinking enabled">
                <Brain size={11} />
              </span>
            )}
          </>
        ) : (
          <span>No project selected</span>
        )}
      </div>
      <div className="status-right">
        {msgCount > 0 && <span className="status-info">{msgCount} msg{msgCount === 1 ? '' : 's'}</span>}
        <span className="status-info">{projectCount} project{projectCount !== 1 ? 's' : ''}</span>
        <span className="status-version">v1.0</span>
      </div>
    </div>
  )
}
