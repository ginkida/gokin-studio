import { useCallback, useEffect, useState } from 'react'
import {
  GitBranch, RefreshCw, Target, ListChecks, CheckCircle2, Circle,
  XCircle, Loader2, SkipForward, PanelRightClose, PanelRightOpen, GitCommitVertical,
  GitCommitHorizontal, Check,
} from 'lucide-react'
import { GetProjectGitContext, GetProjectPlan, CommitChanges } from '../../../wailsjs/go/studio/Studio'
import { EventsOn } from '../../../wailsjs/runtime/runtime'

interface GitFileChange { path: string; status: string }
interface GitCtx {
  branch?: string
  isRepo: boolean
  changedFiles?: GitFileChange[]
  untrackedFiles?: GitFileChange[]
  aheadBehind?: string
  insertions?: number
  deletions?: number
}
interface PlanStep { title: string; status: string }
interface PlanInfo {
  active: boolean
  title?: string
  status?: string
  currentStep: number
  totalSteps: number
  steps?: PlanStep[]
}

const COLLAPSE_KEY = 'gokin:context-panel-collapsed'

function statusChar(s: string): string {
  switch (s) {
    case 'modified': return 'M'
    case 'added': return 'A'
    case 'deleted': return 'D'
    case 'renamed': return 'R'
    case 'untracked': return '?'
    default: return '~'
  }
}

function StepIcon({ status }: { status: string }) {
  switch (status) {
    case 'completed': return <CheckCircle2 size={13} className="cp-ic-done" />
    case 'in_progress': return <Loader2 size={13} className="cp-ic-active spin" />
    case 'failed': return <XCircle size={13} className="cp-ic-fail" />
    case 'skipped': return <SkipForward size={13} className="cp-ic-skip" />
    default: return <Circle size={13} className="cp-ic-pending" />
  }
}

function goalStatusLabel(s?: string): string {
  if (s === 'completed') return 'Complete'
  if (s === 'failed') return 'Failed'
  return 'In progress'
}

export function ContextPanel({ projectId }: { projectId: string }) {
  const [collapsed, setCollapsed] = useState<boolean>(() => localStorage.getItem(COLLAPSE_KEY) === '1')
  const [git, setGit] = useState<GitCtx | null>(null)
  const [plan, setPlan] = useState<PlanInfo | null>(null)
  const [loading, setLoading] = useState(false)
  const [commitOpen, setCommitOpen] = useState(false)
  const [commitMsg, setCommitMsg] = useState('')
  const [committing, setCommitting] = useState(false)
  const [commitError, setCommitError] = useState<string | null>(null)
  const [commitOk, setCommitOk] = useState<string | null>(null)

  const load = useCallback((pid: string, onResult: (g: GitCtx | null, p: PlanInfo | null) => void) => {
    setLoading(true)
    Promise.all([
      GetProjectGitContext(pid).catch(() => null),
      GetProjectPlan(pid).catch(() => null),
    ])
      .then(([g, p]) => onResult(g as unknown as GitCtx | null, p as unknown as PlanInfo | null))
      .finally(() => setLoading(false))
  }, [])

  // Initial load + reload on project switch, with a stale-response guard.
  useEffect(() => {
    // Reset the commit composer when the project changes.
    setCommitOpen(false)
    setCommitMsg('')
    setCommitError(null)
    setCommitOk(null)
    if (!projectId) { setGit(null); setPlan(null); return }
    let cancelled = false
    load(projectId, (g, p) => { if (!cancelled) { setGit(g); setPlan(p) } })
    return () => { cancelled = true }
  }, [projectId, load])

  // Refresh after the agent finishes a turn (git changes + plan progress).
  useEffect(() => {
    if (!projectId) return
    const refresh = () => load(projectId, (g, p) => { setGit(g); setPlan(p) })
    const off1 = EventsOn('chat:complete', (d: any) => { if (d?.projectID === projectId) refresh() })
    const off2 = EventsOn('project:status', (d: any) => { if (d?.projectID === projectId && d?.status === 'idle') refresh() })
    return () => { off1(); off2() }
  }, [projectId, load])

  const toggle = useCallback(() => {
    setCollapsed((c) => {
      const next = !c
      localStorage.setItem(COLLAPSE_KEY, next ? '1' : '0')
      return next
    })
  }, [])

  const manualRefresh = useCallback(() => {
    if (projectId) load(projectId, (g, p) => { setGit(g); setPlan(p) })
  }, [projectId, load])

  const doCommit = useCallback(() => {
    const msg = commitMsg.trim()
    if (!projectId || !msg || committing) return
    setCommitting(true)
    setCommitError(null)
    CommitChanges(projectId, msg)
      .then((res: any) => {
        setCommitMsg('')
        setCommitOpen(false)
        setCommitOk(res?.hash ? `Committed ${res.hash}` : 'Committed')
        load(projectId, (g, p) => { setGit(g); setPlan(p) })
        window.setTimeout(() => setCommitOk(null), 3500)
      })
      .catch((e: any) => setCommitError(String(e?.message || e)))
      .finally(() => setCommitting(false))
  }, [projectId, commitMsg, committing, load])

  // Ctrl+J / Cmd+J toggles the context panel (symmetric with Ctrl+B for the
  // left sidebar). IME-guarded so a CJK/Cyrillic compose-commit doesn't fire it.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'j') {
        e.preventDefault()
        toggle()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggle])

  // The top-bar right panel-toggle button drives the same collapse via a
  // decoupled custom event (the bar doesn't own this panel's state).
  useEffect(() => {
    const onToggle = () => toggle()
    window.addEventListener('gokin:toggle-context', onToggle)
    return () => window.removeEventListener('gokin:toggle-context', onToggle)
  }, [toggle])

  // Something worth surfacing even when the panel is collapsed.
  const collapsedHasState = !!(
    git?.changedFiles?.length ||
    git?.untrackedFiles?.length ||
    git?.insertions ||
    git?.deletions ||
    plan?.active
  )

  if (collapsed) {
    return (
      <div className="context-panel is-collapsed">
        <button className="context-rail-btn" onClick={toggle} title="Show context panel (Ctrl+J)">
          <PanelRightOpen size={16} />
          {collapsedHasState && <span className="context-rail-dot" title="Uncommitted changes or an active plan" />}
        </button>
      </div>
    )
  }

  const changed = git?.changedFiles || []
  const hasDiff = !!(git?.insertions || git?.deletions)
  const untrackedCount = git?.untrackedFiles?.length || 0
  const hasChanges = changed.length > 0 || untrackedCount > 0

  return (
    <div className="context-panel">
      <div className="context-head">
        <span className="context-head-title">Context</span>
        <div className="context-head-actions">
          <button onClick={manualRefresh} title="Refresh" disabled={loading}>
            <RefreshCw size={13} className={loading ? 'spin' : ''} />
          </button>
          <button onClick={toggle} title="Hide panel (Ctrl+J)"><PanelRightClose size={14} /></button>
        </div>
      </div>

      <div className="context-body">
      {/* Git tools */}
      <section className="context-section">
        <div className="context-section-head"><GitBranch size={13} /><span>Git tools</span></div>
        {git?.isRepo ? (
          <div className="context-git">
            <div className="context-git-branch">
              <GitBranch size={12} />
              <span className="cg-branch-name">{git.branch || '—'}</span>
              {git.aheadBehind && <span className="cg-aheadbehind">{git.aheadBehind}</span>}
            </div>
            {hasDiff ? (
              <div className="context-git-stat">
                <span className="cg-changes">Changes</span>
                <span className="cg-add">+{git.insertions || 0}</span>
                <span className="cg-del">-{git.deletions || 0}</span>
              </div>
            ) : (
              <div className="context-git-clean"><GitCommitVertical size={12} /> working tree clean</div>
            )}
            {changed.length > 0 && (
              <ul className="context-git-files">
                {changed.slice(0, 8).map((f, i) => (
                  <li key={i} title={f.path}>
                    <span className={`cg-fstat cg-fs-${f.status}`}>{statusChar(f.status)}</span>
                    <span className="cg-fpath">{f.path}</span>
                  </li>
                ))}
                {changed.length > 8 && <li className="cg-more">+{changed.length - 8} more</li>}
              </ul>
            )}
            {untrackedCount > 0 && (
              <div className="context-git-untracked">{untrackedCount} untracked file{untrackedCount === 1 ? '' : 's'}</div>
            )}
            {hasChanges && (commitOpen ? (
              <div className="commit-composer">
                <textarea
                  className="commit-msg"
                  value={commitMsg}
                  onChange={(e) => setCommitMsg(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.nativeEvent.isComposing || e.keyCode === 229) return
                    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); doCommit() }
                    if (e.key === 'Escape') { e.preventDefault(); setCommitOpen(false); setCommitError(null) }
                  }}
                  placeholder="Commit message…  (Ctrl/Cmd+Enter to commit)"
                  rows={2}
                  maxLength={500}
                  autoFocus
                  disabled={committing}
                />
                {commitError && <div className="commit-error">{commitError}</div>}
                <div className="commit-actions">
                  <button className="commit-cancel" onClick={() => { setCommitOpen(false); setCommitError(null) }} disabled={committing}>Cancel</button>
                  <button className="commit-go" onClick={doCommit} disabled={committing || !commitMsg.trim()}>
                    {committing ? <Loader2 size={12} className="spin" /> : <Check size={12} />} Commit all
                  </button>
                </div>
              </div>
            ) : (
              <button className="context-commit-btn" onClick={() => { setCommitOpen(true); setCommitError(null) }}>
                <GitCommitHorizontal size={13} /> Commit all changes…
              </button>
            ))}
            {commitOk && <div className="commit-ok"><Check size={12} /> {commitOk}</div>}
          </div>
        ) : (
          <div className="context-empty">Not a git repository.</div>
        )}
      </section>

      {/* Goal */}
      <section className="context-section">
        <div className="context-section-head">
          <Target size={13} /><span>Goal</span>
          {plan?.active && (
            <span className={`context-goal-badge gb-${plan.status || 'in_progress'}`}>
              {goalStatusLabel(plan.status)}
            </span>
          )}
        </div>
        {plan?.active ? (
          <div className="context-goal">
            <div className="context-goal-title">{plan.title || 'Untitled plan'}</div>
            <div className="context-goal-meta">{plan.currentStep}/{plan.totalSteps} steps</div>
          </div>
        ) : (
          <div className="context-empty">No active plan. The agent uses plan mode for multi-step tasks.</div>
        )}
      </section>

      {/* Progress */}
      {plan?.active && plan.steps && plan.steps.length > 0 && (
        <section className="context-section">
          <div className="context-section-head"><ListChecks size={13} /><span>Progress</span></div>
          <ul className="context-progress">
            {plan.steps.map((st, i) => (
              <li key={i} className={`cp-step cp-${st.status}`}>
                <StepIcon status={st.status} />
                <span className="cp-step-title">{st.title}</span>
              </li>
            ))}
          </ul>
        </section>
      )}
      </div>
    </div>
  )
}
