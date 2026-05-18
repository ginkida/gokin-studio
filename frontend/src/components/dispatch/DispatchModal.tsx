import { useState, useEffect } from 'react'
import { useProjectStore } from '../../stores/projectStore'
import { useSettingsStore, Settings } from '../../stores/settingsStore'
import { Dispatch } from '../../../wailsjs/go/studio/Studio'
import { Send, X, AlertTriangle, FolderX } from 'lucide-react'

interface DispatchModalProps {
  fromProjectId: string
  fromSessionId?: string
  onClose: () => void
}

// Surface why a given project can't be dispatched to. Returns null when it's
// ready to go. Used to show a preemptive warning under each card so the user
// doesn't pick a target only to get an error back.
function dispatchBlockerFor(provider: string, directoryOK: boolean | undefined, settings: Settings): string | null {
  if (directoryOK === false) return 'project directory missing'
  const keyFields: Record<string, keyof Settings> = {
    glm: 'glmKey',
    minimax: 'minimaxKey',
    kimi: 'kimiKey',
  }
  const keyField = keyFields[provider]
  if (keyField && !settings[keyField]) return 'no API key in settings'
  return null
}

export function DispatchModal({ fromProjectId, fromSessionId, onClose }: DispatchModalProps) {
  const projects = useProjectStore((s) => s.projects)
  const settings = useSettingsStore((s) => s.settings)
  const otherProjects = projects.filter((p) => p.id !== fromProjectId)

  const [targetId, setTargetId] = useState(otherProjects[0]?.id || '')
  const [task, setTask] = useState('')
  const [sending, setSending] = useState(false)
  const [result, setResult] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  // Close on Escape
  useEffect(() => {
    const handleKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handleKey)
    return () => window.removeEventListener('keydown', handleKey)
  }, [onClose])

  const targetProject = otherProjects.find((p) => p.id === targetId)
  const targetBlocker = targetProject
    ? dispatchBlockerFor(targetProject.provider || 'glm', targetProject.directoryOK, settings)
    : null

  const handleSend = async () => {
    if (!targetId || !task.trim() || targetBlocker) return
    setSending(true)
    setResult(null)
    try {
      await Dispatch(fromProjectId, targetId, fromSessionId || 'default', task.trim())
      setResult({ type: 'success', text: 'Dispatch sent. Result will appear in chat when complete.' })
      setTask('')
      // Auto-close after a brief moment so the user sees confirmation.
      setTimeout(onClose, 1800)
    } catch (e: any) {
      setResult({ type: 'error', text: `Dispatch failed: ${String(e)}` })
    } finally {
      setSending(false)
    }
  }

  return (
    <>
      <div className="dispatch-backdrop" onClick={onClose} />
      <div className="dispatch-modal">
        <div className="dispatch-header">
          <h3>Dispatch Task</h3>
          <button className="icon-btn" onClick={onClose} title="Close">
            <X size={16} />
          </button>
        </div>

        {otherProjects.length === 0 ? (
          <div className="dispatch-empty">
            No other projects available. Add another project first.
          </div>
        ) : (
          <>
            <div className="dispatch-field">
              <label>Target Project</label>
              <div className="dispatch-project-cards">
                {otherProjects.map((p) => {
                  const blocker = dispatchBlockerFor(p.provider || 'glm', p.directoryOK, settings)
                  return (
                    <div
                      key={p.id}
                      className={`dispatch-project-card ${targetId === p.id ? 'selected' : ''} ${blocker ? 'has-blocker' : ''}`}
                      onClick={() => setTargetId(p.id)}
                      title={blocker || undefined}
                    >
                      <div className="dispatch-project-card-radio">
                        <div className="dispatch-project-card-radio-inner" />
                      </div>
                      <div className="dispatch-project-card-info">
                        <div className="dispatch-project-card-name">
                          {p.name}
                          {blocker && (
                            <span className="dispatch-card-warn" title={blocker}>
                              {p.directoryOK === false ? <FolderX size={11} /> : <AlertTriangle size={11} />}
                            </span>
                          )}
                        </div>
                        <div className="dispatch-project-card-provider">
                          {p.provider || 'glm'}
                          {blocker && <span className="dispatch-card-blocker-text"> · {blocker}</span>}
                        </div>
                      </div>
                    </div>
                  )
                })}
              </div>
            </div>

            <div className="dispatch-field">
              <label>Task Description</label>
              <textarea
                value={task}
                onChange={(e) => setTask(e.target.value)}
                placeholder="Describe the task to dispatch..."
                rows={4}
                maxLength={10000}
                autoFocus
                onKeyDown={(e) => {
                  if (e.nativeEvent.isComposing || e.keyCode === 229) return
                  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
                    e.preventDefault()
                    if (targetId && task.trim() && !sending && !targetBlocker) handleSend()
                  }
                }}
              />
            </div>

            {targetBlocker && (
              <div className="dispatch-warning">
                <AlertTriangle size={14} />
                <span>Target can't run: {targetBlocker}. Pick another project or fix it first.</span>
              </div>
            )}

            <div className="dispatch-actions">
              <span className="dispatch-hint">Ctrl/Cmd+Enter to send</span>
              <button className="btn-secondary" onClick={onClose}>Cancel</button>
              <button
                className="btn-primary"
                onClick={handleSend}
                disabled={!targetId || !task.trim() || sending || !!targetBlocker}
                title={targetBlocker ? `Cannot dispatch: ${targetBlocker}` : undefined}
              >
                <Send size={14} />
                {sending ? 'Sending...' : 'Send'}
              </button>
            </div>
          </>
        )}

        {result && (
          <div className={`dispatch-result ${result.type}`}>
            {result.text}
          </div>
        )}
      </div>
    </>
  )
}
