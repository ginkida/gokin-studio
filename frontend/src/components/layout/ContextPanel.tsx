import { useCallback, useEffect, useRef, useState, type CSSProperties } from 'react'
import {
  GitBranch, RefreshCw, Target, ListChecks, CheckCircle2, Circle,
  XCircle, Loader2, SkipForward, PanelRightClose, PanelRightOpen, GitCommitVertical,
  GitCommitHorizontal, Check,
  BookOpen, Plus, Trash2, FileText, Sparkles, Monitor, ShieldCheck, Ban,
  Link2, FileDiff,
} from 'lucide-react'
import {
  GetSessionGitContext, GetSessionPlan, CommitSessionChanges,
  ImportProjectKnowledge, ListProjectKnowledge, RemoveProjectKnowledge,
  AddProjectKnowledgeURL, RefreshProjectKnowledgeURL, RemoveProjectKnowledgeURL,
  ListProjectSkills,
  ListProjectComputerPermissions, SetProjectComputerAppPermission,
  ListProjectToolPermissions, RevokeProjectToolPermission,
} from '../../../wailsjs/go/studio/Studio'
import { EventsOn } from '../../../wailsjs/runtime/runtime'
import { hasOpenModal } from '../../hooks/useModalFocusManagement'
import { useConfirmDialog } from '../common/AppDialog'
import { GitReviewModal } from './GitReviewModal'

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
interface KnowledgeDocument {
  id: string
  name: string
  sourceType: 'file' | 'url'
  url?: string
  size: number
  updatedAt: number
}
interface KnowledgeImportResult {
  documents?: KnowledgeDocument[]
  imported?: KnowledgeDocument[]
  failures?: { name: string; error: string }[]
}
interface ProjectSkillInfo { name: string; description: string; path: string; source: string }
interface ProjectSkillIssue { path: string; error: string }
interface ProjectSkillInventory { skills?: ProjectSkillInfo[]; issues?: ProjectSkillIssue[] }
interface ComputerAppPermissions { allowed?: string[]; blocked?: string[] }
interface ProjectToolPermission { tool: string; scope: string; description: string; createdAt?: number }

const COLLAPSE_KEY = 'gokin:context-panel-collapsed'
const WIDTH_KEY = 'gokin:context-panel-width'
const CONTEXT_WIDTH_MIN = 260
const CONTEXT_WIDTH_MAX = 520
const CONTEXT_WIDTH_DEFAULT = 304

function initialContextCollapsed(): boolean {
  try { return localStorage.getItem(COLLAPSE_KEY) === '1' } catch { return false }
}

function clampContextWidth(value: number) {
  return Math.max(CONTEXT_WIDTH_MIN, Math.min(CONTEXT_WIDTH_MAX, Math.round(value)))
}

function initialContextWidth(): number {
  try {
    const stored = Number(localStorage.getItem(WIDTH_KEY))
    return Number.isFinite(stored) && stored > 0 ? clampContextWidth(stored) : CONTEXT_WIDTH_DEFAULT
  } catch {
    return CONTEXT_WIDTH_DEFAULT
  }
}

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

export function ContextPanel({ projectId, sessionId, workspaceMode = false }: { projectId: string; sessionId: string; workspaceMode?: boolean }) {
  const [requestConfirmation, confirmationDialog] = useConfirmDialog()
  const projectIdRef = useRef(projectId)
  projectIdRef.current = projectId
  const knowledgeRequestRef = useRef(0)
  const skillsRequestRef = useRef(0)
  const computerRequestRef = useRef(0)
  const toolPermissionRequestRef = useRef(0)
  const [collapsed, setCollapsed] = useState<boolean>(initialContextCollapsed)
  const [panelWidth, setPanelWidth] = useState<number>(initialContextWidth)
  const panelWidthRef = useRef(panelWidth)
  const resizeActiveRef = useRef(false)
  const resizeRightRef = useRef(0)
  const [resizing, setResizing] = useState(false)
  const [git, setGit] = useState<GitCtx | null>(null)
  const [plan, setPlan] = useState<PlanInfo | null>(null)
  const [loading, setLoading] = useState(false)
  const [commitOpen, setCommitOpen] = useState(false)
  const [commitMsg, setCommitMsg] = useState('')
  const [committing, setCommitting] = useState(false)
  const [commitError, setCommitError] = useState<string | null>(null)
  const [commitOk, setCommitOk] = useState<string | null>(null)
  const [gitReviewOpen, setGitReviewOpen] = useState(false)
  const [knowledge, setKnowledge] = useState<KnowledgeDocument[]>([])
  const [knowledgeBusy, setKnowledgeBusy] = useState(false)
  const [knowledgeError, setKnowledgeError] = useState<string | null>(null)
  const [knowledgeURLOpen, setKnowledgeURLOpen] = useState(false)
  const [knowledgeURLDraft, setKnowledgeURLDraft] = useState('')
  const [skills, setSkills] = useState<ProjectSkillInfo[]>([])
  const [skillIssues, setSkillIssues] = useState<ProjectSkillIssue[]>([])
  const [computerPermissions, setComputerPermissions] = useState<ComputerAppPermissions>({ allowed: [], blocked: [] })
  const [computerPermissionError, setComputerPermissionError] = useState<string | null>(null)
  const [toolPermissions, setToolPermissions] = useState<ProjectToolPermission[]>([])
  const [toolPermissionError, setToolPermissionError] = useState<string | null>(null)

  const loadToolPermissions = useCallback((pid: string) => {
    const request = ++toolPermissionRequestRef.current
    ListProjectToolPermissions(pid)
      .then((raw: any) => {
        if (projectIdRef.current === pid && toolPermissionRequestRef.current === request) {
          setToolPermissions((raw || []) as ProjectToolPermission[])
          setToolPermissionError(null)
        }
      })
      .catch((e: any) => {
        if (projectIdRef.current === pid && toolPermissionRequestRef.current === request) {
          setToolPermissionError(String(e?.message || e))
        }
      })
  }, [])

  const loadComputerPermissions = useCallback((pid: string) => {
    const request = ++computerRequestRef.current
    ListProjectComputerPermissions(pid)
      .then((raw: any) => {
        if (projectIdRef.current === pid && computerRequestRef.current === request) {
          setComputerPermissions((raw || { allowed: [], blocked: [] }) as ComputerAppPermissions)
          setComputerPermissionError(null)
        }
      })
      .catch((e: any) => {
        if (projectIdRef.current === pid && computerRequestRef.current === request) {
          setComputerPermissionError(String(e?.message || e))
        }
      })
  }, [])

  const loadSkills = useCallback((pid: string) => {
    const request = ++skillsRequestRef.current
    ListProjectSkills(pid)
      .then((raw: any) => {
        if (projectIdRef.current !== pid || skillsRequestRef.current !== request) return
        const inventory = (raw || {}) as ProjectSkillInventory
        setSkills(inventory.skills || [])
        setSkillIssues(inventory.issues || [])
      })
      .catch((e: any) => {
        if (projectIdRef.current === pid && skillsRequestRef.current === request) {
          setSkills([])
          setSkillIssues([{ path: 'Skills', error: String(e?.message || e) }])
        }
      })
  }, [])

  const loadKnowledge = useCallback((pid: string) => {
    const request = ++knowledgeRequestRef.current
    ListProjectKnowledge(pid)
      .then((docs) => {
        if (projectIdRef.current === pid && knowledgeRequestRef.current === request) {
          setKnowledge((docs || []) as unknown as KnowledgeDocument[])
        }
      })
      .catch((e: any) => {
        if (projectIdRef.current === pid && knowledgeRequestRef.current === request) {
          setKnowledgeError(String(e?.message || e))
        }
      })
  }, [])

  const load = useCallback((pid: string, sid: string, onResult: (g: GitCtx | null, p: PlanInfo | null) => void) => {
    setLoading(true)
    Promise.all([
      GetSessionGitContext(pid, sid).catch(() => null),
      GetSessionPlan(pid, sid).catch(() => null),
    ])
      .then(([g, p]) => onResult(g as unknown as GitCtx | null, p as unknown as PlanInfo | null))
      .finally(() => setLoading(false))
  }, [])

  const refreshSessionContext = useCallback(() => {
    load(projectId, sessionId, (g, p) => { setGit(g); setPlan(p) })
  }, [load, projectId, sessionId])

  const openGitReview = useCallback(() => {
    if (workspaceMode) window.dispatchEvent(new CustomEvent('gokin:open-git-review'))
    else setGitReviewOpen(true)
  }, [workspaceMode])

  // Initial load + reload on project switch, with a stale-response guard.
  useEffect(() => {
    // Reset the commit composer when the project changes.
    setCommitOpen(false)
    setCommitMsg('')
    setCommitError(null)
    setCommitOk(null)
    setGitReviewOpen(false)
    setKnowledgeError(null)
    setKnowledgeBusy(false)
    setKnowledge([])
    setKnowledgeURLOpen(false)
    setKnowledgeURLDraft('')
    setSkills([])
    setSkillIssues([])
    setComputerPermissions({ allowed: [], blocked: [] })
    setComputerPermissionError(null)
    setToolPermissions([])
    setToolPermissionError(null)
    if (!projectId) { setGit(null); setPlan(null); return }
    let cancelled = false
    load(projectId, sessionId, (g, p) => { if (!cancelled) { setGit(g); setPlan(p) } })
    loadKnowledge(projectId)
    loadSkills(projectId)
    loadComputerPermissions(projectId)
    loadToolPermissions(projectId)
    return () => { cancelled = true }
  }, [projectId, sessionId, load, loadKnowledge, loadSkills, loadComputerPermissions, loadToolPermissions])

  // Refresh after the agent finishes a turn (git changes + plan progress).
  useEffect(() => {
    if (!projectId) return
    const refresh = () => {
      load(projectId, sessionId, (g, p) => { setGit(g); setPlan(p) })
      loadComputerPermissions(projectId)
      loadToolPermissions(projectId)
    }
    const off1 = EventsOn('chat:complete', (d: any) => { if (d?.projectID === projectId) refresh() })
    const off2 = EventsOn('project:status', (d: any) => { if (d?.projectID === projectId && d?.status === 'idle') refresh() })
    const onFileSaved = (event: Event) => {
      const detail = (event as CustomEvent).detail || {}
      if (detail.projectID === projectId && detail.sessionID === sessionId) refresh()
    }
    window.addEventListener('gokin:session-file-saved', onFileSaved)
    return () => { off1(); off2(); window.removeEventListener('gokin:session-file-saved', onFileSaved) }
  }, [projectId, sessionId, load, loadComputerPermissions, loadToolPermissions])

  const toggle = useCallback(() => {
    setCollapsed((c) => {
      const next = !c
      try { localStorage.setItem(COLLAPSE_KEY, next ? '1' : '0') } catch { /* storage unavailable */ }
      return next
    })
  }, [])

  const updatePanelWidth = useCallback((value: number, persist = false) => {
    const next = clampContextWidth(value)
    panelWidthRef.current = next
    setPanelWidth(next)
    if (persist) {
      try { localStorage.setItem(WIDTH_KEY, String(next)) } catch { /* storage unavailable */ }
    }
  }, [])

  useEffect(() => {
    const resetLayout = () => {
      resizeActiveRef.current = false
      setResizing(false)
      setCollapsed(false)
      updatePanelWidth(CONTEXT_WIDTH_DEFAULT)
    }
    window.addEventListener('gokin:layout-reset', resetLayout)
    return () => window.removeEventListener('gokin:layout-reset', resetLayout)
  }, [updatePanelWidth])

  const manualRefresh = useCallback(() => {
    if (projectId) {
      load(projectId, sessionId, (g, p) => { setGit(g); setPlan(p) })
      loadKnowledge(projectId)
      loadSkills(projectId)
      loadComputerPermissions(projectId)
      loadToolPermissions(projectId)
    }
  }, [projectId, sessionId, load, loadKnowledge, loadSkills, loadComputerPermissions, loadToolPermissions])

  const removeComputerPermission = useCallback((appID: string) => {
    if (!projectId) return
    setComputerPermissionError(null)
    SetProjectComputerAppPermission(projectId, appID, 'remove')
      .then(() => loadComputerPermissions(projectId))
      .catch((e: any) => setComputerPermissionError(String(e?.message || e)))
  }, [projectId, loadComputerPermissions])

  const removeToolPermission = useCallback((toolName: string) => {
    if (!projectId) return
    setToolPermissionError(null)
    RevokeProjectToolPermission(projectId, toolName)
      .then(() => loadToolPermissions(projectId))
      .catch((e: any) => setToolPermissionError(String(e?.message || e)))
  }, [projectId, loadToolPermissions])

  const doCommit = useCallback(() => {
    const msg = commitMsg.trim()
    if (!projectId || !msg || committing) return
    setCommitting(true)
    setCommitError(null)
    CommitSessionChanges(projectId, sessionId, msg)
      .then((res: any) => {
        setCommitMsg('')
        setCommitOpen(false)
        setCommitOk(res?.hash ? `Committed ${res.hash}` : 'Committed')
        load(projectId, sessionId, (g, p) => { setGit(g); setPlan(p) })
        window.setTimeout(() => setCommitOk(null), 3500)
      })
      .catch((e: any) => setCommitError(String(e?.message || e)))
      .finally(() => setCommitting(false))
  }, [projectId, sessionId, commitMsg, committing, load])

  const addKnowledge = useCallback(() => {
    if (!projectId || knowledgeBusy) return
    const pid = projectId
    setKnowledgeBusy(true)
    setKnowledgeError(null)
    ImportProjectKnowledge(projectId)
      .then((raw: any) => {
        if (projectIdRef.current !== pid) return
        const result = (raw || {}) as KnowledgeImportResult
        setKnowledge(result.documents || [])
        if (result.failures?.length) {
          const imported = result.imported?.length || 0
          const details = result.failures.map((f) => `${f.name}: ${f.error}`).join('; ')
          setKnowledgeError(`${imported} imported, ${result.failures.length} skipped — ${details}`)
        }
      })
      .catch((e: any) => {
        if (projectIdRef.current === pid) {
          setKnowledgeError(String(e?.message || e))
          loadKnowledge(pid)
        }
      })
      .finally(() => { if (projectIdRef.current === pid) setKnowledgeBusy(false) })
  }, [projectId, knowledgeBusy, loadKnowledge])

  const removeKnowledge = useCallback(async (name: string) => {
    if (!projectId || knowledgeBusy) return
    const accepted = await requestConfirmation({
      title: 'Remove project knowledge?',
      message: `“${name}” will no longer be available to GLM or Kimi in this project. The original file on disk is not deleted.`,
      confirmLabel: 'Remove source',
      cancelLabel: 'Keep source',
      danger: true,
    })
    if (!accepted) return
    const pid = projectId
    setKnowledgeBusy(true)
    setKnowledgeError(null)
    RemoveProjectKnowledge(projectId, name)
      .then(() => { if (projectIdRef.current === pid) setKnowledge((docs) => docs.filter((d) => d.name !== name)) })
      .catch((e: any) => { if (projectIdRef.current === pid) setKnowledgeError(String(e?.message || e)) })
      .finally(() => { if (projectIdRef.current === pid) setKnowledgeBusy(false) })
  }, [projectId, knowledgeBusy, requestConfirmation])

  const addKnowledgeURL = useCallback(() => {
    const value = knowledgeURLDraft.trim()
    if (!projectId || knowledgeBusy || !value) return
    const pid = projectId
    setKnowledgeBusy(true)
    setKnowledgeError(null)
    AddProjectKnowledgeURL(projectId, value)
      .then((raw: any) => {
        if (projectIdRef.current !== pid) return
        setKnowledge((raw || []) as KnowledgeDocument[])
        setKnowledgeURLDraft('')
        setKnowledgeURLOpen(false)
      })
      .catch((e: any) => {
        if (projectIdRef.current === pid) setKnowledgeError(String(e?.message || e))
      })
      .finally(() => { if (projectIdRef.current === pid) setKnowledgeBusy(false) })
  }, [projectId, knowledgeBusy, knowledgeURLDraft])

  const refreshKnowledgeURL = useCallback((id: string) => {
    if (!projectId || knowledgeBusy) return
    const pid = projectId
    setKnowledgeBusy(true)
    setKnowledgeError(null)
    RefreshProjectKnowledgeURL(projectId, id.replace(/^url:/, ''))
      .then((raw: any) => {
        if (projectIdRef.current === pid) setKnowledge((raw || []) as KnowledgeDocument[])
      })
      .catch((e: any) => {
        if (projectIdRef.current === pid) setKnowledgeError(String(e?.message || e))
      })
      .finally(() => { if (projectIdRef.current === pid) setKnowledgeBusy(false) })
  }, [projectId, knowledgeBusy])

  const removeKnowledgeURL = useCallback(async (id: string) => {
    if (!projectId || knowledgeBusy) return
    const document = knowledge.find((item) => item.id === id)
    const accepted = await requestConfirmation({
      title: 'Remove URL snapshot?',
      message: `“${document?.name || document?.url || 'This URL snapshot'}” will no longer be available to GLM or Kimi. You can fetch it again later from the original URL.`,
      confirmLabel: 'Remove snapshot',
      cancelLabel: 'Keep snapshot',
      danger: true,
    })
    if (!accepted) return
    const pid = projectId
    setKnowledgeBusy(true)
    setKnowledgeError(null)
    RemoveProjectKnowledgeURL(projectId, id.replace(/^url:/, ''))
      .then(() => {
        if (projectIdRef.current === pid) setKnowledge((docs) => docs.filter((doc) => doc.id !== id))
      })
      .catch((e: any) => {
        if (projectIdRef.current === pid) setKnowledgeError(String(e?.message || e))
      })
      .finally(() => { if (projectIdRef.current === pid) setKnowledgeBusy(false) })
  }, [projectId, knowledgeBusy, knowledge, requestConfirmation])

  // Ctrl+J / Cmd+J toggles the context panel (symmetric with Ctrl+B for the
  // left sidebar). IME-guarded so a CJK/Cyrillic compose-commit doesn't fire it.
  useEffect(() => {
    if (workspaceMode) return
    const onKey = (e: KeyboardEvent) => {
      if (e.isComposing || e.keyCode === 229) return
      const isToggle = (e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 'j'
      if (!isToggle) return
      e.preventDefault()
      if (hasOpenModal()) return
      toggle()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [toggle, workspaceMode])

  // The top-bar right panel-toggle button drives the same collapse via a
  // decoupled custom event (the bar doesn't own this panel's state).
  useEffect(() => {
    if (workspaceMode) return
    const onToggle = () => toggle()
    window.addEventListener('gokin:toggle-context', onToggle)
    return () => window.removeEventListener('gokin:toggle-context', onToggle)
  }, [toggle, workspaceMode])

  // Changed-file summaries in the chat can open the same review surface even
  // when the context rail is collapsed.
  useEffect(() => {
    if (workspaceMode) return
    const onOpenReview = () => {
      setCollapsed(false)
      try { localStorage.setItem(COLLAPSE_KEY, '0') } catch { /* storage unavailable */ }
      openGitReview()
    }
    window.addEventListener('gokin:open-git-review', onOpenReview)
    return () => window.removeEventListener('gokin:open-git-review', onOpenReview)
  }, [openGitReview, workspaceMode])

  // Something worth surfacing even when the panel is collapsed.
  const collapsedHasState = !!(
    git?.changedFiles?.length ||
    git?.untrackedFiles?.length ||
    git?.insertions ||
    git?.deletions ||
    plan?.active
  )

  if (collapsed && !workspaceMode) {
    return (
      <div className="context-panel is-collapsed">
        <button className="context-rail-btn" onClick={toggle} title="Show context panel (Ctrl+J)" aria-label="Show context panel">
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
    <>
    <div
      className={`context-panel ${workspaceMode ? 'workspace-mode' : ''} ${resizing ? 'is-resizing' : ''}`}
      style={{ '--context-panel-width': `${panelWidth}px` } as CSSProperties}
    >
      {!workspaceMode && <div
        className="context-panel-resizer"
        role="separator"
        aria-label="Resize context panel"
        aria-orientation="vertical"
        aria-valuemin={CONTEXT_WIDTH_MIN}
        aria-valuemax={CONTEXT_WIDTH_MAX}
        aria-valuenow={panelWidth}
        tabIndex={0}
        title="Drag to resize · double-click to reset"
        onPointerDown={(event) => {
          if (event.button !== 0) return
          event.preventDefault()
          resizeActiveRef.current = true
          resizeRightRef.current = event.currentTarget.parentElement?.getBoundingClientRect().right || window.innerWidth
          setResizing(true)
          event.currentTarget.setPointerCapture(event.pointerId)
        }}
        onPointerMove={(event) => {
          if (!resizeActiveRef.current) return
          updatePanelWidth(resizeRightRef.current - event.clientX)
        }}
        onPointerUp={(event) => {
          if (!resizeActiveRef.current) return
          resizeActiveRef.current = false
          setResizing(false)
          updatePanelWidth(panelWidthRef.current, true)
          if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId)
        }}
        onPointerCancel={() => {
          resizeActiveRef.current = false
          setResizing(false)
          updatePanelWidth(panelWidthRef.current, true)
        }}
        onDoubleClick={() => updatePanelWidth(CONTEXT_WIDTH_DEFAULT, true)}
        onKeyDown={(event) => {
          if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
          event.preventDefault()
          const step = event.shiftKey ? 24 : 8
          const next = event.key === 'Home' ? CONTEXT_WIDTH_MIN
            : event.key === 'End' ? CONTEXT_WIDTH_MAX
              : panelWidthRef.current + (event.key === 'ArrowLeft' ? step : -step)
          updatePanelWidth(next, true)
        }}
      />}
      <div className="context-head">
        <span className="context-head-title">Context</span>
        <div className="context-head-actions">
          <button onClick={manualRefresh} title="Refresh" aria-label="Refresh project context" disabled={loading}>
            <RefreshCw size={13} className={loading ? 'spin' : ''} />
          </button>
          {!workspaceMode && <button onClick={toggle} title="Hide panel (Ctrl+J)" aria-label="Hide context panel"><PanelRightClose size={14} /></button>}
        </div>
      </div>

      <div className="context-body">
      {/* Project knowledge shared by all sessions and retrieved per model turn. */}
      <section className="context-section">
        <div className="context-section-head">
          <BookOpen size={13} /><span>Project knowledge</span>
          <div className="knowledge-head-actions">
            <button
              className="knowledge-add"
              onClick={() => { setKnowledgeURLOpen((open) => !open); setKnowledgeError(null) }}
              disabled={knowledgeBusy}
              title="Add a public web URL"
              aria-label="Add project knowledge URL"
            >
              <Link2 size={12} />
            </button>
            <button
              className="knowledge-add"
              onClick={addKnowledge}
              disabled={knowledgeBusy}
              title="Add text, code, PDF, or DOCX files"
              aria-label="Add project knowledge files"
            >
              {knowledgeBusy ? <Loader2 size={12} className="spin" /> : <Plus size={13} />}
            </button>
          </div>
        </div>
        {knowledgeURLOpen && (
          <div className="knowledge-url-composer">
            <input
              type="url"
              value={knowledgeURLDraft}
              onChange={(e) => setKnowledgeURLDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.nativeEvent.isComposing || e.keyCode === 229) return
                if (e.key === 'Enter') { e.preventDefault(); addKnowledgeURL() }
                if (e.key === 'Escape') { e.preventDefault(); setKnowledgeURLOpen(false); setKnowledgeURLDraft('') }
              }}
              placeholder="https://docs.example.com/guide"
              maxLength={8192}
              disabled={knowledgeBusy}
              autoFocus
            />
            <button onClick={addKnowledgeURL} disabled={knowledgeBusy || !knowledgeURLDraft.trim()} title="Fetch and add URL">
              {knowledgeBusy ? <Loader2 size={11} className="spin" /> : <Plus size={12} />}
            </button>
          </div>
        )}
        {knowledge.length > 0 ? (
          <>
            <ul className="knowledge-list">
              {knowledge.map((doc) => (
                <li key={doc.id || doc.name}>
                  {doc.sourceType === 'url' ? <Link2 size={12} /> : <FileText size={12} />}
                  <span title={doc.url || doc.name}>{doc.name}</span>
                  <small>{doc.size < 1024 ? `${doc.size} B` : `${Math.ceil(doc.size / 1024)} KB`}</small>
                  {doc.sourceType === 'url' && (
                    <button
                      className="knowledge-refresh"
                      onClick={() => refreshKnowledgeURL(doc.id)}
                      disabled={knowledgeBusy}
                      title={`Refresh ${doc.url || doc.name}`}
                      aria-label={`Refresh ${doc.name}`}
                    >
                      <RefreshCw size={11} className={knowledgeBusy ? 'spin' : ''} />
                    </button>
                  )}
                  <button
                    onClick={() => doc.sourceType === 'url' ? removeKnowledgeURL(doc.id) : removeKnowledge(doc.name)}
                    disabled={knowledgeBusy}
                    title={`Remove ${doc.name}`}
                    aria-label={`Remove ${doc.name} from project knowledge`}
                  >
                    <Trash2 size={11} />
                  </button>
                </li>
              ))}
            </ul>
            <div className="knowledge-hint">Relevant excerpts from local files and explicit URL snapshots are added automatically. URLs refresh only when you ask. Up to 100 sources, 2 MB each and 50 MB total.</div>
          </>
        ) : (
          <div className="context-empty">Add text/code, PDF, DOCX, or a public URL snapshot to share context across every chat.</div>
        )}
        {knowledgeError && <div className="knowledge-error">{knowledgeError}</div>}
      </section>

      {/* Progressive-disclosure Skills discovered from project-local folders. */}
      <section className="context-section">
        <div className="context-section-head">
          <Sparkles size={13} /><span>Project skills</span>
          {skills.length > 0 && <small className="skills-count">{skills.length}</small>}
        </div>
        {skills.length > 0 ? (
          <>
            <ul className="skills-list">
              {skills.map((skill) => (
                <li key={`${skill.source}:${skill.path}`} title={`${skill.path}\n${skill.description}`}>
                  <div>
                    <span>{skill.name}</span>
                    <small>{skill.source}</small>
                  </div>
                  <p>{skill.description}</p>
                </li>
              ))}
            </ul>
            <div className="knowledge-hint">Metadata is matched automatically. Full SKILL.md instructions load only when relevant.</div>
          </>
        ) : (
          <div className="context-empty">Add trusted Skills under .gokin/skills or .claude/skills, then refresh. Skills use the current project's GLM or Kimi model.</div>
        )}
        {skillIssues.length > 0 && (
          <div className="skills-issues">
            {skillIssues.slice(0, 4).map((issue, i) => (
              <div key={`${issue.path}-${i}`} title={issue.error}><strong>{issue.path}</strong>: {issue.error}</div>
            ))}
            {skillIssues.length > 4 && <div>+{skillIssues.length - 4} more invalid Skills</div>}
          </div>
        )}
      </section>

      {/* Explicit project-scoped ordinary tool permissions. */}
      <section className="context-section">
        <div className="context-section-head">
          <ShieldCheck size={13} /><span>Always allowed tools</span>
          {toolPermissions.length > 0 && <small className="skills-count">{toolPermissions.length}</small>}
        </div>
        {toolPermissions.length > 0 ? (
          <ul className="computer-permission-list tool-permission-list">
            {toolPermissions.map((permission) => (
              <li key={permission.tool}>
                <ShieldCheck size={12} className="computer-permission-allow" />
                <div className="tool-permission-copy" title={`${permission.description} · ${permission.scope}`}>
                  <code>{permission.tool.replace(/_/g, ' ')}</code>
                  <small>{permission.description} · {permission.scope}</small>
                </div>
                <button onClick={() => removeToolPermission(permission.tool)} title="Revoke always allow" aria-label={`Revoke always allow for ${permission.tool}`}>
                  <Trash2 size={11} />
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <div className="context-empty">No remembered tool permissions. Manual approvals apply only to the current turn.</div>
        )}
        <div className="knowledge-hint">Rules apply only to ordinary local actions in this project. Deletes, shell, network, connectors, SSH, browser, and computer actions still require exact review.</div>
        {toolPermissionError && <div className="knowledge-error">{toolPermissionError}</div>}
      </section>

      {/* OS-observed app permissions for computer use. */}
      <section className="context-section">
        <div className="context-section-head">
          <Monitor size={13} /><span>Computer access</span>
        </div>
        {(computerPermissions.allowed?.length || computerPermissions.blocked?.length) ? (
          <ul className="computer-permission-list">
            {(computerPermissions.allowed || []).map((id) => (
              <li key={`allow:${id}`}>
                <ShieldCheck size={12} className="computer-permission-allow" />
                <span title={id}>{id}</span>
                <button onClick={() => removeComputerPermission(id)} title="Forget permission" aria-label={`Forget ${id}`}>
                  <Trash2 size={11} />
                </button>
              </li>
            ))}
            {(computerPermissions.blocked || []).map((id) => (
              <li key={`block:${id}`}>
                <Ban size={12} className="computer-permission-block" />
                <span title={id}>{id}</span>
                <button onClick={() => removeComputerPermission(id)} title="Remove from blocklist" aria-label={`Unblock ${id}`}>
                  <Trash2 size={11} />
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <div className="context-empty">No remembered app permissions. New applications require approval.</div>
        )}
        <div className="knowledge-hint">Green apps may still require per-turn action review. Blocked apps are denied before execution.</div>
        {computerPermissionError && <div className="knowledge-error">{computerPermissionError}</div>}
      </section>

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
            ) : untrackedCount > 0 ? (
              <div className="context-git-pending"><FileDiff size={12} /> Untracked changes</div>
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
            {hasChanges && (
              <button className="context-review-btn" onClick={openGitReview}>
                <FileDiff size={13} /> Review changes
              </button>
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
    {gitReviewOpen && (
      <GitReviewModal
        projectId={projectId}
        sessionId={sessionId}
        onClose={() => setGitReviewOpen(false)}
        onReviewed={refreshSessionContext}
      />
    )}
    {confirmationDialog}
    </>
  )
}
