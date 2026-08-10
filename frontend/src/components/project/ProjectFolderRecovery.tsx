import { useState } from 'react'
import { FolderOpen, FolderX, Loader2 } from 'lucide-react'
import { BrowseDirectory, RelinkProjectDirectory } from '../../../wailsjs/go/studio/Studio'
import { useChatStore } from '../../stores/chatStore'
import { type ProjectInfo, useProjectStore } from '../../stores/projectStore'

export function ProjectFolderRecovery({
  project,
  variant = 'state',
  className = '',
}: {
  project: ProjectInfo
  variant?: 'banner' | 'state'
  className?: string
}) {
  const updateProject = useProjectStore((state) => state.updateProject)
  const projectHasActiveTurn = useChatStore((state) => {
    const prefix = `${project.id}_`
    return Object.entries(state.sessionActive).some(([key, active]) => active && key.startsWith(prefix))
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const relink = async () => {
    if (busy || projectHasActiveTurn) return
    const targetProjectID = project.id
    setBusy(true)
    setError(null)
    try {
      const directory = await BrowseDirectory()
      if (!directory) return
      const updated = await RelinkProjectDirectory(targetProjectID, directory)
      if (!updated || updated.id !== targetProjectID) throw new Error('invalid project response')
      updateProject(targetProjectID, updated as ProjectInfo)
    } catch (reason: any) {
      setError(String(reason?.message || reason || 'unknown error'))
    } finally {
      setBusy(false)
    }
  }

  const banner = variant === 'banner'
  return (
    <div
      className={`${banner ? 'chat-warning chat-warning-error project-folder-recovery-banner' : 'project-folder-recovery-state'} ${className}`.trim()}
      role="alert"
    >
      <FolderX size={banner ? 14 : 32} aria-hidden="true" />
      <span className="project-folder-recovery-copy">
        <strong>Project folder unavailable</strong>
        <small>The connected path no longer exists: <span className="mono">{project.directory}</span></small>
        {error && <small className="project-folder-recovery-error">Could not reconnect: {error}</small>}
      </span>
      <button
        type="button"
        className={banner ? 'chat-warning-action' : 'btn-primary project-folder-recovery-action'}
        disabled={busy || projectHasActiveTurn}
        title={projectHasActiveTurn ? 'Stop all running chats in this project first' : 'Choose the moved or renamed project folder'}
        onClick={() => void relink()}
      >
        {busy
          ? <><Loader2 size={12} className="spin" /> Reconnecting…</>
          : <><FolderOpen size={12} /> Relink folder…</>}
      </button>
    </div>
  )
}
